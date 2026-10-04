package storageguard

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"

	"github.com/anacrolix/torrent"
	"gorm.io/gorm"
)

// absSaveDir resolves the absolute staging directory of a job.
func absSaveDir(saveDir string) string {
	if saveDir == "" {
		saveDir = defaultStageDir
	}
	abs, err := filepath.Abs(saveDir)
	if err != nil {
		return saveDir
	}
	return abs
}

// loadLedgerRows fetches all offload ledger rows of one torrent keyed by file index.
func (g *Guard) loadLedgerRows(infoHash string) map[int]*models.TorrentFileOffload {
	rows := make([]models.TorrentFileOffload, 0)
	if err := db.DB.Where("info_hash = ?", infoHash).Find(&rows).Error; err != nil {
		return nil
	}
	m := make(map[int]*models.TorrentFileOffload, len(rows))
	for i := range rows {
		m[rows[i].FileIndex] = &rows[i]
	}
	return m
}

// ensureLedgerRows creates/refreshes one ledger row per torrent file so the
// guard can track each file's S3 confirmation independently.
func (g *Guard) ensureLedgerRows(cfg models.StorageConfig, job *models.TorrentJob, absDir string, files []*torrent.File) {
	for i, f := range files {
		rel := f.Path()
		abs := filepath.Clean(filepath.Join(absDir, rel))
		key := s3store.KeyForTorrentFile(cfg.S3Prefix, job.InfoHash, rel)

		var row models.TorrentFileOffload
		err := db.DB.Where("info_hash = ? AND file_index = ?", job.InfoHash, i).First(&row).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			db.DB.Create(&models.TorrentFileOffload{
				InfoHash:  job.InfoHash,
				FileIndex: i,
				FilePath:  abs,
				RelPath:   rel,
				Size:      f.Length(),
				S3Key:     key,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			})
		case err == nil && !row.Uploaded && (row.FilePath != abs || row.Size != f.Length() || row.S3Key != key):
			// path drift (moved save dir, changed prefix) — heal before upload
			db.DB.Model(&row).Updates(map[string]interface{}{
				"file_path":  abs,
				"size":       f.Length(),
				"s3_key":     key,
				"updated_at": time.Now(),
			})
		}
	}
}

// wantUpload decides whether completed files of this torrent should be relayed.
func wantUpload(cfg models.StorageConfig, pressure bool, job *models.TorrentJob) bool {
	if !cfg.S3Enabled {
		return false
	}
	return cfg.OffloadOnCompletion || pressure || job.StreamMode
}

// processTorrent drives one live torrent: ledger sync, per-file completion
// detection, upload queueing, S3-confirmed eviction, stream batches, counters.
func (g *Guard) processTorrent(cfg models.StorageConfig, pressure bool, job *models.TorrentJob, t *torrent.Torrent) {
	files := t.Files()
	if len(files) == 0 {
		return
	}
	absDir := absSaveDir(job.SaveDirectory)
	g.ensureLedgerRows(cfg, job, absDir, files)
	rows := g.loadLedgerRows(job.InfoHash)

	var uploaded, evicted int
	var uploadedBytes int64
	protected := restoreProtect(job)
	for i, f := range files {
		row := rows[i]
		if row == nil {
			continue
		}
		if !row.Uploaded {
			if !protected && wantUpload(cfg, pressure, job) && f.BytesCompleted() >= f.Length() {
				g.tryUpload(job, row, i)
			}
		} else {
			uploaded++
			uploadedBytes += row.Size
			if row.EvictedLocal {
				evicted++
				// secured remotely — never let the client redownload it
				f.Cancel()
			}
		}
	}

	// Eviction of S3-confirmed files is unconditional: a local copy is
	// deleted as soon as the ledger shows it secured in object storage
	// (freeing disk), unless the user explicitly restored this torrent.
	if !protected {
		for i, f := range files {
			row := rows[i]
			if row == nil || !row.Uploaded || row.EvictedLocal {
				continue
			}
			if g.evictLocalCopy(job.InfoHash, row, f, absDir) {
				evicted++
			}
		}
	}

	g.applyStreamMode(cfg, job, t, files, rows)
	g.updateJobOffloadState(cfg, job, len(files), uploaded, evicted, uploadedBytes)
}

// evictLocalCopy deletes the local copy of an S3-confirmed file and records
// the eviction. f (optional) gets Cancel()ed so pieces are never re-fetched.
// stopAt is the staging root the empty-dir cleanup must never climb above.
func (g *Guard) evictLocalCopy(infoHash string, row *models.TorrentFileOffload, f *torrent.File, stopAt string) bool {
	if st, err := os.Stat(row.FilePath); err == nil && st.IsDir() {
		return false
	} else if err == nil {
		if err := os.Remove(row.FilePath); err != nil {
			logger.Warn("StorageGuard", "Eviction failed — keeping local copy", "path", row.FilePath, "error", err)
			return false
		}
	} else if !os.IsNotExist(err) {
		return false // unreadable path: do not blindly mark evicted
	}
	now := time.Now()
	db.DB.Model(row).Updates(map[string]interface{}{
		"evicted_local": true,
		"evicted_at":    now,
		"updated_at":    now,
	})
	if f != nil {
		f.Cancel()
	}
	logger.Info("StorageGuard", "Evicted local copy (safe in S3)", "path", row.FilePath, "info_hash", infoHash)

	// clean up now-empty ancestor directories, stopping at the staging root
	if stopAt != "" {
		for dir := filepath.Dir(row.FilePath); dir != stopAt && len(dir) > len(stopAt); dir = filepath.Dir(dir) {
			if err := os.Remove(dir); err != nil {
				return true // not empty (or gone): stop climbing
			}
		}
	}
	return true
}

// evictOrphanedCopies sweeps S3-confirmed files that still exist locally for
// torrents that are no longer live in the client (restarts, drops, purges).
func (g *Guard) evictOrphanedCopies() {
	live := make(map[string]bool)
	for _, t := range g.snapshotTorrents() {
		live[t.InfoHash().HexString()] = true
	}
	rows := make([]models.TorrentFileOffload, 0)
	if err := db.DB.Where("uploaded = ? AND evicted_local = ?", true, false).Find(&rows).Error; err != nil {
		return
	}
	for i := range rows {
		row := &rows[i]
		if live[row.InfoHash] {
			continue // handled by the live per-torrent pass
		}
		if g.inflightAny(row.InfoHash, row.FileIndex) {
			continue
		}
		stopAt := defaultStageDir
		var job models.TorrentJob
		if err := db.DB.Where("info_hash = ?", row.InfoHash).First(&job).Error; err == nil {
			if restoreProtect(&job) {
				continue // user-restored torrent: keep the local copy
			}
			stopAt = job.SaveDirectory
		}
		g.evictLocalCopy(row.InfoHash, row, nil, absSaveDir(stopAt))
	}
}
