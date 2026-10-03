package torrent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/filecore"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"

	"github.com/anacrolix/torrent"
)

// precheckDecision is the outcome of the pre-download existence check for one
// torrent file.
type precheckDecision int

const (
	// precheckDownload: no healthy copy exists anywhere — fetch from the swarm.
	precheckDownload precheckDecision = iota
	// precheckSkipLocal: a local copy was verified healthy piece-by-piece.
	precheckSkipLocal
	// precheckSkipS3: the authoritative copy is confirmed in object storage.
	precheckSkipS3
)

// precheckFile runs the tiered existence/health check for one torrent file
// BEFORE its download priority is set, so the swarm only ever delivers bytes
// that are actually missing or corrupt:
//
//  1. Exact path on the local disk — a size-matching file is hash-verified
//     piece-by-piece (Piece.VerifyDataContext). Verified pieces are marked
//     complete in the torrent client, so nothing is re-fetched and
//     BytesCompleted already counts the file as done. A wrong-size
//     placeholder/partial file is removed so the client cleanly replaces it
//     with the torrent-downloaded version; a right-size-but-corrupt file
//     (typically a punchHole sparse stub) is kept for piece-wise repair but
//     falls through to the S3 tier first, since the stub usually exists
//     precisely because the file was archived to object storage.
//  2. Registered local copy — a FileRegistry record with the same basename
//     and byte size whose file still exists on the managed disk is copied
//     into the expected path and then verified as (1).
//  3. S3 object storage — the offload ledger (per-torrent provenance) and the
//     FileRegistry tiers (exact path / torrent hash / basename) are
//     consulted. A candidate counts only when the S3 object still exists
//     with exactly the expected size (real content, not a placeholder).
//
// precheckFile never holds the manager mutex and never changes priorities
// itself; the caller acts on the returned decision.
func (m *TorrentManager) precheckFile(t *torrent.Torrent, f *torrent.File, absSaveDir, infoHash string) precheckDecision {
	if f.Length() == 0 {
		// Nothing to fetch, nothing to verify.
		return precheckSkipLocal
	}

	// Fast path: the torrent client already counts every piece of this file
	// as completed (hash-verified by the library or marked complete by an
	// earlier pre-check run) — there is nothing to fetch and nothing to hash
	// again. This keeps queue-drain retries and resume re-applications cheap.
	if f.BytesCompleted() >= f.Length() {
		return precheckSkipLocal
	}
	relPath := f.Path()
	absPath := filepath.Clean(filepath.Join(absSaveDir, relPath))

	// ── Tier 1: exact path on the local disk ────────────────────────────────
	corruptLocal := false
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		switch {
		case info.Size() != f.Length():
			// Placeholder / partial file with the wrong size: remove it so the
			// client writes the real content into a clean file instead of
			// extending garbage ("replace with the torrent-downloaded version").
			if rmErr := os.Remove(absPath); rmErr != nil {
				logger.Warn("Torrent", "Pre-check: failed to remove wrong-size local file",
					"file", relPath, "expected", f.Length(), "on_disk", info.Size(), "error", rmErr)
			} else {
				logger.Info("Torrent", "Pre-check: removed wrong-size local copy — will re-download",
					"file", relPath, "expected", f.Length(), "on_disk", info.Size())
			}
		case m.verifyLocalTorrentFile(t, f, absPath):
			logger.Info("Torrent", "Pre-check: local copy hash-verified — skipping download",
				"file", relPath, "size", formatBytes(f.Length()))
			return precheckSkipLocal
		default:
			// Right size but the content failed piece verification — a
			// punchHole sparse stub or genuinely corrupt data. Keep it for
			// now (piece-wise repair can still reuse any good pieces), but
			// consult S3 first: the stub is usually the residue of a file
			// that was already archived to object storage.
			corruptLocal = true
			logger.Info("Torrent", "Pre-check: local copy failed piece verification — checking S3 before re-downloading",
				"file", relPath, "size", formatBytes(f.Length()))
		}
	}

	// ── Tier 2: registered copy elsewhere on the managed disk ──────────────
	if !corruptLocal {
		if src, ok := findLocalRegisteredCopy(relPath, f.Length()); ok && src != absPath {
			if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err == nil {
				if err := copyFile(src, absPath); err == nil {
					if m.verifyLocalTorrentFile(t, f, absPath) {
						logger.Info("Torrent", "Pre-check: restored file from registered local copy — skipping download",
							"file", relPath, "source", src, "size", formatBytes(f.Length()))
						return precheckSkipLocal
					}
					logger.Info("Torrent", "Pre-check: registered local copy failed verification — will download",
						"file", relPath, "source", src)
				} else {
					logger.Warn("Torrent", "Pre-check: copying registered local copy failed",
						"file", relPath, "source", src, "error", err)
				}
			}
		}
	}

	// ── Tier 3: S3 object storage ──────────────────────────────────────────
	if key, ok := findS3CopyForFile(absPath, relPath, infoHash, f.Length()); ok {
		if corruptLocal {
			// The unverified local copy is dead weight now that S3 is
			// confirmed authoritative — drop it instead of hashing it again
			// on every future pre-check.
			if rmErr := os.Remove(absPath); rmErr != nil {
				logger.Warn("Torrent", "Pre-check: failed to remove unverified local copy",
					"file", relPath, "error", rmErr)
			}
		}
		logger.Info("Torrent", "Pre-check: file already in S3 — skipping download",
			"file", relPath, "s3_key", key, "size", formatBytes(f.Length()))
		return precheckSkipS3
	}

	logger.Info("Torrent", "Pre-check: no healthy existing copy — downloading",
		"file", relPath, "size", formatBytes(f.Length()))
	return precheckDownload
}

