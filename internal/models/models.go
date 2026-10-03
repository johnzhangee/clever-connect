package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type User struct {
	gorm.Model
	Username string `gorm:"size:191;uniqueIndex;not null" json:"username"`
	Password string `gorm:"not null" json:"-"`
	Role     string `gorm:"default:'admin'" json:"role"`
}

type ClientSession struct {
	ID            string    `gorm:"primaryKey" json:"id"`
	Username      string    `gorm:"not null" json:"username"`
	IP            string    `json:"ip"`
	Country       string    `json:"country"`
	Flag          string    `json:"flag"`
	Protocol      string    `json:"protocol"`
	ConnectedAt   time.Time `json:"connected_at"`
	UploadSpeed   float64   `json:"upload_speed"`   // MB/s
	DownloadSpeed float64   `json:"download_speed"` // MB/s
	Active        bool      `gorm:"default:true" json:"active"`
}

// EhcoServerConfig stores how the Clever Cloud server listens for incoming tunnel traffic
type EhcoServerConfig struct {
	gorm.Model
	ListenPort string `json:"listen_port" gorm:"default:'3001'"`
	AuthToken  string `json:"auth_token"`
	TargetMode string `json:"target_mode" gorm:"default:'direct'"` // 'direct' or 'xray'
	TargetHost string `json:"target_host" gorm:"default:'127.0.0.1:80'"`

	// --- NEW CTO CONFIGS ---
	EnableMux bool `json:"enable_mux" gorm:"default:true"`
	KeepAlive int  `json:"keep_alive" gorm:"default:15"` // In seconds
	IsActive  bool `json:"is_active" gorm:"default:false"`
}

