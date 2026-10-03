package torrent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/storageguard"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

// Default trackers list of high-quality public trackers to boost download speeds
var DefaultTrackers = []string{
	"udp://tracker.coppersurfer.tk:6969/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://opentracker.i2p.rocks:6969/announce",
	"udp://tracker.internetwarriors.net:1337/announce",
	"udp://tracker.leechers-paradise.org:6969/announce",
	"udp://coppersurfer.tk:6969/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://tracker.cyberia.is:6969/announce",
	"udp://tracker.moack.jacklist.net:1337/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://explodie.org:6969/announce",
	"udp://tracker.tiny-vps.com:6969/announce",
	"http://tracker.gbitt.info:80/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://9.rarbg.to:2710/announce",
	"udp://9.rarbg.me:2780/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://open.stealth.si:80/announce",
}

type torrentSpeed struct {
	lastDownloaded int64
	lastUploaded   int64
	lastTime       time.Time
	downloadSpeed  float64
	uploadSpeed    float64
}

type TorrentManager struct {
	client          *torrent.Client
	mu              sync.Mutex
	speeds          map[string]*torrentSpeed
	registeredFiles map[string]bool  // tracks per-file S3 registration (infoHash:filePath)
	completing      map[string]bool  // infoHash -> true while a torrent is being archived to S3
	skippedBytes    map[string]int64 // infoHash -> bytes satisfied from S3 without a local copy (progress compensation)
	archiveSem      chan struct{}    // bounds concurrent S3 archive (move) transfers: one per CPU core
	stopStats       chan struct{}
	uploadLimiter   *rate.Limiter
	downloadLimiter *rate.Limiter
}

var Manager *TorrentManager

