package s3torrent

import (
	"context"
	"errors"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"

	"gorm.io/gorm"
)

// AbortTorrentUploads tears down the direct-to-S3 state of a torrent at
// delete time. With deleteData it stops any live workers, aborts an
// in-flight multipart upload, deletes the staged part objects and the final
// data object, and drops the persistence row. Without deleteData everything
// is left untouched so a later re-add resumes the upload.
func AbortTorrentUploads(infoHash string, deleteData bool) {
	if st, ok := liveStores.Load(infoHash); ok {
		st.(*torrentStore).Close()
	}
	if !deleteData {
		return
	}
	store, err := s3store.Get()
	if err != nil {
		logger.Warn("S3Torrent", "Cannot purge direct-S3 data — no bucket configured",
			"info_hash", infoHash)
		return
	}
	api := newS3API(store)
	if api == nil {
		return
	}

	var row models.TorrentS3Upload
	if err := db.DB.Where("info_hash = ?", infoHash).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return
		}
		logger.Warn("S3Torrent", "Failed to load upload row for purge",
			"info_hash", infoHash, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if row.UploadID != "" && !row.Finalized {
		if err := api.AbortMultipartUpload(ctx, row.ObjectKey, row.UploadID); err != nil {
			logger.Warn("S3Torrent", "Multipart abort failed (continuing cleanup)",
				"info_hash", infoHash, "error", err)
		}
	}
	if _, err := api.DeletePrefix(ctx, row.PartsPrefix); err != nil {
		logger.Warn("S3Torrent", "Part-object purge failed", "info_hash", infoHash, "error", err)
	}
	if err := api.DeleteObject(ctx, row.ObjectKey); err != nil {
		logger.Warn("S3Torrent", "Data-object purge failed", "info_hash", infoHash, "error", err)
	}
	if err := db.DB.Where("info_hash = ?", infoHash).Delete(&models.TorrentS3Upload{}).Error; err != nil {
		logger.Warn("S3Torrent", "Upload row purge failed", "info_hash", infoHash, "error", err)
	}
	logger.Info("S3Torrent", "Purged direct-S3 torrent data", "info_hash", infoHash)
}
