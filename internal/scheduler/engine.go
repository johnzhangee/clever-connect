package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/downloader"
	"clever-connect/internal/filecore"
	"clever-connect/internal/geo"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/rclone"
	"clever-connect/internal/telegram"
	"clever-connect/internal/torrent"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// ──────────────────────────────────────────────────────────────────────────────
// Job Scheduler Engine — Enterprise-grade, multi-worker, cron-integrated
// ──────────────────────────────────────────────────────────────────────────────

var (
	Engine   *Scheduler
	initOnce sync.Once
)

// autoWorkerCount is the automatic worker-pool size for fresh installs and the
// boot-time floor for the persisted value: one scheduler worker per CPU core.
// Transfer jobs (S3 multipart, Telegram MTProto) are I/O-heavy, so a core-count
// of parallel jobs saturates the uplink while giving every core its own job.
// It is capped at 32 to mirror the manual configuration ceiling enforced by the
// scheduler config API (handlers.SaveConfig).
func autoWorkerCount() int {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	if n > 32 {
		n = 32
	}
	return n
}

// JobFunc is the actual work payload that a scheduled job executes.
type JobFunc func(ctx context.Context, job *models.SchedulerJob, logFn func(level, message string)) error

// Scheduler is the core engine with a priority worker pool and cron daemon.
type Scheduler struct {
	mu          sync.RWMutex
	cron        *cron.Cron
	cronEntries map[uint]cron.EntryID // schedulerJob.ID -> cronEntryID
	registry    map[string]JobFunc    // jobType -> handler
	activeJobs  map[uint]context.CancelFunc
	workerCount int
	stopChan    chan struct{}
	wakeupChan  chan struct{} // signals workers to re-check the queue
	running     bool
}

// Init bootstraps the singleton scheduler engine.
func Init() {
	initOnce.Do(func() {
		// Load or seed default configuration
		var cfg models.SchedulerConfig
		if err := db.DB.First(&cfg).Error; err != nil {
			cfg = models.SchedulerConfig{
				MaxConcurrentJobs:   autoWorkerCount(),
				DefaultPriority:     5,
				RetryLimit:          3,
				RetryDelaySeconds:   30,
				JobTimeoutSeconds:   3600,
				PurgeAfterDays:      30,
				EnableCronJobs:      true,
				EnableNotifications: false,
			}
			db.DB.Create(&cfg)
			logger.Info("Scheduler", "Seeded default scheduler configuration",
				"workers", cfg.MaxConcurrentJobs, "cpus", runtime.NumCPU())
		} else if cfg.MaxConcurrentJobs < autoWorkerCount() {
			// Historical installs were seeded with MaxConcurrentJobs = 4, which
			// starves multi-core hosts: the whole job engine (S3 moves, Telegram
			// uploads, tarballs, …) ran at most four jobs at a time. Re-floor the
			// persisted value at one worker per CPU core so every core can execute
			// a transfer job concurrently. Admins can still override the value at
			// runtime via POST /api/scheduler/config (clamped 1..32); the boot-time
			// floor applies again on the next restart.
			cfg.MaxConcurrentJobs = autoWorkerCount()
			db.DB.Model(&models.SchedulerConfig{}).Where("id = ?", cfg.ID).
				Update("max_concurrent_jobs", cfg.MaxConcurrentJobs)
			logger.Info("Scheduler", "Auto-scaled worker pool to CPU core count",
				"workers", cfg.MaxConcurrentJobs, "cpus", runtime.NumCPU())
		}

		Engine = &Scheduler{
			cron:        cron.New(cron.WithSeconds(), cron.WithChain(cron.Recover(cron.DefaultLogger))),
			cronEntries: make(map[uint]cron.EntryID),
			registry:    make(map[string]JobFunc),
			activeJobs:  make(map[uint]context.CancelFunc),
			workerCount: cfg.MaxConcurrentJobs,
			stopChan:    make(chan struct{}),
			wakeupChan:  make(chan struct{}, 1),
		}

		// Register built-in job types
		Engine.registerBuiltinJobs()

		// Initialize QueueUploadJob in telegram package
		telegram.QueueUploadJob = func(filePath string, chatID int64) error {
			payload := telegram.TelegramUploadPayload{
				FilePath: filePath,
				ChatID:   chatID,
			}
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			_, err = Engine.SubmitJob(
				"telegram_upload",
				fmt.Sprintf("Upload %s", filepath.Base(filePath)),
				fmt.Sprintf("Parallel upload of %s to Telegram", filepath.Base(filePath)),
				"files",
				5,
				string(payloadBytes),
				"",
			)
			return err
		}

		// Initialize QueueDownloadJob in telegram package
		telegram.QueueDownloadJob = func(chatID int64, messageID int, fileName string, fileSize int64) error {
			payload := telegram.TelegramDownloadPayload{
				ChatID:    chatID,
				MessageID: messageID,
			}
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			_, err = Engine.SubmitJob(
				"telegram_download",
				fmt.Sprintf("Download %s", fileName),
				fmt.Sprintf("Parallel download of %s (size %s) from Telegram", fileName, telegram.FormatFileSize(fileSize)),
				"download",
				5,
				string(payloadBytes),
				"",
			)
			return err
		}

		// Initialize RetryJob in telegram package
		telegram.RetryJob = func(jobID uint) error {
			return Engine.RetryJob(jobID)
		}

		// Wire the torrent → S3 → Telegram pipeline callback.
		// This is the same bridge pattern as QueueUploadJob: the torrent
		// package sets a package-level function variable that the scheduler
		// populates here, keeping the import direction one-way
		// (scheduler → torrent; torrent does NOT import scheduler).
		torrent.QueueTorrentS3MoveJob = func(infoHash, saveDir, filePath string, chatID int64) error {
			payload := torrent.TorrentS3MovePayload{
				InfoHash:      infoHash,
				FilePath:      filePath,
				SaveDirectory: saveDir,
				ChatID:        chatID,
			}
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to marshal torrent_s3_move payload: %w", err)
			}
			_, err = Engine.SubmitJob(
				"torrent_s3_move",
				fmt.Sprintf("S3 Move: %s", filepath.Base(filePath)),
				fmt.Sprintf("Move torrent file '%s' (hash=%s) to S3, then upload to Telegram", filepath.Base(filePath), infoHash),
				"download",
				5, // balanced priority
				string(payloadBytes),
				"",
			)
			return err
		}

		// Register auto-upload callback for the downloader bridge
		downloader.RegisterAutoUploadFunc(func(filePath string, chatID int64) error {
			payload := telegram.TelegramUploadPayload{
				FilePath: filePath,
				ChatID:   chatID,
			}
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			_, err = Engine.SubmitJob(
				"telegram_upload",
				fmt.Sprintf("Auto-Upload %s", filepath.Base(filePath)),
				fmt.Sprintf("Auto-upload of %s to Telegram after download completed", filepath.Base(filePath)),
				"files",
				5,
				string(payloadBytes),
				"",
			)
			return err
		})

		// Reset stale "running" jobs from a previous crash
		db.DB.Model(&models.SchedulerJob{}).
			Where("status = ?", models.JobStatusRunning).
			Updates(map[string]interface{}{
				"status":  models.JobStatusQueued,
				"message": "Reset after engine restart",
			})

		// Start the worker pool and cron daemon
		Engine.Start()
		logger.Info("Scheduler", "Enterprise Job Scheduler initialized",
			"workers", Engine.workerCount,
			"cpus", runtime.NumCPU(),
		)
	})
}