// EhcoClientConfig stores how the local machine connects to Clever Cloud
type EhcoClientConfig struct {
	gorm.Model
	LocalPort    string `json:"local_port" gorm:"default:'1080'"`
	RemoteURL    string `json:"remote_url"` // e.g., wss://app.cleverapps.io/tunnel
	SecondaryURL string `json:"secondary_url"`
	AuthToken    string `json:"auth_token"`

	// --- NEW CTO CONFIGS ---
	SNI       string `json:"sni"` // Essential for TLS obfuscation
	EnableMux bool   `json:"enable_mux" gorm:"default:true"`
	KeepAlive int    `json:"keep_alive" gorm:"default:15"`
	BypassIR  bool   `json:"bypass_ir" gorm:"default:true"`
	IsActive  bool   `json:"is_active" gorm:"default:false"`

	// --- DYNAMIC EDGE BRIDGE ---
	EnableBridge bool   `json:"enable_bridge" gorm:"default:false"`
	BridgeURL    string `json:"bridge_url"`
	BridgeSNI    string `json:"bridge_sni"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Soroush WebRTC "The Hive" Tunnel Models (ADDITIVE — parallel to Ehco)
// ──────────────────────────────────────────────────────────────────────────────

// SoroushAccount stores authenticated Soroush messenger accounts used as
// tunnel workers. Each account holds its MTProto auth key material for
// autonomous JWT token generation via the Soroush LiveKit SFU.
type SoroushAccount struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	PhoneNumber   string    `gorm:"size:20;uniqueIndex;not null" json:"phone_number"`
	Name          string    `gorm:"size:100" json:"name"`
	SoroushUserID int64     `json:"soroush_user_id"`
	AccessHash    int64     `json:"access_hash"`
	DisplayName   string    `gorm:"size:100" json:"display_name"`
	AuthKey       []byte    `json:"-"` // 256-byte MTProto auth key
	AuthKeyID     []byte    `json:"-"` // 8-byte auth key ID
	ServerSalt    []byte    `json:"-"` // 8-byte server salt
	DcID          int       `json:"dc_id" gorm:"default:2"`
	Role          string    `json:"role" gorm:"size:20;default:'worker'"` // 'host' or 'worker'
	IsServerNode  bool      `json:"is_server_node" gorm:"default:false"`
	Status        string    `json:"status" gorm:"size:30;default:'idle'"` // idle, connected, busy, tunnel_active, error
	LastActive    string    `json:"last_active"`
	LiveKitToken  string    `json:"livekit_token" gorm:"type:text"` // Per-account LiveKit JWT token (unique identity per worker)
}

// SoroushTunnelConfig stores the Hive tunnel engine configuration.
// This is a singleton row — only one config exists at a time.
// The PSK field is used for QUIC TLS identity verification and
// the HKDF-based handshake sync protocol.
type SoroushTunnelConfig struct {
	gorm.Model
	GroupChatID          int64  `json:"group_chat_id"`
	GroupAccessHash      int64  `json:"group_access_hash"`
	CallID               int64  `json:"call_id"`                                 // Static bypass parameter
	CallAccessHash       string `json:"call_access_hash"`                        // Static bypass parameter
	ServerIdentity       string `json:"server_identity"`                         // The exact Soroush UserID of the Queen (e.g., "64698297")
	PSK                  string `json:"psk"`                                     // Pre-Shared Key for worker auth
	LiveKitURL           string `json:"livekit_url"`                             // LiveKit SFU WebSocket endpoint (e.g., wss://k.splus.ir)
	FallbackLiveKitToken string `json:"fallback_livekit_token" gorm:"type:text"` // Manual fallback LiveKit token
	SocksPort            int    `json:"socks_port" gorm:"default:4046"`
	IsActive             bool   `json:"is_active" gorm:"default:false"`
	EngineMode           string `json:"engine_mode" gorm:"size:30;default:'swarm'"` // 'swarm' (LiveKit SFU Swarm)
	MaxWorkers           int    `json:"max_workers" gorm:"default:5"`
	LoadBalanceAlgo      string `json:"load_balance_algo" gorm:"size:30;default:'least-latency'"` // 'round-robin', 'least-latency'
}

// LeechConfig stores the advanced settings for the download manager
type LeechConfig struct {
	gorm.Model
	DefaultSavePath      string `json:"default_save_path" gorm:"default:'/downloads'"`
	MaxConcurrent        int    `json:"max_concurrent" gorm:"default:3"`
	ThreadsPerJob        int    `json:"threads_per_job" gorm:"default:8"`
	UserAgent            string `json:"user_agent" gorm:"default:'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:126.0) Gecko/20100101 Firefox/126.0'"`
	ProxyURL             string `json:"proxy_url"` // Optional HTTP/SOCKS5 proxy
	PremiumUserID        string `json:"premium_user_id"`
	PremiumAPIKey        string `json:"premium_api_key"`
	AutoUploadToTelegram bool   `json:"auto_upload_to_telegram" gorm:"default:false"` // Auto-upload completed downloads to Telegram
	AutoUploadChatID     int64  `json:"auto_upload_chat_id"`                          // Target chat ID for auto-uploads (0 = first admin)
}

// LeechJob tracks individual remote download tasks
type LeechJob struct {
	ID            string    `gorm:"primaryKey" json:"id"`
	URL           string    `gorm:"type:text;not null" json:"url"`
	Filename      string    `json:"filename"`
	SaveDirectory string    `json:"save_directory"`
	TotalBytes    int64     `json:"total_bytes"`
	Downloaded    int64     `json:"downloaded"`
	Status        string    `json:"status" gorm:"default:'pending'"` // pending, downloading, paused, completed, error
	Progress      float64   `json:"progress"`                        // 0.0 to 100.0
	Speed         float64   `json:"speed"`                           // MB/s
	Threads       int       `json:"threads"`
	Username      string    `json:"username"`
	Password      string    `json:"password"`
	UsePremium    bool      `json:"use_premium" gorm:"default:false"`
	ErrorMessage  string    `json:"error_message"`
	FileExists    bool      `gorm:"-" json:"file_exists"`
	S3Stored      bool      `gorm:"-" json:"s3_stored"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TorrentJob tracks BitTorrent tasks
type TorrentJob struct {
	InfoHash      string  `gorm:"primaryKey" json:"info_hash"`
	Name          string  `json:"name"`
	MagnetURI     string  `gorm:"type:text" json:"magnet_uri"`
	TorrentPath   string  `json:"torrent_path"` // Local path to saved .torrent
	SaveDirectory string  `json:"save_directory"`
	Status        string  `json:"status" gorm:"default:'downloading'"` // downloading, paused, completed, seeding, error, queued (waiting for disk space)
	TotalBytes    int64   `json:"total_bytes"`
	Downloaded    int64   `json:"downloaded"`
	Uploaded      int64   `json:"uploaded"`
	Progress      float64 `json:"progress"`
	DownloadSpeed float64 `json:"download_speed"` // MB/s
	UploadSpeed   float64 `json:"upload_speed"`   // MB/s
	Peers         int     `json:"peers"`
	SelectedFiles string  `gorm:"type:text" json:"selected_files"` // JSON array of selected file indices
	ErrorMessage  string  `json:"error_message"`
	FileExists    bool    `gorm:"-" json:"file_exists"`
	S3Stored      bool    `gorm:"-" json:"s3_stored"` // true when the torrent's files are archived in S3 object storage

	// ── Smart S3 offload fields (Storage Guard) ──
	// OffloadStatus: "" (no offload), "uploading" (S3 upload in progress),
	// "uploaded" (fully uploaded, files still local), "offloaded" (uploaded and
	// local copies removed), "failed" (last upload attempt failed), "restoring"
	RestoreStatus  string    `json:"restore_status" gorm:"default:''"` // "", "restoring", "restored"
	StreamMode     bool      `json:"stream_mode" gorm:"default:false"`
	OffloadStatus  string    `json:"offload_status" gorm:"default:''"`
	OffloadedFiles int       `json:"offloaded_files" gorm:"default:0"` // number of files secured in S3
	OffloadedBytes int64     `json:"offloaded_bytes" gorm:"default:0"`
	UploadSpeedS3  float64   `json:"upload_speed_s3"` // MB/s to Cellar/S3
	PausedByGuard  bool      `json:"paused_by_guard" gorm:"default:false"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TelegramConfig stores the Telegram bot configuration, persisted in the database.
// All settings are configurable from the admin panel and the REST API.
type TelegramConfig struct {
	gorm.Model
	BotToken            string `json:"bot_token" gorm:"type:text"`
	AdminUserIDs        string `json:"admin_user_ids" gorm:"type:text"` // Comma-separated Telegram user IDs
	WelcomeMessage      string `json:"welcome_message" gorm:"type:text"`
	PollingInterval     int    `json:"polling_interval" gorm:"default:10"` // Seconds between long-poll cycles
	MaxFileSize         int    `json:"max_file_size" gorm:"default:2000"`  // Maximum file size in MB
	EnableFileSharing   bool   `json:"enable_file_sharing" gorm:"default:true"`
	EnableNotifications bool   `json:"enable_notifications" gorm:"default:true"`
	IsActive            bool   `json:"is_active" gorm:"default:false"` // Whether the bot should auto-start
	AppID               int    `json:"app_id"`
	AppHash             string `json:"app_hash"`
	MTProtoServer       string `json:"mtproto_server"`
	MTProtoPublicKey    string `json:"mtproto_public_key" gorm:"type:text"`
	PhoneNumber         string `json:"phone_number"`
	AuthType            string `json:"auth_type" gorm:"default:'bot'"` // 'bot' or 'user'
}

// TorrentConfig stores advanced client configurations for BitTorrent client
type TorrentConfig struct {
	gorm.Model
	SaveDirectory            string  `json:"save_directory" gorm:"default:'./data/manager/downloads'"`
	MaxConnectionsPerTorrent int     `json:"max_connections_per_torrent" gorm:"default:200"`
	MaxHalfOpenConnections   int     `json:"max_half_open_connections" gorm:"default:100"`
	UploadLimitMB            float64 `json:"upload_limit_mb" gorm:"default:0"`   // 0 is unlimited
	DownloadLimitMB          float64 `json:"download_limit_mb" gorm:"default:0"` // 0 is unlimited
	EnableDHT                bool    `json:"enable_dht" gorm:"default:true"`
	EnablePEX                bool    `json:"enable_pex" gorm:"default:true"`
	EnableUTP                bool    `json:"enable_utp" gorm:"default:true"`
	EnableTCP                bool    `json:"enable_tcp" gorm:"default:true"`
	EnableUpload             bool    `json:"enable_upload" gorm:"default:true"`
	PieceHashersPerTorrent   int     `json:"piece_hashers_per_torrent" gorm:"default:4"`
	CustomTrackers           string  `json:"custom_trackers" gorm:"type:text"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Enterprise Job Scheduler Models
// ──────────────────────────────────────────────────────────────────────────────

// Job status constants
const (
	JobStatusQueued    = "queued"
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"
	JobStatusScheduled = "scheduled" // For cron-scheduled jobs
)

// SchedulerJob is the central model tracking each unit of work in the scheduler.
type SchedulerJob struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	UUID        string     `gorm:"size:36;uniqueIndex" json:"uuid"`
	Type        string     `gorm:"size:100;not null;index" json:"type"`                   // e.g., file_compress, leech_download, custom_task
	Name        string     `gorm:"size:255;not null" json:"name"`                         // Human-readable name
	Description string     `gorm:"type:text" json:"description"`                          // Extended description
	Category    string     `gorm:"size:100;index;default:'general'" json:"category"`      // Grouping: general, files, download, system, cron
	Status      string     `gorm:"size:50;not null;index;default:'queued'" json:"status"` // queued, running, completed, failed, cancelled, scheduled
	Priority    int        `gorm:"default:5;index" json:"priority"`                       // 1=highest, 10=lowest
	Progress    int        `gorm:"default:0" json:"progress"`                             // 0-100
	Message     string     `gorm:"type:text" json:"message"`                              // Status message or error details
	Payload     string     `gorm:"type:text" json:"payload"`                              // JSON payload for the job handler
	CronExpr    string     `gorm:"size:100" json:"cron_expr"`                             // Optional cron expression (robfig/cron format)
	RetryCount  int        `gorm:"default:0" json:"retry_count"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// SchedulerJobLog stores granular execution logs for each job run.
type SchedulerJobLog struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SchedulerJobID uint      `gorm:"index;not null" json:"scheduler_job_id"`
	Level          string    `gorm:"size:20;not null" json:"level"` // INFO, WARN, ERROR, DEBUG
	Message        string    `gorm:"type:text;not null" json:"message"`
	CreatedAt      time.Time `json:"created_at"`
}

// SchedulerConfig stores admin-configurable scheduler parameters.
type SchedulerConfig struct {
	gorm.Model
	MaxConcurrentJobs   int  `json:"max_concurrent_jobs" gorm:"default:4"`
	DefaultPriority     int  `json:"default_priority" gorm:"default:5"`
	RetryLimit          int  `json:"retry_limit" gorm:"default:3"`
	RetryDelaySeconds   int  `json:"retry_delay_seconds" gorm:"default:30"`
	JobTimeoutSeconds   int  `json:"job_timeout_seconds" gorm:"default:3600"`
	PurgeAfterDays      int  `json:"purge_after_days" gorm:"default:30"`
	EnableCronJobs      bool `json:"enable_cron_jobs" gorm:"default:true"`
	EnableNotifications bool `json:"enable_notifications" gorm:"default:false"`
}

// TelegramSubscriber stores Telegram users who have interacted with the bot.
type TelegramSubscriber struct {
	gorm.Model
	ChatID    int64  `gorm:"uniqueIndex;not null" json:"chat_id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	Active    bool   `gorm:"default:true" json:"active"`
}

// ──────────────────────────────────────────────────────────────────────────────
// YouTube Downloader Models
// ──────────────────────────────────────────────────────────────────────────────

// YouTubeJob tracks individual YouTube video download tasks
type YouTubeJob struct {
	ID              string    `gorm:"primaryKey" json:"id"`
	VideoURL        string    `gorm:"type:text;not null" json:"video_url"`
	VideoID         string    `json:"video_id"`
	Title           string    `gorm:"type:text" json:"title"`
	Author          string    `json:"author"`
	Duration        string    `json:"duration"` // Human-readable duration
	DurationSeconds int64     `json:"duration_seconds"`
	Thumbnail       string    `gorm:"type:text" json:"thumbnail"`
	Filename        string    `json:"filename"`
	SaveDirectory   string    `json:"save_directory"`
	SelectedITag    int       `json:"selected_itag"`
	QualityLabel    string    `json:"quality_label"` // e.g., "1080p", "720p", "360p"
	MimeType        string    `json:"mime_type"`
	TotalBytes      int64     `json:"total_bytes"`
	Downloaded      int64     `json:"downloaded"`
	Status          string    `json:"status" gorm:"default:'pending'"` // pending, fetching, downloading, converting, completed, error
	Progress        float64   `json:"progress"`                        // 0.0 to 100.0
	ConvertProgress float64   `json:"convert_progress"`                // 0.0 to 100.0 (TV conversion progress)
	Speed           float64   `json:"speed"`                           // MB/s
	ConvertToTV     bool      `json:"convert_to_tv" gorm:"default:false"`
	ConvertStatus   string    `json:"convert_status"` // "", "queued", "converting", "completed", "error"
	ErrorMessage    string    `json:"error_message"`
	FileExists      bool      `gorm:"-" json:"file_exists"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// YouTubeConfig stores default configurations for YouTube downloads
type YouTubeConfig struct {
	gorm.Model
	DefaultSavePath string `json:"default_save_path" gorm:"default:'./downloads/youtube'"`
	MaxConcurrent   int    `json:"max_concurrent" gorm:"default:2"`
	ProxyURL        string `json:"proxy_url"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Spotify Downloader Models
// ──────────────────────────────────────────────────────────────────────────────

// SpotifyConfig stores admin-configurable Spotify downloader settings
type SpotifyConfig struct {
	gorm.Model
	ClientID         string `json:"client_id" gorm:"type:text"`
	ClientSecret     string `json:"client_secret" gorm:"type:text"`
	DefaultSavePath  string `json:"default_save_path" gorm:"default:'./downloads/spotify/audios'"`
	DefaultFormat    string `json:"default_format" gorm:"default:'mp3'"`   // mp3, flac, opus, m4a, wav, ogg
	DefaultBitrate   string `json:"default_bitrate" gorm:"default:'320k'"` // 128k, 192k, 256k, 320k, auto
	MaxConcurrent    int    `json:"max_concurrent" gorm:"default:3"`
	EmbedMetadata    bool   `json:"embed_metadata" gorm:"default:true"`                     // Embed ID3 tags & cover art
	EmbedLyrics      bool   `json:"embed_lyrics" gorm:"default:true"`                       // Embed lyrics if available
	OverwriteExist   bool   `json:"overwrite_existing" gorm:"default:false"`                // Overwrite files if they exist
	ProxyURL         string `json:"proxy_url"`                                              // Optional proxy for Spotify API
	FileNameTemplate string `json:"file_name_template" gorm:"default:'{artist} - {title}'"` // Filename template
}

// SpotifyJob tracks individual Spotify track download tasks through the full pipeline
type SpotifyJob struct {
	ID            string    `gorm:"primaryKey" json:"id"`
	SpotifyURL    string    `gorm:"type:text;not null" json:"spotify_url"` // Original Spotify URL
	SpotifyID     string    `json:"spotify_id"`                            // Spotify Track ID
	Title         string    `gorm:"type:text" json:"title"`
	Artist        string    `json:"artist"`
	Artists       string    `gorm:"type:text" json:"artists"` // JSON array of artist names
	Album         string    `json:"album"`
	AlbumArtist   string    `json:"album_artist"`
	CoverURL      string    `gorm:"type:text" json:"cover_url"` // High-res album art URL
	ReleaseDate   string    `json:"release_date"`
	TrackNumber   int       `json:"track_number"`
	TotalTracks   int       `json:"total_tracks"`
	DiscNumber    int       `json:"disc_number"`
	DurationMs    int       `json:"duration_ms"`
	ISRC          string    `json:"isrc"` // International Standard Recording Code
	Genre         string    `json:"genre"`
	Explicit      bool      `json:"explicit"`
	Popularity    int       `json:"popularity"`
	YouTubeURL    string    `gorm:"type:text" json:"youtube_url"` // Matched YouTube video URL
	Filename      string    `json:"filename"`
	SaveDirectory string    `json:"save_directory"`
	Format        string    `json:"format" gorm:"default:'mp3'"`   // Output format
	Bitrate       string    `json:"bitrate" gorm:"default:'320k'"` // Output bitrate
	TotalBytes    int64     `json:"total_bytes"`
	Downloaded    int64     `json:"downloaded"`
	Status        string    `json:"status" gorm:"default:'pending'"` // pending, fetching_meta, matching, downloading, converting, tagging, completed, error
	Progress      float64   `json:"progress"`                        // 0.0 to 100.0
	Speed         float64   `json:"speed"`                           // MB/s
	AlbumJobID    string    `json:"album_job_id"`                    // Group tracks from same album
	ErrorMessage  string    `json:"error_message"`
	FileExists    bool      `gorm:"-" json:"file_exists"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// FileRegistry tracks unique files saved on disk via their BLAKE3 checksum
type FileRegistry struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Checksum    string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"checksum"`
	FilePath    string    `gorm:"type:text;not null" json:"file_path"`
	S3Key       string    `gorm:"type:varchar(255);index" json:"s3_key"`
	FileSize    int64     `json:"file_size"`
	MimeType    string    `json:"mime_type"`
	URL         string    `gorm:"type:text" json:"url"`
	ETag        string    `gorm:"type:varchar(256);index" json:"etag"`
	TgFileID    int64     `gorm:"index" json:"tg_file_id"`
	TorrentHash string    `gorm:"type:varchar(40);index" json:"torrent_hash"`
	CreatedAt   time.Time `json:"created_at"`

	// ── S3 offload fields (Storage Guard) ──
	// InS3: local copy was evicted from disk; serve straight from the Cellar
	// bucket. The S3Key column above is shared with the filecore pipeline.
	InS3 bool `json:"in_s3" gorm:"default:false"`
}

// StorageConfig stores admin-configurable smart storage management settings.
// It powers the Storage Guard which keeps the small Docker instance disk from
// overflowing by relaying torrent data to the S3-compatible bucket (Cellar).
type StorageConfig struct {
	gorm.Model
	// Master switch for relaying completed torrent files into S3.
	S3Enabled bool `json:"s3_enabled" gorm:"default:true"`
	// Begin uploading every torrent file to S3 immediately as it completes.
	OffloadOnCompletion bool `json:"offload_on_completion" gorm:"default:true"`
	// Delete local copies right after every file is confirmed in S3
	// (keeps local disk usage near zero; streaming reads fall back to S3).
	EvictAfterUpload bool `json:"evict_after_upload" gorm:"default:true"`
	// Percentage of disk usage that triggers eviction of already-uploaded
	// content (default 70 ⇒ offload pressure starts once free space drops
	// below 30%).
	HighWatermarkPercent int `json:"high_watermark_percent" gorm:"default:70"`
	// Percentage of disk usage that pauses ALL active downloads (hard stop).
	PauseWatermarkPercent int `json:"pause_watermark_percent" gorm:"default:85"`
	// Torrents bigger than this many GB are downloaded in sequential batches
	// that fit the local staging area ("Stream Mode"), never overflowing disk.
	StreamThresholdGB int `json:"stream_threshold_gb" gorm:"default:12"`
	// Download admission control: hold new torrent downloads in a FIFO queue
	// until the projected disk footprint fits, so several concurrent big
	// torrents cannot overflow the small instance disk.
	AdmissionEnabled bool `json:"admission_enabled" gorm:"default:true"`
	// Percentage of the total disk kept free when admitting a new download.
	AdmissionReservePercent int `json:"admission_reserve_percent" gorm:"default:10"`
	// Absolute floor (GB) of free space kept when admitting a new download.
	AdmissionReserveMinGB int `json:"admission_reserve_min_gb" gorm:"default:5"`
	// Maximum size of a single stream-mode batch (GB) resident on local disk.
	BatchSizeGB int `json:"batch_size_gb" gorm:"default:8"`
	// Number of parallel S3 upload workers.
	MaxConcurrentUploads int `json:"max_concurrent_uploads" gorm:"default:2"`
	// Key prefix inside the bucket (e.g. "clever-connect/").
	S3Prefix string `json:"s3_prefix" gorm:"default:'clever-connect/'"`
	// Stop seeding torrents that were offloaded to S3 (local data is gone).
	StopSeedingOnOffload bool `json:"stop_seeding_on_offload" gorm:"default:true"`
}

// TorrentFileOffload is the per-file ledger of torrent data secured in S3.
// It persists which torrent files were uploaded, under which S3 key, and
// whether their local copy has been evicted. Essential for restart resilience.
type TorrentFileOffload struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	InfoHash     string     `gorm:"type:varchar(40);index;not null" json:"info_hash"`
	FileIndex    int        `gorm:"not null" json:"file_index"`
	FilePath     string     `gorm:"type:text" json:"file_path"` // absolute local path
	RelPath      string     `gorm:"type:text" json:"rel_path"`  // path inside the torrent
	Size         int64      `json:"size"`
	S3Key        string     `gorm:"type:varchar(512)" json:"s3_key"`
	Uploaded     bool       `json:"uploaded" gorm:"default:false"`
	UploadedAt   *time.Time `json:"uploaded_at"`
	EvictedLocal bool       `json:"evicted_local" gorm:"default:false"`
	EvictedAt    *time.Time `json:"evicted_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// StorageLog records important storage guard events for the admin panel.
type StorageLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Level     string    `json:"level" gorm:"size:20"` // info, warn, error
	Source    string    `json:"source" gorm:"size:50"`
	Message   string    `gorm:"type:text" json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Universal Cloud Storage (rclone) Models
// ──────────────────────────────────────────────────────────────────────────────

// RcloneRemote describes one rclone-backed cloud provider endpoint (Drive,
// SFTP host, OneDrive, Dropbox, mega, ...). Credentials are stored purely as
// a JSON option map in the database and are injected into each rclone
// subprocess via RCLONE_CONFIG_<NAME>_<OPTION> environment variables — no
// rclone.conf is ever written to disk.
type RcloneRemote struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Short system name. Every rclone invocation references it as "<Name>:...".
	// Lower-case ASCII letters, digits, "_" and "-" only.
	Name string `gorm:"type:varchar(64);uniqueIndex;not null" json:"name"`
	// rclone provider type (e.g. "sftp", "drive", "onedrive", "ftp").
	Type string `gorm:"type:varchar(64)" json:"type"`
	// RootPrefix is a bucket/directory fragment inserted between the remote name
	// and the per-upload destination path (e.g. "backups/cleverconnect/").
	RootPrefix string `gorm:"type:varchar(255)" json:"root_prefix"`
	// ExtraFlags holds extra safe-listed rclone flags, one per line or
	// space-separated (e.g. "--transfers 4 --sftp-idle-timeout 10"). Validated
	// by internal/rclone — arbitrary shell syntax is rejected.
	ExtraFlags string `gorm:"type:text" json:"extra_flags"`
	// Enabled controls whether scheduler jobs may target this remote.
	Enabled bool `gorm:"default:true" json:"enabled"`
	// Options is the provider option set serialized as a JSON object string,
	// exactly what `rclone config providers` describes for the chosen Type.
	Options string `gorm:"type:text" json:"options"`
	// SecretFields caches which Options keys hold credentials (provider-defined
	// IsPassword flags plus built-in heuristics like "pass"/"token"/"secret").
	// They are masked in API responses and redacted from captured rclone output.
	SecretFields  StringArray `gorm:"type:text" json:"secret_fields"`
	LastTestOk    bool        `gorm:"default:false" json:"last_test_ok"`
	LastTestError string      `gorm:"type:text" json:"last_test_error"`
	LastTestAt    *time.Time  `json:"last_test_at"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// RcloneUpload is the per-file ledger of one cloud transfer: local source,
// remote destination, status, the provider-side metadata captured with
// `lsjson --stat` after a successful upload, and the public share link created
// with `link` when requested. Failed transfers keep the error detail so the
// admin panel can display and retry them.
type RcloneUpload struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	SchedulerJobID uint   `gorm:"index" json:"job_id"`
	RemoteID       uint   `gorm:"index" json:"remote_id"`
	RemoteName     string `gorm:"type:varchar(64)" json:"remote_name"`
	// Absolute local source path inside the file manager sandbox.
	LocalPath string `gorm:"type:varchar(512)" json:"local_path"`
	// LocalRelPath is the path relative to the file manager root; relayed to
	// the provider when keep-structure mirroring was requested.
	LocalRelPath string `gorm:"type:varchar(512)" json:"local_rel_path"`
	// RemotePath is the fully-qualified rclone destination: "name:root/dir/file".
	RemotePath     string `gorm:"type:varchar(600)" json:"remote_path"`
	Size           int64  `json:"size"`
	PublicLink     string `gorm:"type:varchar(2048)" json:"public_link"`
	LinkError      string `gorm:"type:text" json:"link_error"`
	ProviderMeta   string `gorm:"type:text" json:"provider_meta"` // raw lsjson --stat result
	TransferError  string `gorm:"type:text" json:"transfer_error"`
	ProviderFileID string `gorm:"type:varchar(512)" json:"provider_file_id"`
	// queued | uploading | success | failed
	Status string `gorm:"type:varchar(20);index;default:queued" json:"status"`
	// RequirePublicLink: when true a provider `link` was requested for the file.
	RequirePublicLink bool       `gorm:"default:false" json:"require_public_link"`
	TransferredAt     *time.Time `json:"transferred_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// RcloneFile is one object discovered on a provider remote and mirrored into
// the database by the reconciliation engine (internal/rclone.SyncRemoteFiles).
// It is the persistence layer that keeps files that exist on S3 and every
// other storage provider visible and accessible in the UI after app
// restarts: rows live in the SQL database, are refreshed from the provider on
// every sync, and are re-created from a provider listing even when the local
// transfer ledger was lost.
type RcloneFile struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	RemoteID uint `gorm:"index;uniqueIndex:idx_rclone_files_remote_path" json:"remote_id"`
	// Path is the object path relative to the remote's RootPrefix, with
	// forward slashes (e.g. "docs/report.pdf").
	Path string `gorm:"type:varchar(1024)" json:"path"`
	// PathHash is a stable SHA-256 of Path: together with RemoteID it forms
	// the unique identity of one provider object (long varchar paths cannot
	// be unique-indexed safely on MySQL utf8mb4).
	PathHash string `gorm:"type:varchar(64);uniqueIndex:idx_rclone_files_remote_path" json:"path_hash"`
	Name     string `gorm:"type:varchar(512)" json:"name"`
	IsDir    bool   `gorm:"default:false" json:"is_dir"`
	Size     int64  `json:"size"`
	MimeType string `gorm:"type:varchar(191)" json:"mime_type"`
	ModTime  *time.Time `json:"mod_time"`
	// ProviderFileID is the backend's native object/document ID from lsjson.
	ProviderFileID string `gorm:"type:varchar(512)" json:"provider_file_id"`
	// PublicLink is a cached provider share link ("" when none was issued).
	PublicLink string `gorm:"type:varchar(2048)" json:"public_link"`
	LinkError  string `gorm:"type:text" json:"link_error"`
	// LastSeenAt marks the last sync that observed the object on the
	// provider; SyncedAt records when its metadata was last written.
	LastSeenAt time.Time `json:"last_seen_at"`
	SyncedAt   time.Time `json:"synced_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// RcloneSystem is the singleton (ID=1) engine settings row for the universal
// cloud storage feature: which rclone binary to use and its state.
type RcloneSystem struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// AutoInstall enables automatic download of the current stable rclone
	// release from downloads.rclone.org when no usable binary is found.
	AutoInstall bool `gorm:"default:false" json:"auto_install"`
	// ManagedBinary records where the auto-installed binary lives (if any),
	// so it can be found across restarts and re-verified.
	ManagedBinary string    `gorm:"type:varchar(512)" json:"managed_binary"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ──────────────────────────────────────────────────────────────────────────────
// V2Ray Proxy Management Models
// ──────────────────────────────────────────────────────────────────────────────

// V2RayNode represents a managed remote VPS edge node (Server side)
type V2RayNode struct {
	gorm.Model
	Name           string  `json:"name"`
	Hostname       string  `json:"hostname"`
	IP             string  `json:"ip"`
	SSHPort        int     `json:"ssh_port" gorm:"default:22"`
	SSHCredentials string  `json:"ssh_credentials" gorm:"type:text"` // Encrypted private key or password
	Status         string  `json:"status" gorm:"default:'offline'"`  // online, offline, provisioning
	LastHeartbeat  int64   `json:"last_heartbeat"`
	CPUPct         float64 `json:"cpu_pct"`
	RAMPct         float64 `json:"ram_pct"`
}

// V2RayInbound represents a listening proxy endpoint (Server side)
type V2RayInbound struct {
	gorm.Model
	NodeID            uint   `json:"node_id" gorm:"index"`
	Tag               string `json:"tag" gorm:"size:191;uniqueIndex"`
	Protocol          string `json:"protocol"` // vless, vmess, trojan, shadowsocks, hysteria2
	Port              int    `json:"port"`
	Network           string `json:"network"`  // tcp, ws, grpc, xhttp
	TLSMode           string `json:"tls_mode"` // tls, reality, none
	SNI               string `json:"sni"`      // Reality target SNI or custom SNI
	RealityPrivateKey string `json:"reality_private_key"`
	RealityPublicKey  string `json:"reality_public_key"`
	RealityShortIDs   string `json:"reality_short_ids"` // comma-separated
	Path              string `json:"path"`              // websocket path or grpc service name
	FallbackDest      string `json:"fallback_dest"`     // local fallback destination (e.g. 127.0.0.1:80)
	Enabled           bool   `json:"enabled" gorm:"default:true"`
}

// V2RayUser represents a client proxy account (Server side)
type V2RayUser struct {
	gorm.Model
	InboundID    uint      `json:"inbound_id" gorm:"index"`
	UUID         string    `json:"uuid" gorm:"size:191;uniqueIndex"` // Used as proxy password/UUID
	Name         string    `json:"name"`
	GroupID      uint      `json:"group_id" gorm:"index"`
	TrafficLimit int64     `json:"traffic_limit" gorm:"default:0"` // in bytes, 0 = unlimited
	UsedUpload   int64     `json:"used_upload" gorm:"default:0"`
	UsedDownload int64     `json:"used_download" gorm:"default:0"`
	ExpiresAt    time.Time `json:"expires_at"`
	MaxIPs       int       `json:"max_ips" gorm:"default:0"` // 0 = unlimited
	SubToken     string    `json:"sub_token" gorm:"size:191;uniqueIndex"`
	Enabled      bool      `json:"enabled" gorm:"default:true"`
}

// V2RayTrafficLog tracks user data consumption (Server side)
type V2RayTrafficLog struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	UserID        uint      `json:"user_id" gorm:"index"`
	NodeID        uint      `json:"node_id" gorm:"index"`
	Timestamp     time.Time `json:"timestamp" gorm:"index"`
	UploadBytes   int64     `json:"upload_bytes"`
	DownloadBytes int64     `json:"download_bytes"`
}

// V2RayRoutingRule defines custom routing paths (Server side)
type V2RayRoutingRule struct {
	gorm.Model
	RuleType string `json:"rule_type"` // domain, ip, geosite, geoip
	Value    string `json:"value"`
	Action   string `json:"action"` // block, proxy, direct
	GroupID  uint   `json:"group_id" gorm:"index"`
	Priority int    `json:"priority" gorm:"default:0"`
}

// V2RaySecurityEvent stores events like Fail2Ban detections
type V2RaySecurityEvent struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Timestamp   time.Time `json:"timestamp" gorm:"index"`
	IPAddress   string    `json:"ip_address" gorm:"size:100;index"`
	EventType   string    `json:"event_type"`   // failed_auth, port_scan
	ActionTaken string    `json:"action_taken"` // banned, warned
}

