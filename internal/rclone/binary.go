// binary.go — rclone binary discovery and self-provisioning.
//
// Resolution order (first hit wins):
//  1. RCLONE_BINARY env var (explicit admin override)
//  2. the managed copy previously installed by this feature
//  3. `rclone` found on PATH
//
// When nothing is found and auto-install is enabled in models.RcloneSystem, the
// current stable release is downloaded from downloads.rclone.org, the release's
// SHA256SUMS file verifies the archive, the binary is extracted fully in
// memory, chmod 0700'd and atomically renamed into ./data/tools/rclone/bin/.
package rclone

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

const (
	// BinEnvVar allows explicitly pointing the engine at a custom rclone build.
	BinEnvVar = "RCLONE_BINARY"
	// managedBinDir is where self-provisioned binaries live.
	managedBinDir = "./data/tools/rclone/bin"
	// downloadRoot is the official rclone download service.
	downloadRoot = "https://downloads.rclone.org"
	// downloadUA identifies this app in fetch requests.
	downloadUA = "clever-connect-cloud-storage/1.0"
)

var (
	// cachedBinary caches the resolved binary path for this process.
	cachedBinary   string
	cacheBinaryMtx sync.Mutex
	// cachedVersion caches the parsed `rclone version` output line.
	cachedVersion string
	versionMtx    sync.Mutex
)

// System returns the singleton RcloneSystem settings row (ID 1), creating it on
// first access. Returns an error when the database is not initialized yet
// (unit tests must never see a nil-pointer panic from here).
func System() (*models.RcloneSystem, error) {
	if db.DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	var sys models.RcloneSystem
	if err := db.DB.Where("id = ?", 1).First(&sys).Error; err != nil {
		sys = models.RcloneSystem{ManagedBinary: managersDefaultPath()}
		if err := db.DB.Create(&sys).Error; err != nil {
			return nil, fmt.Errorf("failed to seed rclone system config: %w", err)
		}
	}
	return &sys, nil
}

// managersDefaultPath is where a managed install will be placed.
func managersDefaultPath() string {
	base, err := filepath.Abs(managedBinDir)
	if err != nil {
		base = managedBinDir
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return filepath.Join(base, "rclone"+suffix)
}

// isExecutableLike reports whether the path exists and is a regular file.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// ResolveBinary returns a usable rclone binary path following env override →
// managed copy → PATH lookup.
func ResolveBinary() (string, error) {
	cacheBinaryMtx.Lock()
	cached := cachedBinary
	cacheBinaryMtx.Unlock()
	if cached != "" {
		return cached, nil
	}

	var candidates []string
	if fromEnv := strings.TrimSpace(os.Getenv(BinEnvVar)); fromEnv != "" {
		candidates = append(candidates, fromEnv)
	}
	if sys, err := System(); err == nil && sys.ManagedBinary != "" {
		candidates = append(candidates, sys.ManagedBinary)
	}
	if onPath, err := exec.LookPath("rclone"); err == nil {
		candidates = append(candidates, onPath)
	}

	for _, cand := range candidates {
		if isRegularFile(cand) {
			abs, err := filepath.Abs(cand)
			if err != nil {
				abs = cand
			}
			cacheBinaryMtx.Lock()
			cachedBinary = abs
			cacheBinaryMtx.Unlock()
			return abs, nil
		}
	}

	return "", fmt.Errorf("no rclone binary available — set RCLONE_BINARY, install rclone on PATH, or enable auto-install in the Cloud Storage page")
}

// InvalidateBinaryCache forces re-resolution (used after install).
func InvalidateBinaryCache() {
	cacheBinaryMtx.Lock()
	cachedBinary = ""
	cacheBinaryMtx.Unlock()
	versionMtx.Lock()
	cachedVersion = ""
	versionMtx.Unlock()
}

// Version runs `rclone version` and returns the first output line
// (e.g. "rclone v1.75.1"). Result is cached for the process lifetime.
func Version() (string, error) {
	versionMtx.Lock()
	cached := cachedVersion
	versionMtx.Unlock()
	if cached != "" {
		return cached, nil
	}

	res, err := Run(context.Background(), nil, nil, 15*time.Second, "version")
	if err != nil {
		return "", err
	}
	first := res.Stdout
	if idx := strings.IndexByte(first, '\n'); idx > 0 {
		first = first[:idx]
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return "", fmt.Errorf("rclone version returned no output")
	}
	versionMtx.Lock()
	cachedVersion = first
	versionMtx.Unlock()
	return first, nil
}

// latestStableVersion fetches the dotted release (e.g. "1.75.1") served by
// downloads.rclone.org/version.txt and strips the "rclone " prefix.
func latestStableVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadRoot+"/version.txt", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", downloadUA)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch current rclone version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP %d fetching rclone version", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(body))
	// First line is like "rclone v1.75.1" — normalize to the dotted version.
	if idx := strings.IndexByte(line, '\n'); idx > 0 {
		line = strings.TrimSpace(line[:idx])
	}
	line = strings.TrimPrefix(line, "rclone ")
	return strings.TrimSpace(strings.TrimPrefix(line, "v")), nil
}

