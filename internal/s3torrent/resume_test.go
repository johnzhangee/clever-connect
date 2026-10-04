package s3torrent

import (
	"bytes"
	"errors"
	"testing"

	"clever-connect/internal/models"
	"clever-connect/internal/s3store"
)

// TestResumeFromStoredParts simulates a restart: part 0 (5.5 pieces wide, so
// piece 5 straddles the boundary) is already stored. Pieces fully inside
// stored parts must report complete without any local data; the remainder
// must be downloadable, and completing them finishes the torrent.
func TestResumeFromStoredParts(t *testing.T) {
	newTestDB(t)
	const partSize = 1408 * 1024 // 5.5 pieces per part → piece 5 straddles parts 0/1
	const total = 3 * partSize
	data := testPattern(total)

	fake := newFakeS3()
	// Keys must match what the store computes from the live storage config
	// (no StorageConfig row in the test DB → the default prefix applies).
	prefix := loadS3Prefix()
	partsPrefix := s3store.KeyForTorrentPartsPrefix(prefix, testHash)
	objectKey := s3store.KeyForTorrentData(prefix, testHash)
	// Persisted state from the previous session: part 0 staged, nothing else.
	fake.objects[partsPrefix+pad10(0)] = append([]byte(nil), data[:partSize]...)

	row := models.TorrentS3Upload{
		InfoHash:      testHash,
		ObjectKey:     objectKey,
		PartsPrefix:   partsPrefix,
		TotalLength:   total,
		PieceLength:   testPieceSize,
		PartSize:      partSize,
		UploadedParts: "[0]",
	}
	if err := dbCreateRow(&row); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	ts, err := openTorrentStoreWith(nil, buildTestInfo(total, testPieceSize), testHash, fake, partSize)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ts.Close()

	// Pieces 0..4 live fully inside the stored part 0: pre-completed.
	for i := 0; i <= 4; i++ {
		if c := ts.pieces[i].Completion(); !c.Ok || !c.Complete {
			t.Fatalf("piece %d (inside stored part 0) must be pre-completed, got %+v", i, c)
		}
	}
	// Piece 5 straddles stored part 0 and missing part 1: not complete.
	if c := ts.pieces[5].Completion(); c.Complete {
		t.Fatal("piece 5 straddles a missing part and must not be pre-completed")
	}
	if got := len(ts.completedPieces); got != 5 {
		t.Fatalf("pre-completed pieces = %d; want 5", got)
	}

	// Simulate the client re-downloading pieces 5..16 into the missing parts.
	for i, piece := range ts.pieces {
		if i <= 4 {
			continue // already stored
		}
		for off := piece.start; off < piece.end; off += 65536 {
			chunkEnd := off + 65536
			if chunkEnd > piece.end {
				chunkEnd = piece.end
			}
			n, err := piece.WriteAt(data[off:chunkEnd], off-piece.start)
			if err != nil || n != int(chunkEnd-off) {
				t.Fatalf("piece %d WriteAt: n=%d err=%v", i, n, err)
			}
		}
		if err := piece.MarkComplete(); err != nil {
			t.Fatalf("piece %d MarkComplete: %v", i, err)
		}
	}
	waitFor(t, ts.isFinalized, "resumed torrent must finalize once all pieces are verified")
	if got := fake.objects[objectKey]; !bytes.Equal(got, data) {
		t.Fatalf("final object size = %d; want %d", len(got), total)
	}
	var updated models.TorrentS3Upload
	if err := dbGetRow(testHash, &updated); err != nil {
		t.Fatalf("row: %v", err)
	}
	if !updated.Finalized {
		t.Error("row must be finalized after commit")
	}
}

// TestWriteAtRAMValve verifies the non-blocking backpressure: with no RAM
// budget left, WriteAt refuses instead of blocking.
func TestWriteAtRAMValve(t *testing.T) {
	newTestDB(t)
	const total = int64(testPieceSize)

	oldBudget := partBufBudgetBytes
	partBufBudgetBytes = 1
	t.Cleanup(func() { partBufBudgetBytes = oldBudget })

	fake := newFakeS3()
	ts, err := openTorrentStoreWith(nil, buildTestInfo(total, testPieceSize), testHash, fake, testPartSize)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ts.Close()

	piece := ts.pieces[0]
	chunk := make([]byte, 65536)
	if _, err := piece.WriteAt(chunk, 0); !errors.Is(err, errRAMValve) {
		t.Fatalf("WriteAt with exhausted budget: err=%v; want errRAMValve", err)
	}

	// Once budget is available again, the same write succeeds.
	partBufBudgetBytes = oldBudget
	if n, err := piece.WriteAt(chunk, 0); err != nil || n != len(chunk) {
		t.Fatalf("WriteAt after budget release: n=%d err=%v", n, err)
	}
}
