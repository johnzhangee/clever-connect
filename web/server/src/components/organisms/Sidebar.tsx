import React from 'react';
import { FiGrid, FiLayers, FiGlobe, FiRepeat, FiShield, FiTrendingUp, FiUsers, FiSettings, FiBook, FiHelpCircle, FiLogOut, FiSearch, FiZap, FiMenu, FiCpu, FiFolder, FiSend } from 'react-icons/fi';

interface SidebarProps {
  activeTab: string;
  setActiveTab: (tab: string) => void;
  isCollapsed: boolean;
  setIsCollapsed: (collapsed: boolean) => void;
}

const navItems = [
  { id: 'dashboard', label: 'Dashboard', icon: FiGrid },
  {
    id: 'protocol', label: 'Protocol', icon: FiCpu,
    children: [
      { id: 'ehco-tunnel', label: 'Ehco' },
      { id: 'soroush-tunnel', label: 'Soroush' },
      { id: 'trusttunnel', label: 'TrustTunnel' },
      { id: 'bonding-combiner', label: 'Multipath Combiner' },
      { id: 'cloudflare', label: 'Cloudflare' }
    ]
  },
  {
    id: 'v2ray-section', label: 'V2Ray Manager', icon: FiGlobe,
    children: [
      { id: 'v2ray-dashboard', label: 'Dashboard & Nodes' },
      { id: 'v2ray-core', label: 'Core Configuration' },
      { id: 'v2ray-routing', label: 'Routing Rules' }
    ]
  },
  {
    id: 'files-section', label: 'Files', icon: FiFolder,
    children: [
      { id: 'files', label: 'File manager' },
      { id: 'cloud-storage', label: 'Cloud storage' },
      { id: 'leech', label: 'Leech manager' },
      { id: 'torrent', label: 'Torrent client' },
      { id: 'youtube', label: 'YouTube downloader' },
      { id: 'spotify', label: 'Spotify downloader' },
    ]
  },
  { id: 'fw-logs', label: 'System Logs', icon: FiShield },
  {
    id: 'system-section', label: 'System', icon: FiCpu,
    children: [
      { id: 'scheduler', label: 'Job Scheduler' },
    ]
  },
  {
    id: 'settings-section', label: 'Settings', icon: FiSettings,
    children: [
      { id: 'settings', label: 'App settings' },
      { id: 'telegram-settings', label: 'Telegram' },
    ]
  },
];

export const Sidebar: React.FC<SidebarProps> = ({ activeTab, setActiveTab, isCollapsed, setIsCollapsed }) => {
  const username = localStorage.getItem('cc_client_username') || 'salman';

  const [openMenu, setOpenMenu] = React.useState<string | null>(() => {
    const matched = navItems.find(item => item.children && item.children.some(c => c.id === activeTab));
    return matched ? matched.id : null;
  });

  React.useEffect(() => {
    const matched = navItems.find(item => item.children && item.children.some(c => c.id === activeTab));
    setOpenMenu(matched ? matched.id : null);
  }, [activeTab]);

  return (
    <aside className={`sidebar ${isCollapsed ? 'sidebar--collapsed' : ''}`}>
      {/* Header */}
      <div className="sidebar__header">
        <div className="sidebar__logo-icon" style={{ background: 'none', padding: 0, overflow: 'hidden' }}>
          <img src="/favicon.png" alt="Logo" style={{ width: '100%', height: '100%', objectFit: 'contain', borderRadius: 'inherit' }} />
        </div>
        {!isCollapsed && <span className="sidebar__logo-text">CleverConnect<sup>®</sup></span>}
        <button className="sidebar__toggle" onClick={() => setIsCollapsed(!isCollapsed)}>
          <FiMenu size={14} />
        </button>
      </div>

      {/* Navigation */}
      <nav className="sidebar__nav" style={{ marginTop: 20 }}>
        {navItems.map((item) => (
          <React.Fragment key={item.id}>
            <div
              className={`sidebar__item ${activeTab === item.id || (item.children && item.children.some(c => c.id === activeTab)) ? 'sidebar__item--active' : ''}`}
              onClick={() => {
                if (item.children && item.children.length > 0) {
                  // Navigate to the first child by default on click
                  setActiveTab(item.children[0].id);
                } else {
                  setActiveTab(item.id);
                }
              }}
              title={isCollapsed ? item.label : undefined}
            >
              <item.icon className="nav-icon" />
              {!isCollapsed && <span>{item.label}</span>}
            </div>
            {!isCollapsed && item.children && item.children.length > 0 && openMenu === item.id && (
              <div className="sidebar__submenu">
                {item.children.map((child) => (
                  <div
                    key={child.id}
                    className={`sidebar__submenu-item ${activeTab === child.id ? 'sidebar__submenu-item--active' : ''}`}
                    onClick={() => setActiveTab(child.id)}
                  >
                    <span>{child.label}</span>
                  </div>
                ))}
              </div>
            )}
          </React.Fragment>
        ))}
      </nav>

      <div className="sidebar__divider" />

      <div style={{ padding: isCollapsed ? '16px 0' : '0 16px 16px', display: 'flex', flexDirection: isCollapsed ? 'column' : 'row', gap: 10, justifyContent: 'space-between', alignItems: 'center', fontSize: 11, color: 'var(--color-brand-text)' }}>
        {!isCollapsed && (
          <div>
            <div style={{ fontSize: 9, textTransform: 'uppercase', letterSpacing: 1, color: 'var(--color-brand-muted)', marginBottom: 2 }}>Logged in as</div>
            <div style={{ fontWeight: 600, color: 'var(--color-brand-heading)' }}>{username}</div>
          </div>
        )}
        <FiLogOut style={{ cursor: 'pointer', fontSize: 15 }} title="Logout" onClick={() => { localStorage.clear(); window.location.reload(); }} />
      </div>
    </aside>
  );
};