// releaseAssetLabel maps Go platform strings onto the names rclone uses for
// its published archives (their macOS prefix is "osx", everything else is the
// GOOS spelling of the build environment).
func releaseAssetLabel() (string, string, string, error) {
	goos := runtime.GOOS
	forGo := goos
	if goos == "darwin" {
		forGo = "osx"
	}
	return goos, forGo, runtime.GOARCH, nil
}

// extractBinaryFromZip streams the rclone executable out of the official zip
// archive entirely in memory (archives are ~20-25 MiB) and returns its bytes.
func extractBinaryFromZip(zipBytes []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to open downloaded rclone zip: %w", err)
	}
	want := "rclone"
	if runtime.GOOS == "windows" {
		want = "rclone.exe"
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Entries look like "rclone-v1.75.1-linux-amd64/rclone".
		if f.Name == want || strings.HasSuffix(f.Name, "/"+want) {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("rclone executable entry %q not found in downloaded archive", want)
}

// verifyZipSHA256 validates downloaded artifact bytes against the official
// SHA256SUMS file published in the same release directory.
func verifyZipSHA256(ctx context.Context, release, zipName string, zipBytes []byte) error {
	sumsURL := fmt.Sprintf("%s/%s/SHA256SUMS", downloadRoot, release)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", downloadUA)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download SHA256SUMS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP %d fetching SHA256SUMS", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	digest := sha256.Sum256(zipBytes)
	actual := hex.EncodeToString(digest[:])
	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) != 2 {
			continue
		}
		wantSum, name := parts[0], parts[1]
		if strings.TrimPrefix(name, "*") == zipName && strings.EqualFold(wantSum, actual) {
			return nil
		}
	}
	return fmt.Errorf("sha256 mismatch for %s (computed %s)", zipName, actual)
}

// InstallManaged downloads, verifies and installs the current stable rclone
// build into the managed binary directory and records it in the system config.
// An existing intact managed binary is reused unless force is true.
func InstallManaged(ctx context.Context, force bool) (string, error) {
	dest := managersDefaultPath()
	if !force && isRegularFile(dest) {
		if _, err := Version(); err == nil {
			return dest, nil
		}
	}

	version, err := latestStableVersion(ctx)
	if err != nil {
		return "", err
	}

	_, osLabel, archLabel, err := releaseAssetLabel()
	if err != nil {
		return "", err
	}
	zipName := fmt.Sprintf("rclone-v%s-%s-%s.zip", version, osLabel, archLabel)
	zipURL := fmt.Sprintf("%s/v%s/%s", downloadRoot, version, zipName)

	logger.Info("Rclone", "Downloading rclone release", "version", version, "url", zipURL)
	dlCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, zipURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", downloadUA)
	client := &http.Client{Timeout: 4 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download rclone archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP %d downloading %s", resp.StatusCode, zipURL)
	}
	zipBytes, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return "", fmt.Errorf("failed to read rclone archive: %w", err)
	}

	if err := verifyZipSHA256(ctx, "v"+version, zipName, zipBytes); err != nil {
		return "", err
	}
	logger.Info("Rclone", "rclone archive SHA256 verified", "version", version)

	binBytes, err := extractBinaryFromZip(zipBytes)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}
	// Atomic-ish install: write a temp file, chmod 0700, swap into place.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".rclone-inst-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(binBytes); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0700); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", err
	}

	abs, err := filepath.Abs(dest)
	if err != nil {
		abs = dest
	}

	// Record the managed binary location.
	if sys, err := System(); err == nil {
		db.DB.Model(&models.RcloneSystem{}).Where("id = ?", sys.ID).
			Update("managed_binary", abs)
	}

	InvalidateBinaryCache()
	logger.Info("Rclone", "rclone binary installed", "path", abs)

	// Sanity-check the fresh binary before declaring success.
	if ver, err := Version(); err != nil {
		return abs, fmt.Errorf("binary installed but failed runtime check: %w", err)
	} else {
		logger.Info("Rclone", "installed rclone passes runtime check", "version", ver)
	}
	return abs, nil
}

// EnsureBinary guarantees a callable rclone binary, auto-installing when the
// admin enabled auto_install, otherwise returning a descriptive error.
func EnsureBinary(ctx context.Context) (string, error) {
	if bin, err := ResolveBinary(); err == nil {
		return bin, nil
	}
	sys, err := System()
	if err != nil {
		return "", err
	}
	if !sys.AutoInstall {
		return "", fmt.Errorf("rclone binary not found and auto-install is disabled")
	}
	return InstallManaged(ctx, false)
}
