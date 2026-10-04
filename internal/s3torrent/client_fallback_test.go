package s3torrent

import (
	"context"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// recordingFallback is a storage.ClientImpl stand-in for the on-disk storage:
// it records every OpenTorrent call it receives.
type recordingFallback struct {
	opens []metainfo.Hash
}

func (f *recordingFallback) OpenTorrent(_ context.Context, _ *metainfo.Info, ih metainfo.Hash) (storage.TorrentImpl, error) {
	f.opens = append(f.opens, ih)
	return storage.TorrentImpl{}, nil
}

// TestRoutedOpenFailureDoesNotFallBackToDisk verifies the disk-safety rule of
// the dispatcher: a hash routed to the direct-to-S3 backend must never be
// handed to the wrapped local storage when its S3 open fails (tests run with
// no S3 configured, so the open always fails). Silent disk fallback would
// refill the small instance disk — the exact failure direct mode prevents.
func TestRoutedOpenFailureDoesNotFallBackToDisk(t *testing.T) {
	fb := &recordingFallback{}
	c := NewClient(fb)
	c.Route(testHash)

	hash := metainfo.NewHashFromHex(testHash)
	if _, err := c.OpenTorrent(context.Background(), buildTestInfo(1024, testPieceSize), hash); err == nil {
		t.Fatal("routed open must fail while S3 is unconfigured")
	}
	if len(fb.opens) != 0 {
		t.Fatalf("routed hash must not touch the disk fallback, opens=%v", fb.opens)
	}
}

// TestUnroutedFallsBackToLocal verifies that hashes never routed to S3 keep
// using the wrapped file storage (stock behaviour for legacy and non-S3
// setups).
func TestUnroutedFallsBackToLocal(t *testing.T) {
	fb := &recordingFallback{}
	c := NewClient(fb)

	const other = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hash := metainfo.NewHashFromHex(other)
	if _, err := c.OpenTorrent(context.Background(), buildTestInfo(1024, testPieceSize), hash); err != nil {
		t.Fatalf("unrouted open: %v", err)
	}
	if len(fb.opens) != 1 || fb.opens[0].HexString() != other {
		t.Fatalf("fallback must serve the unrouted hash exactly once, opens=%v", fb.opens)
	}
}
