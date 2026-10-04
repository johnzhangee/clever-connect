package torrent

// admission.go — disk-space admission control for torrent downloads.
//
// The Clever Cloud instance has a small disk (~49 GB). When several big
// torrents are added at once they all start downloading simultaneously and
// the disk overflows before any of them completes — the "DiskFull" monitor
// then restarts the whole application.
//
// The mechanism here is proactive instead of reactive (the storage guard's
// pause watermark stays as the last-resort backstop):
//
//   - ApplyFilePriorities, after running the pre-download existence check,
//     knows exactly how many bytes the swarm still has to deliver (verified
//     local copies and S3-confirmed files are excluded from the budget).
//   - needsAdmission compares those bytes against the free space minus what
//     every other live download still intends to write, minus a configurable
//     reserve (percentage of the disk, with an absolute GB floor).
//   - If the torrent does not fit, every file is cancelled, the job moves to
//     the "queued" status and a FIFO entry records it.
//   - The stats loop drains the queue: whenever space frees up (completed
//     files offloaded to S3 and evicted, other torrents finishing, or the
//     user freeing disk), the longest-waiting torrent is pushed through the
//     funnel again and flips back to "downloading".
//
// Torrents governed by the storage guard's Stream Mode (bigger than
// StreamThresholdGB) bypass admission — their footprint is already bounded
// in sequential batches.

import (
	"path/filepath"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/storageguard"

	"github.com/anacrolix/torrent"
)

// diskQueueEntry is one torrent held back until the disk fits its outstanding
// download bytes.
type diskQueueEntry struct {
	infoHash string
	queuedAt time.Time
}

// admissionReserveBytes computes the free-space floor that must survive after
// a new download is admitted: max(percent of the total, absolute GB floor).
func admissionReserveBytes(total uint64, cfg models.StorageConfig) int64 {
	reserve := int64(total) * int64(cfg.AdmissionReservePercent) / 100
	if min := int64(cfg.AdmissionReserveMinGB) << 30; min > reserve {
		reserve = min
	}
	return reserve
}

// admitDownload is the pure admission decision: can `needed` bytes of new
// download data be started, given `free` disk bytes, `inflight` bytes already
// promised to running downloads, and the `reserve` free-space floor?
func admitDownload(needed, inflight, free, reserve int64) bool {
	return free-inflight-needed >= reserve
}

// streamModeGoverns reports whether a torrent of totalLength bytes is instead
// bounded by the storage guard's Stream Mode (sequential batches that fit the
// staging area), in which case admission control must not hold the whole
// torrent hostage — only its current batch ever occupies disk.
func streamModeGoverns(totalLength int64, cfg models.StorageConfig) bool {
	threshold := int64(cfg.StreamThresholdGB) << 30
	return cfg.S3Enabled && threshold > 0 && totalLength > threshold
}

// inflightRemainingBytes sums the bytes every OTHER live torrent still intends
// to write: per file with a non-None priority, its length minus the bytes the
// client already holds completed. S3-skipped, evicted, deselected and queued
// files carry priority None and are correctly excluded; a stream-mode torrent
// contributes only its current batch. Pieces straddling two files are counted
// toward both, which errs on the safe (over-reserving) side.
func (m *TorrentManager) inflightRemainingBytes(excludeHash string) int64 {
	var sum int64
	for _, t := range m.client.Torrents() {
		if t.InfoHash().HexString() == excludeHash {
			continue
		}
		if m.isDirectS3(t.InfoHash().HexString()) {
			continue // its outstanding bytes go to S3, not the local disk
		}
		select {
		case <-t.GotInfo():
		default:
			continue // metadata still arriving
		}
		for _, f := range t.Files() {
			if f.Priority() == torrent.PiecePriorityNone {
				continue
			}
			if left := f.Length() - f.BytesCompleted(); left > 0 {
				sum += left
			}
		}
	}
	return sum
}

// needsAdmission decides whether starting `needed` bytes of downloads now
// would squeeze the disk below the configured reserve. It never blocks on:
//   - admission disabled in the storage config            → false
//   - nothing left to download after the pre-check        → false
//   - stream mode governs this torrent                     → false
//   - the disk cannot be measured (or has no total)       → false
//   - the torrent has no job row (ephemeral, not queueable)
//     or the job is user-paused (resume re-applies)       → false
//
// Otherwise the space math decides.
func (m *TorrentManager) needsAdmission(infoHash, absSaveDir string, totalLength, needed int64) bool {
	if needed <= 0 {
		return false
	}
	// Direct-S3 torrents write to object storage, never to the local disk.
	if m.isDirectS3(infoHash) {
		return false
	}
	cfg := storageguard.LoadConfig()
	if !cfg.AdmissionEnabled {
		return false
	}
	if streamModeGoverns(totalLength, cfg) {
		return false
	}
	var job models.TorrentJob
	if err := db.DB.Where("info_hash = ?", infoHash).First(&job).Error; err != nil {
		return false
	}
	if job.Status == "paused" {
		return false
	}

	free, total, ok := storageguard.DiskUsage(absSaveDir)
	if !ok || total == 0 {
		return false // cannot measure: never block on a blind guess
	}
	inflight := m.inflightRemainingBytes(infoHash)
	reserve := admissionReserveBytes(total, cfg)
	return !admitDownload(needed, inflight, int64(free), reserve)
}

