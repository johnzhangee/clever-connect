package s3store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"

	"clever-connect/internal/logger"
)

// NormalizeGetErr converts S3 "not found" errors to os.ErrNotExist so callers
// can use errors.Is(err, os.ErrNotExist).
func NormalizeGetErr(err error) error {
	if err != nil && IsNotFound(err) {
		return os.ErrNotExist
	}
	return err
}

// DownloadToFile downloads an object to destPath (creating parent dirs) and
// verifies the landed file size.
func (s *Store) DownloadToFile(ctx context.Context, key, destPath string) error {
	if s == nil {
		return ErrNotConfigured
	}
	size, exists, err := s.StatObjectSize(ctx, key)
	if err != nil {
		return err
	}
	if !exists {
		return os.ErrNotExist
	}
	if dir := filepath.Dir(destPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := s.client.FGetObject(ctx, s.cfg.Bucket, strings.Trim(key, "/"), destPath, minio.GetObjectOptions{}); err != nil {
		return NormalizeGetErr(err)
	}
	got, err := os.Stat(destPath)
	if err != nil {
		return err
	}
	if got.Size() != size {
		return errors.New("s3store: size mismatch after download")
	}
	return nil
}

// DeleteObject removes a single object (no error when already gone).
func (s *Store) DeleteObject(ctx context.Context, key string) error {
	if s == nil {
		return ErrNotConfigured
	}
	if err := s.client.RemoveObject(ctx, s.cfg.Bucket, strings.Trim(key, "/"), minio.RemoveObjectOptions{}); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

// DeletePrefix permanently removes every object under a key prefix and
// returns the number of objects deleted. Used when a torrent is deleted.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if s == nil {
		return 0, ErrNotConfigured
	}
	prefix = strings.Trim(prefix, "/")
	objectsCh := make(chan minio.ObjectInfo, 64)
	count := 0
	var listErr error
	go func() {
		defer close(objectsCh)
		for object := range s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if object.Err != nil {
				listErr = object.Err
				return
			}
			count++
			objectsCh <- object
		}
	}()
	for rErr := range s.client.RemoveObjects(ctx, s.cfg.Bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		if rErr.Err != nil && !IsNotFound(rErr.Err) {
			listErr = rErr.Err
		}
	}
	if listErr != nil {
		return count, listErr
	}
	logger.Info("S3Store", "Deleted objects under prefix", "prefix", prefix, "count", count)
	return count, nil
}

// SummarizePrefix returns object count and total bytes under a prefix.
func (s *Store) SummarizePrefix(ctx context.Context, prefix string) (objects int, totalBytes int64, err error) {
	if s == nil {
		return 0, 0, ErrNotConfigured
	}
	prefix = strings.Trim(prefix, "/")
	for object := range s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return objects, totalBytes, object.Err
		}
		objects++
		totalBytes += object.Size
	}
	return objects, totalBytes, nil
}
