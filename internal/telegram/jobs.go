package telegram

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/models"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	tele "gopkg.in/telebot.v4"
)

// We use the public AppID and AppHash from Telegram Desktop
const (
	PublicAppID   = 2040
	PublicAppHash = "b18441a1ff607e10a989891a5624e0d4"
)

type TelegramUploadPayload struct {
	FilePath string `json:"file_path"`
	ChatID   int64  `json:"chat_id"`
	// InfoHash is the torrent info hash when this upload was chained from the
	// torrent → S3 → Telegram pipeline. It lets MaterializeForUploadWithTorrent
	// recover the S3 key by torrent_hash when the local copy was removed and the
	// registry record lives under a different path (checksum dedup / re-download).
	// Empty for non-torrent (manual / leecher) uploads.
	InfoHash string `json:"info_hash,omitempty"`
}

// progressEditInterval is the minimum time between Telegram progress-message
// edits across ALL concurrent jobs. Every running upload or download edits
// its own progress message in the same chat, so the per-job 1.5s throttle
// multiplies with the job count: ~15 concurrent jobs produced ~10 edits per
// second, which Telegram answered with an escalating messaging FLOOD_WAIT
// (27 minutes in one incident) that froze every progress display and blocked
// the final media post of every completed upload. One process-wide edit slot
// keeps the rate safe no matter how many jobs run at once. Package variable
// so tests can shorten it.
var progressEditInterval = 5 * time.Second

// progressEdits is the shared progress-edit slot: one process-wide budget for
// the cosmetic progress messages of every running transfer.
var progressEdits struct {
	mu   sync.Mutex
	last time.Time
}

// claimProgressEdit reports whether a Telegram progress edit may go out now,
// advancing the shared slot when it may. finished (the 100% update) bypasses
// the interval so the completion state is always shown promptly.
func claimProgressEdit(finished bool) bool {
	progressEdits.mu.Lock()
	defer progressEdits.mu.Unlock()
	now := time.Now()
	if !finished && now.Sub(progressEdits.last) < progressEditInterval {
		return false
	}
	progressEdits.last = now
	return true
}

// progressEditAllowed is the single gate every progress-edit call site must
// pass: edits are skipped entirely (never queued) while a messaging flood
// cooldown is active, and otherwise share the process-wide edit slot. The
// 100% update bypasses the slot interval but never the flood check — during
// a ban even it must wait, because sending into an active ban escalates the
// ban that is also blocking the media post.
func progressEditAllowed(finished bool) bool {
	if floodGateRemaining(floodEditMethods...) > 0 {
		return false
	}
	return claimProgressEdit(finished)
}

// uploadProgress tracks the upload progress and throttles Telegram updates.
// Chunk is invoked concurrently by every upload thread, so the throttling
// state is guarded by mu.
type uploadProgress struct {
	job         *models.SchedulerJob
	eng         *Engine
	progressMsg *tele.Message
	fileName    string
	startTime   time.Time

	// mu guards lastUpdate: with parallel uploads, all N threads call Chunk
	// concurrently and must agree on when the last update was sent.
	mu         sync.Mutex
	lastUpdate time.Time

	logFn func(level, message string)

	// gotd message update support
	gotdClient *telegram.Client
	gotdPeer   tg.InputPeerClass
	gotdMsgID  int
}

// Chunk satisfies the uploader.Progress interface. Every concurrent upload
// thread calls it, so the shared throttle timestamp is updated atomically;
// the database write and message edit run outside the lock so a slow update
// never stalls the other threads.
func (p *uploadProgress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	percent := int(100 * float64(state.Uploaded) / float64(state.Total))
	if percent > 100 {
		percent = 100
	}
	finished := percent == 100

	// Throttle both update channels to once per 1.5 seconds (or immediately
	// at 100%). The read-and-set of lastUpdate must be atomic across
	// threads, otherwise each thread sees a stale timestamp and N duplicate
	// edits race for the same message.
	p.mu.Lock()
	due := finished || time.Since(p.lastUpdate) > 1500*time.Millisecond
	if due {
		p.lastUpdate = time.Now()
	}
	p.mu.Unlock()

	// Update job status in database (throttled to once per 1.5s, or when finished at 100%)
	if due {
		db.DB.Model(p.job).Updates(map[string]interface{}{
			"progress": percent,
			"message":  fmt.Sprintf("Uploading: %s / %s (%d%%)", formatFileSize(state.Uploaded), formatFileSize(state.Total), percent),
		})
	}

	// Throttle Telegram status message updates (max once per 1.5 seconds) to avoid rate limits
	if !due {
		return nil
	}

	// Telegram messaging needs far gentler pacing than the database: all
	// progress edits share one process-wide slot and are skipped entirely
	// (never queued) while a messaging flood cooldown is active — see
	// progressEditAllowed. A stale percentage for a few minutes is much
	// better than prolonging a ban that also blocks the media post.
	if !progressEditAllowed(finished) {
		return nil
	}

	elapsed := time.Since(p.startTime).Seconds()
	speed := 0.0
	if elapsed > 0 {
		speed = float64(state.Uploaded) / elapsed / (1024 * 1024) // MB/s
	}

	progressText := formatUploadProgressHTML(p.fileName, state.Uploaded, state.Total, percent, speed, elapsed)

	// The edit runs in the background: the calling thread belongs to the
	// uploader's transfer loop and must never wait on a messaging RPC (a
	// blocked edit behind a cooldown would stall the transfer). At most one
	// edit goes out per shared slot interval, and a failure here is purely
	// cosmetic, so errors are ignored as before.
	go func() {
		if p.progressMsg != nil && p.eng.Bot != nil {
			_, _ = p.eng.Bot.Edit(p.progressMsg, progressText, &tele.SendOptions{
				ParseMode:   tele.ModeHTML,
				ReplyMarkup: restartJobMarkupBot(p.job.ID),
			})
		} else if p.gotdClient != nil && p.gotdPeer != nil && p.gotdMsgID != 0 {
			_ = editGotdMessageHTML(ctx, floodSafeClient("upload-progress-edit", p.gotdClient), p.gotdPeer, p.gotdMsgID, progressText, restartJobMarkupGotd(p.job.ID))
		}
	}()
	return nil
}

