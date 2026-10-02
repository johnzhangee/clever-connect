package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/filecore"
	"clever-connect/internal/logger"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	tele "gopkg.in/telebot.v4"
)

// QueueUploadJob is a callback registered by the scheduler engine to queue Telegram upload jobs.
var QueueUploadJob func(filePath string, chatID int64) error

// QueueDownloadJob is a callback registered by the scheduler engine to queue Telegram download jobs.
var QueueDownloadJob func(chatID int64, messageID int, fileName string, fileSize int64) error

// RetryJob is a callback registered by the scheduler engine to retry/restart a job.
var RetryJob func(jobID uint) error

type progressWriterAt struct {
	writer     io.WriterAt
	total      int64
	downloaded int64
	onProgress func(downloaded, total int64)
	mu         sync.Mutex
}

func (p *progressWriterAt) WriteAt(b []byte, off int64) (int, error) {
	n, err := p.writer.WriteAt(b, off)
	if n > 0 {
		p.mu.Lock()
		p.downloaded += int64(n)
		downloaded := p.downloaded
		p.mu.Unlock()
		if p.onProgress != nil {
			p.onProgress(downloaded, p.total)
		}
	}
	return n, err
}

// FormatFileSize formats file size in bytes to human-readable string.
func FormatFileSize(bytes int64) string {
	return formatFileSize(bytes)
}

// fileManagerRoot is the base directory for the server file manager.
// This matches the FileHandler's rootDir in handlers/files.go.
var fileManagerRoot string

func init() {
	root, err := filepath.Abs("./data/manager")
	if err != nil {
		root = "./data/manager"
	}
	fileManagerRoot = root
}

// securePath ensures path stays within the file manager sandbox.
func securePath(requestedPath string) (string, error) {
	fullPath := filecore.GetAbsolutePath(requestedPath)

	if !strings.HasPrefix(fullPath, fileManagerRoot) {
		return "", os.ErrPermission
	}
	return fullPath, nil
}

// handleFileBrowse sends an inline keyboard listing directory contents.
func (e *Engine) handleFileBrowse(c tele.Context, dirPath string) error {
	safePath, err := securePath(dirPath)
	if err != nil {
		return c.Send("⛔ Access denied: invalid path.")
	}

	entries, err := os.ReadDir(safePath)
	if err != nil {
		return c.Send("❌ Failed to read directory: " + err.Error())
	}

	// Sort: directories first, then files
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	displayPath := filepath.Clean("/" + dirPath)
	if displayPath == "." {
		displayPath = "/"
	}

	text := fmt.Sprintf("📁 *File Browser*\n📂 `%s`\n\n", displayPath)
	if len(entries) == 0 {
		text += "_Empty directory_"
	} else {
		text += fmt.Sprintf("_%d items_", len(entries))
	}

	// Build inline keyboard
	rows := []tele.Row{}

	// Parent directory button (if not at root)
	if dirPath != "/" && dirPath != "" {
		parentPath := filepath.Dir(dirPath)
		if parentPath == "." {
			parentPath = "/"
		}
		rows = append(rows, tele.Row{
			{Text: "⬆️ Parent Directory", Data: "fb:" + parentPath},
		})
	}

	// Limit to 30 items to avoid Telegram message limits
	maxItems := 30
	if len(entries) < maxItems {
		maxItems = len(entries)
	}

	for _, entry := range entries[:maxItems] {
		name := entry.Name()
		entryPath := filepath.Join(dirPath, name)
		if dirPath == "/" {
			entryPath = "/" + name
		}

		if entry.IsDir() {
			rows = append(rows, tele.Row{
				{Text: "📂 " + name, Data: "fb:" + entryPath},
			})
		} else {
			info, _ := entry.Info()
			sizeStr := formatFileSize(info.Size())
			icon := getFileIcon(name)
			rows = append(rows, tele.Row{
				{Text: fmt.Sprintf("%s %s (%s)", icon, name, sizeStr), Data: "send:" + entryPath},
			})
		}
	}

	if len(entries) > 30 {
		text += fmt.Sprintf("\n\n⚠️ _Showing first 30 of %d items_", len(entries))
	}

	markup := &tele.ReplyMarkup{}
	markup.InlineKeyboard = make([][]tele.InlineButton, len(rows))
	for i, row := range rows {
		markup.InlineKeyboard[i] = make([]tele.InlineButton, len(row))
		for j, btn := range row {
			markup.InlineKeyboard[i][j] = tele.InlineButton{
				Text: btn.Text,
				Data: btn.Data,
			}
		}
	}

	// If this is a callback, edit the existing message
	if c.Callback() != nil {
		return c.Edit(text, markup, tele.ModeMarkdown)
	}
	return c.Send(text, markup, tele.ModeMarkdown)
}

