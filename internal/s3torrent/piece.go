package s3torrent

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/anacrolix/torrent/storage"
)

// Backend errors surfaced to the torrent client through WriteAt/ReadAt.
var (
	// errRAMValve is returned by WriteAt when too many part buffers are
	// resident. The client treats the block write as failed and re-requests
	// it shortly — the refusal is the (non-blocking) RAM valve.
	errRAMValve = errors.New("s3torrent: RAM valve closed — too many resident part buffers")
	// errNotAvailable means the byte range exists in no source yet.
	errNotAvailable = errors.New("s3torrent: piece data not available")
	// errFinalized guards against writes into an immutable committed object.
	errFinalized = errors.New("s3torrent: torrent already finalized (immutable)")
	// errClosed is returned once the store's workers were stopped.
	errClosed = errors.New("s3torrent: store closed")
)

// s3Piece implements storage.PieceImpl for one torrent piece. Blocks land
// directly in the buffers of the S3 parts the piece overlaps — there is no
// separate per-piece staging and no fan-out step.
type s3Piece struct {
	ts    *torrentStore
	index int
	start int64 // absolute start offset in the torrent
	end   int64 // absolute end offset (exclusive)

	mu        sync.Mutex
	completed bool
}

// WriteAt stores a block of piece data into the buffers of the overlapping
// S3 parts. It is invoked while the torrent client holds its own lock, so it
// must never block: instead of waiting it refuses with errRAMValve and the
// client re-requests the block. Blocks overlapping parts that are already
// uploading/uploaded are satisfied without a copy — their bytes are already
// staged in the bucket.
func (p *s3Piece) WriteAt(b []byte, off int64) (int, error) {
	if p.ts == nil || p.index < 0 {
		return 0, errNotAvailable
	}
	ts := p.ts
	if ts.isFinalized() {
		return 0, errFinalized
	}
	if ts.isClosed() {
		return 0, errClosed
	}
	if off < 0 || off > p.end-p.start {
		return 0, io.ErrShortWrite
	}
	absStart := p.start + off
	absEnd := absStart + int64(len(b))
	if absEnd > p.end {
		return 0, io.ErrShortWrite
	}

	written := 0
	for _, part := range ts.partsOverlapping(absStart, absEnd) {
		segStart := max(absStart, part.start)
		segEnd := min(absEnd, part.start+part.length)

		part.mu.Lock()
		switch part.state {
		case partIdle:
			if part.buf == nil {
				if !reservePartBuf(part.length) {
					part.mu.Unlock()
					// Bytes written into earlier parts stay — they are
					// idempotently rewritten when the block is re-requested.
					return written, errRAMValve
				}
				part.buf = make([]byte, part.length)
			}
			copy(part.buf[segStart-part.start:segEnd-part.start],
				b[segStart-absStart:segEnd-absStart])
			part.mu.Unlock()
		case partUploading, partUploaded:
			// Already staged in the bucket — nothing to store.
			part.mu.Unlock()
		}
		written = int(segEnd - absStart)
	}
	return written, nil
}

// MarkComplete is called by the client after the piece hash passed. It
// progresses the overlapping parts; a part whose every overlapping piece is
// verified becomes uploadable and is handed to the upload workers.
func (p *s3Piece) MarkComplete() error {
	if p.ts == nil || p.index < 0 {
		return errNotAvailable
	}
	p.mu.Lock()
	if p.completed {
		p.mu.Unlock()
		return nil
	}
	p.completed = true
	p.mu.Unlock()
	p.ts.onPieceCompleted(p)
	return nil
}

// MarkNotComplete rolls a piece back (the client re-downloads it). Parts
// that already left the idle state are untouched: their bytes were verified
// before hand-off and are already safe in the bucket.
func (p *s3Piece) MarkNotComplete() error {
	if p.ts == nil || p.index < 0 {
		return nil
	}
	p.mu.Lock()
	if !p.completed {
		p.mu.Unlock()
		return nil
	}
	p.completed = false
	p.mu.Unlock()
	p.ts.onPieceNotCompleted(p)
	return nil
}

