package s3torrent

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/models"

	sqlite "clever-connect/internal/db/sqlite"

	"github.com/anacrolix/torrent/metainfo"
	"gorm.io/gorm"
)

func (f *fakeS3) CopyObjectPart(_ context.Context, srcObject, dstObject, _ string, partNumber int, length int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src, ok := f.objects[srcObject]
	if !ok {
		return "", fmt.Errorf("NoSuchKey: %s", srcObject)
	}
	if length > int64(len(src)) {
		return "", fmt.Errorf("copy source shorter than requested length")
	}
	cp := fakeCopiedPart{number: partNumber, source: srcObject, length: length,
		etag: fmt.Sprintf("etag-%s-%d", srcObject, partNumber)}
	f.copied[dstObject] = append(f.copied[dstObject], cp)
	return cp.etag, nil
}

func (f *fakeS3) CompleteMultipartUpload(_ context.Context, object, _ string, parts []CompletePart) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	byNumber := map[int]fakeCopiedPart{}
	for _, cp := range f.copied[object] {
		byNumber[cp.number] = cp
	}
	var buf bytes.Buffer
	for i, p := range parts {
		if p.PartNumber != i+1 {
			return fmt.Errorf("part numbers must be sequential: got %d at index %d", p.PartNumber, i)
		}
		cp, ok := byNumber[p.PartNumber]
		if !ok {
			return fmt.Errorf("part %d was never copied", p.PartNumber)
		}
		buf.Write(f.objects[cp.source][:cp.length])
	}
	f.objects[object] = buf.Bytes()
	f.completed[object] = append([]fakeCopiedPart(nil), f.copied[object]...)
	return nil
}

func (f *fakeS3) AbortMultipartUpload(_ context.Context, object, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = append(f.aborted, object)
	return nil
}

// pad10 reproduces the zero-padded part-object index in key names.
func pad10(i int) string { return fmt.Sprintf("%010d", i) }

// dbCreateRow inserts a persistence row directly.
func dbCreateRow(row *models.TorrentS3Upload) error {
	return db.DB.Create(row).Error
}

// dbGetRow loads the persistence row of one torrent.
func dbGetRow(hash string, row *models.TorrentS3Upload) error {
	return db.DB.Where("info_hash = ?", hash).First(row).Error
}

// newTestDB swaps in a fresh in-memory database with the backend's tables.
func newTestDB(t *testing.T) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	if err := gdb.AutoMigrate(&models.TorrentS3Upload{}, &models.StorageConfig{}); err != nil {
		t.Fatalf("failed to migrate schema: %v", err)
	}
	if err := gdb.Where("1 = 1").Delete(&models.TorrentS3Upload{}).Error; err != nil {
		t.Fatalf("failed to clear upload rows: %v", err)
	}
	db.DB = gdb
}

// buildTestInfo builds a single-file metainfo.Info of the given dimensions.
func buildTestInfo(total, pieceLength int64) *metainfo.Info {
	numPieces := int((total + pieceLength - 1) / pieceLength)
	return &metainfo.Info{
		PieceLength: pieceLength,
		Length:      total,
		Name:        "test",
		Pieces:      make([]byte, 20*numPieces),
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

// testPattern fills a byte slice with a deterministic pattern.
func testPattern(total int64) []byte {
	data := make([]byte, total)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

// writeAndComplete simulates the torrent client: block-wise WriteAt calls
// followed by MarkComplete for every piece.
func writeAndComplete(t *testing.T, ts *torrentStore, data []byte) {
	t.Helper()
	for i, piece := range ts.pieces {
		start := piece.start
		end := piece.end
		for off := start; off < end; off += 65536 {
			chunkEnd := off + 65536
			if chunkEnd > end {
				chunkEnd = end
			}
			n, err := piece.WriteAt(data[off:chunkEnd], off-start)
			if err != nil || n != int(chunkEnd-off) {
				t.Fatalf("piece %d WriteAt: n=%d err=%v", i, n, err)
			}
		}
		if err := piece.MarkComplete(); err != nil {
			t.Fatalf("piece %d MarkComplete: %v", i, err)
		}
	}
}
