package torrent

import (
	"testing"

	"clever-connect/internal/db"
	"clever-connect/internal/models"

	sqlite "clever-connect/internal/db/sqlite"

	"gorm.io/gorm"
)

func TestAdmissionReserveBytes(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	cfg := models.StorageConfig{AdmissionReservePercent: 10, AdmissionReserveMinGB: 5}

	// 100 GB disk: 10% of it dominates the 5 GB floor.
	if r := admissionReserveBytes(100*uint64(gb), cfg); r != 10*gb {
		t.Errorf("reserve = %d; want %d", r, 10*gb)
	}
	// 40 GB disk: 10% is only 4 GB, the absolute floor of 5 GB wins.
	if r := admissionReserveBytes(40*uint64(gb), cfg); r != 5*gb {
		t.Errorf("reserve = %d; want %d", r, 5*gb)
	}
	// Degenerate config: both knobs off ⇒ zero reserve.
	if r := admissionReserveBytes(100*uint64(gb), models.StorageConfig{}); r != 0 {
		t.Errorf("reserve = %d; want 0", r)
	}
}

func TestAdmitDownload(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	cases := []struct {
		name             string
		needed, inflight int64
		free, reserve    int64
		want             bool
	}{
		{"comfortable fit", 10 * gb, 0, 40 * gb, 5 * gb, true},
		{"exact boundary", 10 * gb, 5 * gb, 20 * gb, 5 * gb, true},
		{"one byte short", 10*gb + 1, 5 * gb, 20 * gb, 5 * gb, false},
		{"no room at all", 30 * gb, 0, 10 * gb, 5 * gb, false},
		{"nothing needed with room", 0, 0, 10 * gb, 5 * gb, true},
	}
	for _, tc := range cases {
		if got := admitDownload(tc.needed, tc.inflight, tc.free, tc.reserve); got != tc.want {
			t.Errorf("%s: admitDownload(%d, %d, %d, %d) = %v; want %v",
				tc.name, tc.needed, tc.inflight, tc.free, tc.reserve, got, tc.want)
		}
	}
}

// Write-error parking: the chunk-write-error hook parks a torrent only when
// the disk cannot take its remaining bytes. Nothing is parked on a guess — an
// unmeasurable disk leaves retrying to the library.
func TestWriteErrorShouldPark(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	cfg := models.StorageConfig{AdmissionReservePercent: 10, AdmissionReserveMinGB: 5}

	cases := []struct {
		name            string
		remaining, free int64
		total           uint64
		diskOK          bool
		want            bool
	}{
		{"disk unmeasurable never parks", 10 * gb, 0, 100 * uint64(gb), false, false},
		{"unknown disk size never parks", 10 * gb, 0, 0, true, false},
		{"remaining fits comfortably", 10 * gb, 40 * gb, 100 * uint64(gb), true, false},
		{"remaining exactly reaches reserve", 10 * gb, 20 * gb, 100 * uint64(gb), true, false},
		{"remaining one byte over reserve", 10*gb + 1, 20 * gb, 100 * uint64(gb), true, true},
		{"no room at all", 10 * gb, 1 * gb, 100 * uint64(gb), true, true},
	}
	for _, tc := range cases {
		if got := writeErrorShouldPark(tc.remaining, tc.free, tc.total, tc.diskOK, cfg); got != tc.want {
			t.Errorf("%s: writeErrorShouldPark(%d, %d, %d, %v) = %v; want %v",
				tc.name, tc.remaining, tc.free, tc.total, tc.diskOK, got, tc.want)
		}
	}
}

func TestStreamModeGoverns(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	s3On := models.StorageConfig{S3Enabled: true, StreamThresholdGB: 12}
	s3Off := models.StorageConfig{S3Enabled: false, StreamThresholdGB: 12}
	noThreshold := models.StorageConfig{S3Enabled: true, StreamThresholdGB: 0}

	if !streamModeGoverns(12*gb+1, s3On) {
		t.Error("torrent above the stream threshold with S3 on must be governed by stream mode")
	}
	if streamModeGoverns(12*gb, s3On) {
		t.Error("torrent exactly at the stream threshold is not stream mode (strictly greater)")
	}
	if streamModeGoverns(100*gb, s3Off) {
		t.Error("without S3 there is no stream mode — admission must apply")
	}
	if streamModeGoverns(100*gb, noThreshold) {
		t.Error("threshold 0 disables stream mode")
	}
}

func TestDiskQueueOps(t *testing.T) {
	m := &TorrentManager{}

	if m.queueLength() != 0 || m.queueContains("a") {
		t.Fatal("fresh manager must have an empty queue")
	}
	if !m.enqueueDiskQueue("a") {
		t.Error("first enqueue must report newly added")
	}
	if m.enqueueDiskQueue("a") {
		t.Error("duplicate enqueue must not report newly added")
	}
	if !m.enqueueDiskQueue("b") {
		t.Error("second distinct enqueue must report newly added")
	}
	if m.queueLength() != 2 {
		t.Errorf("queueLength = %d; want 2", m.queueLength())
	}
	if m.diskQueue[0].infoHash != "a" || m.diskQueue[1].infoHash != "b" {
		t.Errorf("queue order = [%s %s]; want FIFO [a b]", m.diskQueue[0].infoHash, m.diskQueue[1].infoHash)
	}

	m.dequeueDiskQueue("a")
	if m.queueLength() != 1 || m.queueContains("a") {
		t.Error("dequeue must remove the entry")
	}
	if m.diskQueue[0].infoHash != "b" {
		t.Errorf("head after dequeue = %q; want b", m.diskQueue[0].infoHash)
	}

	m.dequeueDiskQueue("missing") // no-op
	if m.queueLength() != 1 {
		t.Errorf("dequeuing an absent hash must be a no-op; length = %d", m.queueLength())
	}
	m.dequeueDiskQueue("b")
	if m.queueLength() != 0 {
		t.Errorf("queue must be empty; length = %d", m.queueLength())
	}
}

func TestNeedsAdmissionCheapGates(t *testing.T) {
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
	if err := gdb.Where("1 = 1").Delete(&models.StorageConfig{}).Error; err != nil {
		t.Fatalf("failed to clear storage config: %v", err)
	}
	db.DB = gdb

	m := &TorrentManager{}

	// Nothing to download ⇒ never needs admission.
	if m.needsAdmission("nope", t.TempDir(), 1<<30, 0) {
		t.Error("needed = 0 must not require admission")
	}
	// No job row (ephemeral torrent) ⇒ cannot queue ⇒ bypass admission.
	if m.needsAdmission("nope", t.TempDir(), 1<<30, 10<<30) {
		t.Error("torrent without a job row must bypass admission")
	}
	// A paused job row must also bypass (resume re-applies instead).
	if err := gdb.Create(&models.TorrentJob{InfoHash: "paused", Name: "p", Status: "paused", SaveDirectory: t.TempDir()}).Error; err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	if m.needsAdmission("paused", t.TempDir(), 1<<30, 10<<30) {
		t.Error("user-paused job must bypass admission (handled by resume)")
	}
}
