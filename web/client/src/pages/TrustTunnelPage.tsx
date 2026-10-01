import React, { useState, useEffect } from 'react';
import {
  FiSliders,
  FiCpu,
  FiGlobe,
  FiPlay,
  FiSquare,
  FiSave,
  FiRefreshCw,
  FiKey,
  FiPlus,
  FiTrash2,
  FiDownload,
  FiChevronDown,
  FiChevronUp,
  FiHelpCircle
} from 'react-icons/fi';
import { useTrustTunnelStore } from '../store/trusttunnelStore';

export const TrustTunnelPage: React.FC = () => {
  const {
    config,
    rules,
    isRunning,
    isLoading,
    error,
    successMessage,
    fetchConfig,
    saveConfig,
    startEngine,
    stopEngine,
    addRule,
    deleteRule,
    importConnectionToken,
    clearMessages
  } = useTrustTunnelStore();

  // Local Form state
  const [connectAddress, setConnectAddress] = useState('');
  const [socks5Port, setSocks5Port] = useState(1088);
  const [httpPort, setHttpPort] = useState(1089);
  const [forcedTransport, setForcedTransport] = useState<'http2' | 'http1' | 'quic'>('http2');
  const [authFailureStatusCode, setAuthFailureStatusCode] = useState(404);
  const [clientRandomPrefix, setClientRandomPrefix] = useState('a0b0/f0f0');
  const [h2InitialStreamWindowSize, setH2InitialStreamWindowSize] = useState(131072);
  const [h2InitialConnWindowSize, setH2InitialConnWindowSize] = useState(262144);
  const [tlsHandshakeTimeoutSecs, setTlsHandshakeTimeoutSecs] = useState(4);
  const [killSwitchEnabled, setKillSwitchEnabled] = useState(false);
  const [activePreset, setActivePreset] = useState('iran-stealth');
  const [serverHostname, setServerHostname] = useState('');
  const [clientUsername, setClientUsername] = useState('');
  const [clientPassword, setClientPassword] = useState('');
  const [tlsServerCert, setTlsServerCert] = useState('');
  const [skipTlsVerify, setSkipTlsVerify] = useState(false);
  const [dnsUpstreams, setDnsUpstreams] = useState('');

  // Token Import
  const [importTokenStr, setImportTokenStr] = useState('');

  // Add rule state
  const [newCidr, setNewCidr] = useState('');
  const [newStrategy, setNewStrategy] = useState('direct-route');
  const [newDesc, setNewDesc] = useState('');

  // UI state
  const [showAdvanced, setShowAdvanced] = useState(false);

  // Sync store configs with local state
  useEffect(() => {
    fetchConfig();
  }, []);

  useEffect(() => {
    if (config) {
      setConnectAddress(config.connect_address || '');
      setSocks5Port(config.socks5_port || 1088);
      setHttpPort(config.http_port || 1089);
      setForcedTransport(config.forced_transport || 'http2');
      setAuthFailureStatusCode(config.auth_failure_status_code || 404);
      setClientRandomPrefix(config.client_random_prefix || '');
      setH2InitialStreamWindowSize(config.h2_initial_stream_window_size || 131072);
      setH2InitialConnWindowSize(config.h2_initial_conn_window_size || 262144);
      setTlsHandshakeTimeoutSecs(config.tls_handshake_timeout_secs || 4);
      setKillSwitchEnabled(config.kill_switch_enabled || false);
      setActivePreset(config.active_preset || 'iran-stealth');
      setServerHostname(config.server_hostname || '');
      setClientUsername(config.client_username || '');
      setClientPassword(config.client_password || '');
      setTlsServerCert(config.tls_server_cert || '');
      setSkipTlsVerify(config.skip_tls_verify || false);
      setDnsUpstreams(config.dns_upstreams || '');
    }
  }, [config]);

  // Handle Preset application
  const applyPresetLocally = (presetName: string) => {
    setActivePreset(presetName);
    if (presetName === 'iran-stealth') {
      setForcedTransport('http2');
      setAuthFailureStatusCode(407);
      setClientRandomPrefix('a0b0/f0f0');
      setH2InitialStreamWindowSize(131072);
      setH2InitialConnWindowSize(262144);
      setTlsHandshakeTimeoutSecs(4);
      setKillSwitchEnabled(true);
    } else if (presetName === 'standard-web') {
      setForcedTransport('http2');
      setAuthFailureStatusCode(405);
      setClientRandomPrefix('');
      setH2InitialStreamWindowSize(65535);
      setH2InitialConnWindowSize(131072);
      setTlsHandshakeTimeoutSecs(10);
      setKillSwitchEnabled(false);
    } else if (presetName === 'minimal-cover') {
      setForcedTransport('http1');
      setAuthFailureStatusCode(407);
      setClientRandomPrefix('');
      setH2InitialStreamWindowSize(65535);
      setH2InitialConnWindowSize(131072);
      setTlsHandshakeTimeoutSecs(30);
      setKillSwitchEnabled(false);
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    await saveConfig({
      connect_address: connectAddress,
      socks5_port: Number(socks5Port),
      http_port: Number(httpPort),
      forced_transport: forcedTransport,
      auth_failure_status_code: Number(authFailureStatusCode),
      client_random_prefix: clientRandomPrefix,
      h2_initial_stream_window_size: Number(h2InitialStreamWindowSize),
      h2_initial_conn_window_size: Number(h2InitialConnWindowSize),
      tls_handshake_timeout_secs: Number(tlsHandshakeTimeoutSecs),
      kill_switch_enabled: killSwitchEnabled,
      active_preset: activePreset,
      server_hostname: serverHostname,
      client_username: clientUsername,
      client_password: clientPassword,
      tls_server_cert: tlsServerCert,
      skip_tls_verify: skipTlsVerify,
      dns_upstreams: dnsUpstreams,
      is_active: config?.is_active || false
    });
  };

  const handleToggleAutoStart = async () => {
    if (!config) return;
    await saveConfig({
      is_active: !config.is_active
    });
  };

  const handleAddRuleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newCidr) return;
    await addRule(newCidr, newStrategy, newDesc);
    setNewCidr('');
    setNewDesc('');
  };

  const handleImportTokenSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!importTokenStr) return;
    await importConnectionToken(importTokenStr);
    setImportTokenStr('');
  };

  return (
    <div>
      {/* Page Title */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
        <div>
          <h1 style={{ fontSize: 22, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>TrustTunnel Client</h1>
          <p style={{ fontSize: 12, color: 'var(--color-brand-text)', margin: '4px 0 0' }}>
            Stealth Layer-4 Obfuscation VPN Protocol using Chrome Fingerprint simulation.
          </p>
        </div>
        <button className="btn btn--sm" onClick={fetchConfig} disabled={isLoading}>
          <FiRefreshCw className={isLoading ? 'spin-animation' : ''} style={{ marginRight: 6 }} /> Refresh
        </button>
      </div>

      {/* Messages */}
      {error && (
        <div style={{
          padding: '12px 16px',
          borderRadius: 10,
          marginBottom: 20,
          fontSize: 13,
          fontWeight: 500,
          background: '#fee2e2',
          border: '1px solid #fca5a5',
          color: '#b91c1c',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center'
        }}>
          <span>{error}</span>
          <button onClick={clearMessages} style={{ background: 'none', border: 'none', color: '#b91c1c', cursor: 'pointer', fontWeight: 'bold' }}>X</button>
        </div>
      )}

      {successMessage && (
        <div style={{
          padding: '12px 16px',
          borderRadius: 10,
          marginBottom: 20,
          fontSize: 13,
          fontWeight: 500,
          background: 'var(--color-brand-light)',
          border: '1px solid var(--color-brand-border)',
          color: 'var(--color-brand)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center'
        }}>
          <span>{successMessage}</span>
          <button onClick={clearMessages} style={{ background: 'none', border: 'none', color: 'var(--color-brand)', cursor: 'pointer', fontWeight: 'bold' }}>X</button>
        </div>
      )}

      {/* Layout Grid */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 340px', gap: 24, alignItems: 'start' }}>

        {/* Left Column (Forms & Rules) */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>

          {/* Preset Selector */}
          <div className="g-card" style={{ padding: 20 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
              <FiSliders style={{ color: 'var(--color-brand)', fontSize: 18 }} />
              <span style={{ fontSize: 15, fontWeight: 600, color: 'var(--color-brand-heading)' }}>Select Defenses Preset</span>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12 }}>
              {/* Iran Stealth Preset */}
              <div
                onClick={() => applyPresetLocally('iran-stealth')}
                style={{
                  border: activePreset === 'iran-stealth' ? '2px solid var(--color-brand)' : '1px solid var(--color-brand-border)',
                  borderRadius: 10,
                  padding: 14,
                  cursor: 'pointer',
                  background: activePreset === 'iran-stealth' ? 'var(--color-brand-light)' : 'var(--color-brand-card)',
                  transition: 'all 0.2s ease'
                }}
              >
                <div style={{ fontWeight: 600, fontSize: 13, color: activePreset === 'iran-stealth' ? 'var(--color-brand)' : 'var(--color-brand-heading)', marginBottom: 4 }}>
                  🇮🇷 Iran Stealth (DPI Defense)
                </div>
                <div style={{ fontSize: 11, color: 'var(--color-brand-muted)', lineHeight: '1.4' }}>
                  HTTP2 + 404 camouflage, randomized prefix masking, Custom TLS flow windows, and Kill-Switch enabled.
                </div>
              </div>

              {/* Standard Web Emulation Preset */}
              <div
                onClick={() => applyPresetLocally('standard-web')}
                style={{
                  border: activePreset === 'standard-web' ? '2px solid var(--color-brand)' : '1px solid var(--color-brand-border)',
                  borderRadius: 10,
                  padding: 14,
                  cursor: 'pointer',
                  background: activePreset === 'standard-web' ? 'var(--color-brand-light)' : 'var(--color-brand-card)',
                  transition: 'all 0.2s ease'
                }}
              >
                <div style={{ fontWeight: 600, fontSize: 13, color: activePreset === 'standard-web' ? 'var(--color-brand)' : 'var(--color-brand-heading)', marginBottom: 4 }}>
                  🌐 Standard Web Emulation
                </div>
                <div style={{ fontSize: 11, color: 'var(--color-brand-muted)', lineHeight: '1.4' }}>
                  HTTP2 with normal window size, standard 401 unauthorized probe behavior, and longer handshakes.
                </div>
              </div>

              {/* Minimal Cover Mask Preset */}
              <div
                onClick={() => applyPresetLocally('minimal-cover')}
                style={{
                  border: activePreset === 'minimal-cover' ? '2px solid var(--color-brand)' : '1px solid var(--color-brand-border)',
                  borderRadius: 10,
                  padding: 14,
                  cursor: 'pointer',
                  background: activePreset === 'minimal-cover' ? 'var(--color-brand-light)' : 'var(--color-brand-card)',
                  transition: 'all 0.2s ease'
                }}
              >
                <div style={{ fontWeight: 600, fontSize: 13, color: activePreset === 'minimal-cover' ? 'var(--color-brand)' : 'var(--color-brand-heading)', marginBottom: 4 }}>
                  🛡️ Minimal Cover Mask
                </div>
                <div style={{ fontSize: 11, color: 'var(--color-brand-muted)', lineHeight: '1.4' }}>
                  HTTP1.1 transport, 407 proxy authentication error spoofing, and broad timeouts.
                </div>
              </div>
            </div>
          </div>

          {/* Connection Parameters Form */}
          <form onSubmit={handleSave} className="g-card" style={{ padding: 20, display: 'flex', flexDirection: 'column', gap: 16 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
              <FiGlobe style={{ color: 'var(--color-brand)', fontSize: 18 }} />
              <span style={{ fontSize: 15, fontWeight: 600, color: 'var(--color-brand-heading)' }}>Connection Parameters</span>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 180px 180px', gap: 16 }}>
              <div>
                <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Remote Endpoint Address</label>
                <input
                  type="text"
                  value={connectAddress}
                  onChange={(e) => setConnectAddress(e.target.value)}
                  placeholder="e.g. 104.21.32.44:443"
                  style={{
                    width: '100%',
                    padding: '10px 12px',
                    borderRadius: 8,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 13,
                    fontFamily: 'Fira Code'
                  }}
                  required
                />
              </div>

              <div>
                <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Local SOCKS5 Port</label>
                <input
                  type="number"
                  value={socks5Port}
                  onChange={(e) => setSocks5Port(Number(e.target.value))}
                  style={{
                    width: '100%',
                    padding: '10px 12px',
                    borderRadius: 8,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 13,
                    fontFamily: 'Fira Code'
                  }}
                  required
                />
              </div>

              <div>
                <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Local HTTP Port</label>
                <input
                  type="number"
                  value={httpPort}
                  onChange={(e) => setHttpPort(Number(e.target.value))}
                  style={{
                    width: '100%',
                    padding: '10px 12px',
                    borderRadius: 8,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 13,
                    fontFamily: 'Fira Code'
                  }}
                  required
                />
              </div>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
              <div>
                <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Server Hostname / SNI Domain Override</label>
                <input
                  type="text"
                  value={serverHostname}
                  onChange={(e) => setServerHostname(e.target.value)}
                  placeholder="e.g. my-secure-sni.com (optional)"
                  style={{
                    width: '100%',
                    padding: '10px 12px',
                    borderRadius: 8,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 13
                  }}
                />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>DNS Upstream Servers (comma-separated)</label>
                <input
                  type="text"
                  value={dnsUpstreams}
                  onChange={(e) => setDnsUpstreams(e.target.value)}
                  placeholder="e.g. 8.8.8.8, 1.1.1.1"
                  style={{
                    width: '100%',
                    padding: '10px 12px',
                    borderRadius: 8,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 13,
                    fontFamily: 'Fira Code'
                  }}
                />
              </div>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
              <div>
                <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Client Mode Username</label>
                <input
                  type="text"
                  value={clientUsername}
                  onChange={(e) => setClientUsername(e.target.value)}
                  placeholder="e.g. proxy-user"
                  style={{
                    width: '100%',
                    padding: '10px 12px',
                    borderRadius: 8,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 13
                  }}
                />
              </div>

              <div>
                <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Client Mode Password</label>
                <input
                  type="password"
                  value={clientPassword}
                  onChange={(e) => setClientPassword(e.target.value)}
                  placeholder="e.g. proxy-pass"
                  style={{
                    width: '100%',
                    padding: '10px 12px',
                    borderRadius: 8,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 13
                  }}
                />
              </div>
            </div>

            <div>
              <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Server TLS Certificate (PEM Override)</label>
              <textarea
                value={tlsServerCert}
                onChange={(e) => setTlsServerCert(e.target.value)}
                placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"
                style={{
                  width: '100%',
                  height: 100,
                  padding: '10px 12px',
                  borderRadius: 8,
                  border: '1px solid var(--color-brand-border)',
                  background: 'var(--color-brand-card)',
                  color: 'var(--color-brand-heading)',
                  fontSize: 12,
                  fontFamily: 'Fira Code',
                  resize: 'vertical'
                }}
              />
            </div>

            {/* Collapsible Advanced Settings */}
            <div style={{ borderTop: '1px solid var(--color-brand-border)', paddingTop: 16 }}>
              <button
                type="button"
                onClick={() => setShowAdvanced(!showAdvanced)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  background: 'none',
                  border: 'none',
                  color: 'var(--color-brand)',
                  cursor: 'pointer',
                  fontWeight: 600,
                  fontSize: 13,
                  padding: 0
                }}
              >
                {showAdvanced ? <FiChevronUp /> : <FiChevronDown />} Advanced Defensive Settings
              </button>

              {showAdvanced && (
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginTop: 16 }}>
                  <div>
                    <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Forced Transport Layer</label>
                    <select
                      value={forcedTransport}
                      onChange={(e) => setForcedTransport(e.target.value as any)}
                      style={{
                        width: '100%',
                        padding: '10px 12px',
                        borderRadius: 8,
                        border: '1px solid var(--color-brand-border)',
                        background: 'var(--color-brand-card)',
                        color: 'var(--color-brand-heading)',
                        fontSize: 13
                      }}
                    >
                      <option value="http2">HTTP/2 Multiplex (Recommended)</option>
                      <option value="http1">HTTP/1.1 Standard</option>
                      <option value="quic">QUIC / UDP Transport</option>
                    </select>
                  </div>

                  <div>
                    <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Probe Spoof Status Code</label>
                    <select
                      value={authFailureStatusCode}
                      onChange={(e) => setAuthFailureStatusCode(Number(e.target.value))}
                      style={{
                        width: '100%',
                        padding: '10px 12px',
                        borderRadius: 8,
                        border: '1px solid var(--color-brand-border)',
                        background: 'var(--color-brand-card)',
                        color: 'var(--color-brand-heading)',
                        fontSize: 13
                      }}
                    >
                      <option value={407}>407 Proxy Authentication Required</option>
                      <option value={405}>405 Method Not Allowed</option>
                    </select>
                  </div>

                  <div>
                    <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>Entropy Handshake Prefix</label>
                    <input
                      type="text"
                      value={clientRandomPrefix}
                      onChange={(e) => setClientRandomPrefix(e.target.value)}
                      placeholder="e.g. a0b0/f0f0"
                      style={{
                        width: '100%',
                        padding: '10px 12px',
                        borderRadius: 8,
                        border: '1px solid var(--color-brand-border)',
                        background: 'var(--color-brand-card)',
                        color: 'var(--color-brand-heading)',
                        fontSize: 13,
                        fontFamily: 'Fira Code'
                      }}
                    />
                  </div>

                  <div>
                    <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>TLS Handshake Timeout (seconds)</label>
                    <input
                      type="number"
                      value={tlsHandshakeTimeoutSecs}
                      onChange={(e) => setTlsHandshakeTimeoutSecs(Number(e.target.value))}
                      style={{
                        width: '100%',
                        padding: '10px 12px',
                        borderRadius: 8,
                        border: '1px solid var(--color-brand-border)',
                        background: 'var(--color-brand-card)',
                        color: 'var(--color-brand-heading)',
                        fontSize: 13,
                        fontFamily: 'Fira Code'
                      }}
                    />
                  </div>

                  <div>
                    <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>H2 Stream Initial Window</label>
                    <input
                      type="number"
                      value={h2InitialStreamWindowSize}
                      onChange={(e) => setH2InitialStreamWindowSize(Number(e.target.value))}
                      style={{
                        width: '100%',
                        padding: '10px 12px',
                        borderRadius: 8,
                        border: '1px solid var(--color-brand-border)',
                        background: 'var(--color-brand-card)',
                        color: 'var(--color-brand-heading)',
                        fontSize: 13,
                        fontFamily: 'Fira Code'
                      }}
                    />
                  </div>

                  <div>
                    <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', marginBottom: 6, textTransform: 'uppercase' }}>H2 Conn Initial Window</label>
                    <input
                      type="number"
                      value={h2InitialConnWindowSize}
                      onChange={(e) => setH2InitialConnWindowSize(Number(e.target.value))}
                      style={{
                        width: '100%',
                        padding: '10px 12px',
                        borderRadius: 8,
                        border: '1px solid var(--color-brand-border)',
                        background: 'var(--color-brand-card)',
                        color: 'var(--color-brand-heading)',
                        fontSize: 13,
                        fontFamily: 'Fira Code'
                      }}
                    />
                  </div>

                  <div style={{ gridColumn: 'span 2', display: 'flex', flexDirection: 'column', gap: 12, marginTop: 8 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <input
                        type="checkbox"
                        id="skipTlsVerify"
                        checked={skipTlsVerify}
                        onChange={(e) => setSkipTlsVerify(e.target.checked)}
                        style={{ width: 16, height: 16, cursor: 'pointer' }}
                      />
                      <label htmlFor="skipTlsVerify" style={{ fontSize: 13, fontWeight: 500, color: 'var(--color-brand-heading)', cursor: 'pointer' }}>
                        Skip TLS Verification (Recommended for custom or self-signed certs)
                      </label>
                    </div>

                    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <input
                        type="checkbox"
                        id="killSwitchEnabled"
                        checked={killSwitchEnabled}
                        onChange={(e) => setKillSwitchEnabled(e.target.checked)}
                        style={{ width: 16, height: 16, cursor: 'pointer' }}
                      />
                      <label htmlFor="killSwitchEnabled" style={{ fontSize: 13, fontWeight: 500, color: 'var(--color-brand-heading)', cursor: 'pointer' }}>
                        Enable Local Connection Kill-Switch (Prevent Leaks on drops)
                      </label>
                    </div>
                  </div>
                </div>
              )}
            </div>

            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 8 }}>
              <button type="submit" className="btn btn--primary btn--md" disabled={isLoading}>
                <FiSave style={{ marginRight: 6 }} /> Save Configuration
              </button>
            </div>
          </form>

          {/* Firewall / Bypass Rules Section */}
          <div className="g-card" style={{ padding: 20 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
              <FiCpu style={{ color: 'var(--color-brand)', fontSize: 18 }} />
              <span style={{ fontSize: 15, fontWeight: 600, color: 'var(--color-brand-heading)' }}>Bypass / Split-Tunneling Routing Rules</span>
            </div>

            <form onSubmit={handleAddRuleSubmit} style={{ display: 'grid', gridTemplateColumns: '1fr 160px 160px 80px', gap: 12, marginBottom: 18 }}>
              <div>
                <input
                  type="text"
                  placeholder="Target CIDR (e.g. 10.0.0.0/8, 185.12.0.0/16)"
                  value={newCidr}
                  onChange={(e) => setNewCidr(e.target.value)}
                  style={{
                    width: '100%',
                    padding: '8px 10px',
                    borderRadius: 6,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 12,
                    fontFamily: 'Fira Code'
                  }}
                  required
                />
              </div>

              <div>
                <select
                  value={newStrategy}
                  onChange={(e) => setNewStrategy(e.target.value)}
                  style={{
                    width: '100%',
                    padding: '8px 10px',
                    borderRadius: 6,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 12
                  }}
                >
                  <option value="direct-route">Bypass / Direct Route</option>
                  <option value="allow">Allow via Tunnel</option>
                  <option value="deny">Deny Traffic</option>
                </select>
              </div>

              <div>
                <input
                  type="text"
                  placeholder="Description"
                  value={newDesc}
                  onChange={(e) => setNewDesc(e.target.value)}
                  style={{
                    width: '100%',
                    padding: '8px 10px',
                    borderRadius: 6,
                    border: '1px solid var(--color-brand-border)',
                    background: 'var(--color-brand-card)',
                    color: 'var(--color-brand-heading)',
                    fontSize: 12
                  }}
                />
              </div>

              <button type="submit" className="btn btn--sm btn--primary" style={{ height: '100%' }}>
                <FiPlus size={14} style={{ marginRight: 4 }} /> Add
              </button>
            </form>

            <div className="table-responsive" style={{ border: '1px solid var(--color-brand-border)', borderRadius: 8 }}>
              <table className="table" style={{ margin: 0, fontSize: 12 }}>
                <thead>
                  <tr>
                    <th>Target Network IP (CIDR)</th>
                    <th>Strategy</th>
                    <th>Description</th>
                    <th style={{ width: 60, textAlign: 'center' }}>Action</th>
                  </tr>
                </thead>
                <tbody>
                  {rules.length === 0 ? (
                    <tr>
                      <td colSpan={4} style={{ textAlign: 'center', padding: '16px 8px', color: 'var(--color-brand-muted)' }}>
                        No split-tunneling routing overrides configured. All traffic routed via tunnel.
                      </td>
                    </tr>
                  ) : (
                    rules.map((rule) => (
                      <tr key={rule.id}>
                        <td style={{ fontFamily: 'Fira Code', fontWeight: 500 }}>{rule.target_cidr}</td>
                        <td>
                          <span style={{
                            padding: '2px 8px',
                            borderRadius: 12,
                            fontSize: 10,
                            fontWeight: 600,
                            background: rule.bypass_strategy === 'direct-route' ? 'var(--color-brand-light)' : rule.bypass_strategy === 'allow' ? '#dcfce7' : '#fee2e2',
                            color: rule.bypass_strategy === 'direct-route' ? 'var(--color-brand)' : rule.bypass_strategy === 'allow' ? '#15803d' : '#b91c1c'
                          }}>
                            {rule.bypass_strategy}
                          </span>
                        </td>
                        <td>{rule.description || '-'}</td>
                        <td style={{ textAlign: 'center' }}>
                          <button
                            onClick={() => deleteRule(rule.id)}
                            style={{
                              background: 'none',
                              border: 'none',
                              color: 'var(--color-brand-muted)',
                              cursor: 'pointer',
                              padding: 4
                            }}
                            onMouseOver={(e) => (e.currentTarget.style.color = '#ef4444')}
                            onMouseOut={(e) => (e.currentTarget.style.color = 'var(--color-brand-muted)')}
                          >
                            <FiTrash2 size={14} />
                          </button>
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </div>

        </div>

        {/* Right Column (Status & Controls & Import) */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>

          {/* Engine Status Card */}
          <div className="g-card" style={{ padding: 20, textAlign: 'center' }}>
            <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--color-brand-muted)', textTransform: 'uppercase', marginBottom: 12 }}>
              TrustTunnel Status
            </div>

            <div style={{
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              gap: 8,
              padding: '8px 16px',
              borderRadius: 24,
              fontWeight: 700,
              fontSize: 14,
              background: isRunning ? 'var(--color-brand-light)' : '#fee2e2',
              color: isRunning ? 'var(--color-brand)' : '#b91c1c',
              border: isRunning ? '1px solid var(--color-brand-border)' : '1px solid #fca5a5',
              marginBottom: 20
            }}>
              <span style={{
                width: 10,
                height: 10,
                borderRadius: '50%',
                background: isRunning ? 'var(--color-brand)' : '#ef4444',
                boxShadow: isRunning ? '0 0 10px var(--color-brand)' : '0 0 10px #ef4444',
                animation: isRunning ? 'pulse-animation 1.5s infinite' : 'none'
              }}></span>
              {isRunning ? 'DAEMON ACTIVE' : 'DAEMON INACTIVE'}
            </div>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {isRunning ? (
                <button className="btn btn--danger btn--md" onClick={stopEngine} disabled={isLoading} style={{ width: '100%' }}>
                  <FiSquare style={{ marginRight: 6 }} /> Stop Client Engine
                </button>
              ) : (
                <button className="btn btn--primary btn--md" onClick={startEngine} disabled={isLoading} style={{ width: '100%' }}>
                  <FiPlay style={{ marginRight: 6 }} /> Start Client Engine
                </button>
              )}

              <button
                type="button"
                onClick={handleToggleAutoStart}
                className={`btn btn--sm ${config?.is_active ? 'btn--primary' : 'btn--outline'}`}
                style={{ width: '100%', marginTop: 6 }}
              >
                Auto-Start on Boot: {config?.is_active ? 'ENABLED' : 'DISABLED'}
              </button>
            </div>
          </div>

          {/* Active Proxies Card */}
          <div className="g-card" style={{ padding: 20 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
              <FiGlobe style={{ color: 'var(--color-brand)', fontSize: 16 }} />
              <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--color-brand-heading)' }}>Local Proxy Ports</span>
            </div>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 10, fontSize: 12 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', borderBottom: '1px solid var(--color-brand-border)', paddingBottom: 6 }}>
                <span style={{ color: 'var(--color-brand-muted)' }}>SOCKS5 Port:</span>
                <span style={{ fontFamily: 'Fira Code', fontWeight: 600, color: 'var(--color-brand-heading)' }}>127.0.0.1:{socks5Port}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', borderBottom: '1px solid var(--color-brand-border)', paddingBottom: 6 }}>
                <span style={{ color: 'var(--color-brand-muted)' }}>HTTP Port:</span>
                <span style={{ fontFamily: 'Fira Code', fontWeight: 600, color: 'var(--color-brand-heading)' }}>127.0.0.1:{httpPort}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', borderBottom: '1px solid var(--color-brand-border)', paddingBottom: 6 }}>
                <span style={{ color: 'var(--color-brand-muted)' }}>Transport Mode:</span>
                <span style={{ fontWeight: 600, textTransform: 'uppercase', color: 'var(--color-brand)' }}>{forcedTransport}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={{ color: 'var(--color-brand-muted)' }}>Active Preset:</span>
                <span style={{ fontWeight: 600, color: 'var(--color-brand-heading)' }}>{activePreset}</span>
              </div>
            </div>
          </div>

          {/* Connection Token Import Card */}
          <div className="g-card" style={{ padding: 20 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
              <FiKey style={{ color: 'var(--color-brand)', fontSize: 16 }} />
              <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--color-brand-heading)' }}>Import Connection Token</span>
            </div>

            <form onSubmit={handleImportTokenSubmit} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              <textarea
                value={importTokenStr}
                onChange={(e) => setImportTokenStr(e.target.value)}
                placeholder="Paste tt://? connection token here..."
                style={{
                  width: '100%',
                  height: 80,
                  padding: '8px 10px',
                  borderRadius: 6,
                  border: '1px solid var(--color-brand-border)',
                  background: 'var(--color-brand-card)',
                  color: 'var(--color-brand-heading)',
                  fontSize: 12,
                  resize: 'none',
                  fontFamily: 'Fira Code'
                }}
                required
              />
              <button type="submit" className="btn btn--sm btn--primary" style={{ width: '100%' }}>
                <FiDownload style={{ marginRight: 6 }} /> Import & Apply
              </button>
            </form>
          </div>

          {/* Quick Guide */}
          <div className="g-card" style={{ padding: 20, fontSize: 11, color: 'var(--color-brand-muted)', lineHeight: '1.5' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8, fontWeight: 600, color: 'var(--color-brand-heading)' }}>
              <FiHelpCircle size={14} /> Quick Setup Guide
            </div>
            1. Paste a connection token exported from your server under "Import Connection Token".<br />
            2. Verify target endpoint IP address.<br />
            3. Apply a preset like "Iran Stealth" to use optimized fingerprint settings for your network.<br />
            4. Click "Start Client Engine" to launch local SOCKS5 and HTTP proxies.<br />
            5. Configure your browser extension (e.g. SwitchyOmega) to proxy traffic via SOCKS5 port <b>{socks5Port}</b>.
          </div>

        </div>

      </div>
    </div>
  );
};
