package storageguard

import (
	"context"
	"mime"
	"path/filepath"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"
)

// mimeByExt maps a filename to a content type for S3 uploads.
func mimeByExt(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// markInflight registers an in-flight upload; false when already running.
func (g *Guard) markInflight(hash string, idx int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	set := g.inflight[hash]
	if set == nil {
		set = make(map[int]bool)
		g.inflight[hash] = set
	}
	if set[idx] {
		return false
	}
	set[idx] = true
	return true
}

// unmarkInflight clears an in-flight registration.
func (g *Guard) unmarkInflight(hash string, idx int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if set := g.inflight[hash]; set != nil {
		delete(set, idx)
		if len(set) == 0 {
			delete(g.inflight, hash)
		}
	}
}

// inflightAny reports whether hash (optionally idx) has an upload running.
func (g *Guard) inflightAny(hash string, idx int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	set := g.inflight[hash]
	if set == nil {
		return false
	}
	if idx < 0 {
		return len(set) > 0
	}
	return set[idx]
}

// tryUpload starts an async S3 upload for one ledger row (dedup + backoff).
func (g *Guard) tryUpload(job *models.TorrentJob, row *models.TorrentFileOffload, idx int) {
	if !Enabled() {
		return
	}
	k := streamKey(job.InfoHash, idx)
	g.mu.Lock()
	if time.Now().Before(g.retryAfter[k]) {
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	if !g.markInflight(job.InfoHash, idx) {
		return
	}
	r := *row // copy: goroutine must not race caller's struct
	go g.uploadWorker(job.InfoHash, idx, r, k)
}

// uploadWorker streams a finished torrent file to Cellar, records the
// confirmation in the ledger and FileRegistry, and flags failures for retry.
func (g *Guard) uploadWorker(infoHash string, idx int, row models.TorrentFileOffload, k string) {
	defer g.unmarkInflight(infoHash, idx)

	sem := g.semaphore()
	if sem != nil {
		sem <- struct{}{}
		defer func() { <-sem }()
	}

	store, err := s3store.Get()
	if err != nil {
		g.noteUploadBackoff(k)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultUploadTimeout)
	defer cancel()

	uploaded, err := store.UploadFile(ctx, row.FilePath, row.S3Key, mimeByExt(row.RelPath))
	if err != nil {
		g.noteUploadBackoff(k)
		logger.Error("StorageGuard", "S3 upload failed — will retry",
			"path", row.FilePath, "error", err)
		db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", infoHash).
			Update("offload_status", "failed")
		return
	}

	now := time.Now()
	db.DB.Model(&models.TorrentFileOffload{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{
			"uploaded":    true,
			"uploaded_at": now,
			"s3_key":      row.S3Key,
			"updated_at":  now,
		})
	// mirror the confirmation into the file registry used by the Files panel
	db.DB.Model(&models.FileRegistry{}).Where("file_path = ?", row.FilePath).
		Updates(map[string]interface{}{"s3_key": row.S3Key, "in_s3": true})

	logger.Info("StorageGuard", "File secured in Cellar",
		"path", row.RelPath, "info_hash", infoHash, "size", row.Size, "object", uploaded)
}

// noteUploadBackoff delays the next retry attempt for a failed key.
func (g *Guard) noteUploadBackoff(k string) {
	g.mu.Lock()
	g.retryAfter[k] = time.Now().Add(uploadRetryBackoff)
	g.mu.Unlock()
}
