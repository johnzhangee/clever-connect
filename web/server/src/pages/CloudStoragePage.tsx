import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  FiCloud, FiRefreshCw, FiPlus, FiTrash2, FiEdit3, FiZap, FiFolder,
  FiCheckCircle, FiXCircle, FiClock, FiLoader, FiLink, FiCopy, FiSearch,
  FiArrowUp, FiUploadCloud, FiX, FiCloudOff, FiEye, FiEyeOff,
  FiExternalLink, FiInfo, FiHardDrive, FiGlobe, FiChevronDown,
} from 'react-icons/fi';
import { showGlobalAlert, showGlobalConfirm } from '../store/dialogStore';

// ─── Types (mirrors the /api/rclone backend responses) ────────────────────────

interface EngineStatus {
  binary_ready: boolean; binary_path: string; binary_version: string;
  binary_source: string; auto_install: boolean; system_ok: boolean;
  remote_count: number; upload_count: number; provider_count: number;
  secret_marker: string;
}

interface ProviderOption {
  name: string; help: string; required: boolean; is_password: boolean;
  advanced: boolean; has_default: boolean; default: string;
  examples: { value: string; help: string }[];
}

interface Provider {
  name: string; description: string; has_oauth: boolean; options: ProviderOption[];
}

interface Remote {
  id: number; name: string; type: string; root_prefix: string; extra_flags: string;
  enabled: boolean; options: Record<string, string>; secret_fields: string[];
  last_test_ok: boolean; last_test_error: string; last_test_at: string | null;
  created_at: string; updated_at: string;
}

interface UploadRow {
  id: number; job_id: number; remote_id: number; remote_name: string;
  local_path: string; local_rel_path: string; remote_path: string;
  size: number; public_link: string; link_error: string; transfer_error: string;
  status: string; require_public_link: boolean;
  transferred_at: string | null; created_at: string;
}

interface RemoteEntry {
  Name: string; Path: string; Size: number; IsDir: boolean;
  MimeType: string; ModTime: string; ID: string;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

const fmtBytes = (n: number): string => {
  if (n <= 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
};

const fmtDate = (s: string | null): string => (s ? new Date(s).toLocaleString() : '—');

const UPLOAD_STATUS: Record<string, { bg: string; fg: string; icon: React.ReactNode }> = {
  queued:    { bg: 'rgba(59,130,246,0.1)',  fg: '#3b82f6', icon: <FiClock      size={11} /> },
  uploading: { bg: 'rgba(245,158,11,0.1)',  fg: '#f59e0b', icon: <FiLoader     size={11} /> },
  success:   { bg: 'rgba(34,197,94,0.1)',   fg: '#22c55e', icon: <FiCheckCircle size={11} /> },
  failed:    { bg: 'rgba(239,68,68,0.1)',   fg: '#ef4444', icon: <FiXCircle     size={11} /> },
};

const UploadStatusBadge: React.FC<{ status: string }> = ({ status }) => {
  const s = UPLOAD_STATUS[status] || UPLOAD_STATUS.queued;
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, fontWeight: 700, padding: '3px 10px', borderRadius: 40, textTransform: 'uppercase', background: s.bg, color: s.fg }}>
      {s.icon} {status}
    </span>
  );
};

const labelStyle: React.CSSProperties = {
  fontSize: 10, fontWeight: 700, color: 'var(--color-brand-muted)',
  textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: 4, display: 'block',
};

const inputStyle: React.CSSProperties = {
  width: '100%', padding: '8px 12px', fontSize: 13,
  border: '1px solid var(--color-brand-border)', borderRadius: 8,
  background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)',
  outline: 'none', boxSizing: 'border-box',
};

// ─── Main Page ────────────────────────────────────────────────────────────────

