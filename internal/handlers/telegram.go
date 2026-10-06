package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/scheduler"
	"clever-connect/internal/telegram"

	"github.com/gin-gonic/gin"
	tele "gopkg.in/telebot.v4"
)

// TelegramHandler provides REST API endpoints for the Telegram bot core.
// On client mode, ALL requests are proxied to the remote server — the client
// never runs any Telegram operations locally.
type TelegramHandler struct {
	cfg *config.Config
}

// NewTelegramHandler creates a new handler instance.
func NewTelegramHandler(cfg *config.Config) *TelegramHandler {
	return &TelegramHandler{cfg: cfg}
}

func (h *TelegramHandler) proxyToServer(c *gin.Context) bool {
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
	remoteHost = strings.TrimSuffix(remoteHost, "/")

	// Build remote URL using the original request path
	apiPath := c.Request.URL.Path
	remoteURL := remoteHost + apiPath
	if c.Request.URL.RawQuery != "" {
		remoteURL += "?" + c.Request.URL.RawQuery
	}

	var reqBody io.Reader
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions && c.Request.Method != http.MethodDelete {
		reqBody = c.Request.Body
	}

	// Create proxy request
	req, err := http.NewRequest(c.Request.Method, remoteURL, reqBody)
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

	// Overwrite local credentials with the remote server auth token
	if remoteToken != "" {
		req.Header.Set("Authorization", "Bearer "+remoteToken)
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

	// Pipe remote response back directly
	_, _ = io.Copy(c.Writer, resp.Body)
	return true
}

// GetConfig returns the current Telegram configuration from the database.
// GET /api/telegram/config
func (h *TelegramHandler) GetConfig(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{
			"config":              models.TelegramConfig{PollingInterval: 10, MaxFileSize: 2000, EnableFileSharing: true, EnableNotifications: true},
			"running":             false,
			"user_session_exists": telegram.HasUserSession(),
		})
		return
	}

	// Mask the bot token for security — only show last 8 chars
	maskedToken := cfg.BotToken
	if len(maskedToken) > 8 {
		maskedToken = "••••••••" + maskedToken[len(maskedToken)-8:]
	}

	running := telegram.IsRunning()
	var stats map[string]interface{}
	if running {
		if eng := telegram.GetEngine(); eng != nil {
			stats = eng.Stats()
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"config":              cfg,
		"masked_token":        maskedToken,
		"running":             running,
		"stats":               stats,
		"user_session_exists": telegram.HasUserSession(),
	})
}

