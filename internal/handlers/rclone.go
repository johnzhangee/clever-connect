package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/rclone"
	"clever-connect/internal/scheduler"

	"github.com/gin-gonic/gin"
)

// ──────────────────────────────────────────────────────────────────────────────
// Universal Cloud Storage (rclone) REST API Handler
// ──────────────────────────────────────────────────────────────────────────────

// RcloneHandler exposes the multi-provider cloud storage engine: engine status,
// binary self-provisioning, provider metadata, remote CRUD + connectivity
// tests, remote browsing, and the asynchronous upload pipeline.
type RcloneHandler struct {
	cfg     *config.Config
	rootDir string
}

func NewRcloneHandler(cfg *config.Config) *RcloneHandler {
	rootDir, err := filepath.Abs("./data/manager")
	if err != nil {
		rootDir = "./data/manager"
	}
	return &RcloneHandler{cfg: cfg, rootDir: rootDir}
}

// securePath mirrors FileHandler.securePath — no user can escape the sandbox.
func (h *RcloneHandler) securePath(requestedPath string) (string, error) {
	fullPath := filecore.GetAbsolutePath(requestedPath)
	if !strings.HasPrefix(fullPath, h.rootDir) {
		return "", os.ErrPermission
	}
	return fullPath, nil
}

// proxyToServer automatically forwards requests from the Client Panel to the
// remote Clever Cloud server (identical to the SchedulerHandler flow).
func (h *RcloneHandler) proxyToServer(c *gin.Context, method string, apiPath string) bool {
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
	remoteHost = strings.ReplaceAll(remoteHost, "wss://", "https://")
	remoteHost = strings.ReplaceAll(remoteHost, "ws://", "http://")
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

// maskOptions replaces credential values with SecretMarker, leaving the rest
// of the option map intact for form re-population.
func (h *RcloneHandler) maskOptions(remote *models.RcloneRemote) (map[string]string, error) {
	opts, err := rclone.ParseOptions(remote)
	if err != nil {
		return nil, err
	}
	secrets := map[string]bool{}
	for _, f := range remote.SecretFields {
		secrets[strings.ToLower(f)] = true
	}
	masked := make(map[string]string, len(opts))
	for k, v := range opts {
		if secrets[strings.ToLower(k)] {
			masked[k] = rclone.SecretMarker
		} else {
			masked[k] = v
		}
	}
	return masked, nil
}

// remoteResponse shapes the JSON sent for one remote in list endpoints.
func (h *RcloneHandler) remoteResponse(remote *models.RcloneRemote) gin.H {
	maskedOptions, err := h.maskOptions(remote)
	if err != nil {
		maskedOptions = map[string]string{}
	}
	fileCount, lastSync := rclone.RemoteFileStats(remote.ID)
	return gin.H{
		"id":              remote.ID,
		"name":            remote.Name,
		"type":            remote.Type,
		"root_prefix":     remote.RootPrefix,
		"extra_flags":     remote.ExtraFlags,
		"enabled":         remote.Enabled,
		"options":         maskedOptions,
		"secret_fields":   remote.SecretFields,
		"last_test_ok":    remote.LastTestOk,
		"last_test_error": remote.LastTestError,
		"last_test_at":    remote.LastTestAt,
		"file_count":      fileCount,
		"last_sync_at":    lastSync,
		"created_at":      remote.CreatedAt,
		"updated_at":      remote.UpdatedAt,
	}
}

// GetStatus handles GET /api/rclone/status — engine banner data for the UI.
func (h *RcloneHandler) GetStatus(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}

	var remoteCount, uploadCount, fileCount int64
	db.DB.Model(&models.RcloneRemote{}).Count(&remoteCount)
	db.DB.Model(&models.RcloneUpload{}).Count(&uploadCount)
	db.DB.Model(&models.RcloneFile{}).Count(&fileCount)

	envPath := strings.TrimSpace(os.Getenv(rclone.BinEnvVar))
	sys, sysErr := rclone.System()
	autoInstall := false
	if sysErr == nil && sys != nil {
		autoInstall = sys.AutoInstall
	}

	binaryPath, binaryVersion, binarySource := "", "", "missing"
	binaryReady := false
	if path, err := rclone.ResolveBinary(); err == nil {
		binaryPath, binaryReady = path, true
		if ver, vErr := rclone.Version(); vErr == nil {
			binaryVersion = ver
		}
		switch {
		case envPath != "" && path == envPath:
			binarySource = "env"
		case sys != nil && sys.ManagedBinary != "" && path == sys.ManagedBinary:
			binarySource = "managed"
		default:
			binarySource = "path"
		}
	}

	providerCount := 0
	if provs, err := rclone.Providers(c.Request.Context(), false); err == nil {
		providerCount = len(provs)
	}

	c.JSON(http.StatusOK, gin.H{
		"binary_ready":   binaryReady,
		"binary_path":    binaryPath,
		"binary_version": binaryVersion,
		"binary_source":  binarySource,
		"auto_install":   autoInstall,
		"system_ok":      sysErr == nil,
		"remote_count":   remoteCount,
		"upload_count":   uploadCount,
		"file_count":     fileCount,
		"provider_count": providerCount,
		"secret_marker":  rclone.SecretMarker,
	})
}

