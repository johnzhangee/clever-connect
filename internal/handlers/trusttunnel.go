package handlers

import (
	"fmt"
	"net/http"
	"os"
	"strconv"

	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/trusttunnel"

	"github.com/gin-gonic/gin"
)

// TrustTunnelHandler manages API routes for the TrustTunnel stealth protocol.
type TrustTunnelHandler struct {
	cfg *config.Config
}

// NewTrustTunnelHandler creates a new handler instance.
func NewTrustTunnelHandler(cfg *config.Config) *TrustTunnelHandler {
	return &TrustTunnelHandler{cfg: cfg}
}

func (h *TrustTunnelHandler) populateServerCertPEM(ttCfg *models.TrustTunnelConfig) {
	if h.cfg.AppMode == "server" && ttCfg.TlsCertPath != "" {
		if certBytes, err := os.ReadFile(ttCfg.TlsCertPath); err == nil {
			ttCfg.TlsServerCert = string(certBytes)
		}
	}
}

// GetConfig handles GET /api/trusttunnel/config
func (h *TrustTunnelHandler) GetConfig(c *gin.Context) {
	var ttCfg models.TrustTunnelConfig
	if err := db.DB.First(&ttCfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "TrustTunnel config not found"})
		return
	}

	h.populateServerCertPEM(&ttCfg)

	var users []models.TrustTunnelUser
	db.DB.Find(&users)

	var rules []models.TrustTunnelFirewallRule
	db.DB.Find(&rules)

	c.JSON(http.StatusOK, gin.H{
		"config":     ttCfg,
		"users":      users,
		"rules":      rules,
		"is_running": trusttunnel.IsRunning(),
		"app_mode":   h.cfg.AppMode,
	})
}

