// rcloneindex_test.go — covers the cloud-storage recovery tier of the upload
// resolver: a manager path that exists on an indexed remote (but neither on
// local disk nor in the S3 archive) must rehydrate through `rclone cat` into
// a temp file that is deleted right after the upload.
package filecore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	ccdb "clever-connect/internal/db"
	sqlite "clever-connect/internal/db/sqlite"
	"clever-connect/internal/models"
	ccrclone "clever-connect/internal/rclone"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// setupRcloneIndexTestDB points the package-wide DB handle at a fresh SQLite
// file with every table the resolver touches.
func setupRcloneIndexTestDB(t *testing.T) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "rclone-index.db")), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.FileRegistry{}, &models.RcloneRemote{}, &models.RcloneFile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := ccdb.DB
	ccdb.DB = gdb
	t.Cleanup(func() { ccdb.DB = prev })
}

// writeFakeCatRclone installs a fake rclone binary whose stdout is content.
func writeFakeCatRclone(t *testing.T, content string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics are POSIX-only")
	}
	fake := filepath.Join(t.TempDir(), "rclone")
	script := "#!/bin/sh\nprintf '%s' '" + content + "'\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}
	ccrclone.InvalidateBinaryCache()
	t.Setenv(ccrclone.BinEnvVar, fake)
	t.Cleanup(func() {
		ccrclone.InvalidateBinaryCache()
		// ConfigFilePath() writes ./data/tools/rclone/empty.conf relative to CWD.
		_ = os.RemoveAll("./data")
	})
}

// managerSandboxPath builds the absolute sandbox path for a relative path.
func managerSandboxPath(rel string) string {
	absBase, err := filepath.Abs("./data/manager")
	if err != nil {
		absBase = "./data/manager"
	}
	return filepath.Join(absBase, filepath.FromSlash(rel))
}

// TestMaterializeForUploadFallsThroughToRcloneIndex reproduces the reported
// production failure: the file lives on a configured cloud remote (indexed
// under its manager-relative folder structure) while the local copy is gone
// and no S3 archive record exists — the upload must still succeed from a
// transient rehydrated copy.
func TestMaterializeForUploadFallsThroughToRcloneIndex(t *testing.T) {
	setupRcloneIndexTestDB(t)
	writeFakeCatRclone(t, "rehydrated-from-cloud")

	remote := models.RcloneRemote{Name: "s3main", Type: "s3", Enabled: true}
	if err := ccdb.DB.Create(&remote).Error; err != nil {
		t.Fatalf("seed remote: %v", err)
	}
	file := models.RcloneFile{
		RemoteID: remote.ID,
		Path:     "downloads/Twistys.com/23 - Sunny With A Chance Of Orgasm.mp4",
		Name:     "23 - Sunny With A Chance Of Orgasm.mp4",
		Size:     int64(len("rehydrated-from-cloud")),
	}
	if err := ccdb.DB.Create(&file).Error; err != nil {
		t.Fatalf("seed indexed file: %v", err)
	}

	abs := managerSandboxPath("downloads/Twistys.com/23 - Sunny With A Chance Of Orgasm.mp4")
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("precondition: no local copy should exist at %s", abs)
	}

	path, cleanup, err := MaterializeForUpload(abs)
	if err != nil {
		t.Fatalf("MaterializeForUpload should recover from the cloud index: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must not be nil")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if string(data) != "rehydrated-from-cloud" {
		t.Errorf("unexpected content %q", string(data))
	}
	if !strings.HasSuffix(path, ".mp4") {
		t.Errorf("temp file should keep the extension, got %s", path)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("cleanup should delete the transient copy after the upload")
	}
}

// TestMaterializeForUploadBasenameOnlyIndexHit covers flat uploads stored
// under a different folder on the remote: only the basename matches.
func TestMaterializeForUploadBasenameOnlyIndexHit(t *testing.T) {
	setupRcloneIndexTestDB(t)
	writeFakeCatRclone(t, "rehydrated-from-cloud")

	remote := models.RcloneRemote{Name: "flatdrive", Type: "drive", Enabled: true}
	if err := ccdb.DB.Create(&remote).Error; err != nil {
		t.Fatalf("seed remote: %v", err)
	}
	file := models.RcloneFile{RemoteID: remote.ID, Path: "media/23 - Sunny.mp4", Name: "23 - Sunny.mp4", Size: 21}
	if err := ccdb.DB.Create(&file).Error; err != nil {
		t.Fatalf("seed indexed file: %v", err)
	}

	path, cleanup, err := MaterializeForUpload(managerSandboxPath("downloads/Twistys.com/23 - Sunny.mp4"))
	if err != nil {
		t.Fatalf("MaterializeForUpload should recover by basename: %v", err)
	}
	defer cleanup()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
}

// TestMaterializeForUploadStillFailsWhenNowhere keeps the terminal error
// honest when the file is on neither disk, S3 nor any cloud remote.
func TestMaterializeForUploadStillFailsWhenNowhere(t *testing.T) {
	setupRcloneIndexTestDB(t)
	writeFakeCatRclone(t, "rehydrated-from-cloud")

	_, _, err := MaterializeForUpload(managerSandboxPath("downloads/does-not-exist-anywhere.mp4"))
	if err == nil {
		t.Fatal("expected the terminal not-found error")
	}
	if !strings.Contains(err.Error(), "not found locally") {
		t.Errorf("unexpected error: %v", err)
	}
}
