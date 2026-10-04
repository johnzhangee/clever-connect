package torrent

import (
	"testing"

	"clever-connect/internal/db"
	"clever-connect/internal/models"
	"clever-connect/internal/s3torrent"

	sqlite "clever-connect/internal/db/sqlite"

	"gorm.io/gorm"
)

// TestNeedsAdmissionBypassesDirectS3 verifies that direct-to-S3 torrents
// never enter the disk admission queue: their outstanding bytes are written
// to object storage, not to the local disk.
func TestNeedsAdmissionBypassesDirectS3(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	if err := gdb.AutoMigrate(&models.TorrentJob{}, &models.StorageConfig{}); err != nil {
		t.Fatalf("failed to migrate schema: %v", err)
	}
	if err := gdb.Where("1 = 1").Delete(&models.TorrentJob{}).Error; err != nil {
		t.Fatalf("failed to clear jobs: %v", err)
	}
	db.DB = gdb

	m := &TorrentManager{directS3Client: s3torrent.NewClient(nil)}
	if m.isDirectS3("s3torrent-test") {
		t.Fatal("hash must not be routed before Route")
	}
	m.routeDirectS3("s3torrent-test")
	if !m.isDirectS3("s3torrent-test") {
		t.Fatal("routed hash must report isDirectS3")
	}

	// Even with a large amount "needed", a direct-S3 torrent bypasses the
	// disk math entirely. The bypass runs before any torrent-client or
	// config access, so a nil torrent client is fine here — the same
	// isDirectS3 primitive also guards the inflight-sum path.
	if m.needsAdmission("s3torrent-test", t.TempDir(), 1<<30, 10<<30) {
		t.Error("direct-S3 torrent must bypass admission")
	}
}
