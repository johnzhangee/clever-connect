package torrent

// pipeline.go — Torrent → S3 → Telegram archive pipeline
//
// Primary path (ephemeral-disk safe): archiveTorrentFileInline uploads each
// completed torrent file to S3 SYNCHRONOUSLY, in a bounded goroutine, the
// instant it finishes downloading — BEFORE the container's ephemeral disk can
// be wiped by a recycle/restart. It then punches the local blocks (sparse stub
// so anacrolix does not re-request pieces) and chains the Telegram upload.
//
// Fallback path: RunTorrentS3MoveJob is the "torrent_s3_move" scheduler handler
// (registered by scheduler.Init). It runs only when the inline archiver submits
// a fallback (S3 upload failed / file missing), or for jobs left queued across
// a restart. It is recovery-first: it defers when the torrent is still
// re-downloading (instead of permanently failing like the old decoupled design).
//
// Design notes:
//  - QueueTorrentS3MoveJob is a function-pointer bridge (nil until set by
//    scheduler.Init). This avoids a circular import between torrent ↔ scheduler
//    and is the same pattern used for telegram.QueueUploadJob.
//  - RunTorrentS3MoveJob lives here (in the torrent package) and is imported by
//    scheduler/engine.go. The import direction is:
//      scheduler → torrent (safe; torrent does NOT import scheduler)
//
// Two-level idempotency:
//  1. In-memory registeredFiles map (fast path, session-scoped). The caller sets
//     this before launching the archiver goroutine.
//  2. DB COUNT query (slow path) inside submitTorrentS3MoveJob — prevents
//     duplicate fallback jobs after a restart.
//  Telegram chaining is also deduped by createTelegramUploadJob so the inline
//  path and a recovering scheduler job never double-upload to Telegram.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

// TorrentS3MovePayload is the canonical JSON payload for a torrent_s3_move job.
type TorrentS3MovePayload struct {
	InfoHash      string `json:"info_hash"`
	FilePath      string `json:"file_path"` // relative path inside SaveDirectory
	SaveDirectory string `json:"save_directory"`
	ChatID        int64  `json:"chat_id"` // 0 → use default at runtime
}

// QueueTorrentS3MoveJob is a function-pointer bridge set by scheduler.Init().
// It submits a torrent_s3_move scheduler job for a single completed torrent file.
// Callers must never invoke it while it is nil (check before calling).
var QueueTorrentS3MoveJob func(infoHash, saveDir, filePath string, chatID int64) error

