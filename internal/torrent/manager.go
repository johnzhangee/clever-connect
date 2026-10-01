package torrent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		stopStats:       make(chan struct{}),
		uploadLimiter:   uploadLimiter,
		downloadLimiter: downloadLimiter,
	}

	// Reload all existing torrent jobs from database
	var jobs []models.TorrentJob
	if err := db.DB.Find(&jobs).Error; err == nil {
		for _, job := range jobs {
			// Fully offloaded torrents live only in S3 — never re-add them to
			// the client, or it would happily re-download every evicted byte.
			if job.OffloadStatus == "offloaded" {
				continue
			}
			if job.MagnetURI != "" {
				t, err := client.AddMagnet(job.MagnetURI)
				if err == nil {
					Manager.InjectTrackers(t)
					if job.Status == "paused" {
						t.DisallowDataDownload()
						go Manager.ApplyFilePriorities(t, job.SelectedFiles)
					} else {
						t.AllowDataDownload()
						go Manager.ApplyFilePriorities(t, job.SelectedFiles)
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
								go Manager.ApplyFilePriorities(t, job.SelectedFiles)
							} else {
								t.AllowDataDownload()
								go Manager.ApplyFilePriorities(t, job.SelectedFiles)
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

	for {
		select {
		case <-m.stopStats:
			return
		case <-ticker.C:
			m.updateStats()
		}
	}
}

// updateStats recalculates progress, seeds/peers, and delta upload/download speeds
func (m *TorrentManager) updateStats() {
	m.mu.Lock()
	defer m.mu.Unlock()

	torrents := m.client.Torrents()
	now := time.Now()

	for _, t := range torrents {
		infoHash := t.InfoHash().HexString()

		var totalBytes int64
		var downloaded int64
		var uploaded int64
		var peers int
		var progress float64
		var name string

		select {
		case <-t.GotInfo():
			// Metainfo resolved
			totalBytes = t.Length()
			downloaded = t.BytesCompleted()
			stats := t.Stats()
			uploaded = stats.BytesWritten.Int64()
			peers = stats.ActivePeers
			name = t.Name()
			if totalBytes > 0 {
				progress = (float64(downloaded) / float64(totalBytes)) * 100.0
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

		// Save updates to database
		var job models.TorrentJob
		if err := db.DB.Where("info_hash = ?", infoHash).First(&job).Error; err == nil {
			job.Name = name
			job.TotalBytes = totalBytes
			job.Downloaded = downloaded
			job.Uploaded = uploaded
			job.Progress = progress
			job.DownloadSpeed = speedInfo.downloadSpeed
			job.UploadSpeed = speedInfo.uploadSpeed
			job.Peers = peers

			// Update state based on download status
			if job.Status != "paused" && totalBytes > 0 {
				if downloaded >= totalBytes {
					if job.Status != "seeding" && job.Status != "completed" {
						job.Status = "seeding"
						go m.onTorrentCompleted(t, &job)
					}
				} else {
					job.Status = "downloading"
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
		t.DownloadAll()
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
		t.DownloadAll()
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

// ApplyFilePriorities parses the selected files JSON string and sets the torrent file priorities
func (m *TorrentManager) ApplyFilePriorities(t *torrent.Torrent, selectedFilesJSON string) {
	<-t.GotInfo()

	if selectedFilesJSON == "" || selectedFilesJSON == "null" {
		t.DownloadAll()
		return
	}

	var selectedIndices []int
	if err := json.Unmarshal([]byte(selectedFilesJSON), &selectedIndices); err != nil {
		t.DownloadAll()
		return
	}

	files := t.Files()
	selectedIndexMap := make(map[int]bool)
	for _, idx := range selectedIndices {
		selectedIndexMap[idx] = true
	}

	for i, f := range files {
		if selectedIndexMap[i] {
			f.Download()
		} else {
			f.Cancel()
		}
	}
}

// onTorrentCompleted is triggered when a torrent is fully downloaded.
// It iterates over all torrent files and registers them with FileCore.
func (m *TorrentManager) onTorrentCompleted(t *torrent.Torrent, job *models.TorrentJob) {
	// Wait for metadata to resolve
	select {
	case <-t.GotInfo():
	case <-time.After(10 * time.Second):
		logger.Error("Torrent", "Timeout waiting for metadata on completion", "info_hash", job.InfoHash)
		return
	}

	saveDir := job.SaveDirectory
	if saveDir == "" {
		saveDir = "./data/manager/downloads"
	}
	absSaveDir, err := filepath.Abs(saveDir)
	if err != nil {
		absSaveDir = saveDir
	}

	for _, f := range t.Files() {
		torrentFilePath := filepath.Clean(filepath.Join(absSaveDir, f.Path()))
		if info, err := os.Stat(torrentFilePath); err == nil && !info.IsDir() {
			logger.Info("Torrent", "Registering completed torrent file in database", "path", torrentFilePath)
			_, err = filecore.RegisterFile(torrentFilePath, "", "", 0, job.InfoHash)
			if err != nil {
				logger.Error("Torrent", "Failed to register completed torrent file", "path", torrentFilePath, "error", err)
			}
		}
	}
}
