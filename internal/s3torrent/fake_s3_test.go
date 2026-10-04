package s3torrent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

const (
	testPieceSize = 256 * 1024
	testPartSize  = 1024 * 1024 // 1 MiB
	testHash      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// fakeS3 is an in-memory object store with recorded multipart operations.
type fakeS3 struct {
	mu              sync.Mutex
	objects         map[string][]byte
	uploads         map[string]string
	copied          map[string][]fakeCopiedPart
	completed       map[string][]fakeCopiedPart
	aborted         []string
	deletedPrefixes []string
	deletedObjects  []string
	failPut         error
}

type fakeCopiedPart struct {
	number int
	source string
	length int64
	etag   string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{
		objects:   map[string][]byte{},
		uploads:   map[string]string{},
		copied:    map[string][]fakeCopiedPart{},
		completed: map[string][]fakeCopiedPart{},
	}
}

func (f *fakeS3) PutObjectBytes(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPut != nil {
		return f.failPut
	}
	f.objects[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeS3) ReadObjectRange(_ context.Context, key string, start, end int64) (io.ReadCloser, error) {
	f.mu.Lock()
	obj, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", key)
	}
	if end < 0 {
		end = int64(len(obj)) - 1
	}
	if start < 0 || start > end || start >= int64(len(obj)) {
		return nil, fmt.Errorf("range out of bounds: %s [%d,%d]", key, start, end)
	}
	return io.NopCloser(bytes.NewReader(obj[start : end+1])), nil
}

func (f *fakeS3) DeleteObject(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deletedObjects = append(f.deletedObjects, key)
	return nil
}

func (f *fakeS3) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedPrefixes = append(f.deletedPrefixes, prefix)
	removed := 0
	for key := range f.objects {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(f.objects, key)
			removed++
		}
	}
	return removed, nil
}

func (f *fakeS3) StartMultipartUpload(_ context.Context, object string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("upload-%d", len(f.uploads)+1)
	f.uploads[object] = id
	return id, nil
}