// SaveConfig handles POST /api/trusttunnel/config
func (h *TrustTunnelHandler) SaveConfig(c *gin.Context) {
	var input struct {
		IsActive                bool   `json:"is_active"`
		ListenAddress           string `json:"listen_address"`
		ConnectAddress          string `json:"connect_address"`
		Socks5Port              int    `json:"socks5_port"`
		HttpPort                int    `json:"http_port"`
		ForcedTransport         string `json:"forced_transport"`
		AuthFailureStatusCode   int    `json:"auth_failure_status_code"`
		ClientRandomPrefix      string `json:"client_random_prefix"`
		H2InitialStreamWindowSize   int    `json:"h2_initial_stream_window_size"`
		H2InitialConnWindowSize     int    `json:"h2_initial_conn_window_size"`
		TlsHandshakeTimeoutSecs     int    `json:"tls_handshake_timeout_secs"`
		KillSwitchEnabled       bool   `json:"kill_switch_enabled"`
		ActivePreset            string `json:"active_preset"`
		TlsCertPath             string `json:"tls_cert_path"`
		TlsKeyPath              string `json:"tls_key_path"`
		ServerHostname          string `json:"server_hostname"`
		PublicTlsPort           int    `json:"public_tls_port"`
		ClientUsername          string `json:"client_username"`
		ClientPassword          string `json:"client_password"`
		TlsServerCert           string `json:"tls_server_cert"`
		SkipTlsVerify           bool   `json:"skip_tls_verify"`
		DnsUpstreams            string `json:"dns_upstreams"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	// Validate forced_transport
	validTransports := map[string]bool{"http2": true, "http1": true, "quic": true}
	if input.ForcedTransport != "" && !validTransports[input.ForcedTransport] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid transport. Must be http2, http1, or quic"})
		return
	}

	// Validate auth_failure_status_code
	validCodes := map[int]bool{407: true, 405: true}
	if input.AuthFailureStatusCode != 0 && !validCodes[input.AuthFailureStatusCode] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid auth_failure_status_code. Must be 407 or 405"})
		return
	}

	var ttCfg models.TrustTunnelConfig
	if err := db.DB.First(&ttCfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "TrustTunnel config not found"})
		return
	}

	// Apply updates
	ttCfg.IsActive = input.IsActive
	if input.ListenAddress != "" {
		ttCfg.ListenAddress = input.ListenAddress
	}
	ttCfg.ConnectAddress = input.ConnectAddress
	if input.Socks5Port > 0 {
		ttCfg.Socks5Port = input.Socks5Port
	}
	if input.HttpPort > 0 {
		ttCfg.HttpPort = input.HttpPort
	}
	if input.ForcedTransport != "" {
		ttCfg.ForcedTransport = input.ForcedTransport
	}
	if input.AuthFailureStatusCode != 0 {
		ttCfg.AuthFailureStatusCode = input.AuthFailureStatusCode
	}
	ttCfg.ClientRandomPrefix = input.ClientRandomPrefix
	if input.H2InitialStreamWindowSize > 0 {
		ttCfg.H2InitialStreamWindowSize = input.H2InitialStreamWindowSize
	}
	if input.H2InitialConnWindowSize > 0 {
		ttCfg.H2InitialConnWindowSize = input.H2InitialConnWindowSize
	}
	if input.TlsHandshakeTimeoutSecs > 0 {
		ttCfg.TlsHandshakeTimeoutSecs = input.TlsHandshakeTimeoutSecs
	}
	ttCfg.KillSwitchEnabled = input.KillSwitchEnabled
	if input.ActivePreset != "" {
		ttCfg.ActivePreset = input.ActivePreset
	}
	ttCfg.TlsCertPath = input.TlsCertPath
	ttCfg.TlsKeyPath = input.TlsKeyPath
	ttCfg.ServerHostname = input.ServerHostname
	ttCfg.PublicTlsPort = input.PublicTlsPort
	ttCfg.ClientUsername = input.ClientUsername
	ttCfg.ClientPassword = input.ClientPassword
	ttCfg.TlsServerCert = input.TlsServerCert
	ttCfg.SkipTlsVerify = input.SkipTlsVerify
	ttCfg.DnsUpstreams = input.DnsUpstreams

	if err := db.DB.Save(&ttCfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save configuration"})
		return
	}

	h.populateServerCertPEM(&ttCfg)

	logger.Info("TrustTunnel", "Configuration updated",
		"transport", ttCfg.ForcedTransport,
		"preset", ttCfg.ActivePreset,
		"is_active", ttCfg.IsActive,
	)

	// Auto-restart if engine is running
	if trusttunnel.IsRunning() {
		trusttunnel.StopEngine()
		if h.cfg.AppMode == "server" {
			if err := trusttunnel.StartServerEngine(&ttCfg); err != nil {
				logger.Error("TrustTunnel", "Failed to auto-restart server engine after config save", "error", err)
			} else {
				logger.Info("TrustTunnel", "Server engine auto-restarted after config save")
			}
		} else {
			if err := trusttunnel.StartClientEngine(&ttCfg); err != nil {
				logger.Error("TrustTunnel", "Failed to auto-restart client engine after config save", "error", err)
			} else {
				logger.Info("TrustTunnel", "Client engine auto-restarted after config save")
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"config":     ttCfg,
		"is_running": trusttunnel.IsRunning(),
	})
}

// StartEngine handles POST /api/trusttunnel/start
func (h *TrustTunnelHandler) StartEngine(c *gin.Context) {
	var ttCfg models.TrustTunnelConfig
	if err := db.DB.First(&ttCfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "TrustTunnel config not found"})
		return
	}

	if trusttunnel.IsRunning() {
		c.JSON(http.StatusOK, gin.H{
			"message":    "TrustTunnel engine is already running",
			"is_running": true,
		})
		return
	}

	var err error
	if h.cfg.AppMode == "server" {
		err = trusttunnel.StartServerEngine(&ttCfg)
	} else {
		if ttCfg.ConnectAddress == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Connect address is required for client mode"})
			return
		}
		err = trusttunnel.StartClientEngine(&ttCfg)
	}

	if err != nil {
		logger.Error("TrustTunnel", "Failed to start engine", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":      fmt.Sprintf("Failed to start engine: %v", err),
			"is_running": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "TrustTunnel engine started successfully",
		"is_running": true,
	})
}

// StopEngine handles POST /api/trusttunnel/stop
func (h *TrustTunnelHandler) StopEngine(c *gin.Context) {
	trusttunnel.StopEngine()

	c.JSON(http.StatusOK, gin.H{
		"message":    "TrustTunnel engine stopped",
		"is_running": false,
	})
}

// ListUsers handles GET /api/trusttunnel/users
func (h *TrustTunnelHandler) ListUsers(c *gin.Context) {
	var users []models.TrustTunnelUser
	db.DB.Find(&users)

	// Strip password hashes from response
	type safeUser struct {
		ID        uint   `json:"id"`
		Username  string `json:"username"`
		IsActive  bool   `json:"is_active"`
		CreatedAt string `json:"created_at"`
	}
	safe := make([]safeUser, len(users))
	for i, u := range users {
		safe[i] = safeUser{
			ID:        u.ID,
			Username:  u.Username,
			IsActive:  u.IsActive,
			CreatedAt: u.CreatedAt.Format("2006-01-02 15:04"),
		}
	}

	c.JSON(http.StatusOK, gin.H{"users": safe})
}

// CreateUser handles POST /api/trusttunnel/users
func (h *TrustTunnelHandler) CreateUser(c *gin.Context) {
	var input struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	user := models.TrustTunnelUser{
		Username: input.Username,
		Password: input.Password, // In production, this should be bcrypt-hashed
		IsActive: true,
	}

	if err := db.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to create user. Username may already exist."})
		return
	}

	logger.Info("TrustTunnel", "New proxy user created", "username", input.Username)

	c.JSON(http.StatusOK, gin.H{
		"message": "User created successfully",
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"is_active": user.IsActive,
		},
	})
}

// DeleteUser handles DELETE /api/trusttunnel/users/:id
func (h *TrustTunnelHandler) DeleteUser(c *gin.Context) {
	id := c.Param("id")
	uid, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	if err := db.DB.Delete(&models.TrustTunnelUser{}, uid).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}

	logger.Info("TrustTunnel", "Proxy user deleted", "id", id)
	c.JSON(http.StatusOK, gin.H{"message": "User deleted"})
}

// ListRules handles GET /api/trusttunnel/rules
func (h *TrustTunnelHandler) ListRules(c *gin.Context) {
	var rules []models.TrustTunnelFirewallRule
	db.DB.Find(&rules)
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

// CreateRule handles POST /api/trusttunnel/rules
func (h *TrustTunnelHandler) CreateRule(c *gin.Context) {
	var input struct {
		TargetCIDR     string `json:"target_cidr" binding:"required"`
		BypassStrategy string `json:"bypass_strategy"`
		Description    string `json:"description"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_cidr is required"})
		return
	}

	if input.BypassStrategy == "" {
		input.BypassStrategy = "direct-route"
	}

	rule := models.TrustTunnelFirewallRule{
		TargetCIDR:     input.TargetCIDR,
		BypassStrategy: input.BypassStrategy,
		Description:    input.Description,
	}

	if err := db.DB.Create(&rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create rule"})
		return
	}

	logger.Info("TrustTunnel", "Firewall rule created", "cidr", input.TargetCIDR, "strategy", input.BypassStrategy)
	c.JSON(http.StatusOK, gin.H{"message": "Rule created", "rule": rule})
}

