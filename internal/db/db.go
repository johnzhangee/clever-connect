package db

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"clever-connect/internal/config"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"

	sqlite "clever-connect/internal/db/sqlite"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var DB *gorm.DB

func InitDB(cfg *config.Config) *gorm.DB {
	var err error

	// Use GORM silent mode — we handle logging through our own system
	gormCfg := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	}

	if cfg.AppMode == "client" {
		// SQLite Mode
		logger.Info("DB", "Connecting to SQLite database", "path", cfg.SQLitePath)

		// Ensure parent directory exists
		dir := filepath.Dir(cfg.SQLitePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			logger.Fatal("DB", "Failed to create database directories", "error", err)
		}

		DB, err = gorm.Open(sqlite.Open(cfg.SQLitePath), gormCfg)
		if err != nil {
			logger.Fatal("DB", "Failed to connect to SQLite", "path", cfg.SQLitePath, "error", err)
		}
		logger.Info("DB", "SQLite connection established", "path", cfg.SQLitePath)

		// Prevent "database is locked (SQLITE_BUSY)" by serializing connections and setting WAL mode / timeouts
		if sqlDB, err := DB.DB(); err == nil {
			sqlDB.SetMaxOpenConns(1)
		}
		DB.Exec("PRAGMA journal_mode=WAL;")
		DB.Exec("PRAGMA busy_timeout=10000;")
		DB.Exec("PRAGMA synchronous=NORMAL;")
	} else {
		// MySQL Mode (Server panel)
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			cfg.MySQLUser,
			cfg.MySQLPassword,
			cfg.MySQLHost,
			cfg.MySQLPort,
			cfg.MySQLDBName,
		)
		logger.Info("DB", "Connecting to MySQL database",
			"user", cfg.MySQLUser,
			"host", cfg.MySQLHost,
			"port", cfg.MySQLPort,
			"database", cfg.MySQLDBName,
		)

		DB, err = gorm.Open(mysql.Open(dsn), gormCfg)
		if err != nil {
			// Elegant fallback to SQLite for easy development/review!
			fallbackPath := "data/server_fallback.db"
			logger.Warn("DB", "Failed to connect to MySQL — activating SQLite fallback",
				"error", err,
				"fallback", fallbackPath,
			)

			dir := filepath.Dir(fallbackPath)
			_ = os.MkdirAll(dir, 0755)

			DB, err = gorm.Open(sqlite.Open(fallbackPath), gormCfg)
			if err != nil {
				logger.Fatal("DB", "Database initialization failed completely", "error", err)
			}
			logger.Info("DB", "SQLite fallback connection established", "path", fallbackPath)

			// Prevent "database is locked (SQLITE_BUSY)" by serializing connections and setting WAL mode / timeouts
			if sqlDB, err := DB.DB(); err == nil {
				sqlDB.SetMaxOpenConns(1)
			}
			DB.Exec("PRAGMA journal_mode=WAL;")
			DB.Exec("PRAGMA busy_timeout=10000;")
			DB.Exec("PRAGMA synchronous=NORMAL;")
		} else {
			logger.Info("DB", "MySQL connection established",
				"host", cfg.MySQLHost,
				"database", cfg.MySQLDBName,
			)
			if sqlDB, err := DB.DB(); err == nil {
				// Clever Cloud MySQL addon enforces a hard limit of 5 concurrent
				// connections per user. We cap at 4 to leave one slot free for
				// admin/monitoring tools, and keep idle connections low so they
				// are promptly returned and reused rather than sitting open.
				sqlDB.SetMaxOpenConns(4)
				sqlDB.SetMaxIdleConns(2)
				sqlDB.SetConnMaxLifetime(5 * time.Minute)
				sqlDB.SetConnMaxIdleTime(2 * time.Minute)
			}
		}
	}

	// Auto Migration
	logger.Info("DB", "Executing automatic database schema migrations")
	migrateDB := DB
	if DB.Dialector.Name() == "mysql" {
		migrateDB = DB.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci")
	}
	if err := migrateDB.AutoMigrate(
		&models.User{},
		&models.ClientSession{},
		&models.EhcoServerConfig{},
		&models.EhcoClientConfig{},
		&models.SoroushAccount{},
		&models.SoroushTunnelConfig{},
		&models.LeechConfig{},
		&models.LeechJob{},
		&models.TelegramConfig{},
		&models.SchedulerJob{},
		&models.SchedulerJobLog{},
		&models.SchedulerConfig{},
		&models.TelegramSubscriber{},
		&models.YouTubeJob{},
		&models.YouTubeConfig{},
		&models.FileRegistry{},
		&models.SpotifyConfig{},
		&models.SpotifyJob{},
		&models.V2RayNode{},
		&models.V2RayInbound{},
		&models.V2RayUser{},
		&models.V2RayTrafficLog{},
		&models.V2RayRoutingRule{},
		&models.V2RaySecurityEvent{},
		&models.V2RayClientConfig{},
		&models.V2RayClientFrontingMap{},
		&models.V2RayClientSetting{},
		&models.V2RayClientSubscription{},
		&models.NodeCategory{},
		&models.Domain{},
		&models.ScannerSource{},
		&models.ScannerConfig{},
		&models.IPRegistry{},
		&models.IPLookupConfig{},
		&models.IPIntelligenceCache{},
		&models.DomainWhoisCache{},
		&models.DNSResolver{},
		&models.DNSTesterConfig{},
		&models.BondingEngineConfig{},
		&models.BondingArtery{},
		&models.CloudflareAccount{},
		&models.CloudflareWorkerDeployment{},
		&models.NovaForwarderConfig{},
		&models.WarpGlobalConfig{},
		&models.WarpAccount{},
		&models.TrustTunnelConfig{},
		&models.TrustTunnelUser{},
		&models.TrustTunnelFirewallRule{},
		&models.RcloneRemote{},
		&models.RcloneUpload{},
		&models.RcloneFile{},
		&models.RcloneSystem{},
		// Storage guard tables: storageguard.Init runs before the torrent
		// manager (which also migrates these) and needs them at boot.
		&models.StorageConfig{},
		&models.StorageLog{},
	); err != nil {
		logger.Fatal("DB", "Auto migration failed", "error", err)
	}

	// Ensure the table collation is converted to utf8mb4 to support emoji/symbols in welcome messages
	if DB.Dialector.Name() == "mysql" {
		DB.Exec("ALTER TABLE `telegram_configs` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
		DB.Exec("ALTER TABLE `soroush_tunnel_configs` MODIFY COLUMN `call_access_hash` VARCHAR(1024) NULL")

		// Fix legacy api_token column in cloudflare_accounts if it exists
		var columnExists int64
		DB.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'cloudflare_accounts' AND COLUMN_NAME = 'api_token'", cfg.MySQLDBName).Scan(&columnExists)
		if columnExists > 0 {
			logger.Info("DB", "Migrating legacy api_token column in cloudflare_accounts to be nullable")
			DB.Exec("ALTER TABLE `cloudflare_accounts` MODIFY COLUMN `api_token` TEXT NULL")
		}
	}
	logger.Info("DB", "Schema migrations completed successfully")

	if cfg.AppMode == "client" {
		logger.Info("DB", "Executing client-only database schema migrations")
		if err := migrateDB.AutoMigrate(&models.V2RayScannerConfig{}); err != nil {
			logger.Fatal("DB", "Client scanner migration failed", "error", err)
		}

		// Seed default V2RayScannerConfig
		var scannerCfg models.V2RayScannerConfig
		if err := DB.First(&scannerCfg).Error; err != nil {
			logger.Info("DB", "Seeding default scanner configuration")
			DB.Create(&models.V2RayScannerConfig{
				ConcurrencyLimit:  100,
				TotalTargetCount:  1000,
				NetworkTimeoutSec: 5,
				ProbeAttempts:     1,
				Ports:             models.IntArray{443, 80, 8443, 2053, 2083, 2087, 2096, 8080, 8880, 2052, 2082, 2086, 2095},
				ConfigURLs:        models.StringArray{},
				TopLimit:          20,
				EnableNeighbors:   false,
				RequireWS:         false,
				TargetCIDRs:       models.StringArray{},
				TargetMode:        "http",
				TargetSNI:         "speed.cloudflare.com",
				MaxRateLimit:      0,
			})
		}

		// Seed default BondingEngineConfig
		var bondCfg models.BondingEngineConfig
		if err := DB.First(&bondCfg).Error; err != nil {
			logger.Info("DB", "Seeding default bonding engine configuration")
			DB.Create(&models.BondingEngineConfig{
				IsActive:      false,
				Mode:          "selector",
				StripingMode:  "auto",
				MaxArteries:   5,
				MinArteries:   2,
				FrameSize:     4096,
				SocksPort:     cfg.BondingSocksPort,
				HTTPPort:      cfg.BondingHTTPPort,
				EvalWindowMs:  5000,
				DemoteRTTx:    1.5,
				PromoteRTTx:   1.2,
				LossDemotePct: 5.0,
				CooldownSec:   30,
				ErrorBudget:   5,
			})
		}

		// Seed default WarpGlobalConfig
		var warpCfg models.WarpGlobalConfig
		if err := DB.First(&warpCfg).Error; err != nil {
			logger.Info("DB", "Seeding default WARP+ engine configuration")
			DB.Create(&models.WarpGlobalConfig{
				TransportMode: "masque",
				TargetSNI:     "consumer-masque.cloudflareclient.com",
				SocksPort:     10880,
				HTTPPort:      10881,
			})
		}
	}

	// Seed default LeechConfig
	var leechCfg models.LeechConfig
	if err := DB.First(&leechCfg).Error; err != nil {
		logger.Info("DB", "Seeding default remote downloader configuration")
		DB.Create(&models.LeechConfig{
			DefaultSavePath: "./downloads",
			MaxConcurrent:   3,
			ThreadsPerJob:   8,
			UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:126.0) Gecko/20100101 Firefox/126.0",
		})
	}

	// Seed default SchedulerConfig
	var schedCfg models.SchedulerConfig
	if err := DB.First(&schedCfg).Error; err != nil {
		logger.Info("DB", "Seeding default job scheduler configuration")
		DB.Create(&models.SchedulerConfig{
			MaxConcurrentJobs:   4,
			DefaultPriority:     5,
			RetryLimit:          3,
			RetryDelaySeconds:   30,
			JobTimeoutSeconds:   3600,
			PurgeAfterDays:      30,
			EnableCronJobs:      true,
			EnableNotifications: false,
		})
	}

	// Seed default YouTubeConfig
	var ytCfg models.YouTubeConfig
	if err := DB.First(&ytCfg).Error; err != nil {
		logger.Info("DB", "Seeding default YouTube downloader configuration")
		DB.Create(&models.YouTubeConfig{
			DefaultSavePath: "./downloads/youtube",
			MaxConcurrent:   2,
		})
	}
	// Seed default SpotifyConfig
	var spotifyCfg models.SpotifyConfig
	if err := DB.First(&spotifyCfg).Error; err != nil {
		logger.Info("DB", "Seeding default Spotify downloader configuration")
		DB.Create(&models.SpotifyConfig{
			DefaultSavePath:  "./downloads/spotify/audios",
			DefaultFormat:    "mp3",
			DefaultBitrate:   "320k",
			MaxConcurrent:    3,
			EmbedMetadata:    true,
			EmbedLyrics:      true,
			FileNameTemplate: "{artist} - {title}",
		})
	}

	// Seed default SoroushTunnelConfig
	var soroushCfg models.SoroushTunnelConfig
	if err := DB.First(&soroushCfg).Error; err != nil {
		logger.Info("DB", "Seeding default Soroush tunnel configuration")
		// Generate a cryptographically secure random PSK
		pskBytes := make([]byte, 32)
		if _, err := rand.Read(pskBytes); err != nil {
			logger.Fatal("DB", "Failed to generate PSK for Soroush tunnel", "error", err)
		}
		DB.Create(&models.SoroushTunnelConfig{
			PSK:             hex.EncodeToString(pskBytes),
			SocksPort:       4046,
			EngineMode:      "swarm",
			MaxWorkers:      5,
			LoadBalanceAlgo: "least-latency",
		})
	}

	// Seed default EhcoServerConfig
	var ehcoServerCfg models.EhcoServerConfig
	if err := DB.First(&ehcoServerCfg).Error; err != nil {
		logger.Info("DB", "Seeding default Ehco server configuration")
		tokenBytes := make([]byte, 16)
		_, _ = rand.Read(tokenBytes)
		DB.Create(&models.EhcoServerConfig{
			ListenPort: "3001",
			AuthToken:  hex.EncodeToString(tokenBytes),
			TargetMode: "direct",
			TargetHost: "127.0.0.1:80",
			EnableMux:  true,
			KeepAlive:  60,
			IsActive:   false,
		})
	}

	// Seed default EhcoClientConfig
	var ehcoClientCfg models.EhcoClientConfig
	if err := DB.First(&ehcoClientCfg).Error; err != nil {
		logger.Info("DB", "Seeding default Ehco client configuration")
		DB.Create(&models.EhcoClientConfig{
			LocalPort:    "1080",
			RemoteURL:    "",
			AuthToken:    "",
			SNI:          "",
			EnableMux:    true,
			KeepAlive:    60,
			BypassIR:     true,
			IsActive:     false,
			EnableBridge: false,
			BridgeURL:    "",
			BridgeSNI:    "",
		})
	}

	// Seed default TrustTunnelConfig
	var ttCfg models.TrustTunnelConfig
	if err := DB.First(&ttCfg).Error; err != nil {
		logger.Info("DB", "Seeding default TrustTunnel stealth protocol configuration")
		DB.Create(&models.TrustTunnelConfig{
			IsActive:                  false,
			ListenAddress:             "0.0.0.0:443",
			Socks5Port:                1088,
			HttpPort:                  1089,
			ForcedTransport:           "http2",
			AuthFailureStatusCode:     407,
			ClientRandomPrefix:        "a0b0/f0f0",
			H2InitialStreamWindowSize: 131072,
			H2InitialConnWindowSize:   262144,
			TlsHandshakeTimeoutSecs:   4,
			KillSwitchEnabled:         false,
			ActivePreset:              "iran-stealth",
		})
	} else {
		// Database already seeded — ensure legacy configurations are valid
		if ttCfg.AuthFailureStatusCode != 407 && ttCfg.AuthFailureStatusCode != 405 {
			logger.Info("DB", "Migrating invalid auth_failure_status_code in existing TrustTunnelConfig to 407", "old", ttCfg.AuthFailureStatusCode)
			ttCfg.AuthFailureStatusCode = 407
			DB.Save(&ttCfg)
		}
	}

	// Seed default ScannerSource
	var sourceCount int64
	if DB.Model(&models.ScannerSource{}).Count(&sourceCount); sourceCount == 0 {
		logger.Info("DB", "Seeding default scanner sources")
		defaultSources := []models.ScannerSource{
			{Name: "Cloudflare Official", URL: "https://www.cloudflare.com/ips-v4/", Type: "cidr", IsEnabled: true},
			{Name: "CM List", URL: "https://raw.githubusercontent.com/cmliu/cmliu/main/CF-CIDR.txt", Type: "cidr", IsEnabled: false},
			{Name: "AS13335 (Cloudflare)", URL: "https://raw.githubusercontent.com/ipverse/asn-ip/master/as/13335/ipv4-aggregated.txt", Type: "cidr", IsEnabled: false},
			{Name: "AS209242 (Cloudflare)", URL: "https://raw.githubusercontent.com/ipverse/asn-ip/master/as/209242/ipv4-aggregated.txt", Type: "cidr", IsEnabled: false},
			{Name: "AS24429 (Alibaba)", URL: "https://raw.githubusercontent.com/ipverse/asn-ip/master/as/24429/ipv4-aggregated.txt", Type: "cidr", IsEnabled: false},
			{Name: "AS199524 (G-Core)", URL: "https://raw.githubusercontent.com/ipverse/asn-ip/master/as/199524/ipv4-aggregated.txt", Type: "cidr", IsEnabled: false},
			{Name: "Reverse Proxy IPs", URL: "https://raw.githubusercontent.com/cmliu/ACL4SSR/main/baipiao.txt", Type: "proxyip", IsEnabled: false},
			{Name: "Foreign Domains", URL: "https://raw.githubusercontent.com/Blacknuno/Nova-Proxy/refs/heads/main/dominos.text", Type: "domain", IsEnabled: false},
			{Name: "Iranian Domains", URL: "https://raw.githubusercontent.com/Blacknuno/Nova-Proxy/refs/heads/main/IRdominos.text", Type: "domain", IsEnabled: false},
		}
		for _, s := range defaultSources {
			DB.Create(&s)
		}
	}

	// Seed default ScannerConfig
	var configCount int64
	if DB.Model(&models.ScannerConfig{}).Count(&configCount); configCount == 0 {
		logger.Info("DB", "Seeding default scanner config")
		DB.Create(&models.ScannerConfig{
			DeepTestEnabled:     true,
			TargetSNI:           "nova2.altramax083.workers.dev",
			AttemptCount:        3,
			MinSuccessThreshold: 2,
		})
	}

	// Seed default IPLookupConfig
	var ipLookupCount int64
	if DB.Model(&models.IPLookupConfig{}).Count(&ipLookupCount); ipLookupCount == 0 {
		logger.Info("DB", "Seeding default IP lookup configuration")
		DB.Create(&models.IPLookupConfig{
			EnableIP2Location:   true,
			EnableIpApi:         true,
			EnableIpGeolocation: true,
			EnableIpWhois:       true,
			EnableFindIP:        true,
		})
	}

	// Seed default DNSTesterConfig
	var dnsConfigCount int64
	if DB.Model(&models.DNSTesterConfig{}).Count(&dnsConfigCount); dnsConfigCount == 0 {
		logger.Info("DB", "Seeding default DNS Tester configuration")
		DB.Create(&models.DNSTesterConfig{
			ConcurrencyLimit: 100,
			QPSLimit:         500,
			TimeoutMs:        3000,
			Attempts:         3,
			CacheBusting:     true,
			ReferenceDomain:  "google.com",
			QueryTypes:       models.StringArray{"A", "AAAA"},
		})
	}

	// Seed default DNSResolver list
	var dnsResolverCount int64
	if DB.Model(&models.DNSResolver{}).Count(&dnsResolverCount); dnsResolverCount == 0 {
		logger.Info("DB", "Seeding default DNS Resolvers")
		defaultResolvers := []models.DNSResolver{
			{IP: "1.1.1.1", Protocol: "udp", ProviderName: "Cloudflare", Category: "general", SupportUDP: true},
			{IP: "1.0.0.1", Protocol: "udp", ProviderName: "Cloudflare", Category: "general", SupportUDP: true},
			{IP: "8.8.8.8", Protocol: "udp", ProviderName: "Google", Category: "general", SupportUDP: true},
			{IP: "8.8.4.4", Protocol: "udp", ProviderName: "Google", Category: "general", SupportUDP: true},
			{IP: "9.9.9.9", Protocol: "udp", ProviderName: "Quad9", Category: "security", SupportUDP: true, SupportTCP: true, SupportDoT: true},
			{IP: "149.112.112.112", Protocol: "udp", ProviderName: "Quad9", Category: "security", SupportUDP: true},
			{IP: "94.140.14.140", Protocol: "udp", ProviderName: "AdGuard Default", Category: "security", SupportUDP: true},
			{IP: "94.140.14.14", Protocol: "udp", ProviderName: "AdGuard Ad-Blocking", Category: "security", SupportUDP: true},
			{IP: "208.67.222.222", Protocol: "udp", ProviderName: "OpenDNS", Category: "general", SupportUDP: true},
			{IP: "1.1.1.1", Protocol: "dot", ProviderName: "Cloudflare DoT", Category: "general", SupportDoT: true},
			{IP: "8.8.8.8", Protocol: "dot", ProviderName: "Google DoT", Category: "general", SupportDoT: true},
			{IP: "cloudflare-dns.com", Protocol: "doh", ProviderName: "Cloudflare DoH", Category: "general", SupportDoH: true},
			{IP: "dns.google", Protocol: "doh", ProviderName: "Google DoH", Category: "general", SupportDoH: true},
		}
		for _, resolver := range defaultResolvers {
			DB.Create(&resolver)
		}
	}

	// Seed Admin User
	seedAdmin(cfg)

	return DB
}

func seedAdmin(cfg *config.Config) {
	var admin models.User
	result := DB.Where("username = ?", cfg.AdminUsername).First(&admin)
	if result.Error != nil {
		logger.Info("DB", "Seeding administrator account", "username", cfg.AdminUsername)

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			logger.Fatal("DB", "Failed to hash seeded password", "error", err)
		}

		admin = models.User{
			Username: cfg.AdminUsername,
			Password: string(hashedPassword),
			Role:     "admin",
		}

		if err := DB.Create(&admin).Error; err != nil {
			logger.Fatal("DB", "Failed to seed administrator", "error", err)
		}
		logger.Info("DB", "Administrator seeded successfully", "username", cfg.AdminUsername)
	} else {
		logger.Info("DB", "Seed integrity validated — administrator account exists", "username", cfg.AdminUsername)
	}
}
