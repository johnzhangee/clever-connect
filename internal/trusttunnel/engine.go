package trusttunnel

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"clever-connect/internal/db"
	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

var (
	cmdInstance  *exec.Cmd
	mu          sync.Mutex
	expectedRun bool          // whether the engine is expected to be running
	stopChan    chan struct{} // to signal supervisor loop to stop
	isServerMode bool
	restartDelay time.Duration
)

// getBinPath resolves the trusttunnel binary path alongside the main clever-connect binary.
func getBinPath(mode string) string {
	binName := "trusttunnel_client"
	if mode == "server" {
		binName = "trusttunnel_endpoint"
	}

	// 1. Try relative to the executable's directory
	if exe, err := os.Executable(); err == nil {
		path := filepath.Join(filepath.Dir(exe), binName)
		if _, err := os.Stat(path); err == nil {
			if absPath, err := filepath.Abs(path); err == nil {
				return absPath
			}
			return path
		}
	}

	// 2. Fall back to current working directory (e.g., bin/trusttunnel_client)
	path := filepath.Join("bin", binName)
	if _, err := os.Stat(path); err == nil {
		if absPath, err := filepath.Abs(path); err == nil {
			return absPath
		}
		return path
	}

	// 3. Fallback to just the binary name in case it's in the PATH
	return binName
}

// getConfigDir returns a writable temp directory for TrustTunnel TOML configs.
func getConfigDir() string {
	return filepath.Join(os.TempDir(), "clever-connect-data", "trusttunnel")
}

// generateServerTOML writes the TOML configuration files for the TrustTunnel endpoint.
func generateServerTOML(cfg *models.TrustTunnelConfig) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create trusttunnel config dir: %w", err)
	}

	// Format listen_protocols configuration based on forced transport
	var listenProtocols string
	switch cfg.ForcedTransport {
	case "http1":
		listenProtocols = `[listen_protocols.http1]
upload_buffer_size = 32768`
	case "quic":
		listenProtocols = `[listen_protocols.quic]
recv_udp_payload_size = 1350
send_udp_payload_size = 1350`
	default: // http2
		listenProtocols = fmt.Sprintf(`[listen_protocols.http2]
initial_connection_window_size = %d
initial_stream_window_size = %d`, cfg.H2InitialConnWindowSize, cfg.H2InitialStreamWindowSize)
	}

	// vpn.toml — Core server settings (root properties in Settings struct)
	vpnTOML := fmt.Sprintf(`listen_address = "%s"
tls_handshake_timeout_secs = %d
auth_failure_status_code = %d
credentials_file = "credentials.toml"
rules_file = "rules.toml"
ipv6_available = true
allow_private_network_connections = false

[listen_protocols]
%s
`,
		cfg.ListenAddress,
		cfg.TlsHandshakeTimeoutSecs,
		cfg.AuthFailureStatusCode,
		listenProtocols,
	)

	if err := os.WriteFile(filepath.Join(dir, "vpn.toml"), []byte(vpnTOML), 0644); err != nil {
		return fmt.Errorf("failed to write vpn.toml: %w", err)
	}

	// hosts.toml — TLS certificate configuration (uses main_hosts array).
	// The Rust daemon requires BOTH hostname AND cert_chain_path/private_key_path
	// as mandatory fields inside each [[main_hosts]] block. Writing a block that
	// omits either field causes an immediate panic in the Rust TOML parser.
	// We therefore validate upfront and return a clear error rather than writing
	// an invalid file.
	if cfg.ServerHostname == "" {
		return fmt.Errorf(
			"TrustTunnel server cannot start: ServerHostname is not configured. " +
				"Set the public hostname (e.g. vpn.example.com) in the TrustTunnel settings and try again.",
		)
	}
	if cfg.TlsCertPath == "" || cfg.TlsKeyPath == "" {
		logger.Warn("TrustTunnel", "TLS certificate paths not configured; generating fallback self-signed certificate", "hostname", cfg.ServerHostname)
		certPath, keyPath, err := GenerateSelfSignedCert(cfg.ServerHostname, "data")
		if err != nil {
			return fmt.Errorf("failed to generate fallback self-signed certificate: %w", err)
		}
		cfg.TlsCertPath = certPath
		cfg.TlsKeyPath = keyPath
		// Update DB config in place
		_ = db.DB.Model(cfg).Updates(map[string]interface{}{
			"tls_cert_path": certPath,
			"tls_key_path":  keyPath,
		})
	}

	hostsTOML := fmt.Sprintf(
		"[[main_hosts]]\nhostname = \"%s\"\ncert_chain_path = \"%s\"\nprivate_key_path = \"%s\"\n",
		cfg.ServerHostname,
		cfg.TlsCertPath,
		cfg.TlsKeyPath,
	)

	if err := os.WriteFile(filepath.Join(dir, "hosts.toml"), []byte(hostsTOML), 0644); err != nil {
		return fmt.Errorf("failed to write hosts.toml: %w", err)
	}

	// credentials.toml — User authentication (uses client array)
	var users []models.TrustTunnelUser
	db.DB.Where("is_active = ?", true).Find(&users)

	credsTOML := ""
	for _, u := range users {
		credsTOML += fmt.Sprintf(`[[client]]
username = "%s"
password = "%s"

`, u.Username, u.Password)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.toml"), []byte(credsTOML), 0644); err != nil {
		return fmt.Errorf("failed to write credentials.toml: %w", err)
	}

	// rules.toml — Firewall / bypass rules (uses rule array)
	rulesToml := ""
	if cfg.ClientRandomPrefix != "" {
		rulesToml += fmt.Sprintf(`[[rule]]
client_random_prefix = "%s"
action = "allow"

`, cfg.ClientRandomPrefix)
	}

	var rules []models.TrustTunnelFirewallRule
	db.DB.Find(&rules)

	for _, r := range rules {
		action := "allow"
		if r.BypassStrategy == "deny" {
			action = "deny"
		}
		rulesToml += fmt.Sprintf(`[[rule]]
cidr = "%s"
action = "%s"

`, r.TargetCIDR, action)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules.toml"), []byte(rulesToml), 0644); err != nil {
		return fmt.Errorf("failed to write rules.toml: %w", err)
	}

	return nil
}