// RunTelegramUploadJob executes a standard file upload to Telegram using
// parallel multi-connection transfers.
func RunTelegramUploadJob(ctx context.Context, job *models.SchedulerJob, logFn func(level, message string)) error {
	logFn("INFO", "Telegram upload job started")

	var payload TelegramUploadPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	// Resolve absolute path from file manager sandbox
	safePath, err := securePath(payload.FilePath)
	if err != nil {
		return fmt.Errorf("invalid file path: %w", err)
	}

	// Preserve the original display name before we possibly swap in a
	// materialized (S3-sourced) temp copy below.
	origFileName := filepath.Base(safePath)

	// Materialize a local-readable copy of the file. When the leecher archived
	// the file to S3 and removed the local copy (stateless), this streams it
	// back from object storage into a temp file. cleanup removes that temp file.
	// The torrent info hash (when present) enables precise S3-key recovery by
	// torrent_hash for torrent-originated uploads whose local copy was removed.
	readPath, cleanup, err := filecore.MaterializeForUploadWithTorrent(safePath, payload.InfoHash)
	if err != nil {
		// Torrent-originated uploads carry the originating info_hash. When the
		// file is temporarily unavailable (not on the ephemeral disk and not yet
		// in S3) because the torrent is still re-downloading after a container
		// restart wiped the disk, do NOT burn the retry budget or fail
		// permanently. The inline S3 archiver (updateStats per-file hook)
		// re-archives the file the instant it re-completes and chains a fresh
		// telegram_upload job. Ending this job as a terminal failure (instead of
		// retrying or completing) lets that fresh job through the
		// createTelegramUploadJob idempotency guard, which otherwise skips when a
		// non-terminal telegram_upload job already exists for the same path.
		//
		// Manual uploads (no info_hash) intentionally fall through: they rely on
		// the scheduler retry instead, which preserves their target chat_id and
		// succeeds the moment the file reappears on disk or in S3.
		if payload.InfoHash != "" && torrentJobIsDownloading(payload.InfoHash) {
			logFn("INFO", fmt.Sprintf(
				"File not available for upload yet — torrent is re-downloading after a container restart "+
					"(ephemeral disk was wiped). Deferring to the inline S3 archiver, which will re-archive the file "+
					"and queue a fresh upload once it re-completes; this job will not retry (path=%s).",
				safePath))
			job.RetryCount = 9999 // sentinel: terminal failure so the inline archiver can re-chain
			return fmt.Errorf("file not available for upload yet — torrent re-downloading (deferred to inline archiver): %w", err)
		}
		return fmt.Errorf("file not available for upload: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	safePath = readPath

	info, err := os.Stat(safePath)
	if err != nil {
		return fmt.Errorf("file not found on disk: %w", err)
	}

	if info.IsDir() {
		return fmt.Errorf("target path is a directory: %s", payload.FilePath)
	}

	eng := GetEngine()
	if eng == nil || !eng.running.Load() {
		return fmt.Errorf("telegram bot engine is not running — start it from the Telegram settings page (or check the Telegram engine logs)")
	}

	eng.mu.RLock()
	cfg := eng.Config
	eng.mu.RUnlock()

	if cfg.AuthType == "bot" && cfg.BotToken == "" {
		return fmt.Errorf("telegram bot token is empty or unconfigured")
	}

	// Determine chat ID to upload to
	chatID := payload.ChatID
	if chatID == 0 {
		// Use the first admin user ID as default
		adminIDs := strings.Split(cfg.AdminUserIDs, ",")
		if len(adminIDs) > 0 && adminIDs[0] != "" {
			var parsed int64
			if _, err := fmt.Sscanf(strings.TrimSpace(adminIDs[0]), "%d", &parsed); err == nil {
				chatID = parsed
			}
		}
	}

	if chatID == 0 {
		return fmt.Errorf("no target chat ID or admin ID found to send the file to")
	}

	fileName := origFileName
	logFn("INFO", fmt.Sprintf("Preparing upload of %s (size %s) to chat %d", fileName, formatFileSize(info.Size()), chatID))

	// Send initial progress text message via the active telebot instance
	var progressMsg *tele.Message
	if eng.Bot != nil {
		initialText := formatUploadInitialHTML(fileName, info.Size())

		msg, err := eng.Bot.Send(tele.ChatID(chatID), initialText, &tele.SendOptions{
			ParseMode:   tele.ModeHTML,
			ReplyMarkup: restartJobMarkupBot(job.ID),
		})
		if err != nil {
			logFn("WARN", fmt.Sprintf("Failed to send initial progress message to Telegram: %v", err))
		} else {
			progressMsg = msg
		}
	}

	// Reuse the engine's already-running MTProto client instead of creating a new one.
	// This eliminates cold auth handshakes and halves connection overhead.
	if eng.currentClient() == nil {
		return fmt.Errorf("MTProto client is not initialized — cannot upload via MTProto")
	}

	var mediaSentErr error
	var pPeer tg.InputPeerClass
	var pMsgID int

	// The engine's gotdClient is already running inside client.Run().
	// We can use the engine's gotdCtx to execute API calls directly.
	// The client is flood-safe: the initial send, error edits and cleanup
	// deletes wait out FLOOD_WAIT via the shared gate instead of failing
	// the job.
	api := floodSafeClient("upload-messaging", eng.currentClient())

	// Peer resolution is a read-only RPC — retry transient transport failures
	// so a dead connection cannot kill the job before the upload even starts.
	resolveAPI := tg.NewClient(newRetryingInvoker("upload-peer-resolve",
		func() (tg.Invoker, error) { return liveClient(nil), nil }, nil))
	peer, err := resolveInputPeer(eng.gotdCtx, resolveAPI, chatID)
	if err != nil {
		return fmt.Errorf("failed to resolve peer for chat ID: %w", err)
	}
	pPeer = peer

	if eng.Bot == nil {
		// User mode: send initial progress message via MTProto client
		initialText := formatUploadInitialHTML(fileName, info.Size())

		sender := message.NewSender(api)
		kbMarkup := restartJobMarkupGotd(job.ID)
		msg, err := sender.To(peer).Markup(kbMarkup).StyledText(eng.gotdCtx, html.String(nil, initialText))
		if err == nil {
			if upd, ok := msg.(*tg.UpdateShortSentMessage); ok {
				pMsgID = upd.ID
			} else if updates, ok := msg.(*tg.Updates); ok {
				for _, u := range updates.Updates {
					if newMessage, ok := u.(*tg.UpdateNewMessage); ok {
						pMsgID = newMessage.Message.GetID()
						break
					}
				}
			}
		} else {
			logFn("WARN", fmt.Sprintf("Failed to send initial progress message to Telegram (MTProto): %v", err))
		}
	}

	// Ensure the media Telegram receives is instantly playable: MP4s get a
	// faststart remux when the moov atom is at the end, and MKV/AVI/etc are
	// remuxed to MP4 when the codecs allow it (lossless stream copy).
	if prepPath, prepName, prepCleanup := preparePlayableMedia(safePath, fileName, logFn); prepPath != safePath {
		safePath = prepPath
		fileName = prepName
		defer prepCleanup()
		if st, statErr := os.Stat(safePath); statErr == nil {
			info = st
		}
		logFn("INFO", fmt.Sprintf("Uploading playable remux: %s (size %s)", fileName, formatFileSize(info.Size())))
	}

	// Initialize uploader progress tracker
	progressTracker := &uploadProgress{
		job:         job,
		eng:         eng,
		progressMsg: progressMsg,
		fileName:    fileName,
		startTime:   time.Now(),
		lastUpdate:  time.Now(),
		logFn:       logFn,
		gotdClient:  eng.currentClient(),
		gotdPeer:    peer,
		gotdMsgID:   pMsgID,
	}

	logFn("INFO", "Uploading file over parallel MTProto connections...")

	// The upload phase is flood-tolerant: part-level FLOOD_WAITs are waited
	// out transparently by the retrying invoker (and coordinated across all
	// jobs through the shared gate); only a persistent flood longer than the
	// automatic cap reaches here, where the job waits it out and restarts
	// the upload instead of failing and burning a scheduler retry.
	var inputFile tg.InputFileClass
	uploadErr := runFloodTolerant(eng.gotdCtx, logFn, "file upload", floodUploadMethods, func() error {
		var uerr error
		inputFile, uerr = UploadFile(eng.gotdCtx, eng.currentClient(), safePath, progressTracker)
		return uerr
	})
	if uploadErr != nil {
		return fmt.Errorf("file upload failed: %w", uploadErr)
	}

	// Generate a JWT download token for the direct download button
	appCfg := config.LoadConfig()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": "admin",
		"role":     "admin",
		"exp":      time.Now().Add(7 * 24 * time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(appCfg.JWTSecret)
	if err != nil {
		logFn("WARN", fmt.Sprintf("Failed to sign JWT download token: %v", err))
	}

	// Construct absolute download link
	var absoluteDownloadURL string
	downloadPath := fmt.Sprintf("/api/files/stream?path=%s&download=true", url.QueryEscape(payload.FilePath))
	if tokenString != "" {
		downloadPath += fmt.Sprintf("&token=%s", url.QueryEscape(tokenString))
	}

	// Determine base host from ServerURL config
	if appCfg.ServerURL != "" {
		domain := appCfg.ServerURL
		domain = strings.Replace(domain, "wss://", "https://", 1)
		domain = strings.Replace(domain, "ws://", "http://", 1)
		domain = strings.TrimSuffix(domain, "/ws")
		domain = strings.TrimSuffix(domain, "/tunnel")
		domain = strings.TrimSuffix(domain, "/")
		absoluteDownloadURL = fmt.Sprintf("%s%s", domain, downloadPath)
	} else {
		// Fall back to public domain
		absoluteDownloadURL = fmt.Sprintf("https://ondata.ir%s", downloadPath)
	}

	logFn("INFO", "Assembling media post...")
	ext := strings.ToLower(filepath.Ext(fileName))
	caption := fmt.Sprintf("🎬 <b>CleverConnect Share</b>\n"+
		"━━━━━━━━━━━━━━━━━━━━━━\n\n"+
		"📄 <b>File:</b> <code>%s</code>\n"+
		"📦 <b>Size:</b> <code>%s</code>\n"+
		"📅 <b>Uploaded:</b> <code>%s</code>\n\n"+
		"━━━━━━━━━━━━━━━━━━━━━━\n"+
		"🔷 <i>CleverConnect Engine</i>",
		escapeHTML(fileName),
		escapeHTML(formatFileSize(info.Size())),
		time.Now().Format("2006-01-02 15:04"),
	)

	var mediaOption message.MediaOption
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif":
		mediaOption = message.UploadedPhoto(inputFile, html.String(nil, caption))
	case ".mp4", ".mkv", ".webm", ".avi", ".mov":
		w, h, duration, videoCodec, audioCodec, totalBitrate, title, artist := probeMediaMetadata(safePath)

		durationStr := "Unknown"
		if duration > 0 {
			durationStr = formatDuration(duration)
		}

		resStr := "Unknown"
		if w > 0 && h > 0 {
			resStr = formatResolution(w, h)
		}

		codecStr := "Unknown"
		if videoCodec != "" || audioCodec != "" {
			codecStr = formatCodecs(videoCodec, audioCodec)
		}

		bitrateStr := "Unknown"
		if totalBitrate > 0 {
			bitrateStr = formatBitrate(totalBitrate)
		}

		videoCaption := fmt.Sprintf("🎬 <b>CleverConnect Premium Share</b>\n"+
			"━━━━━━━━━━━━━━━━━━━━━━\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n"+
			"🕒 <b>Duration:</b> <code>%s</code>\n"+
			"🖥 <b>Resolution:</b> <code>%s</code>\n"+
			"⚙️ <b>Codecs:</b> <code>%s</code>\n"+
			"⚡ <b>Bitrate:</b> <code>%s</code>\n"+
			"📅 <b>Uploaded:</b> <code>%s</code>\n\n"+
			"━━━━━━━━━━━━━━━━━━━━━━\n"+
			"🔷 <i>CleverConnect Engine</i>",
			escapeHTML(fileName),
			escapeHTML(formatFileSize(info.Size())),
			durationStr,
			resStr,
			codecStr,
			bitrateStr,
			time.Now().Format("2006-01-02 15:04"),
		)

		if title != "" {
			prefix := fmt.Sprintf("🎵 <b>Title:</b> <code>%s</code>", escapeHTML(title))
			if artist != "" {
				prefix += fmt.Sprintf(" — <code>%s</code>", escapeHTML(artist))
			}
			videoCaption = prefix + "\n" + videoCaption
		}

		doc := message.UploadedDocument(inputFile, html.String(nil, videoCaption))
		mimeStr := "video/mp4"
		if ext == ".mkv" {
			mimeStr = "video/x-matroska"
		} else if ext == ".webm" {
			mimeStr = "video/webm"
		} else if ext == ".mov" {
			mimeStr = "video/quicktime"
		} else if ext == ".avi" {
			mimeStr = "video/x-msvideo"
		}

		videoBuilder := doc.MIME(mimeStr).Filename(fileName).Video()
		if w > 0 && h > 0 {
			videoBuilder = videoBuilder.Resolution(w, h)
		}
		if duration > 0 {
			videoBuilder = videoBuilder.DurationSeconds(duration)
		}
		mediaOption = videoBuilder.SupportsStreaming()

	case ".mp3", ".m4a", ".flac", ".wav":
		_, _, duration, _, _, _, title, artist := probeMediaMetadata(safePath)

		audioCaption := fmt.Sprintf("🎵 <b>CleverConnect Audio Share</b>\n"+
			"━━━━━━━━━━━━━━━━━━━━━━\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n"+
			"🕒 <b>Duration:</b> <code>%s</code>\n"+
			"📅 <b>Uploaded:</b> <code>%s</code>\n\n"+
			"━━━━━━━━━━━━━━━━━━━━━━\n"+
			"🔷 <i>CleverConnect Engine</i>",
			escapeHTML(fileName),
			escapeHTML(formatFileSize(info.Size())),
			formatDuration(duration),
			time.Now().Format("2006-01-02 15:04"),
		)

		if title != "" {
			prefix := fmt.Sprintf("🎵 <b>Title:</b> <code>%s</code>", escapeHTML(title))
			if artist != "" {
				prefix += fmt.Sprintf(" — <code>%s</code>", escapeHTML(artist))
			}
			audioCaption = prefix + "\n" + audioCaption
		}

		doc := message.UploadedDocument(inputFile, html.String(nil, audioCaption))
		audioBuilder := doc.MIME(mimeType).Filename(fileName).Audio()
		if duration > 0 {
			audioBuilder = audioBuilder.DurationSeconds(duration)
		}
		if title != "" {
			audioBuilder = audioBuilder.Title(title)
		} else {
			audioBuilder = audioBuilder.Title(strings.TrimSuffix(fileName, filepath.Ext(fileName)))
		}
		if artist != "" {
			audioBuilder = audioBuilder.Performer(artist)
		}
		mediaOption = audioBuilder

	case ".ogg", ".opus":
		_, _, duration, _, _, _, _, _ := probeMediaMetadata(safePath)
		doc := message.UploadedDocument(inputFile, html.String(nil, caption))
		audioBuilder := doc.MIME(mimeType).Filename(fileName).Audio().Voice()
		if duration > 0 {
			audioBuilder = audioBuilder.DurationSeconds(duration)
		}
		mediaOption = audioBuilder

	default:
		doc := message.UploadedDocument(inputFile, html.String(nil, caption))
		doc.MIME(mimeType).Filename(fileName)
		mediaOption = doc
	}

	// Send media post — only attach download button if URL is a valid public HTTPS link.
	// The send phase is flood-tolerant: a FLOOD_WAIT on the send waits out
	// the shared cooldown and re-sends, instead of failing the job and
	// discarding the fully uploaded file.
	mediaSentErr = runFloodTolerant(eng.gotdCtx, logFn, "media send", floodSendMethods, func() error {
		// Re-resolve the live client in case the MTProto client was replaced
		// mid-upload by the self-healing supervisor — the fresh client must
		// send the final media message. A fresh sender per attempt keeps the
		// request builder state clean across flood retries.
		sender := message.NewSender(floodSafeClient("upload-send", eng.currentClient()))
		if strings.HasPrefix(absoluteDownloadURL, "https://") {
			kbMarkup := &tg.ReplyInlineMarkup{
				Rows: []tg.KeyboardButtonRow{
					{
						Buttons: []tg.KeyboardButtonClass{
							&tg.KeyboardButtonURL{
								Text: "📥 Download Direct Link",
								URL:  absoluteDownloadURL,
							},
						},
					},
				},
			}
			_, serr := sender.To(peer).Markup(kbMarkup).Media(eng.gotdCtx, mediaOption)
			return serr
		}
		_, serr := sender.To(peer).Media(eng.gotdCtx, mediaOption)
		return serr
	})

	if mediaSentErr != nil {
		// Attempt to update the progress message with error
		errMsg := fmt.Sprintf("failed to send media post: %v", mediaSentErr)
		errorText := formatErrorHTML(true, fileName, errMsg)
		if progressMsg != nil && eng.Bot != nil {
			_, _ = eng.Bot.Edit(progressMsg, errorText, &tele.SendOptions{ParseMode: tele.ModeHTML})
		} else if pMsgID != 0 && pPeer != nil {
			_ = editGotdMessageHTML(eng.gotdCtx, api, pPeer, pMsgID, errorText, nil)
		}
		return fmt.Errorf("%s", errMsg)
	}

	logFn("INFO", "Media post sent successfully. Cleaning up progress message...")
	if progressMsg != nil && eng.Bot != nil {
		_ = eng.Bot.Delete(progressMsg)
	} else if pMsgID != 0 {
		_, _ = api.MessagesDeleteMessages(eng.gotdCtx, &tg.MessagesDeleteMessagesRequest{
			ID:     []int{pMsgID},
			Revoke: true,
		})
	}

	return nil
}

// torrentJobIsDownloading reports whether the torrent for the given info hash
// is still actively downloading/seeding on this container. After a Clever Cloud
// container restart the ephemeral download disk is wiped, but the torrent
// manager re-adds non-paused torrents which re-download their files. This lets
// the Telegram upload defer (instead of failing permanently) when the file is
// temporarily absent, mirroring the torrent_s3_move handler's defer logic in
// internal/torrent/pipeline.go. It uses a direct DB lookup to avoid importing
// the torrent package (keeping the import graph one-way: scheduler → torrent,
// scheduler → telegram; telegram does NOT import torrent).
func torrentJobIsDownloading(infoHash string) bool {
	if infoHash == "" {
		return false
	}
	var tj models.TorrentJob
	if err := db.DB.Where("info_hash = ?", infoHash).First(&tj).Error; err != nil {
		return false
	}
	return tj.Status == "downloading" || tj.Status == "seeding"
}

// resolveInputPeer queries dialogs to resolve chat ID with AccessHash if required.
func resolveInputPeer(ctx context.Context, api *tg.Client, chatID int64) (tg.InputPeerClass, error) {
	// Query dialogs to find peer details
	res, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		Limit: 100,
	})
	if err == nil {
		switch dialogs := res.(type) {
		case *tg.MessagesDialogsSlice:
			for _, user := range dialogs.Users {
				if user.GetID() == chatID {
					userObj, ok := user.(*tg.User)
					if ok && userObj.AccessHash != 0 {
						return &tg.InputPeerUser{UserID: userObj.ID, AccessHash: userObj.AccessHash}, nil
					}
				}
			}
			for _, chat := range dialogs.Chats {
				if chat.GetID() == chatID {
					switch c := chat.(type) {
					case *tg.Chat:
						return &tg.InputPeerChat{ChatID: c.ID}, nil
					case *tg.Channel:
						if c.AccessHash != 0 {
							return &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash}, nil
						}
					}
				}
			}
		case *tg.MessagesDialogs:
			for _, user := range dialogs.Users {
				if user.GetID() == chatID {
					userObj, ok := user.(*tg.User)
					if ok && userObj.AccessHash != 0 {
						return &tg.InputPeerUser{UserID: userObj.ID, AccessHash: userObj.AccessHash}, nil
					}
				}
			}
			for _, chat := range dialogs.Chats {
				if chat.GetID() == chatID {
					switch c := chat.(type) {
					case *tg.Chat:
						return &tg.InputPeerChat{ChatID: c.ID}, nil
					case *tg.Channel:
						if c.AccessHash != 0 {
							return &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash}, nil
						}
					}
				}
			}
		}
	}

	// Fallbacks
	if chatID > 0 {
		return &tg.InputPeerUser{UserID: chatID, AccessHash: 0}, nil
	}

	// Check channel vs chat prefix
	strID := fmt.Sprintf("%d", chatID)
	if strings.HasPrefix(strID, "-100") {
		cleanID := strings.TrimPrefix(strID, "-100")
		var parsed int64
		_, _ = fmt.Sscanf(cleanID, "%d", &parsed)
		return &tg.InputPeerChannel{ChannelID: parsed, AccessHash: 0}, nil
	}

	absID := chatID
	if absID < 0 {
		absID = -absID
	}
	return &tg.InputPeerChat{ChatID: absID}, nil
}

