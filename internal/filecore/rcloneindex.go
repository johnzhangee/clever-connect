// rcloneindex.go — the cloud-storage recovery tier of the upload resolver.
//
// When a file is neither on the ephemeral disk nor in the S3 archive, but an
// indexed object on a configured rclone remote (S3, Google Drive, SFTP, ...)
// matches it, the content is streamed back through `rclone cat` into a temp
// file. The caller deletes it via the returned cleanup as soon as the upload
// finished — the transient copy never outlives its job.
package filecore

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"clever-connect/internal/logger"
	"clever-connect/internal/rclone"
)

// cloudMaterializeCtxTimeout bounds one `rclone cat` rehydration. rclone
// aborts stalled transfers on its own IO timeout, so this cap only guards
// against a wedged process; it is deliberately generous because a single
// `cat` stream cannot be parallelized like the S3 multipart tier.
const cloudMaterializeCtxTimeout = 60 * time.Minute

// relativeToManagerRoot maps an absolute sandbox path to its clean relative
// form ("downloads/movies/a.mp4"), or "" when it lies outside the sandbox.
// It uses the same absolute base convention as GetAbsolutePath.
func relativeToManagerRoot(absPath string) string {
	absBase, err := filepath.Abs("./data/manager")
	if err != nil {
		absBase = "./data/manager"
	}
	rel, relErr := filepath.Rel(absBase, absPath)
	if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// tryRcloneIndex recovers a file that lives on a configured cloud remote.
// Candidates are resolved from the provider file index by exact
// manager-relative path first, then by basename, and the first one whose
// `rclone cat` yields real content wins.
//
// Returns:
//   - (path, cleanup, true, nil)  on success.
//   - (nil, nil, false, nil)      when no candidate exists (fall through).
//   - (nil, nil, false, err)      when candidates existed but every fetch
//     failed — the error is propagated so the job surfaces the real cause
//     instead of a misleading "not archived in S3".
func tryRcloneIndex(absPath string) (string, func(), bool, error) {
	basename := filepath.Base(absPath)
	if basename == "" || basename == "." || basename == string(filepath.Separator) {
		return "", nil, false, nil
	}
	candidates := rclone.FindIndexedFileCandidates(relativeToManagerRoot(absPath), basename)
	if len(candidates) == 0 {
		return "", nil, false, nil
	}

	var lastErr error
	for i := range candidates {
		cand := candidates[i]
		ctx, cancel := context.WithTimeout(context.Background(), cloudMaterializeCtxTimeout)
		p, cleanup, err := rclone.MaterializeRemoteFile(ctx, &cand.Remote, &cand.File)
		cancel()
		if err == nil {
			logger.Info("FileCore", "Materialized file from cloud storage (rclone index)",
				"path", absPath, "remote", cand.Remote.Name, "index_path", cand.File.Path)
			return p, cleanup, true, nil
		}
		lastErr = err
		logger.Warn("FileCore", "Cloud storage candidate failed — trying next candidate",
			"path", absPath, "remote", cand.Remote.Name, "error", err)
	}
	return "", nil, false, fmt.Errorf(
		"file exists in the cloud-storage index but could not be fetched (path=%s): %w", absPath, lastErr)
}
