// Package s3store provides a lightweight client for S3-compatible object
// storage (Clever Cloud Cellar). It auto-discovers credentials from:
//
//  1. The Clever Cloud injected s3cmd config file (configs/*-s3cfg)
//  2. Clever Cloud environment variables (CELLAR_ADDON_*)
//  3. Manual S3_* environment variables
//
// If no configuration is found, the store simply reports itself as not
// available and callers no-op gracefully.
package s3store

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the fully resolved S3 endpoint configuration.
type Config struct {
	Endpoint  string // bare host[:port], no scheme
	Secure    bool   // HTTPS?
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	Source    string // human-readable origin of this configuration
}

// String renders the config without leaking the secret key.
func (c Config) String() string {
	return fmt.Sprintf("endpoint=%s bucket=%q region=%s secure=%v source=%s",
		c.Endpoint, c.Bucket, c.Region, c.Secure, c.Source)
}

// parseS3CfgCookie parses an s3cmd-format INI config into a lowercase key map.
// The Clever Cloud provided file is a flat `key = value` list (optionally
// preceded by an s3cmd-compatible [default] section header).
func parseS3CfgCookie(r io.Reader) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			continue // section header (e.g. [default]) — values are flat anyway
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(line[:eq]), `"`)
		val := strings.Trim(strings.TrimSpace(line[eq+1:]), `"`)
		values[strings.ToLower(key)] = strings.ReplaceAll(val, "%(...)s", "")
	}
	return values
}

// BuildConfigFromS3CfgValues converts a parsed s3cmd value map into a Config.
// filename is used to derive the bucket name (Clever Cloud names the file
// "<add-on id with '_' replaced by '-'>-s3cfg").
func BuildConfigFromS3CfgValues(values map[string]string, filename string) Config {
	secure := true
	if v, ok := values["use_https"]; ok {
		b, _ := strconv.ParseBool(v)
		secure = b
	}
	region := "us-east-1"
	if v, ok := values["bucket_location"]; ok && v != "" && v != "US" {
		region = v
	}
	return Config{
		Endpoint:  strings.TrimSuffix(strings.TrimPrefix(values["host_base"], "https://"), "/"),
		Secure:    secure,
		AccessKey: values["access_key"],
		SecretKey: values["secret_key"],
		Bucket:    DeriveBucketFromConfigFilename(filename),
		Region:    region,
		Source:    "s3cfg file " + filename,
	}
}

// DeriveBucketFromConfigFilename extracts the bucket name from a Clever Cloud
// s3cmd config filename. Clever Cloud stores the file as
// "<add-on id with '_' replaced by '-'>-s3cfg" and the add-on id (equal to the
// default bucket name) looks like "cellar_9a75d5a4-...":
//
//	"cellar-cellar_9a75d5a4-...-s3cfg" -> "cellar_9a75d5a4-..."
//
// The leading "cellar-" part is only the add-on type marker; the bucket id
// always starts with "cellar_" so the marker is stripped at that point.
func DeriveBucketFromConfigFilename(filename string) string {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".s3cfg")
	base = strings.TrimSuffix(base, "-s3cfg")
	if idx := strings.Index(base, "cellar_"); idx > 0 {
		base = base[idx:]
	}
	return base
}
