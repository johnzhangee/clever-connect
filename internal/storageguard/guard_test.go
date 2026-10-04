package storageguard

import (
	"fmt"
	"testing"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/models"

	sqlite "clever-connect/internal/db/sqlite"

	"gorm.io/gorm"
)

// newGuardTestDB swaps in a fresh, isolated in-memory database with the tables
// Init touches (the StorageLog table backs logEvent). A unique database name
// per test prevents the shared-cache in-memory SQLite from leaking rows
// between tests.
func newGuardTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:guarddb%d?mode=memory&cache=shared", time.Now().UnixNano())
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	if err := gdb.AutoMigrate(&models.StorageConfig{}, &models.StorageLog{}); err != nil {
		t.Fatalf("failed to migrate schema: %v", err)
	}
	db.DB = gdb
}

// seedConfig writes a StorageConfig row directly, bypassing the defaults.
func seedConfig(t *testing.T, row models.StorageConfig) {
	t.Helper()
	// Snapshot the intended flags BEFORE Create: GORM excludes zero-valued
	// fields carrying a `default` tag from the INSERT (the column default
	// wins) and fills the struct field back with that default afterwards —
	// row.DirectS3Enabled would read true even though the test said false.
	wantEnabled, wantInitialized := row.DirectS3Enabled, row.DirectS3Initialized
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := db.DB.Model(&models.StorageConfig{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{
			"direct_s3_enabled":     wantEnabled,
			"direct_s3_initialized": wantInitialized,
		}).Error; err != nil {
		t.Fatalf("force seed flags: %v", err)
	}
}

func loadOnlyConfig(t *testing.T) models.StorageConfig {
	t.Helper()
	var cfg models.StorageConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// TestInitFlipsLegacyDirectS3Once: rows created before the direct-to-S3
// feature shipped carry the zero-value default (flag off, not initialized).
// Init must turn the flag on exactly once and record the initialization, so
// a later explicit opt-out survives restarts.
func TestInitFlipsLegacyDirectS3Once(t *testing.T) {
	newGuardTestDB(t)
	seedConfig(t, models.StorageConfig{
		HighWatermarkPercent:  70,
		PauseWatermarkPercent: 85,
		// MaxConcurrentUploads >= autoUploadWorkers avoids the auto-scale update.
		MaxConcurrentUploads: 16,
		DirectS3Enabled:      false,
	})

	Init()

	cfg := loadOnlyConfig(t)
	if !cfg.DirectS3Enabled {
		t.Fatal("legacy row must be flipped to direct-to-S3 enabled")
	}
	if !cfg.DirectS3Initialized {
		t.Fatal("initialization marker must be set")
	}
}

// TestInitRespectsExplicitOptOut verifies that a row whose flag was explicitly
// disabled after the one-time rollout is never re-enabled by Init.
func TestInitRespectsExplicitOptOut(t *testing.T) {
	newGuardTestDB(t)
	seedConfig(t, models.StorageConfig{
		HighWatermarkPercent:  70,
		PauseWatermarkPercent: 85,
		MaxConcurrentUploads:  16,
		DirectS3Enabled:       false,
		DirectS3Initialized:   true,
	})

	Init()

	cfg := loadOnlyConfig(t)
	if cfg.DirectS3Enabled {
		t.Fatal("explicit opt-out must not be overridden by Init")
	}
	if !cfg.DirectS3Initialized {
		t.Fatal("initialization marker must remain set")
	}
}

// TestInitSeedsDirectS3Default verifies the fresh-install path: with no config
// row, Init seeds the defaults, which enable direct-to-S3 storage from day one.
func TestInitSeedsDirectS3Default(t *testing.T) {
	newGuardTestDB(t)

	Init()

	cfg := loadOnlyConfig(t)
	if !cfg.DirectS3Enabled || !cfg.DirectS3Initialized {
		t.Fatalf("seeded defaults must have direct-to-S3 on: enabled=%v initialized=%v",
			cfg.DirectS3Enabled, cfg.DirectS3Initialized)
	}
}
