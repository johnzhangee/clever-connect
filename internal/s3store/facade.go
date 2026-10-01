package s3store

import (
	"context"
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

// KeyForTorrentFile builds the S3 object key for one torrent file:
//
//	<prefix>torrents/<infohash>/<sanitized relative path>
//
// The prefix (e.g. "clever-connect/") comes from StorageConfig. Path traversal
// segments (..) are stripped to keep keys safely inside the torrent subtree.
func KeyForTorrentFile(prefix, infoHash, relPath string) string {
	clean := strings.ReplaceAll(relPath, "\\", "/")
	segments := make([]string, 0, 8)
	for _, seg := range strings.Split(clean, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		segments = append(segments, seg)
	}
	prefix = strings.Trim(prefix, "/")
	joined := "torrents/" + infoHash + "/" + path.Join(segments...)
	if prefix != "" {
		joined = prefix + "/" + joined
	}
	return joined
}

// TorrentPrefix returns the common S3 prefix holding all files of a torrent,
// which is deleted wholesale when a torrent job is deleted.
func TorrentPrefix(prefix, infoHash string) string {
	if p := strings.Trim(prefix, "/"); p != "" {
		return p + "/torrents/" + infoHash + "/"
	}
	return "torrents/" + infoHash + "/"
}