// Init initializes the torrent client instance
func Init() error {
	// If an active manager already exists, close it first to avoid leaks
	if Manager != nil {
		Manager.Close()
	}

	// Auto-migrate the GORM TorrentJob and TorrentConfig models
	if err := db.DB.AutoMigrate(
		&models.TorrentJob{},
		&models.TorrentConfig{},
		&models.StorageConfig{},
		&models.TorrentFileOffload{},
		&models.StorageLog{},
	); err != nil {
		return fmt.Errorf("failed to migrate torrent DB tables: %w", err)
	}

	// Load or create the default TorrentConfig
	var dbCfg models.TorrentConfig
	if err := db.DB.First(&dbCfg).Error; err != nil {
		dbCfg = models.TorrentConfig{
			SaveDirectory:            "./data/manager/downloads",
			MaxConnectionsPerTorrent: 200,
			MaxHalfOpenConnections:   100,
			UploadLimitMB:            0,
			DownloadLimitMB:          0,
			EnableDHT:                true,
			EnablePEX:                true,
			EnableUTP:                true,
			EnableTCP:                true,
			EnableUpload:             true,
			PieceHashersPerTorrent:   4,
			CustomTrackers:           strings.Join(DefaultTrackers, "\n"),
		}
		_ = db.DB.Create(&dbCfg)
	}

	saveDir := dbCfg.SaveDirectory
	if saveDir == "" {
		saveDir = "./data/manager/downloads"
	}
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return fmt.Errorf("failed to create downloads directory: %w", err)
	}

	torrentsDir := "./data/manager/torrents"
	if err := os.MkdirAll(torrentsDir, 0755); err != nil {
		return fmt.Errorf("failed to create torrent metadata directory: %w", err)
	}

	// Warn when running on Clever Cloud's ephemeral filesystem: the download
	// directory must survive container recycles, otherwise files are wiped
	// before they can be archived to S3. The inline S3 archiver (active by
	// default when S3 is configured) pushes each file to object storage the
	// instant it completes — the primary protection against disk loss. A
	// persistent FS-bucket mount at the download path is the recommended
	// defence-in-depth.
	if os.Getenv("CLEVER_CLOUD_APP_ID") != "" || os.Getenv("CC_APP_ID") != "" {
		absSave, _ := filepath.Abs(saveDir)
		logger.Warn("Torrent", "Running on Clever Cloud with an ephemeral download directory — "+
			"enable S3 (CELLAR_ADDON_*) so the inline archiver persists files before disk recycle, "+
			"or mount a persistent FS-bucket at the download path",
			"save_dir", absSave)
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = saveDir
	cfg.NoUpload = !dbCfg.EnableUpload
	cfg.Seed = dbCfg.EnableUpload

	// Apply high performance connection & parallel limits
	cfg.EstablishedConnsPerTorrent = dbCfg.MaxConnectionsPerTorrent
	cfg.HalfOpenConnsPerTorrent = dbCfg.MaxHalfOpenConnections
	cfg.TotalHalfOpenConns = dbCfg.MaxHalfOpenConnections * 2
	cfg.TorrentPeersHighWater = dbCfg.MaxConnectionsPerTorrent * 2
	cfg.TorrentPeersLowWater = dbCfg.MaxConnectionsPerTorrent / 4
	cfg.PieceHashersPerTorrent = dbCfg.PieceHashersPerTorrent

	// Network options
	cfg.NoDHT = !dbCfg.EnableDHT
	cfg.DisablePEX = !dbCfg.EnablePEX
	cfg.DisableUTP = !dbCfg.EnableUTP
	cfg.DisableTCP = !dbCfg.EnableTCP

	// High speed dialing parameters
	cfg.DialForPeerConns = true
	cfg.AlwaysWantConns = true
	cfg.NominalDialTimeout = 5 * time.Second
	cfg.MaxUnverifiedBytes = 256 * 1024 * 1024 // 256MB to saturate network pipes

	// Setup Rate Limiters
	uploadLimiter := rate.NewLimiter(rate.Inf, 1024*1024)
	downloadLimiter := rate.NewLimiter(rate.Inf, 1024*1024)

	if dbCfg.UploadLimitMB > 0 {
		uploadLimiter.SetLimit(rate.Limit(dbCfg.UploadLimitMB * 1024 * 1024))
	}
	if dbCfg.DownloadLimitMB > 0 {
		downloadLimiter.SetLimit(rate.Limit(dbCfg.DownloadLimitMB * 1024 * 1024))
	}

	cfg.UploadRateLimiter = uploadLimiter
	cfg.DownloadRateLimiter = downloadLimiter

	client, err := torrent.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("failed to create torrent client: %w", err)
	}

	Manager = &TorrentManager{
		client:          client,
		speeds:          make(map[string]*torrentSpeed),
		registeredFiles: make(map[string]bool),
		completing:      make(map[string]bool),
		skippedBytes:    make(map[string]int64),
		archiveSem:      make(chan struct{}, runtime.NumCPU()),
		stopStats:       make(chan struct{}),
		uploadLimiter:   uploadLimiter,
		downloadLimiter: downloadLimiter,
	}

	// Reload all existing torrent jobs from database
	var jobs []models.TorrentJob
	if err := db.DB.Find(&jobs).Error; err == nil {
		for _, job := range jobs {
			// Fully offloaded torrents live only in S3 — never re-add them to
			// the client, or it would re-download every evicted byte.
			// Completed torrents whose local files were stripped by the
			// filecore S3 pipeline are in the same boat — unless the storage
			// guard still has an upload or restore in flight, in which case
			// the torrent resumes in a normal state.
			if job.OffloadStatus == "offloaded" ||
				(job.Status == "completed" && job.OffloadStatus == "") {
				continue
			}
			if job.MagnetURI != "" {
				t, err := client.AddMagnet(job.MagnetURI)
				if err == nil {
					Manager.InjectTrackers(t)
					if job.Status == "paused" {
						t.DisallowDataDownload()
						go Manager.ApplyFilePriorities(t, job.SelectedFiles, job.SaveDirectory)
					} else {
						t.AllowDataDownload()
						go Manager.ApplyFilePriorities(t, job.SelectedFiles, job.SaveDirectory)
					}
				}
			} else if job.TorrentPath != "" {
				if _, err := os.Stat(job.TorrentPath); err == nil {
					mi, err := metainfo.LoadFromFile(job.TorrentPath)
					if err == nil {
						t, err := client.AddTorrent(mi)
						if err == nil {
							Manager.InjectTrackers(t)
							if job.Status == "paused" {
								t.DisallowDataDownload()
								go Manager.ApplyFilePriorities(t, job.SelectedFiles, job.SaveDirectory)
							} else {
								t.AllowDataDownload()
								go Manager.ApplyFilePriorities(t, job.SelectedFiles, job.SaveDirectory)
							}
						}
					}
				}
			}
		}
	}

	// Start background speed & progress monitoring loop
	go Manager.statsLoop()

	return nil
}

