package storageguard

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"

	"gorm.io/gorm"
)

// LedgerTotals summarizes the offload ledger for the dashboard.
type LedgerTotals struct {
	Files         int64 `json:"files"`
	Uploaded      int64 `json:"uploaded"`
	EvictedLocal  int64 `json:"evicted_local"`
	UploadedBytes int64 `json:"uploaded_bytes"`
}

// Totals aggregates ledger counters across all torrents.
func Totals() LedgerTotals {
	var t LedgerTotals
	db.DB.Model(&models.TorrentFileOffload{}).Select(
		"COUNT(*) as files, SUM(CASE WHEN uploaded THEN 1 ELSE 0 END) as uploaded, " +
			"SUM(CASE WHEN evicted_local THEN 1 ELSE 0 END) as evicted_local, " +
			"COALESCE(SUM(CASE WHEN uploaded THEN size ELSE 0 END),0) as uploaded_bytes").
		Scan(&t)
	return t
}

// Status consolidates guard state for the admin storage panel.
type Status struct {
	S3Enabled       bool   `json:"s3_enabled"`
	GuardRunning    bool   `json:"guard_running"`
	Bucket          string `json:"bucket,omitempty"`
	Prefix          string `json:"prefix"`
	PausedByGuard   bool   `json:"paused_by_guard"`
	UploadsInFlight int    `json:"uploads_in_flight"`
	Disk            struct {
		TotalBytes  uint64  `json:"total_bytes"`
		FreeBytes   uint64  `json:"free_bytes"`
		UsedBytes   uint64  `json:"used_bytes"`
		UsedPercent float64 `json:"used_percent"`
	} `json:"disk"`
	Config models.StorageConfig `json:"config"`
}

// Snapshot captures the current guard status (safe for any goroutine).
func Snapshot() Status {
	g := Default
	cfg := LoadConfig()
	usage := getDiskUsage(stageDir())

	g.mu.Lock()
	paused := g.pausedByGuard
	inflight := 0
	for _, set := range g.inflight {
		inflight += len(set)
	}
	g.mu.Unlock()

	st := Status{
		S3Enabled:       Enabled(),
		GuardRunning:    g.started,
		PausedByGuard:   paused,
		UploadsInFlight: inflight,
		Config:          cfg,
	}
	if Enabled() {
		st.Bucket = s3store.BucketName()
	}
	st.Disk.TotalBytes = usage.TotalBytes
	st.Disk.FreeBytes = usage.FreeBytes
	st.Disk.UsedBytes = usage.UsedBytes
	st.Disk.UsedPercent = usage.UsedPercent
	return st
}

// ResetOffloadProtection clears the "restored" protection so the guard may
// re-offload a restored torrent to reclaim disk space.
func ResetOffloadProtection(infoHash string) error {
	job, err := jobByHash(infoHash)
	if err != nil {
		return err
	}
	if job.RestoreStatus != "" {
		db.DB.Model(job).Update("restore_status", "")
		if t := Default.liveTorrentByHash(infoHash); t != nil {
			t.AllowDataDownload()
		}
		logger.Info("StorageGuard", "Offload protection cleared", "info_hash", infoHash)
		logEvent("info", "reoffload", "Offload protection cleared: "+infoHash)
	}
	return nil
}

// PurgeTorrentData removes every S3 object and ledger row of a torrent.
// Called when a torrent job is deleted with its data.
func PurgeTorrentData(infoHash string) error {
	cfg := LoadConfig()
	prefix := s3store.TorrentPrefix(cfg.S3Prefix, infoHash)

	db.DB.Where("info_hash = ?", infoHash).Delete(&models.TorrentFileOffload{})
	db.DB.Model(&models.FileRegistry{}).Where("torrent_hash = ? AND in_s3 = ?", infoHash, true).
		Updates(map[string]interface{}{"in_s3": false, "s3_key": ""})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		n, err := s3store.DeletePrefix(ctx, prefix)
		if err != nil {
			logger.Error("StorageGuard", "S3 purge failed", "info_hash", infoHash, "error", err)
			logEvent("error", "purge", "S3 purge failed for "+infoHash)
			return
		}
		logger.Info("StorageGuard", "Purged torrent data from Cellar",
			"info_hash", infoHash, "objects", n)
		logEvent("info", "purge", "Purged torrent data from Cellar: "+infoHash)
	}()
	return nil
}

// PurgeLocalPathFromS3 removes S3 objects, ledger rows and registry mirrors
// tied to a local path (one file or everything under a directory). Used by
// the Files panel delete action so evicted copies cannot linger in Cellar.
func PurgeLocalPathFromS3(path string) {
	rows := make([]models.TorrentFileOffload, 0)
	if err := db.DB.Where("uploaded = ?", true).Find(&rows).Error; err != nil {
		return
	}
	matched := make([]*models.TorrentFileOffload, 0)
	for i := range rows {
		p := rows[i].FilePath
		if p == path || strings.HasPrefix(p, path+string(os.PathSeparator)) {
			matched = append(matched, &rows[i])
		}
	}
	if len(matched) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, row := range matched {
		if row.S3Key != "" {
			if err := s3store.DeleteObject(ctx, row.S3Key); err != nil {
				logger.Warn("StorageGuard", "Failed deleting S3 object during purge",
					"key", row.S3Key, "error", err)
			}
		}
		db.DB.Delete(row)
	}
	// registry rows: exact file or anything under the deleted directory
	db.DB.Model(&models.FileRegistry{}).
		Where("file_path = ? OR file_path LIKE ?", path, path+string(os.PathSeparator)+"%").
		Updates(map[string]interface{}{"in_s3": false, "s3_key": ""})
	logger.Info("StorageGuard", "Purged S3 state for deleted path", "path", path, "entries", len(matched))
	logEvent("info", "purge", "Purged "+itoa(len(matched))+" S3 entries for deleted path "+path)
}

// jobByHash loads a TorrentJob or returns a friendly error.
func jobByHash(infoHash string) (*models.TorrentJob, error) {
	var job models.TorrentJob
	if err := db.DB.Where("info_hash = ?", infoHash).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	return &job, nil
}
