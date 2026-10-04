package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/models"
	"clever-connect/internal/torrent"

	"github.com/gin-gonic/gin"
)

type TorrentHandler struct {
	cfg *config.Config
}

func NewTorrentHandler(cfg *config.Config) *TorrentHandler {
	return &TorrentHandler{cfg: cfg}
}

func (h *TorrentHandler) proxyToServer(c *gin.Context, method string, apiPath string) bool {
	if h.cfg.AppMode == "server" {
		return false
	}

	if h.cfg.ServerURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No remote server API connection configured (missing SERVER_URL in environment)"})
		return true
	}

	remoteURLTarget := strings.TrimSpace(h.cfg.ServerURL)
	remoteToken := strings.TrimSpace(h.cfg.ServerAuthToken)

	remoteHost := remoteURLTarget
	remoteHost = strings.Replace(remoteHost, "wss://", "https://", 1)
	remoteHost = strings.Replace(remoteHost, "ws://", "http://", 1)

	if idx := strings.Index(remoteHost, "/ws"); idx != -1 {
		remoteHost = remoteHost[:idx]
	}
	if idx := strings.Index(remoteHost, "/tunnel"); idx != -1 {
		remoteHost = remoteHost[:idx]
	}
	remoteHost = strings.TrimSuffix(remoteHost, "/")

	remoteURL := remoteHost + apiPath
	if c.Request.URL.RawQuery != "" {
		remoteURL += "?" + c.Request.URL.RawQuery
	}

	var reqBody io.Reader
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions && method != http.MethodDelete {
		reqBody = c.Request.Body
	}

	req, err := http.NewRequest(method, remoteURL, reqBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create proxy request", "details": err.Error()})
		return true
	}

	for k, vv := range c.Request.Header {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	if remoteToken != "" {
		req.Header.Set("Authorization", "Bearer "+remoteToken)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Remote server connection refused or timed out", "details": err.Error()})
		return true
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Remote server rejected proxy token (401). Please update the remote server or verify your Auth Token."})
		return true
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Writer.Header().Add(k, v)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)

	_, _ = io.Copy(c.Writer, resp.Body)
	return true
}

// ListTorrents returns all active torrent jobs
func (h *TorrentHandler) ListTorrents(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var jobs []models.TorrentJob
	if err := db.DB.Order("created_at desc").Find(&jobs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query torrent database", "details": err.Error()})
		return
	}

	// Populate FileExists for completed or seeding torrents
	for i := range jobs {
		jobs[i].FileExists = true
		if jobs[i].Status == "completed" || jobs[i].Status == "seeding" {
			absSaveDir := filecore.GetAbsoluteSavePath(jobs[i].SaveDirectory)
			destPath := filepath.Join(absSaveDir, jobs[i].Name)
			if _, err := os.Stat(destPath); os.IsNotExist(err) {
				jobs[i].FileExists = false
			}
		}

		// Report whether the torrent's files are archived in S3 object storage
		// (the local copy may have been removed by the stateless move-to-S3 flow).
		// Direct-S3 torrents keep their data directly in object storage.
		var s3Count int64
		db.DB.Model(&models.FileRegistry{}).
			Where("torrent_hash = ? AND s3_key <> ''", jobs[i].InfoHash).
			Count(&s3Count)
		jobs[i].S3Stored = s3Count > 0 || jobs[i].DirectS3
	}

	c.JSON(http.StatusOK, jobs)
}

// AddTorrent adds a torrent by magnet or file upload
func (h *TorrentHandler) AddTorrent(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	// 1. Check for Magnet link first
	var input struct {
		MagnetURI     string `json:"magnet_uri"`
		SaveDirectory string `json:"save_directory"`
		SelectFiles   bool   `json:"select_files"`
	}

	if err := c.ShouldBind(&input); err == nil && input.MagnetURI != "" {
		infoHash, err := torrent.Manager.AddMagnet(input.MagnetURI, input.SaveDirectory, input.SelectFiles)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add magnet link", "details": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "added", "info_hash": infoHash})
		return
	}

	// 2. Check for File Upload (.torrent)
	file, err := c.FormFile("file")
	if err == nil {
		saveDir := c.PostForm("save_directory")
		selectFilesVal := c.PostForm("select_files")
		selectFiles := selectFilesVal == "true"
		
		// Ensure temporary folder exists
		tempDir := "./data/manager/temp"
		_ = os.MkdirAll(tempDir, 0755)

		tempPath := filepath.Join(tempDir, file.Filename)
		if err := c.SaveUploadedFile(file, tempPath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save uploaded file", "details": err.Error()})
			return
		}
		defer os.Remove(tempPath)

		infoHash, err := torrent.Manager.AddTorrentFile(tempPath, saveDir, selectFiles)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load torrent metadata", "details": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "added", "info_hash": infoHash})
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{"error": "Please provide a magnet_uri or upload a .torrent file"})
}

// PauseTorrent halts piece matching for a torrent
func (h *TorrentHandler) PauseTorrent(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var input struct {
		InfoHash string `json:"info_hash" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload", "details": err.Error()})
		return
	}

	torrent.Manager.PauseTorrent(input.InfoHash)
	c.JSON(http.StatusOK, gin.H{"status": "paused", "info_hash": input.InfoHash})
}

// ResumeTorrent downloads all pieces
func (h *TorrentHandler) ResumeTorrent(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var input struct {
		InfoHash string `json:"info_hash" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload", "details": err.Error()})
		return
	}

	torrent.Manager.ResumeTorrent(input.InfoHash)
	c.JSON(http.StatusOK, gin.H{"status": "resumed", "info_hash": input.InfoHash})
}

