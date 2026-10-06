package telegram

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/downloader"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	tele "gopkg.in/telebot.v4"
)

// registerCommands wires all bot commands and callback handlers.
func (e *Engine) registerCommands() {

	// ────────────────── /start ──────────────────
	e.Bot.Handle("/start", func(c tele.Context) error {
		e.commandsProcessed.Add(1)

		// Register or activate subscriber
		var sub models.TelegramSubscriber
		if err := db.DB.Where("chat_id = ?", c.Chat().ID).First(&sub).Error; err != nil {
			sub = models.TelegramSubscriber{
				ChatID:    c.Chat().ID,
				Username:  c.Sender().Username,
				FirstName: c.Sender().FirstName,
				Active:    true,
			}
			db.DB.Create(&sub)
		} else {
			db.DB.Model(&sub).Updates(map[string]interface{}{
				"active":     true,
				"username":   c.Sender().Username,
				"first_name": c.Sender().FirstName,
			})
		}

		e.mu.RLock()
		welcome := e.Config.WelcomeMessage
		e.mu.RUnlock()

		if welcome == "" {
			welcome = "👋 Welcome to *CleverConnect Bot*!\n\nUse /help to see available commands."
		}

		// Replace placeholders
		welcome = strings.ReplaceAll(welcome, "{name}", c.Sender().FirstName)
		welcome = strings.ReplaceAll(welcome, "{username}", c.Sender().Username)

		return c.Send(welcome, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	})

	// ────────────────── /stop ──────────────────
	e.Bot.Handle("/stop", func(c tele.Context) error {
		e.commandsProcessed.Add(1)

		var sub models.TelegramSubscriber
		if err := db.DB.Where("chat_id = ?", c.Chat().ID).First(&sub).Error; err == nil {
			db.DB.Model(&sub).Update("active", false)
		}

		return c.Send("❌ *You have stopped the bot.*\n\nYou will no longer receive system notification broadcasts. Use `/start` to resubscribe at any time.", &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	})

	// ────────────────── /help ──────────────────
	e.Bot.Handle("/help", func(c tele.Context) error {
		e.commandsProcessed.Add(1)

		isAdmin := e.IsAdmin(c.Sender().ID)

		help := "🤖 *CleverConnect Bot Commands*\n\n"
		help += "📌 `/start` — Welcome message & subscribe\n"
		help += "🛑 `/stop` — Unsubscribe from broadcasts\n"
		help += "❓ `/help` — This help menu\n"
		help += "📊 `/status` — Bot & server status\n"
		help += "🆔 `/myid` — Get your Telegram user ID\n"

		if isAdmin {
			help += "\n🔐 *Admin Commands:*\n"
			help += "📁 `/files` — Browse server files\n"
			help += "⚙️ `/settings` — View bot configuration\n"
			help += "🔄 `/reload` — Hot-reload config from DB\n"
		}

		return c.Send(help, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	})

	// ────────────────── /myid ──────────────────
	e.Bot.Handle("/myid", func(c tele.Context) error {
		e.commandsProcessed.Add(1)
		msg := fmt.Sprintf("🆔 Your Telegram User ID: `%d`\n👤 Username: @%s",
			c.Sender().ID, c.Sender().Username)
		return c.Send(msg, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	})

	// ────────────────── /status ──────────────────
	e.Bot.Handle("/status", func(c tele.Context) error {
		e.commandsProcessed.Add(1)

		uptime := time.Since(e.startedAt)
		stats := fmt.Sprintf(
			"📊 *CleverConnect Bot Status*\n\n"+
				"🟢 *Status:* Online\n"+
				"⏱ *Uptime:* %s\n"+
				"🧵 *Workers:* %d (all CPU cores)\n"+
				"📨 *Messages Processed:* %d\n"+
				"⚡ *Commands Processed:* %d\n"+
				"📁 *Files Sent:* %d\n"+
				"❌ *Errors:* %d\n"+
				"🤖 *Bot:* @%s",
			formatUptime(uptime),
			runtime.NumCPU(),
			e.messagesProcessed.Load(),
			e.commandsProcessed.Load(),
			e.filesSent.Load(),
			e.errors.Load(),
			e.Bot.Me.Username,
		)

		// Fetch active downloads
		var activeDownloads []models.LeechJob
		if err := db.DB.Where("status = ?", "downloading").Find(&activeDownloads).Error; err == nil && len(activeDownloads) > 0 {
			stats += "\n\n📥 *Active Downloads:*"
			for _, job := range activeDownloads {
				stats += fmt.Sprintf("\n• `%s`: %.1f%% (⚡ %.1f MB/s)", job.Filename, job.Progress, job.Speed)
			}
		}

		// Fetch active uploads (telegram upload jobs)
		var activeUploads []models.SchedulerJob
		if err := db.DB.Where("job_type = ? AND status = ?", "telegram_upload", "running").Find(&activeUploads).Error; err == nil && len(activeUploads) > 0 {
			stats += "\n\n📤 *Active Uploads:*"
			for _, job := range activeUploads {
				stats += fmt.Sprintf("\n• `%s`: %d%%", job.Name, job.Progress)
			}
		}

		return c.Send(stats, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	})

	// ────────────────── /settings (Admin only) ──────────────────
	e.Bot.Handle("/settings", e.AdminOnly(func(c tele.Context) error {
		e.commandsProcessed.Add(1)

		e.mu.RLock()
		cfg := e.Config
		e.mu.RUnlock()

		adminIDs := cfg.AdminUserIDs
		if adminIDs == "" {
			adminIDs = "(none configured)"
		}

		features := []string{}
		if cfg.EnableFileSharing {
			features = append(features, "📁 File Sharing")
		}
		if cfg.EnableNotifications {
			features = append(features, "🔔 Notifications")
		}
		if len(features) == 0 {
			features = append(features, "None enabled")
		}

		msg := fmt.Sprintf(
			"⚙️ *Bot Configuration*\n\n"+
				"🤖 *Bot Username:* @%s\n"+
				"👥 *Admin IDs:* `%s`\n"+
				"⏱ *Polling Interval:* %ds\n"+
				"🎯 *Enabled Features:*\n%s\n"+
				"📝 *Welcome Message:*\n_%s_",
			e.Bot.Me.Username,
			adminIDs,
			cfg.PollingInterval,
			strings.Join(features, "\n"),
			truncate(cfg.WelcomeMessage, 100),
		)
		return c.Send(msg, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	}))

	// ────────────────── /reload (Admin only) ──────────────────
	e.Bot.Handle("/reload", e.AdminOnly(func(c tele.Context) error {
		e.commandsProcessed.Add(1)

		if err := e.ReloadConfig(); err != nil {
			logger.Error("Telegram", "Config reload failed", "error", err)
			return c.Send("❌ Failed to reload configuration: " + err.Error())
		}
		return c.Send("✅ Configuration reloaded successfully from database.")
	}))

	// ────────────────── /files (Admin only) ──────────────────
	e.Bot.Handle("/files", e.AdminOnly(func(c tele.Context) error {
		e.commandsProcessed.Add(1)
		return e.handleFileBrowse(c, "/")
	}))

	// ────────────────── Callback query router (for inline keyboards) ──────────────────
	e.Bot.Handle(tele.OnCallback, func(c tele.Context) error {
		data := c.Callback().Data
		logger.Info("Telegram", "Callback received", "data", data, "user_id", c.Sender().ID)

		switch {
		case strings.HasPrefix(data, "fb:"):
			// File browser navigation
			if !e.IsAdmin(c.Sender().ID) {
				return c.Respond(&tele.CallbackResponse{Text: "⛔ Admin only"})
			}
			path := strings.TrimPrefix(data, "fb:")
			return e.handleFileBrowse(c, path)

		case strings.HasPrefix(data, "send:"):
			// Send file to chat
			if !e.IsAdmin(c.Sender().ID) {
				return c.Respond(&tele.CallbackResponse{Text: "⛔ Admin only"})
			}
			filePath := strings.TrimPrefix(data, "send:")
			// Dispatch file sending to worker pool for parallel processing
			e.Dispatch(func() {
				if err := e.sendFileToChat(c, filePath); err != nil {
					logger.Error("Telegram", "Failed to send file", "path", filePath, "error", err)
				}
			})
			return c.Respond(&tele.CallbackResponse{Text: "📤 Sending file..."})

		case strings.HasPrefix(data, "restart_job:"):
			if !e.IsAdmin(c.Sender().ID) {
				return c.Respond(&tele.CallbackResponse{Text: "⛔ Admin only"})
			}
			jobIDStr := strings.TrimPrefix(data, "restart_job:")
			jobID, err := strconv.ParseUint(jobIDStr, 10, 64)
			if err != nil {
				return c.Respond(&tele.CallbackResponse{Text: "❌ Invalid job ID"})
			}

			if RetryJob == nil {
				return c.Respond(&tele.CallbackResponse{Text: "❌ Scheduler callback not registered"})
			}

			if err := RetryJob(uint(jobID)); err != nil {
				return c.Respond(&tele.CallbackResponse{Text: fmt.Sprintf("❌ Failed: %v", err)})
			}

			return c.Respond(&tele.CallbackResponse{Text: "🔄 Job restarted!"})

		default:
			return c.Respond(&tele.CallbackResponse{Text: "Unknown action"})
		}
	})

	// ────────────────── Register bot commands menu ──────────────────
	commands := []tele.Command{
		{Text: "start", Description: "Welcome message"},
		{Text: "help", Description: "Show available commands"},
		{Text: "status", Description: "Bot & server status"},
		{Text: "myid", Description: "Get your Telegram user ID"},
		{Text: "files", Description: "Browse server files (admin)"},
		{Text: "settings", Description: "View bot config (admin)"},
		{Text: "reload", Description: "Reload config from DB (admin)"},
	}

	if err := e.Bot.SetCommands(commands); err != nil {
		logger.Warn("Telegram", "Failed to set bot commands menu", "error", err)
	}

	// ────────────────── OnText Message Handler (Admin only) ──────────────────
	e.Bot.Handle(tele.OnText, e.AdminOnly(func(c tele.Context) error {
		e.messagesProcessed.Add(1)
		text := strings.TrimSpace(c.Text())
		if text == "" {
			return nil
		}

		// Ignore standard slash commands
		if strings.HasPrefix(text, "/") {
			return nil
		}

		// Extract any URL starting with http:// or https://
		var downloadURL string
		for _, word := range strings.Fields(text) {
			if strings.HasPrefix(word, "http://") || strings.HasPrefix(word, "https://") {
				downloadURL = word
				break
			}
		}

		if downloadURL != "" {
			return e.handleDownloadLink(c, downloadURL)
		}

		return c.Send("ℹ️ Send a valid HTTP/HTTPS link to download it directly to the server file manager.")
	}))

	// Media message handlers (admin only)
	mediaHandler := e.AdminOnly(func(c tele.Context) error {
		return e.handleMediaMessage(c)
	})

	e.Bot.Handle(tele.OnDocument, mediaHandler)
	e.Bot.Handle(tele.OnVideo, mediaHandler)
	e.Bot.Handle(tele.OnAudio, mediaHandler)
	e.Bot.Handle(tele.OnPhoto, mediaHandler)
	e.Bot.Handle(tele.OnVoice, mediaHandler)
	e.Bot.Handle(tele.OnAnimation, mediaHandler)

	// Seed default Telegram config if none exists
	seedTelegramConfig()
}

// handleDownloadLink queues a file download from a given URL via the download manager.
func (e *Engine) handleDownloadLink(c tele.Context, downloadURL string) error {
	if downloader.Manager == nil {
		return c.Send("❌ Downloader engine is not running on the server.")
	}

	jobID, err := downloader.Manager.AddJob(
		downloadURL,
		"",    // default save directory
		"",    // extract filename from URL
		"",    // username
		"",    // password
		4,     // threads
		false, // usePremium
	)
	if err != nil {
		return c.Send("❌ Failed to queue download: " + err.Error())
	}

	return c.Send(fmt.Sprintf("📥 *Download Queued!*\n\nJob ID: `%s`\nLink: %s\n\nUse `/status` to monitor the download progress.", jobID, downloadURL), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

// seedTelegramConfig ensures a default config row exists in the database.
func seedTelegramConfig() {
	var cfg models.TelegramConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		logger.Info("Telegram", "Seeding default Telegram bot configuration")
		db.DB.Create(&models.TelegramConfig{
			PollingInterval:     10,
			MaxFileSize:         2000,
			EnableFileSharing:   true,
			EnableNotifications: true,
			WelcomeMessage:      "👋 Welcome to *CleverConnect Bot*, {name}!\n\nUse /help to see available commands.",
		})
	} else if cfg.MaxFileSize == 50 {
		logger.Info("Telegram", "Upgrading default MaxFileSize from 50MB to 2000MB")
		db.DB.Model(&cfg).Update("max_file_size", 2000)
	}
}

// ──────────────────────────────────────────────────────────────
// User Account / MTProto Command Handlers and Helpers
// ──────────────────────────────────────────────────────────────

func mdToHTML(s string) string {
	s = replacePair(s, "`", "<code>", "</code>")
	s = replacePair(s, "*", "<b>", "</b>")
	s = replacePair(s, "_", "<i>", "</i>")
	return s
}

func replacePair(s, placeholder, startTag, endTag string) string {
	for {
		i1 := strings.Index(s, placeholder)
		if i1 == -1 {
			break
		}
		rest := s[i1+len(placeholder):]
		i2 := strings.Index(rest, placeholder)
		if i2 == -1 {
			break
		}
		s = s[:i1] + startTag + rest[:i2] + endTag + rest[i2+len(placeholder):]
	}
	return s
}

func getPeerInput(peer tg.PeerClass, entities tg.Entities) tg.InputPeerClass {
	switch p := peer.(type) {
	case *tg.PeerUser:
		if user, ok := entities.Users[p.UserID]; ok {
			return &tg.InputPeerUser{
				UserID:     user.ID,
				AccessHash: user.AccessHash,
			}
		}
		return &tg.InputPeerUser{UserID: p.UserID}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		if ch, ok := entities.Channels[p.ChannelID]; ok {
			return &tg.InputPeerChannel{
				ChannelID:  ch.ID,
				AccessHash: ch.AccessHash,
			}
		}
		return &tg.InputPeerChannel{ChannelID: p.ChannelID}
	}
	return &tg.InputPeerSelf{}
}

func (e *Engine) sendUserMessage(ctx context.Context, entities tg.Entities, peer tg.PeerClass, text string) error {
	inputPeer := getPeerInput(peer, entities)
	sender := message.NewSender(floodSafeClient("user-message", e.currentClient()))
	htmlText := mdToHTML(text)
	_, err := sender.To(inputPeer).StyledText(ctx, html.String(nil, htmlText))
	return err
}

func (e *Engine) handleUserCallbackQuery(ctx context.Context, entities tg.Entities, u *tg.UpdateBotCallbackQuery) error {
	api := floodSafeClient("user-callback", e.currentClient())

	data := string(u.Data)
	logger.Info("Telegram", "Callback received (MTProto)", "data", data, "user_id", u.UserID)

	switch {
	case strings.HasPrefix(data, "fb:"):
		_, _ = api.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
			QueryID: u.QueryID,
		})
		if !e.IsAdmin(u.UserID) {
			return nil
		}
		path := strings.TrimPrefix(data, "fb:")
		return e.handleFileBrowseUser(ctx, entities, u.Peer, u.MsgID, path)

	case strings.HasPrefix(data, "send:"):
		_, _ = api.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
			QueryID: u.QueryID,
		})
		if !e.IsAdmin(u.UserID) {
			return nil
		}
		filePath := strings.TrimPrefix(data, "send:")
		e.Dispatch(func() {
			if err := e.sendFileToChatUser(ctx, entities, u.Peer, filePath); err != nil {
				logger.Error("Telegram", "Failed to send file", "path", filePath, "error", err)
			}
		})
		return nil

	case strings.HasPrefix(data, "restart_job:"):
		if !e.IsAdmin(u.UserID) {
			_, _ = api.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
				QueryID: u.QueryID,
				Message: "⛔ Admin only",
				Alert:   true,
			})
			return nil
		}
		jobIDStr := strings.TrimPrefix(data, "restart_job:")
		jobID, err := strconv.ParseUint(jobIDStr, 10, 64)
		if err != nil {
			_, _ = api.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
				QueryID: u.QueryID,
				Message: "❌ Invalid job ID",
				Alert:   true,
			})
			return nil
		}

		if RetryJob == nil {
			_, _ = api.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
				QueryID: u.QueryID,
				Message: "❌ Scheduler callback not registered",
				Alert:   true,
			})
			return nil
		}

		if err := RetryJob(uint(jobID)); err != nil {
			_, _ = api.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
				QueryID: u.QueryID,
				Message: fmt.Sprintf("❌ Failed: %v", err),
				Alert:   true,
			})
			return nil
		}

		_, _ = api.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
			QueryID: u.QueryID,
			Message: "🔄 Job restarted!",
			Alert:   false,
		})
		return nil
	}

	return nil
}

