package storageguard

import (
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

// updateJobOffloadState persists per-torrent offload counters, live upload
// speed, offload status transitions, and stops seeding when fully offloaded.
func (g *Guard) updateJobOffloadState(cfg models.StorageConfig, job *models.TorrentJob, totalFiles, uploaded, evicted int, uploadedBytes int64) {
	updates := map[string]interface{}{
		"offloaded_files": uploaded,
		"offloaded_bytes": uploadedBytes,
	}

	newStatus := ""
	switch {
	case totalFiles > 0 && uploaded >= totalFiles && evicted >= totalFiles:
		newStatus = "offloaded"
	case totalFiles > 0 && uploaded >= totalFiles:
		newStatus = "uploaded"
	case uploaded > 0 || g.inflightAny(job.InfoHash, -1):
		newStatus = "uploading"
	case job.OffloadStatus == "failed":
		newStatus = "failed"
	}
	if newStatus != "" && newStatus != job.OffloadStatus {
		updates["offload_status"] = newStatus
	}

	// S3 relay speed from the per-sweep uploaded-bytes delta
	now := time.Now()
	g.mu.Lock()
	prev, had := g.lastUploaded[job.InfoHash]
	g.lastUploaded[job.InfoHash] = uploadedSnapshot{uploadedBytes, now}
	g.mu.Unlock()
	speed := 0.0
	if had {
		dt := now.Sub(prev.at).Seconds()
		if dt > 0 && uploadedBytes > prev.bytes {
			speed = clampFloat(float64(uploadedBytes-prev.bytes)/dt/(1024*1024), 0, 4096)
		}
	}
	updates["upload_speed_s3"] = speed

	db.DB.Model(job).Updates(updates)

	if newStatus == "offloaded" && newStatus != job.OffloadStatus {
		logger.Info("StorageGuard", "Torrent fully offloaded to Cellar",
			"name", job.Name, "info_hash", job.InfoHash, "files", uploaded, "bytes", uploadedBytes)
		logEvent("info", "offload", "Torrent fully offloaded: "+job.Name)
	}

	// fully offloaded + seeding stopped: data lives only in S3 now
	if newStatus == "offloaded" && cfg.StopSeedingOnOffload && job.Status != "completed" {
		if t := g.liveTorrentByHash(job.InfoHash); t != nil {
			t.Drop() // never deletes any local data — safe by design
			db.DB.Model(job).Updates(map[string]interface{}{
				"status":          "completed",
				"paused_by_guard": false,
				"upload_speed_s3": 0,
			})
			logger.Info("StorageGuard", "Stopped seeding fully offloaded torrent", "info_hash", job.InfoHash)
		}
	}
}

// clampFloat keeps numbers inside sane bounds before DB writes.
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