// Default dummy self-signed certificate to satisfy client configuration parser when no cert is provided.
const defaultDummyCert = `-----BEGIN CERTIFICATE-----
MIIDFTCCAf2gAwIBAgIUTfhfRC7UppRit2j2n5hMfLwHhxowDQYJKoZIhvcNAQEL
BQAwGjEYMBYGA1UEAwwPdnBuLmV4YW1wbGUuY29tMB4XDTI2MDYyMTEzMTA0OFoX
DTI3MDYyMTEzMTA0OFowGjEYMBYGA1UEAwwPdnBuLmV4YW1wbGUuY29tMIIBIjAN
BgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA25zl0YjvRjBogEx8a2Pi59fcAscM
fNy0R+a9JkuU4I4VOayJij4lg9FbgewdGAkf/SYfvyebtnIxwbPYu6nBexyiR4aQ
GUtOc5WBUeM2lk2UvP/bjWPRSyVy9GUoB6Jx1I+rHS7CFYhIYqQ9lnGZDADfwjls
hYwXTB45B0vz+FtrUa7okaJ+FZI45jl1I/pc77ZwZExOg1KVSmBIdvnXpEIXwLgF
U+jzt//Kz7t/B4/buUTArOrEVsi/m/qSHRvvIdk5guERQ8Cvm4lMIu4fZ55h8UPg
1j53psZUrscELCY8Sx+ffuuzAXYxyicL4rnpXkWBP+II4LwgzUHl7QtQfwIDAQAB
o1MwUTAdBgNVHQ4EFgQUnjeZyKA14G/MPjrim7nvYGMs4uwwHwYDVR0jBBgwFoAU
njeZyKA14G/MPjrim7nvYGMs4uwwDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0B
AQsFAAOCAQEAj6mslRal+6xuFKSFlIuqdBGZOoExY5HtpYzOnK/8Pf+NPxhHEG1b
RQxcZ6L8e7TcPLElddJ/zH8liFpqRvwvxOJ3NtavhWyshPTRAIj5lDg42q1fSF2y
FQrRiLXovf2mtw+erxYAqGkVkV7pxWYP7XV2zciID33GKEYpGGy4JD7ugMpQvqPh
Fv+Mb+lXLPoYVRpXg1IFZIuCpgiU4Be8zh5H92xet1W4EHPF4w4771AhQjA8T/Cm
P+dJieDW44yHDkvJhp/JD85uJRT9oub12NgyGwxHiztXgHtiRg8FP7294+Mz5ZDr
9BWz6ZYwlYwjw8g5zUZeUkf6jqCYSahGDg==
-----END CERTIFICATE-----`