// ── FIFO disk queue ──────────────────────────────────────────────────────────

// enqueueDiskQueue records a torrent as waiting for disk space. It reports
// whether the entry is newly added (false ⇒ it was already queued).
func (m *TorrentManager) enqueueDiskQueue(infoHash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.diskQueue {
		if e.infoHash == infoHash {
			return false
		}
	}
	m.diskQueue = append(m.diskQueue, diskQueueEntry{infoHash: infoHash, queuedAt: time.Now()})
	return true
}

// dequeueDiskQueue removes a torrent from the wait queue (no-op if absent).
func (m *TorrentManager) dequeueDiskQueue(infoHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.diskQueue {
		if e.infoHash == infoHash {
			m.diskQueue = append(m.diskQueue[:i], m.diskQueue[i+1:]...)
			return
		}
	}
}

// queueLength reports how many torrents are waiting for disk space.
func (m *TorrentManager) queueLength() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.diskQueue)
}

// queueContains reports whether a torrent is waiting in the disk queue.
func (m *TorrentManager) queueContains(infoHash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.diskQueue {
		if e.infoHash == infoHash {
			return true
		}
	}
	return false
}

// tryAdmitQueueHead nudges the longest-waiting torrent through the priority
// funnel. If the disk can now fit its outstanding bytes the funnel admits it
// and the job flips back to "downloading"; otherwise the funnel re-queues it
// harmlessly. Called from the stats loop — never while holding the manager
// mutex.
func (m *TorrentManager) tryAdmitQueueHead() {
	m.mu.Lock()
	if len(m.diskQueue) == 0 {
		m.mu.Unlock()
		return
	}
	head := m.diskQueue[0].infoHash
	if m.applying[head] {
		m.mu.Unlock()
		return // a previous nudge is still in flight
	}
	m.applying[head] = true
	m.mu.Unlock()

	var job models.TorrentJob
	if err := db.DB.Where("info_hash = ?", head).First(&job).Error; err != nil {
		m.dequeueDiskQueue(head) // job vanished (deleted): drop the entry
		m.clearApplying(head)
		return
	}
	if job.Status != "queued" {
		// Paused / resumed / completed meanwhile — the entry is stale.
		m.dequeueDiskQueue(head)
		m.clearApplying(head)
		return
	}
	t := m.TorrentByHash(head)
	if t == nil {
		m.dequeueDiskQueue(head)
		m.clearApplying(head)
		return
	}

	// Cheap pre-check to avoid re-running the whole pre-download check
	// (hashing, S3 HEADs) while the disk is plainly still too full.
	if absDir, err := absPath(job.SaveDirectory); err == nil {
		if free, total, ok := storageguard.DiskUsage(absDir); ok && total > 0 {
			if int64(free)-m.inflightRemainingBytes(head) < admissionReserveBytes(total, storageguard.LoadConfig()) {
				m.clearApplying(head)
				return
			}
		}
	}

	go func() {
		defer m.clearApplying(head)
		if !m.applyFilePriorities(t, job.SelectedFiles, job.SaveDirectory) {
			return // still waiting for disk room
		}
		// Admitted: a queued torrent may still be torrent-level disallowed
		// (e.g. it was added via the file-selection flow), so re-enable.
		t.AllowDataDownload()
		db.DB.Model(&models.TorrentJob{}).Where("info_hash = ? AND status = ?", head, "queued").
			Update("status", "downloading")
		logger.Info("Torrent", "Disk queue admitted a waiting torrent", "info_hash", head,
			"still_waiting", m.queueLength())
	}()
}

// clearApplying drops the in-flight nudge marker for a torrent.
func (m *TorrentManager) clearApplying(infoHash string) {
	m.mu.Lock()
	delete(m.applying, infoHash)
	m.mu.Unlock()
}

// absPath resolves p to a cleaned absolute path (best effort).
func absPath(p string) (string, error) {
	if p == "" {
		p = "./data/manager/downloads"
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p, nil
	}
	return abs, nil
}
