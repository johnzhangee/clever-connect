import React, { useState, useEffect, useCallback } from 'react';
import { useAuthStore } from '../store/authStore';
import {
  FiPlay, FiPause, FiTrash2, FiPlus, FiSettings, FiX, FiRefreshCw,
  FiClock, FiCheckCircle, FiXCircle, FiZap, FiList,
  FiActivity, FiCpu, FiTerminal, FiSliders, FiCalendar,
  FiCheck, FiMinus
} from 'react-icons/fi';
import { showGlobalConfirm } from '../store/dialogStore';

// ─── Types ────────────────────────────────────────────────────────────────────

interface SchedulerJob {
  id: number; uuid: string; type: string; name: string; description: string;
  category: string; status: string; priority: number; progress: number;
  message: string; payload: string; cron_expr: string; retry_count: number;
  started_at: string | null; finished_at: string | null;
  created_at: string; updated_at: string;
}
interface SchedulerConfig {
  max_concurrent_jobs: number; default_priority: number; retry_limit: number;
  retry_delay_seconds: number; job_timeout_seconds: number; purge_after_days: number;
  enable_cron_jobs: boolean; enable_notifications: boolean;
}
interface JobLog { id: number; level: string; message: string; created_at: string; }
interface Stats {
  total_jobs: number; queued_jobs: number; running_jobs: number;
  completed_jobs: number; failed_jobs: number; cancelled_jobs: number;
  active_workers: number; max_workers: number; cron_schedules: number;
  avg_duration: number; cpu_count: number;
}

// ─── Constants ────────────────────────────────────────────────────────────────

const JOB_TYPES = [
  { value: 'file_compress',   label: 'File Compression',   cat: 'files'   },
  { value: 'file_decompress', label: 'File Decompression', cat: 'files'   },
  { value: 'rclone_upload',   label: 'Cloud Upload',       cat: 'files'   },
  { value: 'system_cleanup',  label: 'System Cleanup',     cat: 'system'  },
  { value: 'db_backup',       label: 'Database Backup',    cat: 'system'  },
  { value: 'custom_task',     label: 'Custom Task',        cat: 'general' },
];

const STATUS_COLORS: Record<string, { bg: string; fg: string; icon: React.ReactNode }> = {
  queued:    { bg: 'rgba(59,130,246,0.1)',  fg: '#3b82f6', icon: <FiClock     size={11}/> },
  running:   { bg: 'rgba(245,158,11,0.1)', fg: '#f59e0b', icon: <FiActivity   size={11}/> },
  completed: { bg: 'rgba(34,197,94,0.1)',  fg: '#22c55e', icon: <FiCheckCircle size={11}/> },
  failed:    { bg: 'rgba(239,68,68,0.1)',  fg: '#ef4444', icon: <FiXCircle    size={11}/> },
  cancelled: { bg: 'rgba(107,114,128,0.1)',fg: '#6b7280', icon: <FiXCircle    size={11}/> },
  scheduled: { bg: 'rgba(139,92,246,0.1)', fg: '#8b5cf6', icon: <FiCalendar  size={11}/> },
};

// ─── Sub-components ───────────────────────────────────────────────────────────

const StatusBadge: React.FC<{ status: string }> = ({ status }) => {
  const s = STATUS_COLORS[status] || STATUS_COLORS.queued;
  return (
    <span style={{ display:'inline-flex', alignItems:'center', gap:4, fontSize:10, fontWeight:700,
      padding:'3px 10px', borderRadius:40, textTransform:'uppercase', background:s.bg, color:s.fg }}>
      {s.icon} {status}
    </span>
  );
};

const PriorityBadge: React.FC<{ p: number }> = ({ p }) => {
  const c = p <= 2 ? '#ef4444' : p <= 4 ? '#f59e0b' : p <= 6 ? '#3b82f6' : '#6b7280';
  return (
    <span style={{ fontSize:10, fontWeight:700, padding:'2px 8px', borderRadius:40,
      background:`${c}18`, color:c }}>P{p}</span>
  );
};

const ProgressBar: React.FC<{ progress: number; status: string }> = ({ progress, status }) => {
  const color = status==='completed'?'#22c55e':status==='failed'?'#ef4444':status==='running'?'#f59e0b':'#3b82f6';
  return (
    <div style={{ width:'100%', height:5, background:'rgba(0,0,0,0.08)', borderRadius:3, overflow:'hidden' }}>
      <div
        className={status==='running' ? 'scheduler-progress-anim' : ''}
        style={{ width:`${Math.max(2, progress)}%`, height:'100%', borderRadius:3, background:color, transition:'width 0.5s ease' }}
      />
    </div>
  );
};

// Apple-style circular radio checkbox
const AppleRadio: React.FC<{
  checked: boolean;
  indeterminate?: boolean;
  onClick: (e: React.MouseEvent) => void;
}> = ({ checked, indeterminate, onClick }) => (
  <div className="apple-radio-wrapper" onClick={onClick} role="checkbox" aria-checked={checked}>
    <div className={[
      'apple-radio-circle',
      checked ? 'apple-radio-circle--checked' : '',
      indeterminate ? 'apple-radio-circle--indeterminate' : '',
    ].join(' ')}>
      {indeterminate && !checked && <FiMinus size={10} strokeWidth={3.5} />}
      {checked && <FiCheck size={10} strokeWidth={3.5} />}
    </div>
  </div>
);

