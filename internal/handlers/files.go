package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/scheduler"
	"clever-connect/internal/storageguard"
	"clever-connect/internal/torrent"
	anacrolixTorrent "github.com/anacrolix/torrent"
	"github.com/gin-gonic/gin"
)

type FileItem struct {
	Name      string    `json:"name"`
	IsDir     bool      `json:"is_dir"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Extension string    `json:"extension"`
	InS3      bool      `json:"in_s3"` // backed by the Cellar bucket (streamable)
}

type FileHandler struct {
	cfg     *config.Config
	rootDir string
}

func NewFileHandler(cfg *config.Config) *FileHandler {
	rootDir, err := filepath.Abs("./data/manager")
	if err != nil {
		rootDir = "./data/manager"
	}
	// Ensure the root path exists
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		logger.Error("Files", "Failed to create root directory", "error", err)
	}

	logger.Info("Files", "Initialized file manager base directory", "rootDir", rootDir)
	return &FileHandler{
		cfg:     cfg,
		rootDir: rootDir,
	}
}

// securePath guarantees that no user can bypass the sandbox rootDir boundary
func (h *FileHandler) securePath(requestedPath string) (string, error) {
	// Ensure absolute root format in local context
	cleanRel := filepath.Clean("/" + requestedPath)
	fullPath := filepath.Clean(filepath.Join(h.rootDir, cleanRel))

	// Guard against directory traversal attacks
	if !strings.HasPrefix(fullPath, h.rootDir) {
		return "", os.ErrPermission
	}
	return fullPath, nil
}

func (h *FileHandler) proxyToServer(c *gin.Context, method string, apiPath string) bool {
	if h.cfg.AppMode == "server" {
		return false
	}

	if h.cfg.ServerURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No remote server API connection configured (missing SERVER_URL in environment)"})
		return true
	}

	remoteURLTarget := strings.TrimSpace(h.cfg.ServerURL)
	remoteToken := strings.TrimSpace(h.cfg.ServerAuthToken)

	// Convert ws/wss to http/https
	remoteHost := remoteURLTarget
	remoteHost = strings.Replace(remoteHost, "wss://", "https://", 1)
	remoteHost = strings.Replace(remoteHost, "ws://", "http://", 1)

	// Strip trailing path segments like /ws or /tunnel
	if idx := strings.Index(remoteHost, "/ws"); idx != -1 {
		remoteHost = remoteHost[:idx]
	}
	if idx := strings.Index(remoteHost, "/tunnel"); idx != -1 {
		remoteHost = remoteHost[:idx]
	}
	// Strip trailing slashes
	remoteHost = strings.TrimSuffix(remoteHost, "/")

	// Build remote URL
	remoteURL := remoteHost + apiPath
	if c.Request.URL.RawQuery != "" {
		remoteURL += "?" + c.Request.URL.RawQuery
	}

	var reqBody io.Reader
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions && method != http.MethodDelete {
		reqBody = c.Request.Body
	}

	// Create proxy request
	req, err := http.NewRequest(method, remoteURL, reqBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create proxy request", "details": err.Error()})
		return true
	}

	// Copy original request headers
	for k, vv := range c.Request.Header {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	// Overwrite local credentials with the actual remote server's Ehco client auth_token!
	if remoteToken != "" {
		req.Header.Set("Authorization", "Bearer " + remoteToken)
	}

	// Execute proxy request to remote server
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

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Writer.Header().Add(k, v)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)

	// Pipe remote file stream/content back directly
	_, _ = io.Copy(c.Writer, resp.Body)
	return true
}

// getDiskInfo queries the file system statistics using syscall.Statfs.
func getDiskInfo(path string) (total uint64, free uint64, used uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err == nil {
		total = stat.Blocks * uint64(stat.Bsize)
		free = stat.Bfree * uint64(stat.Bsize)
		used = total - free
	}
	return
}

func (h *FileHandler) findActiveTorrentFile(absolutePath string) (*anacrolixTorrent.File, bool) {
	if torrent.Manager == nil || torrent.Manager.Client() == nil {
		return nil, false
	}

	cleanPath := filepath.Clean(absolutePath)

	// Fetch all jobs to know their save directories
	var jobs []models.TorrentJob
	if err := db.DB.Find(&jobs).Error; err != nil {
		return nil, false
	}

	jobMap := make(map[string]string) // infoHash -> saveDir
	for _, job := range jobs {
		jobMap[job.InfoHash] = job.SaveDirectory
	}

	for _, t := range torrent.Manager.Client().Torrents() {
		infoHash := t.InfoHash().HexString()
		saveDir, ok := jobMap[infoHash]
		if !ok {
			saveDir = "./data/manager/downloads"
		}
		absSaveDir, err := filepath.Abs(saveDir)
		if err != nil {
			absSaveDir = saveDir
		}

		select {
		case <-t.GotInfo():
			files := t.Files()
			for i := range files {
				torrentFilePath := filepath.Clean(filepath.Join(absSaveDir, files[i].Path()))
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

// ListDirectory handles GET /api/files/list
func (h *FileHandler) ListDirectory(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	reqPath := c.DefaultQuery("path", "")
	safePath, err := h.securePath(reqPath)
	if err != nil {
		logger.Warn("Files", "Access denied — directory traversal attempt detected", "path", reqPath, "ip", c.ClientIP())
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	entries, err := os.ReadDir(safePath)
	if err != nil && !os.IsNotExist(err) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read directory", "details": err.Error()})
		return
	}

	files := make([]FileItem, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, FileItem{
			Name:      entry.Name(),
			IsDir:     entry.IsDir(),
			Size:      info.Size(),
			ModTime:   info.ModTime(),
			Extension: filepath.Ext(entry.Name()),
		})
	}

	// Merge in virtual files for active torrents that should be in this folder
	virtualFiles := make(map[string]FileItem)
	if torrent.Manager != nil && torrent.Manager.Client() != nil {
		var jobs []models.TorrentJob
		if err := db.DB.Find(&jobs).Error; err == nil {
			jobMap := make(map[string]string)
			for _, job := range jobs {
				jobMap[job.InfoHash] = job.SaveDirectory
			}

			for _, t := range torrent.Manager.Client().Torrents() {
				infoHash := t.InfoHash().HexString()
				saveDir, ok := jobMap[infoHash]
				if !ok {
					saveDir = "./data/manager/downloads"
				}
				absSaveDir, err := filepath.Abs(saveDir)
				if err != nil {
					absSaveDir = saveDir
				}

				select {
				case <-t.GotInfo():
					for _, f := range t.Files() {
						torrentFilePath := filepath.Clean(filepath.Join(absSaveDir, f.Path()))
						parentDir := filepath.Dir(torrentFilePath)

						if parentDir == safePath {
							name := filepath.Base(torrentFilePath)
							virtualFiles[name] = FileItem{
								Name:      name,
								IsDir:     false,
								Size:      f.Length(),
								ModTime:   time.Now(),
								Extension: filepath.Ext(name),
							}
						} else if strings.HasPrefix(parentDir, safePath) {
							rel, err := filepath.Rel(safePath, parentDir)
							if err == nil && rel != "." && rel != ".." {
								parts := strings.Split(filepath.ToSlash(rel), "/")
								if len(parts) > 0 && parts[0] != "" {
									dirName := parts[0]
									virtualFiles[dirName] = FileItem{
										Name:      dirName,
										IsDir:     true,
										Size:      0,
										ModTime:   time.Now(),
										Extension: "",
									}
								}
							}
						}
					}
				default:
				}
			}
		}
	}

	// Merge virtual files with physical ones
	// Also surface files that exist ONLY in S3 (offloaded + evicted by the
	// storage guard) so they remain browsable — they stream from Cellar.
	if storageguard.Enabled() {
		var offloaded []models.TorrentFileOffload
		if err := db.DB.Where("uploaded = ? AND evicted_local = ?", true, true).
			Find(&offloaded).Error; err == nil {
			for _, row := range offloaded {
				rowPath := filepath.Clean(row.FilePath)
				parentDir := filepath.Dir(rowPath)
				name := filepath.Base(rowPath)

				if parentDir == safePath {
					if existing, ok := virtualFiles[name]; ok {
						existing.InS3 = true
						virtualFiles[name] = existing
						continue
					}
					virtualFiles[name] = FileItem{
						Name:      name,
						IsDir:     false,
						Size:      row.Size,
						ModTime:   row.UpdatedAt,
						Extension: filepath.Ext(name),
						InS3:      true,
					}
				} else if strings.HasPrefix(parentDir, safePath+string(filepath.Separator)) {
					if rel, relErr := filepath.Rel(safePath, parentDir); relErr == nil && rel != "." && rel != ".." {
						parts := strings.Split(filepath.ToSlash(rel), "/")
						if len(parts) > 0 && parts[0] != "" {
							dirName := parts[0]
							if existing, ok := virtualFiles[dirName]; ok {
								existing.InS3 = true
								virtualFiles[dirName] = existing
							} else {
								virtualFiles[dirName] = FileItem{
									Name:   dirName,
									IsDir:  true,
									ModTime: time.Now(),
									InS3:   true,
								}
							}
						}
					}
				}
			}
		}
	}

	// Merge virtual files with physical ones
	for _, vf := range virtualFiles {
		foundIdx := -1
		for idx, pf := range files {
			if pf.Name == vf.Name {
				foundIdx = idx
				break
			}
		}

		if foundIdx != -1 {
			if !files[foundIdx].IsDir && vf.Size > files[foundIdx].Size {
				files[foundIdx].Size = vf.Size
			}
		} else {
			files = append(files, vf)
		}
	}

	// Clean standard absolute visual path for display
	displayPath := filepath.Clean("/" + reqPath)
	if displayPath == "." {
		displayPath = "/"
	}

	diskTotal, diskFree, diskUsed := getDiskInfo(h.rootDir)

	c.JSON(http.StatusOK, gin.H{
		"current_path": displayPath,
		"files":        files,
		"disk_total":   diskTotal,
		"disk_free":    diskFree,
		"disk_used":    diskUsed,
	})
}

// SearchFiles handles GET /api/files/search
func (h *FileHandler) SearchFiles(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	reqPath := c.DefaultQuery("path", "")
	query := c.DefaultQuery("q", "")

	if len(query) <= 3 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Query must be more than 3 characters"})
		return
	}

	safePath, err := h.securePath(reqPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	results := make([]gin.H, 0)
	limit := 100

	err = filepath.WalkDir(safePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(results) >= limit {
			return filepath.SkipDir
		}
		name := d.Name()
		if strings.Contains(strings.ToLower(name), strings.ToLower(query)) {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			relPath, err := filepath.Rel(h.rootDir, path)
			if err != nil {
				relPath = name
			}
			relPath = "/" + filepath.ToSlash(relPath)
			results = append(results, gin.H{
				"name":      name,
				"is_dir":    d.IsDir(),
				"size":      info.Size(),
				"mod_time":  info.ModTime(),
				"extension": filepath.Ext(name),
				"path":      relPath,
			})
		}
		return nil
	})

	if err != nil && err != filepath.SkipDir {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Search failed", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, results)
}

// StreamOrDownload handles GET /api/files/stream
// Crucial: Automatically handles HTTP Range headers (HTTP 206 Partial Content)
// for HLS/MP4 video streaming seeking and multi-connection fast download engines!
func (h *FileHandler) StreamOrDownload(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	target := c.Query("path")
	safePath, err := h.securePath(target)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	// Set high-performance HTTP streaming headers
	c.Header("Accept-Ranges", "bytes")
	c.Header("Connection", "keep-alive")
	c.Header("Cache-Control", "public, max-age=3600")

	// 1. Check if the file is part of an active torrent and is still downloading!
	if tFile, found := h.findActiveTorrentFile(safePath); found {
		if tFile.BytesCompleted() < tFile.Length() {
			if c.Query("download") == "true" {
				c.Header("Content-Disposition", "attachment; filename=\""+filepath.Base(safePath)+"\"")
			}

			reader := tFile.NewReader()
			reader.SetReadahead(32 * 1024 * 1024) // 32MB aggressive read-ahead buffer for zero stuttering
			defer reader.Close()

			// Stream content using the torrent client's reader
			http.ServeContent(c.Writer, c.Request, filepath.Base(safePath), time.Now(), reader)
			return
		}
	}

	// 2. Fall back to standard disk file streaming (either non-torrent file, or fully completed torrent file).
	//    When the local copy was evicted by the storage guard but a Cellar
	//    backup exists, stream it straight from S3 with full Range support.
	file, err := os.Open(safePath)
	if err != nil {
		if storageguard.Enabled() {
			if reader, _, serr := storageguard.OpenS3Reader(safePath); serr == nil {
				if c.Query("download") == "true" {
					c.Header("Content-Disposition", "attachment; filename=\""+filepath.Base(safePath)+"\"")
				}
				http.ServeContent(c.Writer, c.Request, filepath.Base(safePath), time.Time{}, reader)
				_ = reader.Close()
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request target"})
		return
	}

	// Forces browser to download instead of streaming if download query is specified
	if c.Query("download") == "true" {
		c.Header("Content-Disposition", "attachment; filename=\""+filepath.Base(safePath)+"\"")
	}

	// Using the optimal standard http.ServeContent seeking framework
	http.ServeContent(c.Writer, c.Request, stat.Name(), stat.ModTime(), file)
}

// RawDownload handles GET /api/files/download
// It serves the file aggressively from disk directly without any stream mechanisms (no HTTP Range support, no torrent reader readahead, etc.)
func (h *FileHandler) RawDownload(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	target := c.Query("path")
	safePath, err := h.securePath(target)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	file, err := os.Open(safePath)
	if err != nil {
		// The local copy may have been evicted to S3 by the storage guard —
		// stream the raw bytes from Cellar instead of failing with 404.
		if storageguard.Enabled() {
			if reader, size, serr := storageguard.OpenS3Reader(safePath); serr == nil {
				defer func() { _ = reader.Close() }()
				c.Header("Content-Description", "File Transfer")
				c.Header("Content-Type", "application/octet-stream")
				c.Header("Content-Disposition", "attachment; filename=\""+filepath.Base(safePath)+"\"")
				c.Header("Content-Length", fmt.Sprintf("%d", size))
				c.Header("Expires", "0")
				c.Header("Cache-Control", "must-revalidate")
				c.Header("Pragma", "public")
				_, _ = io.Copy(c.Writer, reader)
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request target"})
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename=\""+filepath.Base(safePath)+"\"")
	c.Header("Content-Length", fmt.Sprintf("%d", stat.Size()))
	c.Header("Expires", "0")
	c.Header("Cache-Control", "must-revalidate")
	c.Header("Pragma", "public")

	// Read and write directly to response writer aggressively (without range/stream support)
	_, _ = io.Copy(c.Writer, file)
}

// GetContent handles GET /api/files/content for text editor integrations
func (h *FileHandler) GetContent(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	target := c.Query("path")
	safePath, err := h.securePath(target)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	stat, err := os.Stat(safePath)
	if err != nil || stat.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid target path"})
		return
	}

	// Prevent reading huge files into memory (max 10MB edit limit)
	if stat.Size() > 10*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 10MB limit"})
		return
	}

	contentBytes, err := os.ReadFile(safePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"content": string(contentBytes),
	})
}

// SaveContent handles POST /api/files/save to write changes back from text editors
func (h *FileHandler) SaveContent(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		Path    string `json:"path" binding:"required"`
		Content string `json:"content"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	safePath, err := h.securePath(req.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	// Ensure it's a file, not a directory
	stat, err := os.Stat(safePath)
	if err == nil && stat.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot overwrite directory with text file content"})
		return
	}

	if err := os.WriteFile(safePath, []byte(req.Content), 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file", "details": err.Error()})
		return
	}

	logger.Info("Files", "File content updated successfully", "path", req.Path, "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "File saved successfully"})
}

// CreateFolder handles POST /api/files/create-folder
func (h *FileHandler) CreateFolder(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		ParentPath string `json:"parent_path"`
		FolderName string `json:"folder_name" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// Sanitize parent path and target folder name
	targetPath := filepath.Join(req.ParentPath, req.FolderName)
	safePath, err := h.securePath(targetPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	if err := os.MkdirAll(safePath, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create directory", "details": err.Error()})
		return
	}

	logger.Info("Files", "Directory created successfully", "path", targetPath, "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Folder created successfully"})
}

// DeleteItem handles POST /api/files/delete
func (h *FileHandler) DeleteItem(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		Path string `json:"path" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// Prevent deleting the root directory
	if req.Path == "" || req.Path == "/" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot delete root directory"})
		return
	}

	safePath, err := h.securePath(req.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	if err := os.RemoveAll(safePath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete item", "details": err.Error()})
		return
	}

	// Also remove any Cellar objects / ledger rows tied to this path so
	// evicted copies do not linger in the bucket (or ghost into listings).
	if storageguard.Enabled() {
		go storageguard.PurgeLocalPathFromS3(safePath)
	}

	logger.Info("Files", "File system item deleted successfully", "path", req.Path, "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Item deleted successfully"})
}