// V2RayClientConfig stores saved server profiles on the client side
type V2RayClientConfig struct {
	ID                uint      `json:"ID" gorm:"primarykey"`
	CreatedAt         time.Time `json:"CreatedAt"`
	UpdatedAt         time.Time `json:"UpdatedAt"`
	Name              string    `json:"name"`
	Protocol          string    `json:"protocol"`
	Address           string    `json:"address"`
	Port              int       `json:"port"`
	UUID              string    `json:"uuid"`
	Network           string    `json:"network"`
	TLSSettings       string    `json:"tls_settings" gorm:"type:text"` // JSON representation of client TLS settings
	MuxEnabled        bool      `json:"mux_enabled" gorm:"default:false"`
	LatencyMs         int       `json:"latency_ms" gorm:"default:-1"`
	SubscriptionID    uint      `json:"subscription_id"`
	Priority          int       `json:"priority" gorm:"default:0"`
	IsActive          bool      `json:"is_active" gorm:"default:false"`
	PacketLoss        float64   `json:"packet_loss" gorm:"default:0"`
	DownloadSpeedMBps float64   `json:"download_speed_mbps" gorm:"default:0"`
	CdnProvider       string    `json:"cdn_provider"`
	PopLocation       string    `json:"pop_location"`
	CategoryID        uint      `json:"category_id"`
	SourceVector      string    `json:"source_vector"`
	CountryCode       string    `json:"country_code"`
}

