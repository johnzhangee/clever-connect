// materialize.go — transient rehydration of indexed provider objects.
//
// Upload pipelines (Telegram MTProto, cloud-to-cloud transfers) can only read
// from a local path, but the file manager's disk is ephemeral: completed files
// are evicted by the storage guard and wiped on container restarts. These
// helpers reconnect a file-manager path with the copy that still lives on a
// configured rclone remote (S3, Google Drive, SFTP, ...) and stream it back
// into a temp file. The caller MUST call the returned cleanup once its upload
// finished — nothing is persisted on local disk.
package rclone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"clever-connect/internal/db"
	"clever-connect/internal/models"
)

// IndexedFileCandidate pairs one indexed provider object with its owning remote.
type IndexedFileCandidate struct {
	Remote models.RcloneRemote
	File   models.RcloneFile
}

// maxIndexCandidates bounds how many objects one materialization attempt may
// try, so a very common filename cannot stall an upload job for minutes.
const maxIndexCandidates = 5

// FindIndexedFileCandidates resolves a file-manager path to indexed provider
// objects across all enabled remotes.
//
// relPath is the path relative to the file-manager sandbox root with forward
// slashes (e.g. "downloads/movies/a.mp4"); it may be empty when it cannot be
// derived. basename is always consulted as well so flat uploads (transferred
// without keep-structure mirroring, stored under a different folder) are
// found too.
//
// Ordering is deterministic: exact Path matches first, then basename matches,
// each group ordered by (remote id, file id). Rows on disabled remotes and
// directory rows are never returned. When nothing matches, nil is returned.
func FindIndexedFileCandidates(relPath, basename string) []IndexedFileCandidate {
	rel := strings.Trim(filepath.ToSlash(strings.TrimSpace(relPath)), "/")
	base := strings.TrimSpace(basename)

	var rows []models.RcloneFile
	if rel != "" {
		rows = indexedFileRows("path = ?", rel)
	}
	if base != "" {
		rows = append(rows, indexedFileRows("name = ?", base)...)
	}
	if len(rows) == 0 {
		return nil
	}

	// Load the owning remotes in one query and keep only enabled ones,
	// preserving the deterministic (remote_id, id) order of the queries.
	remoteIDs := make(map[uint]bool, len(rows))
	for _, r := range rows {
		remoteIDs[r.RemoteID] = true
	}
	ids := make([]uint, 0, len(remoteIDs))
	for id := range remoteIDs {
		ids = append(ids, id)
	}
	var remotes []models.RcloneRemote
	if err := db.DB.Where("id IN ? AND enabled = ?", ids, true).Find(&remotes).Error; err != nil {
		return nil
	}
	remoteByID := make(map[uint]models.RcloneRemote, len(remotes))
	for _, r := range remotes {
		remoteByID[r.ID] = r
	}

	var out []IndexedFileCandidate
	seen := make(map[uint]bool, len(rows))
	for _, row := range rows {
		if seen[row.ID] || len(out) >= maxIndexCandidates {
			continue
		}
		remote, ok := remoteByID[row.RemoteID]
		if !ok {
			continue // owning remote is disabled (or was deleted)
		}
		seen[row.ID] = true
		out = append(out, IndexedFileCandidate{Remote: remote, File: row})
	}
	return out
}

// indexedFileRows runs one bounded, deterministic candidate query.
func indexedFileRows(where string, arg string) []models.RcloneFile {
	var rows []models.RcloneFile
	err := db.DB.Model(&models.RcloneFile{}).
		Where("is_dir = ?", false).
		Where(where, arg).
		Order("remote_id ASC, id ASC").
		Limit(maxIndexCandidates).
		Find(&rows).Error
	if err != nil {
		return nil
	}
	return rows
}

// MaterializeRemoteFile streams one indexed provider object into a local temp
// file (extension taken from the object name) using the same sandboxed
// `rclone cat` execution as StreamFile: env-only credentials, blank config
// stub, secret-scrubbed errors and the global concurrency gate.
//
// It returns the temp path and a cleanup function the caller MUST invoke once
// its upload finished (also on failure) so the transient copy never lingers
// on disk. An empty stream is treated as an error — the object may have been
// deleted between indexing and the fetch, and uploading a zero-byte file
// would silently corrupt the destination.
func MaterializeRemoteFile(ctx context.Context, remote *models.RcloneRemote, file *models.RcloneFile) (string, func(), error) {
	if remote == nil || file == nil {
		return "", nil, fmt.Errorf("remote or indexed file is nil")
	}

	ext := strings.ToLower(filepath.Ext(file.Name))
	tmp, err := os.CreateTemp("", "rclone-fetch-*"+ext)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	fullPath := FileFullPath(remote, file)
	if err := StreamFile(ctx, remote, fullPath, tmp); err != nil {
		tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("rclone fetch failed for %s: %w", fullPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("temp file close failed: %w", err)
	}
	info, statErr := os.Stat(tmpPath)
	if statErr != nil || info.Size() == 0 {
		cleanup()
		return "", nil, fmt.Errorf("materialized copy of %s is empty or unreadable", fullPath)
	}
	return tmpPath, cleanup, nil
}
