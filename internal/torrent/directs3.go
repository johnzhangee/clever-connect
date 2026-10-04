package torrent

// directs3.go — wiring between the torrent manager and the direct-to-S3
// storage backend (internal/s3torrent).
//
// The StorageConfig.DirectS3Enabled flag (on by default; enabled once for
// pre-existing configurations by storageguard.Init) routes torrents through
// the s3torrent dispatching storage: their piece data is written straight
// into object storage as hash-verified multipart parts and no torrent byte
// ever lands on the local disk. Resumed jobs still in flight are migrated to
// the backend as well, and a routed hash never falls back to disk storage —
// a failed S3 open fails the torrent instead. Such torrents therefore
// bypass the machinery that only makes sense for a local file footprint:
//
//   - disk admission control (admission.go) — their bytes never hit disk;
//   - the pre-download existence precheck — piece completion is served by
//     the storage backend itself;
//   - the inline per-file S3 archiver and onTorrentCompleted — the s3torrent
//     finalizer commits one whole-torrent data object instead;
//   - the storage guard's offload/eviction sweep — there is nothing local
//     to relay or evict.
//
// At commit time the s3torrent finalize notifier chains the downstream
// Telegram upload per file and marks the job fully offloaded.

import (
	"path/filepath"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"
	"clever-connect/internal/s3torrent"
	"clever-connect/internal/storageguard"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"
)

// installDirectS3Storage wraps the client's default storage with the
// direct-to-S3 dispatcher. Torrents not routed to S3 fall through to the
// same on-disk storage the client would have used anyway. Called from Init
// before the torrent client is created.
func installDirectS3Storage(cfg *torrent.ClientConfig, saveDir string) *s3torrent.Client {
	dispatch := s3torrent.NewClient(storage.NewFile(saveDir))
	cfg.DefaultStorage = dispatch
	return dispatch
}

// directS3Enabled reports whether newly added torrents should be routed
// through the direct-to-S3 backend.
func directS3Enabled() bool {
	return storageguard.LoadConfig().DirectS3Enabled && s3store.Enabled()
}

// shouldMigrateToDirectS3 reports whether a job persisted with the legacy
// on-disk storage should be switched to the direct-to-S3 backend when it is
// resumed. Finished or already-offloaded jobs keep their mode; everything
// that would otherwise continue to occupy the small instance disk is
// migrated when direct mode is enabled.
func shouldMigrateToDirectS3(job *models.TorrentJob, directEnabled bool) bool {
	if !directEnabled || job.DirectS3 {
		return false
	}
	if job.OffloadStatus == "offloaded" {
		return false
	}
	return !(job.Status == "completed" && job.OffloadStatus == "")
}

// isDirectS3 reports whether the torrent is served by the direct-to-S3
// backend (no local file footprint).
func (m *TorrentManager) isDirectS3(infoHash string) bool {
	return m.directS3Client != nil && m.directS3Client.Has(infoHash)
}

// routeDirectS3 starts routing the hash through the S3 storage dispatcher.
func (m *TorrentManager) routeDirectS3(infoHash string) {
	if m.directS3Client == nil || m.directS3Client.Has(infoHash) {
		return
	}
	m.directS3Client.Route(infoHash)
	logger.Info("Torrent", "Routing torrent to direct-to-S3 storage", "info_hash", infoHash)
}

// unrouteDirectS3 stops routing the hash; already-stored data and its
// persistence row stay in S3, so a later re-add resumes the upload.
func (m *TorrentManager) unrouteDirectS3(infoHash string) {
	if m.directS3Client == nil {
		return
	}
	m.directS3Client.Unroute(infoHash)
}

// onDirectS3Finalized runs when the s3torrent backend finishes assembling a
// torrent's data object in S3: it chains the downstream Telegram upload for
// every file and marks the job fully offloaded.
func (m *TorrentManager) onDirectS3Finalized(infoHash string) {
	var job models.TorrentJob
	if err := db.DB.Where("info_hash = ?", infoHash).First(&job).Error; err != nil {
		logger.Warn("Torrent", "Direct-S3 finalize callback without job row", "info_hash", infoHash)
		return
	}

	absSaveDir := filecore.GetAbsoluteSavePath(job.SaveDirectory)
	if t := m.TorrentByHash(infoHash); t != nil {
		select {
		case <-t.GotInfo():
		default:
		}
		for _, f := range t.Files() {
			chainTelegramUploadDirect(filepath.Clean(filepath.Join(absSaveDir, f.Path())), 0, infoHash)
		}
	}

	db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", infoHash).Updates(map[string]interface{}{
		"offload_status": "offloaded",
		"status":         "completed",
		"updated_at":     time.Now(),
	})
	db.DB.Create(&models.StorageLog{
		Level:     "info",
		Source:    "s3torrent",
		Message:   "Torrent '" + job.Name + "' committed directly to S3 (" + formatBytes(job.TotalBytes) + ")",
		CreatedAt: time.Now(),
	})
	logger.Info("Torrent", "Torrent committed directly to S3",
		"info_hash", infoHash, "name", job.Name, "bytes", job.TotalBytes)
}
