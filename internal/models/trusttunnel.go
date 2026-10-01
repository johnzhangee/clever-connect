package models

import "gorm.io/gorm"

// ──────────────────────────────────────────────────────────────────────────────
// TrustTunnel Stealth Overlay Protocol Models
// ──────────────────────────────────────────────────────────────────────────────

// TrustTunnelConfig stores the unified configuration for the TrustTunnel
// stealth VPN protocol. This is a singleton row — only one config exists
// at a time. It covers both server-mode (endpoint) and client-mode settings.
//
// Iran-specific DPI evasion parameters (forced_transport, auth_failure_status_code,
// client_random_prefix, h2 window sizes) are pre-tuned for maximum stealth
// under harsh censorship conditions.
type TrustTunnelConfig struct {
	gorm.Model

	// Lifecycle
	IsActive bool `json:"is_active" gorm:"default:false"` // Auto-start on boot

	// Network Binding
	ListenAddress  string `json:"listen_address" gorm:"default:'0.0.0.0:443'"`  // Server: bind address
	ConnectAddress string `json:"connect_address"`                               // Client: remote endpoint
	Socks5Port     int    `json:"socks5_port" gorm:"default:1088"`              // Client: local SOCKS5 proxy port
	HttpPort       int    `json:"http_port" gorm:"default:1089"`                // Client: local HTTP proxy port

	// Transport & Obfuscation
	ForcedTransport        string `json:"forced_transport" gorm:"default:'http2'"`       // http2, http1, quic (quic blocked in Iran)
	AuthFailureStatusCode  int    `json:"auth_failure_status_code" gorm:"default:407"`   // Active probe camouflage: 407, 405
	ClientRandomPrefix    string `json:"client_random_prefix" gorm:"default:'a0b0/f0f0'"` // TLS handshake entropy mask (hex)
	H2InitialStreamWindowSize   int `json:"h2_initial_stream_window_size" gorm:"default:131072"`  // Chrome fingerprint: 131072 bytes
	H2InitialConnWindowSize     int `json:"h2_initial_conn_window_size" gorm:"default:262144"`    // Connection flow control window
	TlsHandshakeTimeoutSecs     int `json:"tls_handshake_timeout_secs" gorm:"default:4"`          // Slow-loris protection

	// Safety
	KillSwitchEnabled bool `json:"kill_switch_enabled" gorm:"default:false"` // Client: block cleartext on disconnect

	// Presets
	ActivePreset string `json:"active_preset" gorm:"default:'iran-stealth'"` // iran-stealth, standard-web, minimal-cover

	// TLS (Server mode)
	TlsCertPath    string `json:"tls_cert_path"`    // Path to TLS certificate
	TlsKeyPath     string `json:"tls_key_path"`     // Path to TLS private key
	ServerHostname string `json:"server_hostname"`  // Public SNI hostname
	PublicTlsPort  int    `json:"public_tls_port" gorm:"default:0"` // Public port override (e.g., Clever Cloud redirection port)

	// Client Mode Credentials and Certificate
	ClientUsername string `json:"client_username"`
	ClientPassword string `json:"client_password"`
	TlsServerCert  string `json:"tls_server_cert"`
	SkipTlsVerify  bool   `json:"skip_tls_verify" gorm:"default:false"`
	DnsUpstreams   string `json:"dns_upstreams" gorm:"default:''"`
}

// TrustTunnelUser stores client authentication credentials for the
// TrustTunnel endpoint. Server-mode only — clients authenticate
// using these credentials when connecting to the endpoint.
type TrustTunnelUser struct {
	gorm.Model
	Username string `json:"username" gorm:"size:191;uniqueIndex;not null"`
	Password string `json:"password" gorm:"not null"` // bcrypt hashed
	IsActive bool   `json:"is_active" gorm:"default:true"`
}

// TrustTunnelFirewallRule defines IP-based routing exceptions for
// split-tunneling. Rules are evaluated in priority order (by ID).
type TrustTunnelFirewallRule struct {
	gorm.Model
	TargetCIDR     string `json:"target_cidr" gorm:"not null"`                    // e.g. "10.0.0.0/8", "192.168.0.0/16"
	BypassStrategy string `json:"bypass_strategy" gorm:"default:'direct-route'"` // allow, deny, direct-route
	Description    string `json:"description"`                                    // Human-readable note
}