// TelegramDownloadPayload represents the JSON payload for a Telegram download job.
type TelegramDownloadPayload struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int   `json:"message_id"`
}

// getMessage retrieves a specific message from Telegram by its ID.
func (e *Engine) getMessage(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	var messagesSlice []tg.MessageClass

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		channelInput := &tg.InputChannel{
			ChannelID:  p.ChannelID,
			AccessHash: p.AccessHash,
		}
		res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: channelInput,
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
		})
		if err != nil {
			return nil, err
		}
		switch val := res.(type) {
		case *tg.MessagesMessagesSlice:
			messagesSlice = val.Messages
		case *tg.MessagesMessages:
			messagesSlice = val.Messages
		case *tg.MessagesChannelMessages:
			messagesSlice = val.Messages
		}
	default:
		res, err := api.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}})
		if err != nil {
			return nil, err
		}
		switch val := res.(type) {
		case *tg.MessagesMessagesSlice:
			messagesSlice = val.Messages
		case *tg.MessagesMessages:
			messagesSlice = val.Messages
		}
	}

	if len(messagesSlice) == 0 {
		return nil, fmt.Errorf("message not found")
	}

	msg, ok := messagesSlice[0].(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("not a message type")
	}

	return msg, nil
}

// RunTelegramDownloadJob executes a parallel multi-connection file download from Telegram.
func RunTelegramDownloadJob(ctx context.Context, job *models.SchedulerJob, logFn func(level, message string)) error {
	logFn("INFO", "Telegram download job started")

	var payload TelegramDownloadPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	eng := GetEngine()
	if eng == nil || !eng.running.Load() {
		return fmt.Errorf("telegram bot engine is not running — start it from the Telegram settings page (or check the Telegram engine logs)")
	}

	if eng.currentClient() == nil {
		return fmt.Errorf("MTProto client is not initialized")
	}

	// Flood-safe client: message fetches and progress/status edits wait out
	// FLOOD_WAIT via the shared gate instead of failing the job.
	api := floodSafeClient("download-messaging", eng.currentClient())

	// Peer resolution is a read-only RPC — retry transient transport failures
	// so a dead connection cannot kill the job before the download even starts.
	resolveAPI := tg.NewClient(newRetryingInvoker("download-peer-resolve",
		func() (tg.Invoker, error) { return liveClient(nil), nil }, nil))

	// 1. Resolve peer
	peer, err := resolveInputPeer(eng.gotdCtx, resolveAPI, payload.ChatID)
	if err != nil {
		return fmt.Errorf("failed to resolve peer for chat ID: %w", err)
	}

	// 2. Fetch the Telegram message containing the file
	logFn("INFO", fmt.Sprintf("Fetching message ID %d from chat %d", payload.MessageID, payload.ChatID))
	msg, err := eng.getMessage(eng.gotdCtx, api, peer, payload.MessageID)
	if err != nil {
		return fmt.Errorf("failed to fetch message: %w", err)
	}

	// 3. Inspect message media
	if msg.Media == nil {
		return fmt.Errorf("message does not contain any media/file")
	}

	var fileLocation tg.InputFileLocationClass
	var fileSize int64
	var fileName string
	var hasFile bool
	var fileDCID int

	switch media := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		if doc, ok := media.Document.(*tg.Document); ok {
			fileSize = doc.Size
			hasFile = true
			// DCID is the authoritative home datacenter of the file. The fast
			// multi-connection download pool targets it directly.
			fileDCID = doc.DCID
			fileLocation = &tg.InputDocumentFileLocation{
				ID:            doc.ID,
				AccessHash:    doc.AccessHash,
				FileReference: doc.FileReference,
			}
			for _, attr := range doc.Attributes {
				if fAttr, ok := attr.(*tg.DocumentAttributeFilename); ok {
					fileName = fAttr.FileName
					break
				}
			}
			if fileName == "" {
				ext := "bin"
				if mimeType := doc.MimeType; mimeType != "" {
					if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
						ext = strings.TrimPrefix(exts[0], ".")
					}
				}
				fileName = fmt.Sprintf("document_%d.%s", doc.ID, ext)
			}
		}
	case *tg.MessageMediaPhoto:
		if photo, ok := media.Photo.(*tg.Photo); ok {
			hasFile = true
			fileDCID = photo.DCID
			fileName = fmt.Sprintf("photo_%d.jpg", photo.ID)
			fileLocation = &tg.InputPhotoFileLocation{
				ID:            photo.ID,
				AccessHash:    photo.AccessHash,
				FileReference: photo.FileReference,
				ThumbSize:     "x",
			}
			for _, size := range photo.Sizes {
				switch s := size.(type) {
				case *tg.PhotoSize:
					if s.Size > int(fileSize) {
						fileSize = int64(s.Size)
					}
				case *tg.PhotoSizeProgressive:
					if len(s.Sizes) > 0 {
						last := s.Sizes[len(s.Sizes)-1]
						if last > int(fileSize) {
							fileSize = int64(last)
						}
					}
				}
			}
			if fileSize == 0 {
				fileSize = 1024 * 1024
			}
		}
	}

	if !hasFile || fileLocation == nil {
		return fmt.Errorf("no downloadable document or photo found in message media")
	}

	// 4. Determine save path
	relPath := filepath.Join("Downloads/telegram/files", fileName)
	safePath, err := securePath(relPath)
	if err != nil {
		return fmt.Errorf("invalid save path: %w", err)
	}

	logFn("INFO", fmt.Sprintf("Downloading %s (size %s) to %s", fileName, FormatFileSize(fileSize), safePath))

	// Pre-download check
	var docID int64
	if docLoc, ok := fileLocation.(*tg.InputDocumentFileLocation); ok {
		docID = docLoc.ID
	} else if photoLoc, ok := fileLocation.(*tg.InputPhotoFileLocation); ok {
		docID = photoLoc.ID
	}

	if docID != 0 {
		if matched, _, err := filecore.CheckDuplicateByTgID(docID, safePath); err == nil && matched {
			logFn("INFO", fmt.Sprintf("Telegram file already exists (instant deduplication match for ID %d)", docID))

			// Register file to ensure it's recorded correctly
			_, _ = filecore.RegisterFile(safePath, "", "", docID, "")

			// Instant completion updates in database
			db.DB.Model(job).Updates(map[string]interface{}{
				"progress": 100,
				"status":   models.JobStatusCompleted,
				"message":  fmt.Sprintf("Deduplicated: saved as %s", fileName),
			})

			// Generate success notification directly
			appCfg := config.LoadConfig()
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
				"username": "admin",
				"role":     "admin",
				"exp":      time.Now().Add(7 * 24 * time.Hour).Unix(),
			})
			tokenString, _ := token.SignedString(appCfg.JWTSecret)

			var absoluteDownloadURL string
			downloadPath := fmt.Sprintf("/api/files/stream?path=%s&download=true", url.QueryEscape(relPath))
			if tokenString != "" {
				downloadPath += fmt.Sprintf("&token=%s", url.QueryEscape(tokenString))
			}

			if appCfg.ServerURL != "" {
				domain := appCfg.ServerURL
				domain = strings.Replace(domain, "wss://", "https://", 1)
				domain = strings.Replace(domain, "ws://", "http://", 1)
				domain = strings.TrimSuffix(domain, "/ws")
				domain = strings.TrimSuffix(domain, "/tunnel")
				domain = strings.TrimSuffix(domain, "/")
				absoluteDownloadURL = fmt.Sprintf("%s%s", domain, downloadPath)
			} else {
				absoluteDownloadURL = fmt.Sprintf("https://ondata.ir%s", downloadPath)
			}

			successText := formatSuccessHTML(false, fileName, fileSize, relPath)

			if eng.Bot != nil {
				kb := &tele.ReplyMarkup{}
				btn := kb.URL("📥 Download Direct Link", absoluteDownloadURL)
				kb.Inline(kb.Row(btn))
				_, _ = eng.Bot.Send(tele.ChatID(payload.ChatID), successText, &tele.SendOptions{ParseMode: tele.ModeHTML, ReplyMarkup: kb})
			} else {
				kbMarkup := &tg.ReplyInlineMarkup{
					Rows: []tg.KeyboardButtonRow{
						{
							Buttons: []tg.KeyboardButtonClass{
								&tg.KeyboardButtonURL{
									Text: "📥 Download Direct Link",
									URL:  absoluteDownloadURL,
								},
							},
						},
					},
				}
				sender := message.NewSender(floodSafeClient("download-notify", eng.currentClient()))
				_, _ = sender.To(peer).Markup(kbMarkup).StyledText(eng.gotdCtx, html.String(nil, successText))
			}

			return nil
		}
	}

	// 5. Send initial progress message
	var progressMsg *tele.Message
	var pMsgID int
	initialText := formatDownloadInitialHTML(fileName, fileSize)

	if eng.Bot != nil {
		msg, err := eng.Bot.Send(tele.ChatID(payload.ChatID), initialText, &tele.SendOptions{
			ParseMode:   tele.ModeHTML,
			ReplyMarkup: restartJobMarkupBot(job.ID),
		})
		if err != nil {
			logFn("WARN", fmt.Sprintf("Failed to send initial download progress message to Telegram: %v", err))
		} else {
			progressMsg = msg
		}
	} else {
		sender := message.NewSender(floodSafeClient("download-notify", eng.currentClient()))
		kbMarkup := restartJobMarkupGotd(job.ID)
		msg, err := sender.To(peer).Markup(kbMarkup).StyledText(eng.gotdCtx, html.String(nil, initialText))
		if err == nil {
			if upd, ok := msg.(*tg.UpdateShortSentMessage); ok {
				pMsgID = upd.ID
			} else if updates, ok := msg.(*tg.Updates); ok {
				for _, u := range updates.Updates {
					if newMessage, ok := u.(*tg.UpdateNewMessage); ok {
						pMsgID = newMessage.Message.GetID()
						break
					}
				}
			}
		} else {
			logFn("WARN", fmt.Sprintf("Failed to send initial progress message to Telegram (MTProto): %v", err))
		}
	}

	// 6. Download with progress callback
	// Use eng.gotdCtx directly — exactly like uploads do. The scheduler's ctx is only
	// used for job lifecycle tracking, not for the actual Telegram API call.
	// fileDCID routes the download through the multi-connection pool for the
	// file's home datacenter, which is what makes the transfer fast.
	lastUpdate := time.Now()
	startTime := time.Now()

	onProgress := func(downloaded, total int64) {
		percent := int(100 * float64(downloaded) / float64(total))
		if percent > 100 {
			percent = 100
		}

		// Update job progress in database (throttled to once per 1.5s, or when finished at 100%)
		if percent == 100 || time.Since(lastUpdate) > 1500*time.Millisecond {
			db.DB.Model(job).Updates(map[string]interface{}{
				"progress": percent,
				"message":  fmt.Sprintf("Downloading: %s / %s (%d%%)", FormatFileSize(downloaded), FormatFileSize(total), percent),
			})
		}

		// Throttle updates
		if time.Since(lastUpdate) > 1500*time.Millisecond {
			// Progress edits share the process-wide edit slot with uploads
			// and are skipped entirely (never queued) while a messaging
			// flood cooldown is active — see progressEditAllowed. The edit
			// also runs in the background so the download callback never
			// waits on a messaging RPC.
			if !progressEditAllowed(false) {
				return
			}
			lastUpdate = time.Now()
			elapsed := time.Since(startTime).Seconds()
			speed := 0.0
			if elapsed > 0 {
				speed = float64(downloaded) / elapsed / (1024 * 1024) // MB/s
			}
			progressText := formatDownloadProgressHTML(fileName, downloaded, total, percent, speed, elapsed)

			go func() {
				if progressMsg != nil && eng.Bot != nil {
					_, _ = eng.Bot.Edit(progressMsg, progressText, &tele.SendOptions{
						ParseMode:   tele.ModeHTML,
						ReplyMarkup: restartJobMarkupBot(job.ID),
					})
				} else if pMsgID != 0 {
					_ = editGotdMessageHTML(eng.gotdCtx, api, peer, pMsgID, progressText, restartJobMarkupGotd(job.ID))
				}
			}()
		}
	}

	// The download phase is flood-tolerant: part-level FLOOD_WAITs are waited
	// out transparently by the retrying invoker; only a persistent flood
	// longer than the automatic cap reaches here, where the job waits it out
	// and retries instead of failing and burning a scheduler retry.
	err = runFloodTolerant(eng.gotdCtx, logFn, "file download", floodDownloadMethods, func() error {
		return FastDownloadFile(eng.gotdCtx, eng.currentClient(), fileDCID, fileLocation, safePath, fileSize, onProgress)
	})

	if err != nil {
		errMsg := fmt.Sprintf("Failed to download file: %v", err)
		errorText := formatErrorHTML(false, fileName, errMsg)
		if progressMsg != nil && eng.Bot != nil {
			_, _ = eng.Bot.Edit(progressMsg, errorText, &tele.SendOptions{ParseMode: tele.ModeHTML})
		} else if pMsgID != 0 {
			_ = editGotdMessageHTML(eng.gotdCtx, api, peer, pMsgID, errorText, nil)
		}
		return err
	}

	// Register file after successful download
	var regDocID int64
	if docLoc, ok := fileLocation.(*tg.InputDocumentFileLocation); ok {
		regDocID = docLoc.ID
	} else if photoLoc, ok := fileLocation.(*tg.InputPhotoFileLocation); ok {
		regDocID = photoLoc.ID
	}

	if _, err := filecore.RegisterFile(safePath, "", "", regDocID, ""); err != nil {
		logFn("WARN", fmt.Sprintf("Failed to register downloaded file in registry: %v", err))
	}

	// 7. Success! Generate download URL
	appCfg := config.LoadConfig()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": "admin",
		"role":     "admin",
		"exp":      time.Now().Add(7 * 24 * time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(appCfg.JWTSecret)
	if err != nil {
		logFn("WARN", fmt.Sprintf("Failed to sign JWT download token: %v", err))
	}

	var absoluteDownloadURL string
	downloadPath := fmt.Sprintf("/api/files/stream?path=%s&download=true", url.QueryEscape(relPath))
	if tokenString != "" {
		downloadPath += fmt.Sprintf("&token=%s", url.QueryEscape(tokenString))
	}

	if appCfg.ServerURL != "" {
		domain := appCfg.ServerURL
		domain = strings.Replace(domain, "wss://", "https://", 1)
		domain = strings.Replace(domain, "ws://", "http://", 1)
		domain = strings.TrimSuffix(domain, "/ws")
		domain = strings.TrimSuffix(domain, "/tunnel")
		domain = strings.TrimSuffix(domain, "/")
		absoluteDownloadURL = fmt.Sprintf("%s%s", domain, downloadPath)
	} else {
		absoluteDownloadURL = fmt.Sprintf("https://ondata.ir%s", downloadPath)
	}

	successText := formatSuccessHTML(false, fileName, fileSize, relPath)

	logFn("INFO", "File downloaded successfully. Updating Telegram message with download link...")

	if progressMsg != nil && eng.Bot != nil {
		kb := &tele.ReplyMarkup{}
		btn := kb.URL("📥 Download Direct Link", absoluteDownloadURL)
		kb.Inline(kb.Row(btn))
		_, _ = eng.Bot.Edit(progressMsg, successText, &tele.SendOptions{ParseMode: tele.ModeHTML, ReplyMarkup: kb})
	} else if pMsgID != 0 {
		kbMarkup := &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{
				{
					Buttons: []tg.KeyboardButtonClass{
						&tg.KeyboardButtonURL{
							Text: "📥 Download Direct Link",
							URL:  absoluteDownloadURL,
						},
					},
				},
			},
		}
		_ = editGotdMessageHTML(eng.gotdCtx, api, peer, pMsgID, successText, kbMarkup)
	}

	return nil
}