// ResolveConnectAddress resolves a client connection address from a raw address and server hostname.
// If the raw address has a wildcard or loopback host, it substitutes the public server hostname.
func ResolveConnectAddress(connectAddress, serverHostname string) string {
	connectAddr := connectAddress
	if connectAddr == "" {
		if serverHostname != "" {
			return serverHostname + ":443"
		}
		return "127.0.0.1:443"
	}

	// Split host and port
	host := connectAddr
	port := "443"
	if idx := strings.LastIndex(connectAddr, ":"); idx != -1 {
		host = connectAddr[:idx]
		port = connectAddr[idx+1:]
	}

	host = strings.Trim(host, "[]")
	if host == "0.0.0.0" || host == "::" || host == "127.0.0.1" || host == "localhost" || host == "" {
		if serverHostname != "" {
			return serverHostname + ":" + port
		}
	}
	return connectAddr
}

// generateClientTOML writes the TOML configuration files for the TrustTunnel client.
func generateClientTOML(cfg *models.TrustTunnelConfig) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create trusttunnel config dir: %w", err)
	}

	// Resolve endpoint hostname
	hostname := cfg.ServerHostname
	if hostname == "" {
		if idx := strings.LastIndex(cfg.ConnectAddress, ":"); idx != -1 {
			hostname = cfg.ConnectAddress[:idx]
			hostname = strings.Trim(hostname, "[]")
		} else {
			hostname = cfg.ConnectAddress
		}
	}
	if hostname == "" {
		hostname = "localhost"
	}

	// Determine skip_verification and certificate content
	skipVerification := cfg.SkipTlsVerify
	certContent := cfg.TlsServerCert
	if certContent == "" {
		certContent = defaultDummyCert
		skipVerification = true
	}

	// Format certificate to ensure it is trimmed and correct
	certContent = strings.TrimSpace(certContent)

	// Determine upstream protocol (http2 or http3)
	upstreamProtocol := cfg.ForcedTransport
	if upstreamProtocol != "http2" && upstreamProtocol != "http3" {
		upstreamProtocol = "http2"
	}

	// Resolve split-tunneling routes from database
	var rules []models.TrustTunnelFirewallRule
	db.DB.Find(&rules)

	var included []string
	var excluded []string
	for _, r := range rules {
		if r.BypassStrategy == "direct-route" {
			excluded = append(excluded, r.TargetCIDR)
		} else {
			included = append(included, r.TargetCIDR)
		}
	}

	var includedStr string
	if len(included) > 0 {
		var quoted []string
		for _, ip := range included {
			quoted = append(quoted, fmt.Sprintf(`"%s"`, ip))
		}
		includedStr = fmt.Sprintf("included_routes = [%s]\n", strings.Join(quoted, ", "))
	}

	var excludedStr string
	if len(excluded) > 0 {
		var quoted []string
		for _, ip := range excluded {
			quoted = append(quoted, fmt.Sprintf(`"%s"`, ip))
		}
		excludedStr = fmt.Sprintf("excluded_routes = [%s]\n", strings.Join(quoted, ", "))
	}

	resolvedConnectAddr := ResolveConnectAddress(cfg.ConnectAddress, cfg.ServerHostname)

	dnsUpstreamsStr := ""
	if cfg.DnsUpstreams != "" {
		var servers []string
		for _, s := range strings.Split(cfg.DnsUpstreams, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if !strings.Contains(s, ":") {
				s = s + ":53"
			}
			servers = append(servers, fmt.Sprintf(`"%s"`, s))
		}
		if len(servers) > 0 {
			dnsUpstreamsStr = fmt.Sprintf("\ndns_upstreams = [%s]", strings.Join(servers, ", "))
		}
	}

	clientTOML := fmt.Sprintf(`vpn_mode = "general"
killswitch_enabled = %t

[endpoint]
hostname = "%s"
addresses = ["%s"]%s
username = "%s"
password = "%s"
upstream_protocol = "%s"
client_random = "%s"
skip_verification = %t
load_certificate = """
%s
"""

[listener]
change_system_dns = false
%s%s
[listener.socks]
address = "127.0.0.1:%d"
`,
		cfg.KillSwitchEnabled,
		hostname,
		resolvedConnectAddr,
		dnsUpstreamsStr,
		cfg.ClientUsername,
		cfg.ClientPassword,
		upstreamProtocol,
		cfg.ClientRandomPrefix,
		skipVerification,
		certContent,
		includedStr,
		excludedStr,
		cfg.Socks5Port,
	)

	if err := os.WriteFile(filepath.Join(dir, "client.toml"), []byte(clientTOML), 0644); err != nil {
		return fmt.Errorf("failed to write client.toml: %w", err)
	}

	return nil
}

