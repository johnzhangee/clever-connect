package s3store

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
)

// current returns the initialized store or nil (all methods handle a nil
// receiver by returning ErrNotConfigured).
func current() *Store {
	st, _ := Get()
	return st
}

// Enabled reports whether S3 storage is configured and ready.
func Enabled() bool {
	st, _ := Get()
	return st != nil
}

// --- Package-level convenience delegates ---

func UploadFile(ctx context.Context, localPath, key, contentType string) (bool, error) {
	return current().UploadFile(ctx, localPath, key, contentType)
}

func ObjectSize(ctx context.Context, key string) (int64, bool, error) {
	return current().StatObjectSize(ctx, key)
}

func OpenStream(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return current().OpenObjectStream(ctx, key)
}

func ReadRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, error) {
	return current().ReadObjectRange(ctx, key, start, end)
}

func DownloadToFile(ctx context.Context, key, destPath string) error {
	return current().DownloadToFile(ctx, key, destPath)
}

func DeleteObject(ctx context.Context, key string) error {
	return current().DeleteObject(ctx, key)
}

func DeletePrefix(ctx context.Context, prefix string) (int, error) {
	return current().DeletePrefix(ctx, prefix)
}

func SummarizePrefix(ctx context.Context, prefix string) (int, int64, error) {
	return current().SummarizePrefix(ctx, prefix)
}

func Ping(ctx context.Context) error {
	return current().Ping(ctx)
}

func BucketName() string {
	return current().BucketName()
}

// sanitizeRelPath splits a torrent-relative path into safe S3 key segments:
// backslashes become slashes and empty, "." and ".." segments (path
// traversal) are dropped, keeping keys inside the torrent subtree.
func sanitizeRelPath(relPath string) []string {
	clean := strings.ReplaceAll(relPath, "\\", "/")
	segments := make([]string, 0, 8)
	for _, seg := range strings.Split(clean, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		segments = append(segments, seg)
	}
	return segments
}

// KeyForTorrentFile builds the S3 object key for one torrent file:
//
//	<prefix>torrents/<infohash>/<sanitized relative path>
//
// The prefix (e.g. "clever-connect/") comes from StorageConfig. Path traversal
// segments (..) are stripped to keep keys safely inside the torrent subtree.
func KeyForTorrentFile(prefix, infoHash, relPath string) string {
	segments := sanitizeRelPath(relPath)
	prefix = strings.Trim(prefix, "/")
	joined := "torrents/" + infoHash + "/" + path.Join(segments...)
	if prefix != "" {
		joined = prefix + "/" + joined
	}
	return joined
}

// KeyForTorrentData builds the S3 object key that holds the whole torrent's
// byte stream in direct-to-S3 mode (one object assembled from multipart
// parts). It lives OUTSIDE the per-file "torrents/<infohash>/" subtree so
// wholesale file deletions never touch it.
func KeyForTorrentData(prefix, infoHash string) string {
	base := "torrents/" + infoHash + ".data"
	if p := strings.Trim(prefix, "/"); p != "" {
		return p + "/" + base
	}
	return base
}

// KeyForTorrentPartsPrefix returns the key prefix under which the direct-to-S3
// backend stages the per-part objects (".../<infohash>.parts/") before they
// are assembled into the final data object.
func KeyForTorrentPartsPrefix(prefix, infoHash string) string {
	base := "torrents/" + infoHash + ".parts/"
	if p := strings.Trim(prefix, "/"); p != "" {
		return p + "/" + base
	}
	return base
}

// KeyForTorrentPart builds the object key of one staged part (0-based index).
// The index is zero-padded so listing the prefix yields stable ordering.
func KeyForTorrentPart(prefix, infoHash string, partIndex int) string {
	return KeyForTorrentPartsPrefix(prefix, infoHash) + fmt.Sprintf("%010d", partIndex)
}

// TorrentPrefix returns the common S3 prefix holding all files of a torrent,
// which is deleted wholesale when a torrent job is deleted.
func TorrentPrefix(prefix, infoHash string) string {
	if p := strings.Trim(prefix, "/"); p != "" {
		return p + "/torrents/" + infoHash + "/"
	}
	return "torrents/" + infoHash + "/"
}
