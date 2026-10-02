package filecore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

// errS3ObjectMissing is returned by materializeFromS3 when the registry record
// claims an S3 key but the object does NOT physically exist in the bucket
// (deleted by a lifecycle rule, manual cleanup, or never uploaded). Callers
// detect it with errors.Is to fall back to local disk or alternate keys
// instead of failing the whole upload.
var errS3ObjectMissing = errors.New("S3 object does not exist physically (registry record is stale)")

// S3ObjectExists performs a cheap HeadObject to verify an object is physically
// present in the bucket. It returns false when S3 is disabled, the key is empty,
// or the object is absent/unreachable. Use this to avoid trusting a stale DB
// record whose underlying object was deleted.
func S3ObjectExists(key string) bool {
	if !IsS3Enabled() || key == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return objectExists(ctx, key)
}

// StreamS3Object streams an object from S3 straight to an http.ResponseWriter
// with full HTTP Range (206 Partial Content) passthrough — enabling instant
// video seeking and chunked downloads with near-zero backend memory.
//
// It mirrors the relevant S3 response headers (Content-Type, Content-Length,
// Content-Range, Accept-Ranges) onto the client response, picks the correct
// status code, and copies the body through a 64 KB ring buffer.
//
// disposition is an optional Content-Disposition value (e.g.
// `attachment; filename="movie.mp4"`) to force a client download name.
//
// Returns false when the object cannot be served from S3 (disabled, missing or
// transport error) so the caller can transparently fall back to local disk.
func StreamS3Object(ctx context.Context, w http.ResponseWriter, key, rangeHeader, fallbackType, disposition string) bool {
	if !IsS3Enabled() || key == "" {
		return false
	}

	out, err := GetObject(ctx, key, rangeHeader)
	if err != nil || out == nil {
		if err != nil {
			logger.Warn("FileCore", "S3 stream fetch failed — falling back to local disk",
				"key", key, "error", err)
		}
		return false
	}
	defer out.Body.Close()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Connection", "keep-alive")

	if out.ContentType != nil && *out.ContentType != "" {
		w.Header().Set("Content-Type", *out.ContentType)
	} else if fallbackType != "" {
		w.Header().Set("Content-Type", fallbackType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	if out.ContentLength != nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", *out.ContentLength))
	}

	if disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}

	if out.ContentRange != nil && *out.ContentRange != "" {
		w.Header().Set("Content-Range", *out.ContentRange)
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	// Zero-allocation streaming copy with a 64 KB reusable buffer. An error
	// here usually means the client disconnected mid-stream, so it is ignored.
	buf := make([]byte, 64*1024)
	_, _ = io.CopyBuffer(w, out.Body, buf)
	return true
}

// PresignDownloadRedirect returns a presigned attachment URL valid for the
// given lifetime, or "" when S3 is unavailable. The handler 302-redirects to
// it so the client downloads directly from Cellar, fully offloading the server.
func PresignDownloadRedirect(ctx context.Context, key, filename string, lifetime time.Duration) string {
	if !IsS3Enabled() || key == "" {
		return ""
	}
	url, err := PresignDownloadURL(ctx, key, filename, lifetime)
	if err != nil {
		logger.Warn("FileCore", "Failed to presign S3 download URL", "key", key, "error", err)
		return ""
	}
	return url
}

// MaterializeForUpload guarantees a local-readable copy of a file for upload
// pipelines (e.g. the Telegram MTProto uploader) that can only read from disk.
//
// It is a thin wrapper around MaterializeForUploadWithTorrent for callers that
// do not have a torrent info hash (manual uploads, leecher downloads). See
// that function for the full lookup strategy.
//
// The caller MUST invoke cleanup() to remove any materialised temp file once done.
func MaterializeForUpload(absPath string) (localPath string, cleanup func(), err error) {
	return MaterializeForUploadWithTorrent(absPath, "")
}

// MaterializeForUploadWithTorrent is the torrent-aware variant of
// MaterializeForUpload. The optional torrentHash lets the resolver fall back
// to a precise S3-key lookup by torrent info hash before guessing by filename,
// which matters for the torrent → S3 → Telegram pipeline: a completed torrent
// file is archived to S3 keyed by its BLAKE3 checksum, and the registry record
// may live under a different file_path (checksum dedup against a prior run, or
// a re-download after an ephemeral-disk wipe). Without the hash hint the
// basename fallback is the only way to reconnect the Telegram job's path to the
// archived S3 object.
//
// Lookup strategy (strictly ordered, no fall-through between mismatched branches):
//
//  Every S3 tier is physically verified with a HeadObject before the object is
//  streamed. A registry record can advertise an s3_key whose underlying object
//  was later deleted from the bucket (lifecycle rule, manual cleanup); such a
//  stale record is skipped and the resolver falls through to local disk or the
//  next recovery tier instead of failing the upload.
//
//  1. File still exists on local disk:
//     - If it is registered by exact path AND its S3 object physically exists
//       → stream from S3 (S3 is authoritative; the local copy may be a sparse stub).
//     - If it is a torrent sparse stub (first 4 KiB all zero, torrentHash != "")
//       it is zeroed on disk after archiving — do NOT upload it; fall through to
//       the S3 recovery tiers so the real content is streamed from object storage.
//     - Otherwise the local copy is authoritative → returned as-is.
//
//  2a. Registry record found by exact file_path AND s3_key is set AND the
//     object physically exists → stream from S3 into a temp file. If the object
//     is missing, fall through (the DB record is stale).
//
//  2b. Registry record found by exact file_path BUT s3_key is EMPTY
//     → S3 upload for this file has not yet completed (or previously failed) and
//     the file is not on disk either. Return an actionable, retryable error
//     immediately; do NOT fall through to basename guessing (spurious S3 404).
//
//  3.  No usable exact-path record.
//     Resolve an S3 key through alternate lookups, in order (each physically verified):
//       a. torrent_hash match (precise, when torrentHash is provided),
//       b. file_path basename LIKE match (content archived under a different
//          path — checksum dedup, leecher flow, or re-download),
//       c. basename is itself the S3 key or the BLAKE3 checksum (stateless
//          leecher flow that queued the Telegram job with a hash-derived name).
//     Any hit with a present object → stream from S3.
//
//  4.  Last resort: basename looks like a hex-encoded key → fetch directly
//     from S3 without a registry record (orphaned reference), verified.
//
//  4b. Cloud-storage index (rclone remotes): the file exists on a configured
//     provider remote (S3, Google Drive, SFTP, ...) while the local copy was
//     evicted. Candidates are resolved by exact manager-relative path, then
//     by basename, and `rclone cat` streams the first that yields content
//     into a temp file the caller must delete via cleanup() right after the
//     upload finished.
//
//  5.  File present on disk (non-sparse) → return local copy.
//  6.  Nothing found → actionable error.
//
// The caller MUST invoke cleanup() to remove any materialised temp file once done.
func MaterializeForUploadWithTorrent(absPath, torrentHash string) (localPath string, cleanup func(), err error) {
	if absPath == "" {
		return "", nil, fmt.Errorf("empty path")
	}
	absPath = GetAbsolutePath(absPath)

	// 1. Inspect the local disk.
	onDisk := false
	if info, statErr := os.Stat(absPath); statErr == nil {
		if info.IsDir() {
			return "", nil, fmt.Errorf("target is a directory: %s", absPath)
		}
		onDisk = true
	}

	// A punched (sparse) stub left by the torrent pipeline after S3 archive is
	// all-zero on disk: punchHole frees the data blocks while preserving the
	// logical size so anacrolix does not re-request pieces. Uploading it would
	// send zeroes, so for torrent-originated uploads (torrentHash != "") we
	// detect the stub and fall through to the S3 recovery tiers instead. The
	// torrentHash guard keeps the behaviour unchanged for plain manual uploads
	// (which never produce sparse stubs).
	sparseStub := onDisk && torrentHash != "" && IsS3Enabled() && IsSparseStubFile(absPath)

	// 2. Exact-path registry record.
	reg, regOK := LookupRegistryByPath(absPath)
	if regOK && IsS3Stored(reg) {
		// 2a. S3 key is set — verify the object physically exists, then stream.
		// A registry record can claim an s3_key whose object was later deleted
		// from the bucket; if so, fall through to local disk / alternate keys
		// rather than failing the upload on a stale DB record.
		if p, c, ok, e := tryS3(reg.S3Key, absPath); e != nil {
			return "", nil, e
		} else if ok {
			return p, c, nil
		}
		logger.Warn("FileCore", "S3 object missing despite registry record — falling back",
			"path", absPath, "s3_key", reg.S3Key)
	}

	// Local copy is authoritative when it is real content on disk (not a
	// torrent sparse stub). This preserves the original fast path for files
	// that are still on disk and not yet archived to S3.
	if onDisk && !sparseStub {
		return absPath, func() {}, nil
	}

	// From here the local file is either absent OR a torrent sparse stub.
	// 2b only applies when the exact-path record has NO s3_key at all (the
	// upload is pending). If the record had an s3_key but the object was
	// physically missing, we already fell through above and now try alternate
	// recovery instead of looping on "retry later".
	if regOK && !IsS3Stored(reg) {
		return "", nil, fmt.Errorf(
			"file registered but S3 upload not yet complete for %s — retry later", absPath)
	}

	// 3. No exact-path record (or its S3 object was missing). Resolve an S3
	//    key through alternate lookups so content archived under a different
	//    path (checksum dedup / leecher / re-download after disk wipe) is still
	//    recoverable for the upload. Each candidate is physically verified.
	if s3Key, tier, found := lookupAlternateS3Key(absPath, torrentHash); found {
		if p, c, ok, e := tryS3(s3Key, absPath); e != nil {
			return "", nil, e
		} else if ok {
			logger.Info("FileCore", "Materialized file from S3 via alternate lookup",
				"tier", tier, "path", absPath, "s3_key", s3Key)
			return p, c, nil
		}
		logger.Warn("FileCore", "Alternate S3 key also physically missing — continuing",
			"tier", tier, "path", absPath, "s3_key", s3Key)
	}

	// 3c. Basename is itself the S3 key or the BLAKE3 checksum — handles the
	//     stateless-leecher flow where the Telegram job was queued with a
	//     hash-derived filename (e.g. /downloads/<checksum>). Verified physically.
	basename := filepath.Base(absPath)
	if basename != "" && basename != "." {
		var candidate models.FileRegistry
		if e := db.DB.Where("s3_key = ?", basename).First(&candidate).Error; e == nil && candidate.S3Key != "" {
			if p, c, ok, e2 := tryS3(candidate.S3Key, absPath); e2 != nil {
				return "", nil, e2
			} else if ok {
				logger.Info("FileCore", "Materialized via s3_key basename match",
					"basename", basename, "s3_key", candidate.S3Key)
				return p, c, nil
			}
		}
		if e := db.DB.Where("checksum = ?", basename).First(&candidate).Error; e == nil && candidate.S3Key != "" {
			if p, c, ok, e2 := tryS3(candidate.S3Key, absPath); e2 != nil {
				return "", nil, e2
			} else if ok {
				logger.Info("FileCore", "Materialized via checksum basename match",
					"basename", basename, "s3_key", candidate.S3Key)
				return p, c, nil
			}
		}
	}

	// 4. Last resort: basename looks like a hex-encoded S3 key — fetch directly
	//    from object storage (orphaned reference with no DB record). Verified.
	if IsS3Enabled() && isHexKey(basename) {
		if p, c, ok, e := tryS3(basename, absPath); e != nil {
			return "", nil, e
		} else if ok {
			logger.Warn("FileCore", "Materializing from S3 by raw key (no registry record)",
				"key", basename, "path", absPath)
			return p, c, nil
		}
	}

	// 4b. Cloud-storage index (universal rclone remotes): the file is on a
	//     configured provider remote (S3, Google Drive, SFTP, ...) while the
	//     local copy was evicted. `rclone cat` streams it into a temp file;
	//     the caller's cleanup() deletes it as soon as the upload finished.
	if p, c, ok, e := tryRcloneIndex(absPath); e != nil {
		return "", nil, e
	} else if ok {
		return p, c, nil
	}

	// 5. A torrent sparse stub on disk with no recoverable S3 key anywhere —
	//    the S3 archive likely failed (or the object was deleted). Best effort:
	//    return the local stub so the caller can decide (prior on-disk behaviour).
	if onDisk {
		return absPath, func() {}, nil
	}

	// 6. Genuinely unrecoverable: not on disk, not physically in any S3 key
	//    and not present on any indexed cloud-storage remote either.
	return "", nil, fmt.Errorf(
		"file not found locally and not archived in S3 or any cloud remote: %s", absPath)
}

// lookupAlternateS3Key resolves an S3 object key for a path whose exact
// file_path is not registered, recovering content that was archived under a
// different path. It mirrors the three-tier recovery used by the torrent_s3_move
// scheduler handler so the Telegram upload pipeline can rehydrate the same
// archived file.
//
// Order:
//  1. torrent_hash match (precise — only when torrentHash is provided and the
//     registry record was tagged with that hash).
//  2. file_path basename LIKE match (content archived under a different path
//     that shares the same filename — checksum dedup, leecher flow, re-download).
//
// Returns the S3 key, a human-readable tier label, and true when found.
func lookupAlternateS3Key(absPath, torrentHash string) (s3Key, tier string, found bool) {
	if torrentHash != "" {
		var reg models.FileRegistry
		if err := db.DB.Where("torrent_hash = ? AND s3_key <> ''", torrentHash).First(&reg).Error; err == nil {
			return reg.S3Key, "torrent_hash", true
		}
	}
	basename := filepath.Base(absPath)
	if basename != "" && basename != "." {
		var reg models.FileRegistry
		if err := db.DB.Where("file_path LIKE ? AND s3_key <> ''", "%/"+basename).First(&reg).Error; err == nil {
			return reg.S3Key, "filename match", true
		}
	}
	return "", "", false
}

// IsSparseStubFile reports whether the first 4 KiB of the file are all zeroes,
// a reliable heuristic for detecting a punchHole sparse stub left by the torrent
// pipeline after it archived the file to S3. Returns false on any read error or
// for a zero-length file (safe: those are never torrent data stubs).
func IsSparseStubFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return false
	}

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false
	}
	for i := 0; i < n; i++ {
		if buf[i] != 0 {
			return false
		}
	}
	return true
}