// StartServerEngine launches the TrustTunnel endpoint process with server configurations.
func StartServerEngine(cfg *models.TrustTunnelConfig) error {
	mu.Lock()
	defer mu.Unlock()

	if err := StopEngineLocked(); err != nil {
		return err
	}

	if err := startServerEngineLocked(cfg); err != nil {
		return err
	}

	expectedRun = true
	isServerMode = true
	restartDelay = 1 * time.Second
	stopChan = make(chan struct{})
	startSupervisor()

	return nil
}

// StartClientEngine launches the TrustTunnel client process.
func StartClientEngine(cfg *models.TrustTunnelConfig) error {
	mu.Lock()
	defer mu.Unlock()

	if err := StopEngineLocked(); err != nil {
		return err
	}

	if err := startClientEngineLocked(cfg); err != nil {
		return err
	}

	expectedRun = true
	isServerMode = false
	restartDelay = 1 * time.Second
	stopChan = make(chan struct{})
	startSupervisor()

	return nil
}

func startServerEngineLocked(cfg *models.TrustTunnelConfig) error {
	binPath := getBinPath("server")
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("trusttunnel_endpoint binary not found at %s: %w", binPath, err)
	}

	if err := generateServerTOML(cfg); err != nil {
		return fmt.Errorf("failed to generate server TOML configs: %w", err)
	}

	dir := getConfigDir()
	vpnPath := filepath.Join(dir, "vpn.toml")
	hostsPath := filepath.Join(dir, "hosts.toml")

	logger.Info("TrustTunnel", "Starting Endpoint Process",
		"listen", cfg.ListenAddress,
		"transport", cfg.ForcedTransport,
		"probe_mask", cfg.AuthFailureStatusCode,
	)

	cmdInstance = exec.Command(binPath, vpnPath, hostsPath)
	cmdInstance.Dir = dir
	// Assign the child to its own process group so that a kill targeting the
	// group (negative PID) also terminates any sub-processes it may spawn.
	// Pdeathsig ensures the child is killed if the parent Go process dies.
	cmdInstance.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}

	return startAndPipeProcess("TrustTunnelEndpoint")
}

func startClientEngineLocked(cfg *models.TrustTunnelConfig) error {
	binPath := getBinPath("client")
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("trusttunnel_client binary not found at %s: %w", binPath, err)
	}

	if err := generateClientTOML(cfg); err != nil {
		return fmt.Errorf("failed to generate client TOML configs: %w", err)
	}

	dir := getConfigDir()
	clientPath := filepath.Join(dir, "client.toml")

	logger.Info("TrustTunnel", "Starting Client Process",
		"connect", cfg.ConnectAddress,
		"socks5_port", cfg.Socks5Port,
		"http_port", cfg.HttpPort,
		"transport", cfg.ForcedTransport,
		"kill_switch", cfg.KillSwitchEnabled,
	)

	cmdInstance = exec.Command(binPath, "--config", clientPath)
	cmdInstance.Dir = dir
	// Same process-group isolation as the server engine.
	cmdInstance.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}

	return startAndPipeProcess("TrustTunnelClient")
}