// SaveConfig persists the Telegram configuration to the database.
// POST /api/telegram/config
func (h *TelegramHandler) SaveConfig(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var req models.TelegramConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	if req.PollingInterval < 1 {
		req.PollingInterval = 10
	}
	if req.MaxFileSize < 1 {
		req.MaxFileSize = 2000
	}

	var existing models.TelegramConfig
	if err := db.DB.First(&existing).Error; err != nil {
		if err := db.DB.Create(&req).Error; err != nil {
			logger.Error("Telegram", "Failed to create Telegram config", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save configuration"})
			return
		}
	} else {
		if req.BotToken == "" {
			req.BotToken = existing.BotToken
		}
		req.Model = existing.Model
		// Preserve the durable User Account Verification data: saving
		// settings must never de-authorize a verified account or wipe the
		// session blob the engine restores after an ephemeral-disk wipe.
		req.UserSessionData = existing.UserSessionData
		req.UserVerifiedAt = existing.UserVerifiedAt
		if err := db.DB.Save(&req).Error; err != nil {
			logger.Error("Telegram", "Failed to update Telegram config", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save configuration"})
			return
		}
	}

	if eng := telegram.GetEngine(); eng != nil {
		_ = eng.ReloadConfig()
	}

	logger.Info("Telegram", "Configuration saved", "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Telegram configuration saved"})
}

// TestConnection tests the bot token by calling Telegram's getMe API.
// POST /api/telegram/test
func (h *TelegramHandler) TestConnection(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var req struct {
		BotToken string `json:"bot_token"`
		AuthType string `json:"auth_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	if req.AuthType == "user" {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "User account verification must be completed using phone number and code.",
		})
		return
	}

	if req.BotToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bot token is required"})
		return
	}

	pref := tele.Settings{
		Token:   req.BotToken,
		Offline: true,
	}

	bot, err := tele.NewBot(pref)
	if err != nil {
		logger.Warn("Telegram", "Bot token validation failed", "error", err, "ip", c.ClientIP())
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	logger.Info("Telegram", "Bot token validated successfully",
		"bot_username", bot.Me.Username,
		"bot_id", bot.Me.ID,
		"ip", c.ClientIP(),
	)

	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"bot_username":    bot.Me.Username,
		"bot_id":          bot.Me.ID,
		"first_name":      bot.Me.FirstName,
		"can_join_groups": bot.Me.CanJoinGroups,
	})
}

// StartBot starts the Telegram bot engine.
// POST /api/telegram/start
func (h *TelegramHandler) StartBot(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	if telegram.IsRunning() {
		c.JSON(http.StatusConflict, gin.H{"error": "Bot is already running"})
		return
	}

	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No Telegram configuration found. Save a configuration first."})
		return
	}

	if cfg.AuthType == "bot" && cfg.BotToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No bot token configured. Save a configuration first."})
		return
	}

	// User mode requires a verified MTProto session file. Give the client a
	// machine-readable code so the UI can guide the user to the verification
	// flow instead of surfacing a cryptic engine error.
	if cfg.AuthType == "user" && !telegram.HasUserSession() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "User account is not verified yet. Complete User Account Verification (phone number → code) in Telegram settings first — the engine will start automatically after verification.",
			"code":  "user_auth_required",
		})
		return
	}

	if err := telegram.StartEngine(&cfg); err != nil {
		logger.Error("Telegram", "Failed to start bot engine", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start: " + err.Error()})
		return
	}

	db.DB.Model(&cfg).Update("is_active", true)

	logger.Info("Telegram", "Engine started via API", "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Telegram engine started"})
}

// StopBot stops the running Telegram bot engine.
// POST /api/telegram/stop
func (h *TelegramHandler) StopBot(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	if err := telegram.StopEngine(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	db.DB.Model(&models.TelegramConfig{}).Where("1 = 1").Update("is_active", false)

	logger.Info("Telegram", "Bot stopped via API", "ip", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Telegram bot stopped"})
}

// GetStatus returns the current bot engine status and metrics.
// GET /api/telegram/status
func (h *TelegramHandler) GetStatus(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	eng := telegram.GetEngine()
	if eng == nil || !telegram.IsRunning() {
		c.JSON(http.StatusOK, gin.H{
			"running": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"running": true,
		"stats":   eng.Stats(),
	})
}

// SendFile sends a file from the server file manager to the Telegram bot's admin chat.
// POST /api/telegram/send-file
func (h *TelegramHandler) SendFile(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var req struct {
		FilePath string `json:"file_path" binding:"required"`
		ChatID   int64  `json:"chat_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_path is required"})
		return
	}

	eng := telegram.GetEngine()
	if eng == nil || !telegram.IsRunning() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Telegram bot is not running"})
		return
	}

	// Prepare payload for job scheduler
	payloadBytes, err := json.Marshal(map[string]interface{}{
		"file_path": req.FilePath,
		"chat_id":   req.ChatID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to serialize upload payload"})
		return
	}

	// Submit job to enterprise job scheduler
	job, err := scheduler.Engine.SubmitJob(
		"telegram_upload",
		fmt.Sprintf("Telegram Upload: %s", filepath.Base(req.FilePath)),
		fmt.Sprintf("Upload of %s to Telegram", req.FilePath),
		"files", // category
		5,       // priority
		string(payloadBytes),
		"", // cronExpr
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to queue upload job in scheduler: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "File upload job queued in scheduler",
		"job_id":  job.ID,
	})
}

// SetBotAvatar updates the Telegram bot profile picture (avatar).
// POST /api/telegram/set-avatar
func (h *TelegramHandler) SetBotAvatar(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	file, err := c.FormFile("avatar")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil || cfg.BotToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Telegram bot is not configured"})
		return
	}

	srcFile, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open uploaded file"})
		return
	}
	defer srcFile.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("photo", file.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create multipart form field"})
		return
	}

	if _, err := io.Copy(part, srcFile); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to copy file data to request body"})
		return
	}
	writer.Close()

	url := fmt.Sprintf("https://api.telegram.org/bot%s/setBotProfilePhoto", cfg.BotToken)
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create API request"})
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send request to Telegram API: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Telegram API returned error: " + string(respBytes)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Bot avatar updated successfully",
	})
}

