package s3torrent

import (
	"context"
	"errors"
	"io"

	"clever-connect/internal/s3store"

	"github.com/minio/minio-go/v7"
)

// ErrNoS3 is returned when the S3 API is needed but no bucket is configured.
var ErrNoS3 = errors.New("s3torrent: S3 storage not configured")

// CompletePart is one committed multipart part (S3 part number + ETag).
type CompletePart struct {
	PartNumber int
	ETag       string
}

// s3API is the object-storage surface the direct-to-S3 backend needs.
// Keeping it an interface makes the aggregation, resume and finalize logic
// unit-testable against an in-memory fake.
type s3API interface {
	// PutObjectBytes uploads one RAM buffer as a single object (part staging).
	PutObjectBytes(ctx context.Context, key string, data []byte) error
	// ReadObjectRange returns a reader for [start, end] inclusive; end < 0
	// reads through the end of the object.
	ReadObjectRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, error)
	// DeleteObject removes one object.
	DeleteObject(ctx context.Context, key string) error
	// DeletePrefix removes every object under the prefix.
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	// StartMultipartUpload initiates a multipart upload and returns its ID.
	StartMultipartUpload(ctx context.Context, object string) (string, error)
	// CopyObjectPart server-side copies the first `length` bytes of srcObject
	// into part `partNumber` of dstObject's upload and returns the part ETag.
	CopyObjectPart(ctx context.Context, srcObject, dstObject, uploadID string, partNumber int, length int64) (string, error)
	// CompleteMultipartUpload commits the parts into the final object.
	CompleteMultipartUpload(ctx context.Context, object, uploadID string, parts []CompletePart) error
	// AbortMultipartUpload discards an in-flight multipart upload.
	AbortMultipartUpload(ctx context.Context, object, uploadID string) error
}

// s3StoreAPI adapts the s3store.Store to the s3API surface. A nil store
// (unconfigured bucket) makes every call return ErrNoS3.
type s3StoreAPI struct{ store *s3store.Store }

// newS3API wraps the store; nil store yields a nil API (callers guard).
func newS3API(store *s3store.Store) s3API {
	if store == nil {
		return nil
	}
	return s3StoreAPI{store: store}
}

func (a s3StoreAPI) PutObjectBytes(ctx context.Context, key string, data []byte) error {
	return a.store.PutObjectBytes(ctx, key, data)
}

func (a s3StoreAPI) ReadObjectRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, error) {
	return a.store.ReadObjectRange(ctx, key, start, end)
}

func (a s3StoreAPI) DeleteObject(ctx context.Context, key string) error {
	return a.store.DeleteObject(ctx, key)
}

func (a s3StoreAPI) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	return a.store.DeletePrefix(ctx, prefix)
}

func (a s3StoreAPI) StartMultipartUpload(ctx context.Context, object string) (string, error) {
	return a.store.StartMultipartUpload(ctx, object)
}

func (a s3StoreAPI) CopyObjectPart(ctx context.Context, srcObject, dstObject, uploadID string, partNumber int, length int64) (string, error) {
	cp, err := a.store.AddMultipartPartByCopy(ctx, srcObject, dstObject, uploadID, partNumber, length)
	if err != nil {
		return "", err
	}
	return cp.ETag, nil
}

func (a s3StoreAPI) CompleteMultipartUpload(ctx context.Context, object, uploadID string, parts []CompletePart) error {
	mp := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		mp[i] = minio.CompletePart{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	return a.store.CompleteMultipartUpload(ctx, object, uploadID, mp)
}

func (a s3StoreAPI) AbortMultipartUpload(ctx context.Context, object, uploadID string) error {
	return a.store.AbortMultipartUpload(ctx, object, uploadID)
}
