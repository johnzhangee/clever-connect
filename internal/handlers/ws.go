package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/config"
	"clever-connect/internal/db"
	"clever-connect/internal/dns"
	"clever-connect/internal/domainchecker"
	"clever-connect/internal/downloader"
	"clever-connect/internal/filecore"
	"clever-connect/internal/geo"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
	"clever-connect/internal/soroush"
	"clever-connect/internal/spotify"
	"clever-connect/internal/torrent"
	"clever-connect/internal/v2ray/scanner"
	"clever-connect/internal/youtube"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local networking app
	},
}

type WSHandler struct {
	cfg *config.Config
}

func NewWSHandler(cfg *config.Config) *WSHandler {
	return &WSHandler{cfg: cfg}
}

func (h *WSHandler) ServeWS(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Error("WS", "WebSocket upgrade failed",
			"error", err.Error(),
			"ip", c.ClientIP(),
		)
		return
	}
	defer conn.Close()

	logger.Info("WS", "Connection established",
		"mode", h.cfg.AppMode,
		"ip", c.ClientIP(),
	)

	// Channel to signal handler exit
	doneChan := make(chan struct{})

	// Registry listener
	clientID := fmt.Sprintf("ws-main-%d", time.Now().UnixNano())
	telemetryChan := make(chan gin.H, 200)

	scanner.GetEngine().RegisterListener(clientID, func(stats scanner.JobStats, event string, details interface{}) {
		payload := gin.H{
			"type":  "scanner:telemetry",
			"event": event,
			"stats": stats,
		}

		if details != nil {
			if detailMap, ok := details.(gin.H); ok {
				if dataField, exists := detailMap["data"]; exists {
					payload["data"] = dataField
				} else {
					payload["data"] = details
				}
			} else if dataMap, ok := details.(map[string]interface{}); ok {
				if dataField, exists := dataMap["data"]; exists {
					payload["data"] = dataField
				} else {
					payload["data"] = details
				}
			} else {
				payload["data"] = details
			}
		}

		select {
		case telemetryChan <- payload:
		default:
		}
	})
	defer func() {
		scanner.GetEngine().UnregisterListener(clientID)
		if h.cfg.AppMode == "client" && scanner.GetEngine().IsRunning() {
			logger.Info("WS", "User disconnected, stopping active scan sweep")
			scanner.GetEngine().CancelActiveScan()
		}
	}()

	// DNS Tester Listener
	dns.GetEngine().RegisterListener(clientID, func(stats dns.DNSJobStats, event string, details interface{}) {
		payload := gin.H{
			"type":  "dns:telemetry",
			"event": event,
			"stats": stats,
		}
		if details != nil {
			payload["data"] = details
		}
		select {
		case telemetryChan <- payload:
		default:
		}
	})
	defer func() {
		dns.GetEngine().UnregisterListener(clientID)
	}()

	domainchecker.GetEngine().RegisterListener(clientID, func(result domainchecker.DomainResult) {
		payload := gin.H{
			"type": "DOMAIN_CHECK_RESULT",
			"data": gin.H{
				"id":              result.ID,
				"domain_name":     result.DomainName,
				"status":          result.Status,
				"ip_addresses":    result.IPAddresses,
				"http_status":     result.HTTPStatus,
				"latency_ms":      result.LatencyMs,
				"tls_status":      result.TLSStatus,
				"tls_expiry_days": result.TLSExpiryDays,
				"last_checked_at": result.LastCheckedAt,
			},
		}
		select {
		case telemetryChan <- payload:
		default:
		}
	})
	defer domainchecker.GetEngine().UnregisterListener(clientID)

	// Geo Engine Listener
	geo.GetEngine().RegisterListener(clientID, func(ip string, reg *models.IPRegistry) {
		payload := gin.H{
			"type": "GEO_RESOLVED",
			"data": reg,
		}
		select {
		case telemetryChan <- payload:
		default:
		}
	})
	defer geo.GetEngine().UnregisterListener(clientID)

	// Read loop (to handle inbound actions like scanner:start, scanner:stop)
	go func() {
		defer close(doneChan)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var incoming struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(message, &incoming); err != nil {
				continue
			}

			switch incoming.Type {
			case "scanner:start":
				var req struct {
					TargetCIDRs        []string `json:"target_cidrs"`
					TargetCDNs         []string `json:"target_cdns"`
					SelectedPorts      []int    `json:"selected_ports"`
					ConcurrencyLimit   int      `json:"concurrency_limit"`
					MaxRateLimit       float64  `json:"max_rate_limit"`
					NetworkTimeoutMs   int      `json:"network_timeout_ms"`
					ProbeAttempts      int      `json:"probe_attempts"`
					TargetMode         string   `json:"target_mode"`
					TargetSNI          string   `json:"target_sni"`
					WebSocketHost      string   `json:"websocket_host"`
					WebSocketPath      string   `json:"websocket_path"`
					RequireWS          bool     `json:"require_ws"`
					EnableNeighbors    bool     `json:"enable_neighbors"`
					TopLimit           int      `json:"top_limit"`
					TotalTargetCount   int      `json:"total_target_count"`
					Retry              bool     `json:"retry"`
					ScanDiscoveredOnly bool     `json:"scan_discovered_only"`
				}
				if err := json.Unmarshal(incoming.Data, &req); err == nil {
					var scanCfg scanner.ScanConfig
					if req.Retry {
						var saved models.V2RayScannerConfig
						if db.DB != nil && db.DB.First(&saved, 1).Error == nil {
							scanCfg = scanner.ScanConfig{
								TargetCIDRs:      []string(saved.TargetCIDRs),
								TargetCDNs:       []string(saved.TargetCDNs),
								SelectedPorts:    []int(saved.Ports),
								ConcurrencyLimit: saved.ConcurrencyLimit,
								MaxRateLimit:     saved.MaxRateLimit,
								NetworkTimeout:   time.Duration(saved.NetworkTimeoutSec) * time.Second,
								ProbeAttempts:    saved.ProbeAttempts,
								TargetMode:       saved.TargetMode,
								TargetSNI:        saved.TargetSNI,
								WebSocketHost:    saved.WebSocketHost,
								WebSocketPath:    saved.WebSocketPath,
								RequireWS:        saved.RequireWS,
								EnableNeighbors:  saved.EnableNeighbors,
								TopLimit:         saved.TopLimit,
								TotalTargetCount: saved.TotalTargetCount,
							}
						}
					} else {
						if db.DB != nil {
							dbCfg := models.V2RayScannerConfig{
								ConcurrencyLimit:  req.ConcurrencyLimit,
								TotalTargetCount:  req.TotalTargetCount,
								NetworkTimeoutSec: req.NetworkTimeoutMs / 1000,
								ProbeAttempts:     req.ProbeAttempts,
								Ports:             models.IntArray(req.SelectedPorts),
								TargetCIDRs:       models.StringArray(req.TargetCIDRs),
								TargetCDNs:        models.StringArray(req.TargetCDNs),
								TargetMode:        req.TargetMode,
								TargetSNI:         req.TargetSNI,
								WebSocketHost:     req.WebSocketHost,
								WebSocketPath:     req.WebSocketPath,
								RequireWS:         req.RequireWS,
								EnableNeighbors:   req.EnableNeighbors,
								MaxRateLimit:      req.MaxRateLimit,
								TopLimit:          req.TopLimit,
							}
							dbCfg.ID = 1
							db.DB.Save(&dbCfg)
						}

						scanCfg = scanner.ScanConfig{
							TargetCIDRs:        req.TargetCIDRs,
							TargetCDNs:         req.TargetCDNs,
							SelectedPorts:      req.SelectedPorts,
							ConcurrencyLimit:   req.ConcurrencyLimit,
							MaxRateLimit:       req.MaxRateLimit,
							NetworkTimeout:     time.Duration(req.NetworkTimeoutMs) * time.Millisecond,
							ProbeAttempts:      req.ProbeAttempts,
							TargetMode:         req.TargetMode,
							TargetSNI:          req.TargetSNI,
							WebSocketHost:      req.WebSocketHost,
							WebSocketPath:      req.WebSocketPath,
							RequireWS:          req.RequireWS,
							EnableNeighbors:    req.EnableNeighbors,
							TopLimit:           req.TopLimit,
							TotalTargetCount:   req.TotalTargetCount,
							ScanDiscoveredOnly: req.ScanDiscoveredOnly,
						}
					}

					if scanCfg.NetworkTimeout <= 0 {
						scanCfg.NetworkTimeout = 5 * time.Second
					}
					_ = scanner.GetEngine().StartScan(c.Request.Context(), &scanCfg, req.Retry)
				}
			case "scanner:stop":
				scanner.GetEngine().CancelActiveScan()
			case "scanner:telemetry":
				stats := scanner.GetEngine().GetLiveStats()
				resp := gin.H{
					"type":  "scanner:telemetry",
					"event": "scanner.init",
					"stats": stats,
				}
				select {
				case telemetryChan <- resp:
				default:
				}
			case "dns:start":
				var req struct {
					ConcurrencyLimit  int                  `json:"concurrency_limit"`
					QPSLimit          int                  `json:"qps_limit"`
					TimeoutMs         int                  `json:"timeout_ms"`
					Attempts          int                  `json:"attempts"`
					CacheBusting      bool                 `json:"cache_busting"`
					ReferenceDomain   string               `json:"reference_domain"`
					SelectedProtocols []string             `json:"selected_protocols"`
					CustomResolvers   []models.DNSResolver `json:"custom_resolvers"`
					QueryTypes        []string             `json:"query_types"`
					DNSClass          string               `json:"dns_class"`
					QueryGenerator    string               `json:"query_generator"`
					DomainSource      string               `json:"domain_source"`
					CustomDomains     []string             `json:"custom_domains"`
					WordlistURL       string               `json:"wordlist_url"`
					ExpectResponse    interface{}          `json:"expect_response"`
				}
				if err := json.Unmarshal(incoming.Data, &req); err == nil {
					if req.ConcurrencyLimit <= 0 {
						req.ConcurrencyLimit = 100
					}
					if req.TimeoutMs <= 0 {
						req.TimeoutMs = 3000
					}
					if req.Attempts <= 0 {
						req.Attempts = 3
					}
					if req.ReferenceDomain == "" {
						req.ReferenceDomain = "google.com"
					}
					if req.DNSClass == "" {
						req.DNSClass = "IN"
					}
					if req.QueryGenerator == "" {
						req.QueryGenerator = "random"
					}
					if req.DomainSource == "" {
						req.DomainSource = "default"
					}

					var expectStr string
					if req.ExpectResponse != nil {
						if b, ok := req.ExpectResponse.(bool); ok {
							if b {
								expectStr = "true"
							} else {
								expectStr = ""
							}
						} else if s, ok := req.ExpectResponse.(string); ok {
							expectStr = s
						}
					}

					testerCfg := &models.DNSTesterConfig{
						ConcurrencyLimit: req.ConcurrencyLimit,
						QPSLimit:         req.QPSLimit,
						TimeoutMs:        req.TimeoutMs,
						Attempts:         req.Attempts,
						CacheBusting:     req.CacheBusting,
						ReferenceDomain:  req.ReferenceDomain,
						QueryTypes:       models.StringArray(req.QueryTypes),
						DNSClass:         req.DNSClass,
						QueryGenerator:   req.QueryGenerator,
						DomainSource:     req.DomainSource,
						CustomDomains:    models.StringArray(req.CustomDomains),
						WordlistURL:      req.WordlistURL,
						ExpectResponse:   expectStr,
					}
					_ = dns.GetEngine().StartTest(testerCfg, req.CustomResolvers, req.SelectedProtocols)
				}
			case "dns:stop":
				dns.GetEngine().StopTest()
			case "dns:telemetry":
				stats := dns.GetEngine().GetLiveStats()
				resp := gin.H{
					"type":  "dns:telemetry",
					"event": "dns.init",
					"stats": stats,
				}
				select {
				case telemetryChan <- resp:
				default:
				}
			case "dns:trace":
				var req struct {
					ResolverIP string `json:"resolver_ip"`
					Domain     string `json:"domain"`
				}
				if err := json.Unmarshal(incoming.Data, &req); err == nil {
					go func() {
						defer func() {
							if r := recover(); r != nil {
								logger.Error("WS", "Recovered from panic in dns:trace handler", "panic", r)
							}
						}()
						steps, err := dns.GetEngine().TraceDNS(c.Request.Context(), req.Domain, req.ResolverIP)
						resp := gin.H{
							"type":  "dns:trace_result",
							"steps": steps,
						}
						if err != nil {
							resp["error"] = err.Error()
						}
						select {
						case telemetryChan <- resp:
						default:
						}
					}()
				}
			case "dns:axfr":
				var req struct {
					ResolverIP string `json:"resolver_ip"`
					Domain     string `json:"domain"`
				}
				if err := json.Unmarshal(incoming.Data, &req); err == nil {
					go func() {
						defer func() {
							if r := recover(); r != nil {
								logger.Error("WS", "Recovered from panic in dns:axfr handler", "panic", r)
							}
						}()
						res, err := dns.GetEngine().TestAXFR(c.Request.Context(), req.Domain, req.ResolverIP)
						resp := gin.H{
							"type":   "dns:axfr_result",
							"result": res,
						}
						if err != nil {
							resp["error"] = err.Error()
						}
						select {
						case telemetryChan <- resp:
						default:
						}
					}()
				}
			case "bulk_lookup":
				var req struct {
					IPs []string `json:"ips"`
				}
				if err := json.Unmarshal(incoming.Data, &req); err == nil {
					go func(ips []string) {
						defer func() {
							if r := recover(); r != nil {
								logger.Error("WS", "Recovered from panic in bulk_lookup handler", "panic", r)
							}
						}()
						total := len(ips)
						if total == 0 {
							return
						}

						cfg, err := geo.GetIPLookupConfig()
						if err != nil {
							logger.Error("WS", "Failed to get config for bulk lookup", "error", err)
							return
						}

						var resolvedCount int
						batchSize := 10

						for i := 0; i < total; i += batchSize {
							end := i + batchSize
							if end > total {
								end = total
							}
							batch := ips[i:end]

							var batchWg sync.WaitGroup
							var batchMu sync.Mutex
							batchResults := make([]*geo.UnifiedIPResult, 0)

							for _, ip := range batch {
								ipClean := strings.TrimSpace(ip)
								if ipClean == "" {
									continue
								}

								batchWg.Add(1)
								go func(ipVal string) {
									defer func() {
										if r := recover(); r != nil {
											logger.Error("WS", "Recovered from panic in bulk_lookup worker", "panic", r)
										}
										batchWg.Done()
									}()

									// Validate IP address format
									if net.ParseIP(ipVal) == nil {
										return
									}

									// 1. Check cache first
									if cached, found := geo.QueryIPIntelligenceCache(ipVal); found {
										batchMu.Lock()
										batchResults = append(batchResults, cached)
										batchMu.Unlock()
										return
									}

									// 2. Resolve using concurrent aggregator
									geoRes, err := geo.ConcurrentGeoResolver(context.Background(), ipVal, cfg)
									if err == nil {
										geo.SaveIPToCache(geoRes)
										batchMu.Lock()
										batchResults = append(batchResults, geoRes)
										batchMu.Unlock()
									} else {
										logger.Warn("WS", "Bulk resolution failed for IP", "ip", ipVal, "error", err)
									}
								}(ipClean)
							}
							batchWg.Wait()

							resolvedCount += len(batchResults)

							// Stream batch progress back to client
							progressMsg := gin.H{
								"type":     "BULK_PROGRESS",
								"event":    "BULK_PROGRESS",
								"resolved": resolvedCount,
								"total":    total,
								"data":     batchResults,
							}
							select {
							case telemetryChan <- progressMsg:
							default:
							}
						}
					}(req.IPs)
				}
			}
		}
	}()

	// Write loop
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	totalDownload := 8120.0
	totalUpload := 2450.0

	for {
		select {
		case <-doneChan:
			return
		case msg := <-telemetryChan:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		case <-ticker.C:
			var msg interface{}
			if h.cfg.AppMode == "client" {
				downloadSpeed := float64(rand.Intn(80) + 10)
				uploadSpeed := float64(rand.Intn(20) + 2)
				latency := rand.Intn(15) + 35
				totalDownload += downloadSpeed / 10
				totalUpload += uploadSpeed / 10

				msg = gin.H{
					"type":           "bandwidth",
					"upload":         uploadSpeed,
					"download":       downloadSpeed,
					"totalDownload":  totalDownload,
					"totalUpload":    totalUpload,
					"latency":        latency,
					"soroush_tunnel": soroush.GetStatus(),
				}
			} else {
				sysStats := GetSystemStatsData()
				cpu := int(sysStats.CPUPercent)
				memory := int(sysStats.MemPercent)
				disk := int(sysStats.DiskPercent)

				var activeLeechCount int64
				db.DB.Model(&models.LeechJob{}).Where("status = ?", "downloading").Count(&activeLeechCount)

				var activeTorrentCount int64
				db.DB.Model(&models.TorrentJob{}).Count(&activeTorrentCount)

				var activeSchedulerCount int64
				db.DB.Model(&models.SchedulerJob{}).Where("status = ?", "running").Count(&activeSchedulerCount)

				downloadSpeed := float64(rand.Intn(120) + 40)
				uploadSpeed := float64(rand.Intn(40) + 10)

				totalDownload += downloadSpeed / 100
				totalUpload += uploadSpeed / 100

				clients := []gin.H{
					{"id": "1", "username": "salman_desktop", "ip": "82.102.23.45", "country": "Iran", "flag": "🇮🇷", "protocol": "VLESS-XTLS", "connectedAt": "12:04:12", "duration": "02h 35m", "uploadSpeed": float64(rand.Intn(10)+1) * 0.4, "downloadSpeed": float64(rand.Intn(30)+5) * 0.4, "active": true},
					{"id": "2", "username": "john_iphone", "ip": "188.45.67.12", "country": "Germany", "flag": "🇩🇪", "protocol": "Shadowsocks", "connectedAt": "13:10:00", "duration": "01h 29m", "uploadSpeed": float64(rand.Intn(5)+1) * 0.2, "downloadSpeed": float64(rand.Intn(15)+2) * 0.2, "active": true},
					{"id": "3", "username": "mary_macbook", "ip": "95.12.89.200", "country": "United Kingdom", "flag": "🇬🇧", "protocol": "Trojan", "connectedAt": "14:02:15", "duration": "37m", "uploadSpeed": float64(rand.Intn(4)+1) * 0.1, "downloadSpeed": float64(rand.Intn(10)+1) * 0.2, "active": true},
					{"id": "4", "username": "office_router", "ip": "104.22.4.90", "country": "United States", "flag": "🇺🇸", "protocol": "Wireguard", "connectedAt": "08:12:45", "duration": "06h 27m", "uploadSpeed": float64(rand.Intn(15)+5) * 0.3, "downloadSpeed": float64(rand.Intn(40)+10) * 0.3, "active": true},
				}

				msg = gin.H{
					"type":                 "telemetry",
					"cpu":                  cpu,
					"memory":               memory,
					"disk":                 disk,
					"connsCount":           len(clients),
					"uploadSpeed":          uploadSpeed,
					"downloadSpeed":        downloadSpeed,
					"totalDownload":        totalDownload,
					"totalUpload":          totalUpload,
					"clients":              clients,
					"cpu_cores_percent":    sysStats.CPUCoresPercent,
					"cpu_mhz":              sysStats.CPUMhz,
					"mem_total_gb":         sysStats.MemTotalGB,
					"mem_used_gb":          sysStats.MemUsedGB,
					"mem_free_gb":          sysStats.MemFreeGB,
					"swap_total_gb":        sysStats.SwapTotalGB,
					"swap_used_gb":         sysStats.SwapUsedGB,
					"swap_percent":         sysStats.SwapPercent,
					"disk_total_gb":        sysStats.DiskTotalGB,
					"disk_used_gb":         sysStats.DiskUsedGB,
					"disk_free_gb":         sysStats.DiskFreeGB,
					"disk_read_bytes_sec":  sysStats.DiskReadBytesSec,
					"disk_write_bytes_sec": sysStats.DiskWriteBytesSec,
					"net_recv_bytes_sec":   sysStats.NetRecvBytesSec,
					"net_sent_bytes_sec":   sysStats.NetSentBytesSec,
					"cpu_temp":             sysStats.CPUTemp,
					"uptime_seconds":       sysStats.UptimeSeconds,
					"boot_time":            sysStats.BootTime,
					"os_platform":          sysStats.OSPlatform,
					"os_kernel":            sysStats.OSKernel,
					"app_mem_mb":           sysStats.AppMemMB,
					"go_version":           runtime.Version(),
					"os_runtime":           runtime.GOOS,
					"active_leeches":       activeLeechCount,
					"active_torrents":      activeTorrentCount,
					"active_scheds":        activeSchedulerCount,
					"s3_enabled":           sysStats.S3Enabled,
					"s3_object_count":      sysStats.S3ObjectCount,
					"s3_total_size_gb":     sysStats.S3TotalSizeGB,
					"soroush_tunnel":       soroush.GetStatus(),
				}
			}

			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}

// ServeWSJobs upgraded stream sending and receiving torrent + leech jobs data
func (h *WSHandler) ServeWSJobs(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Error("WS", "WebSocket jobs upgrade failed",
			"error", err.Error(),
			"ip", c.ClientIP(),
		)
		return
	}
	defer conn.Close()

	logger.Info("WS", "Jobs WebSocket connection established",
		"mode", h.cfg.AppMode,
		"ip", c.ClientIP(),
	)

	if h.cfg.AppMode == "client" {
		// --- CLIENT MODE: PIPE/PROXY TO SERVER ---
		if h.cfg.ServerURL == "" {
			logger.Warn("WS", "No remote server connection configured for jobs proxy (missing SERVER_URL in environment)")
			return
		}

		remoteURLTarget := strings.TrimSpace(h.cfg.ServerURL)
		remoteToken := strings.TrimSpace(h.cfg.ServerAuthToken)

		// Convert http/https to ws/wss if needed
		remoteWS := remoteURLTarget
		remoteWS = strings.Replace(remoteWS, "https://", "wss://", 1)
		remoteWS = strings.Replace(remoteWS, "http://", "ws://", 1)
		if idx := strings.Index(remoteWS, "/ws"); idx != -1 {
			remoteWS = remoteWS[:idx]
		}
		if idx := strings.Index(remoteWS, "/tunnel"); idx != -1 {
			remoteWS = remoteWS[:idx]
		}
		remoteWS = strings.TrimSuffix(remoteWS, "/")
		remoteWS += "/ws/jobs?token=" + url.QueryEscape(remoteToken)

		headers := make(http.Header)
		if remoteToken != "" {
			headers.Set("Authorization", "Bearer "+remoteToken)
		}

		// Dial remote server websocket
		serverConn, _, err := websocket.DefaultDialer.Dial(remoteWS, headers)
		if err != nil {
			logger.Error("WS", "Failed to connect to remote server jobs WebSocket", "error", err.Error())
			return
		}
		defer serverConn.Close()

		// Run bidirectional piping
		errChan := make(chan error, 2)

		// Copy client -> server
		go func() {
			for {
				msgType, message, err := conn.ReadMessage()
				if err != nil {
					errChan <- err
					return
				}
				err = serverConn.WriteMessage(msgType, message)
				if err != nil {
					errChan <- err
					return
				}
			}
		}()

		// Copy server -> client
		go func() {
			for {
				msgType, message, err := serverConn.ReadMessage()
				if err != nil {
					errChan <- err
					return
				}
				err = conn.WriteMessage(msgType, message)
				if err != nil {
					errChan <- err
					return
				}
			}
		}()

		// Wait for error/closure
		<-errChan
		return
	}

	// --- SERVER MODE: REAL BUSINESS LOGIC ---
	// 1. Reader loop to handle incoming actions/commands
	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var cmd struct {
				Action      string `json:"action"`
				InfoHash    string `json:"info_hash,omitempty"`
				JobID       string `json:"job_id,omitempty"`
				DeleteFiles bool   `json:"delete_files,omitempty"`
			}
			if err := json.Unmarshal(message, &cmd); err != nil {
				continue
			}

			switch cmd.Action {
			case "pause_torrent":
				if cmd.InfoHash != "" && torrent.Manager != nil {
					torrent.Manager.PauseTorrent(cmd.InfoHash)
				}
			case "resume_torrent":
				if cmd.InfoHash != "" && torrent.Manager != nil {
					torrent.Manager.ResumeTorrent(cmd.InfoHash)
				}
			case "delete_torrent":
				if cmd.InfoHash != "" && torrent.Manager != nil {
					torrent.Manager.DeleteTorrent(cmd.InfoHash, cmd.DeleteFiles)
				}
			case "pause_leech":
				if cmd.JobID != "" && downloader.Manager != nil {
					downloader.Manager.PauseJob(cmd.JobID)
				}
			case "resume_leech":
				if cmd.JobID != "" {
					_ = db.DB.Model(&models.LeechJob{}).Where("id = ?", cmd.JobID).Update("status", "pending")
				}
			case "delete_leech":
				if cmd.JobID != "" && downloader.Manager != nil {
					downloader.Manager.DeleteJob(cmd.JobID, cmd.DeleteFiles)
				}
			case "cancel_youtube":
				if cmd.JobID != "" && youtube.Manager != nil {
					youtube.Manager.PauseJob(cmd.JobID)
				}
			case "delete_youtube":
				if cmd.JobID != "" && youtube.Manager != nil {
					youtube.Manager.DeleteJob(cmd.JobID, cmd.DeleteFiles)
				}
			case "cancel_spotify":
				if cmd.JobID != "" && spotify.Manager != nil {
					spotify.Manager.CancelJob(cmd.JobID)
				}
			case "delete_spotify":
				if cmd.JobID != "" && spotify.Manager != nil {
					spotify.Manager.DeleteJob(cmd.JobID, cmd.DeleteFiles)
				}
			case "retry_spotify":
				if cmd.JobID != "" && spotify.Manager != nil {
					spotify.Manager.RetryJob(cmd.JobID)
				}
			}
		}
	}()

	// 2. Ticker loop to push live updates of both lists (every 1 second for seamless fluidity)
	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var torrentList []models.TorrentJob
			var leechList []models.LeechJob
			var youtubeList []models.YouTubeJob
			var spotifyList []models.SpotifyJob

			// Fetch lists from database
			_ = db.DB.Order("created_at desc").Find(&torrentList)
			_ = db.DB.Order("created_at desc").Find(&leechList)
			_ = db.DB.Order("created_at desc").Find(&youtubeList)
			_ = db.DB.Order("created_at desc").Find(&spotifyList)

			// Populate FileExists for completed or seeding torrent jobs
			for i := range torrentList {
				torrentList[i].FileExists = true
				if torrentList[i].Status == "completed" || torrentList[i].Status == "seeding" {
					absSaveDir := filecore.GetAbsoluteSavePath(torrentList[i].SaveDirectory)
					destPath := filepath.Join(absSaveDir, torrentList[i].Name)
					if _, err := os.Stat(destPath); os.IsNotExist(err) {
						torrentList[i].FileExists = false
					}
				}

				// Report whether the torrent's files are archived in S3 object storage
				// (the local copy may have been removed by the stateless move-to-S3 flow).
				// Direct-S3 torrents keep their data directly in object storage.
				var s3Count int64
				db.DB.Model(&models.FileRegistry{}).
					Where("torrent_hash = ? AND s3_key <> ''", torrentList[i].InfoHash).
					Count(&s3Count)
				torrentList[i].S3Stored = s3Count > 0 || torrentList[i].DirectS3
			}

			// Populate FileExists for completed leech jobs
			for i := range leechList {
				leechList[i].FileExists = true
				if leechList[i].Status == "completed" {
					absSaveDir := filecore.GetAbsoluteSavePath(leechList[i].SaveDirectory)
					destPath := filepath.Join(absSaveDir, leechList[i].Filename)
					if _, err := os.Stat(destPath); os.IsNotExist(err) {
						leechList[i].FileExists = false
					}
				}
			}

			// Populate FileExists for completed youtube jobs
			for i := range youtubeList {
				youtubeList[i].FileExists = true
				if youtubeList[i].Status == "completed" {
					absSaveDir := filecore.GetAbsoluteSavePath(youtubeList[i].SaveDirectory)
					destPath := filepath.Join(absSaveDir, youtubeList[i].Filename)
					if _, err := os.Stat(destPath); os.IsNotExist(err) {
						youtubeList[i].FileExists = false
					}
				}
			}

			// Populate FileExists for completed spotify jobs
			for i := range spotifyList {
				spotifyList[i].FileExists = true
				if spotifyList[i].Status == "completed" {
					absSaveDir := filecore.GetAbsoluteSavePath(spotifyList[i].SaveDirectory)
					destPath := filepath.Join(absSaveDir, spotifyList[i].Filename)
					if _, err := os.Stat(destPath); os.IsNotExist(err) {
						spotifyList[i].FileExists = false
					}
				}
			}

			response := gin.H{
				"torrents":    torrentList,
				"leechJobs":   leechList,
				"youtubeJobs": youtubeList,
				"spotifyJobs": spotifyList,
			}

			if err := conn.WriteJSON(response); err != nil {
				return
			}
		}
	}
}