// UpdateSystem handles POST /api/rclone/system — engine settings (auto-install).
func (h *RcloneHandler) UpdateSystem(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		AutoInstall *bool `json:"auto_install"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload", "details": err.Error()})
		return
	}

	sys, err := rclone.System()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load engine settings", "details": err.Error()})
		return
	}
	if req.AutoInstall != nil {
		sys.AutoInstall = *req.AutoInstall
		if err := db.DB.Model(sys).Update("auto_install", sys.AutoInstall).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save engine settings", "details": err.Error()})
			return
		}
	}
	logger.Info("Rclone", "Engine settings updated", "auto_install", sys.AutoInstall, "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "auto_install": sys.AutoInstall})
}

// InstallBinary handles POST /api/rclone/install — self-provision the engine
// binary from downloads.rclone.org (SHA256-verified, in-memory extraction).
func (h *RcloneHandler) InstallBinary(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		Force bool `json:"force"`
	}
	_ = c.ShouldBindJSON(&req) // body optional

	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()

	path, err := rclone.InstallManaged(ctx, req.Force)
	if err != nil {
		logger.Warn("Rclone", "Binary install failed", "error", err, "ip", c.ClientIP())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to install rclone binary", "details": err.Error()})
		return
	}
	version, _ := rclone.Version()
	logger.Info("Rclone", "Binary installed via admin panel", "path", path, "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "rclone engine binary installed successfully",
		"path":    path,
		"version": version,
	})
}

// providerOptionOut is the trimmed option shape the wizard's dynamic form needs.
type providerOptionOut struct {
	Name       string           `json:"name"`
	Help       string           `json:"help"`
	Required   bool             `json:"required"`
	IsPassword bool             `json:"is_password"`
	Advanced   bool             `json:"advanced"`
	HasDefault bool             `json:"has_default"`
	Default    string           `json:"default,omitempty"`
	// No omitempty: the wizard form reads examples.length and crashes when
	// the key is absent. Empty examples must serialize as [].
	Examples   []rclone.Example `json:"examples"`
}

// providerOut is the trimmed provider shape for the wizard's provider picker.
type providerOut struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	HasOAuth    bool                `json:"has_oauth"`
	Options     []providerOptionOut `json:"options"`
}

// GetProviders handles GET /api/rclone/providers?refresh=1 — backend metadata
// driving the add-remote wizard's dynamic per-provider form.
func (h *RcloneHandler) GetProviders(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	force := c.Query("refresh") == "1" || strings.EqualFold(c.Query("refresh"), "true")

	provs, err := rclone.Providers(c.Request.Context(), force)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cloud storage engine binary unavailable", "details": err.Error()})
		return
	}

	out := make([]providerOut, 0, len(provs))
	for _, p := range provs {
		opts := make([]providerOptionOut, 0, len(p.Options))
		for _, o := range p.Options {
			def := ""
			hasDef := false
			if len(o.Default) > 0 {
				hasDef = true
				_ = json.Unmarshal(o.Default, &def)
			}
			// Belt and braces: never hand the API a nil Examples slice —
			// without omitempty it would serialize as null, and the form
			// reads .length on it.
			ex := o.Examples
			if ex == nil {
				ex = []rclone.Example{}
			}
			opts = append(opts, providerOptionOut{
				Name:       o.Name,
				Help:       o.Help,
				Required:   o.Required,
				IsPassword: o.IsPassword,
				Advanced:   o.Advanced,
				HasDefault: hasDef,
				Default:    def,
				Examples:   ex,
			})
		}
		out = append(out, providerOut{Name: p.Name, Description: p.Description, HasOAuth: p.HasOAuth, Options: opts})
	}

	c.JSON(http.StatusOK, gin.H{"providers": out, "count": len(out)})
}

// remoteRequest is the create/update payload shape for remotes.
type remoteRequest struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	RootPrefix string            `json:"root_prefix"`
	ExtraFlags string            `json:"extra_flags"`
	Enabled    *bool             `json:"enabled"`
	Options    map[string]string `json:"options"`
}

// buildRemote merges an incoming request into a remote row: masked secrets are
// preserved instead of being overwritten, name changes verify uniqueness, and
// options re-serialize into the stored JSON string.
func (h *RcloneHandler) buildRemote(req *remoteRequest, existing *models.RcloneRemote) (*models.RcloneRemote, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !rclone.ValidRemoteName(name) {
		return nil, fmt.Errorf("invalid remote name %q: use lower-case letters, digits, \"_\" and \"-\"", name)
	}
	if existing != nil && existing.Name != name {
		var clash int64
		db.DB.Model(&models.RcloneRemote{}).Where("name = ?", name).Count(&clash)
		if clash > 0 {
			return nil, fmt.Errorf("a remote named %q already exists", name)
		}
	}
	providerType := strings.ToLower(strings.TrimSpace(req.Type))
	if providerType == "" {
		providerType = "sftp" // sane default for quick forms
	}
	if _, err := rclone.ValidateExtraFlags(req.ExtraFlags); err != nil {
		return nil, err
	}

	// Normalize incoming option keys.
	incoming := map[string]string{}
	for k, v := range req.Options {
		incoming[strings.ToLower(strings.TrimSpace(k))] = v
	}

	// Preserve secrets the UI echoed back masked or left untouched.
	oldSecrets := models.StringArray{}
	oldOptions := map[string]string{}
	if existing != nil {
		oldSecrets = existing.SecretFields
		if parsed, err := rclone.ParseOptions(existing); err == nil {
			oldOptions = parsed
		}
	}
	for _, f := range oldSecrets {
		key := strings.ToLower(f)
		if v, ok := incoming[key]; ok && (v == rclone.SecretMarker || v == "") {
			if oldVal, had := oldOptions[key]; had && oldVal != "" {
				incoming[key] = oldVal
			}
		}
	}

	optsJSON, err := json.Marshal(incoming)
	if err != nil {
		return nil, err
	}

	remote := &models.RcloneRemote{
		Name:         name,
		Type:         providerType,
		RootPrefix:   req.RootPrefix,
		ExtraFlags:   req.ExtraFlags,
		Options:      string(optsJSON),
		SecretFields: rclone.ComputeSecretFields(providerType),
		Enabled:      true,
	}
	if req.Enabled != nil {
		remote.Enabled = *req.Enabled
	}
	if existing != nil {
		remote.ID = existing.ID
		remote.CreatedAt = existing.CreatedAt
		remote.LastTestOk = existing.LastTestOk
		remote.LastTestError = existing.LastTestError
		remote.LastTestAt = existing.LastTestAt
	}
	return remote, nil
}

// ListRemotes handles GET /api/rclone/remotes — masked secrets throughout.
func (h *RcloneHandler) ListRemotes(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var remotes []models.RcloneRemote
	db.DB.Order("name ASC").Find(&remotes)

	out := make([]gin.H, 0, len(remotes))
	for i := range remotes {
		out = append(out, h.remoteResponse(&remotes[i]))
	}
	c.JSON(http.StatusOK, gin.H{"remotes": out, "count": len(out)})
}

// CreateRemote handles POST /api/rclone/remotes.
func (h *RcloneHandler) CreateRemote(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req remoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload", "details": err.Error()})
		return
	}

	var clash int64
	db.DB.Model(&models.RcloneRemote{}).Where("name = ?", strings.ToLower(strings.TrimSpace(req.Name))).Count(&clash)
	if clash > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "A remote with this name already exists"})
		return
	}

	remote, err := h.buildRemote(&req, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := db.DB.Create(remote).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create remote", "details": err.Error()})
		return
	}

	logger.Info("Rclone", "Remote created", "name", remote.Name, "type", remote.Type, "ip", c.ClientIP())
	c.JSON(http.StatusOK, h.remoteResponse(remote))
}

// UpdateRemote handles PUT /api/rclone/remotes/:id.
func (h *RcloneHandler) UpdateRemote(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid remote id"})
		return
	}

	var existing models.RcloneRemote
	if err := db.DB.First(&existing, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Remote not found"})
		return
	}

	var req remoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload", "details": err.Error()})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = existing.Name
	}
	if strings.TrimSpace(req.Type) == "" {
		req.Type = existing.Type
	}

	updated, err := h.buildRemote(&req, &existing)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := db.DB.Model(&existing).Updates(map[string]interface{}{
		"name":          updated.Name,
		"type":          updated.Type,
		"root_prefix":   updated.RootPrefix,
		"extra_flags":   updated.ExtraFlags,
		"options":       updated.Options,
		"secret_fields": updated.SecretFields,
		"enabled":       updated.Enabled,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update remote", "details": err.Error()})
		return
	}

	_ = db.DB.First(&existing, id).Error
	logger.Info("Rclone", "Remote updated", "name", existing.Name, "ip", c.ClientIP())
	c.JSON(http.StatusOK, h.remoteResponse(&existing))
}

// DeleteRemote handles DELETE /api/rclone/remotes/:id?purge_uploads=1.
func (h *RcloneHandler) DeleteRemote(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid remote id"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Remote not found"})
		return
	}

	// Optionally purge this remote's upload history rows (provider objects stay).
	if c.Query("purge_uploads") == "1" {
		db.DB.Where("remote_id = ?", remote.ID).Delete(&models.RcloneUpload{})
	}

	// The persistent provider file index belongs to this remote exclusively.
	db.DB.Where("remote_id = ?", remote.ID).Delete(&models.RcloneFile{})

	if err := db.DB.Delete(&remote).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete remote", "details": err.Error()})
		return
	}
	logger.Info("Rclone", "Remote deleted", "name", remote.Name, "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Remote deleted"})
}

// TestRemote handles POST /api/rclone/remotes/:id/test — connectivity probe
// with persisted LastTestXxx state on the remote row.
func (h *RcloneHandler) TestRemote(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid remote id"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Remote not found"})
		return
	}

	if _, err := rclone.EnsureBinary(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cloud storage engine binary unavailable", "details": err.Error()})
		return
	}

	report, err := rclone.TestRemote(c.Request.Context(), &remote)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Test infrastructure failure", "details": err.Error()})
		return
	}

	db.DB.Model(&remote).Updates(map[string]interface{}{
		"last_test_ok":    report.Ok,
		"last_test_error": report.Detail,
		"last_test_at":    time.Now(),
	})
	logger.Info("Rclone", "Remote test executed", "name", remote.Name, "ok", report.Ok, "ip", c.ClientIP())
	c.JSON(http.StatusOK, report)
}

// ListRemoteDir handles POST /api/rclone/remotes/:id/list — remote browser
// listing. Body: {path: "", recursive: false}.
func (h *RcloneHandler) ListRemoteDir(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid remote id"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Remote not found"})
		return
	}

	var req struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	_ = c.ShouldBindJSON(&req)

	if _, err := rclone.EnsureBinary(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cloud storage engine binary unavailable", "details": err.Error()})
		return
	}

	entries, err := rclone.ListRemoteDir(c.Request.Context(), &remote, req.Path, req.Recursive)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Remote listing failed", "details": err.Error(), "ok": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries, "ok": true, "path": req.Path})
}

// StartUpload handles POST /api/rclone/upload — sandbox-verify sources, create
// the per-file ledger rows, then hand execution to the job scheduler.
func (h *RcloneHandler) StartUpload(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	var req struct {
		Paths             []string `json:"paths" binding:"required"`
		RemoteID          uint     `json:"remote_id" binding:"required"`
		DestDir           string   `json:"dest_dir"`
		KeepStructure     bool     `json:"keep_structure"`
		RequirePublicLink bool     `json:"require_public_link"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload", "details": err.Error()})
		return
	}

	var remote models.RcloneRemote
	if err := db.DB.First(&remote, req.RemoteID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Remote not found"})
		return
	}
	if !remote.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Remote is disabled — enable it first"})
		return
	}
	if _, err := rclone.ValidateExtraFlags(remote.ExtraFlags); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Remote %q has invalid extra flags: %v", remote.Name, err)})
		return
	}
	if _, err := rclone.EnsureBinary(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cloud storage engine binary unavailable", "details": err.Error()})
		return
	}

	// Sandbox every source path and expand to absolute paths.
	absolutePaths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		safe, err := h.securePath(p)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied for: " + p})
			return
		}
		absolutePaths = append(absolutePaths, safe)
	}

	rows, err := rclone.PlanUploads(&rclone.UploadPlanner{
		Remote:            &remote,
		AbsolutePaths:     absolutePaths,
		ManagerRoot:       h.rootDir,
		DestDir:           req.DestDir,
		KeepStructure:     req.KeepStructure,
		RequirePublicLink: req.RequirePublicLink,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if rows, err = rclone.CreateUploads(rows); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create upload records", "details": err.Error()})
		return
	}

	totalBytes := int64(0)
	uploadIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		uploadIDs = append(uploadIDs, r.ID)
		totalBytes += r.Size
	}

	payloadBytes, _ := json.Marshal(struct {
		UploadIDs  []uint `json:"upload_ids"`
		RemoteID   uint   `json:"remote_id"`
		RemoteName string `json:"remote_name"`
	}{
		UploadIDs:  uploadIDs,
		RemoteID:   remote.ID,
		RemoteName: remote.Name,
	})

	job, err := scheduler.Engine.SubmitJob(
		"rclone_upload",
		fmt.Sprintf("Upload %d file(s) to %s", len(rows), remote.Name),
		fmt.Sprintf("Universal cloud upload of %d item(s) to remote %q (%s) via rclone; provider metadata and public links are captured per file", len(rows), remote.Name, remote.Type),
		"files",
		5,
		string(payloadBytes),
		"",
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to submit cloud-upload job", "details": err.Error()})
		return
	}

	if err := rclone.AttachJobID(rows, job.ID); err != nil {
		logger.Warn("Rclone", "Failed to stamp job id on upload rows", "job", job.ID, "error", err)
	}

	logger.Info("Rclone", "Submitted cloud-upload job", "jobID", job.ID, "remote", remote.Name, "items", len(rows), "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"status":       "success",
		"message":      "Cloud upload job queued successfully",
		"job_id":       job.ID,
		"upload_count": len(rows),
		"total_bytes":  totalBytes,
	})
}

