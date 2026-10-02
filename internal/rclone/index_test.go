// index_test.go — tests for the provider file index: pure path/time helpers
// plus fake-rclone end-to-end reconciles against a temporary database.
package rclone

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	ccdb "clever-connect/internal/db"
	ccsqlite "clever-connect/internal/db/sqlite"
	"clever-connect/internal/models"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// setupIndexTestDB points the package-wide DB handle at a fresh SQLite file.
func setupIndexTestDB(t *testing.T) {
	t.Helper()
	gdb, err := gorm.Open(ccsqlite.Open(filepath.Join(t.TempDir(), "index.db")), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.RcloneRemote{}, &models.RcloneUpload{}, &models.RcloneFile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := ccdb.DB
	ccdb.DB = gdb
	t.Cleanup(func() { ccdb.DB = prev })
}

// writeFakeListerRclone installs a fake rclone binary whose lsjson output is
// whatever currently sits in outPath (rewrite the file between syncs).
func writeFakeListerRclone(t *testing.T, outPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics are POSIX-only")
	}
	fake := filepath.Join(t.TempDir(), "rclone")
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = lsjson ]; then cat \"" + outPath + "\"; exit 0; fi\ndone\nexit 0\n"
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

func TestRelativeRemotePath(t *testing.T) {
	withRoot := &models.RcloneRemote{Name: "s3main", RootPrefix: "backups/clever"}
	noRoot := &models.RcloneRemote{Name: "mydrv"}

	cases := []struct {
		remote   *models.RcloneRemote
		fullPath string
		want     string
	}{
		{withRoot, "s3main:backups/clever/docs/a.pdf", "docs/a.pdf"},
		{withRoot, "s3main:backups/clever", ""},
		{withRoot, "s3main:", ""},
		{noRoot, "mydrv:file.txt", "file.txt"},
		{noRoot, "mydrv:", ""},
		// A path that is not under the root must never lose a path fragment
		// to a naive prefix strip ("backups2" vs root "backups").
		{&models.RcloneRemote{Name: "r", RootPrefix: "backups"}, "r:backups2/x", "backups2/x"},
	}
	for _, tc := range cases {
		if got := RelativeRemotePath(tc.remote, tc.fullPath); got != tc.want {
			t.Errorf("RelativeRemotePath(%q) = %q, want %q", tc.fullPath, got, tc.want)
		}
	}
}

func TestParseRemoteTime(t *testing.T) {
	if parseRemoteTime("") != nil {
		t.Error("empty ModTime must parse to nil")
	}
	if parseRemoteTime("0001-01-01T00:00:00Z") != nil {
		t.Error("rclone zero time must parse to nil")
	}
	got := parseRemoteTime("2024-05-01T10:05:00Z")
	if got == nil || got.UTC().Format(time.RFC3339) != "2024-05-01T10:05:00Z" {
		t.Errorf("RFC3339 parse = %+v", got)
	}
	if parseRemoteTime("2024-05-01 10:05:00") == nil {
		t.Error("space-separated layout must parse")
	}
	if parseRemoteTime("not-a-time") != nil {
		t.Error("garbage must parse to nil")
	}
}

func TestSyncRemoteFilesReconcilesProviderContents(t *testing.T) {
	setupIndexTestDB(t)

	dir := t.TempDir()
	lsjson := filepath.Join(dir, "listing.json")
	writeFakeListerRclone(t, lsjson)

	remote := &models.RcloneRemote{Name: "s3main", Type: "s3", RootPrefix: "backups", Enabled: true}
	if err := ccdb.DB.Create(remote).Error; err != nil {
		t.Fatalf("create remote: %v", err)
	}

	// First sync: one directory + two files.
	listing1 := `[
		{"Name":"docs","Path":"docs","Size":0,"IsDir":true,"MimeType":"inode/directory","ModTime":"2024-05-01T10:00:00Z"},
		{"Name":"a.pdf","Path":"docs/a.pdf","Size":123,"IsDir":false,"MimeType":"application/pdf","ModTime":"2024-05-01T10:05:00Z","ID":"obj-1"},
		{"Name":"b.txt","Path":"b.txt","Size":45,"IsDir":false,"MimeType":"text/plain","ModTime":"2024-05-02T11:00:00Z","ID":"obj-2"}
	]`
	if err := os.WriteFile(lsjson, []byte(listing1), 0o600); err != nil {
		t.Fatalf("write listing: %v", err)
	}

	report, err := SyncRemoteFiles(context.Background(), remote)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if report.Created != 3 || report.FilesSeen != 2 || report.DirsSeen != 1 || report.Updated != 0 || report.Removed != 0 {
		t.Fatalf("unexpected first report: %+v", report)
	}

	var count int64
	ccdb.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remote.ID).Count(&count)
	if count != 3 {
		t.Fatalf("indexed rows = %d, want 3", count)
	}
	var a models.RcloneFile
	if err := ccdb.DB.Where("remote_id = ? AND path = ?", remote.ID, "docs/a.pdf").First(&a).Error; err != nil {
		t.Fatalf("lookup docs/a.pdf: %v", err)
	}
	if a.Size != 123 || a.ProviderFileID != "obj-1" || a.Name != "a.pdf" || a.IsDir {
		t.Fatalf("unexpected row for docs/a.pdf: %+v", a)
	}
	if a.ModTime == nil || a.ModTime.UTC().Format(time.RFC3339) != "2024-05-01T10:05:00Z" {
		t.Fatalf("unexpected mod time: %+v", a.ModTime)
	}

	// Second sync: a.pdf size changed, b.txt vanished, c.bin appeared.
	listing2 := `[
		{"Name":"docs","Path":"docs","Size":0,"IsDir":true,"MimeType":"inode/directory","ModTime":"2024-05-01T10:00:00Z"},
		{"Name":"a.pdf","Path":"docs/a.pdf","Size":999,"IsDir":false,"MimeType":"application/pdf","ModTime":"2024-05-01T10:05:00Z","ID":"obj-1"},
		{"Name":"c.bin","Path":"c.bin","Size":7,"IsDir":false,"MimeType":"application/octet-stream","ModTime":"2024-05-03T09:00:00Z","ID":"obj-3"}
	]`
	if err := os.WriteFile(lsjson, []byte(listing2), 0o600); err != nil {
		t.Fatalf("rewrite listing: %v", err)
	}

	report2, err := SyncRemoteFiles(context.Background(), remote)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if report2.Created != 1 || report2.Updated != 1 || report2.Removed != 1 {
		t.Fatalf("unexpected second report: %+v", report2)
	}

	var paths []string
	ccdb.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remote.ID).Order("path ASC").Pluck("path", &paths)
	want := []string{"c.bin", "docs", "docs/a.pdf"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths after reconcile = %v, want %v", paths, want)
	}

	// Third sync with an identical listing must be a metadata-only no-op.
	report3, err := SyncRemoteFiles(context.Background(), remote)
	if err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if report3.Created != 0 || report3.Updated != 0 || report3.Removed != 0 {
		t.Fatalf("unchanged listing should not count as updates: %+v", report3)
	}
}