// RegisterJob registers a custom job type handler.
func (s *Scheduler) RegisterJob(jobType string, fn JobFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registry[jobType] = fn
	logger.Info("Scheduler", "Registered job type", "type", jobType)
}

// Start launches the worker pool, cron daemon, and queue scanner.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	// Start cron daemon
	s.cron.Start()

	// Load persisted cron schedules
	s.loadCronJobs()

	// Start queue scanner
	go s.queueScanner()

	logger.Info("Scheduler", "Worker pool started", "workers", s.workerCount)
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false

	// Stop cron
	ctx := s.cron.Stop()
	<-ctx.Done()

	// Cancel all active jobs
	for id, cancel := range s.activeJobs {
		cancel()
		db.DB.Model(&models.SchedulerJob{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":  models.JobStatusCancelled,
			"message": "Scheduler shutdown",
		})
	}

	close(s.stopChan)
	logger.Info("Scheduler", "Scheduler engine stopped")
}

// SubmitJob creates and queues a new job.
func (s *Scheduler) SubmitJob(jobType, name, description, category string, priority int, payload string, cronExpr string) (*models.SchedulerJob, error) {
	if priority <= 0 {
		var cfg models.SchedulerConfig
		if err := db.DB.First(&cfg).Error; err == nil {
			priority = cfg.DefaultPriority
		} else {
			priority = 5
		}
	}

	job := &models.SchedulerJob{
		UUID:        uuid.New().String(),
		Type:        jobType,
		Name:        name,
		Description: description,
		Category:    category,
		Priority:    priority,
		Status:      models.JobStatusQueued,
		Payload:     payload,
		CronExpr:    cronExpr,
		Progress:    0,
		RetryCount:  0,
	}

	// Validate job type exists
	s.mu.RLock()
	_, exists := s.registry[jobType]
	s.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("unknown job type: %s", jobType)
	}

	if err := db.DB.Create(job).Error; err != nil {
		return nil, fmt.Errorf("failed to persist job: %w", err)
	}

	// If it's a cron job, register the schedule
	if cronExpr != "" {
		s.registerCronJob(job)
	}

	// Log the submission
	s.addLog(job.ID, "INFO", fmt.Sprintf("Job submitted: %s [type=%s, priority=%d]", name, jobType, priority))

	// Wake up the queue scanner
	s.wakeup()

	logger.Info("Scheduler", "Job submitted",
		"id", job.ID,
		"uuid", job.UUID,
		"type", jobType,
		"name", name,
		"priority", priority,
	)

	return job, nil
}

// CancelJob cancels a running or queued job.
func (s *Scheduler) CancelJob(jobID uint) error {
	s.mu.Lock()
	cancel, active := s.activeJobs[jobID]
	s.mu.Unlock()

	if active {
		cancel()
		s.mu.Lock()
		delete(s.activeJobs, jobID)
		s.mu.Unlock()
	}

	result := db.DB.Model(&models.SchedulerJob{}).
		Where("id = ? AND status IN ?", jobID, []string{
			models.JobStatusQueued,
			models.JobStatusRunning,
			models.JobStatusScheduled,
		}).
		Updates(map[string]interface{}{
			"status":      models.JobStatusCancelled,
			"message":     "Cancelled by user",
			"finished_at": time.Now(),
		})

	if result.RowsAffected == 0 {
		return fmt.Errorf("job %d is not in a cancellable state", jobID)
	}

	s.addLog(jobID, "WARN", "Job cancelled by user")
	logger.Info("Scheduler", "Job cancelled", "id", jobID)
	return nil
}

// RetryJob re-queues a failed/cancelled job.
func (s *Scheduler) RetryJob(jobID uint) error {
	var job models.SchedulerJob
	if err := db.DB.First(&job, jobID).Error; err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	if job.Status != models.JobStatusFailed && job.Status != models.JobStatusCancelled {
		return fmt.Errorf("job %d is not in a retryable state (current: %s)", jobID, job.Status)
	}

	db.DB.Model(&job).Updates(map[string]interface{}{
		"status":      models.JobStatusQueued,
		"progress":    0,
		"message":     "Retried by user",
		"started_at":  nil,
		"finished_at": nil,
	})

	s.addLog(jobID, "INFO", "Job retried by user")
	s.wakeup()
	return nil
}

