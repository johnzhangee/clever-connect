package s3store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
)

const (
	uploadPartSize    = 64 << 20 // 64 MiB multipart parts
	uploadConcurrency = 4        // parallel part uploads
)

// IsNotFound reports whether the error is an S3 "object does not exist" error.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject" || resp.Code == "NotFound"
	}
	return false
}

// UploadFile streams a local file to S3 under `key` using an automatic
// resumable multipart upload (minio-go handles part retry internally).
// It is idempotent: when the object already exists with the exact same size,
// nothing is uploaded and (false, nil) is returned.
func (s *Store) UploadFile(ctx context.Context, localPath, key, contentType string) (bool, error) {
	if s == nil {
		return false, ErrNotConfigured
	}
	st, err := os.Stat(localPath)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", localPath, err)
	}
	key, err = normalizeKeyWithFallback(localPath, key)
	if err != nil {
		return false, err
	}
	// S3 objects are atomic: an object either exists (complete) or not, so a
	// matching size means a previously successful upload we must not repeat.
	if o, exists, serr := s.StatObjectSize(ctx, key); serr == nil && exists && o == st.Size() {
		return false, nil
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	f, err := os.Open(localPath)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()
	_, err = s.client.PutObject(ctx, s.cfg.Bucket, key, f, st.Size(), minio.PutObjectOptions{
		PartSize:              uploadPartSize,
		ConcurrentStreamParts: true, // buffer + upload parts in parallel
		NumThreads:            uploadConcurrency,
		ContentType:           contentType,
		DisableMultipart:      false,
	})
	if err != nil {
		return false, fmt.Errorf("put %q: %w", key, err)
	}
	return true, nil
}

// normalizeKeyWithFallback ensures a key is never empty or leading-slash
// rooted; falls back to the local basename when omitted.
func normalizeKeyWithFallback(localPath, key string) (string, error) {
	if key = strings.Trim(strings.TrimSpace(key), "/"); key != "" {
		return key, nil
	}
	if base := filepath.Base(localPath); base != "." && base != "/" {
		return base, nil
	}
	return "", errors.New("s3store: object key required")
}

// StatObjectSize returns the object's size, or ok=false without error when
// the object is absent.
func (s *Store) StatObjectSize(ctx context.Context, key string) (int64, bool, error) {
	if s == nil {
		return 0, false, ErrNotConfigured
	}
	info, err := s.client.StatObject(ctx, s.cfg.Bucket, strings.Trim(key, "/"), minio.StatObjectOptions{})
	if err != nil {
		if IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return info.Size, true, nil
}

// OpenObjectStream returns a streaming reader over the whole object plus its
// total size. The caller must Close the reader.
func (s *Store) OpenObjectStream(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if s == nil {
		return nil, 0, ErrNotConfigured
	}
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, strings.Trim(key, "/"), minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, NormalizeGetErr(err)
	}
	// Stat() issues the underlying GET and surfaces HTTP errors immediately
	// (GetObject is lazy; errors would otherwise appear on first Read).
	st, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, 0, NormalizeGetErr(err)
	}
	return obj, st.Size, nil
}

// ReadObjectRange returns a reader for [start, end] inclusive of the object
// (end < 0 means "through end of object").
func (s *Store) ReadObjectRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, error) {
	if s == nil {
		return nil, ErrNotConfigured
	}
	if start < 0 {
		return nil, errors.New("s3store: negative range start")
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(start, end); err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, strings.Trim(key, "/"), opts)
	if err != nil {
		return nil, NormalizeGetErr(err)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, NormalizeGetErr(err)
	}
	return obj, nil
}