// materializeFromS3 streams an S3 object identified by key into a temp file
// whose extension is inferred from the reference path. Returns the temp path
// and a cleanup function the caller must call when done.
//
// It first verifies the object physically exists in the bucket with a
// HeadObject. A registry record may advertise an s3_key that was later deleted
// from S3 (lifecycle rule, manual cleanup); without this check the system would
// trust the database and stream a non-existent object. When the object is
// missing it returns the errS3ObjectMissing sentinel so callers can fall back
// to local disk or an alternate S3 key instead of hard-failing.
func materializeFromS3(s3Key, refPath string) (string, func(), error) {
	if !S3ObjectExists(s3Key) {
		return "", nil, fmt.Errorf("%w: %s", errS3ObjectMissing, s3Key)
	}

	ext := filepath.Ext(refPath)
	tmp, err := os.CreateTemp("", "s3-upload-*"+ext)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	ctx, cancel := context.WithTimeout(context.Background(), archiveUploadCtxTimeout)
	defer cancel()

	out, gErr := GetObject(ctx, s3Key, "")
	if gErr != nil {
		tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("S3 fetch failed for key %s: %w", s3Key, gErr)
	}
	defer out.Body.Close()

	if _, cErr := io.Copy(tmp, out.Body); cErr != nil {
		tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("S3 download to temp failed: %w", cErr)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("temp file close failed: %w", err)
	}

	logger.Info("FileCore", "Materialized file from S3 for upload",
		"key", s3Key, "temp", tmpPath)
	return tmpPath, cleanup, nil
}

// tryS3 attempts to materialize an S3 object and reports whether the object
// was physically present. It is a thin wrapper around materializeFromS3 used by
// the multi-tier resolver so a stale (deleted) S3 object does not abort the
// whole upload — instead the caller falls through to the next recovery tier or
// to the local disk copy.
//
// Returns:
//   - (path, cleanup, true, nil)  on success.
//   - (nil, nil, false, nil)      when the object is physically missing (fall through).
//   - (nil, nil, false, err)      on a real transport/IO error (propagate).
func tryS3(s3Key, refPath string) (string, func(), bool, error) {
	p, c, err := materializeFromS3(s3Key, refPath)
	if err == nil {
		return p, c, true, nil
	}
	if errors.Is(err, errS3ObjectMissing) {
		return "", nil, false, nil
	}
	return "", nil, false, err
}

// isHexKey reports whether s looks like a lowercase hex-encoded hash that
// could be a valid S3 object key (BLAKE3 checksum: exactly 64 chars, or any
// even-length run of [0-9a-f] ≥ 16 chars).
func isHexKey(s string) bool {
	if len(s) < 16 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

