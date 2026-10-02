package filecore

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"

	"clever-connect/internal/logger"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithymiddleware "github.com/aws/smithy-go/middleware"
)

// Multipart upload tuning for Clever Cloud Cellar (HTTP/1.1, real-SHA256 mode):
//
//   - 16 MiB parts: each concurrent worker holds one 16 MiB buffer in RAM.
//     At 8 workers (the floor), peak memory is 128 MiB — well within Clever
//     Cloud container limits.
//   - 8 minimum workers: fully saturates Cellar's multi-Gbps interconnect
//     without exhausting the container's memory budget.
//   - Buffer size matches part size so the SDK never allocates on the hot path.
//
// # Checksum / payload-hash strategy
//
// The manager.Uploader struct has its OWN RequestChecksumCalculation field
// that defaults to WhenSupported — it is independent of the S3 client's
// RequestChecksumCalculation setting.  When left at WhenSupported, the
// uploader auto-sets ChecksumAlgorithm=CRC32 on every UploadPartInput.
//
// That CRC32 algorithm triggers the SDK's ComputeInputPayloadChecksum
// middleware to take the *trailing-checksum* path for HTTPS requests:
// it wraps each part body in aws-chunked content-encoding with a CRC32
// trailer and sets x-amz-content-sha256 to
// "STREAMING-UNSIGNED-PAYLOAD-TRAILER".  Cellar / Ceph RGW does NOT support
// aws-chunked encoding for UploadPart: it computes the SHA256 of the raw
// received bytes (including the chunked-encoding framing) and compares
// it against the header value, producing a 400 XAmzContentSHA256Mismatch.
//
// Setting RequestChecksumCalculation=WhenRequired on the uploader prevents
// auto-setting ChecksumAlgorithm, so the trailing-checksum / aws-chunked
// path is never taken.
//
// Separately, the S3 operation middleware stack installs a
// dynamicPayloadSigningMiddleware (same ID as ComputePayloadSHA256) that
// sends "UNSIGNED-PAYLOAD" as x-amz-content-sha256 for HTTPS requests.
// Cellar does not honour UNSIGNED-PAYLOAD for authenticated (non-presigned)
// UploadPart requests and returns XAmzContentSHA256Mismatch.  We therefore
// swap that middleware back to the real ComputePayloadSHA256 so the SDK
// hashes the actual body bytes and sends the true digest.
const (
	defaultPartSize      = 16 * 1024 * 1024 // 16 MiB per part
	minimumConcurrency   = 8                // floor for single-core containers
	maxUploadConcurrency = 16               // ceiling keeping multi-file parallel archives memory-safe
	uploadBufPartSize    = 16 * 1024 * 1024 // must match defaultPartSize
)

// concurrencyForUpload scales the parallel part workers with available CPU,
// floored at minimumConcurrency so even single-core containers saturate the link
// (the bottleneck is always network, not CPU). The ceiling matters now that
// whole-file transfers execute one per CPU core (the scheduler file_to_s3
// fan-out and the torrent inline archiver): worker memory stays bounded at
// maxUploadConcurrency × 16 MiB = 256 MiB per in-flight file — about 3 GiB
// across 12 parallel archives on a 12-core host.
func concurrencyForUpload() int {
	c := runtime.GOMAXPROCS(0)
	if c < minimumConcurrency {
		c = minimumConcurrency
	}
	if c > maxUploadConcurrency {
		c = maxUploadConcurrency
	}
	return c
}

