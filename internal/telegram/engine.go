// Package telegram provides a high-performance, parallel Telegram bot engine
// for CleverConnect. It uses a worker pool spanning all CPU cores for maximum
// throughput and integrates deeply with the application's file manager, settings,
// and database layer.
package telegram

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/updates"
	updhook "github.com/gotd/td/telegram/updates/hook"
	"github.com/gotd/td/tg"
	tele "gopkg.in/telebot.v4"
)

// ──────────────────────────────────────────────────────────────
// Engine is the central Telegram bot runtime. It is designed to
// be started and stopped dynamically from the admin panel.
// ──────────────────────────────────────────────────────────────

// Engine holds the running telebot instance and worker pool.
type Engine struct {
	Bot         *tele.Bot        // nil if AuthType == "user"
	gotdClient  *telegram.Client // nil if AuthType == "bot"; guarded by clientMu
	gotdCtx     context.Context
	gotdCancel  context.CancelFunc
	meUsername  string
	meID        int64
	meFirstName string
	Config      *models.TelegramConfig
	workerPool  chan func()
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	running     atomic.Bool
	startedAt   time.Time
	mu          sync.RWMutex
	// clientMu guards gotdClient: the self-healing supervisor may replace
	// the MTProto client at runtime, while jobs read it concurrently.
	clientMu sync.RWMutex

	// Metrics (atomic for lock-free reads from API)
	messagesProcessed atomic.Int64
	commandsProcessed atomic.Int64
	filesSent         atomic.Int64
	errors            atomic.Int64
}

// Global singleton (only one bot can run at a time)
var (
	instance *Engine
	mu       sync.Mutex
)

type AuthRequest struct {
	Type          string // "send_code", "sign_in", "password"
	PhoneNumber   string
	Code          string
	Password      string
	PhoneCodeHash string
	ResponseChan  chan AuthResponse
}

type AuthResponse struct {
	Success          bool
	Error            string
	PhoneCodeHash    string
	PasswordRequired bool
}

// GetEngine returns the active engine instance, or nil.
func GetEngine() *Engine {
	mu.Lock()
	defer mu.Unlock()
	return instance
}

// IsRunning returns true if the bot engine is currently active.
func IsRunning() bool {
	e := GetEngine()
	return e != nil && e.running.Load()
}

// currentClient returns the live MTProto client (nil when none exists). The
// self-healing supervisor may replace the client at runtime after a crash, so
// callers must re-fetch it per operation instead of caching it.
func (e *Engine) currentClient() *telegram.Client {
	e.clientMu.RLock()
	defer e.clientMu.RUnlock()
	return e.gotdClient
}

// setGotdClient publishes a new (possibly replacement) MTProto client.
func (e *Engine) setGotdClient(c *telegram.Client) {
	e.clientMu.Lock()
	e.gotdClient = c
	e.clientMu.Unlock()
}

// UserSessionPath returns the filesystem path of the MTProto user session file.
func UserSessionPath() string {
	return filepath.Join("./data/manager", ".telegram", "session.json")
}

// HasUserSession reports whether a verified MTProto user session file exists.
// The engine cannot start in user mode until this file has been created by the
// interactive verification flow (phone number + login code [+ 2FA password]).
func HasUserSession() bool {
	_, err := os.Stat(UserSessionPath())
	return err == nil
}