// ApplyLimits dynamically updates rate limits at runtime without resetting connections
func (m *TorrentManager) ApplyLimits(uploadLimitMB float64, downloadLimitMB float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.uploadLimiter != nil {
		if uploadLimitMB > 0 {
			m.uploadLimiter.SetLimit(rate.Limit(uploadLimitMB * 1024 * 1024))
		} else {
			m.uploadLimiter.SetLimit(rate.Inf)
		}
	}

	if m.downloadLimiter != nil {
		if downloadLimitMB > 0 {
			m.downloadLimiter.SetLimit(rate.Limit(downloadLimitMB * 1024 * 1024))
		} else {
			m.downloadLimiter.SetLimit(rate.Inf)
		}
	}
}

// InjectTrackers adds a set of robust trackers to a torrent to accelerate download speed
func (m *TorrentManager) InjectTrackers(t *torrent.Torrent) {
	var dbCfg models.TorrentConfig
	trackersList := []string{}
	if err := db.DB.First(&dbCfg).Error; err == nil && dbCfg.CustomTrackers != "" {
		lines := strings.Split(dbCfg.CustomTrackers, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				trackersList = append(trackersList, line)
			}
		}
	}

	if len(trackersList) == 0 {
		trackersList = DefaultTrackers
	}

	announceList := make([][]string, len(trackersList))
	for i, tr := range trackersList {
		announceList[i] = []string{tr}
	}

	t.AddTrackers(announceList)
}

// Close gracefully closes the torrent client
func (m *TorrentManager) Close() {
	close(m.stopStats)
	m.client.Close()
}

// statsLoop tick worker
func (m *TorrentManager) statsLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var tick int
	for {
		select {
		case <-m.stopStats:
			return
		case <-ticker.C:
			tick++
			// Persist DB stats every 5 seconds to reduce connection pressure.
			// In-memory speeds are updated every tick for the UI.
			m.updateStats(tick%5 == 0)
		}
	}
}