// DeleteRule handles DELETE /api/trusttunnel/rules/:id
func (h *TrustTunnelHandler) DeleteRule(c *gin.Context) {
	id := c.Param("id")
	uid, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid rule ID"})
		return
	}

	if err := db.DB.Delete(&models.TrustTunnelFirewallRule{}, uid).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete rule"})
		return
	}

	logger.Info("TrustTunnel", "Firewall rule deleted", "id", id)
	c.JSON(http.StatusOK, gin.H{"message": "Rule deleted"})
}

// ExportToken handles GET /api/trusttunnel/export
func (h *TrustTunnelHandler) ExportToken(c *gin.Context) {
	var ttCfg models.TrustTunnelConfig
	if err := db.DB.First(&ttCfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "TrustTunnel config not found"})
		return
	}

	token := trusttunnel.GenerateExportToken(&ttCfg)

	c.JSON(http.StatusOK, gin.H{
		"token": token,
	})
}

// ImportToken handles POST /api/trusttunnel/import
func (h *TrustTunnelHandler) ImportToken(c *gin.Context) {
	var input struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token is required"})
		return
	}

	params, err := trusttunnel.ParseImportToken(input.Token)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid token: %v", err)})
		return
	}

	var ttCfg models.TrustTunnelConfig
	if err := db.DB.First(&ttCfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "TrustTunnel config not found"})
		return
	}

	// Apply parsed parameters
	if hostname, ok := params["hostname"]; ok {
		ttCfg.ServerHostname = hostname
	}
	if addr, ok := params["addr"]; ok {
		ttCfg.ConnectAddress = trusttunnel.ResolveConnectAddress(addr, ttCfg.ServerHostname)
	}
	if transport, ok := params["transport"]; ok {
		ttCfg.ForcedTransport = transport
	}
	if probe, ok := params["probe"]; ok {
		if v, err := strconv.Atoi(probe); err == nil {
			ttCfg.AuthFailureStatusCode = v
		}
	}
	if prefix, ok := params["prefix"]; ok {
		ttCfg.ClientRandomPrefix = prefix
	}
	if h2win, ok := params["h2win"]; ok {
		if v, err := strconv.Atoi(h2win); err == nil {
			ttCfg.H2InitialStreamWindowSize = v
		}
	}
	if timeout, ok := params["timeout"]; ok {
		if v, err := strconv.Atoi(timeout); err == nil {
			ttCfg.TlsHandshakeTimeoutSecs = v
		}
	}
	if user, ok := params["user"]; ok {
		ttCfg.ClientUsername = user
	}
	if pass, ok := params["pass"]; ok {
		ttCfg.ClientPassword = pass
	}
	if cert, ok := params["cert"]; ok {
		ttCfg.TlsServerCert = cert
	}
	if skipVerify, ok := params["skip_verify"]; ok {
		if v, err := strconv.ParseBool(skipVerify); err == nil {
			ttCfg.SkipTlsVerify = v
		}
	}
	if dns, ok := params["dns_upstreams"]; ok {
		ttCfg.DnsUpstreams = dns
	}

	if err := db.DB.Save(&ttCfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save imported configuration"})
		return
	}

	h.populateServerCertPEM(&ttCfg)

	logger.Info("TrustTunnel", "Configuration imported from token",
		"connect", ttCfg.ConnectAddress,
		"transport", ttCfg.ForcedTransport,
	)

	c.JSON(http.StatusOK, gin.H{
		"message": "Token imported successfully",
		"config":  ttCfg,
	})
}