func (e *Engine) handleFileBrowseUser(ctx context.Context, entities tg.Entities, peer tg.PeerClass, messageID int, dirPath string) error {
	safePath, err := securePath(dirPath)
	if err != nil {
		return e.sendUserMessage(ctx, entities, peer, "⛔ Access denied: invalid path.")
	}

	entries, err := os.ReadDir(safePath)
	if err != nil {
		return e.sendUserMessage(ctx, entities, peer, "❌ Failed to read directory: "+err.Error())
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	displayPath := filepath.Clean("/" + dirPath)
	if displayPath == "." {
		displayPath = "/"
	}

	text := fmt.Sprintf("📁 *File Browser*\n📂 `%s`\n\n", displayPath)
	if len(entries) == 0 {
		text += "_Empty directory_"
	} else {
		text += fmt.Sprintf("_%d items_", len(entries))
	}

	var kbRows []tg.KeyboardButtonRow

	if dirPath != "/" && dirPath != "" {
		parentPath := filepath.Dir(dirPath)
		if parentPath == "." {
			parentPath = "/"
		}
		kbRows = append(kbRows, tg.KeyboardButtonRow{
			Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonCallback{
					Text: "⬆️ Parent Directory",
					Data: []byte("fb:" + parentPath),
				},
			},
		})
	}

	maxItems := 30
	if len(entries) < maxItems {
		maxItems = len(entries)
	}

	for _, entry := range entries[:maxItems] {
		name := entry.Name()
		entryPath := filepath.Join(dirPath, name)
		if dirPath == "/" {
			entryPath = "/" + name
		}

		if entry.IsDir() {
			kbRows = append(kbRows, tg.KeyboardButtonRow{
				Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonCallback{
						Text: "📂 " + name,
						Data: []byte("fb:" + entryPath),
					},
				},
			})
		} else {
			info, _ := entry.Info()
			sizeStr := formatFileSize(info.Size())
			icon := getFileIcon(name)
			kbRows = append(kbRows, tg.KeyboardButtonRow{
				Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonCallback{
						Text: fmt.Sprintf("%s %s (%s)", icon, name, sizeStr),
						Data: []byte("send:" + entryPath),
					},
				},
			})
		}
	}

	if len(entries) > 30 {
		text += fmt.Sprintf("\n\n⚠️ _Showing first 30 of %d items_", len(entries))
	}

	inputPeer := getPeerInput(peer, entities)
	kbMarkup := &tg.ReplyInlineMarkup{Rows: kbRows}
	htmlText := mdToHTML(text)

	api := floodSafeClient("user-browser", e.currentClient())
	if messageID != 0 {
		// Editing via the raw RPC would render the HTML tags literally (MTProto
		// has no parse mode); the message builder parses them into entities.
		_, err = message.NewSender(api).To(inputPeer).Markup(kbMarkup).Edit(messageID).StyledText(ctx, html.String(nil, htmlText))
	} else {
		sender := message.NewSender(api)
		_, err = sender.To(inputPeer).Markup(kbMarkup).StyledText(ctx, html.String(nil, htmlText))
	}

	return err
}