// UploadFile handles POST /api/files/upload
func (h *FileHandler) UploadFile(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	targetFolder := c.PostForm("path")
	safeFolder, err := h.securePath(targetFolder)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing file form payload"})
		return
	}

	// Ensure the base directory exists
	_ = os.MkdirAll(safeFolder, 0755)

	// Combine to build absolute local path
	filename := filepath.Base(file.Filename)
	safeFilePath := filepath.Join(safeFolder, filename)

	if err := c.SaveUploadedFile(file, safeFilePath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write file on disk", "details": err.Error()})
		return
	}

	// Register uploaded file (deduplicates automatically if already existing)
	if _, err := filecore.RegisterFile(safeFilePath, "", "", 0, ""); err != nil {
		logger.Error("Files", "Failed to register uploaded file in registry", "path", safeFilePath, "error", err)
	}

	logger.Info("Files", "File uploaded successfully", "folder", targetFolder, "filename", filename, "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "File uploaded successfully"})
}

// copyFile copies a single file from src to dst.
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

	if _, err = io.Copy(out, in); err != nil {
		return err
	}

	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, si.Mode())
}

// copyDir recursively copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	si, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err = os.MkdirAll(dst, si.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err = copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err = copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// MoveItem handles POST /api/files/move for renaming and moving
func (h *FileHandler) MoveItem(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		SrcPath string `json:"src_path" binding:"required"`
		DstPath string `json:"dst_path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	safeSrc, err := h.securePath(req.SrcPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}
	safeDst, err := h.securePath(req.DstPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}
	// Ensure parent dir of destination exists
	if err := os.MkdirAll(filepath.Dir(safeDst), 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create destination parent folder"})
		return
	}
	if err := os.Rename(safeSrc, safeDst); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to move item", "details": err.Error()})
		return
	}
	logger.Info("Files", "Item moved/renamed successfully", "src", req.SrcPath, "dst", req.DstPath)
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Item moved successfully"})
}

// CopyItem handles POST /api/files/copy for duplicating items
func (h *FileHandler) CopyItem(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		SrcPath string `json:"src_path" binding:"required"`
		DstPath string `json:"dst_path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	safeSrc, err := h.securePath(req.SrcPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}
	safeDst, err := h.securePath(req.DstPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}
	stat, err := os.Stat(safeSrc)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source item not found"})
		return
	}
	if stat.IsDir() {
		err = copyDir(safeSrc, safeDst)
	} else {
		err = copyFile(safeSrc, safeDst)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to copy item", "details": err.Error()})
		return
	}
	logger.Info("Files", "Item copied successfully", "src", req.SrcPath, "dst", req.DstPath)
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Item copied successfully"})
}