// ─── Utilities ────────────────────────────────────────────────────────────────

const fmtDuration = (start: string | null, end: string | null) => {
  if (!start) return '—';
  const s = new Date(start).getTime();
  const e = end ? new Date(end).getTime() : Date.now();
  const diff = Math.max(0, (e - s) / 1000);
  if (diff < 60)   return `${Math.round(diff)}s`;
  if (diff < 3600) return `${Math.floor(diff/60)}m ${Math.round(diff%60)}s`;
  return `${Math.floor(diff/3600)}h ${Math.floor((diff%3600)/60)}m`;
};

// ─── Global styles ────────────────────────────────────────────────────────────

const PAGE_STYLES = `
  @keyframes schedulerProgress {
    from { background-position: 0 0 }
    to   { background-position: 20px 0 }
  }
  @keyframes floatingPanelIn {
    from { transform: translate(-50%, 28px); opacity: 0; }
    to   { transform: translate(-50%, 0);    opacity: 1; }
  }
  @keyframes radioCheckIn {
    0%   { transform: scale(0.6); opacity: 0; }
    60%  { transform: scale(1.18); }
    100% { transform: scale(1);   opacity: 1; }
  }
  @keyframes spin {
    from { transform: rotate(0deg); }
    to   { transform: rotate(360deg); }
  }

  .scheduler-progress-anim {
    background-image: repeating-linear-gradient(
      45deg,
      rgba(255,255,255,0.18),
      rgba(255,255,255,0.18) 5px,
      transparent 5px,
      transparent 10px
    ) !important;
    background-size: 20px 100%;
    animation: schedulerProgress 0.7s linear infinite;
  }

  /* ── Rows ── */
  .sched-row { transition: background 0.12s ease; cursor: pointer; }
  .sched-row:hover  { background: var(--color-brand-light) !important; }
  .sched-row--selected { background: color-mix(in srgb, var(--color-brand) 7%, transparent) !important; }

  /* ── Inline action buttons ── */
  .sched-action { transition: background 0.15s, color 0.15s; }
  .sched-action:hover { background: var(--color-brand) !important; color: #fff !important; }

  /* ── Apple-style radio checkbox ── */
  .apple-radio-wrapper {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    cursor: pointer;
    flex-shrink: 0;
    border-radius: 50%;
    transition: background 0.2s;
  }
  .apple-radio-wrapper:hover {
    background: color-mix(in srgb, var(--color-brand) 12%, transparent);
  }
  .apple-radio-circle {
    width: 18px;
    height: 18px;
    border-radius: 50%;
    border: 1.75px solid var(--color-brand-border, rgba(0,0,0,0.2));
    background: transparent;
    display: flex;
    align-items: center;
    justify-content: center;
    color: transparent;
    transition:
      background 0.22s cubic-bezier(0.16,1,0.3,1),
      border-color 0.22s cubic-bezier(0.16,1,0.3,1),
      transform 0.22s cubic-bezier(0.34,1.56,0.64,1);
  }
  .apple-radio-wrapper:hover .apple-radio-circle {
    border-color: var(--color-brand);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-brand) 14%, transparent);
  }
  .apple-radio-circle--checked {
    background: var(--color-brand);
    border-color: var(--color-brand);
    color: #fff;
    transform: scale(1.04);
  }
  .apple-radio-circle--checked svg {
    animation: radioCheckIn 0.28s cubic-bezier(0.34,1.56,0.64,1);
  }
  .apple-radio-circle--indeterminate {
    background: var(--color-brand);
    border-color: var(--color-brand);
    color: #fff;
  }

  /* ── Floating action dock ── */
  .bulk-dock {
    position: fixed;
    bottom: 28px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 900;
    display: flex;
    align-items: center;
    gap: 0;
    background: var(--color-brand-card, rgba(255,255,255,0.9));
    backdrop-filter: blur(24px) saturate(200%);
    -webkit-backdrop-filter: blur(24px) saturate(200%);
    border: 1px solid var(--color-brand-border);
    border-radius: 22px;
    padding: 8px 10px;
    box-shadow:
      0 8px 32px rgba(0,0,0,0.14),
      0 2px 8px  rgba(0,0,0,0.08),
      inset 0 1px 0 rgba(255,255,255,0.5);
    animation: floatingPanelIn 0.38s cubic-bezier(0.16,1,0.3,1) both;
  }
  .bulk-dock__divider {
    width: 1px;
    height: 26px;
    background: var(--color-brand-border);
    margin: 0 10px;
    flex-shrink: 0;
  }
  .bulk-dock__count {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0 10px 0 4px;
    font-size: 13px;
    font-weight: 600;
    color: var(--color-brand-heading);
    white-space: nowrap;
  }
  .bulk-dock__badge {
    display: flex;
    align-items: center;
    justify-content: center;
    min-width: 24px;
    height: 24px;
    padding: 0 6px;
    border-radius: 12px;
    background: var(--color-brand);
    color: #fff;
    font-size: 11px;
    font-weight: 700;
  }
  .bulk-dock__btn {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 7px 14px;
    border-radius: 14px;
    border: none;
    background: transparent;
    color: var(--color-brand-heading);
    font-size: 12.5px;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.18s, color 0.18s;
    white-space: nowrap;
  }
  .bulk-dock__btn:hover {
    background: color-mix(in srgb, var(--color-brand) 10%, transparent);
    color: var(--color-brand);
  }
  .bulk-dock__btn--danger:hover {
    background: rgba(239,68,68,0.1);
    color: #ef4444;
  }
  .bulk-dock__btn--close {
    width: 30px;
    height: 30px;
    padding: 0;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    margin-left: 4px;
  }
  .bulk-dock__btn--close:hover {
    background: rgba(239,68,68,0.1);
    color: #ef4444;
  }

  /* ── Toast notification ── */
  @keyframes toastIn  { from { transform: translateX(-50%) translateY(20px); opacity:0; } to { transform: translateX(-50%) translateY(0); opacity:1; } }
  @keyframes toastOut { from { opacity:1; } to { opacity:0; } }
  .sched-toast {
    position: fixed;
    bottom: 88px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 1100;
    padding: 10px 20px;
    border-radius: 14px;
    font-size: 13px;
    font-weight: 600;
    white-space: nowrap;
    box-shadow: 0 6px 24px rgba(0,0,0,0.15);
    animation: toastIn 0.3s cubic-bezier(0.16,1,0.3,1);
    backdrop-filter: blur(12px);
  }
  .sched-toast--ok  { background: rgba(34,197,94,0.15);  border:1px solid rgba(34,197,94,0.3);  color:#15803d; }
  .sched-toast--err { background: rgba(239,68,68,0.12);  border:1px solid rgba(239,68,68,0.3);  color:#b91c1c; }
`;

