package s3torrent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"clever-connect/internal/logger"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Client is the storage.ClientImpl installed as the torrent client's
// default storage. It dispatches by info hash: torrents registered with
// Route() are served by the direct-to-S3 backend (piece data lives in
// object storage, never on the local disk); every other torrent falls
// through to the wrapped default file storage, preserving stock behaviour.
type Client struct {
	mu         sync.Mutex
	routed     map[string]bool
	fallback   storage.ClientImpl
	onFinalize func(infoHash string)
}

// NewClient builds the dispatching storage. fallback is used for torrents
// that are not routed to S3 — pass the same file storage the client would
// have used by default.
func NewClient(fallback storage.ClientImpl) *Client {
	return &Client{
		routed:   make(map[string]bool),
		fallback: fallback,
	}
}

// Route sends every torrent with the given hex info hash through the
// direct-to-S3 backend. Must be called before the torrent is added to the
// client, because the client opens torrent storage as soon as metadata
// resolves.
func (c *Client) Route(infoHashHex string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.routed[infoHashHex] = true
	c.mu.Unlock()
}

// Unroute stops routing the hash. Already-stored data and its persistence
// row are left untouched, so a later re-add resumes the upload.
func (c *Client) Unroute(infoHashHex string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.routed, infoHashHex)
	c.mu.Unlock()
}

// Has reports whether the hash is routed to the direct-to-S3 backend.
func (c *Client) Has(infoHashHex string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.routed[infoHashHex]
}

// SetFinalizeNotifier installs the callback fired once a torrent's S3 data
// object is fully assembled. Invoked from a background goroutine; the
// torrent manager uses it to chain downstream per-file handling.
func (c *Client) SetFinalizeNotifier(f func(infoHash string)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.onFinalize = f
	c.mu.Unlock()
}

func (c *Client) notifyFinalize(infoHash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	f := c.onFinalize
	c.mu.Unlock()
	if f != nil {
		f(infoHash)
	}
}

// OpenTorrent implements storage.ClientImpl: routed hashes open the
// direct-to-S3 store, everything else uses the fallback storage.
//
// A routed hash NEVER falls back to the fallback (disk) storage: silently
// handing the torrent to local storage would refill the small instance disk
// — the exact failure direct mode exists to prevent. A failed S3 open fails
// the torrent instead (the client surfaces the error; re-adding resumes).
func (c *Client) OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	if c == nil {
		return storage.TorrentImpl{}, errors.New("s3torrent: nil dispatch client")
	}
	hex := infoHash.HexString()
	if c.Has(hex) {
		ts, err := openTorrentStore(c, info, hex)
		if err != nil {
			logger.Error("S3Torrent", "Direct-to-S3 open failed — refusing disk fallback, torrent errors out",
				"info_hash", hex, "error", err)
			return storage.TorrentImpl{}, fmt.Errorf("s3torrent: direct-to-S3 open failed for %s: %w", hex, err)
		}
		return ts.torrentImpl(), nil
	}
	if c.fallback != nil {
		return c.fallback.OpenTorrent(ctx, info, infoHash)
	}
	return storage.TorrentImpl{}, errors.New("s3torrent: no fallback storage configured")
}

var _ storage.ClientImpl = (*Client)(nil)
