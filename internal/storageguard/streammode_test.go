package storageguard

import (
	"reflect"
	"testing"

	"clever-connect/internal/models"
)

func TestPlanStreamBatchesPacking(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	sizes := []int64{3 * gb, 3 * gb, 3 * gb, 2 * gb} // batches: [0,1], [2,3]
	batches := planStreamBatches(sizes, 8*gb)
	want := [][]int{{0, 1}, {2, 3}}
	if !reflect.DeepEqual(batches, want) {
		t.Errorf("batches = %v; want %v", batches, want)
	}
}

func TestPlanStreamBatchesOversizeFile(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	sizes := []int64{2 * gb, 20 * gb, 2 * gb}
	batches := planStreamBatches(sizes, 8*gb)
	// the middle 20GB file cannot share a batch: it takes one of its own
	want := [][]int{{0}, {1}, {2}}
	if !reflect.DeepEqual(batches, want) {
		t.Errorf("batches = %v; want %v", batches, want)
	}
}

func TestPlanStreamBatchesZeroSizesAndDefaults(t *testing.T) {
	// zero-size files ride along with the current batch
	batches := planStreamBatches([]int64{4, 0, 4}, 8)
	want := [][]int{{0, 1, 2}}
	if !reflect.DeepEqual(batches, want) {
		t.Errorf("batches = %v; want %v", batches, want)
	}
	// non-positive max means "use 8 GiB"; only count check needed here
	if got := planStreamBatches([]int64{1, 2, 3}, -5); len(got) != 1 {
		t.Errorf("expected single batch for tiny files, got %v", got)
	}
	// empty torrent
	if got := planStreamBatches(nil, 8); got != nil {
		t.Errorf("expected nil batches, got %v", got)
	}
}

func TestBatchIndexMapAndSettled(t *testing.T) {
	batches := [][]int{{0, 1}, {2}}
	m := batchIndexMap(batches)
	if m[0] != 0 || m[1] != 0 || m[2] != 1 || len(m) != 3 {
		t.Fatalf("batchIndexMap = %v", m)
	}

	rows := map[int]*models.TorrentFileOffload{
		0: {Uploaded: true, EvictedLocal: true},
		1: {Uploaded: true, EvictedLocal: false},
	}
	if batchSettled(batches[0], rows, true) {
		t.Error("batch must not be settled while eviction is pending")
	}
	if !batchSettled(batches[0], rows, false) {
		t.Error("batch counts as settled when eviction is disabled")
	}
	rows[1].EvictedLocal = true
	if !batchSettled(batches[0], rows, true) {
		t.Error("fully uploaded+evicted batch must be settled")
	}
	if batchSettled(batches[1], rows, true) {
		t.Error("batch with a missing row must not be settled")
	}
}

func TestRestoreProtect(t *testing.T) {
	for _, status := range []string{"restoring", "restored"} {
		if !restoreProtect(&models.TorrentJob{RestoreStatus: status}) {
			t.Errorf("status %q must protect", status)
		}
	}
	for _, status := range []string{"", "failed"} {
		if restoreProtect(&models.TorrentJob{RestoreStatus: status}) {
			t.Errorf("status %q must not protect", status)
		}
	}
}

func TestClampFloat(t *testing.T) {
	if clampFloat(-1, 0, 100) != 0 || clampFloat(101, 0, 100) != 100 || clampFloat(50, 0, 100) != 50 {
		t.Error("clampFloat bounds broken")
	}
}
