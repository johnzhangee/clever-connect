// uploads.go — per-file upload records and their transfer-state helpers.
package rclone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

// UploadPlanner bundles everything needed to pre-create upload ledger rows.
type UploadPlanner struct {
	Remote            *models.RcloneRemote
	AbsolutePaths     []string // sandbox-verified absolute source paths
	ManagerRoot       string   // absolute file-manager root, for rel paths
	DestDir           string   // destination folder under remote RootPrefix
	KeepStructure     bool     // mirror local relative folders remotely
	RequirePublicLink bool     // create provider `link` after success
	SchedulerJobID    uint
}

// fileSize is a best-effort size lookup for ledger rows.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// relToRoot strips managerRoot from an absolute path (basename fallback).
func relToRoot(managerRoot, abs string) string {
	root := filepath.Clean(managerRoot)
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(abs)
	}
	return rel
}

// destFor derives the remote destination sub-path for one file: either a flat
// upload into DestDir, or a structure-mirroring upload preserving folders.
func destFor(p *UploadPlanner, relPath string) string {
	name := filepath.Base(relPath)
	if !p.KeepStructure || relPath == name {
		if strings.TrimSpace(p.DestDir) == "" {
			return name
		}
		return strings.Trim(p.DestDir, "/") + "/" + name
	}
	dir := strings.Trim(strings.ReplaceAll(filepath.Dir(relPath), "\\", "/"), "/")
	if strings.TrimSpace(p.DestDir) == "" {
		return dir
	}
	return strings.Trim(p.DestDir, "/") + "/" + dir
}

// PlanUploads converts sandbox-verified absolute source paths into upload
// records, expanding directories recursively. The returned rows carry final
// remote destinations and are ready for the rclone_upload job.
func PlanUploads(p *UploadPlanner) ([]models.RcloneUpload, error) {
	if p.Remote == nil {
		return nil, fmt.Errorf("no remote selected")
	}

	var files []struct {
		abs string
		rel string
	}
	for _, abs := range p.AbsolutePaths {
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("source missing: %s (%v)", abs, err)
		}
		if !info.IsDir() {
			files = append(files, struct {
				abs string
				rel string
			}{abs: abs, rel: relToRoot(p.ManagerRoot, abs)})
			continue
		}
		err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			files = append(files, struct {
				abs string
				rel string
			}{abs: path, rel: relToRoot(p.ManagerRoot, path)})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed walking %s: %w", abs, err)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found to upload")
	}

	rows := make([]models.RcloneUpload, 0, len(files))
	for _, f := range files {
		rows = append(rows, models.RcloneUpload{
			SchedulerJobID:    p.SchedulerJobID,
			RemoteID:          p.Remote.ID,
			RemoteName:        p.Remote.Name,
			LocalPath:         f.abs,
			LocalRelPath:      f.rel,
			RemotePath:        joinRemotePath(p.Remote, destFor(p, f.rel)),
			Size:              fileSize(f.abs),
			Status:            "queued",
			RequirePublicLink: p.RequirePublicLink,
		})
	}
	return rows, nil
}

// CreateUploads persists planned rows.
func CreateUploads(rows []models.RcloneUpload) ([]models.RcloneUpload, error) {
	for i := range rows {
		if err := db.DB.Create(&rows[i]).Error; err != nil {
			return nil, fmt.Errorf("failed to create upload record: %w", err)
		}
	}
	return rows, nil
}

// AttachJobID stamps every planned row with the scheduler job that will run it.
func AttachJobID(rows []models.RcloneUpload, jobID uint) error {
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if err := db.DB.Model(&models.RcloneUpload{}).Where("id IN ?", ids).
		Update("scheduler_job_id", jobID).Error; err != nil {
		return err
	}
	for i := range rows {
		rows[i].SchedulerJobID = jobID
	}
	return nil
}

// MarkUploading flips a row into its in-progress state.
func MarkUploading(id uint) {
	db.DB.Model(&models.RcloneUpload{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": "uploading", "transfer_error": ""})
}

// MarkFailed persists a failed transfer with its (sanitized) error text.
func MarkFailed(id uint, errText string) {
	if len(errText) > 2000 {
		errText = errText[:2000]
	}
	db.DB.Model(&models.RcloneUpload{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": "failed", "transfer_error": errText})
}

// FinalizeUpload runs post-transfer enrichment for one successfully uploaded
// file: provider-side stat is stored as ProviderMeta (with the provider file
// ID), the requested public share link is created when enabled, and the file
// is mirrored into the persistent rclone_files index so it stays visible in
// the UI after app restarts. Enrichment failures are recorded on the row but
// never fail the transfer.
func FinalizeUpload(ctx context.Context, remote *models.RcloneRemote, row *models.RcloneUpload) {
	now := time.Now()
	updates := map[string]interface{}{
		"status":         "success",
		"transferred_at": now,
	}

	var statEntry *RemoteEntry
	if raw, entry, err := Stat(ctx, remote, row.RemotePath); err == nil {
		updates["provider_meta"] = raw
		statEntry = entry
		if entry != nil && entry.ID != "" {
			updates["provider_file_id"] = entry.ID
		}
	} else {
		logger.Debug("Rclone", "post-upload stat unavailable", "row", row.ID, "error", err)
	}

	if row.RequirePublicLink {
		if link, err := CreateLink(ctx, remote, row.RemotePath); err == nil {
			updates["public_link"] = link
			updates["link_error"] = ""
		} else if IsOptionalOpError(err) {
			updates["link_error"] = fmt.Sprintf("provider does not support public links: %s", err.(*OptionalExitError).Detail)
		} else {
			updates["link_error"] = err.Error()
		}
	}

	if err := db.DB.Model(&models.RcloneUpload{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		logger.Error("Rclone", "failed finalizing upload record", "row", row.ID, "error", err)
	} else {
		logger.Info("Rclone", "upload finalized", "row", row.ID, "remote", row.RemotePath)
	}

	// Mirror the freshly uploaded object into the persistent provider index.
	if fileRow := UpsertRemoteFile(remote, row.RemotePath, statEntry); fileRow != nil {
		if link, ok := updates["public_link"].(string); ok && link != "" {
			db.DB.Model(fileRow).Updates(map[string]interface{}{
				"public_link": link,
				"link_error":  "",
			})
		}
	}
}