// makeProgressBar creates a premium animated-style progress bar using Unicode block characters.
// The bar uses a gradient fill with a cursor indicator for a modern look.
func makeProgressBar(percent int, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	completed := percent * width / 100
	if completed > width {
		completed = width
	}
	remaining := width - completed

	// Block characters for a clean, modern progress bar
	const (
		fillChar  = "█"
		emptyChar = "░"
		leftCap   = "▐"
		rightCap  = "▌"
	)

	// Choose indicator emoji based on progress range
	var indicator string
	switch {
	case percent >= 100:
		indicator = "✅"
	case percent >= 75:
		indicator = "🟢"
	case percent >= 50:
		indicator = "🔵"
	case percent >= 25:
		indicator = "🟡"
	default:
		indicator = "🟠"
	}

	var sb strings.Builder
	sb.WriteString(indicator)
	sb.WriteString(" ")
	sb.WriteString(leftCap)
	for i := 0; i < completed; i++ {
		sb.WriteString(fillChar)
	}
	for i := 0; i < remaining; i++ {
		sb.WriteString(emptyChar)
	}
	sb.WriteString(rightCap)
	return sb.String()
}

// formatETA calculates and formats the estimated time remaining.
func formatETA(downloaded, total int64, elapsed float64) string {
	if downloaded <= 0 || elapsed <= 0 {
		return "calculating..."
	}
	speed := float64(downloaded) / elapsed
	if speed <= 0 {
		return "∞"
	}
	remaining := float64(total-downloaded) / speed
	if remaining < 0 {
		remaining = 0
	}

	mins := int(remaining) / 60
	secs := int(remaining) % 60
	if mins > 60 {
		hours := mins / 60
		mins = mins % 60
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	if mins > 0 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// msgDivider visually separates the header and footer of Telegram status messages.
const msgDivider = "━━━━━━━━━━━━━━━━━━━━━━"

// escapeHTML escapes user-controlled values (file names, paths, error text)
// before they are interpolated into Telegram HTML markup. Both the telebot
// HTML parse mode and gotd's html.String require this to keep such text literal.
func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// formatUploadProgressHTML builds the HTML upload progress message.
func formatUploadProgressHTML(fileName string, uploaded, total int64, percent int, speed float64, elapsed float64) string {
	eta := formatETA(uploaded, total, elapsed)

	return fmt.Sprintf(
		"📤 <b>UPLOADING FILE</b>\n"+
			msgDivider+"\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n\n"+
			"%s <b>%d%%</b>\n"+
			"💾 <code>%s</code> / <code>%s</code>\n\n"+
			"⚡ <b>Speed:</b> <code>%.2f MB/s</code>\n"+
			"⏱ <b>ETA:</b> <code>%s</code>\n\n"+
			msgDivider+"\n"+
			"🔷 <i>CleverConnect Engine</i>",
		escapeHTML(fileName),
		formatFileSize(total),
		makeProgressBar(percent, 15),
		percent,
		formatFileSize(uploaded),
		formatFileSize(total),
		speed,
		escapeHTML(eta),
	)
}

// formatDownloadProgressHTML builds the HTML download progress message.
func formatDownloadProgressHTML(fileName string, downloaded, total int64, percent int, speed float64, elapsed float64) string {
	eta := formatETA(downloaded, total, elapsed)

	return fmt.Sprintf(
		"📥 <b>DOWNLOADING FILE</b>\n"+
			msgDivider+"\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n\n"+
			"%s <b>%d%%</b>\n"+
			"💾 <code>%s</code> / <code>%s</code>\n\n"+
			"⚡ <b>Speed:</b> <code>%.2f MB/s</code>\n"+
			"⏱ <b>ETA:</b> <code>%s</code>\n\n"+
			msgDivider+"\n"+
			"🔷 <i>CleverConnect Engine</i>",
		escapeHTML(fileName),
		FormatFileSize(total),
		makeProgressBar(percent, 15),
		percent,
		FormatFileSize(downloaded),
		FormatFileSize(total),
		speed,
		escapeHTML(eta),
	)
}

// formatUploadInitialHTML builds the HTML initial upload message.
func formatUploadInitialHTML(fileName string, fileSize int64) string {
	return fmt.Sprintf(
		"📤 <b>UPLOADING FILE</b>\n"+
			msgDivider+"\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n\n"+
			"%s <b>0%%</b>\n\n"+
			"⏳ <i>Starting parallel transfer…</i>\n\n"+
			msgDivider+"\n"+
			"🔷 <i>CleverConnect Engine</i>",
		escapeHTML(fileName),
		formatFileSize(fileSize),
		makeProgressBar(0, 15),
	)
}

// formatDownloadInitialHTML builds the HTML initial download message.
func formatDownloadInitialHTML(fileName string, fileSize int64) string {
	return fmt.Sprintf(
		"📥 <b>DOWNLOADING FILE</b>\n"+
			msgDivider+"\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n\n"+
			"%s <b>0%%</b>\n\n"+
			"⏳ <i>Starting download…</i>\n\n"+
			msgDivider+"\n"+
			"🔷 <i>CleverConnect Engine</i>",
		escapeHTML(fileName),
		FormatFileSize(fileSize),
		makeProgressBar(0, 15),
	)
}

// formatSuccessHTML builds the HTML download/upload completion message.
func formatSuccessHTML(isUpload bool, fileName string, fileSize int64, savedPath string) string {
	action := "DOWNLOAD"
	if isUpload {
		action = "UPLOAD"
	}

	return fmt.Sprintf(
		"✅ <b>%s COMPLETE</b>\n"+
			msgDivider+"\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n"+
			"📁 <b>Path:</b> <code>%s</code>\n\n"+
			"%s <b>100%%</b>\n\n"+
			msgDivider+"\n"+
			"🔷 <i>CleverConnect Engine</i>",
		action,
		escapeHTML(fileName),
		FormatFileSize(fileSize),
		escapeHTML(savedPath),
		makeProgressBar(100, 15),
	)
}

// formatErrorHTML builds the HTML error message.
func formatErrorHTML(isUpload bool, fileName string, errMsg string) string {
	action := "DOWNLOAD"
	if isUpload {
		action = "UPLOAD"
	}

	return fmt.Sprintf(
		"❌ <b>%s FAILED</b>\n"+
			msgDivider+"\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"⚠️ <b>Reason:</b>\n<code>%s</code>\n\n"+
			msgDivider+"\n"+
			"🔷 <i>CleverConnect Engine</i>",
		action,
		escapeHTML(fileName),
		escapeHTML(errMsg),
	)
}

// restartJobMarkupBot builds the inline "Restart Job" keyboard for telebot messages.
func restartJobMarkupBot(jobID uint) *tele.ReplyMarkup {
	btnRestart := tele.InlineButton{
		Text:   "🔄 Restart Job",
		Unique: "restart_job",
		Data:   fmt.Sprintf("%d", jobID),
	}
	return &tele.ReplyMarkup{
		InlineKeyboard: [][]tele.InlineButton{
			{btnRestart},
		},
	}
}

// restartJobMarkupGotd builds the inline "Restart Job" keyboard for MTProto messages.
func restartJobMarkupGotd(jobID uint) *tg.ReplyInlineMarkup {
	return &tg.ReplyInlineMarkup{
		Rows: []tg.KeyboardButtonRow{
			{
				Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonCallback{
						Text: "🔄 Restart Job",
						Data: []byte(fmt.Sprintf("restart_job:%d", jobID)),
					},
				},
			},
		},
	}
}

// editGotdMessageHTML edits an existing MTProto message so the given HTML is
// rendered with real formatting entities. Calling tg.MessagesEditMessage
// directly with an HTML string would show the tags literally — MTProto has no
// parse mode, so the markup must be parsed into entities first, which is
// exactly what the message builder's StyledText does. A nil markup keeps the
// message's existing keyboard.
func editGotdMessageHTML(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, msgID int, htmlText string, markup *tg.ReplyInlineMarkup) error {
	b := &message.NewSender(api).To(peer).Builder
	if markup != nil {
		b = b.Markup(markup)
	}
	_, err := b.Edit(msgID).StyledText(ctx, html.String(nil, htmlText))
	return err
}

type ffprobeOutput struct {
	Streams []struct {
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Duration  string `json:"duration"`
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		BitRate   string `json:"bit_rate"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
		BitRate  string            `json:"bit_rate"`
	} `json:"format"`
}

func probeMediaMetadata(filePath string) (w, h, duration int, videoCodec, audioCodec string, totalBitrate int64, title, artist string) {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", filePath)
	out, err := cmd.Output()
	if err != nil {
		return
	}

	var data ffprobeOutput
	if err := json.Unmarshal(out, &data); err != nil {
		return
	}

	// 1. Parse Streams
	for _, stream := range data.Streams {
		if stream.CodecType == "video" {
			w = stream.Width
			h = stream.Height
			videoCodec = stream.CodecName
		} else if stream.CodecType == "audio" {
			audioCodec = stream.CodecName
		}
	}

	// 2. Parse Duration
	var durStr string
	if data.Format.Duration != "" {
		durStr = data.Format.Duration
	} else {
		for _, stream := range data.Streams {
			if stream.Duration != "" {
				durStr = stream.Duration
				break
			}
		}
	}
	if durStr != "" {
		if f, err := strconv.ParseFloat(durStr, 64); err == nil {
			duration = int(f)
		}
	}

	// 3. Parse Bitrate
	if data.Format.BitRate != "" {
		if br, err := strconv.ParseInt(data.Format.BitRate, 10, 64); err == nil {
			totalBitrate = br
		}
	}

	// 4. Parse Tags
	if data.Format.Tags != nil {
		for k, v := range data.Format.Tags {
			lk := strings.ToLower(k)
			if lk == "title" {
				title = v
			} else if lk == "artist" || lk == "performer" {
				artist = v
			}
		}
	}

	return
}

func formatDuration(sec int) string {
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// mp4IsFaststart reports whether an MP4 file has its moov atom (the index of
// all samples) located BEFORE the mdat atom (the media payload). Telegram only
// streams/plays videos whose moov atom is at the front — when it trails mdat
// the client must download the whole file first and the inline player refuses
// to stream it. The check walks the top-level ISO/IEC 14496-12 boxes until it
// finds moov or mdat.
func mp4IsFaststart(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	var header [8]byte
	for {
		if _, err := io.ReadFull(f, header[:]); err != nil {
			// EOF without seeing either box: not a valid/finalized MP4.
			return false, fmt.Errorf("moov/mdat box not found: %w", err)
		}

		size := binary.BigEndian.Uint32(header[0:4])
		typ := string(header[4:8])

		switch typ {
		case "moov":
			return true, nil
		case "mdat":
			return false, nil
		}

		boxLen := int64(size)
		if size == 1 {
			// 64-bit "largesize" variant.
			var ext [8]byte
			if _, err := io.ReadFull(f, ext[:]); err != nil {
				return false, err
			}
			boxLen = int64(binary.BigEndian.Uint64(ext[:]))
			if boxLen < 16 {
				return false, fmt.Errorf("invalid box size %d", boxLen)
			}
			boxLen -= 16 // header bytes already consumed
		} else if size == 0 {
			// Box extends to EOF — can't skip, and neither box was seen.
			return false, fmt.Errorf("box %q extends to EOF", typ)
		}

		if boxLen < 8 {
			return false, fmt.Errorf("invalid box size %d", boxLen)
		}
		if _, err := f.Seek(boxLen-8, io.SeekCurrent); err != nil {
			return false, err
		}
	}
}

// remuxToFaststartMP4 stream-copies a media file into dstPath as an MP4 with
// the moov atom at the front. Only the first video and first audio track are
// kept (subtitles/attachments are dropped — they frequently break stream-copy
// remuxes). This is a container-level operation: no re-encoding, lossless,
// and runs at disk speed (a few seconds per GB).
func remuxToFaststartMP4(srcPath, dstPath string) error {
	// Independent of the job's context so a scheduler cancel cannot corrupt
	// the output; bounded so a hung ffmpeg cannot wedge a worker forever.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-i", srcPath,
		"-map", "0:v:0", // first video track (required)
		"-map", "0:a:0?", // first audio track (optional — silent videos)
		"-c", "copy",
		"-movflags", "+faststart",
		"-loglevel", "error",
		dstPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("remux timed out: %w", err)
		}
		return fmt.Errorf("ffmpeg remux failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// preparePlayableMedia makes a video instantly playable in Telegram before it
// is uploaded:
//   - MP4 (and M4V) whose moov atom trails the media data is remuxed with
//     +faststart so the inline player can stream it.
//   - MKV / AVI / FLV / WMV / MOV are remuxed into MP4 when the codecs are
//     MP4-compatible (H.264/H.265/VP9 + AAC/MP3/AC3 all are), which upgrades
//     them from "download-only document" to a streamable Telegram video.
//
// Every conversion is a lossless stream copy. On any failure (no ffmpeg on
// the host, incompatible codecs, no disk space, ...) the ORIGINAL file is
// returned unchanged — upload correctness always beats playability.
//
// It returns the path to upload, the display name (extension becomes .mp4
// when the container changed), and a cleanup func that removes the temp remux
// (nil when no remux was created).
func preparePlayableMedia(safePath, fileName string, logFn func(level, message string)) (uploadPath, displayName string, cleanup func()) {
	log := func(level, format string, args ...any) {
		if logFn != nil {
			logFn(level, fmt.Sprintf(format, args...))
		}
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	needsRemux := false

	switch ext {
	case ".mp4", ".m4v":
		if fast, err := mp4IsFaststart(safePath); err == nil && !fast {
			needsRemux = true
			log("INFO", "MP4 is not faststart (moov atom at end) — remuxing for instant playback")
		}
	case ".mkv", ".avi", ".flv", ".wmv", ".mov":
		needsRemux = true
		log("INFO", "Remuxing %s container to MP4 so Telegram can stream it", ext)
	}

	if !needsRemux {
		return safePath, fileName, nil
	}

	// Temp file in the same directory (same filesystem, no cross-device copy).
	tmp, err := os.CreateTemp(filepath.Dir(safePath), ".cc-playable-*.mp4")
	if err != nil {
		log("WARN", "Cannot create remux temp file, uploading original: %v", err)
		return safePath, fileName, nil
	}
	tmpPath := tmp.Name()
	tmp.Close()

	if err := remuxToFaststartMP4(safePath, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		log("WARN", "Remux to playable MP4 failed, uploading original container: %v", err)
		return safePath, fileName, nil
	}

	log("INFO", "Playable remux ready: %s", tmpPath)
	return tmpPath, strings.TrimSuffix(fileName, ext) + ".mp4", func() { _ = os.Remove(tmpPath) }
}

func formatResolution(w, h int) string {
	if w == 0 || h == 0 {
		return "Unknown"
	}
	label := "SD"
	switch {
	case h >= 2160:
		label = "4K UHD"
	case h >= 1440:
		label = "2K QHD"
	case h >= 1080:
		label = "1080p FHD"
	case h >= 720:
		label = "720p HD"
	}
	return fmt.Sprintf("%dx%d (%s)", w, h, label)
}

func formatBitrate(bps int64) string {
	if bps == 0 {
		return "Unknown"
	}
	mbps := float64(bps) / 1000000.0
	if mbps >= 1.0 {
		return fmt.Sprintf("%.2f Mbps", mbps)
	}
	kbps := float64(bps) / 1000.0
	return fmt.Sprintf("%.0f Kbps", kbps)
}

func formatCodecs(vc, ac string) string {
	if vc == "" && ac == "" {
		return "Unknown"
	}
	if vc == "" {
		return strings.ToUpper(ac)
	}
	if ac == "" {
		return strings.ToUpper(vc)
	}
	return fmt.Sprintf("%s / %s", strings.ToUpper(vc), strings.ToUpper(ac))
}
