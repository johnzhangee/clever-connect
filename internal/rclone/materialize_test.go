// materialize_test.go — tests for the cloud-storage rehydration helpers:
// candidate resolution against a temporary database and `rclone cat` fetches
// against a fake binary (same seam as the providers/index tests).
package rclone

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	ccdb "clever-connect/internal/db"
	"clever-connect/internal/models"
)

// writeFakeCatRclone installs a fake rclone binary that either prints content
// on stdout (success) or fails with exit code 1.
func writeFakeCatRclone(t *testing.T, content string, fail bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics are POSIX-only")
	}
	fake := filepath.Join(t.TempDir(), "rclone")
	script := "#!/bin/sh\nprintf '%s' '" + content + "'\nexit 0\n"
	if fail {
		script = "#!/bin/sh\necho 'boom' >&2\nexit 1\n"
	}
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}
	InvalidateBinaryCache()
	t.Setenv(BinEnvVar, fake)
	t.Cleanup(func() {
		InvalidateBinaryCache()
		// ConfigFilePath() writes ./data/tools/rclone/empty.conf relative to CWD.
		_ = os.RemoveAll("./data")
	})
}

func TestFindIndexedFileCandidates(t *testing.T) {
	setupIndexTestDB(t)

	enabled := models.RcloneRemote{Name: "s3main", Type: "s3", Enabled: true}
	disabled := models.RcloneRemote{Name: "old", Type: "s3", Enabled: false}
	other := models.RcloneRemote{Name: "drv", Type: "drive", Enabled: true}
	for _, r := range []*models.RcloneRemote{&enabled, &disabled, &other} {
		if err := ccdb.DB.Create(r).Error; err != nil {
			t.Fatalf("seed remote: %v", err)
		}
	}
	// GORM omits zero-value fields that carry a default (enabled:true), so
	// the disabled flag must be forced with an explicit update.
	if err := ccdb.DB.Model(&models.RcloneRemote{}).Where("id = ?", disabled.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable remote: %v", err)
	}

	exact := models.RcloneFile{RemoteID: enabled.ID, Path: "downloads/Twistys.com/23 - Sunny.mp4", Name: "23 - Sunny.mp4", Size: 42, PathHash: pathHashOf("downloads/Twistys.com/23 - Sunny.mp4")}
	flat := models.RcloneFile{RemoteID: other.ID, Path: "media/23 - Sunny.mp4", Name: "23 - Sunny.mp4", Size: 7, PathHash: pathHashOf("media/23 - Sunny.mp4")}
	off := models.RcloneFile{RemoteID: disabled.ID, Path: "downloads/Twistys.com/23 - Sunny.mp4", Name: "23 - Sunny.mp4", Size: 9, PathHash: pathHashOf("downloads/Twistys.com/23 - Sunny.mp4")}
	dir := models.RcloneFile{RemoteID: enabled.ID, Path: "downloads/Twistys.com", Name: "Twistys.com", IsDir: true, PathHash: pathHashOf("downloads/Twistys.com")}
	for _, f := range []*models.RcloneFile{&exact, &flat, &off, &dir} {
		if err := ccdb.DB.Create(f).Error; err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}

	// Exact manager-relative path + basename: enabled remotes only, the
	// exact-path row first, deterministic order. The disabled remote's row
	// and the directory row must never surface.
	got := FindIndexedFileCandidates("downloads/Twistys.com/23 - Sunny.mp4", "23 - Sunny.mp4")
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	if got[0].File.ID != exact.ID || got[0].Remote.Name != "s3main" {
		t.Errorf("first candidate should be the exact-path row on s3main, got remote %q", got[0].Remote.Name)
	}
	if got[1].File.ID != flat.ID || got[1].Remote.Name != "drv" {
		t.Errorf("second candidate should be the basename row on drv, got remote %q", got[1].Remote.Name)
	}

	// Empty relPath still resolves through the basename fallback.
	got = FindIndexedFileCandidates("", "23 - Sunny.mp4")
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates via basename only, got %d", len(got))
	}

	// Unknown basename → no candidates.
	if got := FindIndexedFileCandidates("", "nope.mp4"); len(got) != 0 {
		t.Errorf("expected no candidates for unknown basename, got %d", len(got))
	}
}

func TestMaterializeRemoteFile(t *testing.T) {
	setupIndexTestDB(t)
	writeFakeCatRclone(t, "cloud-payload-123", false)

	remote := models.RcloneRemote{Name: "s3main", Type: "s3", RootPrefix: "backups/clever", Enabled: true}
	if err := ccdb.DB.Create(&remote).Error; err != nil {
		t.Fatalf("seed remote: %v", err)
	}
	file := models.RcloneFile{RemoteID: remote.ID, Path: "movies/a.mp4", Name: "a.mp4", Size: 16}
	if err := ccdb.DB.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}

	path, cleanup, err := MaterializeRemoteFile(context.Background(), &remote, &file)
	if err != nil {
		t.Fatalf("MaterializeRemoteFile: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must not be nil")
	}
	if filepath.Ext(path) != ".mp4" {
		t.Errorf("temp file should keep the object extension, got %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if string(data) != "cloud-payload-123" {
		t.Errorf("unexpected content %q", string(data))
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup should remove the temp file, stat err=%v", err)
	}
}

func TestMaterializeRemoteFileFailures(t *testing.T) {
	setupIndexTestDB(t)

	remote := models.RcloneRemote{Name: "s3main", Type: "s3", Enabled: true}
	if err := ccdb.DB.Create(&remote).Error; err != nil {
		t.Fatalf("seed remote: %v", err)
	}
	file := models.RcloneFile{RemoteID: remote.ID, Path: "a.mp4", Name: "a.mp4", Size: 1}

	// Transport failure: rclone exits non-zero.
	writeFakeCatRclone(t, "", true)
	if _, _, err := MaterializeRemoteFile(context.Background(), &remote, &file); err == nil {
		t.Error("expected error when rclone cat fails")
	}

	// Empty stream must never be handed to the uploader.
	writeFakeCatRclone(t, "", false)
	if _, _, err := MaterializeRemoteFile(context.Background(), &remote, &file); err == nil {
		t.Error("expected error for an empty materialized copy")
	}
}
