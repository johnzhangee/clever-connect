// session_store.go — durable persistence of the verified MTProto user session.
//
// The interactive User Account Verification flow (phone number → code →
// optional 2FA password) produces an MTProto session file under
// ./data/manager/.telegram. The manager disk is ephemeral: container restarts
// and redeploys wipe it, which would force the user to repeat the whole
// verification even though the account is already authorized. To keep the
// account permanently verified, the session blob is mirrored into the
// TelegramConfig database row the moment verification succeeds (and refreshed
// after every successful user-engine connect, since gotd may re-key or
// migrate datacenters), and it is transparently restored from the database
// whenever the file is missing. Once verified, the account stays verified and
// the user engine is always ready to start.
package telegram

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

// userSessionMu serializes session file/DB mirroring: ensureUserSessionFile
// may run concurrently (boot auto-start, HTTP handlers, StartEngine) with a
// SaveUserSessionData from the auth or engine goroutines.
var userSessionMu sync.Mutex

// ensureUserSessionFile restores the verified MTProto session file from the
// durable database copy when the ephemeral disk lost it. It reports whether a
// usable session file exists afterwards.
func ensureUserSessionFile() bool {
	userSessionMu.Lock()
	defer userSessionMu.Unlock()

	path := UserSessionPath()
	if _, err := os.Stat(path); err == nil {
		return true
	}

	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil || cfg.UserSessionData == "" {
		return false
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		logger.Warn("Telegram", "Failed to create session directory for session restore", "error", err)
		return false
	}
	if err := os.WriteFile(path, []byte(cfg.UserSessionData), 0600); err != nil {
		logger.Warn("Telegram", "Failed to restore verified MTProto session from database", "error", err)
		return false
	}

	logger.Info("Telegram", "Restored verified MTProto user session from database — account stays verified")
	return true
}

// SaveUserSessionData mirrors the current on-disk MTProto session into the
// TelegramConfig row so the verified account survives ephemeral-disk loss.
// No-op when the file is missing or the stored copy is already identical.
func SaveUserSessionData() {
	userSessionMu.Lock()
	defer userSessionMu.Unlock()

	data, err := os.ReadFile(UserSessionPath())
	if err != nil || len(data) == 0 {
		return
	}
	session := string(data)

	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		return
	}
	if cfg.UserSessionData == session && cfg.UserVerifiedAt != nil {
		return
	}

	updates := map[string]interface{}{"user_session_data": session}
	if cfg.UserVerifiedAt == nil {
		now := time.Now()
		updates["user_verified_at"] = &now
	}
	if err := db.DB.Model(&cfg).Updates(updates).Error; err != nil {
		logger.Warn("Telegram", "Failed to persist verified user session to database", "error", err)
		return
	}
	logger.Info("Telegram", "Verified user session persisted to database — account stays verified across restarts")
}