// sendFileToChat reads a file and sends it via the Telegram bot,
// automatically detecting whether it should be sent as a photo, video,
// audio, or generic document.
func (e *Engine) sendFileToChat(c tele.Context, filePath string) error {
	safePath, err := securePath(filePath)
	if err != nil {
		return c.Send("⛔ Access denied.")
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return c.Send("❌ File not found: " + filepath.Base(filePath))
	}

	if info.IsDir() {
		return c.Send("📂 That's a directory, not a file. Use /files to browse.")
	}

	// Check file size (Telegram limit: 50MB for bots)
	e.mu.RLock()
	maxSizeMB := e.Config.MaxFileSize
	e.mu.RUnlock()
	if maxSizeMB <= 0 {
		maxSizeMB = 2000
	}
	maxBytes := int64(maxSizeMB) * 1024 * 1024

	if info.Size() > maxBytes {
		return c.Send(fmt.Sprintf("❌ File too large (%s). Maximum allowed: %dMB.",
			formatFileSize(info.Size()), maxSizeMB))
	}

	// Telegram standard Bot API limit is 50MB. If file is larger, upload via MTProto parallel uploader.
	if info.Size() > 50*1024*1024 {
		if QueueUploadJob != nil {
			err := QueueUploadJob(filePath, c.Chat().ID)
			if err != nil {
				return c.Send("❌ Failed to queue parallel upload: " + err.Error())
			}
			return nil // The job handles progress/completion notifications
		}
	}

	fileName := filepath.Base(safePath)

	// Make the media instantly playable in Telegram (faststart remux for MP4,
	// MKV/AVI/... → MP4 container conversion) before uploading. Falls back to
	// the original file whenever the conversion is impossible.
	if prepPath, prepName, prepCleanup := preparePlayableMedia(safePath, fileName, nil); prepPath != safePath {
		safePath = prepPath
		fileName = prepName
		defer prepCleanup()
		if st, statErr := os.Stat(safePath); statErr == nil {
			info = st
		}
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	mimeType := mime.TypeByExtension(ext)

	caption := fmt.Sprintf("📁 %s\n📏 %s", filePath, formatFileSize(info.Size()))

	fileObj := tele.FromDisk(safePath)

	var sendErr error

	switch {
	case isImageExt(ext):
		photo := &tele.Photo{File: fileObj, Caption: caption}
		sendErr = c.Send(photo, tele.ModeMarkdown)

	case isVideoExt(ext):
		// Streaming=true marks the video as streamable so the inline player
		// can start playing it immediately (Bot API supports_streaming).
		video := &tele.Video{File: fileObj, Caption: caption, Streaming: true}
		if m := mime.TypeByExtension(ext); m != "" {
			video.MIME = m
		}
		sendErr = c.Send(video, tele.ModeMarkdown)

	case isAudioExt(ext):
		audio := &tele.Audio{File: fileObj, Caption: caption}
		sendErr = c.Send(audio, tele.ModeMarkdown)

	case isVoiceExt(ext):
		voice := &tele.Voice{File: fileObj, Caption: caption}
		sendErr = c.Send(voice, tele.ModeMarkdown)

	default:
		doc := &tele.Document{File: fileObj, Caption: caption, MIME: mimeType}
		sendErr = c.Send(doc, tele.ModeMarkdown)
	}

	if sendErr != nil {
		e.errors.Add(1)
		logger.Error("Telegram", "Failed to send file", "path", filePath, "error", sendErr)
		return c.Send("❌ Failed to send file: " + sendErr.Error())
	}

	e.filesSent.Add(1)
	logger.Info("Telegram", "File sent successfully", "path", filePath, "size", info.Size())
	return nil
}

// ──────────────────────────────────────────────────────────────
// File type helpers
// ──────────────────────────────────────────────────────────────

func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp":
		return true
	}
	return false
}

