package s3torrent

import (
	"context"
	"errors"
	"time"

	"clever-connect/internal/logger"

	"github.com/minio/minio-go/v7"
)

// isNoSuchUpload reports whether the S3 error says the multipart upload
// session no longer exists (expired or aborted). The staged part objects
// remain valid in that case — only the assembly session must be restarted.
func isNoSuchUpload(err error) bool {
	if err == nil {
		return false
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchUpload"
	}
	return false
}

// startWorkers spawns the parallel part-object upload workers.
func (ts *torrentStore) startWorkers() {
	ts.uploadQueue = make(chan *part, len(ts.parts)+8)
	for i := 0; i < uploadWorkers; i++ {
		ts.wg.Add(1)
		go ts.uploadWorker()
	}
}

// uploadWorker drains the upload queue until the store closes.
func (ts *torrentStore) uploadWorker() {
	defer ts.wg.Done()
	for {
		select {
		case <-ts.stopCh:
			return
		case part := <-ts.uploadQueue:
			if part == nil {
				return
			}
			ts.uploadPart(part)
		}
	}
}

// enqueueUpload queues a fully-covered part for upload. The queue can
// overflow only pathologically (more parts than slots); retry shortly
// rather than dropping — the part's bytes exist only in RAM.
func (ts *torrentStore) enqueueUpload(part *part) {
	select {
	case ts.uploadQueue <- part:
	default:
		if ts.isClosed() {
			return
		}
		go func() {
			time.Sleep(time.Second)
			ts.enqueueUpload(part)
		}()
	}
}

// uploadPart PUTs a fully-covered part buffer into the bucket as its staged
// object, records the confirmation in the DB row, frees the RAM and nudges
// the finalizer.
func (ts *torrentStore) uploadPart(part *part) {
	part.mu.Lock()
	if part.state != partUploading || part.buf == nil {
		part.mu.Unlock()
		return
	}
	buf := part.buf
	part.mu.Unlock()

	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err = ts.s3.PutObjectBytes(ctx, part.key, buf)
		cancel()
		if err == nil {
			break
		}
		logger.Error("S3Torrent", "Part upload failed — retrying",
			"info_hash", ts.hash, "part", part.index, "attempt", attempt, "error", err)
		select {
		case <-ts.stopCh:
			return // store closing: state stays resumable via the DB row
		case <-time.After(time.Duration(attempt) * 2 * time.Second):
		}
	}
	if err != nil {
		logger.Error("S3Torrent", "Part upload abandoned — bytes stay in RAM until restart",
			"info_hash", ts.hash, "part", part.index, "error", err)
		return
	}

	ts.recordPartUploaded(part.index)

	part.mu.Lock()
	part.state = partUploaded
	part.buf = nil
	part.mu.Unlock()
	releasePartBuf(part.length)

	logger.Info("S3Torrent", "Part stored in S3",
		"info_hash", ts.hash, "part", part.index, "bytes", part.length)
	ts.maybeFinalize()
}

// maybeFinalize kicks the finalizer when every piece is verified and every
// part object is stored.
func (ts *torrentStore) maybeFinalize() {
	ts.piecesMu.RLock()
	if ts.finalized || ts.finalizing {
		ts.piecesMu.RUnlock()
		return
	}
	complete := len(ts.completedPieces) >= ts.numPieces
	uploaded := len(ts.uploadedParts) >= len(ts.parts)
	ts.piecesMu.RUnlock()
	if complete && uploaded {
		ts.kickFinalize()
	}
}

// kickFinalize transitions to the finalizing state and launches finalize().
func (ts *torrentStore) kickFinalize() {
	ts.piecesMu.Lock()
	if ts.finalized || ts.finalizing {
		ts.piecesMu.Unlock()
		return
	}
	ts.finalizing = true
	ts.finalizeAttempts++
	ts.piecesMu.Unlock()
	go ts.finalize()
}

// ensureUploadID returns the current multipart session, starting one if the
// store never initiated an upload.
func (ts *torrentStore) ensureUploadID(ctx context.Context) (string, error) {
	ts.piecesMu.Lock()
	id := ts.uploadID
	ts.piecesMu.Unlock()
	if id != "" {
		return id, nil
	}
	return ts.startUpload(ctx)
}