// NodeCategory stores tags/folders to organize proxy configs
type NodeCategory struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Name      string    `json:"name" gorm:"size:191;uniqueIndex"`
	Type      string    `json:"type"` // "auto" or "custom"
	ColorHex  string    `json:"color_hex"`
}

// V2RayClientSubscription stores imported subscription links on the client side
type V2RayClientSubscription struct {
	gorm.Model
	Name           string    `json:"name"`
	URL            string    `gorm:"type:text;not null" json:"url"`
	UpdateInterval int       `json:"update_interval" gorm:"default:12"` // In hours
	LastUpdatedAt  time.Time `json:"last_updated_at"`
}

// V2RayClientFrontingMap stores domain fronting maps (Client side)
type V2RayClientFrontingMap struct {
	gorm.Model
	TargetDomain string `json:"target_domain" gorm:"size:191;uniqueIndex"`
	FrontDomain  string `json:"front_domain"`
	CDNIP        string `json:"cdn_ip"`
}

// V2RayClientSetting represents Key-Value settings (Client side)
type V2RayClientSetting struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Key   string `json:"key" gorm:"size:191;uniqueIndex"`
	Value string `json:"value"`
}

// IntArray represents an array of integers that GORM can persist as JSON text in SQLite/MySQL
type IntArray []int