// updateStats recalculates progress, seeds/peers, and delta upload/download speeds.
// persistDB controls whether the computed values are written to the database.
// Call with persistDB=true every ~5 seconds to reduce DB connection pressure.
func (m *TorrentManager) updateStats(persistDB bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	torrents := m.client.Torrents()
	now := time.Now()

	for _, t := range torrents {
		infoHash := t.InfoHash().HexString()

		// Skip torrents that are currently being archived to S3 — their stats
		// are frozen until onTorrentCompleted finishes and removes the entry.
		// This also prevents the per-second Save below from clobbering the
		// "completed" status that onTorrentCompleted writes.
		if m.completing[infoHash] {
			continue
		}

		var totalBytes int64
		var downloaded int64
		var uploaded int64
		var peers int
		var progress float64
		var name string
		var skipped int64

		select {
		case <-t.GotInfo():
			// Metainfo resolved
			totalBytes = t.Length()
			downloaded = t.BytesCompleted()
			// Bytes already satisfied without a swarm download (S3 skips from
			// the pre-download check) so fully-skipped torrents still reach
			// 100% and trigger completion. Raw `downloaded` keeps feeding the
			// speed-delta calculation below.
			skipped = m.skippedBytes[infoHash]
			stats := t.Stats()
			uploaded = stats.BytesWritten.Int64()
			peers = stats.ActivePeers
			name = t.Name()
			if totalBytes > 0 {
				progress = (float64(downloaded+skipped) / float64(totalBytes)) * 100.0
			}
		default:
			// Metainfo still fetching
			name = "Fetching metadata..."
			stats := t.Stats()
			peers = stats.ActivePeers
		}

		speedInfo, exists := m.speeds[infoHash]
		if !exists {
			speedInfo = &torrentSpeed{
				lastDownloaded: downloaded,
				lastUploaded:   uploaded,
				lastTime:       now,
			}
			m.speeds[infoHash] = speedInfo
		}

		duration := now.Sub(speedInfo.lastTime).Seconds()
		if duration > 0 {
			downloadDelta := downloaded - speedInfo.lastDownloaded
			uploadDelta := uploaded - speedInfo.lastUploaded

			speedInfo.downloadSpeed = float64(downloadDelta) / (1024 * 1024) / duration
			speedInfo.uploadSpeed = float64(uploadDelta) / (1024 * 1024) / duration

			speedInfo.lastDownloaded = downloaded
			speedInfo.lastUploaded = uploaded
			speedInfo.lastTime = now
		}

		// Only persist to DB every 5 ticks to stay under the Clever Cloud
		// 5-connection MySQL limit. In-memory state is always current.
		if !persistDB {
			continue
		}

		// Save updates to database
		var job models.TorrentJob
		if err := db.DB.Where("info_hash = ?", infoHash).First(&job).Error; err == nil {
			job.Name = name
			job.TotalBytes = totalBytes
			job.Downloaded = downloaded + skipped
			job.Uploaded = uploaded
			job.Progress = progress
			job.DownloadSpeed = speedInfo.downloadSpeed
			job.UploadSpeed = speedInfo.uploadSpeed
			job.Peers = peers

			// Update state based on download status
			if job.Status != "paused" && totalBytes > 0 {
				if downloaded+skipped >= totalBytes {
					if job.Status != "seeding" && job.Status != "completed" {
						job.Status = "seeding"
						m.completing[infoHash] = true
						go m.onTorrentCompleted(t, job.InfoHash, job.SaveDirectory)
					}
				} else {
					job.Status = "downloading"

					// Per-file pipeline: as each individual torrent file finishes
					// downloading, launch the inline synchronous S3 archiver so the
					// file is pushed to object storage the instant it completes —
					// BEFORE Clever Cloud can recycle the container and wipe the
					// ephemeral download disk. The archiver uploads → punches the
					// local blocks → chains the Telegram upload, and falls back to
					// a retryable scheduler job only on failure. The in-memory
					// registeredFiles map guards against duplicate submissions
					// within a session.
					if filecore.IsS3Enabled() {
						for _, f := range t.Files() {
							if f.Length() > 0 && f.BytesCompleted() >= f.Length() {
								fileKey := infoHash + ":" + f.Path()
								if !m.registeredFiles[fileKey] {
									m.registeredFiles[fileKey] = true
									// Bounded by archiveSem; never holds the
									// manager mutex, so it cannot deadlock the stats
									// loop that is currently holding it.
									go m.archiveTorrentFileInline(job.InfoHash, job.SaveDirectory, f.Path(), 0)
								}
							}
						}
					}
				}
			}
			db.DB.Save(&job)
		}
	}
}