func (e *Engine) sendFileToChatUser(ctx context.Context, entities tg.Entities, peer tg.PeerClass, filePath string) error {
	safePath, err := securePath(filePath)
	if err != nil {
		return e.sendUserMessage(ctx, entities, peer, "⛔ Access denied.")
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return e.sendUserMessage(ctx, entities, peer, "❌ File not found: "+filepath.Base(filePath))
	}

	if info.IsDir() {
		return e.sendUserMessage(ctx, entities, peer, "📂 That's a directory, not a file.")
	}

	e.mu.RLock()
	maxSizeMB := e.Config.MaxFileSize
	e.mu.RUnlock()
	if maxSizeMB <= 0 {
		maxSizeMB = 2000
	}
	maxBytes := int64(maxSizeMB) * 1024 * 1024

	if info.Size() > maxBytes {
		return e.sendUserMessage(ctx, entities, peer, fmt.Sprintf("❌ File too large (%s). Maximum allowed: %dMB.",
			formatFileSize(info.Size()), maxSizeMB))
	}

	var chatID int64
	switch p := peer.(type) {
	case *tg.PeerUser:
		chatID = p.UserID
	case *tg.PeerChat:
		chatID = p.ChatID
	case *tg.PeerChannel:
		chatID = p.ChannelID
	}

	if info.Size() > 10*1024*1024 {
		if QueueUploadJob != nil {
			err := QueueUploadJob(filePath, chatID)
			if err != nil {
				return e.sendUserMessage(ctx, entities, peer, "❌ Failed to queue background upload: "+err.Error())
			}
			return nil
		}
	}

	fileName := filepath.Base(safePath)

	// Make the media instantly playable in Telegram (faststart remux for MP4,
	// MKV/AVI/... → MP4 container conversion) before uploading. Falls back to
	// the original file whenever the conversion is impossible.
	if prepPath, prepName, prepCleanup := preparePlayableMedia(safePath, fileName, nil); prepPath != safePath {
		safePath = prepPath
		fileName = prepName
		defer prepCleanup()
		if st, statErr := os.Stat(safePath); statErr == nil {
			info = st
		}
	}

	caption := fmt.Sprintf("📁 %s\n📏 %s", filePath, formatFileSize(info.Size()))
	ext := strings.ToLower(filepath.Ext(fileName))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	inputPeer := getPeerInput(peer, entities)
	// Send requests are retried on fresh connections the same way as the
	// upload below (transient transport failures only).
	api := tg.NewClient(newRetryingInvoker("command-upload",
		func() (tg.Invoker, error) { return liveClient(e.currentClient()), nil }, nil))

	// Upload through the shared fast multi-connection uploader — the same
	// code path the scheduled upload jobs use — then send the result with
	// the command API client.
	fileObj, err := UploadFile(ctx, e.currentClient(), safePath, nil)
	if err != nil {
		return e.sendUserMessage(ctx, entities, peer, "❌ Failed to upload file: "+err.Error())
	}

	sender := message.NewSender(api)

	var sendErr error
	var mediaOption message.MediaOption

	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif":
		mediaOption = message.UploadedPhoto(fileObj, styling.Plain(caption))

	case ".mp4", ".mkv", ".webm", ".avi", ".mov", ".m4v", ".flv", ".wmv":
		w, h, duration, _, _, _, _, _ := probeMediaMetadata(safePath)
		mimeStr := "video/mp4"
		switch ext {
		case ".mkv":
			mimeStr = "video/x-matroska"
		case ".webm":
			mimeStr = "video/webm"
		case ".mov":
			mimeStr = "video/quicktime"
		case ".avi":
			mimeStr = "video/x-msvideo"
		}
		videoBuilder := message.UploadedDocument(fileObj, styling.Plain(caption)).MIME(mimeStr).Filename(fileName).Video()
		if w > 0 && h > 0 {
			videoBuilder = videoBuilder.Resolution(w, h)
		}
		if duration > 0 {
			videoBuilder = videoBuilder.DurationSeconds(duration)
		}
		mediaOption = videoBuilder.SupportsStreaming()

	case ".mp3", ".m4a", ".flac", ".wav", ".aac", ".wma":
		_, _, duration, _, _, _, title, artist := probeMediaMetadata(safePath)
		audioBuilder := message.UploadedDocument(fileObj, styling.Plain(caption)).MIME(mimeType).Filename(fileName).Audio()
		if duration > 0 {
			audioBuilder = audioBuilder.DurationSeconds(duration)
		}
		if title != "" {
			audioBuilder = audioBuilder.Title(title)
		} else {
			audioBuilder = audioBuilder.Title(strings.TrimSuffix(fileName, filepath.Ext(fileName)))
		}
		if artist != "" {
			audioBuilder = audioBuilder.Performer(artist)
		}
		mediaOption = audioBuilder

	case ".ogg", ".opus":
		_, _, duration, _, _, _, _, _ := probeMediaMetadata(safePath)
		voiceBuilder := message.UploadedDocument(fileObj, styling.Plain(caption)).MIME(mimeType).Filename(fileName).Audio().Voice()
		if duration > 0 {
			voiceBuilder = voiceBuilder.DurationSeconds(duration)
		}
		mediaOption = voiceBuilder

	default:
		doc := message.UploadedDocument(fileObj, styling.Plain(caption))
		doc.MIME(mimeType).Filename(fileName)
		mediaOption = doc
	}

	_, sendErr = sender.To(inputPeer).Media(ctx, mediaOption)

	if sendErr != nil {
		e.errors.Add(1)
		logger.Error("Telegram", "Failed to send file directly via MTProto", "path", filePath, "error", sendErr)
		return e.sendUserMessage(ctx, entities, peer, "❌ Failed to send file: "+sendErr.Error())
	}

	e.filesSent.Add(1)
	return nil
}