func isVideoExt(ext string) bool {
	switch ext {
	case ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".webm", ".flv", ".m4v":
		return true
	}
	return false
}

func isAudioExt(ext string) bool {
	switch ext {
	case ".mp3", ".flac", ".wav", ".aac", ".m4a", ".wma":
		return true
	}
	return false
}

func isVoiceExt(ext string) bool {
	switch ext {
	case ".ogg", ".oga":
		return true
	}
	return false
}

func getFileIcon(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case isImageExt(ext):
		return "🖼"
	case isVideoExt(ext):
		return "🎬"
	case isAudioExt(ext):
		return "🎵"
	case ext == ".pdf":
		return "📄"
	case ext == ".zip" || ext == ".tar" || ext == ".gz" || ext == ".rar" || ext == ".7z":
		return "📦"
	case ext == ".go" || ext == ".py" || ext == ".js" || ext == ".ts" || ext == ".tsx":
		return "💻"
	case ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".toml":
		return "📋"
	case ext == ".txt" || ext == ".md" || ext == ".log":
		return "📝"
	default:
		return "📄"
	}
}

func formatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ──────────────────────────────────────────────────────────────
// High-Performance MTProto Transfer Functions
// Inspired by devgagantools ParallelTransferrer
// ──────────────────────────────────────────────────────────────

// calculateOptimalThreads determines the optimal number of concurrent download
// threads based on file size. Each thread issues 512KB upload.getFile requests
// over its own pooled MTProto connection, so throughput scales nearly linearly
// with the thread count. Telegram tolerates ~16 parallel connections per
// session, which is the practical ceiling:
//   - Files < 50MB    → 2 threads
//   - Files 50-200MB  → 4 threads
//   - Files 200-500MB → 8 threads
//   - Files 500MB-1GB → 12 threads
//   - Files 1-2GB     → 14 threads
//   - Files > 2GB     → 16 threads
func calculateOptimalThreads(fileSize int64) int {
	const (
		MB50  = 50 * 1024 * 1024
		MB200 = 200 * 1024 * 1024
		MB500 = 500 * 1024 * 1024
		GB1   = 1024 * 1024 * 1024
		GB2   = 2 * GB1
	)

	switch {
	case fileSize < MB50:
		return 2
	case fileSize < MB200:
		return 4
	case fileSize < MB500:
		return 8
	case fileSize < GB1:
		return 12
	case fileSize < GB2:
		return 14
	default:
		return 16
	}
}

// calculateUploadThreads is a slightly more aggressive thread count for uploads,
// since uploads are more tolerant of parallel connections than downloads.
func calculateUploadThreads(fileSize int64) int {
	const (
		MB100 = 100 * 1024 * 1024
		MB500 = 500 * 1024 * 1024
		GB1   = 1024 * 1024 * 1024
		GB2   = 2 * GB1
	)

	switch {
	case fileSize < MB100:
		return 1
	case fileSize < MB500:
		return 4
	case fileSize < GB1:
		return 8
	case fileSize < GB2:
		return 12
	default:
		threads := int(math.Ceil(float64(fileSize) / float64(MB100)))
		if threads > 16 {
			threads = 16
		}
		return threads
	}
}

// uploadPoolMu guards uploadPool below.
var uploadPoolMu sync.Mutex

// uploadPool caches the multi-connection invoker used by FastUploadFile.
// Uploads always target the session's home DC, so one shared pool is reused
// across upload jobs instead of paying the pool setup cost (N TCP + MTProto
// handshakes) on every upload. Connections are opened lazily and the pool is
// bound to the client's lifetime context, so it shuts down with the engine.
var uploadPool telegram.CloseInvoker

// getUploadPoolInvoker returns the cached home-DC upload pool, creating it on
// first use.
func getUploadPoolInvoker(ctx context.Context, client *telegram.Client) (telegram.CloseInvoker, error) {
	uploadPoolMu.Lock()
	defer uploadPoolMu.Unlock()

	if uploadPool != nil {
		return uploadPool, nil
	}
	invoker, err := client.Pool(downloadPoolMaxConns)
	if err != nil {
		return nil, err
	}
	uploadPool = invoker
	return invoker, nil
}

