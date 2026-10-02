// index.go — provider content reconciliation: mirrors the objects that exist
// on a cloud storage remote into the rclone_files database table so files stay
// visible and accessible in the UI across app restarts.
//
// The remote (S3 bucket, Drive folder, SFTP dir, ...) is always the source of
// truth. Each sync performs one recursive `lsjson` listing and reconciles the
// table inside a transaction: new objects are created, changed metadata is
// updated, vanished objects are removed. A failed listing never touches the
// existing rows, so a temporary provider outage degrades to a stale-but-
// visible index instead of data loss.
package rclone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"

	"gorm.io/gorm"
)

// syncLocks serializes reconciliation per remote so overlapping triggers
// (startup sweep + manual admin sync) cannot interleave their transactions.
var (
	syncLocksMu sync.Mutex
	syncLocks   = map[uint]*sync.Mutex{}
)

func lockRemoteSync(remoteID uint) func() {
	syncLocksMu.Lock()
	mu, ok := syncLocks[remoteID]
	if !ok {
		mu = &sync.Mutex{}
		syncLocks[remoteID] = mu
	}
	syncLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// RelativeRemotePath derives the provider path relative to the remote's
// RootPrefix from a fully qualified "name:root/dir/file" path. It is the exact
// inverse of joinRemotePath and powers round-trips between the upload ledger
// and the file index.
func RelativeRemotePath(remote *models.RcloneRemote, fullPath string) string {
	s := strings.TrimPrefix(fullPath, remote.Name+":")
	root := strings.Trim(strings.ReplaceAll(strings.TrimSpace(remote.RootPrefix), "\\", "/"), "/")
	if root == "" {
		return strings.Trim(s, "/")
	}
	if s == root {
		return ""
	}
	if strings.HasPrefix(s, root+"/") {
		s = s[len(root)+1:]
	}
	return strings.Trim(s, "/")
}

// FileFullPath rebuilds the fully qualified rclone path of an indexed file.
func FileFullPath(remote *models.RcloneRemote, file *models.RcloneFile) string {
	return joinRemotePath(remote, file.Path)
}

// pathHashOf is the stable hash used for the unique (remote_id, path)
// identity of indexed objects.
func pathHashOf(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

// parseRemoteTime accepts the ISO-ish ModTime shapes rclone lsjson emits and
// normalizes rclone's zero time to nil.
func parseRemoteTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0001-01-01") {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// equalModTime compares two parsed provider timestamps at second granularity
// (sub-second noise between listings must not look like a change).
func equalModTime(a, b *time.Time) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return a.UTC().Truncate(time.Second).Equal(b.UTC().Truncate(time.Second))
}

// SyncReport summarizes one reconciliation run.
type SyncReport struct {
	RemoteName string    `json:"remote_name"`
	FilesSeen  int       `json:"files_seen"`
	DirsSeen   int       `json:"dirs_seen"`
	Created    int       `json:"created"`
	Updated    int       `json:"updated"`
	Removed    int       `json:"removed"`
	SyncedAt   time.Time `json:"synced_at"`
}

// listAllRemoteObjects runs one recursive lsjson with a caller-governed
// deadline: unlike ListRemoteDir (60s hard cap) large buckets may take
// minutes to enumerate.
func listAllRemoteObjects(ctx context.Context, remote *models.RcloneRemote) ([]RemoteEntry, error) {
	target := joinRemotePath(remote, "")
	env, redact, envErr := mkEnv(remote, nil)
	if envErr != nil {
		return nil, envErr
	}
	res, err := Run(ctx, env, redact, 0, "lsjson", target, "--recursive")
	if err != nil {
		return nil, err
	}
	var entries []RemoteEntry
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return entries, nil
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("failed to parse remote listing: %w", err)
	}
	return entries, nil
}