// ListUploads handles GET /api/rclone/uploads?remote_id=&status=&job_id=&limit=&offset=.
func (h *RcloneHandler) ListUploads(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	query := db.DB.Model(&models.RcloneUpload{})
	if rid := c.Query("remote_id"); rid != "" {
		if id, err := strconv.ParseUint(rid, 10, 64); err == nil {
			query = query.Where("remote_id = ?", id)
		}
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if c.Query("job_id") != "" {
		if jid, err := strconv.ParseUint(c.Query("job_id"), 10, 64); err == nil {
			query = query.Where("scheduler_job_id = ?", jid)
		}
	}

	var total int64
	query.Count(&total)

	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 500 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(c.Query("offset")); err == nil && o > 0 {
		offset = o
	}

	var rows []models.RcloneUpload
	query.Order("id DESC").Limit(limit).Offset(offset).Find(&rows)
	c.JSON(http.StatusOK, gin.H{"uploads": rows, "total": total, "limit": limit, "offset": offset})
}

// RefreshUploadLink handles POST /api/rclone/uploads/:id/link — re-issues the
// provider `link` for one upload row.
func (h *RcloneHandler) RefreshUploadLink(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid upload id"})
		return
	}
	var row models.RcloneUpload
	if err := db.DB.First(&row, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Upload record not found"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, row.RemoteID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Owning remote no longer exists"})
		return
	}

	link, err := rclone.CreateLink(c.Request.Context(), &remote, row.RemotePath)
	if err != nil {
		db.DB.Model(&row).Updates(map[string]interface{}{
			"require_public_link": true,
			"link_error":          err.Error(),
		})
		if rclone.IsOptionalOpError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "This provider does not support public links", "details": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to create public link", "details": err.Error()})
		return
	}

	db.DB.Model(&row).Updates(map[string]interface{}{
		"require_public_link": true,
		"public_link":         link,
		"link_error":          "",
	})
	c.JSON(http.StatusOK, gin.H{"status": "success", "public_link": link})
}