func (a IntArray) Value() (driver.Value, error) {
	if a == nil {
		return "[]", nil
	}
	bytes, err := json.Marshal(a)
	return string(bytes), err
}

func (a *IntArray) Scan(src interface{}) error {
	var bytes []byte
	switch v := src.(type) {
	case string:
		bytes = []byte(v)
	case []byte:
		bytes = v
	case nil:
		*a = nil
		return nil
	default:
		return fmt.Errorf("failed to scan IntArray: %v", src)
	}
	return json.Unmarshal(bytes, a)
}

// StringArray represents an array of strings that GORM can persist as JSON text in SQLite/MySQL
type StringArray []string

func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return "[]", nil
	}
	bytes, err := json.Marshal(a)
	return string(bytes), err
}

func (a *StringArray) Scan(src interface{}) error {
	var bytes []byte
	switch v := src.(type) {
	case string:
		bytes = []byte(v)
	case []byte:
		bytes = v
	case nil:
		*a = nil
		return nil
	default:
		return fmt.Errorf("failed to scan StringArray: %v", src)
	}
	return json.Unmarshal(bytes, a)
}

// V2RayScannerConfig stores the parameters of the scanner engine for persistence
type V2RayScannerConfig struct {
	gorm.Model
	ConcurrencyLimit  int         `json:"concurrency_limit" gorm:"default:100"`
	TotalTargetCount  int         `json:"total_target_count" gorm:"default:1000"`
	NetworkTimeoutSec int         `json:"network_timeout_sec" gorm:"default:5"`
	ProbeAttempts     int         `json:"probe_attempts" gorm:"default:1"`
	Ports             IntArray    `json:"ports" gorm:"type:text"`
	ConfigURLs        StringArray `json:"config_urls" gorm:"type:text"`
	TopLimit          int         `json:"top_limit" gorm:"default:20"`
	EnableNeighbors   bool        `json:"enable_neighbors" gorm:"default:false"`
	RequireWS         bool        `json:"require_ws" gorm:"default:false"`
	WebSocketHost     string      `json:"websocket_host"`
	WebSocketPath     string      `json:"websocket_path"`
	TargetCIDRs       StringArray `json:"target_cidrs" gorm:"type:text"`
	TargetCDNs        StringArray `json:"target_cdns" gorm:"type:text"`
	TargetMode        string      `json:"target_mode"`
	TargetSNI         string      `json:"target_sni"`
	MaxRateLimit      float64     `json:"max_rate_limit" gorm:"default:0"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Network Tools - Domain Checker Models
// ──────────────────────────────────────────────────────────────────────────────

type Domain struct {
	ID            string    `gorm:"primaryKey" json:"id"` // UUID
	DomainName    string    `gorm:"size:191;uniqueIndex;not null" json:"domain_name"`
	Status        string    `json:"status" gorm:"size:50;index"`                  // pending, checking, online, offline, timeout, nxdomain
	Category      string    `json:"category" gorm:"size:100;index;default:'ALL'"` // Category name, default 'ALL'
	IPAddresses   string    `json:"ip_addresses"`
	HTTPStatus    int       `json:"http_status"`
	LatencyMs     int       `json:"latency_ms" gorm:"index"`
	TLSStatus     bool      `json:"tls_status"`
	TLSExpiryDays int       `json:"tls_expiry_days"`
	LastCheckedAt time.Time `json:"last_checked_at" gorm:"index"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ScannerSource struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Name        string     `json:"name"`
	URL         string     `json:"url"`
	Type        string     `json:"type"` // Enum: cidr, proxyip, domain
	IsEnabled   bool       `json:"is_enabled" gorm:"default:true"`
	LastFetched *time.Time `json:"last_fetched"`
}

