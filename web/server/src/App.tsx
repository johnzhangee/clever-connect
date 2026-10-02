import React, { Suspense, useEffect, lazy } from 'react';
import { createBrowserRouter, RouterProvider, Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { useAuthStore } from './store/authStore';
import { PanelLayout } from './components/templates/PanelLayout';

// Lazy-loaded pages
const LoginPage = lazy(() => import('./pages/LoginPage').then(m => ({ default: m.LoginPage })));
const DashboardPage = lazy(() => import('./pages/DashboardPage').then(m => ({ default: m.DashboardPage })));
const SettingsPage = lazy(() => import('./pages/SettingsPage').then(m => ({ default: m.SettingsPage })));
const LogsPage = lazy(() => import('./pages/LogsPage').then(m => ({ default: m.LogsPage })));
const EhcoServerPage = lazy(() => import('./pages/EhcoServerPage').then(m => ({ default: m.EhcoServerPage })));
const FilesPage = lazy(() => import('./pages/FilesPage').then(m => ({ default: m.FilesPage })));
const LeechPage = lazy(() => import('./pages/LeechPage').then(m => ({ default: m.LeechPage })));
const PlayerPage = lazy(() => import('./pages/PlayerPage').then(m => ({ default: m.PlayerPage })));
const TorrentPage = lazy(() => import('./pages/TorrentPage').then(m => ({ default: m.TorrentPage })));
const YouTubePage = lazy(() => import('./pages/YouTubePage').then(m => ({ default: m.YouTubePage })));
const SpotifyPage = lazy(() => import('./pages/SpotifyPage').then(m => ({ default: m.SpotifyPage })));
const CloudStoragePage = lazy(() => import('./pages/CloudStoragePage').then(m => ({ default: m.CloudStoragePage })));
const TelegramSettingsPage = lazy(() => import('./pages/TelegramSettingsPage').then(m => ({ default: m.TelegramSettingsPage })));
const JobSchedulerPage = lazy(() => import('./pages/JobSchedulerPage').then(m => ({ default: m.JobSchedulerPage })));
const SoroushPage = lazy(() => import('./pages/SoroushPage').then(m => ({ default: m.SoroushPage })));
const V2RayServerPage = lazy(() => import('./pages/V2RayServerPage').then(m => ({ default: m.V2RayServerPage })));
const V2RayCorePage = lazy(() => import('./pages/V2RayCorePage').then(m => ({ default: m.V2RayCorePage })));
const V2RayRoutingPage = lazy(() => import('./pages/V2RayRoutingPage').then(m => ({ default: m.V2RayRoutingPage })));
const BondingCombinerPage = lazy(() => import('./pages/BondingCombinerPage').then(m => ({ default: m.BondingCombinerPage })));
const CloudflarePage = lazy(() => import('./pages/CloudflarePage').then(m => ({ default: m.CloudflarePage })));
const TrustTunnelPage = lazy(() => import('./pages/TrustTunnelPage').then(m => ({ default: m.TrustTunnelPage })));

// Loading spinner
const PageLoader = () => (
  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100vh', background: 'var(--color-brand-bg)' }}>
    <div style={{ width: 32, height: 32, border: '3px solid var(--color-brand-border)', borderTopColor: 'var(--color-brand)', borderRadius: '50%', animation: 'spin .6s linear infinite' }} />
    <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
  </div>
);

// Protected wrapper synchronizing URL path with sidebar highlight
const ProtectedLayout: React.FC = () => {
  const { isAuthenticated, initialize } = useAuthStore();
  const location = useLocation();
  const navigate = useNavigate();

  useEffect(() => { initialize(); }, [initialize]);

  if (!isAuthenticated) return <Navigate to="/login" replace />;

  const currentTab = location.pathname.split('/')[1] || 'dashboard';

  const crumbs: Record<string, string[]> = {
    dashboard: ['Dashboard', 'Overview'],
    clients: ['Users', 'Clients'],
    settings: ['Configure', 'Settings'],
    'fw-logs': ['System Logs', 'Diagnostics'],
    'ehco-tunnel': ['Protocol', 'Ehco'],
    files: ['Storage', 'Files Explorer'],
    'cloud-storage': ['Storage', 'Cloud Storage'],
    leech: ['Storage', 'Remote Leech Manager'],
    torrent: ['Storage', 'Torrent Client'],
    youtube: ['Storage', 'YouTube Downloader'],
    spotify: ['Storage', 'Spotify Downloader'],
    'telegram-settings': ['Settings', 'Telegram Bot'],
    scheduler: ['System', 'Job Scheduler'],
    'soroush-tunnel': ['Protocol', 'Soroush WebRTC Tunnel'],
    trusttunnel: ['Protocol', 'TrustTunnel Stealth'],
    'v2ray-dashboard': ['V2Ray', 'Dashboard & Nodes'],
    'v2ray-core': ['V2Ray', 'Core Configuration'],
    'v2ray-routing': ['V2Ray', 'Routing Rules'],
    'bonding-combiner': ['Protocol', 'Multipath Combiner'],
    cloudflare: ['Protocol', 'Cloudflare'],
  };

  // Inject user local preferences (Font and Theme) on initial bootstrap
  useEffect(() => {
    const savedFont = localStorage.getItem('cc_font') || 'inter';
    document.body.classList.add(`font-${savedFont}`);

    const savedTheme = localStorage.getItem('cc_theme') || 'light';
    const applyThemeMode = (isDark: boolean) => {
      if (isDark) {
        document.body.classList.add('dark-theme');
      } else {
        document.body.classList.remove('dark-theme');
      }
    };

    if (savedTheme === 'system') {
      const isDarkOS = window.matchMedia('(prefers-color-scheme: dark)').matches;
      applyThemeMode(isDarkOS);
    } else {
      applyThemeMode(savedTheme === 'dark');
    }
  }, []);

  return (
    <PanelLayout activeTab={currentTab} setActiveTab={(tab) => navigate(`/${tab}`)} breadcrumbs={crumbs[currentTab] || ['Dashboard']}>
      <Suspense fallback={<PageLoader />}>
        <Outlet />
      </Suspense>
    </PanelLayout>
  );
};

// Auth guard for login
const LoginGuard: React.FC = () => {
  const { isAuthenticated, initialize } = useAuthStore();
  useEffect(() => { initialize(); }, [initialize]);
  if (isAuthenticated) return <Navigate to="/dashboard" replace />;
  return <Suspense fallback={<PageLoader />}><LoginPage /></Suspense>;
};

const PlayerGuard: React.FC = () => {
  const { isAuthenticated, initialize } = useAuthStore();
  useEffect(() => { initialize(); }, [initialize]);
  if (!isAuthenticated) return <Navigate to="/login" replace />;
  return <Suspense fallback={<PageLoader />}><PlayerPage /></Suspense>;
};

const router = createBrowserRouter([
  { path: '/login', element: <LoginGuard /> },
  { path: '/player', element: <PlayerGuard /> },
  {
    path: '/',
    element: <ProtectedLayout />,
    children: [
      { index: true, element: <Navigate to="/dashboard" replace /> },
      { path: 'dashboard', element: <DashboardPage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: 'fw-logs', element: <LogsPage /> },
      { path: 'ehco-tunnel', element: <EhcoServerPage /> },
      { path: 'files', element: <FilesPage /> },
      { path: 'cloud-storage', element: <CloudStoragePage /> },
      { path: 'leech', element: <LeechPage /> },
      { path: 'torrent', element: <TorrentPage /> },
      { path: 'youtube', element: <YouTubePage /> },
      { path: 'spotify', element: <SpotifyPage /> },
      { path: 'telegram-settings', element: <TelegramSettingsPage /> },
      { path: 'scheduler', element: <JobSchedulerPage /> },
      { path: 'soroush-tunnel', element: <SoroushPage /> },
      { path: 'trusttunnel', element: <TrustTunnelPage /> },
      { path: 'v2ray-dashboard', element: <V2RayServerPage /> },
      { path: 'v2ray-core', element: <V2RayCorePage /> },
      { path: 'v2ray-routing', element: <V2RayRoutingPage /> },
      { path: 'bonding-combiner', element: <BondingCombinerPage /> },
      { path: 'cloudflare', element: <CloudflarePage /> },
    ],
  },
  { path: '*', element: <Navigate to="/dashboard" replace /> },
]);

import { GlobalDialog } from './components/molecules/GlobalDialog';

export default function App() {
  return (
    <>
      <RouterProvider router={router} />
      <GlobalDialog />
    </>
  );
}