func (e *Engine) handleDownloadLinkUser(ctx context.Context, entities tg.Entities, peer tg.PeerClass, downloadURL string) error {
	if downloader.Manager == nil {
		return e.sendUserMessage(ctx, entities, peer, "❌ Downloader engine is not running on the server.")
	}

	jobID, err := downloader.Manager.AddJob(
		downloadURL,
		"",    // default save directory
		"",    // extract filename from URL
		"",    // username
		"",    // password
		4,     // threads
		false, // usePremium
	)
	if err != nil {
		return e.sendUserMessage(ctx, entities, peer, "❌ Failed to queue download: "+err.Error())
	}

	return e.sendUserMessage(ctx, entities, peer, fmt.Sprintf("📥 *Download Queued!*\n\nJob ID: `%s`\nLink: %s\n\nUse `/status` to monitor the download progress.", jobID, downloadURL))
}

func (e *Engine) handleUserMessage(ctx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
	m, ok := update.Message.(*tg.Message)
	if !ok || m.Out {
		return nil
	}

	// Extract sender details
	var senderID int64
	switch p := m.PeerID.(type) {
	case *tg.PeerUser:
		senderID = p.UserID
	case *tg.PeerChat:
		senderID = p.ChatID
	case *tg.PeerChannel:
		senderID = p.ChannelID
	}

	// Intercept media files from admin
	if m.Media != nil && e.IsAdmin(senderID) {
		e.messagesProcessed.Add(1)
		e.Dispatch(func() {
			if err := e.handleMediaMessageUser(ctx, entities, m.PeerID, m); err != nil {
				logger.Error("Telegram", "Failed to handle media message user", "error", err)
			}
		})
		return nil
	}

	text := strings.TrimSpace(m.Message)
	if text == "" {
		return nil
	}

	e.messagesProcessed.Add(1)
	logger.Info("Telegram", "Incoming update (MTProto)", "user_id", senderID, "text", truncate(text, 80))

	if !strings.HasPrefix(text, "/myid") && !e.IsAdmin(senderID) {
		logger.Warn("Telegram", "Unauthorized access blocked (MTProto)", "user_id", senderID, "text", text)
		return e.sendUserMessage(ctx, entities, m.PeerID, "⛔ Access denied. You are not an authorized administrator.")
	}

	switch {
	case strings.HasPrefix(text, "/start"):
		e.commandsProcessed.Add(1)

		var sub models.TelegramSubscriber
		if err := db.DB.Where("chat_id = ?", senderID).First(&sub).Error; err != nil {
			sub = models.TelegramSubscriber{
				ChatID: senderID,
				Active: true,
			}
			if p, ok := m.PeerID.(*tg.PeerUser); ok {
				if user, ok := entities.Users[p.UserID]; ok {
					sub.Username = user.Username
					sub.FirstName = user.FirstName
				}
			}
			db.DB.Create(&sub)
		} else {
			updatesMap := map[string]interface{}{"active": true}
			if p, ok := m.PeerID.(*tg.PeerUser); ok {
				if user, ok := entities.Users[p.UserID]; ok {
					updatesMap["username"] = user.Username
					updatesMap["first_name"] = user.FirstName
				}
			}
			db.DB.Model(&sub).Updates(updatesMap)
		}

		e.mu.RLock()
		welcome := e.Config.WelcomeMessage
		e.mu.RUnlock()

		if welcome == "" {
			welcome = "👋 Welcome to *CleverConnect Bot*!\n\nUse /help to see available commands."
		}

		firstName := ""
		username := ""
		if p, ok := m.PeerID.(*tg.PeerUser); ok {
			if user, ok := entities.Users[p.UserID]; ok {
				firstName = user.FirstName
				username = user.Username
			}
		}
		welcome = strings.ReplaceAll(welcome, "{name}", firstName)
		welcome = strings.ReplaceAll(welcome, "{username}", username)

		return e.sendUserMessage(ctx, entities, m.PeerID, welcome)

	case strings.HasPrefix(text, "/stop"):
		e.commandsProcessed.Add(1)
		var sub models.TelegramSubscriber
		if err := db.DB.Where("chat_id = ?", senderID).First(&sub).Error; err == nil {
			db.DB.Model(&sub).Update("active", false)
		}
		return e.sendUserMessage(ctx, entities, m.PeerID, "❌ *You have stopped the bot.*\n\nYou will no longer receive system notification broadcasts. Use `/start` to resubscribe at any time.")

	case strings.HasPrefix(text, "/help"):
		e.commandsProcessed.Add(1)
		help := "🤖 *CleverConnect Bot Commands*\n\n"
		help += "📌 `/start` — Welcome message & subscribe\n"
		help += "🛑 `/stop` — Unsubscribe from broadcasts\n"
		help += "❓ `/help` — This help menu\n"
		help += "📊 `/status` — Bot & server status\n"
		help += "🆔 `/myid` — Get your Telegram user ID\n"
		help += "\n🔐 *Admin Commands:*\n"
		help += "📁 `/files` — Browse server files\n"
		help += "⚙️ `/settings` — View bot configuration\n"
		help += "🔄 `/reload` — Hot-reload config from DB\n"
		return e.sendUserMessage(ctx, entities, m.PeerID, help)

	case strings.HasPrefix(text, "/myid"):
		e.commandsProcessed.Add(1)
		username := ""
		if p, ok := m.PeerID.(*tg.PeerUser); ok {
			if user, ok := entities.Users[p.UserID]; ok {
				username = user.Username
			}
		}
		msg := fmt.Sprintf("🆔 Your Telegram User ID: `%d`\n👤 Username: @%s", senderID, username)
		return e.sendUserMessage(ctx, entities, m.PeerID, msg)

	case strings.HasPrefix(text, "/status"):
		e.commandsProcessed.Add(1)
		uptime := time.Since(e.startedAt)

		e.mu.RLock()
		meUsername := e.meUsername
		e.mu.RUnlock()

		stats := fmt.Sprintf(
			"📊 *CleverConnect Bot Status*\n\n"+
				"🟢 *Status:* Online\n"+
				"⏱ *Uptime:* %s\n"+
				"🧵 *Workers:* %d (all CPU cores)\n"+
				"📨 *Messages Processed:* %d\n"+
				"⚡ *Commands Processed:* %d\n"+
				"📁 *Files Sent:* %d\n"+
				"❌ *Errors:* %d\n"+
				"🤖 *Bot:* @%s",
			formatUptime(uptime),
			runtime.NumCPU(),
			e.messagesProcessed.Load(),
			e.commandsProcessed.Load(),
			e.filesSent.Load(),
			e.errors.Load(),
			meUsername,
		)

		var activeDownloads []models.LeechJob
		if err := db.DB.Where("status = ?", "downloading").Find(&activeDownloads).Error; err == nil && len(activeDownloads) > 0 {
			stats += "\n\n📥 *Active Downloads:*"
			for _, job := range activeDownloads {
				stats += fmt.Sprintf("\n• `%s`: %.1f%% (⚡ %.1f MB/s)", job.Filename, job.Progress, job.Speed)
			}
		}
		var activeUploads []models.SchedulerJob
		if err := db.DB.Where("job_type = ? AND status = ?", "telegram_upload", "running").Find(&activeUploads).Error; err == nil && len(activeUploads) > 0 {
			stats += "\n\n📤 *Active Uploads:*"
			for _, job := range activeUploads {
				stats += fmt.Sprintf("\n• `%s`: %d%%", job.Name, job.Progress)
			}
		}

		return e.sendUserMessage(ctx, entities, m.PeerID, stats)

	case strings.HasPrefix(text, "/settings"):
		e.commandsProcessed.Add(1)
		e.mu.RLock()
		cfg := e.Config
		e.mu.RUnlock()

		adminIDs := cfg.AdminUserIDs
		if adminIDs == "" {
			adminIDs = "(none configured)"
		}

		features := []string{}
		if cfg.EnableFileSharing {
			features = append(features, "📁 File Sharing")
		}
		if cfg.EnableNotifications {
			features = append(features, "🔔 Notifications")
		}
		if len(features) == 0 {
			features = append(features, "None enabled")
		}

		e.mu.RLock()
		meUsername := e.meUsername
		e.mu.RUnlock()

		msg := fmt.Sprintf(
			"⚙️ *Bot Configuration*\n\n"+
				"🤖 *Bot Username:* @%s\n"+
				"👥 *Admin IDs:* `%s`\n"+
				"⏱ *Polling Interval:* %ds\n"+
				"🎯 *Enabled Features:*\n%s\n"+
				"📝 *Welcome Message:*\n_%s_",
			meUsername,
			adminIDs,
			cfg.PollingInterval,
			strings.Join(features, "\n"),
			truncate(cfg.WelcomeMessage, 100),
		)
		return e.sendUserMessage(ctx, entities, m.PeerID, msg)

	case strings.HasPrefix(text, "/reload"):
		e.commandsProcessed.Add(1)
		if err := e.ReloadConfig(); err != nil {
			logger.Error("Telegram", "Config reload failed (MTProto)", "error", err)
			return e.sendUserMessage(ctx, entities, m.PeerID, "❌ Failed to reload configuration: "+err.Error())
		}
		return e.sendUserMessage(ctx, entities, m.PeerID, "✅ Configuration reloaded successfully from database.")

	case strings.HasPrefix(text, "/files"):
		e.commandsProcessed.Add(1)
		return e.handleFileBrowseUser(ctx, entities, m.PeerID, 0, "/")

	default:
		var downloadURL string
		for _, word := range strings.Fields(text) {
			if strings.HasPrefix(word, "http://") || strings.HasPrefix(word, "https://") {
				downloadURL = word
				break
			}
		}

		if downloadURL != "" {
			return e.handleDownloadLinkUser(ctx, entities, m.PeerID, downloadURL)
		}
	}
	return nil
}