// DeleteUpload handles DELETE /api/rclone/uploads/:id?purge=1 — removes the
// ledger row and, when purge=1, best-effort deletes the remote object too.
func (h *RcloneHandler) DeleteUpload(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid upload id"})
		return
	}
	var row models.RcloneUpload
	if err := db.DB.First(&row, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Upload record not found"})
		return
	}

	purgeErr := ""
	if c.Query("purge") == "1" {
		var remote models.RcloneRemote
		if err := db.DB.First(&remote, row.RemoteID).Error; err == nil {
			if err := rclone.DeleteObject(c.Request.Context(), &remote, row.RemotePath); err != nil {
				purgeErr = err.Error()
			} else {
				// Object gone from the provider: drop it from the persistent
				// provider file index too so the UI matches the remote again.
				rclone.RemoveIndexedFile(&remote, row.RemotePath)
			}
		}
	}

	if err := db.DB.Delete(&row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete upload record", "details": err.Error()})
		return
	}
	logger.Info("Rclone", "Upload record deleted", "row", row.ID, "purge", c.Query("purge") == "1", "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Upload record deleted", "purge_error": purgeErr})
}

// RemoteFiles handles GET /api/rclone/remotes/:id/files?search=&include_dirs=1&limit=&offset=
// — the DB-backed provider file index. It survives app restarts and is what
// keeps files that exist on S3/Drive/... visible in the UI.
func (h *RcloneHandler) RemoteFiles(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid remote id"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Remote not found"})
		return
	}

	query := db.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remote.ID)
	if c.Query("include_dirs") != "1" {
		query = query.Where("is_dir = ?", false)
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		query = query.Where("path LIKE ?", "%"+search+"%")
	}

	var total int64
	query.Count(&total)

	limit := 200
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 500 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(c.Query("offset")); err == nil && o > 0 {
		offset = o
	}

	var rows []models.RcloneFile
	query.Order("path ASC").Limit(limit).Offset(offset).Find(&rows)

	_, lastSync := rclone.RemoteFileStats(remote.ID)
	c.JSON(http.StatusOK, gin.H{
		"files":        rows,
		"total":        total,
		"limit":        limit,
		"offset":       offset,
		"last_sync_at": lastSync,
		"remote": gin.H{
			"id":          remote.ID,
			"name":        remote.Name,
			"type":        remote.Type,
			"root_prefix": remote.RootPrefix,
		},
	})
}

