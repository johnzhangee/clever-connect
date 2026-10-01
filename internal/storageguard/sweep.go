package storageguard

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"

	"gorm.io/gorm"
)

// LoadConfig returns the current StorageConfig (DB row, cached briefly).
func LoadConfig() models.StorageConfig {
	g := Default
	g.mu.Lock()
	if time.Since(g.lastCfgAt) < configCacheTTL && g.lastCfg.ID != 0 {
		cfg := g.lastCfg
		g.mu.Unlock()
		return cfg
	}
	g.mu.Unlock()

	var cfg models.StorageConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		cfg = defaultStorageConfig()
	}
	g.mu.Lock()
	g.lastCfg = cfg
	g.lastCfgAt = time.Now()
	g.mu.Unlock()
	return cfg
}

// InvalidateConfigCache forces a config re-read after admin changes.
func InvalidateConfigCache() {
	g := Default
	g.mu.Lock()
	g.lastCfgAt = time.Time{}
	g.mu.Unlock()
}

// stageDir returns where torrent data lands (matches TorrentConfig default).
func stageDir() string {
	var tc models.TorrentConfig
	if err := db.DB.First(&tc).Error; err == nil && tc.SaveDirectory != "" {
		return tc.SaveDirectory
	}
	return defaultStageDir
}

// logEvent records a storage event for the admin panel.
func logEvent(level, source, msg string) {
	db.DB.Create(&models.StorageLog{
		Level:     level,
		Source:    source,
		Message:   msg,
		CreatedAt: time.Now(),
	})
}

// s3Available reports whether relaying to S3 is possible right now.
func s3Available() bool {
	return Enabled()
}

// sweep is the guard heartbeat: watermarks first (safety), then per-torrent
// offloading, then global pressure eviction and counter bookkeeping.
func (g *Guard) sweep() {
	cfg := LoadConfig()
	if !s3Available() {
		return
	}

	usage := getDiskUsage(stageDir())
	g.enforceWatermarks(cfg, usage)
	pressure := usage.Valid && usage.UsedPercent >= float64(cfg.HighWatermarkPercent)

	// Track jobs seen this sweep so speed snapshots can be pruned.
	seen := make(map[string]bool)

	for _, t := range g.snapshotTorrents() {
		select {
		case <-t.GotInfo():
		default:
			continue // metadata still arriving
		}
		hash := t.InfoHash().HexString()
		var job models.TorrentJob
		if err := db.DB.Where("info_hash = ?", hash).First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue // unknown to the jobs table (e.g. ephemeral)
			}
			continue
		}
		seen[hash] = true
		if job.RestoreStatus == "restoring" {
			continue // user is pulling this one back; never race disk space
		}
		g.processTorrent(cfg, pressure, &job, t)
	}

	// Evict leftover uploaded copies of torrents no longer in the client.
	g.evictOrphanedCopies(cfg, pressure)

	// Decay speed snapshots for jobs that went away.
	g.mu.Lock()
	for hash := range g.lastUploaded {
		if !seen[hash] {
			delete(g.lastUploaded, hash)
		}
	}
	g.mu.Unlock()
}

// enforceWatermarks applies the two-level hysteresis:
//   - pause watermark: stop ALL downloads immediately (hard floor);
//   - high watermark: resume once usage falls back below it.
func (g *Guard) enforceWatermarks(cfg models.StorageConfig, usage diskUsage) {
	if !usage.Valid {
		return
	}
	g.mu.Lock()
	pausedByGuard := g.pausedByGuard
	g.mu.Unlock()

	if !pausedByGuard && usage.UsedPercent >= float64(cfg.PauseWatermarkPercent) {
		g.mu.Lock()
		g.pausedByGuard = true
		g.mu.Unlock()
		reason := fmt.Sprintf("Disk usage %d%% reached the pause watermark (%d%%) — all downloads paused",
			int(usage.UsedPercent), cfg.PauseWatermarkPercent)
		g.setAllDownloadFlow(false, reason)
	} else if pausedByGuard && usage.UsedPercent < float64(cfg.HighWatermarkPercent) {
		g.mu.Lock()
		g.pausedByGuard = false
		g.mu.Unlock()
		reason := fmt.Sprintf("Disk usage %d%% dropped below the high watermark (%d%%) — downloads resumed",
			int(usage.UsedPercent), cfg.HighWatermarkPercent)
		g.setAllDownloadFlow(true, reason)
	}
}

// setAllDownloadFlow pauses/resumes only torrents the guard paused itself.
func (g *Guard) setAllDownloadFlow(allow bool, reason string) {
	for _, t := range g.snapshotTorrents() {
		hash := t.InfoHash().HexString()
		var job models.TorrentJob
		if err := db.DB.Where("info_hash = ?", hash).First(&job).Error; err != nil {
			continue
		}
		if job.RestoreStatus == "restoring" {
			continue
		}
		if allow {
			if job.PausedByGuard {
				t.AllowDataDownload()
				db.DB.Model(&job).Updates(map[string]interface{}{
					"paused_by_guard": false,
					"status":          "downloading",
				})
			}
		} else if !job.PausedByGuard && job.Status != "paused" {
			t.DisallowDataDownload()
			db.DB.Model(&job).Updates(map[string]interface{}{
				"paused_by_guard": true,
				"status":          "paused",
			})
		}
	}
	msg := reason
	if allow {
		logger.Info("StorageGuard", msg)
	} else {
		logger.Warn("StorageGuard", "PAUSING all downloads — disk nearly full", "reason", msg)
	}
	logEvent("warn", "watermark", msg)
}

// streamKey is the retry/backoff map key for one file of one torrent.
func streamKey(hash string, idx int) string {
	return hash + ":" + itoa(idx)
}

// itoa is strconv.Itoa spelled short (kept local for brevity in hot code).
func itoa(i int) string { return strconv.Itoa(i) }
