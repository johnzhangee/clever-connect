// Package storageguard protects the small Clever Cloud instance disk
// (~40 GB) from overflowing: as soon as torrent files finish downloading they
// are relayed to the Clever Cellar (S3-compatible) bucket, local copies are
// evicted once confirmed in S3, giant torrents are downloaded in bounded
// sequential batches ("Stream Mode"), and a disk watchdog with hysteresis
// pauses everything as a last resort.
//
// Safety rule (eviction): a local file is only ever deleted AFTER its S3
// upload is confirmed via the persistent TorrentFileOffload ledger.
package storageguard

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"

	"github.com/anacrolix/torrent"
)

const (
	defaultStageDir      = "./data/manager/downloads"
	sweepInterval        = 5 * time.Second
	configCacheTTL       = 15 * time.Second
	uploadRetryBackoff   = 30 * time.Second
	defaultUploadTimeout = 2 * time.Hour
)

// TorrentProvider grants the guard read access to the live torrent client.
type TorrentProvider interface {
	Torrents() []*torrent.Torrent
	TorrentByHash(infoHash string) *torrent.Torrent
}

type uploadedSnapshot struct {
	bytes int64
	at    time.Time
}

// Guard is the storage guard engine singleton.
type Guard struct {
	mu            sync.Mutex
	provider      TorrentProvider
	stop          chan struct{}
	started       bool
	pausedByGuard bool
	// offloadReady: S3 relay is configured and enabled. When false the guard
	// still runs, but only enforces the disk watermarks (pause/resume) —
	// disk protection must never depend on S3 being reachable.
	offloadReady bool
	// in-flight uploads: infoHash -> set of file indices
	inflight map[string]map[int]bool
	// "hash:idx" -> earliest retry time after a failed upload
	retryAfter map[string]time.Time
	// last per-hash uploaded-bytes snapshot, for S3 speed computation
	lastUploaded map[string]uploadedSnapshot
	sem          chan struct{}
	semCap       int
	lastCfg      models.StorageConfig
	lastCfgAt    time.Time
}

// Default is the global guard instance.
var Default = &Guard{
	inflight:     make(map[string]map[int]bool),
	retryAfter:   make(map[string]time.Time),
	lastUploaded: make(map[string]uploadedSnapshot),
}

// Init seeds the default StorageConfig row (called after DB migrations). It
// also re-floors legacy configurations — MaxConcurrentUploads was historically
// seeded at 2, which starves multi-core hosts — to one offload upload per CPU
// core so eviction sweeps use the full machine.
func Init() {
	var cfg models.StorageConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		cfg = defaultStorageConfig()
		if err := db.DB.Create(&cfg).Error; err != nil {
			logger.Error("StorageGuard", "Failed to seed StorageConfig", "error", err)
		} else {
			logEvent("info", "init", fmt.Sprintf(
				"Seeded default storage config (S3 offloading on, %d concurrent uploads)", cfg.MaxConcurrentUploads))
		}
	} else {
		if cfg.MaxConcurrentUploads < autoUploadWorkers() {
			cfg.MaxConcurrentUploads = autoUploadWorkers()
			db.DB.Model(&models.StorageConfig{}).Where("id = ?", cfg.ID).
				Update("max_concurrent_uploads", cfg.MaxConcurrentUploads)
			logEvent("info", "init", fmt.Sprintf(
				"Auto-scaled max_concurrent_uploads to %d (one S3 upload per CPU core)", cfg.MaxConcurrentUploads))
		}
		// Re-floor legacy watermark rows toward today's defaults: offload
		// pressure once free space drops below 30% (used ≥ 70) and the hard
		// pause at 85% used. Only tightening is applied — an admin's stricter
		// setting is never loosened.
		def := defaultStorageConfig()
		if cfg.HighWatermarkPercent > def.HighWatermarkPercent {
			cfg.HighWatermarkPercent = def.HighWatermarkPercent
			db.DB.Model(&models.StorageConfig{}).Where("id = ?", cfg.ID).
				Update("high_watermark_percent", cfg.HighWatermarkPercent)
			logEvent("info", "init", fmt.Sprintf(
				"Tightened high watermark to %d%% — offload pressure now starts at 30%% free", cfg.HighWatermarkPercent))
		}
		if cfg.PauseWatermarkPercent > def.PauseWatermarkPercent {
			cfg.PauseWatermarkPercent = def.PauseWatermarkPercent
			db.DB.Model(&models.StorageConfig{}).Where("id = ?", cfg.ID).
				Update("pause_watermark_percent", cfg.PauseWatermarkPercent)
			logEvent("info", "init", fmt.Sprintf(
				"Tightened pause watermark to %d%%", cfg.PauseWatermarkPercent))
		}
		// One-time rollout of direct-to-S3 torrent storage: rows created
		// before the feature shipped carry the zero-value default (off),
		// which was never an admin decision. Enable it once and remember it
		// with the marker, so a later explicit opt-out survives restarts.
		// The flag is inert while no S3 backend is configured.
		if !cfg.DirectS3Initialized {
			db.DB.Model(&models.StorageConfig{}).Where("id = ?", cfg.ID).
				Updates(map[string]interface{}{
					"direct_s3_enabled":     true,
					"direct_s3_initialized": true,
				})
			logEvent("info", "init", "Enabled direct-to-S3 torrent storage (rollout default) — "+
				"torrent pieces now stream into object storage instead of the local disk")
		}
	}
}

