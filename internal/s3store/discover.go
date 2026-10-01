package s3store

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"clever-connect/internal/logger"
)

// discoverConfigFilePaths returns candidate s3cfg file locations in priority order.
func discoverConfigFilePaths() []string {
	// Explicit file or directory override wins
	if p := os.Getenv("S3_CONFIG_PATH"); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return []string{p}
		}
		if entries, err := os.ReadDir(p); err == nil {
			var files []string
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if strings.HasSuffix(e.Name(), "-s3cfg") || strings.HasSuffix(e.Name(), ".s3cfg") {
					files = append(files, filepath.Join(p, e.Name()))
				}
			}
			sort.Strings(files)
			return files
		}
	}

	// Default locations relative to the working directory and Clever images
	locators := []string{"configs", "./configs", "/app/configs", "/app/config"}
	seen := map[string]bool{}
	result := []string{}
	for _, dir := range locators {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var files []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if (strings.HasPrefix(name, "cellar-") && strings.HasSuffix(name, "-s3cfg")) ||
				strings.HasSuffix(name, ".s3cfg") {
				files = append(files, filepath.Join(dir, name))
			}
		}
		sort.Strings(files)
		result = append(result, files...)
	}
	return result
}

// LoadDiscoveryConfig resolves storage credentials from the environment and
// the on-disk Clever Cloud s3cfg file. Returns nil when nothing is found.
func LoadDiscoveryConfig() *Config {
	// 1. Manual S3_* environment variables (highest priority)
	if os.Getenv("S3_ENDPOINT") != "" &&
		os.Getenv("S3_ACCESS_KEY") != "" &&
		os.Getenv("S3_SECRET_KEY") != "" {

		bucket := os.Getenv("S3_BUCKET")
		if bucket == "" {
			bucket = os.Getenv("S3_BUCKET_NAME")
		}
		ep := os.Getenv("S3_ENDPOINT")
		secure := true
		if v := os.Getenv("S3_SECURE"); v != "" {
			b, _ := strconv.ParseBool(v)
			secure = b
		} else if strings.HasPrefix(ep, "http://") {
			secure = false
		}
		region := os.Getenv("S3_REGION")
		if region == "" {
			region = "us-east-1"
		}
		return &Config{
			Endpoint:  strings.TrimPrefix(strings.TrimPrefix(ep, "http://"), "https://"),
			Secure:    secure,
			AccessKey: os.Getenv("S3_ACCESS_KEY"),
			SecretKey: os.Getenv("S3_SECRET_KEY"),
			Bucket:    bucket,
			Region:    region,
			Source:    "S3_* environment variables",
		}
	}

	// 2. Clever Cloud injected environment variables
	if host := os.Getenv("CELLAR_ADDON_HOST"); host != "" &&
		os.Getenv("CELLAR_ADDON_KEY") != "" &&
		os.Getenv("CELLAR_ADDON_PASSWORD") != "" {

		bucket := os.Getenv("CELLAR_ADDON_BUCKET")
		if bucket == "" {
			bucket = os.Getenv("S3_BUCKET")
		}
		return &Config{
			Endpoint:  strings.TrimPrefix(host, "https://"),
			Secure:    true,
			AccessKey: os.Getenv("CELLAR_ADDON_KEY"),
			SecretKey: os.Getenv("CELLAR_ADDON_PASSWORD"),
			Bucket:    bucket,
			Region:    "us-east-1",
			Source:    "CELLAR_ADDON_* environment variables",
		}
	}

	// 3. Clever Cloud s3cfg config file on disk
	for _, path := range discoverConfigFilePaths() {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		values := parseS3CfgCookie(f)
		_ = f.Close()
		cfg := BuildConfigFromS3CfgValues(values, path)
		if cfg.AccessKey != "" && cfg.SecretKey != "" && cfg.Endpoint != "" {
			return &cfg
		}
	}

	return nil
}

// LogFailedDiscovery emits a warning once when no S3 configuration could be
// located, so admins know the smart storage features are disabled.
func LogFailedDiscovery() {
	logger.Warn("S3Store", "No S3 configuration found (no configs/*-s3cfg file and no S3_*/CELLAR_* env vars) — smart S3 offloading is disabled")
}