// startAndPipeProcess starts the exec.Cmd and pipes stdout/stderr to the logger.
func startAndPipeProcess(tag string) error {
	stdoutPipe, err := cmdInstance.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	stderrPipe, err := cmdInstance.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := cmdInstance.Start(); err != nil {
		cmdInstance = nil
		return fmt.Errorf("failed to start %s process: %w", tag, err)
	}

	// Read stdout in background
	go func(pipe io.ReadCloser) {
		defer pipe.Close()
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			logger.Info(tag, scanner.Text())
		}
	}(stdoutPipe)

	// Read stderr in background
	go func(pipe io.ReadCloser) {
		defer pipe.Close()
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			logger.Error(tag, scanner.Text())
		}
	}(stderrPipe)

	// Reap process and log when it exits
	go func(cmd *exec.Cmd) {
		err := cmd.Wait()
		logger.Warn("TrustTunnel", "Subprocess exited", "error", err)
	}(cmdInstance)

	return nil
}

// StopEngine gracefully shuts down the active TrustTunnel process.
func StopEngine() {
	mu.Lock()
	defer mu.Unlock()
	_ = StopEngineLocked()
}

// StopEngineLocked stops the engine (caller must hold mu).
func StopEngineLocked() error {
	expectedRun = false
	if stopChan != nil {
		close(stopChan)
		stopChan = nil
	}
	return stopEngineLockedOnly()
}

func stopEngineLockedOnly() error {
	if cmdInstance != nil {
		logger.Info("TrustTunnel", "Terminating active TrustTunnel process")
		if cmdInstance.Process != nil {
			// Kill the entire process group (negative PID) so any child processes
			// spawned by the Rust binary are also terminated. This prevents orphaned
			// processes from holding onto ports across restarts on Clever Cloud.
			pid := cmdInstance.Process.Pid
			if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
				// Group kill failed (e.g. process already gone); fall back to direct kill
				if err := cmdInstance.Process.Signal(syscall.SIGTERM); err != nil {
					_ = cmdInstance.Process.Kill()
				}
			}
		}
		_ = cmdInstance.Wait()
		cmdInstance = nil
	}
	return nil
}

// IsRunning returns true if the engine process is active.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	if cmdInstance == nil || cmdInstance.Process == nil {
		return false
	}
	if cmdInstance.ProcessState != nil && cmdInstance.ProcessState.Exited() {
		return false
	}
	err := cmdInstance.Process.Signal(syscall.Signal(0))
	return err == nil
}

// startSupervisor spawns the background monitoring loop (must hold lock to setup/initialize variables).
func startSupervisor() {
	go func() {
		for {
			mu.Lock()
			if !expectedRun {
				mu.Unlock()
				return
			}

			// Check liveness
			isAlive := false
			if cmdInstance != nil && cmdInstance.Process != nil {
				if cmdInstance.ProcessState != nil && cmdInstance.ProcessState.Exited() {
					isAlive = false
				} else {
					err := cmdInstance.Process.Signal(syscall.Signal(0))
					isAlive = (err == nil)
				}
			}
			curStopChan := stopChan
			mu.Unlock()

			if !isAlive {
				logger.Warn("TrustTunnel", "Subprocess is not running. Initiating auto-restart.")

				mu.Lock()
				if restartDelay == 0 {
					restartDelay = 1 * time.Second
				} else {
					restartDelay *= 2
					if restartDelay > 30*time.Second {
						restartDelay = 30 * time.Second
					}
				}
				delay := restartDelay
				mu.Unlock()

				select {
				case <-curStopChan:
					return
				case <-time.After(delay):
				}

				mu.Lock()
				stillExpected := expectedRun
				mu.Unlock()

				if !stillExpected {
					return
				}

				if err := restartEngineFromDB(); err != nil {
					logger.Error("TrustTunnel", "Failed to auto-restart engine", "error", err)
				} else {
					logger.Info("TrustTunnel", "Subprocess auto-restarted successfully")
					mu.Lock()
					restartDelay = 1 * time.Second
					mu.Unlock()
				}
			} else {
				select {
				case <-curStopChan:
					return
				case <-time.After(3 * time.Second):
				}
			}
		}
	}()
}