// ─── Main component ───────────────────────────────────────────────────────────

export const JobSchedulerPage: React.FC = () => {
  const { token } = useAuthStore();
  const [jobs,      setJobs]      = useState<SchedulerJob[]>([]);
  const [stats,     setStats]     = useState<Stats | null>(null);
  const [config,    setConfig]    = useState<SchedulerConfig>({
    max_concurrent_jobs:4, default_priority:5, retry_limit:3,
    retry_delay_seconds:30, job_timeout_seconds:3600, purge_after_days:30,
    enable_cron_jobs:true, enable_notifications:false,
  });
  const [filter,      setFilter]      = useState('');
  const [showCreate,  setShowCreate]  = useState(false);
  const [showConfig,  setShowConfig]  = useState(false);
  const [showLogs,    setShowLogs]    = useState<number | null>(null);
  const [logs,        setLogs]        = useState<JobLog[]>([]);
  const [newJob,      setNewJob]      = useState({
    type:'custom_task', name:'', description:'', category:'general',
    priority:5, payload:'', cron_expr:'',
  });

  // ── Bulk selection state ──────────────────────────────────────────────────
  const [selectedIds,  setSelectedIds]  = useState<Set<number>>(new Set());
  const [bulkLoading,  setBulkLoading]  = useState(false);
  const [toast,        setToast]        = useState<{ msg: string; type: 'ok' | 'err' } | null>(null);
  const [configSaving, setConfigSaving] = useState(false);

  // Auto-dismiss toast after 3.5 s
  const showToast = useCallback((msg: string, type: 'ok' | 'err' = 'ok') => {
    setToast({ msg, type });
    setTimeout(() => setToast(null), 3500);
  }, []);

  // ─────────────────────────────────────────────────────────────────────────

  const api = useCallback(async (path: string, opts?: RequestInit) => {
    const res = await fetch(`/api/scheduler${path}`, {
      ...opts,
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`,
        ...opts?.headers,
      },
    });
    return res.json();
  }, [token]);

  const refresh = useCallback(async () => {
    const [j, s, c] = await Promise.all([api('/jobs'), api('/stats'), api('/config')]);
    if (j.jobs)                           setJobs(j.jobs);
    if (s.total_jobs !== undefined)       setStats(s);
    if (c.max_concurrent_jobs !== undefined) setConfig(c); // use !== undefined, not truthiness
  }, [api]);

  useEffect(() => {
    if (token) {
      refresh();
      const i = setInterval(refresh, 2000);
      return () => clearInterval(i);
    }
  }, [token, refresh]);

  const doAction = async (id: number, action: string) => {
    await api(`/jobs/${id}/${action}`, { method: 'POST' });
    // Remove from selection if it was selected
    setSelectedIds(prev => { const next = new Set(prev); next.delete(id); return next; });
    refresh();
  };

  const createJob = async () => {
    if (!newJob.name.trim()) return;
    await api('/jobs', { method: 'POST', body: JSON.stringify(newJob) });
    setShowCreate(false);
    setNewJob({ type:'custom_task', name:'', description:'', category:'general', priority:5, payload:'', cron_expr:'' });
    refresh();
  };

  const saveConfig = async () => {
    setConfigSaving(true);
    try {
      const res = await api('/config', { method: 'POST', body: JSON.stringify(config) });
      if (res.status === 'saved') {
        setShowConfig(false);
        showToast('Scheduler configuration saved successfully.');
        refresh();
      } else {
        showToast(res.error || 'Failed to save configuration.', 'err');
      }
    } catch (e) {
      showToast('Network error saving configuration.', 'err');
    } finally {
      setConfigSaving(false);
    }
  };

  const loadLogs = async (id: number) => {
    setShowLogs(id);
    const data = await api(`/jobs/${id}/logs`);
    setLogs(Array.isArray(data) ? data : []);
  };

  const purge = async () => {
    if (!(await showGlobalConfirm(
      `Purge all completed/failed jobs older than ${config.purge_after_days} days?`,
      { title: 'Purge Jobs', variant: 'warning' }
    ))) return;
    await api('/purge', { method: 'POST', body: JSON.stringify({ older_than_days: config.purge_after_days }) });
    refresh();
  };

  // ── Selection helpers ────────────────────────────────────────────────────

  const filtered = filter ? jobs.filter(j => j.status === filter) : jobs;
  const filteredIds = filtered.map(j => j.id);
  const selectedCount = filteredIds.filter(id => selectedIds.has(id)).length;
  const allSelected   = filteredIds.length > 0 && filteredIds.every(id => selectedIds.has(id));
  const someSelected  = selectedCount > 0 && !allSelected;

  const toggleOne = (id: number, e: React.MouseEvent) => {
    e.stopPropagation();
    setSelectedIds(prev => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  };

  const toggleAll = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (allSelected) {
      setSelectedIds(prev => {
        const next = new Set(prev);
        filteredIds.forEach(id => next.delete(id));
        return next;
      });
    } else {
      setSelectedIds(prev => {
        const next = new Set(prev);
        filteredIds.forEach(id => next.add(id));
        return next;
      });
    }
  };

  // ── Bulk actions ─────────────────────────────────────────────────────────

  const handleBulkAction = async (action: 'retry' | 'delete' | 'cancel') => {
    const targetIds = filteredIds.filter(id => selectedIds.has(id));
    if (targetIds.length === 0) return;

    if (action === 'delete') {
      const ok = await showGlobalConfirm(
        `Permanently delete ${targetIds.length} selected job${targetIds.length > 1 ? 's' : ''} and all associated logs?`,
        { title: 'Bulk Delete', variant: 'error' }
      );
      if (!ok) return;
    }

    setBulkLoading(true);
    try {
      const res = await api('/jobs/bulk-action', {
        method: 'POST',
        body: JSON.stringify({ action, job_ids: targetIds }),
      });
      if (res.status === 'processed') {
        const succeeded: number[] = res.succeeded || [];
        const failed: { job_id: number; error: string }[] = res.failed || [];

        // Deselect jobs that were successfully acted upon
        setSelectedIds(prev => {
          const next = new Set(prev);
          succeeded.forEach(id => next.delete(id));
          return next;
        });

        if (failed.length === 0) {
          showToast(`✓ ${action.charAt(0).toUpperCase() + action.slice(1)} applied to ${succeeded.length} job${succeeded.length !== 1 ? 's' : ''}.`);
        } else if (succeeded.length === 0) {
          showToast(
            `All ${failed.length} job${failed.length !== 1 ? 's' : ''} failed to ${action}. Check job statuses.`,
            'err'
          );
        } else {
          showToast(
            `${action.charAt(0).toUpperCase() + action.slice(1)}: ${succeeded.length} succeeded, ${failed.length} failed.`,
            'err'
          );
        }
        refresh();
      } else {
        showToast(res.error || `Bulk ${action} failed. Please try again.`, 'err');
      }
    } catch (err) {
      console.error('Bulk action failed:', err);
      showToast('Network error. Please try again.', 'err');
    } finally {
      setBulkLoading(false);
    }
  };

  const handleFilterChange = (f: string) => {
    setFilter(f);
    setSelectedIds(new Set()); // Clear selection when switching tabs
  };

  // ─────────────────────────────────────────────────────────────────────────

  return (
    <div style={{ position:'relative', minHeight:'calc(100vh - 80px)', paddingBottom: selectedCount > 0 ? 100 : 0 }}>
      <style>{PAGE_STYLES}</style>

      {/* ── Toast notification ── */}
      {toast && (
        <div className={`sched-toast sched-toast--${toast.type}`}>
          {toast.type === 'ok' ? '✓ ' : '⚠ '}{toast.msg}
        </div>
      )}

      {/* ── Header ── */}
      <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:20 }}>
        <div>
          <h1 style={{ fontSize:22, fontWeight:700, color:'var(--color-brand-heading)', margin:0,
            display:'flex', alignItems:'center', gap:10 }}>
            <FiCpu style={{ color:'var(--color-brand)' }}/> Job Scheduler
          </h1>
          <p style={{ fontSize:12, color:'var(--color-brand-text)', margin:'4px 0 0' }}>
            Enterprise job queue with priority management, cron scheduling, and automatic retries.
          </p>
        </div>
        <div style={{ display:'flex', gap:8 }}>
          <button className="btn btn--primary" onClick={() => setShowCreate(true)}
            style={{ display:'flex', alignItems:'center', gap:6 }}>
            <FiPlus/> New Job
          </button>
          <button className="btn" onClick={() => setShowConfig(true)}
            style={{ display:'flex', alignItems:'center', gap:6 }}>
            <FiSettings/> Settings
          </button>
          <button className="btn" onClick={purge}
            style={{ display:'flex', alignItems:'center', gap:6, color:'var(--color-brand-red)' }}>
            <FiTrash2/> Purge
          </button>
        </div>
      </div>

      {/* ── Stats Cards ── */}
      {stats && (
        <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fit,minmax(140px,1fr))', gap:12, marginBottom:20 }}>
          {[
            { label:'Total Jobs', value:stats.total_jobs,                      icon:<FiList/>,         color:'var(--color-brand)' },
            { label:'Running',    value:stats.running_jobs,                    icon:<FiActivity/>,     color:'#f59e0b' },
            { label:'Queued',     value:stats.queued_jobs,                     icon:<FiClock/>,        color:'#3b82f6' },
            { label:'Completed',  value:stats.completed_jobs,                  icon:<FiCheckCircle/>,  color:'#22c55e' },
            { label:'Failed',     value:stats.failed_jobs,                     icon:<FiXCircle/>,      color:'#ef4444' },
            { label:'Workers',    value:`${stats.active_workers}/${stats.max_workers}`, icon:<FiCpu/>, color:'#8b5cf6' },
          ].map((s, i) => (
            <div key={i} className="g-card" style={{ padding:14, display:'flex', alignItems:'center', gap:12 }}>
              <div style={{ width:36, height:36, borderRadius:10, background:`${s.color}18`,
                display:'flex', alignItems:'center', justifyContent:'center', color:s.color, fontSize:16 }}>
                {s.icon}
              </div>
              <div>
                <div style={{ fontSize:9, fontWeight:700, textTransform:'uppercase', letterSpacing:1,
                  color:'var(--color-brand-muted)' }}>{s.label}</div>
                <div style={{ fontSize:20, fontWeight:700, color:'var(--color-brand-heading)' }}>{s.value}</div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* ── Filter Tabs ── */}
      <div style={{ display:'flex', gap:6, marginBottom:16, flexWrap:'wrap' }}>
        {(['', 'queued', 'running', 'completed', 'failed', 'cancelled', 'scheduled'] as const).map(f => (
          <button key={f} onClick={() => handleFilterChange(f)} className="btn btn--sm"
            style={{
              background:   filter === f ? 'var(--color-brand)' : '',
              color:        filter === f ? '#fff' : '',
              borderColor:  filter === f ? 'var(--color-brand)' : '',
            }}>
            {f || 'All'} ({f ? jobs.filter(j => j.status === f).length : jobs.length})
          </button>
        ))}
      </div>

      {/* ── Jobs Table ── */}
      <div className="g-card" style={{ padding:0, overflow:'hidden' }}>

        {/* Table header */}
        <div style={{
          display: 'grid',
          gridTemplateColumns: '44px 54px 1fr 104px 82px 68px 130px 90px 136px',
          gap: 8, padding: '10px 16px',
          borderBottom: '1px solid var(--color-brand-border)',
          fontSize: 10, fontWeight: 700, textTransform: 'uppercase', letterSpacing: 1,
          color: 'var(--color-brand-muted)', alignItems: 'center',
        }}>
          {/* Select-all Apple radio */}
          <AppleRadio
            checked={allSelected}
            indeterminate={someSelected}
            onClick={toggleAll}
          />
          <span>ID</span>
          <span>Job</span>
          <span>Status</span>
          <span>Priority</span>
          <span>Progress</span>
          <span>Duration</span>
          <span>Retries</span>
          <span>Actions</span>
        </div>

        {/* Empty state */}
        {filtered.length === 0 ? (
          <div style={{ padding:'50px 20px', textAlign:'center', color:'var(--color-brand-muted)' }}>
            <FiCpu size={40} style={{ margin:'0 auto 12px', opacity:.3, display:'block' }}/>
            <div style={{ fontSize:15, fontWeight:600, color:'var(--color-brand-heading)' }}>No Jobs Found</div>
            <p style={{ fontSize:12, maxWidth:300, margin:'6px auto 0' }}>Submit a new job to the scheduler queue.</p>
          </div>
        ) : filtered.map(job => {
          const isSelected = selectedIds.has(job.id);
          return (
            <div
              key={job.id}
              className={`sched-row ${isSelected ? 'sched-row--selected' : ''}`}
              style={{
                display: 'grid',
                gridTemplateColumns: '44px 54px 1fr 104px 82px 68px 130px 90px 136px',
                gap: 8, padding: '9px 16px',
                borderBottom: '1px solid var(--color-brand-border)',
                alignItems: 'center', fontSize: 13,
              }}
              onClick={() => loadLogs(job.id)}
            >
              {/* Per-row Apple radio */}
              <AppleRadio checked={isSelected} onClick={(e) => toggleOne(job.id, e)} />

              <span style={{ fontSize:11, fontFamily:'monospace', color:'var(--color-brand-muted)' }}>
                #{job.id}
              </span>

              <div style={{ minWidth:0 }}>
                <div style={{ fontWeight:600, color:'var(--color-brand-heading)',
                  whiteSpace:'nowrap', overflow:'hidden', textOverflow:'ellipsis' }}>
                  {job.name}
                </div>
                <div style={{ fontSize:10, color:'var(--color-brand-muted)',
                  display:'flex', gap:6, alignItems:'center', marginTop:2 }}>
                  <span style={{ background:'var(--color-brand-bg)', padding:'1px 6px', borderRadius:4 }}>
                    {job.type}
                  </span>
                  {job.cron_expr && <span style={{ color:'#8b5cf6' }}>⏰ {job.cron_expr}</span>}
                </div>
              </div>

              <span><StatusBadge status={job.status}/></span>
              <span><PriorityBadge p={job.priority}/></span>

              <div style={{ display:'flex', flexDirection:'column', gap:3 }}>
                <span style={{ fontSize:10, fontFamily:'monospace' }}>{job.progress}%</span>
                <ProgressBar progress={job.progress} status={job.status}/>
              </div>

              <span style={{ fontSize:11, fontFamily:'monospace', color:'var(--color-brand-text)' }}>
                {fmtDuration(job.started_at, job.finished_at)}
              </span>

              <span style={{ fontSize:11, fontFamily:'monospace' }}>
                {job.retry_count > 0 ? `${job.retry_count}×` : '—'}
              </span>

              {/* Inline action buttons — stopPropagation so they don't open logs */}
              <div style={{ display:'flex', gap:4 }} onClick={e => e.stopPropagation()}>
                {job.status === 'queued' && (
                  <button className="btn btn--sm sched-action"
                    onClick={() => doAction(job.id, 'force')} title="Force Run">
                    <FiZap size={12}/>
                  </button>
                )}
                {(job.status === 'running' || job.status === 'queued') && (
                  <button className="btn btn--sm sched-action"
                    onClick={() => doAction(job.id, 'cancel')} title="Cancel"
                    style={{ color:'#ef4444' }}>
                    <FiPause size={12}/>
                  </button>
                )}
                {(job.status === 'failed' || job.status === 'cancelled') && (
                  <button className="btn btn--sm sched-action"
                    onClick={() => doAction(job.id, 'retry')} title="Retry">
                    <FiRefreshCw size={12}/>
                  </button>
                )}
                <button className="btn btn--sm sched-action"
                  onClick={() => doAction(job.id, 'delete')} title="Delete"
                  style={{ color:'#ef4444' }}>
                  <FiTrash2 size={12}/>
                </button>
              </div>
            </div>
          );
        })}
      </div>

      {/* ── Apple-style floating bulk-action dock ── */}
      {selectedCount > 0 && (
        <div className="bulk-dock">
          {/* Selection count badge */}
          <div className="bulk-dock__count">
            <span className="bulk-dock__badge">{selectedCount}</span>
            selected
          </div>

          <div className="bulk-dock__divider" />

          {/* Actions */}
          <button
            className="bulk-dock__btn"
            onClick={() => handleBulkAction('retry')}
            disabled={bulkLoading}
            title="Retry all selected jobs"
          >
            <FiRefreshCw size={13}/> Retry
          </button>

          <button
            className="bulk-dock__btn"
            onClick={() => handleBulkAction('cancel')}
            disabled={bulkLoading}
            title="Cancel all selected jobs"
          >
            <FiPause size={13}/> Cancel
          </button>

          <button
            className="bulk-dock__btn bulk-dock__btn--danger"
            onClick={() => handleBulkAction('delete')}
            disabled={bulkLoading}
            title="Delete all selected jobs"
          >
            <FiTrash2 size={13}/> Delete
          </button>

          <div className="bulk-dock__divider" />

          {/* Dismiss */}
          <button
            className="bulk-dock__btn bulk-dock__btn--close"
            onClick={() => setSelectedIds(new Set())}
            title="Deselect all"
          >
            <FiX size={15}/>
          </button>
        </div>
      )}

      {/* ── Modal: Create Job ── */}
      {showCreate && (
        <div style={{ position:'fixed', top:0, left:0, width:'100vw', height:'100vh',
          background:'rgba(0,0,0,0.5)', backdropFilter:'blur(4px)', zIndex:1000,
          display:'flex', alignItems:'center', justifyContent:'center' }}>
          <div className="g-card" style={{ width:'100%', maxWidth:520, display:'flex', flexDirection:'column', gap:14 }}>
            <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center' }}>
              <h2 style={{ fontSize:18, fontWeight:700, margin:0, color:'var(--color-brand-heading)' }}>Submit New Job</h2>
              <button onClick={() => setShowCreate(false)}
                style={{ background:'none', border:'none', cursor:'pointer', color:'var(--color-brand-text)' }}>
                <FiX size={18}/>
              </button>
            </div>
            <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
              <div style={{ display:'flex', gap:10 }}>
                <div style={{ flex:1, display:'flex', flexDirection:'column', gap:4 }}>
                  <label style={{ fontSize:11, fontWeight:700, color:'var(--color-brand-muted)' }}>Job Type</label>
                  <select value={newJob.type}
                    onChange={e => { const t=JOB_TYPES.find(j=>j.value===e.target.value); setNewJob({...newJob,type:e.target.value,category:t?.cat||'general'}); }}
                    style={{ padding:'9px 12px', borderRadius:8, border:'1px solid var(--color-brand-border)',
                      background:'var(--color-brand-bg)', color:'var(--color-brand-heading)', outline:'none' }}>
                    {JOB_TYPES.map(t => <option key={t.value} value={t.value}>{t.label}</option>)}
                  </select>
                </div>
                <div style={{ flex:1, display:'flex', flexDirection:'column', gap:4 }}>
                  <label style={{ fontSize:11, fontWeight:700, color:'var(--color-brand-muted)' }}>Priority (1=highest)</label>
                  <input type="number" min={1} max={10} value={newJob.priority}
                    onChange={e => setNewJob({...newJob, priority:parseInt(e.target.value)||5})}
                    style={{ padding:'9px 12px', borderRadius:8, border:'1px solid var(--color-brand-border)',
                      background:'var(--color-brand-bg)', color:'var(--color-brand-heading)', outline:'none' }}/>
                </div>
              </div>
              <div style={{ display:'flex', flexDirection:'column', gap:4 }}>
                <label style={{ fontSize:11, fontWeight:700, color:'var(--color-brand-muted)' }}>Job Name</label>
                <input type="text" placeholder="e.g. Compress backup folder" value={newJob.name}
                  onChange={e => setNewJob({...newJob, name:e.target.value})}
                  style={{ padding:'9px 12px', borderRadius:8, border:'1px solid var(--color-brand-border)',
                    background:'var(--color-brand-bg)', color:'var(--color-brand-heading)', outline:'none' }}/>
              </div>
              <div style={{ display:'flex', flexDirection:'column', gap:4 }}>
                <label style={{ fontSize:11, fontWeight:700, color:'var(--color-brand-muted)' }}>Description (optional)</label>
                <textarea rows={2} placeholder="Describe what this job does..." value={newJob.description}
                  onChange={e => setNewJob({...newJob, description:e.target.value})}
                  style={{ padding:'9px 12px', borderRadius:8, border:'1px solid var(--color-brand-border)',
                    background:'var(--color-brand-bg)', color:'var(--color-brand-heading)', outline:'none', resize:'vertical' }}/>
              </div>
              <div style={{ display:'flex', flexDirection:'column', gap:4 }}>
                <label style={{ fontSize:11, fontWeight:700, color:'var(--color-brand-muted)' }}>Cron Expression (optional)</label>
                <input type="text" placeholder="e.g. 0 0 2 * * * (daily at 2am)" value={newJob.cron_expr}
                  onChange={e => setNewJob({...newJob, cron_expr:e.target.value})}
                  style={{ padding:'9px 12px', borderRadius:8, border:'1px solid var(--color-brand-border)',
                    background:'var(--color-brand-bg)', color:'var(--color-brand-heading)', outline:'none', fontFamily:'monospace' }}/>
              </div>
              <div style={{ display:'flex', flexDirection:'column', gap:4 }}>
                <label style={{ fontSize:11, fontWeight:700, color:'var(--color-brand-muted)' }}>Payload JSON (optional)</label>
                <textarea rows={2} placeholder='{"path":"/data/backup"}' value={newJob.payload}
                  onChange={e => setNewJob({...newJob, payload:e.target.value})}
                  style={{ padding:'9px 12px', borderRadius:8, border:'1px solid var(--color-brand-border)',
                    background:'var(--color-brand-bg)', color:'var(--color-brand-heading)', outline:'none',
                    fontFamily:'monospace', resize:'vertical' }}/>
              </div>
            </div>
            <div style={{ display:'flex', justifyContent:'flex-end', gap:10, marginTop:8 }}>
              <button className="btn" onClick={() => setShowCreate(false)}>Cancel</button>
              <button className="btn btn--primary" onClick={createJob} disabled={!newJob.name.trim()}>Submit Job</button>
            </div>
          </div>
        </div>
      )}

      {/* ── Modal: Settings ── */}
      {showConfig && (
        <div style={{ position:'fixed', top:0, left:0, width:'100vw', height:'100vh',
          background:'rgba(0,0,0,0.5)', backdropFilter:'blur(4px)', zIndex:1000,
          display:'flex', alignItems:'center', justifyContent:'center' }}>
          <div className="g-card" style={{ width:'100%', maxWidth:480, display:'flex', flexDirection:'column', gap:14 }}>
            <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center' }}>
              <h2 style={{ fontSize:18, fontWeight:700, margin:0, color:'var(--color-brand-heading)',
                display:'flex', alignItems:'center', gap:8 }}>
                <FiSliders/> Scheduler Configuration
              </h2>
              <button onClick={() => setShowConfig(false)}
                style={{ background:'none', border:'none', cursor:'pointer', color:'var(--color-brand-text)' }}>
                <FiX size={18}/>
              </button>
            </div>
            <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
              {([
                { key:'max_concurrent_jobs',   label:'Max Concurrent Workers',   min:1,  max:32    },
                { key:'default_priority',       label:'Default Priority (1-10)',  min:1,  max:10    },
                { key:'retry_limit',            label:'Auto-Retry Limit',         min:0,  max:10    },
                { key:'retry_delay_seconds',    label:'Retry Delay (seconds)',     min:5,  max:600   },
                { key:'job_timeout_seconds',    label:'Job Timeout (seconds)',     min:60, max:86400 },
                { key:'purge_after_days',       label:'Auto-Purge After (days)',  min:1,  max:365   },
              ] as const).map(f => (
                <div key={f.key} style={{ display:'flex', justifyContent:'space-between', alignItems:'center', gap:12 }}>
                  <label style={{ fontSize:12, fontWeight:600, color:'var(--color-brand-heading)', flex:1 }}>{f.label}</label>
                  <input type="number" min={f.min} max={f.max} value={(config as any)[f.key]}
                    onChange={e => setConfig({...config, [f.key]:parseInt(e.target.value)||f.min})}
                    style={{ width:90, padding:'7px 10px', borderRadius:8, border:'1px solid var(--color-brand-border)',
                      background:'var(--color-brand-bg)', color:'var(--color-brand-heading)', outline:'none', textAlign:'center' }}/>
                </div>
              ))}
              <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center' }}>
                <label style={{ fontSize:12, fontWeight:600, color:'var(--color-brand-heading)' }}>Enable Cron Jobs</label>
                <input type="checkbox" checked={config.enable_cron_jobs}
                  onChange={e => setConfig({...config, enable_cron_jobs:e.target.checked})}
                  style={{ accentColor:'var(--color-brand)', width:18, height:18 }}/>
              </div>
            </div>
            <div style={{ display:'flex', justifyContent:'flex-end', gap:10, marginTop:8 }}>
              <button className="btn" onClick={() => setShowConfig(false)} disabled={configSaving}>Cancel</button>
              <button className="btn btn--primary" onClick={saveConfig} disabled={configSaving}
                style={{ display:'flex', alignItems:'center', gap:6 }}>
                {configSaving ? (
                  <><FiRefreshCw size={13} style={{ animation:'spin 0.8s linear infinite' }}/> Saving…</>
                ) : 'Save Configuration'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ── Modal: Job Logs ── */}
      {showLogs !== null && (
        <div style={{ position:'fixed', top:0, left:0, width:'100vw', height:'100vh',
          background:'rgba(0,0,0,0.5)', backdropFilter:'blur(4px)', zIndex:1000,
          display:'flex', alignItems:'center', justifyContent:'center' }}>
          <div className="g-card" style={{ width:'100%', maxWidth:660, maxHeight:'80vh',
            display:'flex', flexDirection:'column' }}>
            <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:12 }}>
              <h2 style={{ fontSize:16, fontWeight:700, margin:0, color:'var(--color-brand-heading)',
                display:'flex', alignItems:'center', gap:8 }}>
                <FiTerminal/> Job #{showLogs} — Execution Logs
              </h2>
              <button onClick={() => { setShowLogs(null); setLogs([]); }}
                style={{ background:'none', border:'none', cursor:'pointer', color:'var(--color-brand-text)' }}>
                <FiX size={18}/>
              </button>
            </div>
            <div style={{ flex:1, overflowY:'auto', background:'#0d1117', borderRadius:8,
              padding:12, fontFamily:'monospace', fontSize:11, lineHeight:1.8 }}>
              {logs.length === 0 ? (
                <div style={{ color:'#8b949e', textAlign:'center', padding:30 }}>No logs recorded yet.</div>
              ) : logs.map(l => (
                <div key={l.id} style={{ display:'flex', gap:8,
                  color: l.level==='ERROR' ? '#f85149' : l.level==='WARN' ? '#d29922' : '#8b949e' }}>
                  <span style={{ color:'#484f58', flexShrink:0 }}>{new Date(l.created_at).toLocaleTimeString()}</span>
                  <span style={{ fontWeight:700, width:44, flexShrink:0 }}>[{l.level}]</span>
                  <span style={{ color: l.level==='ERROR' ? '#f85149' : l.level==='WARN' ? '#d29922' : '#c9d1d9' }}>
                    {l.message}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