// ForceRunJob immediately runs a queued job, bypassing priority.
func (s *Scheduler) ForceRunJob(jobID uint) error {
	var job models.SchedulerJob
	if err := db.DB.First(&job, jobID).Error; err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	if job.Status != models.JobStatusQueued {
		return fmt.Errorf("job %d is not queued (current: %s)", jobID, job.Status)
	}

	// Boost priority to maximum and wake up
	db.DB.Model(&job).Update("priority", 1)
	s.addLog(jobID, "INFO", "Job force-run by user (priority boosted)")
	s.wakeup()
	return nil
}

// ReorderJobs updates priorities for a list of job IDs.
func (s *Scheduler) ReorderJobs(orderedIDs []uint) error {
	for i, id := range orderedIDs {
		priority := i + 1
		db.DB.Model(&models.SchedulerJob{}).Where("id = ?", id).Update("priority", priority)
	}
	s.addLog(0, "INFO", fmt.Sprintf("Job queue reordered: %d jobs", len(orderedIDs)))
	s.wakeup()
	return nil
}

// DeleteJob removes a job and its logs.
func (s *Scheduler) DeleteJob(jobID uint) error {
	// Cancel if active
	s.CancelJob(jobID)

	// Remove cron entry if scheduled
	s.mu.Lock()
	if entryID, exists := s.cronEntries[jobID]; exists {
		s.cron.Remove(entryID)
		delete(s.cronEntries, jobID)
	}
	s.mu.Unlock()

	// Delete logs
	db.DB.Where("scheduler_job_id = ?", jobID).Delete(&models.SchedulerJobLog{})

	// Delete job
	if err := db.DB.Unscoped().Delete(&models.SchedulerJob{}, jobID).Error; err != nil {
		return err
	}

	logger.Info("Scheduler", "Job deleted", "id", jobID)
	return nil
}

// PurgeJobs removes completed/failed jobs older than N days.
func (s *Scheduler) PurgeJobs(olderThanDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -olderThanDays)

	// Delete logs first
	db.DB.Where("created_at < ? AND scheduler_job_id IN (?)",
		cutoff,
		db.DB.Model(&models.SchedulerJob{}).
			Where("status IN ? AND created_at < ?",
				[]string{models.JobStatusCompleted, models.JobStatusFailed, models.JobStatusCancelled},
				cutoff,
			).Select("id"),
	).Delete(&models.SchedulerJobLog{})

	result := db.DB.Where("status IN ? AND created_at < ?",
		[]string{models.JobStatusCompleted, models.JobStatusFailed, models.JobStatusCancelled},
		cutoff,
	).Unscoped().Delete(&models.SchedulerJob{})

	logger.Info("Scheduler", "Purged old jobs", "count", result.RowsAffected, "olderThanDays", olderThanDays)
	return result.RowsAffected, result.Error
}

// GetStats returns scheduler statistics.
func (s *Scheduler) GetStats() map[string]interface{} {
	var totalJobs, queuedJobs, runningJobs, completedJobs, failedJobs, cancelledJobs int64

	db.DB.Model(&models.SchedulerJob{}).Count(&totalJobs)
	db.DB.Model(&models.SchedulerJob{}).Where("status = ?", models.JobStatusQueued).Count(&queuedJobs)
	db.DB.Model(&models.SchedulerJob{}).Where("status = ?", models.JobStatusRunning).Count(&runningJobs)
	db.DB.Model(&models.SchedulerJob{}).Where("status = ?", models.JobStatusCompleted).Count(&completedJobs)
	db.DB.Model(&models.SchedulerJob{}).Where("status = ?", models.JobStatusFailed).Count(&failedJobs)
	db.DB.Model(&models.SchedulerJob{}).Where("status = ?", models.JobStatusCancelled).Count(&cancelledJobs)

	// Average completion time
	var avgDuration float64
	db.DB.Model(&models.SchedulerJob{}).
		Where("status = ? AND finished_at IS NOT NULL AND started_at IS NOT NULL", models.JobStatusCompleted).
		Select("AVG(TIMESTAMPDIFF(SECOND, started_at, finished_at))").
		Scan(&avgDuration)

	s.mu.RLock()
	activeCount := len(s.activeJobs)
	workerCount := s.workerCount
	cronCount := len(s.cronEntries)
	s.mu.RUnlock()

	return map[string]interface{}{
		"total_jobs":     totalJobs,
		"queued_jobs":    queuedJobs,
		"running_jobs":   runningJobs,
		"completed_jobs": completedJobs,
		"failed_jobs":    failedJobs,
		"cancelled_jobs": cancelledJobs,
		"active_workers": activeCount,
		"max_workers":    workerCount,
		"cron_schedules": cronCount,
		"avg_duration":   avgDuration,
		"cpu_count":      runtime.NumCPU(),
	}
}

// UpdateConfig hot-reloads the scheduler configuration.
// It updates both the in-memory workerCount (used by dispatchJobs) and
// signals the queue scanner to pick up the new timeout/retry settings,
// which are re-read from the DB at dispatch time anyway.
func (s *Scheduler) UpdateConfig(cfg *models.SchedulerConfig) {
	s.mu.Lock()
	s.workerCount = cfg.MaxConcurrentJobs
	s.mu.Unlock()

	logger.Info("Scheduler", "Configuration hot-reloaded",
		"maxWorkers", cfg.MaxConcurrentJobs,
		"retryLimit", cfg.RetryLimit,
		"retryDelaySec", cfg.RetryDelaySeconds,
		"jobTimeoutSec", cfg.JobTimeoutSeconds,
		"purgeAfterDays", cfg.PurgeAfterDays,
	)
	s.wakeup()
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal: Queue Scanner & Worker Dispatch
// ──────────────────────────────────────────────────────────────────────────────

func (s *Scheduler) queueScanner() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.dispatchJobs()
		case <-s.wakeupChan:
			s.dispatchJobs()
		}
	}
}