// AddMagnet adds a torrent via magnet link
func (m *TorrentManager) AddMagnet(uri string, saveDir string, selectFiles bool) (string, error) {
	t, err := m.client.AddMagnet(uri)
	if err != nil {
		return "", err
	}

	m.InjectTrackers(t)
	infoHash := t.InfoHash().HexString()

	if saveDir == "" {
		var dbCfg models.TorrentConfig
		if err := db.DB.First(&dbCfg).Error; err == nil && dbCfg.SaveDirectory != "" {
			saveDir = dbCfg.SaveDirectory
		} else {
			saveDir = "./data/manager/downloads"
		}
	}

	status := "downloading"
	if selectFiles {
		status = "paused"
	}

	job := models.TorrentJob{
		InfoHash:      infoHash,
		Name:          "Fetching metadata...",
		MagnetURI:     uri,
		SaveDirectory: saveDir,
		Status:        status,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// Save or update GORM entry
	if err := db.DB.Save(&job).Error; err != nil {
		return "", err
	}

	// Trigger download
	if selectFiles {
		t.DisallowDataDownload()
	} else {
		t.AllowDataDownload()
		// Pre-download existence check: files already verified on disk or in
		// S3 are skipped instead of re-downloaded (see precheck.go).
		go m.ApplyFilePriorities(t, "", saveDir)
	}

	return infoHash, nil
}

// AddTorrentFile adds a torrent via physical .torrent file
func (m *TorrentManager) AddTorrentFile(torrentPath string, saveDir string, selectFiles bool) (string, error) {
	mi, err := metainfo.LoadFromFile(torrentPath)
	if err != nil {
		return "", err
	}

	t, err := m.client.AddTorrent(mi)
	if err != nil {
		return "", err
	}

	m.InjectTrackers(t)
	infoHash := t.InfoHash().HexString()

	if saveDir == "" {
		var dbCfg models.TorrentConfig
		if err := db.DB.First(&dbCfg).Error; err == nil && dbCfg.SaveDirectory != "" {
			saveDir = dbCfg.SaveDirectory
		} else {
			saveDir = "./data/manager/downloads"
		}
	}

	// Save metadata to persistent configs
	persistentPath := filepath.Join("./data/manager/torrents", infoHash+".torrent")
	if torrentPath != persistentPath {
		_ = copyFile(torrentPath, persistentPath)
	}

	status := "downloading"
	if selectFiles {
		status = "paused"
	}

	job := models.TorrentJob{
		InfoHash:      infoHash,
		Name:          t.Name(),
		TorrentPath:   persistentPath,
		SaveDirectory: saveDir,
		Status:        status,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := db.DB.Save(&job).Error; err != nil {
		return "", err
	}

	if selectFiles {
		t.DisallowDataDownload()
		for _, f := range t.Files() {
			f.Cancel()
		}
	} else {
		t.AllowDataDownload()
		// Pre-download existence check: files already verified on disk or in
		// S3 are skipped instead of re-downloaded (see precheck.go).
		go m.ApplyFilePriorities(t, "", saveDir)
	}

	return infoHash, nil
}

// PauseTorrent cancels piece priority and stops download
func (m *TorrentManager) PauseTorrent(infoHash string) {
	for _, t := range m.client.Torrents() {
		if t.InfoHash().HexString() == infoHash {
			t.DisallowDataDownload()
			db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", infoHash).Update("status", "paused")
			break
		}
	}
}

// ResumeTorrent downloads all files/pieces
func (m *TorrentManager) ResumeTorrent(infoHash string) {
	for _, t := range m.client.Torrents() {
		if t.InfoHash().HexString() == infoHash {
			t.AllowDataDownload()
			db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", infoHash).Update("status", "downloading")
			break
		}
	}
}

// DeleteTorrent deletes the torrent from client, GORM, and optionally deletes actual files
func (m *TorrentManager) DeleteTorrent(infoHash string, deleteFiles bool) {
	for _, t := range m.client.Torrents() {
		if t.InfoHash().HexString() == infoHash {
			t.Drop()
			break
		}
	}

	// Forget any pre-download skip compensation for this torrent.
	m.mu.Lock()
	delete(m.skippedBytes, infoHash)
	m.mu.Unlock()

	var job models.TorrentJob
	if err := db.DB.Where("info_hash = ?", infoHash).First(&job).Error; err == nil {
		if deleteFiles {
			// Delete downloaded files/directory if existing
			dataDir := job.SaveDirectory
			if job.Name != "" {
				targetPath := filepath.Join(dataDir, job.Name)
				_ = os.RemoveAll(targetPath)
			}
		}
		if job.TorrentPath != "" {
			_ = os.Remove(job.TorrentPath)
		}
		db.DB.Delete(&job)

		// When deleting with data, purge any S3 objects and ledger rows too,
		// otherwise the offloaded copies would orphan in the Cellar bucket.
		if deleteFiles {
			_ = storageguard.PurgeTorrentData(infoHash)
		}
	}
}

// Helper to copy files
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Sync()
}

// Client returns the underlying client instance
func (m *TorrentManager) Client() *torrent.Client {
	return m.client
}

// Torrents exposes the live torrent list (storage guard provider bridge).
func (m *TorrentManager) Torrents() []*torrent.Torrent {
	return m.client.Torrents()
}

// TorrentByHash finds a live torrent by its hex info hash (nil if absent).
func (m *TorrentManager) TorrentByHash(infoHash string) *torrent.Torrent {
	for _, t := range m.client.Torrents() {
		if t.InfoHash().HexString() == infoHash {
			return t
		}
	}
	return nil
}

// ApplyFilePriorities parses the selected files JSON string and sets per-file
// download priorities. Before a selected file is marked for download, the
// pre-download existence check (precheckFile) runs: files that already exist
// in a verified-healthy state — on the local disk or in S3 — are skipped, so
// the swarm only ever delivers bytes that are missing or corrupt.
//
// selectedFilesJSON is "" / "null" / unparsable ⇒ every file is wanted.
//
// MUST be called from a goroutine: it waits for metadata and performs disk
// hashing, registry lookups and S3 HEAD requests. It never holds the manager
// mutex for the check itself — only briefly for skippedBytes bookkeeping.
func (m *TorrentManager) ApplyFilePriorities(t *torrent.Torrent, selectedFilesJSON, saveDir string) {
	<-t.GotInfo()

	infoHash := t.InfoHash().HexString()
	files := t.Files()

	allFiles := selectedFilesJSON == "" || selectedFilesJSON == "null"
	var selectedIndices []int
	if !allFiles {
		if err := json.Unmarshal([]byte(selectedFilesJSON), &selectedIndices); err != nil {
			allFiles = true
		}
	}
	selectedIndexMap := make(map[int]bool, len(selectedIndices))
	for _, idx := range selectedIndices {
		selectedIndexMap[idx] = true
	}

	if saveDir == "" {
		saveDir = "./data/manager/downloads"
	}
	absSaveDir, err := filepath.Abs(saveDir)
	if err != nil {
		absSaveDir = saveDir
	}

	// Re-derive the S3-skip compensation from scratch on every application.
	m.mu.Lock()
	m.skippedBytes[infoHash] = 0
	m.mu.Unlock()

	var skippedFromS3 int64
	skippedLocal, skippedS3, toDownload := 0, 0, 0

	for i, f := range files {
		if !allFiles && !selectedIndexMap[i] {
			f.Cancel()
			continue
		}
		switch m.precheckFile(t, f, absSaveDir, infoHash) {
		case precheckSkipS3:
			// Nothing to fetch — the authoritative copy is confirmed in S3.
			f.Cancel()
			skippedFromS3 += f.Length()
			skippedS3++
			// Chain the downstream Telegram upload (dedup-safe), matching
			// what a re-downloaded file would trigger at completion time.
			chainTelegramUploadDirect(filepath.Clean(filepath.Join(absSaveDir, f.Path())), 0, infoHash)
		case precheckSkipLocal:
			// Verified on disk — the per-file completion hooks in
			// updateStats pick it up once BytesCompleted reflects the
			// verified pieces; no priority is needed.
			f.Cancel()
			skippedLocal++
		default:
			f.Download()
			toDownload++
		}
	}

	m.mu.Lock()
	m.skippedBytes[infoHash] = skippedFromS3
	m.mu.Unlock()

	logger.Info("Torrent", "Pre-download check complete",
		"info_hash", infoHash, "files", len(files),
		"skipped_local", skippedLocal, "skipped_s3", skippedS3, "downloading", toDownload)
}

// registerTorrentFile has been replaced by the inline S3 archive pipeline.
// Per-file S3 archiving is now handled synchronously at completion time by
// archiveTorrentFileInline (see pipeline.go), which uploads to S3 → punchHole
// → chains the Telegram upload. The decoupled "torrent_s3_move" scheduler job
// (RunTorrentS3MoveJob) is kept only as a crash-recovery / retry fallback.

// onTorrentCompleted is triggered when a torrent is fully downloaded.
//
// It iterates all torrent files and, for any file not yet archived (not in
// registeredFiles), launches the inline synchronous S3 archiver via
// archiveTorrentFileInline. The archiver uploads the file to S3 the instant it
// completes — BEFORE the ephemeral disk can be wiped by a container recycle —
// then punches the local blocks and chains the Telegram upload. It falls back
// to a retryable torrent_s3_move scheduler job only on upload failure.
//
// Files already handled by the per-file hook in updateStats() (registeredFiles
// guard already set) are skipped to prevent duplicate submissions.
//
// When S3 is disabled, files are registered locally via filecore.RegisterFile
// (original behaviour preserved).
func (m *TorrentManager) onTorrentCompleted(t *torrent.Torrent, infoHash, saveDir string) {
	defer func() {
		m.mu.Lock()
		delete(m.completing, infoHash)
		m.mu.Unlock()
	}()

	// Wait for metadata to resolve.
	select {
	case <-t.GotInfo():
	case <-time.After(10 * time.Second):
		logger.Error("Torrent", "Timeout waiting for metadata on completion", "info_hash", infoHash)
		return
	}

	if saveDir == "" {
		saveDir = "./data/manager/downloads"
	}
	absSaveDir, err := filepath.Abs(saveDir)
	if err != nil {
		absSaveDir = saveDir
	}

	s3Enabled := filecore.IsS3Enabled()
	anyLocalFailed := false // only relevant when S3 is disabled

	for _, f := range t.Files() {
		fileKey := infoHash + ":" + f.Path()

		// Check and set the in-memory guard atomically.
		m.mu.Lock()
		alreadyQueued := m.registeredFiles[fileKey]
		if !alreadyQueued {
			m.registeredFiles[fileKey] = true
		}
		m.mu.Unlock()

		if alreadyQueued {
			// Per-file hook already submitted a torrent_s3_move job for this
			// file. Skip it to avoid duplicates.
			continue
		}

		torrentFilePath := filepath.Clean(filepath.Join(absSaveDir, f.Path()))
		info, err := os.Stat(torrentFilePath)
		if err != nil || info.IsDir() {
			// File missing (already archived by a previous run) or is a
			// directory — skip without re-enabling the guard.
			continue
		}

		if s3Enabled {
			// Inline synchronous S3 archive — see archiveTorrentFileInline.
			// Uploads the file to object storage before the ephemeral disk can
			// be wiped, then punches the local blocks and chains Telegram.
			go m.archiveTorrentFileInline(infoHash, saveDir, f.Path(), 0)
		} else {
			// S3 disabled — register locally (original behaviour).
			logger.Info("Torrent", "Registering completed torrent file in database (S3 disabled)",
				"path", torrentFilePath)
			if _, rErr := filecore.RegisterFile(torrentFilePath, "", "", 0, infoHash); rErr != nil {
				logger.Error("Torrent", "Failed to register completed torrent file",
					"path", torrentFilePath, "error", rErr)
				anyLocalFailed = true
			}
		}
	}

	// When S3 is enabled the scheduler jobs handle status updates as they
	// complete. When S3 is disabled and all local registrations succeeded,
	// mark the torrent as "seeding" (files remain on disk).
	if !s3Enabled && !anyLocalFailed {
		logger.Info("Torrent", "All files registered locally (S3 disabled), staying in seeding mode",
			"info_hash", infoHash)
	}

	// When S3 is enabled the torrent_s3_move jobs will update their own status;
	// we cannot know here whether all jobs will succeed. The torrent stays in
	// "seeding" until the scheduler jobs mark individual files complete.
	if s3Enabled {
		logger.Info("Torrent", "Submitted scheduler jobs for remaining torrent files — scheduler will handle S3+Telegram pipeline",
			"info_hash", infoHash)
	}
}

// FindActiveTorrentFile searches all active torrents for a file with the given absolute path.
func (m *TorrentManager) FindActiveTorrentFile(absolutePath string) (*torrent.File, bool) {
	if m == nil || m.Client() == nil {
		return nil, false
	}

	cleanPath := filecore.GetAbsolutePath(absolutePath)

	// Fetch all jobs to know their save directories
	var jobs []models.TorrentJob
	if err := db.DB.Find(&jobs).Error; err != nil {
		return nil, false
	}

	jobMap := make(map[string]string) // infoHash -> saveDir
	for _, job := range jobs {
		jobMap[job.InfoHash] = job.SaveDirectory
	}

	for _, t := range m.Client().Torrents() {
		infoHash := t.InfoHash().HexString()
		saveDir, ok := jobMap[infoHash]
		if !ok {
			saveDir = "./data/manager/downloads"
		}
		absSaveDir := filecore.GetAbsolutePath(saveDir)

		select {
		case <-t.GotInfo():
			files := t.Files()
			for i := range files {
				torrentFilePath := filecore.GetAbsolutePath(filepath.Join(absSaveDir, files[i].Path()))
				if torrentFilePath == cleanPath {
					return files[i], true
				}
			}
		default:
			// Info not resolved yet
		}
	}
	return nil, false
}