// SyncRemote handles POST /api/rclone/remotes/:id/sync — re-indexes the
// provider's current contents into the persistent file table.
func (h *RcloneHandler) SyncRemote(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid remote id"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Remote not found"})
		return
	}

	if _, err := rclone.EnsureBinary(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cloud storage engine binary unavailable", "details": err.Error()})
		return
	}

	// Big buckets can take minutes to enumerate; the request context governs.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	report, err := rclone.SyncRemoteFiles(ctx, &remote)
	if err != nil {
		logger.Warn("Rclone", "Remote file index sync failed", "remote", remote.Name, "error", err, "ip", c.ClientIP())
		c.JSON(http.StatusBadGateway, gin.H{"error": "Remote sync failed", "details": err.Error()})
		return
	}
	logger.Info("Rclone", "Remote file index synced",
		"remote", remote.Name, "files", report.FilesSeen, "created", report.Created,
		"updated", report.Updated, "removed", report.Removed, "ip", c.ClientIP())
	c.JSON(http.StatusOK, report)
}

// lazyFileWriter delays the HTTP status/headers until the first byte arrives
// from the provider so a failed `rclone cat` can still produce a clean JSON
// error instead of a corrupted 200 stream.
type lazyFileWriter struct {
	c           *gin.Context
	name        string
	mimeType    string
	headersSent bool
	Wrote       int64
}