// restartEngineFromDB loads settings from the database and restarts the process.
func restartEngineFromDB() error {
	mu.Lock()
	defer mu.Unlock()

	if !expectedRun {
		return nil
	}

	var cfg models.TrustTunnelConfig
	if err := db.DB.First(&cfg).Error; err != nil {
		return fmt.Errorf("failed to load TrustTunnel config from DB: %w", err)
	}
	// NOTE: We intentionally do NOT gate on cfg.IsActive here.
	// IsActive is only written by the UI toggle; it is not updated by the crash
	// supervisor. The in-memory expectedRun flag (checked above) is the correct
	// authority for whether the engine should keep running. Checking IsActive
	// caused every auto-restart to fail with "TrustTunnel config is no longer
	// active in DB" after an unexpected process crash on a fresh Clever Cloud deploy.

	_ = stopEngineLockedOnly()

	if isServerMode {
		return startServerEngineLocked(&cfg)
	}
	return startClientEngineLocked(&cfg)
}

// GenerateExportToken creates an encoded tt:// connection token string
// containing the server endpoint address and obfuscation parameters.
func GenerateExportToken(cfg *models.TrustTunnelConfig) string {
	params := url.Values{}
	
	addr := cfg.ListenAddress
	if cfg.PublicTlsPort > 0 {
		host := "0.0.0.0"
		if idx := strings.LastIndex(cfg.ListenAddress, ":"); idx != -1 {
			host = cfg.ListenAddress[:idx]
		}
		addr = fmt.Sprintf("%s:%d", host, cfg.PublicTlsPort)
	}
	
	params.Set("addr", addr)
	params.Set("hostname", cfg.ServerHostname)
	params.Set("transport", cfg.ForcedTransport)
	params.Set("probe", fmt.Sprintf("%d", cfg.AuthFailureStatusCode))
	params.Set("prefix", cfg.ClientRandomPrefix)
	params.Set("h2win", fmt.Sprintf("%d", cfg.H2InitialStreamWindowSize))
	params.Set("timeout", fmt.Sprintf("%d", cfg.TlsHandshakeTimeoutSecs))
	params.Set("skip_verify", fmt.Sprintf("%t", cfg.SkipTlsVerify))
	params.Set("dns_upstreams", cfg.DnsUpstreams)

	// Encode user credentials if any exist
	var users []models.TrustTunnelUser
	db.DB.Where("is_active = ?", true).Find(&users)
	if len(users) > 0 {
		params.Set("user", users[0].Username)
		params.Set("pass", users[0].Password)
	}

	// Read and encode public TLS certificate if configured
	if cfg.TlsCertPath != "" {
		if certBytes, err := os.ReadFile(cfg.TlsCertPath); err == nil {
			params.Set("cert", string(certBytes))
		}
	} else if cfg.TlsServerCert != "" {
		params.Set("cert", cfg.TlsServerCert)
	}

	raw := "tt://?" + params.Encode()
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// ParseImportToken decodes a tt:// encoded token and returns the parsed parameters.
func ParseImportToken(token string) (map[string]string, error) {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		// Try URL-safe base64
		decoded, err = base64.URLEncoding.DecodeString(token)
		if err != nil {
			return nil, fmt.Errorf("invalid token encoding: %w", err)
		}
	}

	raw := string(decoded)
	if !strings.HasPrefix(raw, "tt://") {
		return nil, fmt.Errorf("invalid token format: missing tt:// prefix")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token URL: %w", err)
	}

	result := make(map[string]string)
	for key, vals := range u.Query() {
		if len(vals) > 0 {
			result[key] = vals[0]
		}
	}

	return result, nil
}