type ScannerConfig struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	DeepTestEnabled     bool      `json:"deep_test_enabled" gorm:"default:true"`
	TargetSNI           string    `json:"target_sni"`
	AttemptCount        int       `json:"attempt_count" gorm:"default:3"`
	MinSuccessThreshold int       `json:"min_success_threshold" gorm:"default:2"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Geo Geolocation & CDN Registry Models
// ──────────────────────────────────────────────────────────────────────────────

type IPRegistry struct {
	IP          string    `gorm:"primaryKey;size:100" json:"ip"`
	CountryCode string    `gorm:"size:10;index" json:"country_code"`
	CountryName string    `gorm:"size:100;index" json:"country_name"`
	City        string    `gorm:"size:100;index" json:"city"`
	ISP         string    `gorm:"size:255" json:"isp"`
	CDNProvider string    `gorm:"size:100;index" json:"cdn_provider"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	IsCDN       bool      `gorm:"default:false;index" json:"is_cdn"`
	LastUpdated time.Time `json:"last_updated"`
}

// IPLookupConfig stores the credentials and toggles for IP/Domain Whois APIs
type IPLookupConfig struct {
	gorm.Model
	IP2LocationKey      string `json:"ip2location_key"`
	IpApiKey            string `json:"ip_api_key"`
	IpGeolocationKey    string `json:"ip_geolocation_key"`
	IpWhoisKey          string `json:"ip_whois_key"`
	FindIPKey           string `json:"find_ip_key"`
	EnableIP2Location   bool   `json:"enable_ip2location" gorm:"default:true"`
	EnableIpApi         bool   `json:"enable_ip_api" gorm:"default:true"`
	EnableIpGeolocation bool   `json:"enable_ip_geolocation" gorm:"default:true"`
	EnableIpWhois       bool   `json:"enable_ip_whois" gorm:"default:true"`
	EnableFindIP        bool   `json:"enable_find_ip" gorm:"default:true"`
}