// DeleteTorrent deletes job metadata and optionally actual files
func (h *TorrentHandler) DeleteTorrent(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var input struct {
		InfoHash    string `json:"info_hash" binding:"required"`
		DeleteFiles bool   `json:"delete_files"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload", "details": err.Error()})
		return
	}

	torrent.Manager.DeleteTorrent(input.InfoHash, input.DeleteFiles)
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "info_hash": input.InfoHash})
}

// ListTorrentFiles returns file list inside a specific torrent
func (h *TorrentHandler) ListTorrentFiles(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	infoHash := c.Query("info_hash")
	if infoHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing info_hash parameter"})
		return
	}

	for _, t := range torrent.Manager.Client().Torrents() {
		if t.InfoHash().HexString() == infoHash {
			select {
			case <-t.GotInfo():
				type fileItem struct {
					Index      int    `json:"index"`
					Path       string `json:"path"`
					Length     int64  `json:"length"`
					Completed  int64  `json:"completed"`
					Percentage float64 `json:"percentage"`
				}

				files := t.Files()
				resp := make([]fileItem, len(files))
				for i, f := range files {
					percentage := 0.0
					if f.Length() > 0 {
						percentage = (float64(f.BytesCompleted()) / float64(f.Length())) * 100.0
					}
					resp[i] = fileItem{
						Index:      i,
						Path:       f.Path(),
						Length:     f.Length(),
						Completed:  f.BytesCompleted(),
						Percentage: percentage,
					}
				}

				c.JSON(http.StatusOK, resp)
				return
			default:
				c.JSON(http.StatusAccepted, gin.H{"status": "fetching_metadata"})
				return
			}
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
}

// SelectTorrentFiles sets which files inside a torrent are scheduled for downloading
func (h *TorrentHandler) SelectTorrentFiles(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var input struct {
		InfoHash      string `json:"info_hash" binding:"required"`
		SelectedFiles []int  `json:"selected_files"` // Indices of files to download
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload", "details": err.Error()})
		return
	}

	for _, t := range torrent.Manager.Client().Torrents() {
		if t.InfoHash().HexString() == input.InfoHash {
			select {
			case <-t.GotInfo():
				// Normalise the selection JSON: nil / failed marshal becomes
				// "[]" (no files) so it is never confused with "all files".
				selBytes, err := json.Marshal(input.SelectedFiles)
				if err != nil || string(selBytes) == "null" {
					selBytes = []byte("[]")
				}

				// Persist file selection to database (restarts re-apply it).
				db.DB.Model(&models.TorrentJob{}).Where("info_hash = ?", input.InfoHash).
					Update("selected_files", string(selBytes))

				// Resolve the save directory for the pre-download existence
				// check (local disk + S3), then apply the priorities —
				// asynchronously, since the check may hash existing files.
				saveDir := ""
				var job models.TorrentJob
				if err := db.DB.Where("info_hash = ?", input.InfoHash).First(&job).Error; err == nil {
					saveDir = job.SaveDirectory
				}
				go torrent.Manager.ApplyFilePriorities(t, string(selBytes), saveDir)

				c.JSON(http.StatusOK, gin.H{"status": "priorities_updated"})
				return
			default:
				c.JSON(http.StatusBadRequest, gin.H{"error": "Torrent metadata is still loading"})
				return
			}
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
}

// GetConfig returns the BitTorrent configurations
func (h *TorrentHandler) GetConfig(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var cfg models.TorrentConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load torrent config", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, cfg)
}

// SaveConfig updates the BitTorrent configurations
func (h *TorrentHandler) SaveConfig(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var input models.TorrentConfig
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid config payload", "details": err.Error()})
		return
	}

	var cfg models.TorrentConfig
	if err := db.DB.First(&cfg).Error; err == nil {
		cfg.SaveDirectory = input.SaveDirectory
		cfg.MaxConnectionsPerTorrent = input.MaxConnectionsPerTorrent
		cfg.MaxHalfOpenConnections = input.MaxHalfOpenConnections
		cfg.UploadLimitMB = input.UploadLimitMB
		cfg.DownloadLimitMB = input.DownloadLimitMB
		cfg.EnableDHT = input.EnableDHT
		cfg.EnablePEX = input.EnablePEX
		cfg.EnableUTP = input.EnableUTP
		cfg.EnableTCP = input.EnableTCP
		cfg.EnableUpload = input.EnableUpload
		cfg.PieceHashersPerTorrent = input.PieceHashersPerTorrent
		cfg.CustomTrackers = input.CustomTrackers
		db.DB.Save(&cfg)
	} else {
		db.DB.Create(&input)
	}

	// Dynamic limits update or full restart
	if torrent.Manager != nil {
		// Try to apply speed limits dynamically without resetting connections
		torrent.Manager.ApplyLimits(input.UploadLimitMB, input.DownloadLimitMB)
		
		// Reinitialize full engine (only if app mode is server)
		if h.cfg.AppMode == "server" {
			if err := torrent.Init(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload torrent client with new configuration", "details": err.Error()})
				return
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "saved"})
}

