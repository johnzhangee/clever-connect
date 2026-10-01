package storageguard

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"clever-connect/internal/db"
	"clever-connect/internal/models"
	"clever-connect/internal/s3store"
)

// S3KeyForLocalPath resolves the Cellar object key backing a local file path,
// consulting the file registry first, then the offload ledger.
func S3KeyForLocalPath(path string) (key string, size int64, ok bool) {
	path = filepath.Clean(path)

	var reg models.FileRegistry
	if err := db.DB.Where("file_path = ?", path).First(&reg).Error; err == nil &&
		reg.InS3 && reg.S3Key != "" {
		return reg.S3Key, reg.FileSize, true
	}

	var row models.TorrentFileOffload
	if err := db.DB.Where("file_path = ?", path).First(&row).Error; err == nil &&
		row.Uploaded && row.S3Key != "" {
		return row.S3Key, row.Size, true
	}
	return "", 0, false
}

// HasRemoteCopy reports whether a local path has an S3-confirmed backup.
func HasRemoteCopy(path string) bool {
	_, _, ok := S3KeyForLocalPath(path)
	return ok
}

// s3FileReader adapts a Cellar object to an io.ReadSeekCloser so the Files
// panel can http.ServeContent evicted files (HTTP Range = sparse re-GETs).
type s3FileReader struct {
	key         string
	size        int64
	pos         int64
	body        io.ReadCloser // open stream covering [streamStart, size)
	streamStart int64
}

// OpenS3Reader returns a seeking reader over the S3 object backing path.
func OpenS3Reader(path string) (io.ReadSeekCloser, int64, error) {
	key, size, ok := S3KeyForLocalPath(path)
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	return &s3FileReader{key: key, size: size}, size, nil
}

func (r *s3FileReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		body, err := s3store.ReadRange(context.Background(), r.key, r.pos, -1)
		if err != nil {
			return 0, err
		}
		r.body = body
		r.streamStart = r.pos
	}
	n, err := r.body.Read(p)
	r.pos += int64(n)
	if err == io.EOF {
		r.closeBody()
	}
	return n, err
}

// Seek re-opens a fresh range stream when jumping backwards or forwards.
func (r *s3FileReader) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = r.pos + offset
	case io.SeekEnd:
		target = r.size + offset
	}
	if target < 0 {
		return r.pos, os.ErrInvalid
	}
	if target == r.pos {
		return r.pos, nil
	}
	r.closeBody()
	r.pos = target
	if target > r.size {
		r.pos = r.size
	}
	return r.pos, nil
}

func (r *s3FileReader) closeBody() {
	if r.body != nil {
		_ = r.body.Close()
		r.body = nil
	}
}

func (r *s3FileReader) Close() error {
	r.closeBody()
	return nil
}