// StartEngine boots the Telegram bot using the config stored in the database.
// It spawns runtime.NumCPU() worker goroutines for parallel message processing.
func StartEngine(cfg *models.TelegramConfig) error {
	mu.Lock()
	defer mu.Unlock()

	// Tear down any existing instance first
	if instance != nil && instance.running.Load() {
		instance.shutdown()
	}

	logger.Info("Telegram", "Initializing Telegram bot engine",
		"auth_type", cfg.AuthType,
		"workers", runtime.NumCPU(),
		"polling_interval", cfg.PollingInterval,
	)

	ctx, cancel := context.WithCancel(context.Background())

	eng := &Engine{
		Config:     cfg,
		workerPool: make(chan func(), runtime.NumCPU()*64), // buffered job queue
		ctx:        ctx,
		cancel:     cancel,
		startedAt:  time.Now(),
	}

	// Initialize gotd client for MTProto operations
	appID := cfg.AppID
	if appID == 0 {
		appID = 2040
	}
	appHash := cfg.AppHash
	if appHash == "" {
		appHash = "b18441a1ff607e10a989891a5624e0d4"
	}

	sessionDir := filepath.Join("./data/manager", ".telegram")
	_ = os.MkdirAll(sessionDir, 0755)

	var sessionPath string
	if cfg.AuthType == "user" {
		sessionPath = UserSessionPath()
		// Check if session file exists
		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			cancel()
			return fmt.Errorf("MTProto user session not found — complete User Account Verification (phone number + code) in Telegram settings first")
		}
	} else {
		sessionPath = filepath.Join(sessionDir, "session_bot.json")
	}

	d := tg.NewUpdateDispatcher()
	gaps := updates.New(updates.Config{
		Handler: d,
	})

	opts := telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{
			Path: sessionPath,
		},
	}

	if cfg.AuthType == "user" {
		opts.UpdateHandler = gaps
		opts.Middlewares = []telegram.Middleware{
			updhook.UpdateHook(gaps.Handle),
		}
	}

	if cfg.MTProtoServer != "" {
		if strings.Contains(cfg.MTProtoServer, "149.154.167.40") || strings.Contains(strings.ToLower(cfg.MTProtoServer), "test") {
			opts.DCList = dcs.Test()
		}
	}

	client := telegram.NewClient(appID, appHash, opts)
	eng.setGotdClient(client)
	eng.gotdCtx = ctx
	eng.gotdCancel = cancel

	if cfg.AuthType == "user" {
		// Register gotd commands and updates for user mode
		d.OnNewMessage(func(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage) error {
			eng.Dispatch(func() {
				if err := eng.handleUserMessage(ctx, entities, u); err != nil {
					logger.Error("Telegram", "Failed to handle user message", "error", err)
				}
			})
			return nil
		})

		d.OnBotCallbackQuery(func(ctx context.Context, entities tg.Entities, u *tg.UpdateBotCallbackQuery) error {
			eng.Dispatch(func() {
				if err := eng.handleUserCallbackQuery(ctx, entities, u); err != nil {
					logger.Error("Telegram", "Failed to handle user callback query", "error", err)
				}
			})
			return nil
		})
	}

	// Start the gotd client under a self-healing supervisor.
	//
	// gotd reconnects individual connections automatically, but client.Run can
	// still exit on fatal errors (session/auth failures, unrecoverable MTProto
	// state). A dead client would strand every in-flight transfer on
	// "engine forcibly closed" errors until a human restarts the engine, so
	// the supervisor transparently replaces the client (a fresh instance over
	// the same persisted session) with capped exponential backoff until the
	// engine itself is stopped. Because the engine-wide request context
	// survives client replacements, in-flight uploads/downloads keep their
	// progress across a crash (upload file IDs are session-scoped, not
	// client-scoped).
	eng.running.Store(true)
	gotdErrChan := make(chan error, 1)
	clientReady := &atomic.Bool{}

	runOne := func(c *telegram.Client) error {
		return c.Run(ctx, func(runCtx context.Context) error {
			// Perform bot authentication if in bot mode
			if cfg.AuthType == "bot" {
				status, err := c.Auth().Status(runCtx)
				if err != nil {
					return err
				}
				if !status.Authorized {
					logger.Info("Telegram", "Authenticating gotd client as bot via MTProto...")
					_, err = c.Auth().Bot(runCtx, cfg.BotToken)
					if err != nil {
						return err
					}
				}
			}

			self, err := c.Self(runCtx)
			if err != nil {
				return err
			}

			eng.mu.Lock()
			if cfg.AuthType == "user" {
				eng.meUsername = self.Username
				eng.meID = self.ID
				eng.meFirstName = self.FirstName
			}
			eng.mu.Unlock()

			logger.Info("Telegram", "MTProto client running",
				"username", self.Username,
				"id", self.ID,
				"mode", cfg.AuthType,
			)

			clientReady.Store(true)

			// Signal success
			select {
			case gotdErrChan <- nil:
			default:
			}

			<-runCtx.Done()
			return nil
		})
	}

	go func() {
		const maxBackoff = 30 * time.Second
		backoff := time.Second
		for iteration := 0; ; iteration++ {
			var c *telegram.Client
			if iteration == 0 {
				c = client
			} else {
				c = telegram.NewClient(appID, appHash, opts)
				eng.setGotdClient(c)
				logger.Warn("Telegram", "MTProto client restarted after failure", "backoff", backoff)
			}

			err := runOne(c)
			if ctx.Err() != nil {
				return // engine stopped — clean exit
			}

			// The very first run failed before the client ever became ready
			// (bad token, missing/revoked session, …) — a startup error.
			// Surface it to StartEngine and give up, exactly like the old
			// single-shot behavior did.
			if iteration == 0 && !clientReady.Load() {
				select {
				case gotdErrChan <- err:
				default:
				}
				return
			}

			logger.Error("Telegram", "MTProto client run failed, restarting",
				"error", err,
				"backoff", backoff,
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
	}()

	// Wait for gotd client to start and connect
	select {
	case err := <-gotdErrChan:
		if err != nil {
			cancel()
			return err
		}
	case <-time.After(15 * time.Second):
		cancel()
		return fmt.Errorf("timeout waiting for MTProto client to start")
	}

	if cfg.AuthType == "bot" {
		// Bot Token mode (original telebot)
		if cfg.BotToken == "" {
			cancel()
			return fmt.Errorf("telegram bot token is empty")
		}

		pref := tele.Settings{
			Token:  cfg.BotToken,
			Poller: &tele.LongPoller{Timeout: time.Duration(cfg.PollingInterval) * time.Second},
		}

		bot, err := tele.NewBot(pref)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to create telebot: %w", err)
		}

		eng.Bot = bot
		eng.meUsername = bot.Me.Username
		eng.meID = bot.Me.ID
		eng.meFirstName = bot.Me.FirstName

		// Register telebot handlers and middleware
		eng.registerMiddleware()
		eng.registerCommands()

		go func() {
			logger.Info("Telegram", "Bot polling started", "username", bot.Me.Username)
			bot.Start()
		}()
	}

	// Spin up worker goroutines — one per CPU core
	numWorkers := runtime.NumCPU()
	for i := 0; i < numWorkers; i++ {
		eng.wg.Add(1)
		go eng.worker(i)
	}

	instance = eng

	logger.Info("Telegram", "Telegram engine started successfully",
		"username", eng.meUsername,
		"id", eng.meID,
		"workers", numWorkers,
	)

	return nil
}