// GenerateCert handles POST /api/trusttunnel/generate-cert
func (h *TrustTunnelHandler) GenerateCert(c *gin.Context) {
	var input struct {
		Hostname string `json:"hostname" binding:"required"`
		Email    string `json:"email"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Hostname is required: " + err.Error()})
		return
	}

	logger.Info("TrustTunnel", "Generating Let's Encrypt certificate", "hostname", input.Hostname)

	// Call certmanager to generate the certificate
	certPath, keyPath, err := trusttunnel.GenerateCertificate(c.Request.Context(), input.Hostname, input.Email, "data")
	if err != nil {
		logger.Error("TrustTunnel", "Certificate generation failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Certificate generation failed: %v", err)})
		return
	}

	// Update DB config with new paths and hostname
	var ttCfg models.TrustTunnelConfig
	if err := db.DB.First(&ttCfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "TrustTunnel config not found"})
		return
	}

	ttCfg.TlsCertPath = certPath
	ttCfg.TlsKeyPath = keyPath
	ttCfg.ServerHostname = input.Hostname

	if err := db.DB.Save(&ttCfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save configuration with new cert paths"})
		return
	}

	h.populateServerCertPEM(&ttCfg)

	logger.Info("TrustTunnel", "Certificate successfully generated and configuration updated",
		"hostname", input.Hostname,
		"cert_path", certPath,
		"key_path", keyPath,
	)

	// Auto-restart if engine is running
	if trusttunnel.IsRunning() {
		trusttunnel.StopEngine()
		if h.cfg.AppMode == "server" {
			if err := trusttunnel.StartServerEngine(&ttCfg); err != nil {
				logger.Error("TrustTunnel", "Failed to auto-restart server engine after cert generation", "error", err)
			} else {
				logger.Info("TrustTunnel", "Server engine auto-restarted after cert generation")
			}
		} else {
			if err := trusttunnel.StartClientEngine(&ttCfg); err != nil {
				logger.Error("TrustTunnel", "Failed to auto-restart client engine after cert generation", "error", err)
			} else {
				logger.Info("TrustTunnel", "Client engine auto-restarted after cert generation")
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":           "Certificate generated successfully",
		"cert_chain_path":  certPath,
		"private_key_path": keyPath,
		"config":            ttCfg,
		"is_running":        trusttunnel.IsRunning(),
	})
}

// HandleACMEChallenge serves the HTTP-01 challenge response at /.well-known/acme-challenge/:token
func HandleACMEChallenge(c *gin.Context) {
	token := c.Param("token")
	keyAuth, ok := trusttunnel.GetChallenge(token)
	if !ok {
		c.String(http.StatusNotFound, "Challenge token not found")
		return
	}
	c.String(http.StatusOK, keyAuth)
}

