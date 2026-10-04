package s3torrent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"gorm.io/gorm"
)

const (
	// PartSize is the S3 part granularity: a completed part is staged as its
	// own object and assembled server-side into the final data object.
	PartSize = 64 << 20
	// maxResidentPartBuffers caps how many part-size buffers may hold data
	// at once (the RAM valve). Beyond it, WriteAt refuses blocks and the
	// torrent client re-requests them shortly.
	maxResidentPartBuffers = 4
	// uploadWorkers is the number of parallel part-object uploads per torrent.
	uploadWorkers = 2
	// maxFinalizeAttempts bounds the finalize retry loop.
	maxFinalizeAttempts = 5
)

// partBufBudgetBytes is the RAM-valve ceiling in bytes across all resident
// part buffers. A var so unit tests can shrink it.
var partBufBudgetBytes int64 = maxResidentPartBuffers * PartSize

// residentPartBufBytes counts bytes currently held in live part buffers
// across every direct-S3 torrent.
var residentPartBufBytes atomic.Int64

// reservePartBuf takes RAM-valve budget for one part buffer. Returns false
// when too many buffers are resident; the caller must refuse the write.
func reservePartBuf(length int64) bool {
	for {
		cur := residentPartBufBytes.Load()
		if cur+length > partBufBudgetBytes {
			return false
		}
		if residentPartBufBytes.CompareAndSwap(cur, cur+length) {
			return true
		}
	}
}

func releasePartBuf(length int64) {
	residentPartBufBytes.Add(-length)
}

// partState is the lifecycle of one S3 part.
type partState int

const (
	partIdle      partState = iota // receiving pieces into RAM
	partUploading                  // buffer handed to an uploader goroutine
	partUploaded                   // staged part object confirmed in the bucket
)

type part struct {
	index  int    // 0-based part number
	start  int64  // absolute start offset in the torrent
	length int64  // byte length (the last part is short)
	key    string // S3 key of the staged part object

	mu      sync.Mutex
	buf     []byte
	state   partState
	pending int // overlapping pieces not yet hash-verified
}

// torrentStore is the direct-to-S3 storage bound to one torrent.
type torrentStore struct {
	hash        string
	object      string // final assembled S3 object
	partsPrefix string // staging prefix of the part objects
	total       int64
	pieceLength int64
	numPieces   int
	partSize    int64
	parts       []*part
	single      bool // total ≤ partSize: the whole torrent is one object

	s3     s3API
	client *Client

	// piecesMu guards piece-completion and upload-state bookkeeping and all
	// mutations of the persistence row. Held only for brief in-memory and
	// DB updates — never while network I/O runs.
	piecesMu         sync.RWMutex
	completedPieces  map[int]bool
	uploadedParts    map[int]bool
	uploadID         string
	finalized        bool
	finalizing       bool
	finalizeAttempts int
	finalizedAt      *time.Time

	pieces []*s3Piece // per-piece structs handed to the torrent client

	uploadQueue chan *part
	stopCh      chan struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup
}

// liveStores tracks open stores by info hash so delete-time cleanup can stop
// workers and abort in-flight uploads without the torrent running.
var liveStores sync.Map // infoHash hex → *torrentStore

// openTorrentStore opens the direct-to-S3 storage for one torrent, resuming
// from the persistence row when present.
func openTorrentStore(cl *Client, info *metainfo.Info, hashHex string) (*torrentStore, error) {
	st, err := s3store.Get()
	if err != nil {
		return nil, err
	}
	api := newS3API(st)
	if api == nil {
		return nil, ErrNoS3
	}
	return openTorrentStoreWith(cl, info, hashHex, api, PartSize)
}

// openTorrentStoreWith is the injectable core of openTorrentStore.
func openTorrentStoreWith(cl *Client, info *metainfo.Info, hashHex string, api s3API, partSize int64) (*torrentStore, error) {
	if api == nil {
		return nil, ErrNoS3
	}
	total := info.TotalLength()
	pieceLength := info.PieceLength
	numPieces := info.NumPieces()
	if total <= 0 || pieceLength <= 0 || numPieces <= 0 {
		return nil, fmt.Errorf("s3torrent: unsupported torrent dimensions (total=%d piece_length=%d pieces=%d)",
			total, pieceLength, numPieces)
	}

	prefix := loadS3Prefix()
	ts := &torrentStore{
		hash:            hashHex,
		object:          s3store.KeyForTorrentData(prefix, hashHex),
		partsPrefix:     s3store.KeyForTorrentPartsPrefix(prefix, hashHex),
		total:           total,
		pieceLength:     pieceLength,
		numPieces:       numPieces,
		partSize:        partSize,
		single:          total <= partSize,
		s3:              api,
		client:          cl,
		completedPieces: make(map[int]bool),
		uploadedParts:   make(map[int]bool),
		stopCh:          make(chan struct{}),
	}
	ts.buildParts()
	ts.buildPieces(info)

	if err := ts.loadOrCreateRow(); err != nil {
		return nil, err
	}
	liveStores.Store(hashHex, ts)
	ts.startWorkers()
	logger.Info("S3Torrent", "Opened direct-to-S3 torrent storage",
		"info_hash", hashHex, "total", total, "parts", len(ts.parts),
		"resumed_parts", len(ts.uploadedParts), "finalized", ts.isFinalized())
	return ts, nil
}