// BroadcastMessage sends a message to all active subscribers.
// POST /api/telegram/broadcast
func (h *TelegramHandler) BroadcastMessage(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var req struct {
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message content is required"})
		return
	}

	eng := telegram.GetEngine()
	if eng == nil || !telegram.IsRunning() || eng.Bot == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Telegram Bot engine is not running"})
		return
	}

	var subs []models.TelegramSubscriber
	if err := db.DB.Where("active = ?", true).Find(&subs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscribers"})
		return
	}

	if len(subs) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"status":            "success",
			"message":           "No active subscribers to broadcast to",
			"subscribers_count": 0,
		})
		return
	}

	// Run broadcast in background
	go func(subs []models.TelegramSubscriber, msgText string) {
		logger.Info("Telegram", "Starting broadcast to subscribers", "count", len(subs))
		for _, sub := range subs {
			_, err := eng.Bot.Send(tele.ChatID(sub.ChatID), msgText, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
			if err != nil {
				logger.Error("Telegram", "Failed to send broadcast to subscriber", "chat_id", sub.ChatID, "error", err)
			}
			// Rate limiting delay
			time.Sleep(35 * time.Millisecond)
		}
		logger.Info("Telegram", "Broadcast completed successfully")
	}(subs, req.Message)

	c.JSON(http.StatusOK, gin.H{
		"status":            "success",
		"message":           fmt.Sprintf("Broadcast initiated successfully to %d active subscribers", len(subs)),
		"subscribers_count": len(subs),
	})
}

// SendAuthCode initiates the user login flow by sending a verification code.
// POST /api/telegram/auth/send-code
func (h *TelegramHandler) SendAuthCode(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var req struct {
		PhoneNumber string `json:"phone_number" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "phone_number is required"})
		return
	}

	if telegram.IsRunning() {
		_ = telegram.StopEngine()
	}

	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Save Telegram configuration first before login."})
		return
	}

	telegram.InitAuthFlow()
	telegram.StartAuthClient(req.PhoneNumber, &cfg)

	// Wait for code sent confirmation or immediate error
	select {
	case <-telegram.GetCodeSentChan():
		c.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"message": "Verification code sent to " + req.PhoneNumber,
		})
	case err := <-telegram.GetErrChan():
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case <-time.After(15 * time.Second):
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Timeout waiting for Telegram to send code. Verify your App api_id and api_hash."})
	}
}

// VerifyAuthCode submits the verification code received on SMS/Telegram.
// POST /api/telegram/auth/verify-code
func (h *TelegramHandler) VerifyAuthCode(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	select {
	case telegram.GetCodeChan() <- req.Code:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "No active authentication flow"})
		return
	}

	select {
	case <-telegram.GetSuccessChan():
		var cfg models.TelegramConfig
		_ = db.DB.First(&cfg)
		// Column-scoped update: a full-row Save() here would race with the
		// auth goroutine persisting the fresh session blob to the same row.
		db.DB.Model(&cfg).Update("is_active", true)
		// Persist the verification data immediately (the auth goroutine also
		// saves it) so the account stays verified across restarts.
		telegram.SaveUserSessionData()

		_ = telegram.StartEngine(&cfg)

		c.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"message": "Authenticated successfully. User engine started.",
		})
	case <-telegram.GetPwReqChan():
		c.JSON(http.StatusOK, gin.H{
			"status":            "2fa_required",
			"password_required": true,
			"message":           "Two-factor authentication is enabled. Please enter your password.",
		})
	case err := <-telegram.GetErrChan():
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case <-time.After(20 * time.Second):
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Timeout waiting for code verification"})
	}
}

// VerifyAuthPassword submits the 2FA password.
// POST /api/telegram/auth/verify-password
func (h *TelegramHandler) VerifyAuthPassword(c *gin.Context) {
	if h.proxyToServer(c) {
		return
	}

	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password is required"})
		return
	}

	select {
	case telegram.GetPasswordChan() <- req.Password:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "No active authentication flow"})
		return
	}

	select {
	case <-telegram.GetSuccessChan():
		var cfg models.TelegramConfig
		_ = db.DB.First(&cfg)
		// Column-scoped update: a full-row Save() here would race with the
		// auth goroutine persisting the fresh session blob to the same row.
		db.DB.Model(&cfg).Update("is_active", true)
		// Persist the verification data immediately (the auth goroutine also
		// saves it) so the account stays verified across restarts.
		telegram.SaveUserSessionData()

		_ = telegram.StartEngine(&cfg)

		c.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"message": "Authenticated successfully. User engine started.",
		})
	case err := <-telegram.GetErrChan():
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case <-time.After(20 * time.Second):
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Timeout waiting for password verification"})
	}
}
