import { create } from 'zustand';

export interface TrustTunnelConfig {
  ID?: number;
  is_active: boolean;
  listen_address: string;
  connect_address: string;
  socks5_port: number;
  http_port: number;
  forced_transport: 'http2' | 'http1' | 'quic';
  auth_failure_status_code: number;
  client_random_prefix: string;
  h2_initial_stream_window_size: number;
  h2_initial_conn_window_size: number;
  tls_handshake_timeout_secs: number;
  kill_switch_enabled: boolean;
  active_preset: string;
  tls_cert_path: string;
  tls_key_path: string;
  server_hostname: string;
  public_tls_port?: number;
  client_username?: string;
  client_password?: string;
  tls_server_cert?: string;
  skip_tls_verify?: boolean;
  dns_upstreams?: string;
  UpdatedAt?: string;
}

export interface TrustTunnelUser {
  id: number;
  username: string;
  is_active: boolean;
  created_at?: string;
}

export interface TrustTunnelFirewallRule {
  id: number;
  target_cidr: string;
  bypass_strategy: string;
  description: string;
}

interface TrustTunnelStore {
  config: TrustTunnelConfig | null;
  users: TrustTunnelUser[];
  rules: TrustTunnelFirewallRule[];
  isRunning: boolean;
  isLoading: boolean;
  appMode: 'server' | 'client';
  error: string | null;
  successMessage: string | null;

  fetchConfig: () => Promise<void>;
  saveConfig: (cfg: Partial<TrustTunnelConfig>) => Promise<void>;
  startEngine: () => Promise<void>;
  stopEngine: () => Promise<void>;
  addUser: (username: string, password: string) => Promise<void>;
  deleteUser: (id: number) => Promise<void>;
  addRule: (cidr: string, strategy: string, desc?: string) => Promise<void>;
  deleteRule: (id: number) => Promise<void>;
  exportConnectionToken: () => Promise<string>;
  importConnectionToken: (token: string) => Promise<void>;
  clearMessages: () => void;
}

const getToken = () =>
  localStorage.getItem('cc_client_token') || localStorage.getItem('cc_server_token') || '';

const apiHeaders = () => ({
  Authorization: `Bearer ${getToken()}`,
  'Content-Type': 'application/json',
});

export const useTrustTunnelStore = create<TrustTunnelStore>((set, get) => ({
  config: null,
  users: [],
  rules: [],
  isRunning: false,
  isLoading: false,
  appMode: 'client',
  error: null,
  successMessage: null,

  clearMessages: () => set({ error: null, successMessage: null }),

  fetchConfig: async () => {
    set({ isLoading: true, error: null });
    try {
      const res = await fetch('/api/trusttunnel/config', { headers: apiHeaders() });
      if (res.ok) {
        const data = await res.json();
        set({
          config: data.config,
          users: data.users || [],
          rules: data.rules || [],
          isRunning: data.is_running,
          appMode: data.app_mode || 'client',
        });
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to fetch TrustTunnel configuration' });
      }
    } catch {
      set({ error: 'Network error while fetching TrustTunnel configuration' });
    } finally {
      set({ isLoading: false });
    }
  },

  saveConfig: async (cfg) => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch('/api/trusttunnel/config', {
        method: 'POST',
        headers: apiHeaders(),
        body: JSON.stringify(cfg),
      });
      if (res.ok) {
        const data = await res.json();
        set({
          config: data.config,
          isRunning: data.is_running,
          successMessage: 'Configuration saved successfully',
        });
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to save TrustTunnel configuration' });
      }
    } catch {
      set({ error: 'Network error while saving configuration' });
    } finally {
      set({ isLoading: false });
    }
  },

  startEngine: async () => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch('/api/trusttunnel/start', {
        method: 'POST',
        headers: apiHeaders(),
      });
      if (res.ok) {
        const data = await res.json();
        set({
          isRunning: data.is_running,
          successMessage: 'TrustTunnel engine started successfully',
        });
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to start TrustTunnel engine' });
      }
    } catch {
      set({ error: 'Network error while starting TrustTunnel engine' });
    } finally {
      set({ isLoading: false });
    }
  },

  stopEngine: async () => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch('/api/trusttunnel/stop', {
        method: 'POST',
        headers: apiHeaders(),
      });
      if (res.ok) {
        const data = await res.json();
        set({
          isRunning: data.is_running,
          successMessage: 'TrustTunnel engine stopped',
        });
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to stop TrustTunnel engine' });
      }
    } catch {
      set({ error: 'Network error while stopping TrustTunnel engine' });
    } finally {
      set({ isLoading: false });
    }
  },

  addUser: async (username, password) => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch('/api/trusttunnel/users', {
        method: 'POST',
        headers: apiHeaders(),
        body: JSON.stringify({ username, password }),
      });
      if (res.ok) {
        set({ successMessage: 'User created successfully' });
        // Refresh config / users list
        await get().fetchConfig();
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to add proxy user' });
      }
    } catch {
      set({ error: 'Network error while creating proxy user' });
    } finally {
      set({ isLoading: false });
    }
  },

  deleteUser: async (id) => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch(`/api/trusttunnel/users/${id}`, {
        method: 'DELETE',
        headers: apiHeaders(),
      });
      if (res.ok) {
        set({ successMessage: 'User deleted' });
        await get().fetchConfig();
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to delete user' });
      }
    } catch {
      set({ error: 'Network error while deleting user' });
    } finally {
      set({ isLoading: false });
    }
  },

  addRule: async (cidr, strategy, desc = '') => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch('/api/trusttunnel/rules', {
        method: 'POST',
        headers: apiHeaders(),
        body: JSON.stringify({ target_cidr: cidr, bypass_strategy: strategy, description: desc }),
      });
      if (res.ok) {
        set({ successMessage: 'Firewall rule created' });
        await get().fetchConfig();
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to add rule' });
      }
    } catch {
      set({ error: 'Network error while adding firewall rule' });
    } finally {
      set({ isLoading: false });
    }
  },

  deleteRule: async (id) => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch(`/api/trusttunnel/rules/${id}`, {
        method: 'DELETE',
        headers: apiHeaders(),
      });
      if (res.ok) {
        set({ successMessage: 'Firewall rule deleted' });
        await get().fetchConfig();
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to delete rule' });
      }
    } catch {
      set({ error: 'Network error while deleting rule' });
    } finally {
      set({ isLoading: false });
    }
  },

  exportConnectionToken: async () => {
    set({ isLoading: true, error: null });
    try {
      const res = await fetch('/api/trusttunnel/export', { headers: apiHeaders() });
      if (res.ok) {
        const data = await res.json();
        return data.token;
      } else {
        const err = await res.json();
        throw new Error(err.error || 'Failed to export connection token');
      }
    } catch (e: any) {
      set({ error: e.message || 'Network error while exporting token' });
      throw e;
    } finally {
      set({ isLoading: false });
    }
  },

  importConnectionToken: async (token) => {
    set({ isLoading: true, error: null, successMessage: null });
    try {
      const res = await fetch('/api/trusttunnel/import', {
        method: 'POST',
        headers: apiHeaders(),
        body: JSON.stringify({ token }),
      });
      if (res.ok) {
        const data = await res.json();
        set({
          config: data.config,
          successMessage: 'Token imported successfully',
        });
      } else {
        const err = await res.json();
        set({ error: err.error || 'Failed to import connection token' });
      }
    } catch {
      set({ error: 'Network error while importing token' });
    } finally {
      set({ isLoading: false });
    }
  },
}));