// autoUploadWorkers is the automatic offload upload concurrency: one transfer
// per CPU core, capped at 16 to mirror the manual ceiling enforced by the
// storage admin API.
func autoUploadWorkers() int {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	return n
}

func defaultStorageConfig() models.StorageConfig {
	return models.StorageConfig{
		S3Enabled:               true,
		OffloadOnCompletion:     true,
		EvictAfterUpload:        true,
		HighWatermarkPercent:    70,
		PauseWatermarkPercent:   85,
		StreamThresholdGB:       12,
		BatchSizeGB:             8,
		MaxConcurrentUploads:    autoUploadWorkers(),
		S3Prefix:                "clever-connect/",
		StopSeedingOnOffload:    true,
		AdmissionEnabled:        true,
		AdmissionReservePercent: 10,
		AdmissionReserveMinGB:   5,
		DirectS3Enabled:         true,
		DirectS3Initialized:     true,
	}
}

// Enabled reports whether S3 offloading is fully usable.
func Enabled() bool {
	return s3store.Enabled()
}

// Working reports whether the guard loop is running.
func Working() bool {
	Default.mu.Lock()
	defer Default.mu.Unlock()
	return Default.started
}

// Start launches the guard loop (no-op when already running).
//
// Offloading (S3 relay, eviction, stream mode) needs a usable bucket, but the
// disk watermarks — the last-resort protection that pauses ALL downloads
// before the disk fills — must never depend on S3 being reachable or enabled.
// Without a bucket the guard therefore starts in watermark-only mode instead
// of staying completely inert.
func Start(provider TorrentProvider) {
	if provider == nil {
		return
	}
	offloadReady := true
	if err := s3store.Init(); err != nil {
		offloadReady = false
		logger.Warn("StorageGuard", "S3 storage unavailable — offloading disabled, disk watermarks remain active", "error", err)
	}
	cfg := LoadConfig()
	if !cfg.S3Enabled {
		offloadReady = false
		logger.Info("StorageGuard", "S3 offloading disabled in storage config — guard runs in watermark-only mode")
	}

	g := Default
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return
	}
	g.provider = provider
	g.stop = make(chan struct{})
	g.started = true
	g.pausedByGuard = false
	g.offloadReady = offloadReady
	g.applyUploadSlots(cfg.MaxConcurrentUploads)
	g.mu.Unlock()

	logger.Info("StorageGuard", "Storage guard started",
		"sweepInterval", sweepInterval.String(), "offloadReady", offloadReady)
	go g.loop()
}

// Stop terminates the guard loop (kept idempotent for shutdown paths).
func Stop() {
	g := Default
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started {
		close(g.stop)
		g.started = false
	}
}

func (g *Guard) loop() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			g.sweep()
		}
	}
}

// applyUploadSlots (re)builds the upload concurrency semaphore. Caller holds g.mu.
func (g *Guard) applyUploadSlots(n int) {
	if n < 1 {
		n = 1
	}
	if g.semCap == n && g.sem != nil {
		return
	}
	g.semCap = n
	g.sem = make(chan struct{}, n)
}

// snapshotTorrents lists the live torrents from the provider (nil before Start).
func (g *Guard) snapshotTorrents() []*torrent.Torrent {
	g.mu.Lock()
	p := g.provider
	needSlots := g.sem == nil
	g.mu.Unlock()
	if p == nil {
		return nil
	}
	if needSlots {
		// sanitize: ensure a semaphore exists before any uploads can start
		cfg := LoadConfig()
		g.mu.Lock()
		g.applyUploadSlots(cfg.MaxConcurrentUploads)
		g.mu.Unlock()
	}
	return p.Torrents()
}

// liveTorrentByHash finds a live torrent by its hex info hash (nil if absent).
func (g *Guard) liveTorrentByHash(infoHash string) *torrent.Torrent {
	g.mu.Lock()
	p := g.provider
	g.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.TorrentByHash(infoHash)
}

// semaphore returns the upload slot limiter (may be nil pre-Start).
func (g *Guard) semaphore() chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sem
}