// CompressItems handles POST /api/files/compress to ZIP multiple files/directories
func (h *FileHandler) CompressItems(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		ParentPath string   `json:"parent_path"`
		Items      []string `json:"items" binding:"required"`
		ZipName    string   `json:"zip_name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	var absolutePaths []string
	for _, item := range req.Items {
		itemPath := filepath.Join(req.ParentPath, item)
		safeItemPath, err := h.securePath(itemPath)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied for: " + item})
			return
		}
		absolutePaths = append(absolutePaths, safeItemPath)
	}

	destName := req.ZipName
	if filepath.Ext(destName) == "" {
		destName = destName + ".zip"
	}

	payloadObj := struct {
		Files    []string `json:"files"`
		DestName string   `json:"dest_name"`
	}{
		Files:    absolutePaths,
		DestName: destName,
	}

	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal payload"})
		return
	}

	job, err := scheduler.Engine.SubmitJob(
		"file_compress",
		fmt.Sprintf("Compress %d items", len(absolutePaths)),
		fmt.Sprintf("Compressing %s to %s/Compressed", strings.Join(req.Items, ", "), h.rootDir),
		"files",
		5,
		string(payloadBytes),
		"",
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to submit compress job", "details": err.Error()})
		return
	}

	logger.Info("Files", "Submitted compression job successfully", "jobID", job.ID, "zipName", destName)
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Compression job queued successfully",
		"job_id":  job.ID,
	})
}

// DecompressItem handles POST /api/files/decompress to extract ZIP/TAR/RAR/7Z archives
func (h *FileHandler) DecompressItem(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		Path     string `json:"path" binding:"required"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	safePath, err := h.securePath(req.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	info, err := os.Stat(safePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Archive file not found"})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Target is a directory, not an archive file"})
		return
	}

	// Verify if archive requires password
	requiresPassword, err := filecore.IsArchivePasswordProtected(safePath)
	if err == nil && requiresPassword && req.Password == "" {
		c.JSON(http.StatusOK, gin.H{
			"status":            "password_required",
			"message":           "Archive is password-protected",
			"requires_password": true,
		})
		return
	}

	payload := safePath
	if req.Password != "" {
		pData, _ := json.Marshal(map[string]string{
			"path":     safePath,
			"password": req.Password,
		})
		payload = string(pData)
	}

	job, err := scheduler.Engine.SubmitJob(
		"file_decompress",
		fmt.Sprintf("Decompress %s", filepath.Base(safePath)),
		fmt.Sprintf("Extracting %s near the archive file", filepath.Base(safePath)),
		"files",
		5,
		payload,
		"",
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to submit decompress job", "details": err.Error()})
		return
	}

	logger.Info("Files", "Submitted decompression job successfully", "jobID", job.ID, "path", req.Path)
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Decompression job queued successfully",
		"job_id":  job.ID,
	})
}