// startUpload initiates a multipart upload and records its ID.
func (ts *torrentStore) startUpload(ctx context.Context) (string, error) {
	id, err := ts.s3.StartMultipartUpload(ctx, ts.object)
	if err != nil {
		return "", err
	}
	ts.piecesMu.Lock()
	ts.uploadID = id
	ts.persistRow()
	ts.piecesMu.Unlock()
	return id, nil
}

// restartUpload replaces a dead upload session with a fresh one; the staged
// part objects survive and are re-copied into the new session.
func (ts *torrentStore) restartUpload(ctx context.Context) (string, error) {
	ts.piecesMu.Lock()
	old := ts.uploadID
	ts.uploadID = ""
	ts.piecesMu.Unlock()
	if old != "" {
		abortCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		_ = ts.s3.AbortMultipartUpload(abortCtx, ts.object, old)
		cancel()
	}
	return ts.startUpload(ctx)
}

// finalize assembles the staged part objects into the final data object via
// server-side CopyObjectPart calls, commits the multipart upload, deletes
// the part objects and marks the DB row finalized.
func (ts *torrentStore) finalize() {
	if ts.single {
		// The staged object IS the final object (single PUT path).
		ts.markFinalized()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	uploadID, err := ts.ensureUploadID(ctx)
	if err != nil {
		ts.finalizeFailed(err)
		return
	}
	parts := make([]CompletePart, 0, len(ts.parts))
	for _, part := range ts.parts {
		etag, err := ts.s3.CopyObjectPart(ctx, part.key, ts.object, uploadID, part.index+1, part.length)
		if err != nil && isNoSuchUpload(err) {
			// Upload session expired/aborted — start a fresh one; the
			// staged part objects are re-copied from the beginning.
			if uploadID, err = ts.restartUpload(ctx); err != nil {
				ts.finalizeFailed(err)
				return
			}
			if etag, err = ts.s3.CopyObjectPart(ctx, part.key, ts.object, uploadID, part.index+1, part.length); err != nil {
				ts.finalizeFailed(err)
				return
			}
		} else if err != nil {
			ts.finalizeFailed(err)
			return
		}
		parts = append(parts, CompletePart{PartNumber: part.index + 1, ETag: etag})
	}
	if err := ts.s3.CompleteMultipartUpload(ctx, ts.object, uploadID, parts); err != nil {
		ts.finalizeFailed(err)
		return
	}
	// The part objects are redundant once assembled — reclaim bucket space.
	if _, err := ts.s3.DeletePrefix(ctx, ts.partsPrefix); err != nil {
		logger.Warn("S3Torrent", "Failed to delete staged part objects",
			"info_hash", ts.hash, "error", err)
	}
	ts.markFinalized()
}

// finalizeFailed logs, releases the finalizing guard and schedules a retry.
func (ts *torrentStore) finalizeFailed(err error) {
	logger.Error("S3Torrent", "Finalize failed — will retry",
		"info_hash", ts.hash, "error", err)
	ts.piecesMu.Lock()
	ts.finalizing = false
	if isNoSuchUpload(err) {
		// The session is gone; a fresh one starts on the retry.
		ts.uploadID = ""
	}
	ts.persistRow()
	attempts := ts.finalizeAttempts
	ts.piecesMu.Unlock()
	if attempts < maxFinalizeAttempts && !ts.isClosed() {
		time.AfterFunc(30*time.Second, ts.kickFinalize)
	}
}

// markFinalized commits the finalized state and fires the notifier.
func (ts *torrentStore) markFinalized() {
	now := time.Now()
	ts.piecesMu.Lock()
	if ts.finalized {
		ts.piecesMu.Unlock()
		return
	}
	ts.finalized = true
	ts.finalizing = false
	ts.finalizedAt = &now
	ts.uploadID = ""
	ts.persistRow()
	ts.piecesMu.Unlock()

	logger.Info("S3Torrent", "Torrent committed to S3",
		"info_hash", ts.hash, "object", ts.object, "bytes", ts.total)
	ts.client.notifyFinalize(ts.hash)
}