// StopEngine gracefully shuts down the running bot engine.
func StopEngine() error {
	mu.Lock()
	defer mu.Unlock()

	if instance == nil || !instance.running.Load() {
		return fmt.Errorf("telegram bot engine is not running")
	}

	instance.shutdown()
	logger.Info("Telegram", "Telegram bot engine stopped")
	instance = nil
	return nil
}

// shutdown performs the actual teardown.
func (e *Engine) shutdown() {
	e.running.Store(false)
	e.cancel()
	if e.Bot != nil {
		e.Bot.Stop()
	}
	if e.gotdCancel != nil {
		e.gotdCancel()
	}
	close(e.workerPool)
	e.wg.Wait()
}

// worker is a goroutine that pulls jobs from the pool and executes them.
func (e *Engine) worker(id int) {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case job, ok := <-e.workerPool:
			if !ok {
				return
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						e.errors.Add(1)
						logger.Error("Telegram", "Worker panic recovered",
							"worker_id", id,
							"panic", fmt.Sprintf("%v", r),
						)
					}
				}()
				job()
			}()
		}
	}
}

// Dispatch submits a job to the worker pool for parallel execution.
// If the pool is full, it logs a warning and drops the job to prevent blocking.
func (e *Engine) Dispatch(job func()) {
	select {
	case e.workerPool <- job:
		// submitted
	default:
		e.errors.Add(1)
		logger.Warn("Telegram", "Worker pool is full — dropping job")
	}
}

// IsAdmin checks whether a Telegram user ID is in the admin list.
func (e *Engine) IsAdmin(userID int64) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, id := range parseAdminIDs(e.Config.AdminUserIDs) {
		if id == userID {
			return true
		}
	}
	return false
}

// ReloadConfig fetches the latest config from the database and hot-reloads it.
func (e *Engine) ReloadConfig() error {
	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		return fmt.Errorf("failed to reload telegram config: %w", err)
	}
	e.mu.Lock()
	e.Config = &cfg
	e.mu.Unlock()
	logger.Info("Telegram", "Configuration hot-reloaded")
	return nil
}

// Stats returns engine runtime metrics.
func (e *Engine) Stats() map[string]interface{} {
	e.mu.RLock()
	meUsername := e.meUsername
	meID := e.meID
	e.mu.RUnlock()

	return map[string]interface{}{
		"running":            e.running.Load(),
		"uptime_seconds":     int(time.Since(e.startedAt).Seconds()),
		"workers":            runtime.NumCPU(),
		"messages_processed": e.messagesProcessed.Load(),
		"commands_processed": e.commandsProcessed.Load(),
		"files_sent":         e.filesSent.Load(),
		"errors":             e.errors.Load(),
		"bot_username":       meUsername,
		"bot_id":             meID,
	}
}

// parseAdminIDs splits a comma-separated string of Telegram user IDs.
func parseAdminIDs(raw string) []int64 {
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var id int64
		if _, err := fmt.Sscanf(p, "%d", &id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