// SyncRemoteFiles mirrors the provider's current contents into rclone_files.
// The listing runs outside the transaction; the reconcile itself is atomic.
func SyncRemoteFiles(ctx context.Context, remote *models.RcloneRemote) (*SyncReport, error) {
	unlock := lockRemoteSync(remote.ID)
	defer unlock()

	if _, err := EnsureBinary(ctx); err != nil {
		return nil, err
	}

	entries, err := listAllRemoteObjects(ctx, remote)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	report := &SyncReport{RemoteName: remote.Name, SyncedAt: now}

	err = db.DB.Transaction(func(tx *gorm.DB) error {
		var existing []models.RcloneFile
		if err := tx.Where("remote_id = ?", remote.ID).Find(&existing).Error; err != nil {
			return err
		}
		existingByPath := make(map[string]models.RcloneFile, len(existing))
		for _, f := range existing {
			existingByPath[f.Path] = f
		}

		seen := make(map[string]bool, len(entries))
		for _, e := range entries {
			rel := strings.Trim(e.Path, "/")
			if rel == "" {
				rel = strings.Trim(e.Name, "/")
			}
			if rel == "" {
				continue
			}
			if e.IsDir {
				report.DirsSeen++
			} else {
				report.FilesSeen++
			}
			seen[rel] = true

			name := e.Name
			if name == "" {
				name = rel
				if i := strings.LastIndex(name, "/"); i >= 0 {
					name = name[i+1:]
				}
			}
			modTime := parseRemoteTime(e.ModTime)

			if prev, ok := existingByPath[rel]; ok {
				changed := prev.IsDir != e.IsDir || prev.Size != e.Size ||
					prev.MimeType != e.MimeType || prev.ProviderFileID != e.ID ||
					!equalModTime(prev.ModTime, modTime)
				if changed {
					if err := tx.Model(&models.RcloneFile{}).Where("id = ?", prev.ID).Updates(map[string]interface{}{
						"name": name, "is_dir": e.IsDir, "size": e.Size,
						"mime_type": e.MimeType, "mod_time": modTime,
						"provider_file_id": e.ID, "last_seen_at": now, "synced_at": now,
					}).Error; err != nil {
						return err
					}
					report.Updated++
				} else {
					if err := tx.Model(&models.RcloneFile{}).Where("id = ?", prev.ID).Updates(map[string]interface{}{
						"last_seen_at": now, "synced_at": now,
					}).Error; err != nil {
						return err
					}
				}
				continue
			}

			row := models.RcloneFile{
				RemoteID:       remote.ID,
				Path:           rel,
				PathHash:       pathHashOf(rel),
				Name:           name,
				IsDir:          e.IsDir,
				Size:           e.Size,
				MimeType:       e.MimeType,
				ModTime:        modTime,
				ProviderFileID: e.ID,
				LastSeenAt:     now,
				SyncedAt:       now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			report.Created++
		}

		// Remove rows whose objects vanished from the provider.
		var gone []uint
		for path, prev := range existingByPath {
			if !seen[path] {
				gone = append(gone, prev.ID)
			}
		}
		if len(gone) > 0 {
			if err := tx.Where("id IN ?", gone).Delete(&models.RcloneFile{}).Error; err != nil {
				return err
			}
			report.Removed = len(gone)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed reconciling provider file index: %w", err)
	}
	return report, nil
}

// UpsertRemoteFile inserts or refreshes one indexed object row — called right
// after a successful upload so the file is immediately visible in the Files
// view without waiting for the next full sync. Returns the persisted row (nil
// on failure; indexing problems never fail the upload).
func UpsertRemoteFile(remote *models.RcloneRemote, fullPath string, entry *RemoteEntry) *models.RcloneFile {
	rel := RelativeRemotePath(remote, fullPath)
	if rel == "" {
		return nil
	}

	now := time.Now()
	var name string
	var isDir bool
	var size int64
	var mimeType, providerID string
	var modTime *time.Time
	if entry != nil {
		name, isDir, size = entry.Name, entry.IsDir, entry.Size
		mimeType, providerID = entry.MimeType, entry.ID
		modTime = parseRemoteTime(entry.ModTime)
	}
	if name == "" {
		name = rel
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
	}

	var row models.RcloneFile
	found := db.DB.Where("remote_id = ? AND path_hash = ?", remote.ID, pathHashOf(rel)).First(&row).Error == nil
	row.RemoteID = remote.ID
	row.Path = rel
	row.PathHash = pathHashOf(rel)
	row.Name = name
	row.IsDir = isDir
	row.Size = size
	row.MimeType = mimeType
	row.ModTime = modTime
	row.ProviderFileID = providerID
	row.LastSeenAt = now
	row.SyncedAt = now
	if err := db.DB.Save(&row).Error; err != nil {
		logger.Debug("Rclone", "failed to upsert indexed file", "path", rel, "error", err)
		return nil
	}
	if !found {
		logger.Debug("Rclone", "indexed new provider file", "remote", remote.Name, "path", rel)
	}
	return &row
}

// RemoveIndexedFile drops one indexed object row after it was removed from
// the provider (upload-purge flow).
func RemoveIndexedFile(remote *models.RcloneRemote, fullPath string) {
	rel := RelativeRemotePath(remote, fullPath)
	if rel == "" {
		return
	}
	db.DB.Where("remote_id = ? AND path_hash = ?", remote.ID, pathHashOf(rel)).Delete(&models.RcloneFile{})
}

// RemoteFileStats returns how many objects are indexed for one remote plus
// the timestamp of the freshest sync (nil when nothing was synced yet).
func RemoteFileStats(remoteID uint) (int64, *time.Time) {
	var count int64
	db.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remoteID).Count(&count)
	var res struct {
		Last *time.Time
	}
	db.DB.Model(&models.RcloneFile{}).Where("remote_id = ?", remoteID).
		Select("MAX(synced_at) AS last").Scan(&res)
	return count, res.Last
}

// ReconcileAllRemotes re-indexes every configured remote into rclone_files.
// It runs in the background at app start so files living on S3 and every
// other storage provider are visible in the UI again right after a restart,
// whatever happened to local state. Failures are logged and skipped so one
// broken remote never blocks the others.
func ReconcileAllRemotes(ctx context.Context) {
	if db.DB == nil {
		return
	}
	var remotes []models.RcloneRemote
	if err := db.DB.Order("id ASC").Find(&remotes).Error; err != nil {
		logger.Error("Rclone", "Failed to load remotes for provider index reconciliation", "error", err)
		return
	}
	if len(remotes) == 0 {
		return
	}

	logger.Info("Rclone", "Reconciling provider file index", "remotes", len(remotes))
	for i := range remotes {
		if ctx.Err() != nil {
			return
		}
		if !remotes[i].Enabled {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		report, err := SyncRemoteFiles(rctx, &remotes[i])
		cancel()
		if err != nil {
			logger.Warn("Rclone", "Provider file index sync failed", "remote", remotes[i].Name, "error", err)
			continue
		}
		logger.Info("Rclone", "Provider file index synced",
			"remote", report.RemoteName, "files", report.FilesSeen, "dirs", report.DirsSeen,
			"created", report.Created, "updated", report.Updated, "removed", report.Removed)
	}
}