// IPIntelligenceCache caches the fully compiled details of previously resolved IPs
type IPIntelligenceCache struct {
	IP          string    `gorm:"primaryKey;size:100" json:"ip"`
	Country     string    `json:"country"`
	CountryCode string    `json:"country_code"`
	City        string    `json:"city"`
	ASN         string    `json:"asn"`
	ISP         string    `json:"isp"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	ProxyStatus string    `json:"proxy_status"` // VPN/Tor/DCH/Clean
	RawJSON     string    `gorm:"type:text" json:"raw_json"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DomainWhoisCache caches Domain WHOIS ownership query details
type DomainWhoisCache struct {
	DomainName   string    `gorm:"primaryKey;size:191" json:"domain_name"`
	Registrar    string    `json:"registrar"`
	CreationDate string    `json:"creation_date"`
	ExpiryDate   string    `json:"expiry_date"`
	NameServers  string    `json:"name_servers"`
	RawJSON      string    `gorm:"type:text" json:"raw_json"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Dynamic Multipath Bonding (DMB) Engine Models
// ──────────────────────────────────────────────────────────────────────────────

// BondingEngineConfig stores the global configuration for the multipath bonding
// sub-system. This is a singleton row — only one config exists at a time.
// It controls both Mode A (Selector/Failover) and Mode B (True Bonding/DMB).
type BondingEngineConfig struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	IsActive     bool   `json:"is_active" gorm:"default:false"`
	Mode         string `json:"mode" gorm:"size:30;default:'selector'"`      // "selector" | "bonding"
	StripingMode string `json:"striping_mode" gorm:"size:30;default:'auto'"` // "auto" | "stripe" | "duplicate"
	MaxArteries  int    `json:"max_arteries" gorm:"default:5"`
	MinArteries  int    `json:"min_arteries" gorm:"default:2"`
	CombinerURL  string `json:"combiner_url" gorm:"type:text"`   // wss://app.clever:8080/bond (bonding mode only)
	OriginID     string `json:"origin_id" gorm:"size:100"`       // bonding group origin identity; arteries must match
	PSKHex       string `json:"psk_hex" gorm:"type:text"`        // optional inner AEAD PSK; empty = rely on artery TLS
	FrameSize    int    `json:"frame_size" gorm:"default:4096"`  // on-wire frame size, 2-4KB recommended
	SocksPort    int    `json:"socks_port" gorm:"default:10646"` // user-facing SOCKS5 proxy port
	HTTPPort     int    `json:"http_port" gorm:"default:10545"`  // user-facing HTTP proxy port
	// Controller tuning thresholds (tunable without rebuild)
	EvalWindowMs  int       `json:"eval_window_ms" gorm:"default:5000"` // evaluation window in ms
	DemoteRTTx    float64   `json:"demote_rtt_x" gorm:"default:1.5"`    // srtt > DemoteRTTx × median → demote
	PromoteRTTx   float64   `json:"promote_rtt_x" gorm:"default:1.2"`   // srtt within PromoteRTTx × best → promote
	LossDemotePct float64   `json:"loss_demote_pct" gorm:"default:5.0"` // loss% threshold for demotion
	CooldownSec   int       `json:"cooldown_sec" gorm:"default:30"`     // cooldown before re-promotion
	ErrorBudget   int       `json:"error_budget" gorm:"default:5"`      // K failures in window → quarantine
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// BondingArtery represents a V2Ray configuration line currently active in the
// bonding pool. It maps a scanner-discovered node to a local dokodemo-door
// inbound port and tracks real-time performance metrics.
type BondingArtery struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	NodeConfigID   uint      `json:"node_config_id"`                        // references V2RayClientConfig from PebbleDB
	Tag            string    `json:"tag" gorm:"size:50"`                    // "artery-0", "artery-1", ...
	LocalPort      int       `json:"local_port"`                            // 21001, 21002, ...
	State          string    `json:"state" gorm:"size:30;default:'active'"` // active|shadow|probation|dead|quarantined
	WinRate        float64   `json:"win_rate" gorm:"default:0"`             // percentage of packet races won
	SrttMs         float64   `json:"srtt_ms" gorm:"default:0"`              // smoothed round-trip time
	LossPct        float64   `json:"loss_pct" gorm:"default:0"`             // packet loss percentage
	ThroughputMBps float64   `json:"throughput_mbps" gorm:"default:0"`      // measured throughput
	BytesUp        uint64    `json:"bytes_up" gorm:"default:0"`             // cumulative upload bytes
	BytesDown      uint64    `json:"bytes_down" gorm:"default:0"`           // cumulative download bytes
	ErrorCount     int       `json:"error_count" gorm:"default:0"`          // consecutive error count
	LastSwapAt     time.Time `json:"last_swap_at"`                          // last time this artery's node was swapped
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Cloudflare WARP+ Core Engine Models
// ──────────────────────────────────────────────────────────────────────────────

// WarpGlobalConfig stores the global WARP+ engine configuration.
// This is a singleton row — only one config exists at a time.
type WarpGlobalConfig struct {
	gorm.Model
	ActiveAccountID uint   `json:"active_account_id"`                              // FK to the currently executing WarpAccount
	TransportMode   string `json:"transport_mode" gorm:"size:50;default:'masque'"` // masque, masque_h2, wireguard
	TargetSNI       string `json:"target_sni" gorm:"size:255;default:'consumer-masque.cloudflareclient.com'"`
	SocksPort       int    `json:"socks_port" gorm:"default:10880"`    // Local SOCKS5 proxy port
	HTTPPort        int    `json:"http_port" gorm:"default:10881"`     // Local HTTP proxy port
	IsActive        bool   `json:"is_active" gorm:"default:false"`     // Whether the engine is running
	LastTraceOK     bool   `json:"last_trace_ok" gorm:"default:false"` // Last captive portal trace status
}

// WarpAccount stores a Cloudflare WARP account profile in the fleet pool.
// Multiple accounts can be registered; only one is active at a time
// (referenced by WarpGlobalConfig.ActiveAccountID).
type WarpAccount struct {
	gorm.Model
	LicenseKey       string `json:"license_key" gorm:"size:30"`                 // 26-char WARP+ license token
	DeviceID         string `json:"device_id" gorm:"size:191;uniqueIndex"`      // Unique device identity from CF registration
	Token            string `json:"token" gorm:"type:text"`                     // JWT Bearer token from CF edge
	PrivateKey       string `json:"private_key" gorm:"type:text"`               // Base64 Curve25519 private key (ours)
	PublicKey        string `json:"public_key" gorm:"size:255"`                 // Base64 Curve25519 public key (ours)
	PeerPublicKey    string `json:"peer_public_key" gorm:"size:255"`            // Cloudflare's WireGuard server public key
	ClientID         string `json:"client_id" gorm:"size:30"`                   // Base64 3-byte billing prefix (reserved bytes)
	AssignedIPv4     string `json:"assigned_ipv4" gorm:"size:20"`               // WARP virtual IPv4 (e.g. 172.16.0.2)
	AssignedIPv6     string `json:"assigned_ipv6" gorm:"size:50"`               // WARP virtual IPv6
	AccountType      string `json:"account_type" gorm:"size:20;default:'free'"` // free, premium, warp_plus
	TotalQuota       int64  `json:"total_quota" gorm:"default:0"`               // Total data allocation in bytes
	UsedQuota        int64  `json:"used_quota" gorm:"default:0"`                // Consumed data allocation in bytes
	IsFunctional     bool   `json:"is_functional" gorm:"default:true"`          // Invalidated on registration failure
	MasquePrivateKey string `json:"masque_private_key"`
	MasquePublicKey  string `json:"masque_public_key"`
	MasqueActive     bool   `json:"masque_active"`
}
