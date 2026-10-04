package s3torrent

import (
	"bytes"
	"testing"

	"clever-connect/internal/models"
)

// TestAggregationAndCommit runs the full pipeline: 256 KiB pieces aggregated
// into 1 MiB parts, staged as part objects, then server-side assembled into
// the final data object.
func TestAggregationAndCommit(t *testing.T) {
	newTestDB(t)
	const partSize = int64(testPartSize)
	const total = 2*partSize + 512*1024 // three parts: 1M, 1M, 512K
	data := testPattern(total)

	fake := newFakeS3()
	ts, err := openTorrentStoreWith(nil, buildTestInfo(total, testPieceSize), testHash, fake, partSize)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ts.Close()

	if got := len(ts.parts); got != 3 {
		t.Fatalf("parts = %d; want 3", got)
	}
	if ts.single {
		t.Fatal("total > partSize must not be single-object mode")
	}

	writeAndComplete(t, ts, data)
	waitFor(t, ts.isFinalized, "torrent must finalize after all pieces complete")

	// The final object holds exactly the aggregated bytes.
	if got := fake.objects[ts.object]; !bytes.Equal(got, data) {
		t.Fatalf("final object size = %d; want %d (content mismatch)", len(got), total)
	}
	// Three parts were copied server-side, in order, from the staged objects.
	copies := fake.completed[ts.object]
	if len(copies) != 3 {
		t.Fatalf("copied parts = %d; want 3", len(copies))
	}
	lengths := []int64{partSize, partSize, total - 2*partSize}
	for i, cp := range copies {
		if cp.number != i+1 {
			t.Errorf("part %d number = %d; want %d", i, cp.number, i+1)
		}
		if cp.length != lengths[i] {
			t.Errorf("part %d length = %d; want %d", i, cp.length, lengths[i])
		}
		if want := ts.partsPrefix + pad10(i); cp.source != want {
			t.Errorf("part %d source = %q; want %q", i, cp.source, want)
		}
	}
	// Staged part objects were reclaimed after the assembly.
	for key := range fake.objects {
		if len(key) > len(ts.partsPrefix) && key[:len(ts.partsPrefix)] == ts.partsPrefix {
			t.Errorf("staged part object %q survived finalization", key)
		}
	}
	// The persistence row records the commit.
	var row models.TorrentS3Upload
	if err := dbGetRow(testHash, &row); err != nil {
		t.Fatalf("row: %v", err)
	}
	if !row.Finalized || row.UploadID != "" {
		t.Errorf("row finalized=%v upload_id=%q; want true/empty", row.Finalized, row.UploadID)
	}
	// Post-commit reads come from the final object.
	buf := make([]byte, total)
	if _, err := ts.readAt(buf, 0); err != nil {
		t.Fatalf("post-commit readAt: %v", err)
	}
	if !bytes.Equal(buf, data) {
		t.Fatal("post-commit readAt returned wrong bytes")
	}
}

// TestSingleObjectMode covers totals that fit in one part: the whole torrent
// is a single PUT to the final object key, no multipart at all.
func TestSingleObjectMode(t *testing.T) {
	newTestDB(t)
	const total = int64(testPieceSize) // one piece
	data := testPattern(total)

	fake := newFakeS3()
	ts, err := openTorrentStoreWith(nil, buildTestInfo(total, testPieceSize), testHash, fake, testPartSize)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ts.Close()

	if !ts.single {
		t.Fatal("total <= partSize must be single-object mode")
	}
	writeAndComplete(t, ts, data)
	waitFor(t, ts.isFinalized, "single-object torrent must finalize")

	if got := fake.objects[ts.object]; !bytes.Equal(got, data) {
		t.Fatalf("final object size = %d; want %d", len(got), total)
	}
	if len(fake.uploads) != 0 {
		t.Errorf("multipart uploads started = %d; want 0", len(fake.uploads))
	}
}
