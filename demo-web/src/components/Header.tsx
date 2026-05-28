import { useState, useEffect } from 'react';
import {
  Sun,
  Moon,
  Clock,
  Wifi,
  WifiOff,
  Gauge,
  SlidersHorizontal,
  RefreshCw,
  AlertTriangle
} from 'lucide-react';
import { ActivePage, Theme, DemoVisualState } from '../types';

interface HeaderProps {
  activePage: ActivePage;
  theme: Theme;
  setTheme: (t: Theme) => void;
  connectionState: 'Connected' | 'Reconnecting' | 'Disconnected' | 'Fallback Active' | 'Stale Data';
  setConnectionState: (state: any) => void;
  playbackSpeed: number; // 0 for pause, 1, 10, 50
  setPlaybackSpeed: (speed: number) => void;
  demoState: DemoVisualState;
  setDemoState: (state: DemoVisualState) => void;
  // Dynamic replay clock
  simulatedTime: string;
  setSimulatedTime: (time: string) => void;
}

export default function Header({
  activePage,
  theme,
  setTheme,
  connectionState,
  setConnectionState,
  playbackSpeed,
  setPlaybackSpeed,
  demoState,
  setDemoState,
  simulatedTime,
  setSimulatedTime
}: HeaderProps) {
  const isDark = theme === 'dark';

  // Format active page to header title
  const getPageMeta = (page: ActivePage) => {
    switch (page) {
      case 'overview':
        return { title: 'Global Operations Dashboard', desc: 'Realtime view across all active replay sandboxes and metadata metrics.' };
      case 'datasets':
        return { title: 'Replay Datasets', desc: 'Manage historical backtest files and high-frequency market simulation files.' };
      case 'sandbox-accounts':
        return { title: 'Virtual Sandbox Accounts', desc: 'Provision test accounts, track balances, and manage API keys & tokens.' };
      case 'live-accounts':
        return { title: 'Live Metadata Registers', desc: 'Operational metadata summary. Safe environment — strictly no execution triggers.' };
      case 'system-health':
        return { title: 'Operator System Health', desc: 'Monitor ingest cache pipeline statistics and connection latency records.' };
      case 'sandboxes':
        return { title: 'Sandboxes Control', desc: 'Configure, start, pause, or terminate isolated broker state machines.' };
      case 'sandbox-monitor':
        return { title: 'Sandbox Realtime Monitor', desc: 'Deep-dive analytical monitoring of a selected sandbox and indicators.' };
      case 'performance':
        return { title: 'Account Performance Ranking', desc: 'Compare multi-account sandbox strategies, drawdowns, and return curves.' };
      case 'activity':
        return { title: 'Operational Activity Log', desc: 'Search and audit telemetry triggers, order files, and boundary blocks.' };
      case 'alerts':
        return { title: 'Alert Resolution Center', desc: 'Categorized exception log indicating system latency, or disallowed actions.' };
      case 'agent-boundary':
        return { title: 'Agent Boundary Rules', desc: 'Read-only specification definitions governing token safety thresholds.' };
      default:
        return { title: 'Operations Console', desc: 'Interactive Sandbox Console' };
    }
  };

  const meta = getPageMeta(activePage);

  // Generate a fake auto-updating simulated time tick when speed is > 0
  useEffect(() => {
    if (playbackSpeed === 0) return;
    
    const interval = setInterval(() => {
      // Parse current string and add seconds based on speed
      const baseDate = new Date(simulatedTime);
      if (!isNaN(baseDate.getTime())) {
        const addedSeconds = Math.max(1, Math.floor(playbackSpeed / 2));
        baseDate.setSeconds(baseDate.getSeconds() + addedSeconds);
        
        // Format to ISO-like pretty style: "2026-05-24 19:33:57 UTC"
        const year = baseDate.getUTCFullYear();
        const month = String(baseDate.getUTCMonth() + 1).padStart(2, '0');
        const day = String(baseDate.getUTCDate()).padStart(2, '0');
        const hours = String(baseDate.getUTCHours()).padStart(2, '0');
        const minutes = String(baseDate.getUTCMinutes()).padStart(2, '0');
        const seconds = String(baseDate.getUTCSeconds()).padStart(2, '0');
        setSimulatedTime(`${year}-${month}-${day} ${hours}:${minutes}:${seconds} UTC`);
      }
    }, 1000);

    return () => clearInterval(interval);
  }, [playbackSpeed, simulatedTime]);

  const toggleTheme = () => setTheme(theme === 'light' ? 'dark' : 'light');

  return (
    <header
      className="border-b px-6 py-4 transition-colors duration-200 bg-app-sidebar border-app-border text-app-text-main"
    >
      {/* Upper Header Row */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
        
        {/* Title and breadcrumbs */}
        <div className="space-y-0.5">
          <div className="flex items-center space-x-2">
            <span className="text-[10px] font-mono px-2 py-0.5 rounded uppercase font-bold bg-app-accent/10 text-app-accent">
              Sandbox Ops
            </span>
            <span className="text-xs text-app-text-muted">/</span>
            <span className="text-xs font-semibold capitalize text-app-accent">{activePage.replace('-', ' ')}</span>
          </div>
          <h1 className="text-lg font-bold tracking-tight">{meta.title}</h1>
          <p className="text-xs text-app-text-muted">
            {meta.desc}
          </p>
        </div>

        {/* Right side controls */}
        <div className="flex flex-wrap items-center gap-2 md:gap-3">
          
          {/* Simulated playback controls */}
          <div className="flex items-center space-x-1 py-1 px-2 rounded-lg border text-xs font-mono select-none bg-app-bg border-app-border text-app-text-main">
            <Clock size={13} className="text-app-accent" />
            <span className="text-[11px] font-semibold text-app-accent min-w-[150px] text-center">
              {simulatedTime}
            </span>
            <span className="text-app-text-muted/50">|</span>
            <button
              onClick={() => setPlaybackSpeed(0)}
              className={`px-1 rounded cursor-pointer ${playbackSpeed === 0 ? 'bg-app-accent text-white font-bold' : 'text-app-text-muted hover:text-app-text-main'}`}
              title="Pause replay ticker"
            >
              PAUSE
            </button>
            <button
              onClick={() => setPlaybackSpeed(1)}
              className={`px-1 rounded cursor-pointer ${playbackSpeed === 1 ? 'bg-app-accent text-white font-bold' : 'text-app-text-muted hover:text-app-text-main'}`}
              title="1x replay rate"
            >
              1x
            </button>
            <button
              onClick={() => setPlaybackSpeed(10)}
              className={`px-1 rounded cursor-pointer ${playbackSpeed === 10 ? 'bg-app-accent text-white font-bold' : 'text-app-text-muted hover:text-app-text-main'}`}
              title="10x replay fast-forward"
            >
              10x
            </button>
            <button
              onClick={() => setPlaybackSpeed(50)}
              className={`px-1 rounded cursor-pointer ${playbackSpeed === 50 ? 'bg-app-accent text-white font-bold' : 'text-app-text-muted hover:text-app-text-main'}`}
              title="50x operational replay blast"
            >
              50x
            </button>
          </div>

          {/* Connection Status Selector (Interactive Setup) */}
          <div className="relative group">
            <button className={`flex items-center space-x-1.5 px-2.5 py-1.5 rounded-lg border text-xs font-medium cursor-pointer ${
              connectionState === 'Connected' ? 'bg-app-success/10 text-app-success border-app-success/20' :
              connectionState === 'Reconnecting' ? 'bg-app-warning/10 text-app-warning border-app-warning/20' :
              connectionState === 'Disconnected' ? 'bg-app-error/10 text-app-error border-app-error/20' :
              connectionState === 'Fallback Active' ? 'bg-purple-500/10 text-purple-600 dark:text-purple-400 border-purple-500/20' :
              'bg-app-accent/10 text-app-accent border-app-accent/20'
            }`}>
              {connectionState === 'Connected' || connectionState === 'Fallback Active' ? <Wifi size={13} /> : <WifiOff size={13} />}
              <span className="capitalize text-[11px] font-mono">{connectionState}</span>
            </button>
            
            {/* Quick Change Dropdown */}
            <div className="absolute right-0 mt-1 w-44 rounded-lg shadow-lg border hidden group-hover:block z-50 text-xs bg-app-card border-app-border text-app-text-main">
              <div className="p-1.5 space-y-0.5">
                <div className="p-1 px-2 text-[9px] font-semibold text-app-text-muted uppercase">Simulate Socket State</div>
                {[
                  { name: 'Connected', desc: 'Realtime ws pipelines active' },
                  { name: 'Reconnecting', desc: 'Attempting retry handshakes' },
                  { name: 'Disconnected', desc: 'Simulated downtime mode' },
                  { name: 'Fallback Active', desc: 'Serving historical frames' },
                  { name: 'Stale Data', desc: 'Warning banner condition' }
                ].map((st) => (
                  <button
                    key={st.name}
                    onClick={() => setConnectionState(st.name as any)}
                    className="w-full text-left p-1.5 px-2 rounded hover:bg-app-bg transition-colors block"
                  >
                    <p className="font-semibold text-[11px]">{st.name}</p>
                    <p className="text-[9px] text-app-text-muted">{st.desc}</p>
                  </button>
                ))}
              </div>
            </div>
          </div>

          {/* Theme Toggle Button */}
          <button
            onClick={toggleTheme}
            className="p-2 rounded-lg border transition-colors cursor-pointer bg-app-bg border-app-border hover:bg-app-card text-app-text-muted hover:text-app-text-main"
            title={isDark ? 'Switch to Light Theme' : 'Switch to Dark Theme'}
          >
            {isDark ? <Sun size={14} /> : <Moon size={14} />}
          </button>

        </div>
      </div>

      {/* Visual State Diagnosis Injector Bar - Satisfies "At least one example of empty, loading, error, disconnected states" */}
      <div className="mt-3 pt-2.5 border-t flex flex-wrap items-center justify-between text-xs gap-3 border-app-border">
        <div className="flex items-center space-x-1.5 text-app-text-muted">
          <SlidersHorizontal size={12} className="text-app-accent" />
          <span className="text-[10px] font-semibold tracking-wider uppercase">Interactive Demo State Injector:</span>
        </div>
        
        <div className="flex items-center space-x-1">
          {[
            { id: 'normal', label: 'Normal Data', style: 'bg-app-success/10 text-app-success border-app-success/20' },
            { id: 'loading', label: 'Simulate Loading', style: 'bg-app-accent/10 text-app-accent border-app-accent/20' },
            { id: 'empty', label: 'Simulate Empty States', style: 'bg-app-text-muted/10 text-app-text-muted border-app-text-muted/20' },
            { id: 'error', label: 'Simulate Error Bounds', style: 'bg-app-error/10 text-app-error border-app-error/20' }
          ].map((st) => (
            <button
              key={st.id}
              onClick={() => setDemoState(st.id as DemoVisualState)}
              className={`px-2.5 py-1 text-[10px] rounded border font-mono font-medium transition-all cursor-pointer ${
                demoState === st.id 
                  ? 'ring-2 ring-app-accent font-bold scale-105'
                  : 'opacity-70 hover:opacity-100'
              } ${st.style}`}
            >
              {st.label}
            </button>
          ))}
        </div>
      </div>

      {/* Persistent Connection Event Banner for Disconnected or Stale states */}
      {connectionState !== 'Connected' && (
        <div className={`mt-3 p-2 rounded-lg text-xs flex items-center justify-between border ${
          connectionState === 'Disconnected' 
            ? 'bg-app-error/10 text-app-error border-app-error/30' 
            : connectionState === 'Reconnecting'
            ? 'bg-app-warning/10 text-app-warning border-app-warning/30'
            : connectionState === 'Stale Data'
            ? 'bg-app-accent/10 text-app-accent border-app-accent/30'
            : 'bg-purple-500/10 text-purple-600 dark:text-purple-400 border-purple-500/30'
        }`}>
          <div className="flex items-center space-x-2">
            <AlertTriangle size={14} className="flex-shrink-0" />
            <span>
              <strong>Simulated Banner Event:</strong> {
                connectionState === 'Disconnected' ? 'Disconnected from telemetry streaming. Feed is currently frozen.' :
                connectionState === 'Reconnecting' ? 'Re-establishing TLS socket with Binance and OKX archival mirrors...' :
                connectionState === 'Stale Data' ? 'Network latency currently exceeding SLA limits (~981ms stream buffer lag).' :
                'Historical playback frames active. Read-only control constraints applied.'
              }
            </span>
          </div>
          <button 
            onClick={() => setConnectionState('Connected')}
            className="text-[10px] font-semibold underline hover:no-underline px-2 py-0.5 rounded cursor-pointer"
          >
            Reconnect State Now
          </button>
        </div>
      )}
    </header>
  );
}
