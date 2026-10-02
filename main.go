package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	bonding_client "clever-connect/internal/bonding/client"
	"clever-connect/internal/bonding/selector"
	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/db/pebble"
	"clever-connect/internal/downloader"
	"clever-connect/internal/ehcocore"
	"clever-connect/internal/filecore"
	"clever-connect/internal/geo"
	"clever-connect/internal/handlers"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/scheduler"
	"clever-connect/internal/soroush"
	"clever-connect/internal/spotify"
	"clever-connect/internal/storageguard"
	"clever-connect/internal/telegram"
	"clever-connect/internal/torrent"
	"clever-connect/internal/trusttunnel"
	"clever-connect/internal/v2ray/scanner"
	"clever-connect/internal/v2ray/traffic"
	"clever-connect/internal/warp"
	"clever-connect/internal/youtube"

	"github.com/gin-gonic/gin"
)

//go:embed all:web/client/dist
var clientDist embed.FS

//go:embed all:web/server/dist
var serverDist embed.FS

func main() {
	// Load configuration first (before logger, since we need AppMode)
	cfg := config.LoadConfig()

	// Initialize the async structured logging system
	logger.Init("logs", cfg.AppMode)
	defer logger.Shutdown()

	logger.Info("Core", "Starting CleverConnect VPN Orchestrator",
		"mode", strings.ToUpper(cfg.AppMode),
		"port", cfg.Port,
	)

	// Initialize Database
	database := db.InitDB(cfg)
	_ = database // keep reference

	// Initialize S3-Compatible Object Storage (Clever Cloud Cellar).
	// Disabled automatically when CELLAR_ADDON_* credentials are absent, in
	// which case the file system keeps using local disk with no degradation.
	if cfg.S3Enabled {
		if err := filecore.InitStorageCore(cfg.S3Host, cfg.S3KeyID, cfg.S3KeySecret, cfg.S3Bucket, cfg.S3Region); err != nil {
			logger.Error("FileCore", "S3 storage engine failed to initialize — continuing with local disk", "error", err)
		} else {
			logger.Info("FileCore", "S3 object storage enabled",
				"bucket", cfg.S3Bucket, "host", cfg.S3Host)
		}
	} else {
		logger.Info("FileCore", "S3 object storage not configured — using local disk")
	}

	// Initialize Geo Geolocation & CDN Engine
	if err := geo.GetEngine().Init("data"); err != nil {
		logger.Error("GeoEngine", "Failed to initialize Geo IP Engine", "error", err)
	} else {
		defer geo.GetEngine().Close()
	}

	// Initialize CDN IP Registry
	if err := scanner.InitCDNRegistry("data"); err != nil {
		logger.Error("CDNRegistry", "Failed to initialize CDN IP Registry", "error", err)
	} else {
		logger.Info("CDNRegistry", "CDN IP Registry initialized successfully")
	}

	// Initialize PebbleDB for V2Ray Client Configs
	if err := pebble.InitPebble("data/pebble_nodes"); err != nil {
		logger.Error("Pebble", "Failed to initialize PebbleDB", "error", err)
	} else {
		defer pebble.Close()
		// Migrate SQLite table if it exists
		if err := pebble.MigrateFromSQLite(database); err != nil {
			logger.Error("Pebble", "Migration error", "error", err)
		}
	}

	// Initialize Downloader Engine on server only
	if cfg.AppMode == "server" {
		downloader.Init()
		if err := torrent.Init(); err != nil {
			logger.Error("Torrent", "Failed to initialize torrent manager", "error", err)
		} else {
			defer torrent.Manager.Close()

			// Seed the smart storage config and start the Storage Guard
			// (torrent offloading to the Clever Cellar S3 bucket with disk
			// watermarks, stream batches, eviction and restore support).
			storageguard.Init()
			storageguard.Start(torrent.Manager)
		}

		// Initialize YouTube Download Engine
		youtube.Init()

		// Initialize Spotify Download Engine
		spotify.Init()

		// Initialize Enterprise Job Scheduler Engine
		scheduler.Init()
	}

	// Auto-start active tunnel engine on bootstrap
	if cfg.AppMode == "server" {
		var serverCfg models.EhcoServerConfig
		if err := db.DB.First(&serverCfg).Error; err == nil && serverCfg.IsActive {
			logger.Info("Ehco", "Auto-starting active server tunnel engine")
			if err := ehcocore.StartServerEngine(&serverCfg); err != nil {
				logger.Error("Ehco", "Failed to auto-start server tunnel", "error", err)
			}
		}
	} else {
		var clientCfg models.EhcoClientConfig
		if err := db.DB.First(&clientCfg).Error; err == nil && clientCfg.IsActive {
			logger.Info("Ehco", "Auto-starting active client tunnel engine")
			if err := ehcocore.StartClientEngine(&clientCfg); err != nil {
				logger.Error("Ehco", "Failed to auto-start client tunnel", "error", err)
			}
		}
		// Start client V2Ray subscription auto-update background worker (disabled to prevent automatic loss of node testing results)
		// go sub.StartSubscriptionUpdater(context.Background())
	}

	// Auto-start DMB Bonding Engine (Selector/Failover or True Bonding) if configured
	if cfg.AppMode == "client" {
		var bondCfg models.BondingEngineConfig
		if err := db.DB.First(&bondCfg).Error; err == nil && bondCfg.IsActive {
			logger.Info("Bonding", "Auto-starting DMB Engine", "mode", bondCfg.Mode)
			var startErr error
			if bondCfg.Mode == "bonding" {
				engine := bonding_client.GetBondingEngine()
				startErr = engine.StartEngine(&bondCfg)
			} else {
				engine := selector.GetEngine()
				startErr = engine.StartEngine(&bondCfg)
			}
			if startErr != nil {
				logger.Error("Bonding", "Failed to auto-start DMB Engine", "error", startErr)
			}
		}
	}

	// Auto-start WARP+ engine if configured (client mode only)
	if cfg.AppMode == "client" {
		var warpCfg models.WarpGlobalConfig
		if err := db.DB.First(&warpCfg).Error; err == nil && warpCfg.IsActive {
			var warpAccount models.WarpAccount
			if err := db.DB.First(&warpAccount, warpCfg.ActiveAccountID).Error; err == nil {
				logger.Info("WARP", "Auto-starting WARP+ engine", "mode", warpCfg.TransportMode)
				if err := warp.GetEngine().StartEngine(&warpCfg, &warpAccount); err != nil {
					logger.Error("WARP", "Failed to auto-start WARP+ engine", "error", err)
				}
			}
		}
	}

	// (Combiner auto-start deferred to after handler creation below)

	// Auto-start Telegram bot engine if configured and active
	if cfg.AppMode == "server" {
		var telegramCfg models.TelegramConfig
		if err := db.DB.First(&telegramCfg).Error; err == nil && telegramCfg.IsActive && telegramCfg.BotToken != "" {
			logger.Info("Telegram", "Auto-starting Telegram bot engine")
			if err := telegram.StartEngine(&telegramCfg); err != nil {
				logger.Error("Telegram", "Failed to auto-start Telegram bot", "error", err)
			}
		}
	}

	// Auto-start Soroush WebRTC tunnel engine if configured and active
	{
		var soroushCfg models.SoroushTunnelConfig
		if err := db.DB.First(&soroushCfg).Error; err == nil && soroushCfg.IsActive {
			var accounts []models.SoroushAccount
			db.DB.Where("status = ?", "verified").Find(&accounts)
			if len(accounts) > 0 {
				isServer := cfg.AppMode == "server"
				logger.Info("Soroush", "Auto-starting Soroush tunnel engine",
					"mode", cfg.AppMode,
					"accounts", len(accounts),
				)
				if err := soroush.StartEngine(&soroushCfg, accounts, isServer); err != nil {
					logger.Error("Soroush", "Failed to auto-start tunnel engine", "error", err)
				}
			}
		}
	}

	// Auto-start active V2Ray/Xray proxy core
	if cfg.AppMode == "server" {
		var inboundCount int64
		if err := db.DB.Model(&models.V2RayInbound{}).Count(&inboundCount).Error; err == nil && inboundCount > 0 {
			logger.Info("V2Ray", "Auto-starting V2Ray/Xray server proxy engine")
			if err := traffic.ReloadCoreConfig(); err != nil {
				logger.Error("V2Ray", "Failed to auto-start V2Ray core on boot", "error", err)
			} else {
				traffic.StartInterceptor()
			}
		}
	}

	// Auto-start TrustTunnel if configured
	{
		var ttCfg models.TrustTunnelConfig
		if err := db.DB.First(&ttCfg).Error; err == nil && ttCfg.IsActive {
			logger.Info("TrustTunnel", "Auto-starting TrustTunnel engine")
			if cfg.AppMode == "server" {
				if err := trusttunnel.StartServerEngine(&ttCfg); err != nil {
					logger.Error("TrustTunnel", "Failed to auto-start server engine", "error", err)
				}
			} else {
				if err := trusttunnel.StartClientEngine(&ttCfg); err != nil {
					logger.Error("TrustTunnel", "Failed to auto-start client engine", "error", err)
				}
			}
		}
	}

	// Setup Gin Router in release mode
	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = logger.GinWriter()
	gin.DefaultErrorWriter = logger.GinWriter()
	router := gin.New()

	// Use our structured logging middleware instead of gin.Logger()
	router.Use(logger.GinRecoveryMiddleware())
	router.Use(logger.GinMiddleware())

	router.GET("/swagger", handlers.ServeSwagger)

	// /generate_204 — public captive-portal probe endpoint (no auth required).
	// Returns HTTP 204 No Content with zero body, exactly like https://www.google.com/generate_204.
	// Use this to verify a V2Ray/Xray config can reach this server panel:
	//   curl -x socks5://127.0.0.1:10808 https://<server>/generate_204
	//   → HTTP/1.1 204 No Content  ✓  tunnel works end-to-end
	router.GET("/generate_204", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.HEAD("/generate_204", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/.well-known/acme-challenge/:token", handlers.HandleACMEChallenge)

	// Setup API Route Handlers
	authHandler := handlers.NewAuthHandler(cfg)
	wsHandler := handlers.NewWSHandler(cfg)
	ehcoHandler := handlers.NewEhcoHandler(cfg)
	fileHandler := handlers.NewFileHandler(cfg)
	leechHandler := handlers.NewLeechHandler(cfg)
	torrentHandler := handlers.NewTorrentHandler(cfg)
	storageHandler := handlers.NewStorageHandler()
	youtubeHandler := handlers.NewYouTubeHandler(cfg)
	spotifyHandler := handlers.NewSpotifyHandler(cfg)
	telegramHandler := handlers.NewTelegramHandler(cfg)
	schedulerHandler := handlers.NewSchedulerHandler(cfg)
	soroushHandler := handlers.NewSoroushHandler(cfg)
	v2rayHandler := handlers.NewV2RayHandler(cfg)
	domainHandler := handlers.NewDomainHandler(cfg)
	geoHandler := handlers.NewGeoHandler(cfg)
	dnsHandler := handlers.NewDNSHandler(cfg)
	bondingHandler := handlers.NewBondingHandler(cfg)
	combinerHandler := handlers.NewCombinerHandler(cfg)
	cloudflareHandler := handlers.NewCloudflareHandler(cfg)
	novaHandler := handlers.NewNovaForwarderHandler(cfg)
	warpHandler := handlers.NewWarpHandler(cfg)
	trusttunnelHandler := handlers.NewTrustTunnelHandler(cfg)
	rcloneHandler := handlers.NewRcloneHandler(cfg)

	// Auto-start combiner if configured (after handler creation)
	if cfg.AppMode == "server" {
		combinerHandler.AutoStartCombiner()
	}

	// API Group
	api := router.Group("/api")
	{
		api.POST("/auth/login", authHandler.Login)
		api.GET("/sub/:token", v2rayHandler.ServeSubscription)
		api.Any("/nova/forward", novaHandler.HandleForwardStream)

		// Cloudflare Public OAuth routes
		api.GET("/cloudflare/oauth/login", cloudflareHandler.OAuthLogin)
		api.GET("/cloudflare/oauth/callback", cloudflareHandler.OAuthCallback)

		// Protected API routes
		protected := api.Group("")
		protected.Use(handlers.AuthMiddleware(cfg.JWTSecret))
		{
			protected.POST("/clients/disconnect/:id", func(c *gin.Context) {
				id := c.Param("id")
				logger.Info("API", "Client disconnect requested", "clientId", id, "ip", c.ClientIP())
				c.JSON(http.StatusOK, gin.H{"status": "disconnected", "id": id})
			})

			protected.GET("/logs/download", handlers.DownloadTodayLog)

			// Ehco Tunneling routes
			protected.GET("/ehco/config", ehcoHandler.GetConfig)
			protected.POST("/ehco/config", ehcoHandler.SaveConfig)
			protected.POST("/ehco/start", ehcoHandler.StartEngine)
			protected.POST("/ehco/stop", ehcoHandler.StopEngine)

			// V2Ray Server-side core endpoints
			protected.GET("/v2ray/core/status", v2rayHandler.GetCoreStatus)
			protected.POST("/v2ray/core/start", v2rayHandler.StartCore)
			protected.POST("/v2ray/core/stop", v2rayHandler.StopCore)

			// V2Ray Inbounds endpoints
			protected.GET("/v2ray/inbounds", v2rayHandler.ListInbounds)
			protected.POST("/v2ray/inbounds", v2rayHandler.CreateInbound)
			protected.PUT("/v2ray/inbounds/:id", v2rayHandler.UpdateInbound)
			protected.DELETE("/v2ray/inbounds/:id", v2rayHandler.DeleteInbound)

			// V2Ray Users endpoints
			protected.GET("/v2ray/users", v2rayHandler.ListUsers)
			protected.POST("/v2ray/users", v2rayHandler.CreateUser)
			protected.PUT("/v2ray/users/:id", v2rayHandler.UpdateUser)
			protected.DELETE("/v2ray/users/:id", v2rayHandler.DeleteUser)
			protected.GET("/v2ray/traffic/logs", v2rayHandler.GetUserTrafficLogs)

			// V2Ray Routing Rules endpoints
			protected.GET("/v2ray/routing", v2rayHandler.ListRoutingRules)
			protected.POST("/v2ray/routing", v2rayHandler.CreateRoutingRule)
			protected.DELETE("/v2ray/routing/:id", v2rayHandler.DeleteRoutingRule)

			// V2Ray Client-side endpoints
			protected.GET("/v2ray/client/status", v2rayHandler.GetClientStatus)
			protected.POST("/v2ray/client/start", v2rayHandler.StartClientCore)
			protected.POST("/v2ray/client/stop", v2rayHandler.StopClientCore)
			protected.GET("/v2ray/client/configs", v2rayHandler.ListClientConfigs)
			protected.POST("/v2ray/client/configs", v2rayHandler.CreateClientConfig)
			protected.PUT("/v2ray/client/configs/:id", v2rayHandler.UpdateClientConfig)
			protected.DELETE("/v2ray/client/configs/all", v2rayHandler.DeleteAllClientConfigs)
			protected.DELETE("/v2ray/client/configs/failed", v2rayHandler.DeleteFailedClientConfigs)
			protected.DELETE("/v2ray/client/configs/discovered", v2rayHandler.DeleteDiscoveredClientConfigs)
			protected.DELETE("/v2ray/client/configs/:id", v2rayHandler.DeleteClientConfig)
			protected.POST("/v2ray/client/configs/delete-selected", v2rayHandler.DeleteSelectedClientConfigs)
			protected.POST("/v2ray/client/configs/:id/active", v2rayHandler.SetActiveClientConfig)
			protected.POST("/v2ray/client/configs/reorder", v2rayHandler.ReorderClientConfigs)
			protected.POST("/v2ray/client/configs/import-manual", v2rayHandler.ImportManualConfig)
			protected.POST("/v2ray/client/configs/import-bulk", v2rayHandler.ImportBulkConfigs)
			protected.POST("/v2ray/client/configs/import-qr", v2rayHandler.ImportQRConfig)

			// Category management routes
			protected.GET("/v2ray/client/categories", v2rayHandler.ListCategories)
			protected.POST("/v2ray/client/categories", v2rayHandler.CreateCategory)
			protected.PUT("/v2ray/client/categories/:id", v2rayHandler.UpdateCategory)
			protected.DELETE("/v2ray/client/categories/:id", v2rayHandler.DeleteCategory)
			protected.POST("/v2ray/client/configs/assign-category", v2rayHandler.AssignCategoryToConfigs)

			// Profiles compatibility aliases for client panel
			protected.GET("/v2ray/client/profiles", v2rayHandler.ListClientConfigs)
			protected.POST("/v2ray/client/profiles", v2rayHandler.CreateClientConfig)
			protected.PUT("/v2ray/client/profiles/:id", v2rayHandler.UpdateClientConfig)
			protected.DELETE("/v2ray/client/profiles/:id", v2rayHandler.DeleteClientConfig)
			protected.POST("/v2ray/client/profiles/:id/activate", v2rayHandler.SetActiveClientConfig)
			protected.POST("/v2ray/client/profiles/import", v2rayHandler.ImportManualConfig)
			protected.POST("/v2ray/client/profiles/import-bulk", v2rayHandler.ImportBulkConfigs)
			protected.GET("/v2ray/client/subscriptions", v2rayHandler.ListSubscriptions)
			protected.POST("/v2ray/client/subscriptions", v2rayHandler.ImportSubscription)
			protected.POST("/v2ray/client/subscriptions/:id/update", v2rayHandler.UpdateSubscription)
			protected.DELETE("/v2ray/client/subscriptions/:id", v2rayHandler.DeleteSubscription)
			protected.POST("/v2ray/client/export-pdf", v2rayHandler.ExportSelectedConfigsPDF)
			protected.POST("/v2ray/client/import", v2rayHandler.ImportSubscription)
			protected.GET("/v2ray/client/settings", v2rayHandler.GetClientSettings)
			protected.POST("/v2ray/client/settings", v2rayHandler.SaveClientSettings)
			protected.GET("/v2ray/client/core-template", v2rayHandler.GetCoreTemplate)
			protected.POST("/v2ray/client/core-template", v2rayHandler.SaveCoreTemplate)
			protected.POST("/v2ray/client/test-profile/:id", v2rayHandler.TestClientProfile)
			protected.POST("/v2ray/client/test-mass", v2rayHandler.TestMassProfiles)
			protected.POST("/v2ray/client/test-config-direct", v2rayHandler.TestConfigDirect)
			protected.POST("/v2ray/client/speed-test", v2rayHandler.RunDetailedSpeedTest)
			protected.GET("/v2ray/client/logs", v2rayHandler.GetClientLogs)
			protected.POST("/v2ray/client/probe-ports", v2rayHandler.ProbePorts)
			protected.POST("/v2ray/client/wol", v2rayHandler.WakeOnLAN)
			protected.GET("/v2ray/client/discover", v2rayHandler.DiscoverDevices)
			protected.POST("/v2ray/client/debug-proxy/start", v2rayHandler.StartDebugProxy)
			protected.POST("/v2ray/client/debug-proxy/stop", v2rayHandler.StopDebugProxy)
			protected.GET("/v2ray/client/debug-proxy/logs", v2rayHandler.GetDebugProxyLogs)
			protected.GET("/v2ray/client/hotkeys", v2rayHandler.GetHotkeys)
			protected.POST("/v2ray/client/hotkeys", v2rayHandler.SaveHotkeys)
			protected.GET("/v2ray/client/system-tray", v2rayHandler.GetSystemTrayConfig)
			protected.POST("/v2ray/client/system-tray", v2rayHandler.SaveSystemTrayConfig)
			protected.POST("/v2ray/client/scan-cdn", v2rayHandler.ScanCDN)
			protected.GET("/v2ray/client/scan-cdn/status", v2rayHandler.GetScanStatus)
			protected.POST("/v2ray/client/scan-cdn/stop", v2rayHandler.StopScan)
			protected.POST("/v2ray/scanner/start", v2rayHandler.StartNetworkScannerSweep)
			protected.POST("/v2ray/scanner/stop", v2rayHandler.StopNetworkScannerSweep)
			protected.GET("/v2ray/scanner/stats", v2rayHandler.GetNetworkScannerLiveTelemetry)
			protected.GET("/v2ray/scanner/config", v2rayHandler.GetScannerConfig)
			protected.POST("/v2ray/scanner/config/reset", v2rayHandler.ResetScannerConfig)
			protected.GET("/v2ray/scanner/ws", v2rayHandler.GetNetworkScannerWebSocket)
			protected.GET("/v2ray/scanner/sources", v2rayHandler.ListScannerSources)
			protected.POST("/v2ray/scanner/sources", v2rayHandler.CreateScannerSource)
			protected.PUT("/v2ray/scanner/sources/:id", v2rayHandler.UpdateScannerSource)
			protected.DELETE("/v2ray/scanner/sources/:id", v2rayHandler.DeleteScannerSource)
			protected.POST("/v2ray/nodes/:id/provision", v2rayHandler.ProvisionNode)
			protected.POST("/v2ray/firewall/block", v2rayHandler.BlockFirewallIP)
			protected.POST("/v2ray/mcp", v2rayHandler.HandleMCP)
			protected.Any("/v2ray/webdav/*filepath", v2rayHandler.ServeWebDAV)

			// DMB Bonding Engine API
			protected.GET("/v2ray/bonding/config", bondingHandler.GetConfig)
			protected.POST("/v2ray/bonding/config", bondingHandler.SaveConfig)
			protected.POST("/v2ray/bonding/start", bondingHandler.StartEngine)
			protected.POST("/v2ray/bonding/stop", bondingHandler.StopEngine)
			protected.GET("/v2ray/bonding/status", bondingHandler.GetStatus)
			protected.GET("/v2ray/bonding/arteries", bondingHandler.ListArteries)
			protected.Any("/v2ray/bonding/diagnose", bondingHandler.DiagnoseEngine)

			// WARP+ Engine API (client mode)
			protected.GET("/v2ray/warp/config", warpHandler.GetConfig)
			protected.POST("/v2ray/warp/config", warpHandler.SaveConfig)
			protected.GET("/v2ray/warp/accounts", warpHandler.ListAccounts)
			protected.POST("/v2ray/warp/accounts", warpHandler.AddAccount)
			protected.POST("/v2ray/warp/accounts/verify", warpHandler.VerifyLicenseKey) // 4-stage key diagnostic — BEFORE :id routes
			protected.DELETE("/v2ray/warp/accounts/:id", warpHandler.DeleteAccount)
			protected.POST("/v2ray/warp/accounts/:id/activate", warpHandler.SetActiveAccount)
			protected.POST("/v2ray/warp/accounts/:id/verify", warpHandler.VerifyAccount) // live account health check
			protected.POST("/v2ray/warp/scan", warpHandler.StartScan)
			protected.POST("/v2ray/warp/scan/stop", warpHandler.StopScan)
			protected.GET("/v2ray/warp/scan/events", warpHandler.GetScanEvents)
			protected.GET("/v2ray/warp/scan/results", warpHandler.GetScanResults)
			protected.POST("/v2ray/warp/tunnel/start", warpHandler.StartTunnel)
			protected.POST("/v2ray/warp/tunnel/stop", warpHandler.StopTunnel)
			protected.GET("/v2ray/warp/status", warpHandler.GetStatus)

			// System monitoring route
			protected.GET("/system/stats", handlers.GetSystemStats)

			// Domain Checker Endpoints
			protected.GET("/domains", domainHandler.List)
			protected.GET("/domains/categories", domainHandler.Categories)
			protected.POST("/domains", domainHandler.Add)
			protected.POST("/domains/check/:id", domainHandler.CheckSingle)
			protected.POST("/domains/check/bulk", domainHandler.CheckBulk)
			protected.DELETE("/domains/:id", domainHandler.DeleteSingle)
			protected.POST("/domains/delete", domainHandler.DeleteBulk)

			// Geo Geolocation & CDN Endpoints
			protected.POST("/geo/resolve", geoHandler.Resolve)
			protected.GET("/settings/apikeys", geoHandler.GetAPIKeys)
			protected.POST("/settings/apikeys", geoHandler.SaveAPIKeys)
			protected.POST("/settings/test-key", geoHandler.TestAPIKey)
			protected.POST("/network/lookup", geoHandler.PerformLookup)

			protected.GET("/dns/resolvers", dnsHandler.ListResolvers)
			protected.POST("/dns/resolvers", dnsHandler.AddResolver)
			protected.POST("/dns/resolvers/bulk", dnsHandler.AddResolverBulk)
			protected.GET("/dns/resolvers/bulk/progress", dnsHandler.GetBulkProgress)
			protected.DELETE("/dns/resolvers/:id", dnsHandler.DeleteResolver)
			protected.POST("/dns/resolvers/batch-delete", dnsHandler.BatchDeleteResolvers)
			protected.POST("/dns/resolvers/fetch-public", dnsHandler.FetchPublicResolvers)
			protected.POST("/dns/resolvers/:id/test", dnsHandler.TestSingleResolver)
			protected.GET("/dns/config", dnsHandler.GetConfig)
			protected.POST("/dns/config", dnsHandler.SaveConfig)
			protected.POST("/dns/config/reset", dnsHandler.ResetConfig)
			protected.GET("/dns/metrics", dnsHandler.GetMetrics)
			protected.POST("/dns/core/apply", dnsHandler.ApplyActiveResolver)

			// File Manager API Endpoints
			protected.GET("/files/list", fileHandler.ListDirectory)
			protected.GET("/files/info", fileHandler.FileInfo)
			protected.GET("/files/search", fileHandler.SearchFiles)
			protected.GET("/files/stream", fileHandler.StreamOrDownload)
			protected.GET("/files/download", fileHandler.RawDownload)
			protected.GET("/files/content", fileHandler.GetContent)
			protected.POST("/files/save", fileHandler.SaveContent)
			protected.POST("/files/create-folder", fileHandler.CreateFolder)
			protected.POST("/files/delete", fileHandler.DeleteItem)
			protected.POST("/files/upload", fileHandler.UploadFile)
			protected.POST("/files/move", fileHandler.MoveItem)
			protected.POST("/files/copy", fileHandler.CopyItem)
			protected.POST("/files/compress", fileHandler.CompressItems)
			protected.POST("/files/decompress", fileHandler.DecompressItem)
			protected.POST("/files/to-s3", fileHandler.MoveToS3)

			// Remote Downloader (Leecher) API Endpoints
			protected.GET("/leech/jobs", leechHandler.ListJobs)
			protected.POST("/leech/add", leechHandler.AddJob)
			protected.POST("/leech/pause", leechHandler.PauseJob)
			protected.POST("/leech/start", leechHandler.StartJob)
			protected.POST("/leech/delete", leechHandler.DeleteJob)
			protected.GET("/leech/config", leechHandler.GetConfig)
			protected.POST("/leech/config", leechHandler.SaveConfig)

			// BitTorrent Client API Endpoints
			protected.GET("/torrent/list", torrentHandler.ListTorrents)
			protected.POST("/torrent/add", torrentHandler.AddTorrent)
			protected.POST("/torrent/pause", torrentHandler.PauseTorrent)
			protected.POST("/torrent/resume", torrentHandler.ResumeTorrent)
			protected.POST("/torrent/delete", torrentHandler.DeleteTorrent)
			protected.GET("/torrent/files", torrentHandler.ListTorrentFiles)
			protected.POST("/torrent/select-files", torrentHandler.SelectTorrentFiles)
			protected.GET("/torrent/config", torrentHandler.GetConfig)
			protected.POST("/torrent/config", torrentHandler.SaveConfig)

			// Smart Storage Guard (Cellar/S3 offloading) API Endpoints
			protected.GET("/storage/status", storageHandler.GetStatus)
			protected.GET("/storage/config", storageHandler.GetConfig)
			protected.POST("/storage/config", storageHandler.SaveConfig)
			protected.GET("/storage/logs", storageHandler.GetLogs)
			protected.POST("/storage/restore/:hash", storageHandler.RestoreTorrent)
			protected.POST("/storage/reoffload/:hash", storageHandler.ReoffloadTorrent)

			// Universal Cloud Storage (rclone multi-provider) API Endpoints
			protected.GET("/rclone/status", rcloneHandler.GetStatus)
			protected.POST("/rclone/system", rcloneHandler.UpdateSystem)
			protected.POST("/rclone/install", rcloneHandler.InstallBinary)
			protected.GET("/rclone/providers", rcloneHandler.GetProviders)
			protected.GET("/rclone/remotes", rcloneHandler.ListRemotes)
			protected.POST("/rclone/remotes", rcloneHandler.CreateRemote)
			protected.PUT("/rclone/remotes/:id", rcloneHandler.UpdateRemote)
			protected.DELETE("/rclone/remotes/:id", rcloneHandler.DeleteRemote)
			protected.POST("/rclone/remotes/:id/test", rcloneHandler.TestRemote)
			protected.POST("/rclone/remotes/:id/list", rcloneHandler.ListRemoteDir)
			protected.POST("/rclone/upload", rcloneHandler.StartUpload)
			protected.GET("/rclone/uploads", rcloneHandler.ListUploads)
			protected.POST("/rclone/uploads/:id/link", rcloneHandler.RefreshUploadLink)
			protected.DELETE("/rclone/uploads/:id", rcloneHandler.DeleteUpload)

			// YouTube Downloader API Endpoints
			protected.POST("/youtube/info", youtubeHandler.FetchInfo)
			protected.POST("/youtube/add", youtubeHandler.AddJob)
			protected.GET("/youtube/jobs", youtubeHandler.ListJobs)
			protected.POST("/youtube/cancel", youtubeHandler.CancelJob)
			protected.POST("/youtube/delete", youtubeHandler.DeleteJob)
			protected.GET("/youtube/config", youtubeHandler.GetConfig)
			protected.POST("/youtube/config", youtubeHandler.SaveConfig)

			// Spotify Downloader API Endpoints
			protected.POST("/spotify/info", spotifyHandler.FetchInfo)
			protected.POST("/spotify/add", spotifyHandler.AddJob)
			protected.GET("/spotify/jobs", spotifyHandler.ListJobs)
			protected.POST("/spotify/cancel", spotifyHandler.CancelJob)
			protected.POST("/spotify/delete", spotifyHandler.DeleteJob)
			protected.POST("/spotify/retry", spotifyHandler.RetryJob)
			protected.GET("/spotify/config", spotifyHandler.GetConfig)
			protected.POST("/spotify/config", spotifyHandler.SaveConfig)

			// Telegram Bot Core API Endpoints
			protected.GET("/telegram/config", telegramHandler.GetConfig)
			protected.POST("/telegram/config", telegramHandler.SaveConfig)
			protected.POST("/telegram/test", telegramHandler.TestConnection)
			protected.POST("/telegram/start", telegramHandler.StartBot)
			protected.POST("/telegram/stop", telegramHandler.StopBot)
			protected.GET("/telegram/status", telegramHandler.GetStatus)
			protected.POST("/telegram/send-file", telegramHandler.SendFile)
			protected.POST("/telegram/set-avatar", telegramHandler.SetBotAvatar)
			protected.POST("/telegram/broadcast", telegramHandler.BroadcastMessage)
			protected.POST("/telegram/auth/send-code", telegramHandler.SendAuthCode)
			protected.POST("/telegram/auth/verify-code", telegramHandler.VerifyAuthCode)
			protected.POST("/telegram/auth/verify-password", telegramHandler.VerifyAuthPassword)
			protected.POST("/settings/favicon", handlers.UploadFavicon)
			protected.GET("/settings/persistent-token", authHandler.GetPersistentToken)

			// Enterprise Job Scheduler API Endpoints
			protected.GET("/scheduler/jobs", schedulerHandler.ListJobs)
			protected.POST("/scheduler/jobs", schedulerHandler.CreateJob)
			protected.POST("/scheduler/jobs/bulk-action", schedulerHandler.BulkAction) // Must be before :id routes
			protected.POST("/scheduler/jobs/reorder", schedulerHandler.ReorderJobs)    // Must be before :id routes
			protected.POST("/scheduler/jobs/:id/cancel", schedulerHandler.CancelJob)
			protected.POST("/scheduler/jobs/:id/retry", schedulerHandler.RetryJob)
			protected.POST("/scheduler/jobs/:id/force", schedulerHandler.ForceRunJob)
			protected.POST("/scheduler/jobs/:id/delete", schedulerHandler.DeleteJob)
			protected.GET("/scheduler/jobs/:id/logs", schedulerHandler.GetJobLogs)
			protected.GET("/scheduler/config", schedulerHandler.GetConfig)
			protected.POST("/scheduler/config", schedulerHandler.SaveConfig)
			protected.GET("/scheduler/stats", schedulerHandler.GetStats)
			protected.POST("/scheduler/purge", schedulerHandler.PurgeJobs)

			// Soroush WebRTC Tunnel API Endpoints (ADDITIVE — parallel to Ehco)
			protected.GET("/soroush/accounts", soroushHandler.GetSoroushAccounts)
			protected.POST("/soroush/accounts", soroushHandler.AddSoroushAccount)
			protected.DELETE("/soroush/accounts/:id", soroushHandler.DeleteSoroushAccount)
			protected.PUT("/soroush/accounts/:id/token", soroushHandler.UpdateSoroushAccountToken)
			protected.POST("/soroush/accounts/:id/send-code", soroushHandler.SendVerificationCode)
			protected.POST("/soroush/accounts/:id/verify", soroushHandler.VerifyAccount)
			protected.GET("/soroush/config", soroushHandler.GetSoroushConfig)
			protected.PUT("/soroush/config", soroushHandler.UpdateSoroushConfig)
			protected.GET("/soroush/groups", soroushHandler.GetSoroushGroups)
			protected.POST("/soroush/engine/start", soroushHandler.StartSoroushEngine)
			protected.POST("/soroush/engine/stop", soroushHandler.StopSoroushEngine)
			protected.GET("/soroush/engine/status", soroushHandler.GetSoroushEngineStatus)
			protected.POST("/soroush/test-token", soroushHandler.TestTokenFetch)
			protected.GET("/soroush/sync", soroushHandler.SyncConfig)
			protected.POST("/soroush/sync", soroushHandler.IngestSync)

			// TrustTunnel Stealth Protocol API Endpoints
			protected.GET("/trusttunnel/config", trusttunnelHandler.GetConfig)
			protected.POST("/trusttunnel/config", trusttunnelHandler.SaveConfig)
			protected.POST("/trusttunnel/start", trusttunnelHandler.StartEngine)
			protected.POST("/trusttunnel/stop", trusttunnelHandler.StopEngine)
			protected.GET("/trusttunnel/users", trusttunnelHandler.ListUsers)
			protected.POST("/trusttunnel/users", trusttunnelHandler.CreateUser)
			protected.DELETE("/trusttunnel/users/:id", trusttunnelHandler.DeleteUser)
			protected.GET("/trusttunnel/rules", trusttunnelHandler.ListRules)
			protected.POST("/trusttunnel/rules", trusttunnelHandler.CreateRule)
			protected.DELETE("/trusttunnel/rules/:id", trusttunnelHandler.DeleteRule)
			protected.GET("/trusttunnel/export", trusttunnelHandler.ExportToken)
			protected.POST("/trusttunnel/import", trusttunnelHandler.ImportToken)
			protected.POST("/trusttunnel/generate-cert", trusttunnelHandler.GenerateCert)

			// DMB Combiner Server API (server mode only)
			protected.GET("/bonding/combiner/config", combinerHandler.GetCombinerConfig)
			protected.POST("/bonding/combiner/config", combinerHandler.SaveCombinerConfig)
			protected.POST("/bonding/combiner/start", combinerHandler.StartCombiner)
			protected.POST("/bonding/combiner/stop", combinerHandler.StopCombiner)
			protected.GET("/bonding/combiner/status", combinerHandler.GetCombinerStatus)
			protected.Any("/bonding/combiner/diagnose", combinerHandler.DiagnoseCombiner)

			// Cloudflare API routes
			protected.GET("/cloudflare/accounts", cloudflareHandler.ListAccounts)
			protected.POST("/cloudflare/accounts", cloudflareHandler.AddAccount)
			protected.PUT("/cloudflare/accounts/:id", cloudflareHandler.UpdateAccount)
			protected.DELETE("/cloudflare/accounts/:id", cloudflareHandler.DeleteAccount)
			protected.GET("/cloudflare/accounts/:id/stats", cloudflareHandler.GetAccountStats)
			protected.GET("/cloudflare/zones", cloudflareHandler.GetZones)
			protected.POST("/cloudflare/workers/deploy", cloudflareHandler.DeployWorker)
			protected.GET("/cloudflare/workers/deployments", cloudflareHandler.ListDeployments)
			protected.DELETE("/cloudflare/workers/deployments/:id", cloudflareHandler.DeleteDeployment)
			protected.POST("/cloudflare/workers/deployments/:id/check-health", cloudflareHandler.CheckDeploymentHealth)

			// Nova Forwarder settings routes
			protected.GET("/cloudflare/forwarder", novaHandler.GetConfig)
			protected.POST("/cloudflare/forwarder", novaHandler.UpdateConfig)
		}
	}

	// Real-time WebSocket endpoints (protected via token query param handled in middleware)
	router.GET("/ws", handlers.AuthMiddleware(cfg.JWTSecret), wsHandler.ServeWS)
	router.GET("/ws/jobs", handlers.AuthMiddleware(cfg.JWTSecret), wsHandler.ServeWSJobs)
	router.GET("/ws/logs", handlers.AuthMiddleware(cfg.JWTSecret), handlers.ServeLogWS)
	router.GET("/ws/stats", handlers.AuthMiddleware(cfg.JWTSecret), handlers.HandleStatsStream)
	router.GET("/ws/v2ray/test", handlers.AuthMiddleware(cfg.JWTSecret), v2rayHandler.ServeWSV2RayTest)
	router.GET("/ws/v2ray/bonding/telemetry", handlers.AuthMiddleware(cfg.JWTSecret), bondingHandler.ServeTelemetryWS)
	// Combiner WS is unauthenticated via JWT — uses PSK token in query param
	router.GET("/ws/bonding/combiner", combinerHandler.ServeCombinerWS)

	// Static Assets & SPA Fallback Serving
	var embedFS fs.FS
	var err error

	if cfg.AppMode == "server" {
		embedFS, err = fs.Sub(serverDist, "web/server/dist")
		if err != nil {
			logger.Fatal("Static", "Failed to sub server embed FS", "error", err)
		}
		logger.Info("Static", "Serving CleverConnect Server Panel UI")
	} else {
		embedFS, err = fs.Sub(clientDist, "web/client/dist")
		if err != nil {
			logger.Fatal("Static", "Failed to sub client embed FS", "error", err)
		}
		logger.Info("Static", "Serving CleverConnect Client Panel UI")
	}

	router.GET("/favicon.ico", handlers.ServeFavicon)
	router.GET("/favicon.png", handlers.ServeFavicon)

	router.Use(serveEmbeddedSPA(embedFS))

	// Start Gin Server
	bindAddr := "0.0.0.0:" + cfg.Port
	logger.Info("Core", "Orchestrator listening", "addr", "http://"+bindAddr)
	if err := router.Run(bindAddr); err != nil {
		logger.Fatal("Core", "Failed to run server", "error", err)
	}
}

func serveEmbeddedSPA(embedFS fs.FS) gin.HandlerFunc {
	fileServer := http.FileServer(http.FS(embedFS))

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// If route matches API endpoints, continue to other middleware/handlers
		if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/ws") || strings.HasPrefix(path, "/swagger") || strings.HasPrefix(path, "/.well-known") || path == "/favicon.ico" || path == "/favicon.png" {
			c.Next()
			return
		}

		// Format file path for searching inside embedded FS
		filePath := strings.TrimPrefix(path, "/")
		if filePath == "" {
			filePath = "index.html"
		}

		// Check if target file exists in embed FS
		_, err := embedFS.Open(filePath)
		if err != nil {
			// Serve index.html as fallback for Single Page App router
			indexFile, err := embedFS.Open("index.html")
			if err != nil {
				c.String(http.StatusInternalServerError, "Embedded index.html not found")
				return
			}
			defer indexFile.Close()

			stat, err := indexFile.Stat()
			if err != nil {
				c.String(http.StatusInternalServerError, "Failed to inspect index.html")
				return
			}

			c.DataFromReader(http.StatusOK, stat.Size(), "text/html; charset=utf-8", indexFile, nil)
			c.Abort()
			return
		}

		// File exists - serve it
		fileServer.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}