export const CloudStoragePage: React.FC = () => {
  const apiBase = '/api/rclone';
  const token = localStorage.getItem('cc_server_token') || '';

  const api = useCallback(async (path: string, opts?: RequestInit) => {
    const res = await fetch(`${apiBase}${path}`, {
      ...opts,
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`,
        ...opts?.headers,
      },
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || data.details || `HTTP ${res.status}`);
    return data;
  }, [token]);

  // ── Data state ──
  const [status, setStatus] = useState<EngineStatus | null>(null);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [remotes, setRemotes] = useState<Remote[]>([]);
  const [uploads, setUploads] = useState<UploadRow[]>([]);
  const [uploadTotal, setUploadTotal] = useState(0);

  // ── Busy flags ──
  const [installing, setInstalling] = useState(false);
  const [testingId, setTestingId] = useState<number | null>(null);
  const [savingRemote, setSavingRemote] = useState(false);

  // ── Wizard state (add / edit remote) ──
  const [showWizard, setShowWizard] = useState(false);
  const [wizardStep, setWizardStep] = useState(0); // 0=provider 1=options 2=identity
  const [editingRemote, setEditingRemote] = useState<Remote | null>(null);
  const [providerSearch, setProviderSearch] = useState('');
  const [wizardType, setWizardType] = useState('');
  const [wizardName, setWizardName] = useState('');
  const [wizardPrefix, setWizardPrefix] = useState('');
  const [wizardFlags, setWizardFlags] = useState('');
  const [wizardEnabled, setWizardEnabled] = useState(true);
  const [optionValues, setOptionValues] = useState<Record<string, string>>({});
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [showSecrets, setShowSecrets] = useState<Record<string, boolean>>({});
  const wizardRef = useRef<HTMLDivElement | null>(null);

  // ── Browser state ──
  const [browseRemote, setBrowseRemote] = useState<Remote | null>(null);
  const [browsePath, setBrowsePath] = useState('');
  const [browseEntries, setBrowseEntries] = useState<RemoteEntry[]>([]);
  const [browsing, setBrowsing] = useState(false);

  // ── Uploads filter state ──
  const [filterRemote, setFilterRemote] = useState('');
  const [filterStatus, setFilterStatus] = useState('');
  const [copiedId, setCopiedId] = useState<number | null>(null);

  const activeProvider = providers.find(p => p.name === wizardType) || null;

  // ── Data loading ──
  const refreshCore = useCallback(async () => {
    try {
      const s = await api('/status');
      setStatus(s);
      const r = await api('/remotes');
      setRemotes(r.remotes || []);
    } catch { /* banner shows missing engine */ }
  }, [api]);

  const refreshUploads = useCallback(async () => {
    try {
      const q = new URLSearchParams();
      if (filterRemote) q.set('remote_id', filterRemote);
      if (filterStatus) q.set('status', filterStatus);
      q.set('limit', '200');
      const u = await api(`/uploads?${q.toString()}`);
      setUploads(u.uploads || []);
      setUploadTotal(u.total || 0);
    } catch { /* transient */ }
  }, [api, filterRemote, filterStatus]);

  const loadProviders = useCallback(async (force = false) => {
    const p = await api(`/providers${force ? '?refresh=1' : ''}`);
    setProviders(p.providers || []);
    return p.providers || [];
  }, [api]);

  useEffect(() => { refreshCore(); }, [refreshCore]);
  useEffect(() => {
    refreshUploads();
    const i = setInterval(refreshUploads, 5000);
    return () => clearInterval(i);
  }, [refreshUploads]);

  // ── Engine actions ──
  const toggleAutoInstall = async () => {
    if (!status) return;
    try {
      await api('/system', { method: 'POST', body: JSON.stringify({ auto_install: !status.auto_install }) });
      await refreshCore();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Update Failed', variant: 'error' }); }
  };

  const installBinary = async (force = false) => {
    setInstalling(true);
    try {
      const r = await api('/install', { method: 'POST', body: JSON.stringify({ force }) });
      showGlobalAlert(`${r.message} (v${r.version})`, { title: 'Engine Installed', variant: 'success' });
      await refreshCore();
      loadProviders();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Install Failed', variant: 'error' }); }
    finally { setInstalling(false); }
  };

  // ── Remote actions ──
  const testRemote = async (remote: Remote) => {
    setTestingId(remote.id);
    try {
      const report = await api(`/remotes/${remote.id}/test`, { method: 'POST', body: '{}' });
      if (report.ok) {
        showGlobalAlert(`Connection OK — ${report.entries} entries at root (${Math.round(report.latency_ms / 1000000)} ms)`, { title: remote.name, variant: 'success' });
      } else {
        showGlobalAlert(report.detail || 'Test failed', { title: remote.name, variant: 'error' });
      }
      await refreshCore();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Test Failed', variant: 'error' }); }
    finally { setTestingId(null); }
  };

  const deleteRemote = async (remote: Remote) => {
    const ok = await showGlobalConfirm(
      `Delete remote "${remote.name}"? Its upload history rows stay unless you also purge them.`,
      { title: 'Delete Remote', variant: 'warning' },
    );
    if (!ok) return;
    const purge = await showGlobalConfirm(
      `Also purge the ${remote.name} upload history rows (provider objects are never touched)?`,
      { title: 'Purge Upload History', variant: 'question' },
    );
    try {
      await api(`/remotes/${remote.id}${purge ? '?purge_uploads=1' : ''}`, { method: 'DELETE' });
      showGlobalAlert('Remote deleted', { title: 'Deleted', variant: 'success' });
      await refreshCore();
      await refreshUploads();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Delete Failed', variant: 'error' }); }
  };

  // ── Wizard ──
  const openWizard = async (remote: Remote | null) => {
    setEditingRemote(remote);
    setOptionValues({});
    setShowAdvanced(false);
    setShowSecrets({});
    setProviderSearch('');
    setWizardFlags(remote?.extra_flags || '');
    setWizardPrefix(remote?.root_prefix || '');
    setWizardEnabled(remote ? remote.enabled : true);
    if (remote) {
      setWizardType(remote.type);
      setWizardName(remote.name);
      setOptionValues({ ...remote.options });
      setWizardStep(1);
    } else {
      setWizardType('');
      setWizardName('');
      setWizardStep(0);
    }
    setShowWizard(true);
    if (providers.length === 0) {
      try { await loadProviders(); } catch { /* banner surfaces it */ }
    }
  };

  const pickProvider = (p: Provider) => {
    setWizardType(p.name);
    if (!editingRemote && !wizardName) setWizardName(p.name.replace(/[^a-z0-9_-]/g, '').slice(0, 24) || p.name);
    const seeded: Record<string, string> = {};
    p.options.forEach(o => { if (o.has_default && o.default) seeded[o.name] = o.default; });
    setOptionValues(seeded);
    setWizardStep(1);
  };

  const saveRemote = async () => {
    if (!wizardName.trim()) { showGlobalAlert('Remote name is required', { title: 'Validation', variant: 'warning' }); return; }
    // Required-option check (masked secrets from edits count as filled).
    const missing = (activeProvider?.options || []).filter(o => o.required && !(optionValues[o.name] || '').trim())
      .map(o => o.name);
    if (missing.length > 0) {
      showGlobalAlert(`Missing required option(s): ${missing.join(', ')}`, { title: 'Validation', variant: 'warning' });
      setWizardStep(1);
      return;
    }
    setSavingRemote(true);
    const body = JSON.stringify({
      name: wizardName.trim().toLowerCase(),
      type: wizardType,
      root_prefix: wizardPrefix.trim(),
      extra_flags: wizardFlags.trim(),
      enabled: wizardEnabled,
      options: optionValues,
    });
    try {
      await api(editingRemote ? `/remotes/${editingRemote.id}` : '/remotes', { method: editingRemote ? 'PUT' : 'POST', body });
      showGlobalAlert(editingRemote ? 'Remote updated' : 'Remote created', { title: 'Saved', variant: 'success' });
      setShowWizard(false);
      await refreshCore();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Save Failed', variant: 'error' }); }
    finally { setSavingRemote(false); }
  };

  // ── Browser ──
  const openBrowser = async (remote: Remote) => {
    setBrowseRemote(remote);
    setBrowsePath('');
    setBrowseEntries([]);
    await browse(remote, '');
  };

  const browse = async (remote: Remote, path: string) => {
    setBrowsing(true);
    try {
      const r = await api(`/remotes/${remote.id}/list`, { method: 'POST', body: JSON.stringify({ path, recursive: false }) });
      setBrowseEntries(r.entries || []);
      setBrowsePath(path);
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Listing Failed', variant: 'error' }); }
    finally { setBrowsing(false); }
  };

  // ── Upload row actions ──
  const copyLink = async (row: UploadRow) => {
    if (!row.public_link) return;
    try { await navigator.clipboard.writeText(row.public_link); } catch { /* clipboard denied */ }
    setCopiedId(row.id);
    setTimeout(() => setCopiedId(null), 1500);
  };

  const refreshLink = async (row: UploadRow) => {
    try {
      const r = await api(`/uploads/${row.id}/link`, { method: 'POST' });
      showGlobalAlert(r.public_link, { title: 'Public Link Created', variant: 'success' });
      await refreshUploads();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Link Failed', variant: 'error' }); }
  };

  const deleteUpload = async (row: UploadRow, purge: boolean) => {
    const ok = await showGlobalConfirm(
      purge
        ? `Delete the record AND remove "${row.remote_path}" from the provider?`
        : `Delete upload record #${row.id} (the remote object stays)?`,
      { title: purge ? 'Delete & Purge' : 'Delete Record', variant: 'warning' },
    );
    if (!ok) return;
    try {
      await api(`/uploads/${row.id}${purge ? '?purge=1' : ''}`, { method: 'DELETE' });
      await refreshUploads();
      await refreshCore();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Delete Failed', variant: 'error' }); }
  };

  const toggleRemoteEnabled = async (remote: Remote) => {
    try {
      await api(`/remotes/${remote.id}`, {
        method: 'PUT',
        // Round-trip the masked options so secrets survive (backend preserves
        // any secret field whose value equals the mask marker).
        body: JSON.stringify({
          name: remote.name,
          type: remote.type,
          root_prefix: remote.root_prefix,
          extra_flags: remote.extra_flags,
          enabled: !remote.enabled,
          options: remote.options,
        }),
      });
      await refreshCore();
    } catch (e: any) { showGlobalAlert(e.message, { title: 'Toggle Failed', variant: 'error' }); }
  };

  // ── Render ──
  const filteredProviders = (providerSearch
    ? providers.filter(p => p.name.includes(providerSearch.toLowerCase()) || p.description.toLowerCase().includes(providerSearch.toLowerCase()))
    : providers);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>

      {/* ── Engine Banner ── */}
      <div className="g-card animate-slide-in" style={{ padding: '18px 22px', display: 'flex', flexWrap: 'wrap', gap: 18, alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 14, minWidth: 260 }}>
          <div style={{ width: 44, height: 44, borderRadius: 12, background: status?.binary_ready ? 'rgba(34,197,94,0.12)' : 'rgba(239,68,68,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <FiCloud size={22} style={{ color: status?.binary_ready ? '#22c55e' : '#ef4444' }} />
          </div>
          <div>
            <div style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-brand-heading)' }}>
              Universal Cloud Storage {status?.binary_version ? <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)' }}>· rclone v{status.binary_version}</span> : null}
            </div>
            <div style={{ fontSize: 12, color: 'var(--color-brand-text)', marginTop: 2 }}>
              {status?.binary_ready
                ? <>Engine ready ({status.binary_source}) — {status.provider_count} providers, {status.remote_count} remotes, {status.upload_count} uploads</>
                : 'Engine binary missing — install it or set RCLONE_BINARY to enable cloud features'}
            </div>
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 7, fontSize: 12, fontWeight: 600, color: 'var(--color-brand-text)', cursor: 'pointer', userSelect: 'none' }}>
            <input type="checkbox" checked={!!status?.auto_install} onChange={toggleAutoInstall} disabled={!status?.system_ok} />
            Auto-install engine
          </label>
          <button className="btn btn--sm" onClick={() => loadProviders(true)} style={{ display: 'flex', alignItems: 'center', gap: 6 }} title="Re-read provider metadata from the engine">
            <FiGlobe size={13} /> Refresh Providers
          </button>
          <button className="btn btn--primary btn--sm" onClick={() => installBinary(!status?.binary_ready)} disabled={installing} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <FiRefreshCw size={13} className={installing ? 'spin-anim' : ''} />
            {installing ? 'Installing…' : status?.binary_ready ? 'Reinstall Engine' : 'Install Engine'}
          </button>
        </div>
      </div>

      {/* ── Remotes ── */}
      <div className="g-card animate-slide-in" style={{ padding: '16px 22px' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <h3 style={{ margin: 0, fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)', display: 'flex', alignItems: 'center', gap: 8 }}>
            <FiHardDrive size={15} /> Configured Remotes
          </h3>
          <button className="btn btn--primary btn--sm" onClick={() => openWizard(null)} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <FiPlus size={13} /> Add Remote
          </button>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
            <thead>
              <tr style={{ textAlign: 'left', color: 'var(--color-brand-muted)', fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Name</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Provider</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Enabled</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Last Test</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)', textAlign: 'right' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {remotes.length === 0 && (
                <tr><td colSpan={5} style={{ padding: '18px 10px', textAlign: 'center', color: 'var(--color-brand-muted)' }}>No remotes configured yet — add one to upload files to any cloud provider.</td></tr>
              )}
              {remotes.map(r => (
                <tr key={r.id} style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
                  <td style={{ padding: '9px 10px', fontWeight: 600, color: 'var(--color-brand-heading)' }}>{r.name}</td>
                  <td style={{ padding: '9px 10px', color: 'var(--color-brand-text)' }}>{r.type}</td>
                  <td style={{ padding: '9px 10px' }}>
                    <input type="checkbox" checked={r.enabled} onChange={() => toggleRemoteEnabled(r)} />
                  </td>
                  <td style={{ padding: '9px 10px' }}>
                    {r.last_test_at ? (
                      r.last_test_ok
                        ? <span style={{ color: '#22c55e', fontWeight: 600, fontSize: 12 }}><FiCheckCircle size={12} style={{ verticalAlign: -2 }} /> OK · {fmtDate(r.last_test_at)}</span>
                        : <span title={r.last_test_error} style={{ color: '#ef4444', fontWeight: 600, fontSize: 12 }}><FiXCircle size={12} style={{ verticalAlign: -2 }} /> Failed · {fmtDate(r.last_test_at)}</span>
                    ) : <span style={{ color: 'var(--color-brand-muted)', fontSize: 12 }}>never</span>}
                  </td>
                  <td style={{ padding: '9px 10px', textAlign: 'right', whiteSpace: 'nowrap' }}>
                    <button className="btn btn--sm" onClick={() => testRemote(r)} disabled={testingId === r.id} title="Test connectivity" style={{ marginRight: 4 }}>
                      <FiZap size={13} className={testingId === r.id ? 'spin-anim' : ''} />
                    </button>
                    <button className="btn btn--sm" onClick={() => openBrowser(r)} title="Browse remote" style={{ marginRight: 4 }}>
                      <FiFolder size={13} />
                    </button>
                    <button className="btn btn--sm" onClick={() => openWizard(r)} title="Edit remote" style={{ marginRight: 4 }}>
                      <FiEdit3 size={13} />
                    </button>
                    <button className="btn btn--sm" onClick={() => deleteRemote(r)} title="Delete remote">
                      <FiTrash2 size={13} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* ── Upload History ── */}
      <div className="g-card animate-slide-in" style={{ padding: '16px 22px' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12, flexWrap: 'wrap', gap: 10 }}>
          <h3 style={{ margin: 0, fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)', display: 'flex', alignItems: 'center', gap: 8 }}>
            <FiUploadCloud size={15} /> Upload History
            <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)' }}>({uploadTotal})</span>
          </h3>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <select value={filterRemote} onChange={e => setFilterRemote(e.target.value)} style={{ ...inputStyle, width: 'auto', padding: '6px 10px', fontSize: 12 }}>
              <option value="">All remotes</option>
              {remotes.map(r => <option key={r.id} value={String(r.id)}>{r.name}</option>)}
            </select>
            <select value={filterStatus} onChange={e => setFilterStatus(e.target.value)} style={{ ...inputStyle, width: 'auto', padding: '6px 10px', fontSize: 12 }}>
              <option value="">All statuses</option>
              <option value="queued">Queued</option>
              <option value="uploading">Uploading</option>
              <option value="success">Success</option>
              <option value="failed">Failed</option>
            </select>
            <button className="btn btn--sm" onClick={() => refreshUploads()} title="Refresh uploads">
              <FiRefreshCw size={13} />
            </button>
          </div>
        </div>
        <div style={{ overflowX: 'auto', maxHeight: 420, overflowY: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12.5 }}>
            <thead style={{ position: 'sticky', top: 0, background: 'var(--color-brand-bg)', zIndex: 1 }}>
              <tr style={{ textAlign: 'left', color: 'var(--color-brand-muted)', fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>File</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Remote</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Size</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Status</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)' }}>Created</th>
                <th style={{ padding: '8px 10px', borderBottom: '1px solid var(--color-brand-border)', textAlign: 'right' }}>Link / Actions</th>
              </tr>
            </thead>
            <tbody>
              {uploads.length === 0 && (
                <tr><td colSpan={6} style={{ padding: '18px 10px', textAlign: 'center', color: 'var(--color-brand-muted)' }}>No uploads yet — use the file manager's "Cloud" action to send files to a remote.</td></tr>
              )}
              {uploads.map(u => {
                const fileName = u.local_path.split('/').filter(Boolean).pop() || u.local_path;
                return (
                  <tr key={u.id} style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
                    <td style={{ padding: '8px 10px', maxWidth: 260 }}>
                      <div style={{ fontWeight: 600, color: 'var(--color-brand-heading)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={u.local_path}>{fileName}</div>
                      <div style={{ fontSize: 10, color: 'var(--color-brand-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={u.remote_path}>{u.remote_path}</div>
                      {u.transfer_error && <div style={{ fontSize: 10, color: '#ef4444', marginTop: 2 }} title={u.transfer_error}>{u.transfer_error.slice(0, 120)}</div>}
                    </td>
                    <td style={{ padding: '8px 10px', color: 'var(--color-brand-text)' }}>{u.remote_name}</td>
                    <td style={{ padding: '8px 10px', color: 'var(--color-brand-text)' }}>{fmtBytes(u.size)}</td>
                    <td style={{ padding: '8px 10px' }}><UploadStatusBadge status={u.status} /></td>
                    <td style={{ padding: '8px 10px', color: 'var(--color-brand-muted)', fontSize: 11, whiteSpace: 'nowrap' }}>{fmtDate(u.created_at)}</td>
                    <td style={{ padding: '8px 10px', textAlign: 'right', whiteSpace: 'nowrap' }}>
                      {u.public_link ? (
                        <>
                          <a href={u.public_link} target="_blank" rel="noopener noreferrer" className="btn btn--sm" style={{ marginRight: 4 }} title={u.public_link}>
                            <FiExternalLink size={12} />
                          </a>
                          <button className="btn btn--sm" onClick={() => copyLink(u)} title="Copy link" style={{ marginRight: 4 }}>
                            {copiedId === u.id ? <FiCheckCircle size={12} style={{ color: '#22c55e' }} /> : <FiCopy size={12} />}
                          </button>
                        </>
                      ) : u.require_public_link ? (
                        <button className="btn btn--sm" onClick={() => refreshLink(u)} title={u.link_error || 'Fetch public link'} style={{ marginRight: 4 }}>
                          <FiLink size={12} />
                        </button>
                      ) : null}
                      <button className="btn btn--sm" onClick={() => deleteUpload(u, false)} title="Delete record (keeps remote object)" style={{ marginRight: 4 }}>
                        <FiTrash2 size={12} />
                      </button>
                      <button className="btn btn--sm" onClick={() => deleteUpload(u, true)} title="Delete record and purge remote object">
                        <FiCloudOff size={12} />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>

      {/* ── Add / Edit Remote Wizard ── */}
      {showWizard && (
        <div
          onClick={e => { if (e.target === e.currentTarget) setShowWizard(false); }}
          style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)', backdropFilter: 'blur(4px)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 20 }}
        >
          <div ref={wizardRef} className="g-card" style={{ width: 640, maxWidth: '100%', maxHeight: '88vh', display: 'flex', flexDirection: 'column', padding: 0 }}>
            {/* Wizard header */}
            <div style={{ padding: '16px 22px', borderBottom: '1px solid var(--color-brand-border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <div>
                <div style={{ fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)' }}>
                  {editingRemote ? `Edit Remote — ${editingRemote.name}` : 'Add Remote'}
                </div>
                <div style={{ fontSize: 11, color: 'var(--color-brand-muted)', marginTop: 2 }}>
                  {wizardStep === 0 && 'Step 1 of 3 — pick a provider'}
                  {wizardStep === 1 && `Step 2 of 3 — ${wizardType} options`}
                  {wizardStep === 2 && 'Step 3 of 3 — identity & advanced'}
                </div>
              </div>
              <button className="btn btn--sm" onClick={() => setShowWizard(false)}><FiX size={14} /></button>
            </div>

            <div style={{ padding: '16px 22px', overflowY: 'auto', flex: 1 }}>
              {/* Step 0: provider picker */}
              {wizardStep === 0 && (
                <>
                  {providers.length === 0 ? (
                    <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--color-brand-muted)', fontSize: 13 }}>
                      <FiLoader className="spin-anim" size={20} style={{ display: 'block', margin: '0 auto 10px' }} />
                      Loading provider catalog from the rclone engine…
                    </div>
                  ) : (
                    <>
                      <div style={{ position: 'relative', marginBottom: 12 }}>
                        <FiSearch size={13} style={{ position: 'absolute', left: 12, top: 11, color: 'var(--color-brand-muted)' }} />
                        <input
                          autoFocus
                          placeholder="Search providers (s3, drive, dropbox, sftp…)"
                          value={providerSearch}
                          onChange={e => setProviderSearch(e.target.value)}
                          style={{ ...inputStyle, paddingLeft: 32 }}
                        />
                      </div>
                      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))', gap: 8, maxHeight: '48vh', overflowY: 'auto' }}>
                        {filteredProviders.map(p => (
                          <button
                            key={p.name}
                            onClick={() => pickProvider(p)}
                            className="btn"
                            style={{ textAlign: 'left', padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 3, alignItems: 'flex-start', borderColor: 'var(--color-brand-border)' }}
                          >
                            <span style={{ fontWeight: 700, fontSize: 13, color: 'var(--color-brand-heading)' }}>{p.name}</span>
                            <span style={{ fontSize: 10.5, color: 'var(--color-brand-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '100%' }}>
                              {p.has_oauth ? 'OAuth · ' : ''}{p.description || '—'}
                            </span>
                          </button>
                        ))}
                      </div>
                    </>
                  )}
                </>
              )}
              {/* Step 1: provider options */}
              {wizardStep === 1 && activeProvider && (
                <>
                  {activeProvider.has_oauth && (
                    <div style={{ display: 'flex', gap: 8, alignItems: 'flex-start', background: 'rgba(99,102,241,0.08)', border: '1px solid rgba(99,102,241,0.25)', borderRadius: 8, padding: '10px 12px', marginBottom: 12 }}>
                      <FiInfo size={14} style={{ color: 'var(--color-brand)', flexShrink: 0, marginTop: 1 }} />
                      <span style={{ fontSize: 11.5, color: 'var(--color-brand-text)' }}>
                        This provider uses OAuth. Paste a <code>token</code> (JSON) or machine key below, or generate one on a machine with a browser via <code>rclone config create</code>. After saving, press Test to verify.
                      </span>
                    </div>
                  )}
                  {(() => {
                    const core = activeProvider.options.filter(o => !o.advanced);
                    const adv = activeProvider.options.filter(o => o.advanced);
                    const renderOpt = (o: ProviderOption) => {
                      const val = optionValues[o.name] ?? '';
                      const masked = !!editingRemote && val === (status?.secret_marker || '••••••••');
                      return (
                        <div key={o.name} style={{ marginBottom: 12 }}>
                          <span style={labelStyle}>
                            {o.name}
                            {o.required ? <span style={{ color: '#ef4444' }}> *</span> : null}
                            {o.is_password ? ' · secret' : ''}
                            {masked ? ' (unchanged)' : ''}
                          </span>
                          <div style={{ position: 'relative' }}>
                            <input
                              type={o.is_password && !showSecrets[o.name] ? 'password' : 'text'}
                              value={val}
                              placeholder={o.has_default ? o.default : (o.examples.length > 0 ? o.examples[0].value : '')}
                              onChange={e => setOptionValues(v => ({ ...v, [o.name]: e.target.value }))}
                              style={inputStyle}
                              autoComplete="off"
                            />
                            {o.is_password && (
                              <button type="button" onClick={() => setShowSecrets(s => ({ ...s, [o.name]: !s[o.name] }))}
                                style={{ position: 'absolute', right: 8, top: 8, background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-muted)', padding: 2 }}>
                                {showSecrets[o.name] ? <FiEyeOff size={13} /> : <FiEye size={13} />}
                              </button>
                            )}
                          </div>
                          {o.help && <div style={{ fontSize: 10.5, color: 'var(--color-brand-muted)', marginTop: 3 }}>{o.help.slice(0, 220)}</div>}
                          {o.examples.length > 0 && (
                            <div style={{ fontSize: 10, color: 'var(--color-brand-muted)', marginTop: 2 }}>
                              e.g. {o.examples.slice(0, 2).map(x => x.value).join(' · ')}
                            </div>
                          )}
                        </div>
                      );
                    };
                    return (
                      <>
                        {core.map(renderOpt)}
                        {adv.length > 0 && (
                          <>
                            <button type="button" className="btn btn--sm" onClick={() => setShowAdvanced(a => !a)} style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 12 }}>
                              <FiChevronDown size={12} style={{ transform: showAdvanced ? 'rotate(180deg)' : 'none', transition: 'transform .15s' }} />
                              Advanced options ({adv.length})
                            </button>
                            {showAdvanced && adv.map(renderOpt)}
                          </>
                        )}
                      </>
                    );
                  })()}
                </>
              )}
              {/* Step 2: identity & advanced */}
              {wizardStep === 2 && (
                <>
                  <div style={{ marginBottom: 12 }}>
                    <span style={labelStyle}>Remote name <span style={{ color: '#ef4444' }}>*</span></span>
                    <input
                      autoFocus
                      value={wizardName}
                      onChange={e => setWizardName(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))}
                      placeholder="my-backup"
                      style={inputStyle}
                    />
                    <div style={{ fontSize: 10.5, color: 'var(--color-brand-muted)', marginTop: 3 }}>
                      Lower-case letters, digits, "_" and "-". Used as <code>{wizardName || 'name'}:{wizardPrefix || 'path'}</code>
                    </div>
                  </div>
                  <div style={{ marginBottom: 12 }}>
                    <span style={labelStyle}>Root prefix (optional)</span>
                    <input
                      value={wizardPrefix}
                      onChange={e => setWizardPrefix(e.target.value)}
                      placeholder="backups/clever-connect"
                      style={inputStyle}
                    />
                    <div style={{ fontSize: 10.5, color: 'var(--color-brand-muted)', marginTop: 3 }}>
                      All uploads land under this directory on the provider; empty means the bucket/remote root.
                    </div>
                  </div>
                  <div style={{ marginBottom: 12 }}>
                    <span style={labelStyle}>Extra rclone flags (optional)</span>
                    <textarea
                      value={wizardFlags}
                      onChange={e => setWizardFlags(e.target.value)}
                      placeholder="--transfers 4 --sftp-set-modtime"
                      rows={2}
                      style={{ ...inputStyle, fontFamily: 'monospace', fontSize: 12, resize: 'vertical' }}
                    />
                    <div style={{ fontSize: 10.5, color: 'var(--color-brand-muted)', marginTop: 3 }}>
                      Space-separated <code>--flag value</code> pairs appended to every rclone invocation for this remote.
                    </div>
                  </div>
                  <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, fontWeight: 600, color: 'var(--color-brand-text)', cursor: 'pointer' }}>
                    <input type="checkbox" checked={wizardEnabled} onChange={e => setWizardEnabled(e.target.checked)} />
                    Enabled (visible to upload actions)
                  </label>
                </>
              )}
            </div>

            {/* Wizard footer */}
            <div style={{ padding: '12px 22px', borderTop: '1px solid var(--color-brand-border)', display: 'flex', justifyContent: 'space-between', gap: 8 }}>
              <button
                className="btn btn--sm"
                onClick={() => setWizardStep(s => Math.max(0, s - 1))}
                disabled={wizardStep === 0 || (editingRemote !== null && wizardStep === 1)}
                style={{ visibility: wizardStep === 0 ? 'hidden' : 'visible' }}
              >
                ← Back
              </button>
              <div style={{ display: 'flex', gap: 8 }}>
                {wizardStep < 2 ? (
                  <button className="btn btn--primary btn--sm" onClick={() => setWizardStep(s => s + 1)} disabled={wizardStep === 0 && !wizardType}>
                    Continue →
                  </button>
                ) : (
                  <button className="btn btn--primary btn--sm" onClick={saveRemote} disabled={savingRemote}>
                    <FiCheckCircle size={13} /> {savingRemote ? 'Saving…' : editingRemote ? 'Save Changes' : 'Create Remote'}
                  </button>
                )}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* ── Remote Browser ── */}
      {browseRemote && (
        <div
          onClick={e => { if (e.target === e.currentTarget) setBrowseRemote(null); }}
          style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)', backdropFilter: 'blur(4px)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 20 }}
        >
          <div className="g-card" style={{ width: 720, maxWidth: '100%', maxHeight: '88vh', display: 'flex', flexDirection: 'column', padding: 0 }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--color-brand-border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 10 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 14, fontWeight: 700, color: 'var(--color-brand-heading)', overflow: 'hidden' }}>
                <FiFolder size={14} />
                {browseRemote.name}:<span style={{ color: 'var(--color-brand-muted)', fontWeight: 500 }}>{browsePath || (browseRemote.root_prefix ? browseRemote.root_prefix : '')}</span>
              </div>
              <div style={{ display: 'flex', gap: 6 }}>
                <button className="btn btn--sm" onClick={() => browse(browseRemote, browsePath)} disabled={browsing} title="Refresh">
                  <FiRefreshCw size={13} className={browsing ? 'spin-anim' : ''} />
                </button>
                <button className="btn btn--sm" onClick={() => browse(browseRemote, browsePath.includes('/') ? browsePath.slice(0, browsePath.lastIndexOf('/')) : '')} disabled={browsing || !browsePath} title="Parent directory">
                  <FiArrowUp size={13} />
                </button>
                <button className="btn btn--sm" onClick={() => setBrowseRemote(null)}><FiX size={14} /></button>
              </div>
            </div>
            <div style={{ overflowY: 'auto', flex: 1, maxHeight: '62vh' }}>
              {browsing && browseEntries.length === 0 ? (
                <div style={{ textAlign: 'center', padding: '50px 0', color: 'var(--color-brand-muted)', fontSize: 13 }}>
                  <FiLoader className="spin-anim" size={20} style={{ display: 'block', margin: '0 auto 10px' }} />
                  Listing directory…
                </div>
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
                  <tbody>
                    {browseEntries.length === 0 && (
                      <tr><td style={{ padding: '22px 12px', textAlign: 'center', color: 'var(--color-brand-muted)' }}>Empty directory</td></tr>
                    )}
                    {browseEntries
                      .slice()
                      .sort((a, b) => (a.IsDir === b.IsDir ? a.Name.localeCompare(b.Name) : a.IsDir ? -1 : 1))
                      .map(e => (
                        <tr key={e.Path || e.Name} style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
                          <td style={{ padding: '9px 14px', width: 34, color: e.IsDir ? 'var(--color-brand)' : 'var(--color-brand-muted)' }}>
                            {e.IsDir ? <FiFolder size={14} /> : <FiExternalLink size={13} />}
                          </td>
                          <td style={{ padding: '9px 6px' }}>
                            {e.IsDir ? (
                              <button
                                className="btn"
                                style={{ padding: 0, border: 'none', background: 'none', color: 'var(--color-brand-heading)', fontWeight: 600, fontSize: 13, textAlign: 'left' }}
                                onClick={() => browse(browseRemote, browsePath ? `${browsePath}/${e.Name}` : e.Name)}
                              >
                                {e.Name}
                              </button>
                            ) : (
                              <span style={{ color: 'var(--color-brand-text)' }}>{e.Name}</span>
                            )}
                          </td>
                          <td style={{ padding: '9px 14px', textAlign: 'right', color: 'var(--color-brand-muted)', fontSize: 11, whiteSpace: 'nowrap' }}>
                            {e.ModTime ? new Date(e.ModTime).toLocaleDateString() : ''}
                          </td>
                          <td style={{ padding: '9px 14px', textAlign: 'right', color: 'var(--color-brand-muted)', fontSize: 11, whiteSpace: 'nowrap', width: 80 }}>
                            {e.IsDir ? '—' : fmtBytes(e.Size)}
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default CloudStoragePage;