// resetUploadPool drops the cached upload pool after a connection died
// mid-transfer, so the next upload gets fresh connections. The old pool is
// intentionally not closed (pool.Close would cancel any concurrent upload's
// in-flight requests); it dies with the engine's context instead.
func resetUploadPool() {
	uploadPoolMu.Lock()
	uploadPool = nil
	uploadPoolMu.Unlock()
}

// FastUploadFile uploads a file using concurrent goroutines via the gotd MTProto uploader.
// It automatically calculates optimal threads based on file size.
// The progress parameter is optional — pass nil to skip progress tracking.
func FastUploadFile(ctx context.Context, client *telegram.Client, filePath string, progress uploader.Progress) (tg.InputFileClass, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("file not found: %w", err)
	}

	threads := calculateUploadThreads(info.Size())

	logger.Info("Telegram", "Starting fast parallel upload",
		"file", filepath.Base(filePath),
		"size", formatFileSize(info.Size()),
		"threads", threads,
	)

	upload := func() (tg.InputFileClass, error) {
		var invoker tg.Invoker = client
		if threads > 1 {
			poolInvoker, err := getUploadPoolInvoker(ctx, client)
			if err != nil {
				logger.Warn("Telegram", "Failed to create connection pool for upload, using single connection", "error", err)
			} else {
				invoker = poolInvoker
			}
		}

		up := uploader.NewUploader(tg.NewClient(invoker)).
			WithThreads(threads).
			WithPartSize(512 * 1024) // 512KB chunks — maximum for speed

		if progress != nil {
			up = up.WithProgress(progress)
		}

		return up.FromPath(ctx, filePath)
	}

	inputFile, err := upload()
	if err != nil && strings.Contains(err.Error(), "invoke pool") {
		// A pooled connection died mid-upload. Drop the cached pool so this
		// job (and every future one) gets fresh connections, then retry once.
		logger.Warn("Telegram", "Upload pool connection died mid-transfer, retrying with fresh connections", "error", err)
		resetUploadPool()
		inputFile, err = upload()
	}
	if err != nil {
		return nil, fmt.Errorf("parallel upload failed: %w", err)
	}

	logger.Info("Telegram", "Fast parallel upload completed",
		"file", filepath.Base(filePath),
		"threads", threads,
	)

	return inputFile, nil
}

// downloadPoolMaxConns is the number of MTProto connections opened per Telegram
// datacenter for downloads. Telegram tolerates ~16 parallel connections per
// session; this is the single biggest download speed lever (one connection tops
// out around 1-5 MB/s, 16 connections reach 30-80+ MB/s from a fast host).
const downloadPoolMaxConns = 16

// dcDownloadPools caches multi-connection invokers per datacenter (dc id →
// telegram.CloseInvoker). Pools are expensive to create (TCP + MTProto
// handshake + auth transfer), so they are created once and reused across every
// subsequent download job from the same DC. The pools are bound to the client's
// lifetime context, so they shut down automatically with the engine.
var dcDownloadPools sync.Map

// dcPoolMu serializes pool creation so concurrent jobs never race on
// client.DC's authorization transfer.
var dcPoolMu sync.Mutex

// getDCPoolInvoker returns a cached multi-connection invoker for the given DC.
// client.DC() dials the target DC, transfers the session's authorization to it
// (export/import, done once per DC), and gives every download thread its own
// real MTProto connection. This is what makes downloads fast: the request
// fan-out happens over N sockets instead of sharing one.
func getDCPoolInvoker(ctx context.Context, client *telegram.Client, dcID int) (telegram.CloseInvoker, error) {
	dcPoolMu.Lock()
	defer dcPoolMu.Unlock()

	if v, ok := dcDownloadPools.Load(dcID); ok {
		return v.(telegram.CloseInvoker), nil
	}

	invoker, err := client.DC(ctx, dcID, downloadPoolMaxConns)
	if err != nil {
		return nil, fmt.Errorf("create DC %d pool: %w", dcID, err)
	}

	dcDownloadPools.Store(dcID, invoker)
	return invoker, nil
}

