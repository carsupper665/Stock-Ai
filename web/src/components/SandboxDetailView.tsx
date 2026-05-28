import {
  AlertTriangle,
  ArrowUpRight,
  BarChart3,
  Box,
  Clock,
  History,
  ListChecks,
  Pause,
  Play,
  ShieldCheck,
  StopCircle,
  Users
} from 'lucide-react';
import { ViewState, OperationalLog, Sandbox, SandboxAccount, Theme } from '../types';
import type { SandboxMonitorSnapshot } from '../api/monitor';

interface SandboxDetailViewProps {
  theme: Theme;
  viewState: ViewState;
  sandbox?: Sandbox;
  accounts: SandboxAccount[];
  logs: OperationalLog[];
  snapshotsBySandboxId: Record<string, SandboxMonitorSnapshot>;
  playbackSpeed: number;
  setPlaybackSpeed: (speed: number) => void;
  onOpenMonitor: () => void;
}

export default function SandboxDetailView({
  theme,
  viewState,
  sandbox,
  accounts,
  logs,
  snapshotsBySandboxId,
  playbackSpeed,
  onOpenMonitor
}: SandboxDetailViewProps) {
  const isDark = theme === 'dark';
  const relatedAccounts = sandbox ? accounts.filter((account) => account.sandboxId === sandbox.id) : [];
  const relatedLogs = sandbox ? logs.filter((log) => log.sandboxLabel && sandbox.name.includes(log.sandboxLabel)) : [];
  const snapshot = sandbox ? snapshotsBySandboxId[sandbox.id] : undefined;
  const indicatorEntries = snapshot ? Object.entries(snapshot.indicators) : [];

  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-7 w-72 bg-slate-300 dark:bg-slate-700 rounded"></div>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
          {[1, 2, 3, 4].map((item) => <div key={item} className="h-24 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>)}
        </div>
        <div className="h-64 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (viewState === 'error') {
    return (
      <div className="p-6 text-center max-w-lg mx-auto mt-12 rounded-xl border border-red-500/20 bg-red-500/5">
        <AlertTriangle size={34} className="mx-auto text-red-500 mb-3" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-900'}`}>Sandbox Snapshot Unavailable</h3>
        <p className="text-xs text-slate-500 mt-2 mb-4">
          The backend snapshot renderer could not load this sandbox detail state. Retry returns the viewer to the monitor deck.
        </p>
        <button onClick={onOpenMonitor} className="px-3 py-1.5 bg-blue-600 text-white rounded-lg text-xs font-semibold hover:bg-blue-500">
          Open Monitor Deck
        </button>
      </div>
    );
  }

  if (!sandbox) {
    return (
      <div className="p-6 text-center max-w-md mx-auto mt-12">
        <Box size={34} className="mx-auto text-slate-400 mb-3" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-900'}`}>No sandbox selected</h3>
        <p className="text-xs text-slate-500 mt-2">
          Open a sandbox from the control table to inspect replay controls, accounts, events, and indicator panels.
        </p>
      </div>
    );
  }

  const statusClass = sandbox.status === 'Running'
    ? 'bg-app-success/15 text-app-success border-app-success/20'
    : sandbox.status === 'Paused'
    ? 'bg-app-warning/15 text-app-warning border-app-warning/20'
    : sandbox.status === 'Failed'
    ? 'bg-app-error/15 text-app-error border-app-error/20'
    : 'bg-app-text-muted/10 text-app-text-muted border-app-border';

  return (
    <div className="p-6 space-y-6 animate-fadeIn">
      <section className="rounded-2xl border bg-app-card border-app-border text-app-text-main p-5 space-y-4">
        <div className="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-4">
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <span className={`px-2.5 py-1 rounded-full text-[10px] font-bold border ${statusClass}`}>{sandbox.status}</span>
              <span className="px-2.5 py-1 rounded-full text-[10px] font-bold border border-app-accent/20 bg-app-accent/10 text-app-accent">{sandbox.mode}</span>
              <span className="text-[10px] font-mono text-app-text-muted">Updated {sandbox.updated}</span>
            </div>
            <h2 className="text-xl font-bold tracking-tight">{sandbox.name}</h2>
            <p className="text-xs text-app-text-muted max-w-2xl">
              Detailed control and observation surface for one isolated sandbox. Values are loaded from backend admin APIs and monitor snapshots.
            </p>
          </div>
          <button onClick={onOpenMonitor} className="px-3 py-1.5 rounded-lg bg-app-accent text-white text-xs font-semibold hover:bg-app-accent/90 inline-flex items-center gap-1.5">
            <ArrowUpRight size={13} />
            Open Monitor
          </button>
        </div>
        <div className="flex flex-wrap gap-2 text-[10px] uppercase font-bold tracking-wider text-app-text-muted">
          {['Overview', 'Replay', 'Accounts & Tokens', 'Positions / Orders / Trades', 'Indicators', 'Events'].map((label) => (
            <span key={label} className="px-2.5 py-1 rounded-full border bg-app-bg border-app-border">{label}</span>
          ))}
        </div>
      </section>

      <section className="grid grid-cols-1 md:grid-cols-4 gap-4">
        <div className="p-4 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Dataset</p>
          <p className="text-sm font-bold mt-1 truncate">{sandbox.datasetName || 'None assigned'}</p>
          <span className="text-[10px] text-app-text-muted">Replay source</span>
        </div>
        <div className="p-4 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Replay Time</p>
          <p className="text-sm font-bold font-mono text-app-accent mt-1">{sandbox.replayTime}</p>
          <span className="text-[10px] text-app-text-muted">Current pointer</span>
        </div>
        <div className="p-4 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Accounts</p>
          <p className="text-2xl font-bold mt-1">{relatedAccounts.length}</p>
          <span className="text-[10px] text-app-text-muted">Attached tokens</span>
        </div>
        <div className="p-4 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Freshness</p>
          <p className="text-sm font-bold text-app-warning mt-1">{sandbox.freshness}</p>
          <span className="text-[10px] text-app-text-muted">Stale states stay visible</span>
        </div>
      </section>

      <section className="grid grid-cols-1 xl:grid-cols-3 gap-6">
        <div className="xl:col-span-2 space-y-6">
          <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main space-y-4">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Clock size={15} className="text-app-accent" />
                <h3 className="text-xs font-bold uppercase tracking-wider">Replay Control Panel</h3>
              </div>
              <span className="text-[10px] font-mono text-app-text-muted">Speed {snapshot?.replaySpeed ?? playbackSpeed}x</span>
            </div>
            <div className="rounded-lg border border-app-border bg-app-bg p-3 text-xs text-app-text-muted">
              Backend status: <span className="font-mono text-app-accent">{snapshot?.status ?? sandbox.status}</span> at{' '}
              <span className="font-mono text-app-accent">{snapshot?.replayCurrentTime ?? sandbox.replayTime}</span>.
            </div>
            <div className="flex flex-wrap gap-2 text-[10px] text-app-text-muted">
              <span className="inline-flex items-center gap-1 rounded-lg border border-app-border bg-app-bg px-2 py-1"><Play size={12} /> Start</span>
              <span className="inline-flex items-center gap-1 rounded-lg border border-app-border bg-app-bg px-2 py-1"><Pause size={12} /> Pause</span>
              <span className="inline-flex items-center gap-1 rounded-lg border border-app-border bg-app-bg px-2 py-1"><StopCircle size={12} /> Stop</span>
              <span>Use the Sandboxes table for lifecycle API actions.</span>
            </div>
          </div>

          <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main space-y-4">
            <div className="flex items-center gap-2">
              <ListChecks size={15} className="text-app-accent" />
              <h3 className="text-xs font-bold uppercase tracking-wider">Positions / Orders / Trades</h3>
            </div>
            <div className="grid grid-cols-1 lg:grid-cols-3 gap-3 text-xs">
              {snapshotRecordPanels(snapshot).map(({ title, count }) => (
                <div key={title} className="rounded-lg border border-app-border overflow-hidden">
                  <div className="px-3 py-2 bg-app-bg text-[10px] uppercase font-bold text-app-text-muted">{title}</div>
                  <div className="px-3 py-4 text-center text-[11px] text-app-text-muted">
                    {count === 0 ? 'No backend records returned for this snapshot.' : `${count} backend records returned.`}
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main space-y-4">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <BarChart3 size={15} className="text-app-accent" />
                <h3 className="text-xs font-bold uppercase tracking-wider">Indicator Panel</h3>
              </div>
              <span className="text-[10px] text-app-warning font-semibold">Informational only. No signals or advice.</span>
            </div>
            {indicatorEntries.length === 0 ? (
              <p className="text-xs text-app-text-muted">
                No indicator snapshot available from backend. Indicators will be populated once the sandbox has active replay data.
              </p>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                {indicatorEntries.slice(0, 6).map(([name, points]) => {
                  const latestPoint = points[points.length - 1];
                  return (
                    <div key={name} className="rounded-lg border border-app-border bg-app-bg p-3 text-xs">
                      <p className="text-[10px] uppercase font-bold text-app-text-muted">{name.replace(/_/g, ' ')}</p>
                      <p className="mt-1 font-mono text-app-accent">{latestPoint ? latestPoint.value.toFixed(4) : 'No points'}</p>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>

        <aside className="space-y-6">
          <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main space-y-3">
            <div className="flex items-center gap-2">
              <Users size={15} className="text-app-accent" />
              <h3 className="text-xs font-bold uppercase tracking-wider">Realtime Accounts</h3>
            </div>
            {relatedAccounts.length === 0 ? (
              <p className="text-xs text-app-text-muted">No virtual accounts are attached to this sandbox yet.</p>
            ) : relatedAccounts.map((account) => (
              <div key={account.id} className="p-3 rounded-lg border bg-app-bg border-app-border text-xs">
                <div className="flex items-center justify-between">
                  <span className="font-bold">{account.name}</span>
                  <span className="text-[10px] text-app-success">{account.status}</span>
                </div>
                <div className="mt-2 grid grid-cols-2 gap-2 font-mono text-[10px] text-app-text-muted">
                  <span>Equity ${account.equity.toLocaleString()}</span>
                  <span className="text-right">Last {account.updated}</span>
                </div>
              </div>
            ))}
          </div>

          <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main space-y-3">
            <div className="flex items-center gap-2">
              <History size={15} className="text-app-accent" />
              <h3 className="text-xs font-bold uppercase tracking-wider">Event Feed</h3>
            </div>
            {relatedLogs.length === 0 ? (
              <p className="text-xs text-app-text-muted">No events have been recorded for this sandbox.</p>
            ) : relatedLogs.map((log) => (
              <div key={log.id} className="relative pl-4 border-l border-app-border text-xs space-y-1">
                <span className="absolute -left-1 top-1.5 h-2 w-2 rounded-full bg-app-accent"></span>
                <div className="flex items-center justify-between gap-2">
                  <span className="font-bold text-app-text-main">{log.title}</span>
                  <span className="text-[9px] font-mono text-app-text-muted">{log.timestamp.slice(-8)}</span>
                </div>
                <p className="text-[10px] text-app-text-muted">{log.details}</p>
              </div>
            ))}
          </div>

          <div className="p-4 rounded-xl border border-app-accent/20 bg-app-accent/5 text-xs text-app-text-main">
            <div className="flex items-start gap-2">
              <ShieldCheck size={15} className="text-app-accent flex-shrink-0 mt-0.5" />
              <p>Snapshot values come from backend monitor APIs. Stale data is labeled instead of being treated as current.</p>
            </div>
          </div>
        </aside>
      </section>
    </div>
  );
}

function snapshotRecordPanels(snapshot: SandboxMonitorSnapshot | undefined): { title: string; count: number }[] {
  return [
    { title: 'Positions', count: snapshot?.positions.length ?? 0 },
    { title: 'Orders', count: snapshot?.orders.length ?? 0 },
    { title: 'Trades', count: snapshot?.trades.length ?? 0 },
  ];
}
