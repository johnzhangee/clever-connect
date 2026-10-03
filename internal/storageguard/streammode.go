package storageguard

import (
	"encoding/json"

	"clever-connect/internal/db"
	"clever-connect/internal/models"

	"github.com/anacrolix/torrent"
)

const giga = int64(1024 * 1024 * 1024)

// planStreamBatches packs ordered file indices into batches that each hold
// at most maxBatchBytes of payload. A single oversized file gets its own
// batch; zero-size files ride with the current batch.
func planStreamBatches(sizes []int64, maxBatchBytes int64) [][]int {
	if maxBatchBytes <= 0 {
		maxBatchBytes = 8 * giga
	}
	var batches [][]int
	cur := make([]int, 0, 16)
	var curBytes int64
	for i, s := range sizes {
		if s <= 0 {
			cur = append(cur, i)
			continue
		}
		if curBytes > 0 && curBytes+s > maxBatchBytes {
			batches = append(batches, cur)
			cur = make([]int, 0, 16)
			curBytes = 0
		}
		cur = append(cur, i)
		curBytes += s
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// batchIndexMap maps file index -> batch number.
func batchIndexMap(batches [][]int) map[int]int {
	m := make(map[int]int, 64)
	for bi, b := range batches {
		for _, idx := range b {
			m[idx] = bi
		}
	}
	return m
}

// batchSettled reports whether every file of a batch is secured in S3 (and
// evicted, or eviction disabled).
func batchSettled(b []int, rows map[int]*models.TorrentFileOffload, evictOn bool) bool {
	for _, idx := range b {
		row := rows[idx]
		if row == nil || !row.Uploaded {
			return false
		}
		if evictOn && !row.EvictedLocal {
			return false
		}
	}
	return true
}

// selectedFileSet decodes the SelectedFiles JSON with the same semantics as
// the torrent manager's priority funnel: "" / "null" / unparsable means every
// file is wanted; "[]" means none; otherwise only the listed indices are.
func selectedFileSet(selectedJSON string) (all bool, set map[int]bool) {
	if selectedJSON == "" || selectedJSON == "null" {
		return true, nil
	}
	var idx []int
	if err := json.Unmarshal([]byte(selectedJSON), &idx); err != nil {
		return true, nil
	}
	if len(idx) == 0 {
		return false, nil
	}
	s := make(map[int]bool, len(idx))
	for _, i := range idx {
		s[i] = true
	}
	return false, s
}

// applyStreamMode bounds the disk footprint of giant torrents: only the
// earliest batch that still needs work is queued for download while every
// later batch stays cancelled until its predecessors are secured in S3.
func (g *Guard) applyStreamMode(cfg models.StorageConfig, job *models.TorrentJob, t *torrent.Torrent, files []*torrent.File, rows map[int]*models.TorrentFileOffload) {
	threshold := int64(cfg.StreamThresholdGB) * giga
	if !cfg.S3Enabled || threshold <= 0 || t.Length() <= threshold {
		return
	}
	if !job.StreamMode {
		job.StreamMode = true
		db.DB.Model(job).Update("stream_mode", true)
		logEvent("info", "stream", "Stream Mode engaged for "+job.Name)
	}

	sizes := make([]int64, len(files))
	for i, f := range files {
		sizes[i] = f.Length()
	}
	batches := planStreamBatches(sizes, int64(cfg.BatchSizeGB)*giga)
	if len(batches) == 0 {
		return
	}
	where := batchIndexMap(batches)

	current := len(batches) - 1
	for bi, b := range batches {
		if !batchSettled(b, rows, cfg.EvictAfterUpload) {
			current = bi
			break
		}
	}

	// A user-paused torrent, or one held in the torrent manager's disk
	// admission queue, keeps every file cancelled until it actually runs.
	paused := (job.Status == "paused" && !job.PausedByGuard) || job.Status == "queued"
	selAll, selSet := selectedFileSet(job.SelectedFiles)
	for i, f := range files {
		want := selAll || selSet[i]
		switch {
		case where[i] < current:
			f.Cancel() // earlier batch still settling: wait your turn
		case where[i] == current && !paused && want:
			f.Download()
		default:
			f.Cancel()
		}
	}
}
