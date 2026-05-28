import {
  Sun,
  Moon,
  Wifi,
  WifiOff,
  AlertTriangle,
  Clock
} from 'lucide-react';
import { ActivePage, ConnectionState, Theme } from '../types';

interface HeaderProps {
  activePage: ActivePage;
  theme: Theme;
  setTheme: (t: Theme) => void;
  connectionState: ConnectionState;
  replayTimeLabel: string;
}

export default function Header({
  activePage,
  theme,
  setTheme,
  connectionState,
  replayTimeLabel,
}: HeaderProps) {
  const isDark = theme === 'dark';
  const meta = getPageMeta(activePage);
  const toggleTheme = () => setTheme(theme === 'light' ? 'dark' : 'light');

  return (
    <header className="border-b px-6 py-4 transition-colors duration-200 bg-app-sidebar border-app-border text-app-text-main">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
        <div className="space-y-0.5">
          <div className="flex items-center space-x-2">
            <span className="text-[10px] font-mono px-2 py-0.5 rounded uppercase font-bold bg-app-accent/10 text-app-accent">
              Sandbox Ops
            </span>
            <span className="text-xs text-app-text-muted">/</span>
            <span className="text-xs font-semibold capitalize text-app-accent">{activePage.replace('-', ' ')}</span>
          </div>
          <h1 className="text-lg font-bold tracking-tight">{meta.title}</h1>
          <p className="text-xs text-app-text-muted">{meta.desc}</p>
        </div>

        <div className="flex flex-wrap items-center gap-2 md:gap-3">
          <div className="flex items-center space-x-1 py-1 px-2 rounded-lg border text-xs font-mono bg-app-bg border-app-border text-app-text-main">
            <Clock size={13} className="text-app-accent" />
            <span className="text-[10px] text-app-text-muted uppercase font-semibold">Backend Replay Time</span>
            <span className="text-[11px] font-semibold text-app-accent max-w-[220px] truncate" title={replayTimeLabel}>
              {replayTimeLabel}
            </span>
          </div>

          <div className={connectionBadgeClass(connectionState)}>
            {connectionState === 'Connected' || connectionState === 'Fallback Active' ? <Wifi size={13} /> : <WifiOff size={13} />}
            <span className="capitalize text-[11px] font-mono">{connectionState}</span>
          </div>

          <button
            onClick={toggleTheme}
            className="p-2 rounded-lg border transition-colors cursor-pointer bg-app-bg border-app-border hover:bg-app-card text-app-text-muted hover:text-app-text-main"
            title={isDark ? 'Switch to Light Theme' : 'Switch to Dark Theme'}
          >
            {isDark ? <Sun size={14} /> : <Moon size={14} />}
          </button>
        </div>
      </div>

      {connectionState !== 'Connected' && (
        <div className={connectionBannerClass(connectionState)}>
          <div className="flex items-center space-x-2">
            <AlertTriangle size={14} className="flex-shrink-0" />
            <span>{connectionBannerText(connectionState)}</span>
          </div>
        </div>
      )}
    </header>
  );
}

function getPageMeta(page: ActivePage) {
  switch (page) {
    case 'overview':
      return { title: 'Global Operations Dashboard', desc: 'Realtime view across backend replay sandboxes and metadata metrics.' };
    case 'datasets':
      return { title: 'Replay Datasets', desc: 'Manage historical backtest files and high-frequency market simulation files.' };
    case 'sandbox-accounts':
      return { title: 'Virtual Sandbox Accounts', desc: 'Provision test accounts, track balances, and manage API keys and tokens.' };
    case 'live-accounts':
      return { title: 'Live Metadata Registers', desc: 'Operational metadata summary. Safe environment with no execution triggers.' };
    case 'system-health':
      return { title: 'Operator System Health', desc: 'Monitor backend live-symbol cache and connection freshness.' };
    case 'sandboxes':
      return { title: 'Sandboxes', desc: 'Create and control replay or live sandbox environments through backend APIs.' };
    case 'sandbox-detail':
      return { title: 'Sandbox Detail', desc: 'Inspect backend replay controls, account state, events, and indicators for one sandbox.' };
    case 'sandbox-monitor':
      return { title: 'Sandbox Monitor', desc: 'Watch backend account records, events, and indicators for a selected sandbox.' };
    case 'performance':
      return { title: 'Account Performance Ranking', desc: 'Compare backend sandbox account balances and equity without synthetic rows.' };
    case 'activity':
      return { title: 'Operational Activity Log', desc: 'Audit backend-derived activity records.' };
    case 'alerts':
      return { title: 'Alert Resolution Center', desc: 'Backend-derived exceptions and feed freshness warnings.' };
    case 'agent-boundary':
      return { title: 'Agent Boundary', desc: 'Review what agent tokens are allowed and forbidden to do.' };
    default:
      return { title: 'Operations Console', desc: 'Interactive sandbox operations console' };
  }
}

function connectionBadgeClass(connectionState: ConnectionState): string {
  const base = 'flex items-center space-x-1.5 px-2.5 py-1.5 rounded-lg border text-xs font-medium';
  if (connectionState === 'Connected') {
    return `${base} bg-app-success/10 text-app-success border-app-success/20`;
  }
  if (connectionState === 'Reconnecting') {
    return `${base} bg-app-warning/10 text-app-warning border-app-warning/20`;
  }
  if (connectionState === 'Disconnected') {
    return `${base} bg-app-error/10 text-app-error border-app-error/20`;
  }
  if (connectionState === 'Fallback Active') {
    return `${base} bg-purple-500/10 text-purple-600 dark:text-purple-400 border-purple-500/20`;
  }
  return `${base} bg-app-accent/10 text-app-accent border-app-accent/20`;
}

function connectionBannerClass(connectionState: ConnectionState): string {
  const base = 'mt-3 p-2 rounded-lg text-xs flex items-center justify-between border';
  if (connectionState === 'Disconnected') {
    return `${base} bg-app-error/10 text-app-error border-app-error/30`;
  }
  if (connectionState === 'Reconnecting') {
    return `${base} bg-app-warning/10 text-app-warning border-app-warning/30`;
  }
  if (connectionState === 'Stale Data') {
    return `${base} bg-app-accent/10 text-app-accent border-app-accent/30`;
  }
  return `${base} bg-purple-500/10 text-purple-600 dark:text-purple-400 border-purple-500/30`;
}

function connectionBannerText(connectionState: ConnectionState): string {
  if (connectionState === 'Disconnected') {
    return 'Backend API is not connected. Authenticate successfully to load live backend data.';
  }
  if (connectionState === 'Reconnecting') {
    return 'Reconnecting to the backend API.';
  }
  if (connectionState === 'Stale Data') {
    return 'Backend data is stale. Refresh the current view before acting on it.';
  }
  return 'Backend fallback mode is active.';
}