// buildParts lays out the part list (aligned at partSize multiples).
func (ts *torrentStore) buildParts() {
	if ts.single {
		// One part covering the whole torrent; its staged object key IS the
		// final object key — a single PUT replaces the multipart dance.
		ts.parts = []*part{{
			index:  0,
			start:  0,
			length: ts.total,
			key:    ts.object,
		}}
		return
	}
	n := int((ts.total + ts.partSize - 1) / ts.partSize)
	ts.parts = make([]*part, 0, n)
	for i := 0; i < n; i++ {
		start := int64(i) * ts.partSize
		length := ts.partSize
		if rem := ts.total - start; rem < length {
			length = rem
		}
		ts.parts = append(ts.parts, &part{
			index:  i,
			start:  start,
			length: length,
			key:    fmt.Sprintf("%s%010d", ts.partsPrefix, i),
		})
	}
}

// buildPieces preallocates the per-piece structs handed to the client.
func (ts *torrentStore) buildPieces(info *metainfo.Info) {
	ts.pieces = make([]*s3Piece, ts.numPieces)
	for i := 0; i < ts.numPieces; i++ {
		p := info.Piece(i)
		ts.pieces[i] = &s3Piece{
			ts:    ts,
			index: i,
			start: p.Offset(),
			end:   p.Offset() + p.Length(),
		}
	}
}

// pieceFor returns the shared per-piece struct for a piece index.
func (ts *torrentStore) pieceFor(index int) *s3Piece {
	if index < 0 || index >= len(ts.pieces) {
		start := int64(index) * ts.pieceLength
		end := start + ts.pieceLength
		if end > ts.total {
			end = ts.total
		}
		return &s3Piece{ts: ts, index: index, start: start, end: end}
	}
	return ts.pieces[index]
}

// partsOverlapping returns the parts covering [start, end) in order.
func (ts *torrentStore) partsOverlapping(start, end int64) []*part {
	if len(ts.parts) == 0 {
		return nil
	}
	first := int(start / ts.partSize)
	if first < 0 {
		first = 0
	}
	last := int((end - 1) / ts.partSize)
	if last >= len(ts.parts) {
		last = len(ts.parts) - 1
	}
	if last < first {
		return nil
	}
	if ts.single {
		return ts.parts
	}
	return ts.parts[first : last+1]
}

// initPartPending seeds each idle part with the number of overlapping pieces
// that must be verified before the part becomes uploadable.
func (ts *torrentStore) initPartPending() {
	for _, p := range ts.pieces {
		for _, part := range ts.partsOverlapping(p.start, p.end) {
			part.mu.Lock()
			if part.state == partIdle {
				part.pending++
			}
			part.mu.Unlock()
		}
	}
}

// loadOrCreateRow resumes from (or creates) the persistence row and marks
// the parts already stored as part objects as uploaded.
func (ts *torrentStore) loadOrCreateRow() error {
	var row models.TorrentS3Upload
	err := db.DB.Where("info_hash = ?", ts.hash).First(&row).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = models.TorrentS3Upload{
			InfoHash:    ts.hash,
			ObjectKey:   ts.object,
			PartsPrefix: ts.partsPrefix,
			TotalLength: ts.total,
			PieceLength: ts.pieceLength,
			PartSize:    ts.partSize,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := db.DB.Create(&row).Error; err != nil {
			return fmt.Errorf("s3torrent: create upload row: %w", err)
		}
	case err != nil:
		return fmt.Errorf("s3torrent: load upload row: %w", err)
	default:
		if row.TotalLength != ts.total || (row.PartSize != 0 && row.PartSize != ts.partSize) {
			// Dimension drift (e.g. the part granularity changed across an
			// upgrade) — the staged layout is unusable, reset the row.
			logger.Warn("S3Torrent", "Upload row dimensions drifted — resetting upload state",
				"info_hash", ts.hash, "row_total", row.TotalLength, "torrent_total", ts.total)
			row.UploadID = ""
			row.UploadedParts = ""
			row.Finalized = false
			row.FinalizedAt = nil
			row.TotalLength = ts.total
			row.PieceLength = ts.pieceLength
			row.PartSize = ts.partSize
		}
	}

	ts.piecesMu.Lock()
	ts.uploadID = row.UploadID
	ts.finalized = row.Finalized
	ts.finalizedAt = row.FinalizedAt
	var uploaded []int
	if row.UploadedParts != "" {
		if jerr := json.Unmarshal([]byte(row.UploadedParts), &uploaded); jerr != nil {
			uploaded = nil
		}
	}
	for _, idx := range uploaded {
		ts.uploadedParts[idx] = true
	}
	// Pieces fully covered by stored parts are complete from the start: the
	// client will treat them as verified via Completion(), so they must
	// count toward the finalize trigger.
	for i, p := range ts.pieces {
		complete := ts.finalized
		if !complete {
			complete = true
			for _, part := range ts.partsOverlapping(p.start, p.end) {
				if !ts.uploadedParts[part.index] {
					complete = false
					break
				}
			}
		}
		if complete {
			ts.completedPieces[i] = true
		}
	}
	ts.piecesMu.Unlock()

	for idx := range ts.uploadedParts {
		if idx >= 0 && idx < len(ts.parts) {
			p := ts.parts[idx]
			p.mu.Lock()
			p.state = partUploaded
			p.mu.Unlock()
		}
	}
	ts.initPartPending()
	return nil
}