// handleMediaMessage is the telebot (bot-mode) media file handler.
func (e *Engine) handleMediaMessage(c tele.Context) error {
	msg := c.Message()
	if msg == nil {
		return nil
	}

	var fileName string
	var fileSize int64
	var hasFile bool

	if msg.Document != nil {
		fileName = msg.Document.FileName
		fileSize = msg.Document.FileSize
		hasFile = true
	} else if msg.Video != nil {
		fileName = msg.Video.FileName
		if fileName == "" {
			fileName = fmt.Sprintf("video_%d.mp4", msg.ID)
		}
		fileSize = msg.Video.FileSize
		hasFile = true
	} else if msg.Audio != nil {
		fileName = msg.Audio.FileName
		if fileName == "" {
			fileName = fmt.Sprintf("audio_%d.mp3", msg.ID)
		}
		fileSize = msg.Audio.FileSize
		hasFile = true
	} else if msg.Voice != nil {
		fileName = fmt.Sprintf("voice_%d.ogg", msg.ID)
		fileSize = msg.Voice.FileSize
		hasFile = true
	} else if msg.Photo != nil {
		fileName = fmt.Sprintf("photo_%d.jpg", msg.ID)
		fileSize = msg.Photo.FileSize
		hasFile = true
	} else if msg.Animation != nil {
		fileName = msg.Animation.FileName
		if fileName == "" {
			fileName = fmt.Sprintf("animation_%d.mp4", msg.ID)
		}
		fileSize = msg.Animation.FileSize
		hasFile = true
	}

	if !hasFile {
		return nil
	}

	if fileName == "" {
		fileName = fmt.Sprintf("file_%d", msg.ID)
	}

	if QueueDownloadJob == nil {
		return c.Send("❌ Scheduler is not initialized to handle download queue.")
	}

	err := QueueDownloadJob(c.Chat().ID, msg.ID, fileName, fileSize)
	if err != nil {
		return c.Send("❌ Failed to add download job: " + err.Error())
	}

	return c.Send(fmt.Sprintf("📥 *Download Queued!*\n\n📄 *File:* `%s`\n📏 *Size:* %s\n\nUse `/status` to monitor progress.", fileName, FormatFileSize(fileSize)), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

// handleMediaMessageUser is the MTProto (user-mode) media file handler.
func (e *Engine) handleMediaMessageUser(ctx context.Context, entities tg.Entities, peer tg.PeerClass, m *tg.Message) error {
	if m.Media == nil {
		return nil
	}

	var fileName string
	var fileSize int64
	var hasFile bool

	switch media := m.Media.(type) {
	case *tg.MessageMediaDocument:
		if doc, ok := media.Document.(*tg.Document); ok {
			fileSize = doc.Size
			hasFile = true
			for _, attr := range doc.Attributes {
				if fAttr, ok := attr.(*tg.DocumentAttributeFilename); ok {
					fileName = fAttr.FileName
					break
				}
			}
			if fileName == "" {
				ext := "bin"
				if mimeType := doc.MimeType; mimeType != "" {
					if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
						ext = strings.TrimPrefix(exts[0], ".")
					}
				}
				fileName = fmt.Sprintf("document_%d.%s", doc.ID, ext)
			}
		}
	case *tg.MessageMediaPhoto:
		if photo, ok := media.Photo.(*tg.Photo); ok {
			hasFile = true
			fileName = fmt.Sprintf("photo_%d.jpg", photo.ID)
			for _, size := range photo.Sizes {
				switch s := size.(type) {
				case *tg.PhotoSize:
					if s.Size > int(fileSize) {
						fileSize = int64(s.Size)
					}
				case *tg.PhotoSizeProgressive:
					if len(s.Sizes) > 0 {
						last := s.Sizes[len(s.Sizes)-1]
						if last > int(fileSize) {
							fileSize = int64(last)
						}
					}
				}
			}
			if fileSize == 0 {
				fileSize = 1024 * 1024 // 1MB estimate
			}
		}
	}

	if !hasFile {
		return nil
	}

	var chatID int64
	switch p := peer.(type) {
	case *tg.PeerUser:
		chatID = p.UserID
	case *tg.PeerChat:
		chatID = p.ChatID
	case *tg.PeerChannel:
		chatID = p.ChannelID
	}

	if QueueDownloadJob == nil {
		return e.sendUserMessage(ctx, entities, peer, "❌ Scheduler is not initialized to handle download queue.")
	}

	err := QueueDownloadJob(chatID, m.ID, fileName, fileSize)
	if err != nil {
		return e.sendUserMessage(ctx, entities, peer, "❌ Failed to add download job: "+err.Error())
	}

	return e.sendUserMessage(ctx, entities, peer, fmt.Sprintf("📥 *Download Queued!*\n\n📄 *File:* `%s`\n📏 *Size:* %s\n\nUse `/status` to monitor progress.", fileName, FormatFileSize(fileSize)))
}
