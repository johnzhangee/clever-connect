package s3store

// multipart.go — low-level S3 multipart primitives exposed on top of the
// store's minio client. The direct-to-S3 torrent backend (internal/s3torrent)
// drives them to assemble one large S3 object out of RAM-buffered parts,
// without a single torrent byte ever touching the local disk.

import (
	"bytes"
	"context"

	"github.com/minio/minio-go/v7"
)

// Core returns a minio.Core handle on top of this store's client, exposing
// the low-level S3 primitives (multipart uploads, raw part operations) that
// the high-level Client API does not surface. Returns nil when the store is
// unconfigured.
func (s *Store) Core() *minio.Core {
	if s == nil || s.client == nil {
		return nil
	}
	return &minio.Core{Client: s.client}
}

// PutObjectBytes uploads one in-memory buffer as a single S3 object with a
// single PUT call. Used for the per-part staging objects (≤ 64 MiB) of the
// direct-to-S3 torrent backend.
func (s *Store) PutObjectBytes(ctx context.Context, key string, data []byte) error {
	if s == nil {
		return ErrNotConfigured
	}
	core := s.Core()
	if core == nil {
		return ErrNotConfigured
	}
	_, err := core.PutObject(ctx, s.cfg.Bucket, key, bytes.NewReader(data), int64(len(data)), "", "", minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	return err
}

// StartMultipartUpload initiates a multipart upload for object and returns
// the upload ID that part operations must reference.
func (s *Store) StartMultipartUpload(ctx context.Context, object string) (string, error) {
	if s == nil {
		return "", ErrNotConfigured
	}
	core := s.Core()
	if core == nil {
		return "", ErrNotConfigured
	}
	return core.NewMultipartUpload(ctx, s.cfg.Bucket, object, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
}

// AddMultipartPartByCopy creates multipart part number `partNumber` of
// dstObject by server-side copying the first `length` bytes of srcObject.
// The data moves inside the S3 service — no bytes flow through this host.
func (s *Store) AddMultipartPartByCopy(ctx context.Context, srcObject, dstObject, uploadID string, partNumber int, length int64) (minio.CompletePart, error) {
	if s == nil {
		return minio.CompletePart{}, ErrNotConfigured
	}
	core := s.Core()
	if core == nil {
		return minio.CompletePart{}, ErrNotConfigured
	}
	return core.CopyObjectPart(ctx, s.cfg.Bucket, srcObject, s.cfg.Bucket, dstObject, uploadID, partNumber, 0, length, nil)
}

// CompleteMultipartUpload commits the uploaded parts into the final object.
func (s *Store) CompleteMultipartUpload(ctx context.Context, object, uploadID string, parts []minio.CompletePart) error {
	if s == nil {
		return ErrNotConfigured
	}
	core := s.Core()
	if core == nil {
		return ErrNotConfigured
	}
	_, err := core.CompleteMultipartUpload(ctx, s.cfg.Bucket, object, uploadID, parts, minio.PutObjectOptions{})
	return err
}

// AbortMultipartUpload aborts an in-flight multipart upload, discarding any
// parts that were uploaded directly into it.
func (s *Store) AbortMultipartUpload(ctx context.Context, object, uploadID string) error {
	if s == nil {
		return ErrNotConfigured
	}
	core := s.Core()
	if core == nil {
		return ErrNotConfigured
	}
	return core.AbortMultipartUpload(ctx, s.cfg.Bucket, object, uploadID)
}