// submitTorrentS3MoveJob is the internal helper called (in a goroutine) from
// updateStats() and onTorrentCompleted(). It enforces DB-level idempotency
// before delegating to the QueueTorrentS3MoveJob callback.
//
// MUST be called from a goroutine — it performs a DB query and never holds
// the manager mutex.
func submitTorrentS3MoveJob(m *TorrentManager, infoHash, saveDir, filePath string, chatID int64) {
	fileKey := infoHash + ":" + filePath

	// Fast nil-check: scheduler not yet initialised (brief window at startup).
	if QueueTorrentS3MoveJob == nil {
		// Re-enable the in-memory guard so the next stats tick retries.
		m.mu.Lock()
		delete(m.registeredFiles, fileKey)
		m.mu.Unlock()
		logger.Warn("Torrent", "Scheduler not ready yet — will retry torrent_s3_move submission",
			"info_hash", infoHash, "file", filePath)
		return
	}

	// DB-level idempotency — survive restarts.
	// Match on JSON payload fragments; escape backslashes for Windows paths.
	hashFrag := fmt.Sprintf(`"info_hash":"%s"`, infoHash)
	escapedPath := strings.ReplaceAll(filePath, `\`, `\\`)
	pathFrag := fmt.Sprintf(`"file_path":"%s"`, escapedPath)

	var jobCount int64
	db.DB.Model(&models.SchedulerJob{}).
		Where(
			"type = ? AND status NOT IN ? AND payload LIKE ? AND payload LIKE ?",
			"torrent_s3_move",
			[]string{models.JobStatusFailed, models.JobStatusCancelled},
			"%"+hashFrag+"%",
			"%"+pathFrag+"%",
		).
		Count(&jobCount)

	if jobCount > 0 {
		logger.Info("Torrent", "torrent_s3_move already queued/running — skipping duplicate",
			"info_hash", infoHash, "file", filePath)
		return
	}

	// Submit the scheduler job via the bridge callback.
	if err := QueueTorrentS3MoveJob(infoHash, saveDir, filePath, chatID); err != nil {
		// Re-enable the in-memory guard so the next tick can retry.
		m.mu.Lock()
		delete(m.registeredFiles, fileKey)
		m.mu.Unlock()
		logger.Error("Torrent", "Failed to submit torrent_s3_move job",
			"info_hash", infoHash, "file", filePath, "error", err)
	} else {
		logger.Info("Torrent", "Submitted torrent_s3_move scheduler job",
			"info_hash", infoHash, "file", filePath)
	}
}

// findArchivedRegistry performs the three-tier S3 registry lookup used to
// recover a file that is no longer on the ephemeral disk:
//  1. exact file_path  — this exact file was archived in a prior run.
//  2. torrent_hash     — any file of this torrent was archived (single-file
//     torrents, or a sibling file already pushed to S3).
//  3. basename LIKE    — the file leecher archived the same content under a
//     different local path.
//
// Returns the matching record (always with a non-empty S3Key), the human label
// of the tier that matched, and true when found. Shared by the inline archiver
// and the torrent_s3_move scheduler handler.
func findArchivedRegistry(torrentFilePath, infoHash string) (reg models.FileRegistry, tier string, found bool) {
	if db.DB.Where("file_path = ? AND s3_key != ''", torrentFilePath).First(&reg).Error == nil {
		return reg, "exact path", true
	}
	if infoHash != "" && db.DB.Where("torrent_hash = ? AND s3_key != ''", infoHash).First(&reg).Error == nil {
		return reg, "torrent_hash", true
	}
	if db.DB.Where("file_path LIKE ? AND s3_key != ''", "%/"+filepath.Base(torrentFilePath)).First(&reg).Error == nil {
		return reg, "filename match", true
	}
	return reg, "", false
}

// archiveTorrentFileInline is the synchronous inline S3 archive path for a
// single completed torrent file. It is launched (in a bounded goroutine) the
// instant a file finishes downloading, so the file is pushed to persistent
// object storage BEFORE Clever Cloud can recycle the container and wipe the
// ephemeral download disk.
//
// This replaces the previous fully-decoupled pattern (submit a torrent_s3_move
// scheduler job and rely on the worker pool to upload later), which was
// fragile on stateless infrastructure: any delay or container restart between
// completion and the worker picking up the job could wipe the local file and
// permanently lose it.
//
// Pipeline:
//  1. Stat the file on disk.
//  2. If missing → three-tier S3 registry lookup; if already archived, chain
//     the Telegram upload, otherwise submit a scheduler fallback job (the
//     torrent may still be re-downloading the file after a restart).
//  3. Synchronously upload to S3 via filecore.RegisterAndArchiveToS3
//     (keepSparse=true so anacrolix does not re-request pieces). Dedup is
//     checksum-based, so a file already in S3 (e.g. re-downloaded after a
//     restart) is never re-uploaded.
//  4. On upload failure → keep the local file (do NOT punch) and submit a
//     scheduler fallback job so the worker retries the upload from disk.
//  5. On success → punchHole to free the ephemeral disk blocks (sparse stub
//     preserves the logical size) and chain the Telegram upload.
//
// The goroutine is bounded by m.archiveSem (one archive transfer per CPU core) to
// protect container memory on large multi-file torrents. It never holds the
// manager mutex.
func (m *TorrentManager) archiveTorrentFileInline(infoHash, saveDir, filePath string, chatID int64) {
	m.archiveSem <- struct{}{}
	defer func() { <-m.archiveSem }()

	if saveDir == "" {
		saveDir = "./data/manager/downloads"
	}
	torrentFilePath := filecore.GetAbsolutePath(filepath.Join(saveDir, filePath))

	info, err := os.Stat(torrentFilePath)
	if err != nil || info.IsDir() {
		// File not on the ephemeral disk. If it was already archived to S3
		// (exact path / torrent hash / basename match) just chain Telegram;
		// otherwise submit a scheduler fallback job that retries once the
		// torrent re-downloads the file.
		if reg, tier, found := findArchivedRegistry(torrentFilePath, infoHash); found {
			logger.Info("Torrent", "Inline archive: file already in S3 — chaining Telegram",
				"info_hash", infoHash, "file", filePath, "match", tier, "s3_key", reg.S3Key)
			chainTelegramUploadDirect(torrentFilePath, chatID, infoHash)
			return
		}
		logger.Warn("Torrent", "Inline archive: file missing from disk and not in S3 — submitting scheduler fallback",
			"info_hash", infoHash, "file", filePath)
		submitTorrentS3MoveJob(m, infoHash, saveDir, filePath, chatID)
		return
	}

	// Synchronous inline upload — the critical safety net that gets the file
	// into persistent object storage before the ephemeral disk can be wiped by
	// a container recycle. Dedup is checksum-based, so a file already in S3
	// (e.g. re-downloaded after a restart) is not re-uploaded.
	logger.Info("Torrent", "Inline S3 archive starting",
		"info_hash", infoHash, "file", filepath.Base(torrentFilePath), "size", formatBytes(info.Size()))

	reg, err := filecore.RegisterAndArchiveToS3(torrentFilePath, "", "", 0, infoHash, true)
	if err != nil {
		// Upload failed. Keep the local file (do NOT punch) so a later retry
		// can read it from disk, and submit a scheduler fallback job.
		logger.Error("Torrent", "Inline S3 archive failed — submitting scheduler fallback (local file kept)",
			"info_hash", infoHash, "file", filePath, "error", err)
		submitTorrentS3MoveJob(m, infoHash, saveDir, filePath, chatID)
		return
	}

	logger.Info("Torrent", "Inline S3 archive complete",
		"info_hash", infoHash, "file", filepath.Base(torrentFilePath), "s3_key", reg.S3Key)

	// Free the ephemeral disk blocks. The sparse stub preserves the logical
	// file size so anacrolix's checkCompleteFileSizes does not re-request the
	// pieces (which would re-download the whole file).
	if err := punchHole(torrentFilePath); err != nil {
		// Non-fatal: the S3 copy is the source of truth.
		logger.Warn("Torrent", "punchHole failed after inline archive (S3 copy is safe)",
			"file", filePath, "error", err)
	}

	// Chain the downstream Telegram upload. MaterializeForUpload streams the
	// authoritative copy from S3 (the local file is now a zeroed sparse stub).
	chainTelegramUploadDirect(torrentFilePath, chatID, infoHash)
}

// RunTorrentS3MoveJob is the scheduler handler registered as "torrent_s3_move".
// It is invoked by the scheduler worker pool — never by torrent goroutines
// directly. It is now the FALLBACK / crash-recovery path: the primary path is
// the inline synchronous archiver archiveTorrentFileInline, which uploads each
// file to S3 the instant it completes. This job runs only when the inline
// archiver explicitly submits a fallback (because its S3 upload failed or the
// file was missing), or for jobs left queued across a container restart.
//
// Recovery-first logic:
//   - If the file is on disk → upload to S3, punch the local blocks, chain
//     Telegram (the normal fallback upload).
//   - If the file is missing but already in the S3 registry (exact path /
//     torrent hash / basename) → just chain Telegram.
//   - If the file is missing and not in S3, but the torrent is still
//     re-downloading after a restart → defer (success) so the inline archiver
//     handles the re-download; do not burn the retry budget.
//   - Otherwise (torrent paused/gone) → permanent failure.
//
// Pipeline (normal fallback upload, file present):
//  1. Parse payload & validate file on disk.
//  2. Upload to S3 via filecore.RegisterAndArchiveToS3 (keepSparse=true so
//     anacrolix does not re-request pieces for the missing file).
//  3. Free local data blocks via punchHole (sparse stub preserves logical size).
//  4. Submit a downstream telegram_upload job.
func RunTorrentS3MoveJob(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
	if !filecore.IsS3Enabled() {
		return fmt.Errorf("S3 object storage is disabled — torrent_s3_move requires S3")
	}

	// ── 1. Parse payload ──────────────────────────────────────────────────────
	var payload TorrentS3MovePayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal torrent_s3_move payload: %w", err)
	}
	if payload.SaveDirectory == "" {
		payload.SaveDirectory = "./data/manager/downloads"
	}

	torrentFilePath := filecore.GetAbsolutePath(filepath.Join(payload.SaveDirectory, payload.FilePath))

	logFn("INFO", fmt.Sprintf("torrent_s3_move: verifying file: %s", torrentFilePath))

	// ── 2. File existence check ───────────────────────────────────────────────
	info, err := os.Stat(torrentFilePath)
	if err != nil {
		// File missing from local disk. This typically means the Clever Cloud
		// container restarted (ephemeral disk wiped) after the job was queued
		// but before it could run — or the file was already archived by another
		// pipeline component (the file leecher) under a different registry path.
		//
		// Three-tier S3 registry lookup (exact path → torrent hash → basename)
		// to recover a file that is no longer on the ephemeral disk.
		if reg, tier, found := findArchivedRegistry(torrentFilePath, payload.InfoHash); found {
			logFn("INFO", fmt.Sprintf("File already archived in S3 (%s, s3_key=%s) — chaining Telegram", tier, reg.S3Key))
			return chainTelegramUpload(job, logFn, torrentFilePath, payload.ChatID, payload.InfoHash)
		}

		// File not on disk and not in any S3 registry entry. Before declaring
		// permanent failure, check whether the torrent is still active: after a
		// container restart the ephemeral disk is wiped, but the torrent manager
		// re-adds non-paused torrents and re-downloads the file. The inline
		// archiver (updateStats per-file hook) pushes it to S3 the instant it
		// re-completes, so this legacy scheduler job should defer instead of
		// burning through its retry budget — which would mark it permanently
		// failed before a large file can re-download.
		if torrentIsDownloading(payload.InfoHash) {
			logFn("INFO", fmt.Sprintf(
				"File not on disk yet — torrent is re-downloading after a container restart "+
					"(ephemeral disk was wiped). Deferring to the inline S3 archiver; "+
					"this scheduler job will not retry (path=%s).",
				torrentFilePath,
			))
			db.DB.Model(job).Update("progress", 100)
			return nil
		}

		// Truly unrecoverable: the torrent is gone/paused/completed and the file
		// is neither on disk nor in S3. Bump RetryCount past the limit to stop
		// the scheduler from retrying endlessly — there is nothing to upload.
		logFn("ERROR", fmt.Sprintf(
			"File not found on disk and not in S3 registry (path=%s). "+
				"The torrent is no longer downloading, so the file cannot reappear. "+
				"Marking job as permanently failed to prevent futile retries.",
			torrentFilePath,
		))
		job.RetryCount = 9999 // sentinel: force permanent failure in engine
		return fmt.Errorf(
			"file lost from ephemeral disk and not found in S3 registry by path, "+
				"torrent_hash, or filename — cannot recover (path=%s): %w",
			torrentFilePath, err,
		)
	}
	if info.IsDir() {
		return fmt.Errorf("target path is a directory, expected a file: %s", torrentFilePath)
	}

	// Sparse stub detection: first 4 KiB all-zero means punchHole already ran.
	if isTorrentFileSparse(torrentFilePath) {
		logFn("INFO", "File is a sparse stub — checking registry for existing S3 key")
		var reg models.FileRegistry
		if db.DB.Where("torrent_hash = ? AND s3_key != ''", payload.InfoHash).First(&reg).Error == nil {
			logFn("INFO", fmt.Sprintf("S3 key confirmed via torrent_hash (%s) — chaining Telegram upload", reg.S3Key))
			return chainTelegramUpload(job, logFn, torrentFilePath, payload.ChatID, payload.InfoHash)
		}
		// Try basename match — leecher may have archived the file under a different path.
		if db.DB.Where("file_path LIKE ? AND s3_key != ''", "%/"+filepath.Base(torrentFilePath)).First(&reg).Error == nil {
			logFn("INFO", fmt.Sprintf("S3 key found via filename match (%s) — chaining Telegram upload", reg.S3Key))
			return chainTelegramUpload(job, logFn, torrentFilePath, payload.ChatID, payload.InfoHash)
		}
		// Sparse stub but no S3 key found anywhere — previous upload failed mid-way.
		// Allow the scheduler to retry (do NOT set RetryCount sentinel here because
		// the stub still exists: we can retry the upload from the sparse stub path).
		return fmt.Errorf("file is a sparse stub but has no S3 key in registry — will retry upload")
	}

	// ── 3. Upload to S3 ───────────────────────────────────────────────────────
	logFn("INFO", fmt.Sprintf("Uploading %s to S3 (info_hash=%s, size=%s)",
		filepath.Base(torrentFilePath), payload.InfoHash, formatBytes(info.Size())))
	db.DB.Model(job).Update("progress", 10)

	reg, err := filecore.RegisterAndArchiveToS3(torrentFilePath, "", "", 0, payload.InfoHash, true)
	if err != nil {
		return fmt.Errorf("S3 upload failed: %w", err)
	}

	logFn("INFO", fmt.Sprintf("S3 upload complete (s3_key=%s)", reg.S3Key))
	db.DB.Model(job).Update("progress", 70)

	// ── 4. Free local disk blocks ─────────────────────────────────────────────
	logFn("INFO", "Freeing local disk blocks via punchHole (sparse stub preserved)")
	if err := punchHole(torrentFilePath); err != nil {
		// Non-fatal: S3 copy is the source of truth. Log and continue.
		logFn("WARN", fmt.Sprintf("punchHole failed — local blocks not freed (S3 succeeded): %v", err))
	} else {
		logFn("INFO", "Local blocks freed. Sparse stub in place. Disk space recovered.")
	}
	db.DB.Model(job).Update("progress", 85)

	// ── 5. Chain downstream Telegram upload ──────────────────────────────────
	return chainTelegramUpload(job, logFn, torrentFilePath, payload.ChatID, payload.InfoHash)
}

// createTelegramUploadJob inserts a telegram_upload scheduler job into the DB
// and returns it. It is shared by the inline torrent archiver and the
// torrent_s3_move scheduler handler so the Telegram chain is identical in both
// paths.
//
// infoHash is the originating torrent's info hash; it is stored in the job
// payload so RunTelegramUploadJob can recover the S3 key by torrent_hash when
// the local copy was removed (ephemeral-disk wipe) and the registry record lives
// under a different path (checksum dedup / re-download). Pass "" for non-torrent
// uploads.
//
// Idempotency: if a non-terminal telegram_upload job already exists for the
// same file path it returns (nil, nil) so callers skip the duplicate. This
// prevents double Telegram uploads when both the inline archiver and a
// recovering torrent_s3_move scheduler job run for the same file.
//
// It deliberately does NOT import the scheduler package to keep the import
// graph clean (scheduler already imports torrent for this file). The wakeup
// signal is not needed: the queue scanner's 2-second ticker picks up the job.
func createTelegramUploadJob(filePath string, chatID int64, priority int, infoHash string) (*models.SchedulerJob, error) {
	type tgPayload struct {
		FilePath string `json:"file_path"`
		ChatID   int64  `json:"chat_id"`
		InfoHash string `json:"info_hash,omitempty"`
	}

	// Idempotency — match on the file_path JSON fragment in the payload.
	pathFrag := fmt.Sprintf(`"file_path":"%s"`, strings.ReplaceAll(filePath, `\`, `\\`))
	var existing int64
	db.DB.Model(&models.SchedulerJob{}).
		Where("type = ? AND status NOT IN ? AND payload LIKE ?",
			"telegram_upload",
			[]string{models.JobStatusFailed, models.JobStatusCancelled},
			"%"+pathFrag+"%").
		Count(&existing)
	if existing > 0 {
		return nil, nil // already queued/running — skip duplicate
	}

	payloadBytes, err := json.Marshal(tgPayload{FilePath: filePath, ChatID: chatID, InfoHash: infoHash})
	if err != nil {
		return nil, err
	}

	p := priority
	if p <= 0 {
		p = 5
	}
	telegramJob := &models.SchedulerJob{
		UUID:        generateUUID(),
		Type:        "telegram_upload",
		Name:        fmt.Sprintf("Upload Torrent: %s", filepath.Base(filePath)),
		Description: fmt.Sprintf("Auto-upload of torrent file '%s' to Telegram after S3 archive", filepath.Base(filePath)),
		Category:    "files",
		Priority:    p,
		Status:      models.JobStatusQueued,
		Payload:     string(payloadBytes),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.DB.Create(telegramJob).Error; err != nil {
		return nil, err
	}
	return telegramJob, nil
}

// torrentSendToTelegramEnabled reports whether the torrent was explicitly
// opted in to the Telegram chain via the Add Torrent modal checkbox (persisted
// on the TorrentJob row). Every torrent → Telegram path funnels through
// chainTelegramUpload / chainTelegramUploadDirect (see gates below), so a
// single lookup here covers the inline archiver, the pre-download skip path,
// the direct-S3 finalize notifier and the torrent_s3_move fallback. Unknown
// hashes (no row) and legacy rows default to false: the S3 archive safety net
// keeps running for every torrent, only the Telegram leg is opt-in.
func torrentSendToTelegramEnabled(infoHash string) bool {
	if infoHash == "" {
		return false
	}
	var tj models.TorrentJob
	if err := db.DB.Select("send_to_telegram").Where("info_hash = ?", infoHash).First(&tj).Error; err != nil {
		return false
	}
	return tj.SendToTelegram
}

// chainTelegramUpload queues a downstream telegram_upload job for the given
// file and reports the outcome via the scheduler logFn. The torrent_s3_move job
// is marked complete regardless — the file is already safe in S3, so a
// Telegram failure is non-fatal. infoHash is forwarded into the telegram job
// payload so the upload can recover the S3 key by torrent_hash.
//
// The upload only happens for torrents opted in via the Add Torrent modal
// (SendToTelegram): otherwise the chain is skipped and the parent job is
// marked complete — the file is already archived in S3.
func chainTelegramUpload(job *models.SchedulerJob, logFn func(string, string), filePath string, chatID int64, infoHash string) error {
	if !torrentSendToTelegramEnabled(infoHash) {
		logFn("INFO", fmt.Sprintf("Telegram upload not requested for this torrent — skipping '%s' (file is archived in S3)", filepath.Base(filePath)))
		db.DB.Model(job).Update("progress", 100)
		return nil
	}
	tgJob, err := createTelegramUploadJob(filePath, chatID, job.Priority, infoHash)
	if err != nil {
		logFn("WARN", fmt.Sprintf("telegram_upload job submission failed: %v — file is safe in S3", err))
		db.DB.Model(job).Update("progress", 100)
		return nil
	}
	if tgJob == nil {
		logFn("INFO", fmt.Sprintf("telegram_upload already queued for '%s' — skipping duplicate", filepath.Base(filePath)))
		db.DB.Model(job).Update("progress", 100)
		return nil
	}
	logFn("INFO", fmt.Sprintf("Queued telegram_upload job (id=%d) for '%s'", tgJob.ID, filepath.Base(filePath)))
	db.DB.Model(job).Update("progress", 100)
	return nil
}

// chainTelegramUploadDirect is the inline-path variant of chainTelegramUpload:
// it has no parent scheduler job and no logFn callback, logging directly via
// the structured logger. Used by archiveTorrentFileInline (see
// chainTelegramUpload for the opt-in gate description). infoHash is forwarded
// into the telegram job payload for S3-key recovery by torrent_hash.
func chainTelegramUploadDirect(filePath string, chatID int64, infoHash string) {
	if !torrentSendToTelegramEnabled(infoHash) {
		logger.Info("Torrent", "Inline: Telegram upload not requested for this torrent — skipping (file is archived in S3)",
			"info_hash", infoHash, "file", filepath.Base(filePath))
		return
	}
	tgJob, err := createTelegramUploadJob(filePath, chatID, 5, infoHash)
	if err != nil {
		logger.Warn("Torrent", "Inline: telegram_upload job submission failed — file is safe in S3",
			"file", filePath, "error", err)
		return
	}
	if tgJob == nil {
		logger.Info("Torrent", "Inline: telegram_upload already queued — skipping duplicate",
			"file", filepath.Base(filePath))
		return
	}
	logger.Info("Torrent", "Inline: queued telegram_upload job",
		"file", filepath.Base(filePath), "id", tgJob.ID)
}

// torrentIsDownloading reports whether the torrent for the given info hash is
// still actively downloading/seeding on this container. After a Clever Cloud
// container restart the ephemeral disk is wiped, but the torrent manager
// re-adds non-paused torrents which re-download their files. This lets the
// torrent_s3_move handler decide whether a missing file will reappear (defer
// to the inline archiver) or is permanently lost.
func torrentIsDownloading(infoHash string) bool {
	var tj models.TorrentJob
	if err := db.DB.Where("info_hash = ?", infoHash).First(&tj).Error; err != nil {
		return false
	}
	// A paused torrent is not re-downloading; a completed torrent should
	// already be in S3. Only downloading/seeding torrents re-fetch files.
	return tj.Status == "downloading" || tj.Status == "seeding"
}

// isTorrentFileSparse checks whether the first 4 KiB of a file are all zeroes
// — a reliable heuristic for detecting a punchHole sparse stub. Returns false
// on any read error (safe: causes a redundant S3 put of zeroes at worst).
func isTorrentFileSparse(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false
	}
	for i := 0; i < n; i++ {
		if buf[i] != 0 {
			return false
		}
	}
	return true
}

// generateUUID produces a UUID v4 string using crypto/rand.
func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: timestamp-based pseudo-unique string.
		return fmt.Sprintf("torrent-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// formatBytes returns a human-readable byte count (e.g. "1.23 GiB").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
