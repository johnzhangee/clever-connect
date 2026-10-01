package storageguard

import (
	"context"
	"errors"
	"os"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"
)

// restoreProtect says whether a torrent is locally protected after a user
// restore (restoring in progress, or restored and not yet unprotected).
func restoreProtect(job *models.TorrentJob) bool {
	return job.RestoreStatus == "restoring" || job.RestoreStatus == "restored"
}

// RestoreTorrent pulls all S3-stored files of a torrent back to local disk.
// While restored, the torrent is protected from re-offload until the user
// clears protection (ResetOffloadProtection) or deletes the data.
func RestoreTorrent(infoHash string) error {
	job, err := jobByHash(infoHash)
	if err != nil {
		return err
	}
	if !Enabled() {
		return errors.New("S3 storage is not configured")
	}
	if job.RestoreStatus == "restoring" {
		return nil // already in flight
	}

	rows := make([]models.TorrentFileOffload, 0)
	if err := db.DB.Where("info_hash = ? AND uploaded = ?", infoHash, true).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("nothing to restore — no S3-secured files recorded")
	}

	db.DB.Model(job).Update("restore_status", "restoring")
	logger.Info("StorageGuard", "Restoring torrent from Cellar", "info_hash", infoHash, "files", len(rows))
	logEvent("info", "restore", "Restoring "+itoa(len(rows))+" files from Cellar for "+infoHash)
	go restoreWorker(infoHash, rows)
	return nil
}

// restoreWorker downloads each S3-secured file back to its original path.
func restoreWorker(infoHash string, rows []models.TorrentFileOffload) {
	cfg := LoadConfig()
	for i := range rows {
		row := rows[i]
		if row.S3Key == "" {
			continue
		}
		if st, err := os.Stat(row.FilePath); err == nil && st.Size() == row.Size {
			// already back on disk (earlier partial restore): just unmark
			db.DB.Model(&row).Updates(map[string]interface{}{"evicted_local": false, "updated_at": time.Now()})
			continue
		}
		if !waitForDiskRoom(cfg, 180, 10*time.Second) {
			db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", infoHash).
				Update("restore_status", "failed")
			logEvent("error", "restore", "Restore aborted — disk stays too full: "+infoHash)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultUploadTimeout)
		err := s3store.DownloadToFile(ctx, row.S3Key, row.FilePath)
		cancel()
		if err != nil {
			logger.Error("StorageGuard", "Restore download failed — keeping eviction state",
				"path", row.FilePath, "error", err)
			logEvent("error", "restore", "Restore failed for "+row.RelPath+" of "+infoHash)
			db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", infoHash).
				Update("restore_status", "failed")
			return
		}
		db.DB.Model(&row).Updates(map[string]interface{}{"evicted_local": false, "updated_at": time.Now()})
		logger.Info("StorageGuard", "File restored to disk", "path", row.FilePath, "info_hash", infoHash)
	}
	db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", infoHash).
		Update("restore_status", "restored")
	logEvent("info", "restore", "Restore finished: "+infoHash)
}

// waitForDiskRoom sleeps while the staging filesystem is above the pause
// watermark, giving the guard/sweeps a chance to free space first.
func waitForDiskRoom(cfg models.StorageConfig, maxTries int, every time.Duration) bool {
	for i := 0; i < maxTries; i++ {
		usage := getDiskUsage(stageDir())
		if !usage.Valid || usage.UsedPercent < float64(cfg.PauseWatermarkPercent) {
			return true
		}
		time.Sleep(every)
	}
	return false
}
