package torrent

import (
	"os"
	"path/filepath"
	"testing"

	"clever-connect/internal/db"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"

	sqlite "clever-connect/internal/db/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	if err := gdb.AutoMigrate(&models.FileRegistry{}, &models.TorrentFileOffload{}); err != nil {
		t.Fatalf("failed to migrate schema: %v", err)
	}
	// The shared in-memory DB persists across tests in this package — start clean.
	if err := gdb.Where("1 = 1").Delete(&models.FileRegistry{}).Error; err != nil {
		t.Fatalf("failed to clear file registry: %v", err)
	}
	if err := gdb.Where("1 = 1").Delete(&models.TorrentFileOffload{}).Error; err != nil {
		t.Fatalf("failed to clear offload ledger: %v", err)
	}
	db.DB = gdb
}

func mustCreate(t *testing.T, row interface{}) {
	t.Helper()
	if err := db.DB.Create(row).Error; err != nil {
		t.Fatalf("failed to create %T row: %v", row, err)
	}
}

func TestFindLedgerRow(t *testing.T) {
	setupTestDB(t)

	mustCreate(t, &models.TorrentFileOffload{InfoHash: "h1", FileIndex: 0, RelPath: "a.mp4", FilePath: "/d/a.mp4", Size: 10, S3Key: "key-a", Uploaded: true})
	mustCreate(t, &models.TorrentFileOffload{InfoHash: "h1", FileIndex: 1, RelPath: "b.mkv", FilePath: "/d/renamed.mkv", Size: 20, S3Key: "key-b", Uploaded: true})
	// Uploaded but no S3 key — unusable, must be skipped.
	mustCreate(t, &models.TorrentFileOffload{InfoHash: "h1", FileIndex: 2, RelPath: "c.avi", FilePath: "/d/c.avi", Size: 30, S3Key: "", Uploaded: true})
	// Not uploaded — must never match.
	mustCreate(t, &models.TorrentFileOffload{InfoHash: "h1", FileIndex: 3, RelPath: "d.flv", FilePath: "/d/d.flv", Size: 40, S3Key: "key-d", Uploaded: false})
	// Another torrent sharing the rel path.
	mustCreate(t, &models.TorrentFileOffload{InfoHash: "h2", FileIndex: 0, RelPath: "a.mp4", FilePath: "/d/a.mp4", Size: 10, S3Key: "key-x", Uploaded: true})

	// Match by relative path inside the torrent.
	if row, ok := findLedgerRow("/d/a.mp4", "a.mp4", "h1", 10); !ok || row.S3Key != "key-a" {
		t.Errorf("rel-path match: ok=%v key=%q; want key-a", ok, row.S3Key)
	}
	// Match by absolute local path when the rel path drifted.
	if row, ok := findLedgerRow("/d/renamed.mkv", "old.mkv", "h1", 20); !ok || row.S3Key != "key-b" {
		t.Errorf("abs-path match: ok=%v key=%q; want key-b", ok, row.S3Key)
	}
	// Size drift — the archived content differs from this torrent's file.
	if _, ok := findLedgerRow("/d/a.mp4", "a.mp4", "h1", 11); ok {
		t.Error("size-drifted row must not match")
	}
	// Empty S3 key rows are skipped.
	if _, ok := findLedgerRow("/d/c.avi", "c.avi", "h1", 30); ok {
		t.Error("row without S3 key must not match")
	}
	// Not-uploaded rows are filtered out by the query.
	if _, ok := findLedgerRow("/d/d.flv", "d.flv", "h1", 40); ok {
		t.Error("not-uploaded row must not match")
	}
	// A different torrent's ledger is not consulted.
	if _, ok := findLedgerRow("/d/a.mp4", "a.mp4", "h9", 10); ok {
		t.Error("foreign info hash must not match")
	}
}

func TestFindLocalRegisteredCopy(t *testing.T) {
	setupTestDB(t)

	tmp := t.TempDir()
	src := filepath.Join(tmp, "movie.mp4")
	if err := os.WriteFile(src, make([]byte, 100), 0o644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	mustCreate(t, &models.FileRegistry{Checksum: "c1", FilePath: src, FileSize: 100})
	// Registered but the file is gone from disk.
	mustCreate(t, &models.FileRegistry{Checksum: "c2", FilePath: filepath.Join(tmp, "vanished.mp4"), FileSize: 100})
	// Registered with a different size — filtered by the query.
	mustCreate(t, &models.FileRegistry{Checksum: "c3", FilePath: filepath.Join(tmp, "sized.mp4"), FileSize: 50})

	if got, ok := findLocalRegisteredCopy("torrent/dir/movie.mp4", 100); !ok || got != src {
		t.Errorf("findLocalRegisteredCopy = (%q, %v); want (%q, true)", got, ok, src)
	}
	if _, ok := findLocalRegisteredCopy("torrent/dir/movie.mp4", 99); ok {
		t.Error("length mismatch must not match")
	}
	if _, ok := findLocalRegisteredCopy("torrent/dir/different.avi", 100); ok {
		t.Error("basename mismatch must not match")
	}
}

func TestFindArchivedRegistryTiers(t *testing.T) {
	setupTestDB(t)

	mustCreate(t, &models.FileRegistry{Checksum: "r1", FilePath: "/d/exact.mp4", S3Key: "exact-key", FileSize: 10})
	mustCreate(t, &models.FileRegistry{Checksum: "r2", FilePath: "/d/byhash.mp4", S3Key: "hash-key", TorrentHash: "th", FileSize: 20})
	mustCreate(t, &models.FileRegistry{Checksum: "r3", FilePath: "/elsewhere/base.mp4", S3Key: "name-key", FileSize: 30})

	if reg, tier, ok := findArchivedRegistry("/d/exact.mp4", ""); !ok || tier != "exact path" || reg.S3Key != "exact-key" {
		t.Errorf("exact tier: ok=%v tier=%q key=%q", ok, tier, reg.S3Key)
	}
	if reg, tier, ok := findArchivedRegistry("/d/nothing.mp4", "th"); !ok || tier != "torrent_hash" || reg.S3Key != "hash-key" {
		t.Errorf("hash tier: ok=%v tier=%q key=%q", ok, tier, reg.S3Key)
	}
	if reg, tier, ok := findArchivedRegistry("/d/base.mp4", ""); !ok || tier != "filename match" || reg.S3Key != "name-key" {
		t.Errorf("basename tier: ok=%v tier=%q key=%q", ok, tier, reg.S3Key)
	}
	if _, _, ok := findArchivedRegistry("/d/absent.mp4", "other"); ok {
		t.Error("no tier should match an unknown file")
	}
}

func TestS3ChecksUnconfigured(t *testing.T) {
	if s3store.Enabled() {
		t.Skip("S3 store configured in this environment — skipping unconfigured-store test")
	}

	if s3ObjectHealthy("some/key", 10) {
		t.Error("unconfigured store must never report a healthy object")
	}

	setupTestDB(t)
	mustCreate(t, &models.TorrentFileOffload{InfoHash: "h1", RelPath: "a.mp4", FilePath: "/d/a.mp4", Size: 10, S3Key: "key-a", Uploaded: true})
	if _, ok := findS3CopyForFile("/d/a.mp4", "a.mp4", "h1", 10); ok {
		t.Error("tier-3 lookup must be a no-op while S3 storage is disabled")
	}
}