func (w *lazyFileWriter) Write(p []byte) (int, error) {
	if !w.headersSent {
		w.headersSent = true
		w.c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": w.name}))
		if w.mimeType != "" {
			w.c.Header("Content-Type", w.mimeType)
		} else {
			w.c.Header("Content-Type", "application/octet-stream")
		}
		w.c.Status(http.StatusOK)
	}
	n, err := w.c.Writer.Write(p)
	w.Wrote += int64(n)
	return n, err
}

// FileDownload handles GET /api/rclone/files/:id/download — streams one
// indexed provider object straight through `rclone cat` to the response.
func (h *RcloneHandler) FileDownload(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file id"})
		return
	}
	var fileRow models.RcloneFile
	if err := db.DB.First(&fileRow, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File record not found"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, fileRow.RemoteID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Owning remote no longer exists"})
		return
	}

	if _, err := rclone.EnsureBinary(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cloud storage engine binary unavailable", "details": err.Error()})
		return
	}

	w := &lazyFileWriter{c: c, name: fileRow.Name, mimeType: fileRow.MimeType}
	err = rclone.StreamFile(c.Request.Context(), &remote, rclone.FileFullPath(&remote, &fileRow), w)
	if err != nil {
		if !w.headersSent {
			c.JSON(http.StatusBadGateway, gin.H{"error": "File download failed", "details": err.Error()})
			return
		}
		// Headers are already on the wire — nothing recoverable left to send.
		logger.Warn("Rclone", "Download stream aborted mid-transfer", "file", fileRow.Path, "remote", remote.Name, "error", err)
		return
	}
	logger.Info("Rclone", "File downloaded from provider", "file", fileRow.Path, "remote", remote.Name, "bytes", w.Wrote, "ip", c.ClientIP())
}

// FileLink handles POST /api/rclone/files/:id/link — issues/refreshes the
// provider public link for one indexed file and caches it on the row.
func (h *RcloneHandler) FileLink(c *gin.Context) {
	if h.proxyToServer(c, c.Request.Method, c.Request.URL.Path) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file id"})
		return
	}
	var fileRow models.RcloneFile
	if err := db.DB.First(&fileRow, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File record not found"})
		return
	}
	var remote models.RcloneRemote
	if err := db.DB.First(&remote, fileRow.RemoteID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Owning remote no longer exists"})
		return
	}

	link, err := rclone.CreateLink(c.Request.Context(), &remote, rclone.FileFullPath(&remote, &fileRow))
	if err != nil {
		db.DB.Model(&fileRow).Update("link_error", err.Error())
		if rclone.IsOptionalOpError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "This provider does not support public links", "details": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to create public link", "details": err.Error()})
		return
	}

	db.DB.Model(&fileRow).Updates(map[string]interface{}{
		"public_link": link,
		"link_error":  "",
	})
	c.JSON(http.StatusOK, gin.H{"status": "success", "public_link": link})
}
