package handlers

import (
	"errors"
	"net/http"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/storageguard"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StorageHandler exposes the Smart Storage Guard (Cellar S3 offloading)
// state, configuration and restore operations to the admin panel.
type StorageHandler struct{}

func NewStorageHandler() *StorageHandler { return &StorageHandler{} }

// GetStatus handles GET /api/storage/status — disk health + guard activity.
func (h *StorageHandler) GetStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": storageguard.Snapshot(),
		"totals": storageguard.Totals(),
	})
}

// GetConfig handles GET /api/storage/config
func (h *StorageHandler) GetConfig(c *gin.Context) {
	var cfg models.StorageConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Storage config missing", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// SaveConfig handles POST /api/storage/config with threshold sanitation.
func (h *StorageHandler) SaveConfig(c *gin.Context) {
	var current models.StorageConfig
	if err := db.DB.First(&current).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Storage config missing"})
		return
	}

	var incoming models.StorageConfig
	if err := c.ShouldBindJSON(&incoming); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload", "details": err.Error()})
		return
	}

	// Hysteresis sanity: high watermark well below pause watermark, both bounded.
	high := incoming.HighWatermarkPercent
	pause := incoming.PauseWatermarkPercent
	if high < 10 {
		high = 10
	}
	if high > 95 {
		high = 95
	}
	if pause <= high {
		pause = high + 3 // always keep room to resume before pausing again
	}
	if pause > 99 {
		pause = 99
	}
	if incoming.MaxConcurrentUploads < 1 {
		incoming.MaxConcurrentUploads = 1
	}
	if incoming.MaxConcurrentUploads > 16 {
		incoming.MaxConcurrentUploads = 16
	}
	incoming.HighWatermarkPercent = high
	incoming.PauseWatermarkPercent = pause

	updates := map[string]interface{}{
		"s3_enabled":              incoming.S3Enabled,
		"offload_on_completion":   incoming.OffloadOnCompletion,
		"evict_after_upload":      incoming.EvictAfterUpload,
		"high_watermark_percent":  high,
		"pause_watermark_percent": pause,
		"stream_threshold_gb":     incoming.StreamThresholdGB,
		"batch_size_gb":           incoming.BatchSizeGB,
		"max_concurrent_uploads":  incoming.MaxConcurrentUploads,
		"s3_prefix":               incoming.S3Prefix,
		"stop_seeding_on_offload": incoming.StopSeedingOnOffload,
	}
	if err := db.DB.Model(&models.StorageConfig{}).Where("id = ?", current.ID).
		Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save storage config", "details": err.Error()})
		return
	}
	storageguard.InvalidateConfigCache()
	logger.Info("Storage", "Storage guard configuration updated", "high", high, "pause", pause)

	var fresh models.StorageConfig
	if err := db.DB.First(&fresh).Error; err == nil {
		c.JSON(http.StatusOK, fresh)
		return
	}
	c.JSON(http.StatusOK, incoming)
}

// GetLogs handles GET /api/storage/logs — latest storage events.
func (h *StorageHandler) GetLogs(c *gin.Context) {
	logs := make([]models.StorageLog, 0)
	db.DB.Order("created_at DESC").Limit(100).Find(&logs)
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}

// RestoreTorrent handles POST /api/storage/restore/:hash — pulls a fully
// offloaded torrent back onto local disk from Cellar.
func (h *StorageHandler) RestoreTorrent(c *gin.Context) {
	hash := c.Param("hash")
	if err := storageguard.RestoreTorrent(hash); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent job not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "restoring", "info_hash": hash})
}

// ReoffloadTorrent handles POST /api/storage/reoffload/:hash — clears the
// restore protection so the guard can re-offload locally restored data.
func (h *StorageHandler) ReoffloadTorrent(c *gin.Context) {
	hash := c.Param("hash")
	if err := storageguard.ResetOffloadProtection(hash); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent job not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "protection_cleared", "info_hash": hash})
}