// Completion reports the piece state. Pieces fully covered by stored part
// objects (resume after restart) are complete without any local data.
func (p *s3Piece) Completion() storage.Completion {
	if p.ts == nil || p.index < 0 {
		return storage.Completion{}
	}
	p.mu.Lock()
	completed := p.completed
	p.mu.Unlock()
	if completed || p.ts.pieceFullyUploaded(p.index, p.start, p.end) {
		return storage.Completion{Ok: true, Complete: true}
	}
	return storage.Completion{}
}

// ReadAt serves piece bytes from the freshest available source: a resident
// part buffer, else the staged part object in the bucket, else (after the
// torrent is finalized) the assembled data object.
func (p *s3Piece) ReadAt(b []byte, off int64) (int, error) {
	if p.ts == nil || p.index < 0 {
		return 0, errNotAvailable
	}
	if off < 0 {
		return 0, errors.New("s3torrent: negative read offset")
	}
	avail := p.end - p.start - off
	if avail <= 0 {
		if len(b) == 0 {
			return 0, nil
		}
		return 0, io.EOF
	}
	if int64(len(b)) > avail {
		b = b[:avail]
	}
	n, err := p.ts.readAt(b, p.start+off)
	if err != nil && n == 0 && len(b) > 0 {
		return 0, err
	}
	return n, err
}

// onPieceCompleted records the verified piece and makes any part that is now
// fully covered uploadable.
func (ts *torrentStore) onPieceCompleted(p *s3Piece) {
	var ready []*part
	ts.piecesMu.Lock()
	ts.completedPieces[p.index] = true
	ts.piecesMu.Unlock()

	for _, part := range ts.partsOverlapping(p.start, p.end) {
		part.mu.Lock()
		if part.state == partIdle && part.pending > 0 {
			part.pending--
			if part.pending == 0 {
				part.state = partUploading
				ready = append(ready, part)
			}
		}
		part.mu.Unlock()
	}
	for _, part := range ready {
		ts.enqueueUpload(part)
	}
	ts.maybeFinalize()
}

// onPieceNotCompleted rolls the piece bookkeeping back.
func (ts *torrentStore) onPieceNotCompleted(p *s3Piece) {
	ts.piecesMu.Lock()
	delete(ts.completedPieces, p.index)
	ts.piecesMu.Unlock()

	for _, part := range ts.partsOverlapping(p.start, p.end) {
		part.mu.Lock()
		if part.state == partIdle {
			part.pending++
		}
		part.mu.Unlock()
	}
}

// readAt fills b from the absolute torrent offset abs, stitching together
// the sources of every overlapped part.
func (ts *torrentStore) readAt(b []byte, abs int64) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if ts.isFinalized() {
		return ts.readObjectRange(b, ts.object, abs)
	}
	total := 0
	for _, part := range ts.partsOverlapping(abs, abs+int64(len(b))) {
		segStart := max(abs, part.start)
		segEnd := min(abs+int64(len(b)), part.start+part.length)
		segLen := int(segEnd - segStart)
		n, err := ts.readPartAt(part, b[total:total+segLen], segStart)
		total += n
		if err != nil {
			return total, err
		}
		if n != segLen {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}

// readPartAt reads one segment from its freshest source.
func (ts *torrentStore) readPartAt(part *part, b []byte, abs int64) (int, error) {
	part.mu.Lock()
	buf := part.buf
	state := part.state
	if buf != nil {
		// Buffer bytes are immutable once the part is uploading; for idle
		// parts concurrent block writes may race benignly — the client only
		// reads piece data it has verified.
		n := copy(b, buf[abs-part.start:])
		part.mu.Unlock()
		return n, nil
	}
	part.mu.Unlock()
	if state == partUploaded {
		return ts.readObjectRange(b, part.key, abs)
	}
	return 0, errNotAvailable
}

// readObjectRange pulls one byte range straight from an S3 object.
func (ts *torrentStore) readObjectRange(b []byte, key string, abs int64) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rc, err := ts.s3.ReadObjectRange(ctx, key, abs, abs+int64(len(b))-1)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	return io.ReadFull(rc, b)
}
