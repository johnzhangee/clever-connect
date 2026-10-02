package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	AppMode             string // "client" or "server"
	Port                string
	JWTSecret           []byte
	WSHeartbeatInterval time.Duration
	ServerURL           string
	ServerAuthToken     string

	// SQLite (Client mode)
	SQLitePath string

	// PostgreSQL (Server mode)
	PostgresUser     string
	PostgresPassword string
	PostgresHost     string
	PostgresPort     string
	PostgresDBName   string

	// PostgresURI is the full connection URI/DSN handed to the GORM driver.
	// It is taken from POSTGRESQL_ADDON_URI (Clever Cloud PostgreSQL addon)
	// or built from the individual POSTGRES_* variables.
	PostgresURI string

	// Seed Admin
	AdminUsername string
	AdminPassword string

	// DMB Bonding Engine
	BondingSocksPort   int
	BondingHTTPPort    int
	BondingPSKHex      string
	BondingCombinerURL string
	BondingMaxArteries int
	BondingFrameSize   int

	// Cloudflare OAuth
	CloudflareClientID     string
	CloudflareClientSecret string
	CloudflareRedirectURL  string
	CloudflareScopes       []string

	// S3-Compatible Object Storage (Clever Cloud Cellar)
	// When all three connection values are present, S3 storage is enabled and
	// fetched files are uploaded to / streamed from the object store.
	S3Enabled   bool
	S3Host      string // e.g. cellar-fr-north-hds-c1.services.clever-cloud.com
	S3KeyID     string
	S3KeySecret string
	S3Bucket    string // bucket name (auto-created on boot if missing)
	S3Region    string // SigV4 signing region (default "us-east-1")
}

func LoadConfig() *Config {
	// Try loading from .env if present
	_ = godotenv.Load()

	appMode := os.Getenv("APP_MODE")
	if appMode == "" {
		appMode = "client" // default
	}

	port := os.Getenv("PORT")
	if port == "" {
		if appMode == "server" {
			port = "8081"
		} else {
			port = "8080"
		}
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "super-secret-jwt-key"
	}

	wsIntervalStr := os.Getenv("WS_HEARTBEAT_INTERVAL")
	wsInterval := 5 * time.Second
	if wsIntervalStr != "" {
		if parsed, err := time.ParseDuration(wsIntervalStr); err == nil {
			wsInterval = parsed
		}
	}

	serverURL := os.Getenv("SERVER_URL")
	if serverURL == "" {
		serverURL = os.Getenv("CLIVER_SERVER_URL")
	}

	serverAuthToken := os.Getenv("SERVER_AUTH_TOKEN")
	if serverAuthToken == "" {
		serverAuthToken = os.Getenv("CLIVER_SERVER_AUTH_TOKEN")
	}

	cfg := &Config{
		AppMode:             appMode,
		Port:                port,
		JWTSecret:           []byte(jwtSecret),
		WSHeartbeatInterval: wsInterval,
		ServerURL:           serverURL,
		// DMB Bonding Engine
		BondingSocksPort:   getEnvInt("BONDING_SOCKS_PORT", 10646),
		BondingHTTPPort:    getEnvInt("BONDING_HTTP_PORT", 10545),
		BondingPSKHex:      getEnv("BONDING_PSK_HEX", ""),
		BondingCombinerURL: getEnv("BONDING_COMBINER_URL", ""),
		BondingMaxArteries: getEnvInt("BONDING_MAX_ARTERIES", 5),
		BondingFrameSize:   getEnvInt("BONDING_FRAME_SIZE", 4096),
		ServerAuthToken:     serverAuthToken,
		SQLitePath:          getEnv("SQLITE_DB_PATH", resolveDefaultClientDBPath()),
		PostgresUser:        getEnv("POSTGRES_USER", "postgres"),
		PostgresPassword:    os.Getenv("POSTGRES_PASSWORD"),
		PostgresHost:        getEnv("POSTGRES_HOST", "127.0.0.1"),
		PostgresPort:        getEnv("POSTGRES_PORT", "5432"),
		PostgresDBName:      getEnv("POSTGRES_DB_NAME", "clever_connect_server"),
		AdminUsername:       getEnv("ADMIN_USERNAME", "salman"),
		AdminPassword:       getEnv("ADMIN_PASSWORD", "136517"),

		// Cloudflare OAuth
		CloudflareClientID:     getEnv("CLOUDFLARE_CLIENT_ID", ""),
		CloudflareClientSecret: getEnv("CLOUDFLARE_CLIENT_SECRET", ""),
		CloudflareRedirectURL:  getEnv("CLOUDFLARE_REDIRECT_URL", ""),
		CloudflareScopes:       parseScopes(getEnv("CLOUDFLARE_SCOPES", "account.read,zone.read,zone.write,workers.read,workers.write")),

		// S3-Compatible Object Storage (Clever Cloud Cellar)
		S3Host:      strings.TrimSpace(os.Getenv("CELLAR_ADDON_HOST")),
		S3KeyID:     strings.TrimSpace(os.Getenv("CELLAR_ADDON_KEY_ID")),
		S3KeySecret: os.Getenv("CELLAR_ADDON_KEY_SECRET"),
		S3Bucket:    getEnv("CELLAR_ADDON_BUCKET", "clever-connect"),
		S3Region:    getEnv("CELLAR_ADDON_REGION", "us-east-1"),
	}

	// S3 is considered enabled only when the full credential triple is present
	cfg.S3Enabled = cfg.S3Host != "" && cfg.S3KeyID != "" && cfg.S3KeySecret != ""

	// Automatic parsing of database URIs (e.g. from Clever Cloud PostgreSQL
	// addon). The full URI is handed to the driver verbatim; the individual
	// fields are only parsed for logging purposes.
	pgURI := os.Getenv("POSTGRESQL_ADDON_URI")
	if pgURI == "" {
		pgURI = os.Getenv("POSTGRESQL_URI")
	}
	if pgURI == "" {
		pgURI = os.Getenv("DATABASE_URL")
	}
	if pgURI != "" && (strings.HasPrefix(pgURI, "postgres://") || strings.HasPrefix(pgURI, "postgresql://")) {
		if u, err := url.Parse(pgURI); err == nil {
			if u.User != nil {
				cfg.PostgresUser = u.User.Username()
				if pw, ok := u.User.Password(); ok {
					cfg.PostgresPassword = pw
				}
			}
			if u.Hostname() != "" {
				cfg.PostgresHost = u.Hostname()
			}
			if u.Port() != "" {
				cfg.PostgresPort = u.Port()
			}
			if db := strings.TrimPrefix(u.Path, "/"); db != "" {
				cfg.PostgresDBName = db
			}
		}
		cfg.PostgresURI = pgURI
	} else {
		// Build a keyword/value DSN from the individual variables.
		cfg.PostgresURI = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=prefer",
			cfg.PostgresHost,
			cfg.PostgresPort,
			cfg.PostgresUser,
			cfg.PostgresPassword,
			cfg.PostgresDBName,
		)
	}

	return cfg
}

func getEnv(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}

func getEnvInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return n
}

func resolveDefaultClientDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "data/client.db"
	}
	return filepath.Join(home, ".config", "cleverconnect", "v2ray.db")
}

func parseScopes(val string) []string {
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	var scopes []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			scopes = append(scopes, trimmed)
		}
	}
	return scopes
}