// evictDCPool drops a cached DC pool whose connections have gone bad
// ("invoke pool: engine forcibly closed" mid-request) so the next download
// creates a fresh pool with new connections. The old pool is intentionally
// NOT closed: a concurrent download may still be using it, and pool.DC.Close()
// would cancel its in-flight requests. The abandoned pool is bound to the
// client's lifetime context, so the engine shuts it down automatically.
func evictDCPool(dcID int) {
	dcDownloadPools.Delete(dcID)
}

// extractMigrateDC inspects a download error for a Telegram FILE_MIGRATE /
// *_MIGRATE redirect and returns the target DC id.
func extractMigrateDC(err error) (int, bool) {
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) && strings.HasSuffix(rpcErr.Type, "_MIGRATE") {
		return rpcErr.Argument, true
	}
	return 0, false
}

// FastDownloadFile downloads a file from Telegram using the gotd MTProto
// downloader with a multi-connection pool pointed at the file's home DC.
//
// Speed strategy (the same architecture that makes uploads fast, plus DC
// targeting):
//   - dcID comes from the message media (tg.Document.DCID / tg.Photo.DCID) —
//     the authoritative home of the file. A pool of up to 16 real connections
//     is opened (once) against THAT DC via client.DC(), which transfers auth
//     and lets every parallel download thread use its own socket. Using
//     client.Pool() instead fails on foreign-DC files because the home-DC
//     pool does not handle FILE_MIGRATE errors.
//   - If the DC metadata was somehow wrong, a FILE_MIGRATE error from the
//     attempt is parsed and the pool is transparently re-targeted.
//   - Dead pool connections ("engine forcibly closed") cause a pool eviction
//     and retry with fresh connections, then a sequential stream fallback via
//     the primary client (which handles DC migration internally, so it always
//     works — just slower).
//
// Retry strategy: parallel → re-target DC / evict pool → fewer threads →
// sequential stream fallback.
func FastDownloadFile(ctx context.Context, client *telegram.Client, dcID int, fileLocation tg.InputFileLocationClass, destPath string, fileSize int64, onProgress func(downloaded, total int64)) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create download directory: %w", err)
	}

	threads := calculateOptimalThreads(fileSize)

	// Build the API client over the file's DC pool. Pools are cached per DC,
	// so the (expensive) connection + auth transfer only happens once.
	var api *tg.Client
	pooled := false
	if dcID > 0 {
		invoker, err := getDCPoolInvoker(ctx, client, dcID)
		if err != nil {
			logger.Warn("Telegram", "Failed to create DC download pool, using primary connection",
				"dc", dcID, "error", err)
		} else {
			api = tg.NewClient(invoker)
			pooled = true
		}
	}
	if api == nil {
		// Legacy path: the primary client handles DC migration transparently,
		// but all threads share a single connection (slow).
		api = tg.NewClient(client)
	}

	logger.Info("Telegram", "Starting fast multi-connection Telegram download",
		"dest", filepath.Base(destPath),
		"size", formatFileSize(fileSize),
		"threads", threads,
		"dc", dcID,
		"pooled", pooled,
	)

	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("download cancelled: %w", ctx.Err())
		default:
		}

		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			logger.Warn("Telegram", "Retrying download",
				"attempt", attempt+1, "threads", threads, "backoff", backoff, "error", lastErr)
			select {
			case <-ctx.Done():
				return fmt.Errorf("download cancelled during backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		// Re-target the pool if Telegram told us the file lives on another DC.
		if migrateDC, ok := extractMigrateDC(lastErr); ok && migrateDC > 0 && migrateDC != dcID {
			logger.Warn("Telegram", "Download redirected to another DC, re-targeting pool",
				"from_dc", dcID, "to_dc", migrateDC)
			dcID = migrateDC
			threads = calculateOptimalThreads(fileSize) // fresh start for the new DC
			if invoker, err := getDCPoolInvoker(ctx, client, dcID); err == nil {
				api = tg.NewClient(invoker)
				pooled = true
			} else {
				logger.Warn("Telegram", "Failed to create re-targeted DC pool, using primary connection",
					"dc", dcID, "error", err)
			}
		}

		lastErr = doDownloadParallel(ctx, api, fileLocation, destPath, fileSize, threads, onProgress)
		if lastErr == nil {
			logger.Info("Telegram", "Download completed",
				"dest", filepath.Base(destPath), "size", formatFileSize(fileSize), "attempt", attempt+1)
			return nil
		}

		if ctx.Err() != nil {
			return fmt.Errorf("download cancelled: %w", ctx.Err())
		}

		// A pooled connection that died mid-request surfaces as
		// "invoke pool: engine forcibly closed". Evict the poisoned pool so
		// this attempt and future jobs get fresh connections.
		if pooled && strings.Contains(lastErr.Error(), "invoke pool") {
			logger.Warn("Telegram", "Download pool connection died mid-transfer, evicting cached pool",
				"dc", dcID, "error", lastErr)
			evictDCPool(dcID)
			api = tg.NewClient(client)
			pooled = false
		}

		// Reduce threads for next attempt
		threads = threads / 2
		if threads < 1 {
			threads = 1
		}
		logger.Warn("Telegram", "Download attempt failed", "attempt", attempt+1, "error", lastErr)
	}

	// Final fallback: sequential stream (single goroutine, no parallelism at
	// all). The primary client resolves DC migration internally, so this path
	// succeeds even when every pool strategy has failed.
	logger.Warn("Telegram", "Falling back to sequential stream download", "error", lastErr)
	streamErr := doDownloadStream(ctx, tg.NewClient(client), fileLocation, destPath, fileSize, onProgress)
	if streamErr == nil {
		logger.Info("Telegram", "Stream fallback download completed", "dest", filepath.Base(destPath))
		return nil
	}

	return fmt.Errorf("download failed after all attempts (parallel: %v, stream: %w)", lastErr, streamErr)
}

