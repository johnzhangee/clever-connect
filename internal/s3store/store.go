package s3store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"clever-connect/internal/logger"
)

// ErrNotConfigured is returned when S3 offloading is not set up.
var ErrNotConfigured = errors.New("s3store: no S3 configuration available")

// Store wraps a minio-go client with the resolved Cellar configuration.
type Store struct {
	client *minio.Client
	cfg    Config
}

var (
	storeOnce sync.Once
	store     *Store
	storeErr  error
)

// Init discovers the S3 configuration and prepares the client. It is safe to
// call multiple times; the first call wins. When no configuration exists it
// returns ErrNotConfigured (and the app continues with the feature disabled).
func Init() error {
	storeOnce.Do(func() {
		cfg := LoadDiscoveryConfig()
		if cfg == nil {
			storeErr = ErrNotConfigured
			LogFailedDiscovery()
			return
		}
		client, err := minio.New(cfg.Endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
			Secure: cfg.Secure,
			Region: cfg.Region,
			// Cellar handles path-style addressing and does not support
			// wildcard virtual-host bucket addressing in all setups.
			BucketLookup: minio.BucketLookupPath,
			Transport:    s3Transport(),
		})
		if err != nil {
			storeErr = fmt.Errorf("s3store: create client: %w", err)
			logger.Error("S3Store", "Failed to create S3 client", "error", storeErr)
			return
		}

		bucket, err := resolveBucket(context.Background(), client, *cfg)
		if err != nil {
			storeErr = err
			logger.Error("S3Store", "Failed to resolve S3 bucket", "error", storeErr)
			return
		}
		cfg.Bucket = bucket

		store = &Store{client: client, cfg: *cfg}
		logger.Info("S3Store", "S3 storage initialized", "config", store.cfg.String())
	})
	return storeErr
}

// s3Transport returns a reuse-safe HTTP transport with sane timeouts for
// large uploads/downloads over long-lived streaming connections.
func s3Transport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// resolveBucket verifies the configured/derived bucket exists. If the bucket
// name is unknown (e.g. only CELLAR_ADDON_* env vars are set) it lists the
// account buckets and auto-selects when exactly one exists.
func resolveBucket(ctx context.Context, client *minio.Client, cfg Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if cfg.Bucket != "" {
		exists, err := client.BucketExists(ctx, cfg.Bucket)
		if err != nil {
			return "", fmt.Errorf("s3store: checking bucket %q: %w", cfg.Bucket, err)
		}
		if !exists {
			return "", fmt.Errorf("s3store: bucket %q does not exist on %s", cfg.Bucket, cfg.Endpoint)
		}
		return cfg.Bucket, nil
	}

	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		return "", fmt.Errorf("s3store: listing buckets: %w", err)
	}
	switch len(buckets) {
	case 1:
		logger.Info("S3Store", "Auto-selected single Cellar bucket", "bucket", buckets[0].Name)
		return buckets[0].Name, nil
	case 0:
		return "", fmt.Errorf("s3store: account has no buckets — create one in the Clever Cellar console")
	default:
		names := make([]string, len(buckets))
		for i, b := range buckets {
			names[i] = b.Name
		}
		return "", fmt.Errorf("s3store: multiple buckets exist (%s) and no bucket name was provided — set S3_BUCKET", strings.Join(names, ", "))
	}
}

// Get returns the initialized store, or ErrNotConfigured.
func Get() (*Store, error) {
	if store == nil {
		return nil, ErrNotConfigured
	}
	return store, nil
}

// BucketName returns the bucket this store operates on ("" if unconfigured).
func (s *Store) BucketName() string {
	if s == nil {
		return ""
	}
	return s.cfg.Bucket
}

// Config returns a copy of the resolved configuration.
func (s *Store) Config() Config {
	if s == nil {
		return Config{}
	}
	return s.cfg
}

// Ping verifies connectivity with a cheap bucket listing.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	objCh := s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{MaxKeys: 1})
	select {
	case _, ok := <-objCh:
		if !ok {
			return nil // empty bucket is still a successful listing
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
