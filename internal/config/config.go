package config

import (
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

	// MySQL (Server mode)
	MySQLUser     string
	MySQLPassword string
	MySQLHost     string
	MySQLPort     string
	MySQLDBName   string

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
		MySQLUser:           getEnv("MYSQL_USER", "root"),
		MySQLPassword:       os.Getenv("MYSQL_PASSWORD"),
		MySQLHost:           getEnv("MYSQL_HOST", "127.0.0.1"),
		MySQLPort:           getEnv("MYSQL_PORT", "3306"),
		MySQLDBName:         getEnv("MYSQL_DB_NAME", "clever_connect_server"),
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

	// Automatic parsing of database URIs (e.g. from Clever Cloud MySQL addon)
	mysqlURI := os.Getenv("MYSQL_ADDON_URI")
	if mysqlURI == "" {
		mysqlURI = os.Getenv("DATABASE_URL")
	}
	if mysqlURI != "" && strings.HasPrefix(mysqlURI, "mysql://") {
		uri := strings.TrimPrefix(mysqlURI, "mysql://")
		parts := strings.SplitN(uri, "@", 2)
		if len(parts) == 2 {
			userPass := parts[0]
			hostPortDb := parts[1]

			up := strings.SplitN(userPass, ":", 2)
			if len(up) == 2 {
				cfg.MySQLUser = up[0]
				cfg.MySQLPassword = up[1]
			}

			hpdb := strings.SplitN(hostPortDb, "/", 2)
			if len(hpdb) == 2 {
				hostPort := hpdb[0]
				cfg.MySQLDBName = hpdb[1]

				if idx := strings.Index(cfg.MySQLDBName, "?"); idx > 0 {
					cfg.MySQLDBName = cfg.MySQLDBName[:idx]
				}

				hp := strings.SplitN(hostPort, ":", 2)
				if len(hp) == 2 {
					cfg.MySQLHost = hp[0]
					cfg.MySQLPort = hp[1]
				} else {
					cfg.MySQLHost = hostPort
					cfg.MySQLPort = "3306"
				}
			}
		}
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