// verifyLocalTorrentFile hash-verifies the on-disk content of f piece by
// piece. Verified pieces are marked complete in the torrent client (and its
// persistent piece-completion cache), so the data is never re-fetched.
func (m *TorrentManager) verifyLocalTorrentFile(t *torrent.Torrent, f *torrent.File, path string) bool {
	// A punchHole sparse stub carries the right logical size but zero data —
	// do not waste time hashing zeros (the S3 tier satisfies such files).
	if isTorrentFileSparse(path) {
		return false
	}
	ctx := context.Background()
	for pi := f.BeginPieceIndex(); pi < f.EndPieceIndex(); pi++ {
		if err := t.Piece(pi).VerifyDataContext(ctx); err != nil {
			logger.Warn("Torrent", "Pre-check: piece verification error",
				"file", f.Path(), "piece", pi, "error", err)
			return false
		}
	}
	// The verdict lives in the completion state: every piece of the file must
	// now be complete. Pieces shared with adjacent files hash-verify for all
	// files they cover, so a missing neighbour keeps this file incomplete.
	return f.BytesCompleted() >= f.Length()
}

// findLocalRegisteredCopy searches the FileRegistry for a locally stored file
// with the same basename and byte size that can seed the torrent file. The
// registry holds every file ever saved through the manager sandbox, so this
// covers copies living anywhere on the managed disk, not just the torrent
// save directory. Content identity is confirmed afterwards by piece
// verification, so a basename collision costs a wasted copy at worst — it
// can never produce a wrong skip.
func findLocalRegisteredCopy(relPath string, length int64) (string, bool) {
	base := filepath.Base(filepath.FromSlash(relPath))
	var regs []models.FileRegistry
	if err := db.DB.
		Where("file_size = ? AND file_path LIKE ?", length, "%/"+base).
		Order("created_at DESC").
		Limit(20).
		Find(&regs).Error; err != nil {
		return "", false
	}
	for _, r := range regs {
		if r.FilePath == "" {
			continue
		}
		abs := filecore.GetAbsolutePath(r.FilePath)
		if info, err := os.Stat(abs); err == nil && !info.IsDir() && info.Size() == length {
			return abs, true
		}
	}
	return "", false
}

// findS3CopyForFile reports whether the file is already secured in S3: an
// object that still exists with exactly the expected size. The offload ledger
// (per-torrent, per-file provenance written by the storage guard) is checked
// first, then the FileRegistry tiers (exact path / torrent hash / basename).
// Returns the S3 key of the confirmed copy.
func findS3CopyForFile(absPath, relPath, infoHash string, length int64) (string, bool) {
	if !s3store.Enabled() {
		return "", false
	}
	if row, ok := findLedgerRow(absPath, relPath, infoHash, length); ok {
		if s3ObjectHealthy(row.S3Key, length) {
			return row.S3Key, true
		}
		return "", false
	}
	if reg, _, found := findArchivedRegistry(absPath, infoHash); found {
		// Guard against same-name/different-content registry hits.
		if reg.FileSize > 0 && reg.FileSize != length {
			return "", false
		}
		if s3ObjectHealthy(reg.S3Key, length) {
			return reg.S3Key, true
		}
	}
	return "", false
}

// findLedgerRow looks for an uploaded TorrentFileOffload row of this torrent
// matching the given file (by relative path inside the torrent or absolute
// local path) with a non-empty S3 key.
func findLedgerRow(absPath, relPath, infoHash string, length int64) (models.TorrentFileOffload, bool) {
	var rows []models.TorrentFileOffload
	if err := db.DB.Where("info_hash = ? AND uploaded = ?", infoHash, true).Find(&rows).Error; err != nil {
		return models.TorrentFileOffload{}, false
	}
	for _, r := range rows {
		if r.S3Key == "" {
			continue
		}
		if r.RelPath != relPath && r.FilePath != absPath {
			continue
		}
		// Size drift (torrent v2, healed row) — content differs, not a match.
		if r.Size > 0 && r.Size != length {
			continue
		}
		return r, true
	}
	return models.TorrentFileOffload{}, false
}

// s3ObjectHealthy confirms the S3 object exists and carries exactly the
// expected number of bytes — a deleted or truncated object is not a usable
// copy.
func s3ObjectHealthy(key string, expected int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	size, exists, err := s3store.ObjectSize(ctx, key)
	if err != nil || !exists {
		return false
	}
	return size == expected
}