// persistRow writes the current in-memory upload state into the DB row.
// Callers must hold piecesMu for writing.
func (ts *torrentStore) persistRow() {
	idxs := make([]int, 0, len(ts.uploadedParts))
	for idx := range ts.uploadedParts {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	payloadStr := "[]"
	if payload, err := json.Marshal(idxs); err == nil {
		payloadStr = string(payload)
	}
	updates := map[string]interface{}{
		"uploaded_parts": payloadStr,
		"upload_id":      ts.uploadID,
		"finalized":      ts.finalized,
		"total_length":   ts.total,
		"piece_length":   ts.pieceLength,
		"part_size":      ts.partSize,
		"updated_at":     time.Now(),
	}
	if ts.finalizedAt != nil {
		updates["finalized_at"] = *ts.finalizedAt
	}
	db.DB.Model(&models.TorrentS3Upload{}).Where("info_hash = ?", ts.hash).Updates(updates)
}

// recordPartUploaded marks a staged part object confirmed in the bucket.
func (ts *torrentStore) recordPartUploaded(idx int) {
	ts.piecesMu.Lock()
	ts.uploadedParts[idx] = true
	ts.persistRow()
	ts.piecesMu.Unlock()
}

// pieceFullyUploaded reports whether every byte of the piece is already
// stored inside part objects in the bucket (resume pre-completion).
func (ts *torrentStore) pieceFullyUploaded(pieceIndex int, pieceStart, pieceEnd int64) bool {
	ts.piecesMu.RLock()
	defer ts.piecesMu.RUnlock()
	if ts.finalized {
		return true
	}
	for _, part := range ts.partsOverlapping(pieceStart, pieceEnd) {
		if !ts.uploadedParts[part.index] {
			return false
		}
	}
	return true
}

// isFinalized reports whether the final data object is committed.
func (ts *torrentStore) isFinalized() bool {
	ts.piecesMu.RLock()
	defer ts.piecesMu.RUnlock()
	return ts.finalized
}

// isClosed reports whether the store's workers have been stopped.
func (ts *torrentStore) isClosed() bool {
	select {
	case <-ts.stopCh:
		return true
	default:
		return false
	}
}

// Close stops the upload workers and releases resident RAM. Persisted state
// (DB row + part objects) is untouched, so the torrent resumes later.
func (ts *torrentStore) Close() {
	ts.closeOnce.Do(func() {
		close(ts.stopCh)
		ts.wg.Wait()
		for _, part := range ts.parts {
			part.mu.Lock()
			if part.buf != nil && part.state != partUploaded {
				releasePartBuf(part.length)
				part.buf = nil
			}
			part.mu.Unlock()
		}
		liveStores.Delete(ts.hash)
	})
}

// torrentImpl builds the storage.TorrentImpl handed to the torrent client.
func (ts *torrentStore) torrentImpl() storage.TorrentImpl {
	return storage.TorrentImpl{
		// PieceWithHash is preferred by the client; the hash argument is
		// irrelevant here because the client verifies pieces itself and
		// only then calls MarkComplete.
		PieceWithHash: func(p metainfo.Piece, _ g.Option[[]byte]) storage.PieceImpl {
			return ts.pieceFor(p.Index())
		},
		Close: func() error {
			ts.Close()
			return nil
		},
	}
}

// loadS3Prefix resolves the bucket key prefix from the storage config.
func loadS3Prefix() string {
	var cfg models.StorageConfig
	if err := db.DB.First(&cfg).Error; err == nil {
		return cfg.S3Prefix
	}
	return "clever-connect/"
}