// doDownloadParallel performs a parallel download using the given API client.
// The client may be backed by a multi-connection DC pool (fast) or the primary
// client (slow but always available).
func doDownloadParallel(ctx context.Context, api *tg.Client, fileLocation tg.InputFileLocationClass, destPath string, fileSize int64, threads int, onProgress func(downloaded, total int64)) error {
	dl := downloader.NewDownloader().WithPartSize(512 * 1024)

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	if fileSize > 0 {
		_ = f.Truncate(fileSize)
	}

	var writer io.WriterAt = f
	if onProgress != nil {
		writer = &progressWriterAt{writer: f, total: fileSize, onProgress: onProgress}
	}

	_, err = dl.Download(api, fileLocation).WithThreads(threads).Parallel(ctx, writer)
	if err != nil {
		return fmt.Errorf("parallel download failed (threads=%d): %w", threads, err)
	}
	return nil
}

// doDownloadStream performs a sequential stream download (most reliable, single goroutine).
func doDownloadStream(ctx context.Context, api *tg.Client, fileLocation tg.InputFileLocationClass, destPath string, fileSize int64, onProgress func(downloaded, total int64)) error {
	dl := downloader.NewDownloader().WithPartSize(512 * 1024)

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var writer io.Writer = f
	if onProgress != nil {
		writer = &progressWriter{writer: f, total: fileSize, onProgress: onProgress}
	}

	_, err = dl.Download(api, fileLocation).Stream(ctx, writer)
	if err != nil {
		return fmt.Errorf("stream download failed: %w", err)
	}
	return nil
}

// progressWriter wraps an io.Writer to track sequential write progress.
// Used by the fallback stream download path.
type progressWriter struct {
	writer     io.Writer
	total      int64
	downloaded int64
	onProgress func(downloaded, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.writer.Write(b)
	if n > 0 {
		p.downloaded += int64(n)
		if p.onProgress != nil {
			p.onProgress(p.downloaded, p.total)
		}
	}
	return n, err
}

// ProgressReader wraps an io.Reader to track bytes read and report progress.
// This is useful for streaming uploads with real-time progress bars in the Web UI.
type ProgressReader struct {
	Reader   io.Reader
	Total    int64
	Read_    int64
	OnUpdate func(bytesRead, totalBytes int64) // Called periodically
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.Read_ += int64(n)
	if pr.OnUpdate != nil {
		pr.OnUpdate(pr.Read_, pr.Total)
	}
	return n, err
}