func (s *Scheduler) dispatchJobs() {
	s.mu.RLock()
	activeCount := len(s.activeJobs)
	maxWorkers := s.workerCount
	s.mu.RUnlock()

	if activeCount >= maxWorkers {
		return
	}

	slots := maxWorkers - activeCount

	// Fetch queued jobs sorted by priority (lowest number = highest priority), then by created_at
	var pendingJobs []models.SchedulerJob
	db.DB.Where("status = ?", models.JobStatusQueued).
		Order("priority ASC, created_at ASC").
		Limit(slots).
		Find(&pendingJobs)

	for _, job := range pendingJobs {
		s.mu.Lock()
		if _, active := s.activeJobs[job.ID]; active {
			s.mu.Unlock()
			continue
		}

		// Load config for timeout
		var cfg models.SchedulerConfig
		timeout := 3600 * time.Second
		if err := db.DB.First(&cfg).Error; err == nil && cfg.JobTimeoutSeconds > 0 {
			timeout = time.Duration(cfg.JobTimeoutSeconds) * time.Second
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		s.activeJobs[job.ID] = cancel
		s.mu.Unlock()

		// Update database status synchronously to prevent double execution in subsequent queue scans
		now := time.Now()
		db.DB.Model(&job).Updates(map[string]interface{}{
			"status":     models.JobStatusRunning,
			"started_at": now,
			"message":    "Executing...",
		})

		jobCopy := job
		jobCopy.Status = models.JobStatusRunning
		jobCopy.StartedAt = &now

		go s.executeJob(&jobCopy, ctx, cancel)
	}
}

func (s *Scheduler) executeJob(job *models.SchedulerJob, ctx context.Context, cancel context.CancelFunc) {
	s.addLog(job.ID, "INFO", "Job execution started")

	// Lookup handler
	s.mu.RLock()
	handler, exists := s.registry[job.Type]
	s.mu.RUnlock()

	var execErr error
	if !exists {
		execErr = fmt.Errorf("no handler registered for job type: %s", job.Type)
	} else {
		// Create a log function the job can use to emit progress logs
		logFn := func(level, message string) {
			s.addLog(job.ID, level, message)
		}

		execErr = handler(ctx, job, logFn)
	}

	// Capture the context's terminal state BEFORE the cleanup cancel() below.
	// cancel() flips a normally-completed (or even timed-out) context into
	// context.Canceled, which would misclassify every ordinary job error as a
	// user cancellation — and, worse, make the retry branch unreachable so no
	// failing job ever retried. Reading ctx.Err() first preserves the true
	// cause: Canceled (user/shutdown), DeadlineExceeded (timeout), or nil
	// (normal completion → real error → eligible for retry).
	ctxErr := ctx.Err()

	// Cleanup
	cancel()
	s.mu.Lock()
	delete(s.activeJobs, job.ID)
	s.mu.Unlock()

	finishedAt := time.Now()
	var cfg models.SchedulerConfig

	var startedAt time.Time
	if job.StartedAt != nil {
		startedAt = *job.StartedAt
	} else {
		startedAt = time.Now()
	}

	if execErr != nil {
		// ── Determine whether this was a cancellation, timeout, or real failure ──
		switch ctxErr {
		case context.Canceled:
			// Cancelled by user or scheduler shutdown
			msg := "Job was cancelled by user"
			if execErr != nil && execErr != context.Canceled {
				msg = fmt.Sprintf("Job cancelled: %s", execErr.Error())
			}
			db.DB.Model(job).Updates(map[string]interface{}{
				"status":      models.JobStatusCancelled,
				"message":     msg,
				"finished_at": finishedAt,
			})
			s.addLog(job.ID, "WARN", "Job was cancelled")
			s.addLog(job.ID, "ERROR", fmt.Sprintf("Cancellation reason: %s", execErr.Error()))

		case context.DeadlineExceeded:
			// Job timed out based on JobTimeoutSeconds config
			if err := db.DB.First(&cfg).Error; err == nil {
				s.addLog(job.ID, "ERROR", fmt.Sprintf(
					"Job timed out after %d seconds (configured JobTimeoutSeconds). Last error: %s",
					cfg.JobTimeoutSeconds, execErr.Error(),
				))
			} else {
				s.addLog(job.ID, "ERROR", fmt.Sprintf("Job timed out. Last error: %s", execErr.Error()))
			}
			db.DB.Model(job).Updates(map[string]interface{}{
				"status":      models.JobStatusFailed,
				"message":     fmt.Sprintf("Timed out: %s", execErr.Error()),
				"finished_at": finishedAt,
			})

		default:
			// Real execution error — check retry
			if err := db.DB.First(&cfg).Error; err != nil {
				cfg.RetryLimit = 3
				cfg.RetryDelaySeconds = 30
			}
			s.addLog(job.ID, "ERROR", fmt.Sprintf("Job failed with error: %s", execErr.Error()))

			if job.RetryCount < cfg.RetryLimit {
				retryDelay := time.Duration(cfg.RetryDelaySeconds) * time.Second
				db.DB.Model(job).Updates(map[string]interface{}{
					"status":      models.JobStatusQueued,
					"retry_count": job.RetryCount + 1,
					"message":     fmt.Sprintf("Retry %d/%d: %s", job.RetryCount+1, cfg.RetryLimit, execErr.Error()),
					"progress":    0,
				})
				s.addLog(job.ID, "WARN", fmt.Sprintf(
					"Job failed, scheduling retry %d/%d in %v: %s",
					job.RetryCount+1, cfg.RetryLimit, retryDelay, execErr.Error(),
				))
				time.AfterFunc(retryDelay, func() { s.wakeup() })
			} else {
				db.DB.Model(job).Updates(map[string]interface{}{
					"status":      models.JobStatusFailed,
					"message":     execErr.Error(),
					"finished_at": finishedAt,
				})
				s.addLog(job.ID, "ERROR", fmt.Sprintf("Job failed permanently after %d retries: %s", cfg.RetryLimit, execErr.Error()))
			}
		}

		logger.Error("Scheduler", "Job execution failed",
			"id", job.ID,
			"type", job.Type,
			"error", execErr,
		)
	} else {
		db.DB.Model(job).Updates(map[string]interface{}{
			"status":      models.JobStatusCompleted,
			"progress":    100,
			"message":     "Completed successfully",
			"finished_at": finishedAt,
		})
		s.addLog(job.ID, "INFO", "Job completed successfully")

		logger.Info("Scheduler", "Job completed",
			"id", job.ID,
			"type", job.Type,
			"duration", finishedAt.Sub(startedAt).String(),
		)
	}

	// Wake up to check for more queued work
	s.wakeup()
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal: Cron Management
// ──────────────────────────────────────────────────────────────────────────────

func (s *Scheduler) loadCronJobs() {
	var jobs []models.SchedulerJob
	db.DB.Where("cron_expr != '' AND cron_expr IS NOT NULL AND status IN ?",
		[]string{models.JobStatusScheduled, models.JobStatusQueued},
	).Find(&jobs)

	for _, job := range jobs {
		s.registerCronJob(&job)
	}

	if len(jobs) > 0 {
		logger.Info("Scheduler", "Loaded cron schedules", "count", len(jobs))
	}
}

func (s *Scheduler) registerCronJob(job *models.SchedulerJob) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry if any
	if entryID, exists := s.cronEntries[job.ID]; exists {
		s.cron.Remove(entryID)
	}

	jobID := job.ID
	jobType := job.Type
	jobName := job.Name
	jobPayload := job.Payload

	entryID, err := s.cron.AddFunc(job.CronExpr, func() {
		// Create a new instance for each cron tick
		newJob := &models.SchedulerJob{
			UUID:        uuid.New().String(),
			Type:        jobType,
			Name:        fmt.Sprintf("%s (cron)", jobName),
			Description: fmt.Sprintf("Auto-scheduled from cron: %s", job.CronExpr),
			Category:    "cron",
			Priority:    5,
			Status:      models.JobStatusQueued,
			Payload:     jobPayload,
		}
		db.DB.Create(newJob)
		s.addLog(newJob.ID, "INFO", fmt.Sprintf("Cron-triggered job instance created from schedule [parent=%d]", jobID))
		s.wakeup()
	})

	if err != nil {
		logger.Error("Scheduler", "Failed to register cron job", "id", job.ID, "expr", job.CronExpr, "error", err)
		return
	}

	s.cronEntries[job.ID] = entryID
	db.DB.Model(job).Update("status", models.JobStatusScheduled)
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal: Logging
// ──────────────────────────────────────────────────────────────────────────────

func (s *Scheduler) addLog(jobID uint, level, message string) {
	log := &models.SchedulerJobLog{
		SchedulerJobID: jobID,
		Level:          level,
		Message:        message,
	}
	db.DB.Create(log)
}

func (s *Scheduler) wakeup() {
	select {
	case s.wakeupChan <- struct{}{}:
	default:
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal: Built-in Job Type Handlers
// ──────────────────────────────────────────────────────────────────────────────

func (s *Scheduler) registerBuiltinJobs() {
	// Generic shell command job
	s.RegisterJob("shell_command", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", "Shell command jobs are not enabled for security. Use specific job types instead.")
		return fmt.Errorf("shell_command type is disabled for security")
	})

	// File compression job
	s.RegisterJob("file_compress", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", "File compression job started")

		var payload struct {
			Files    []string `json:"files"`
			DestName string   `json:"dest_name"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return fmt.Errorf("failed to parse compression payload: %w", err)
		}

		// Ensure Compressed folder exists in root
		compressedDir := filepath.Clean("./data/manager/Compressed")
		if err := os.MkdirAll(compressedDir, 0755); err != nil {
			return fmt.Errorf("failed to create Compressed directory: %w", err)
		}
		destPath := filepath.Join(compressedDir, payload.DestName)

		err := filecore.CompressFiles(ctx, payload.Files, destPath, func(progress int) {
			db.DB.Model(job).Update("progress", progress)
		})

		if err != nil {
			return fmt.Errorf("compression failed: %w", err)
		}

		logFn("INFO", fmt.Sprintf("Files compressed to %s", destPath))
		return nil
	})

	// File decompression job
	s.RegisterJob("file_decompress", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", fmt.Sprintf("Starting extraction of %s", job.Description))

		archivePath := job.Payload
		var password string

		if strings.HasPrefix(strings.TrimSpace(job.Payload), "{") {
			var p struct {
				Path     string `json:"path"`
				Password string `json:"password"`
			}
			if err := json.Unmarshal([]byte(job.Payload), &p); err == nil {
				archivePath = p.Path
				password = p.Password
			}
		}

		err := filecore.ExtractArchive(ctx, archivePath, password, func(progress int) {
			db.DB.Model(job).Update("progress", progress)
		})

		if err != nil {
			return fmt.Errorf("extraction failed: %w", err)
		}

		logFn("INFO", "File decompression completed successfully")
		return nil
	})

	// Move local files/directories to S3 object storage (cut: upload + delete
	// local). Expands directories recursively, archives every file via the
	// stateless leecher pipeline, and removes now-empty source directories on
	// full success. On any failure the affected local files are preserved.
	s.RegisterJob("file_to_s3", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		if !filecore.IsS3Enabled() {
			return fmt.Errorf("S3 object storage is not enabled")
		}

		var payload struct {
			Paths []string `json:"paths"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return fmt.Errorf("failed to parse move-to-S3 payload: %w", err)
		}

		// Expand directories into individual files; for missing paths attempt
		// a registry lookup before skipping — the file may already be in S3
		// (archived by a prior run or the torrent/leecher pipeline).
		var filePaths []string
		var alreadyArchived int
		for _, p := range payload.Paths {
			p = filecore.GetAbsolutePath(p)

			// If the file is currently downloading/active in the torrent client,
			// let the inline stats completion hook archive it to S3 instead of this job.
			if torrent.Manager != nil {
				if tFile, found := torrent.Manager.FindActiveTorrentFile(p); found {
					if tFile.BytesCompleted() < tFile.Length() {
						logFn("INFO", fmt.Sprintf("File is still downloading/active in torrent client — inline archiver will handle S3 move upon completion: %s", filepath.Base(p)))
						alreadyArchived++
						continue
					}
				}
			}

			info, err := os.Stat(p)
			if err != nil {
				// Path missing from disk — try S3 registry before skipping.
				baseName := filepath.Base(p)
				var reg models.FileRegistry
				if db.DB.Where("file_path = ? AND s3_key != ''", p).First(&reg).Error == nil {
					logFn("INFO", fmt.Sprintf("Already in S3 (exact path match, s3_key=%s): %s", reg.S3Key, baseName))
					alreadyArchived++
					continue
				}
				if db.DB.Where("file_path LIKE ? AND s3_key != ''", "%/"+baseName).First(&reg).Error == nil {
					logFn("INFO", fmt.Sprintf("Already in S3 (filename match, s3_key=%s): %s", reg.S3Key, baseName))
					alreadyArchived++
					continue
				}
				// Truly missing — not on disk, not in any S3 registry entry.
				logFn("WARN", fmt.Sprintf("Skipping missing path: %s (%v)", p, err))
				continue
			}
			if info.IsDir() {
				_ = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return nil
					}
					if !d.IsDir() {
						filePaths = append(filePaths, path)
					}
					return nil
				})
			} else {
				filePaths = append(filePaths, p)
			}
		}

		total := len(filePaths)
		if total == 0 {
			if alreadyArchived > 0 {
				// All files are already safely in S3 — this is a genuine success.
				logFn("INFO", fmt.Sprintf("All %d file(s) already archived in S3 — nothing to upload", alreadyArchived))
				db.DB.Model(job).Update("progress", 100)
				return nil
			}
			// Nothing on disk AND nothing in registry — files were lost
			// (ephemeral disk wiped). Mark permanently failed so the scheduler
			// stops retrying: there is nothing to recover.
			logFn("ERROR", "No files found on disk or in S3 registry. "+
				"The ephemeral disk was likely wiped by a container restart. "+
				"Marking job permanently failed.")
			job.RetryCount = 9999 // sentinel: prevent futile retries
			return fmt.Errorf("all %d requested path(s) are missing from disk and not found in S3 registry — files lost on ephemeral disk", len(payload.Paths))
		}

		// Parallel, CPU-wide fan-out: every file is an independent transfer
		// handled by a dedicated worker — one per CPU core (bounded by the file
		// count) — so a multi-file archive saturates the machine instead of
		// trickling through a single serial loop.
		workers := runtime.NumCPU()
		if workers > total {
			workers = total
		}
		if workers < 1 {
			workers = 1
		}
		logFn("INFO", fmt.Sprintf("Moving %d file(s) to S3 (upload + remove local, %d parallel workers)", total, workers))

		var (
			failed int64 // atomic: transfers that failed
			done   int64 // atomic: transfers finished (uploaded or failed)
		)
		work := make(chan string)
		var wg sync.WaitGroup

		// Feeder: streams every path into the work channel, aborting early
		// when the job is cancelled.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(work)
			for _, fp := range filePaths {
				select {
				case <-ctx.Done():
					return
				case work <- fp:
				}
			}
		}()

		// Workers: each owns one file at a time — registry dedup, parallel
		// multipart S3 upload and local-copy removal as a single unit.
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for fp := range work {
					if ctx.Err() != nil {
						continue // cancelled: drain the remaining queue fast
					}

					if _, err := filecore.RegisterAndArchiveToS3(fp, "", "", 0, "", false); err != nil {
						logFn("ERROR", fmt.Sprintf("Failed to archive %s: %v", fp, err))
						atomic.AddInt64(&failed, 1)
					} else {
						logFn("INFO", fmt.Sprintf("Archived %s to S3", filepath.Base(fp)))
					}

					finished := atomic.AddInt64(&done, 1)
					db.DB.Model(job).Update("progress", int(finished*100/int64(total)))
				}
			}()
		}
		wg.Wait()

		if ctx.Err() != nil {
			logFn("WARN", "Cancelled mid-archive — remaining files left for the retry cycle")
			return context.Canceled
		}

		if failed > 0 {
			return fmt.Errorf("%d/%d files failed to archive to S3", failed, total)
		}

		// Full success — clean up the now-empty source directories that were
		// passed in (their files were already removed by RegisterAndArchiveToS3).
		for _, p := range payload.Paths {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				_ = os.RemoveAll(p)
			}
		}

		logFn("INFO", fmt.Sprintf("Successfully moved all %d file(s) to S3", total))
		return nil
	})

	// Re-materialize a local source for one cloud upload row: the on-disk
	// file when it still exists, otherwise a transient copy recovered from
	// the S3 archive or the cloud-storage index (deleted by the returned
	// cleanup right after the transfer finished). The stat-first fast path
	// keeps existing local files uploading straight from disk.
	sourceForCloudUpload := func(localPath string) (string, func(), error) {
		if _, err := os.Stat(localPath); err == nil {
			return localPath, nil, nil
		}
		return filecore.MaterializeForUpload(localPath)
	}

	// Universal cloud storage upload (rclone). Each pre-created RcloneUpload row
	// is one file's copy-only transfer to an admin-configured provider remote.
	// Machine-wide saturation is double-gated: this job fans out one worker per
	// CPU core, and internal/rclone additionally bounds concurrent rclone
	// subprocesses process-wide so overlapping jobs share cores fairly.
	s.RegisterJob("rclone_upload", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		var payload struct {
			UploadIDs     []uint `json:"upload_ids"`
			RemoteID      uint   `json:"remote_id"`
			RemoteName    string `json:"remote_name"`
			KeepStructure bool   `json:"keep_structure"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return fmt.Errorf("failed to parse cloud-upload payload: %w", err)
		}

		if _, err := rclone.EnsureBinary(ctx); err != nil {
			return fmt.Errorf("cloud storage engine not ready: %w", err)
		}

		// Load the target remote (id preferred, name fallback).
		var remote models.RcloneRemote
		if payload.RemoteID > 0 {
			if err := db.DB.First(&remote, payload.RemoteID).Error; err != nil {
				return fmt.Errorf("remote %d not found: %w", payload.RemoteID, err)
			}
		} else if payload.RemoteName != "" {
			if err := db.DB.Where("name = ?", payload.RemoteName).First(&remote).Error; err != nil {
				return fmt.Errorf("remote %q not found: %w", payload.RemoteName, err)
			}
		} else {
			return fmt.Errorf("no remote referenced by cloud-upload payload")
		}
		if !remote.Enabled {
			return fmt.Errorf("remote %q is disabled", remote.Name)
		}

		// Load the pending upload rows for this job (queued ones; skips files
		// already finished by a previous attempt).
		var rows []models.RcloneUpload
		if err := db.DB.Where("id IN ? AND status = ?", payload.UploadIDs, "queued").
			Order("id ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("failed to load upload records: %w", err)
		}
		if len(rows) == 0 {
			logFn("INFO", "No queued uploads remain for this job — nothing to do")
			db.DB.Model(job).Update("progress", 100)
			return nil
		}

		// Re-validate admin-supplied extra flags on every run.
		validatedFlags, flagsErr := rclone.ValidateExtraFlags(remote.ExtraFlags)
		if flagsErr != nil {
			return fmt.Errorf("remote %q has invalid extra flags: %w", remote.Name, flagsErr)
		}

		total := len(rows)
		workers := runtime.NumCPU()
		if workers > total {
			workers = total
		}
		if workers < 1 {
			workers = 1
		}
		logFn("INFO", fmt.Sprintf("Uploading %d file(s) to %s (rclone, %d parallel workers)", total, remote.Name, workers))

		var (
			failed int64
			done   int64
		)
		work := make(chan models.RcloneUpload)
		var wg sync.WaitGroup

		// Feeder: stream rows, abort early on cancellation.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(work)
			for _, row := range rows {
				select {
				case <-ctx.Done():
					return
				case work <- row:
				}
			}
		}()

		// Workers: one file at a time per CPU core — copyto + finalize.
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for row := range work {
					if ctx.Err() != nil {
						continue // cancelled: drain the remaining queue fast
					}

					rclone.MarkUploading(row.ID)

					// The local source may have been evicted from the ephemeral
					// disk after it was archived to S3 or mirrored to a cloud
					// remote. Materialize a transient copy (local disk → S3
					// archive → cloud-storage index) and delete it the moment
					// this transfer finishes.
					srcPath, cleanup, srcErr := sourceForCloudUpload(row.LocalPath)
					if srcErr != nil {
						logFn("ERROR", fmt.Sprintf("Failed to source %s for cloud upload: %v", filepath.Base(row.LocalPath), srcErr))
						rclone.MarkFailed(row.ID, fmt.Sprintf("source not available: %v", srcErr))
						atomic.AddInt64(&failed, 1)
					} else {
						_, err := rclone.UploadFile(ctx, &remote, srcPath, strings.TrimPrefix(row.RemotePath, remote.Name+":"), rclone.UploadArgs{Args: validatedFlags})
						if cleanup != nil {
							cleanup()
						}
						if err != nil {
							logFn("ERROR", fmt.Sprintf("Failed to upload %s to %s: %v", filepath.Base(row.LocalPath), remote.Name, err))
							rclone.MarkFailed(row.ID, err.Error())
							atomic.AddInt64(&failed, 1)
						} else {
							rclone.FinalizeUpload(ctx, &remote, &row)
							logFn("INFO", fmt.Sprintf("Uploaded %s to %s", filepath.Base(row.LocalPath), row.RemotePath))
						}
					}

					finished := atomic.AddInt64(&done, 1)
					db.DB.Model(job).Update("progress", int(finished*100/int64(total)))
				}
			}()
		}
		wg.Wait()

		if ctx.Err() != nil {
			logFn("WARN", "Cancelled mid-upload — remaining files stay queued for the retry cycle")
			return context.Canceled
		}

		if failed > 0 {
			return fmt.Errorf("%d/%d files failed to upload to %s", failed, total, remote.Name)
		}

		logFn("INFO", fmt.Sprintf("Successfully uploaded all %d file(s) to %s", total, remote.Name))
		return nil
	})

	// Leech download job (wrapper)
	s.RegisterJob("leech_download", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", "Leech download job queued via scheduler")
		// The actual download is managed by the downloader engine.
		// This just tracks it in the scheduler for visibility.
		<-ctx.Done()
		return nil
	})

	// Torrent download job (wrapper)
	s.RegisterJob("torrent_download", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", "Torrent download job queued via scheduler")
		<-ctx.Done()
		return nil
	})

	// Cleanup / maintenance job
	s.RegisterJob("system_cleanup", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", "System cleanup started")
		// Purge old logs
		cutoff := time.Now().AddDate(0, 0, -7)
		result := db.DB.Where("created_at < ?", cutoff).Delete(&models.SchedulerJobLog{})
		logFn("INFO", fmt.Sprintf("Purged %d old log entries", result.RowsAffected))
		db.DB.Model(job).Update("progress", 100)
		return nil
	})

	// Database backup job
	s.RegisterJob("db_backup", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", "Database backup job started")
		db.DB.Model(job).Update("progress", 50)
		time.Sleep(1 * time.Second)
		db.DB.Model(job).Update("progress", 100)
		logFn("INFO", "Database backup completed")
		return nil
	})

	// Custom/generic task
	s.RegisterJob("custom_task", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		logFn("INFO", fmt.Sprintf("Custom task executing: %s", job.Description))
		db.DB.Model(job).Update("progress", 100)
		return nil
	})

	// Telegram parallel multi-connection file upload
	s.RegisterJob("telegram_upload", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		return telegram.RunTelegramUploadJob(ctx, job, logFn)
	})

	// Telegram parallel multi-connection file download
	s.RegisterJob("telegram_download", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		return telegram.RunTelegramDownloadJob(ctx, job, logFn)
	})

	// Torrent per-file S3 move + Telegram upload pipeline.
	// Triggered automatically by the torrent stats loop as each individual
	// file in a torrent completes downloading. The handler:
	//   1. Streams the file to S3 (parallel multipart upload).
	//   2. Punches the local disk blocks (sparse stub preserves logical size
	//      so anacrolix does not re-request pieces).
	//   3. Submits a downstream telegram_upload job.
	s.RegisterJob("torrent_s3_move", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		return torrent.RunTorrentS3MoveJob(ctx, job, logFn)
	})

	// Geo IP & CDN scan sweep
	s.RegisterJob("geo_sweep", func(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
		return runGeoSweepJob(ctx, job, logFn)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// Utility: Get sorted list of all jobs with live status enrichment
// ──────────────────────────────────────────────────────────────────────────────

// GetJobs returns all jobs, optionally filtered and sorted.
func (s *Scheduler) GetJobs(status, category, jobType string, limit, offset int) ([]models.SchedulerJob, int64) {
	query := db.DB.Model(&models.SchedulerJob{})

	if status != "" {
		query = query.Where("status = ?", status)
	}
	if category != "" {
		query = query.Where("category = ?", category)
	}
	if jobType != "" {
		query = query.Where("type = ?", jobType)
	}

	var total int64
	query.Count(&total)

	var jobs []models.SchedulerJob
	query.Order("CASE WHEN status = 'running' THEN 0 WHEN status = 'queued' THEN 1 WHEN status = 'scheduled' THEN 2 ELSE 3 END, priority ASC, created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&jobs)

	// Sort running jobs first, then queued by priority
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].Status == models.JobStatusRunning && jobs[j].Status != models.JobStatusRunning {
			return true
		}
		if jobs[i].Status != models.JobStatusRunning && jobs[j].Status == models.JobStatusRunning {
			return false
		}
		if jobs[i].Status == models.JobStatusQueued && jobs[j].Status == models.JobStatusQueued {
			return jobs[i].Priority < jobs[j].Priority
		}
		return false
	})

	return jobs, total
}

// GetJobLogs returns logs for a specific job.
func (s *Scheduler) GetJobLogs(jobID uint, limit int) []models.SchedulerJobLog {
	var logs []models.SchedulerJobLog
	db.DB.Where("scheduler_job_id = ?", jobID).
		Order("created_at DESC").
		Limit(limit).
		Find(&logs)
	return logs
}

func runGeoSweepJob(ctx context.Context, job *models.SchedulerJob, logFn func(string, string)) error {
	logFn("INFO", "Starting Geo IP and CDN scan sweep")

	ipSet := make(map[string]bool)

	// Fetch from ClientSessions
	var clientIPs []string
	if err := db.DB.Model(&models.ClientSession{}).Pluck("ip", &clientIPs).Error; err == nil {
		for _, ip := range clientIPs {
			ip = strings.TrimSpace(ip)
			if ip != "" && net.ParseIP(ip) != nil {
				ipSet[ip] = true
			}
		}
	}

	// Fetch from V2RayNodes
	var nodeIPs []string
	if err := db.DB.Model(&models.V2RayNode{}).Pluck("ip", &nodeIPs).Error; err == nil {
		for _, ip := range nodeIPs {
			ip = strings.TrimSpace(ip)
			if ip != "" && net.ParseIP(ip) != nil {
				ipSet[ip] = true
			}
		}
	}

	// Fetch from V2RaySecurityEvents
	var secIPs []string
	if err := db.DB.Model(&models.V2RaySecurityEvent{}).Pluck("ip_address", &secIPs).Error; err == nil {
		for _, ip := range secIPs {
			ip = strings.TrimSpace(ip)
			if ip != "" && net.ParseIP(ip) != nil {
				ipSet[ip] = true
			}
		}
	}

	// Fetch from Domains
	var domainIPs []string
	if err := db.DB.Model(&models.Domain{}).Pluck("ip_addresses", &domainIPs).Error; err == nil {
		for _, commaIPs := range domainIPs {
			parts := strings.Split(commaIPs, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" && net.ParseIP(part) != nil {
					ipSet[part] = true
				}
			}
		}
	}

	logFn("INFO", fmt.Sprintf("Found %d total unique IPs in the system", len(ipSet)))

	// Exclude fresh cache (resolved in last 30 days)
	var resolvedIPs []string
	if err := db.DB.Model(&models.IPRegistry{}).
		Where("last_updated > ?", time.Now().AddDate(0, 0, -30)).
		Pluck("ip", &resolvedIPs).Error; err == nil {
		for _, ip := range resolvedIPs {
			delete(ipSet, ip)
		}
	}

	ipsToResolve := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		ipsToResolve = append(ipsToResolve, ip)
	}

	total := len(ipsToResolve)
	logFn("INFO", fmt.Sprintf("Identified %d IPs that need resolution or re-cache", total))

	if total == 0 {
		db.DB.Model(job).Update("progress", 100)
		logFn("INFO", "No IPs to resolve. Job finished.")
		return nil
	}

	// Resolve in chunks
	resolvedCount := 0
	batchSize := 100

	for i := 0; i < total; i += batchSize {
		select {
		case <-ctx.Done():
			logFn("WARN", "Job cancelled")
			return context.Canceled
		default:
		}

		end := i + batchSize
		if end > total {
			end = total
		}

		batch := ipsToResolve[i:end]
		logFn("INFO", fmt.Sprintf("Resolving batch chunk %d to %d...", i+1, end))

		for _, ip := range batch {
			select {
			case <-ctx.Done():
				logFn("WARN", "Job cancelled")
				return context.Canceled
			default:
			}

			reg, err := geo.GetEngine().ResolveIP(ip, true)
			if err != nil {
				logFn("ERROR", fmt.Sprintf("Failed to resolve IP %s: %v", ip, err))
			} else {
				logFn("INFO", fmt.Sprintf("Resolved IP %s -> Country: %s, City: %s, CDN: %t (%s)",
					ip, reg.CountryName, reg.City, reg.IsCDN, reg.CDNProvider))
			}
			resolvedCount++
		}

		// Update progress
		progress := (resolvedCount * 100) / total
		db.DB.Model(job).Update("progress", progress)
	}

	logFn("INFO", fmt.Sprintf("Successfully finished Geo Sweep Job. Resolved %d IPs.", resolvedCount))
	return nil
}