// newUploader builds a freshly-configured parallel multipart uploader bound to
// the active storage engine. A new uploader per call keeps configuration simple
// and lets the AWS SDK manage its own goroutine pool lifetime.
func (e *StorageEngine) newUploader() *manager.Uploader {
	return manager.NewUploader(e.S3Client, func(u *manager.Uploader) {
		u.PartSize = defaultPartSize
		u.Concurrency = concurrencyForUpload()

		// Prevent the uploader from auto-setting ChecksumAlgorithm=CRC32.
		// The Uploader struct has its own RequestChecksumCalculation field
		// (defaults to WhenSupported) that is independent of the S3 client's
		// setting.  WhenSupported makes the uploader set CRC32 on every part,
		// which triggers aws-chunked trailing-checksum encoding that Cellar
		// rejects with XAmzContentSHA256Mismatch.
		u.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired

		// Pre-allocated seekable buffers matching the part size cut GC pressure
		// and allocation latency while parts are shuffled to S3 in parallel.
		u.BufferProvider = manager.NewBufferedReadSeekerWriteToPool(uploadBufPartSize)

		// Force real SHA256 payload hashing for every S3 operation the uploader
		// performs (UploadPart, PutObject, CreateMultipartUpload, …).
		//
		// The SDK installs dynamicPayloadSigningMiddleware (same middleware ID
		// as ComputePayloadSHA256) which sends "UNSIGNED-PAYLOAD" as
		// x-amz-content-sha256 for HTTPS.  Cellar does not honour
		// UNSIGNED-PAYLOAD for authenticated UploadPart and returns
		// XAmzContentSHA256Mismatch.  Swapping it back to ComputePayloadSHA256
		// makes the SDK hash the real body bytes and send the true digest.
		//
		// Swap is a no-op for operations that already use ComputePayloadSHA256
		// (CreateMultipartUpload, CompleteMultipartUpload, AbortMultipartUpload);
		// it only affects operations where UseDynamicPayloadSigningMiddleware
		// replaced it (UploadPart, PutObject).
		u.ClientOptions = append(u.ClientOptions, func(o *s3.Options) {
			o.APIOptions = append(o.APIOptions, func(stack *smithymiddleware.Stack) error {
				_, _ = stack.Finalize.Swap(
					(*v4.ComputePayloadSHA256)(nil).ID(),
					&v4.ComputePayloadSHA256{},
				)
				return nil
			})
		})
	})
}

// UploadStreamToS3 streams an io.Reader straight into S3 using parallel
// multipart upload. This is the hot path for the downloader: the HTTP source
// body is piped concurrently to the object store without touching local disk.
//
// Returns the object key on success. It is a no-op (returns "") when S3 is
// disabled so callers can branch freely without extra guards.
func UploadStreamToS3(ctx context.Context, key, mimeType string, body io.Reader) (string, error) {
	if engine == nil {
		return "", nil
	}
	if key == "" {
		return "", errors.New("empty object key")
	}

	contentType := mimeType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	uploader := engine.newUploader()
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(engine.BucketName),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// UploadFileToS3 uploads an already-on-disk file to S3 via the parallel
// multipart uploader and returns the object key. Used by the local-disk
// downloaders (torrent / youtube / spotify / telegram / manual upload) to push
// completed files into object storage after processing.
func UploadFileToS3(ctx context.Context, key, mimeType, localPath string) (string, error) {
	if engine == nil {
		return "", nil
	}
	if key == "" {
		return "", errors.New("empty object key")
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	contentType := mimeType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	uploader := engine.newUploader()
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(engine.BucketName),
		Key:         aws.String(key),
		Body:        f,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// DeleteFromS3 removes an object from the bucket. Safe to call with an empty
// key or when S3 is disabled.
func DeleteFromS3(ctx context.Context, key string) error {
	if engine == nil || key == "" {
		return nil
	}
	_, err := engine.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(engine.BucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		logger.Warn("FileCore", "Failed to delete S3 object", "key", key, "error", err)
	}
	return err
}

// GetObject opens a streaming download from S3 for a key, optionally applying
// a byte-range ("bytes=0-1048576"). The returned body MUST be closed by the
// caller. Returns (nil, nil) when S3 is disabled so callers can fall back.
func GetObject(ctx context.Context, key, rangeHeader string) (*s3.GetObjectOutput, error) {
	if engine == nil {
		return nil, nil
	}
	if key == "" {
		return nil, errors.New("empty object key")
	}
	in := &s3.GetObjectInput{
		Bucket: aws.String(engine.BucketName),
		Key:    aws.String(key),
	}
	if rangeHeader != "" {
		in.Range = aws.String(rangeHeader)
	}
	return engine.S3Client.GetObject(ctx, in)
}

// HeadObject returns object metadata (size, content-type, …) without
// downloading the body. Returns (nil, nil) when S3 is disabled.
func HeadObject(ctx context.Context, key string) (*s3.HeadObjectOutput, error) {
	if engine == nil {
		return nil, nil
	}
	if key == "" {
		return nil, errors.New("empty object key")
	}
	return engine.S3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(engine.BucketName),
		Key:    aws.String(key),
	})
}