// TestSyncRemoteFilesKeepsIndexOnListingFailure guards the core restart
// resilience property: a provider outage must never wipe the DB index.
func TestSyncRemoteFilesKeepsIndexOnListingFailure(t *testing.T) {
	setupIndexTestDB(t)
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics are POSIX-only")
	}

	remote := &models.RcloneRemote{Name: "s3main", Type: "s3", Enabled: true}
	if err := ccdb.DB.Create(remote).Error; err != nil {
		t.Fatalf("create remote: %v", err)
	}
	seed := models.RcloneFile{
		RemoteID: remote.ID, Path: "keep.txt", PathHash: pathHashOf("keep.txt"),
		Name: "keep.txt", Size: 10, LastSeenAt: time.Now(), SyncedAt: time.Now(),
	}
	if err := ccdb.DB.Create(&seed).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// A fake binary whose lsjson always fails (like an unreachable bucket).
	fake := filepath.Join(t.TempDir(), "rclone")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}
	InvalidateBinaryCache()
	t.Setenv(BinEnvVar, fake)
	t.Cleanup(func() {
		InvalidateBinaryCache()
		_ = os.RemoveAll("./data")
	})

	if _, err := SyncRemoteFiles(context.Background(), remote); err == nil {
		t.Fatal("expected the sync to fail with a failing provider listing")
	}
	var count int64
	ccdb.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remote.ID).Count(&count)
	if count != 1 {
		t.Fatalf("index must survive a failed listing, rows = %d", count)
	}
}

func TestUpsertAndRemoveIndexedFile(t *testing.T) {
	setupIndexTestDB(t)

	remote := &models.RcloneRemote{Name: "mydrv", Type: "drive", RootPrefix: "media", Enabled: true}
	if err := ccdb.DB.Create(remote).Error; err != nil {
		t.Fatalf("create remote: %v", err)
	}

	row := UpsertRemoteFile(remote, "mydrv:media/photos/pic.jpg", &RemoteEntry{
		Name: "pic.jpg", Size: 2048, MimeType: "image/jpeg", ID: "drv-9",
		ModTime: "2024-06-01T12:00:00Z",
	})
	if row == nil {
		t.Fatal("upsert returned nil")
	}
	if row.Path != "photos/pic.jpg" || row.Name != "pic.jpg" || row.Size != 2048 || row.ProviderFileID != "drv-9" {
		t.Fatalf("unexpected row: %+v", row)
	}

	// Re-upserting the same path refreshes the row instead of duplicating it.
	row2 := UpsertRemoteFile(remote, "mydrv:media/photos/pic.jpg", &RemoteEntry{
		Name: "pic.jpg", Size: 4096, MimeType: "image/jpeg", ID: "drv-10",
		ModTime: "2024-06-02T12:00:00Z",
	})
	var count int64
	ccdb.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remote.ID).Count(&count)
	if count != 1 {
		t.Fatalf("upsert must not duplicate rows, count = %d", count)
	}
	if row2 == nil || row2.Size != 4096 || row2.ProviderFileID != "drv-10" {
		t.Fatalf("unexpected refreshed row: %+v", row2)
	}

	// A nil entry (stat unavailable) still derives the name from the path.
	row3 := UpsertRemoteFile(remote, "mydrv:media/notes.txt", nil)
	if row3 == nil || row3.Name != "notes.txt" || row3.Path != "notes.txt" {
		t.Fatalf("unexpected derived row: %+v", row3)
	}

	files, last := RemoteFileStats(remote.ID)
	if files != 2 || last == nil {
		t.Fatalf("stats = (%d, %v), want (2, non-nil)", files, last)
	}

	RemoveIndexedFile(remote, "mydrv:media/photos/pic.jpg")
	var remaining []string
	ccdb.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remote.ID).Pluck("path", &remaining)
	if !reflect.DeepEqual(remaining, []string{"notes.txt"}) {
		t.Fatalf("remaining paths = %v, want [notes.txt]", remaining)
	}
}
