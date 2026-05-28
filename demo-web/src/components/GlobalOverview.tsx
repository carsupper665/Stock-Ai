import {
  Layers,
  Box,
  Users,
  AlertOctagon,
  History,
  Activity,
  ArrowUpRight,
  TrendingUp,
  AlertTriangle,
  PlayCircle
} from 'lucide-react';
import { Sandbox, SandboxAccount, OperationalLog, SystemAlert, Theme, DemoVisualState } from '../types';

interface GlobalOverviewProps {
  theme: Theme;
  demoState: DemoVisualState;
  sandboxes: Sandbox[];
  accounts: SandboxAccount[];
  logs: OperationalLog[];
  alerts: SystemAlert[];
  onNavigate: (page: any) => void;
}

export default function GlobalOverview({
  theme,
  demoState,
  sandboxes,
  accounts,
  logs,
  alerts,
  onNavigate
}: GlobalOverviewProps) {
  const isDark = theme === 'dark';

  // Calculations
  const activeSbs = sandboxes.filter((s) => s.status === 'Running').length;
  const connectedKeys = accounts.filter((a) => a.token && !a.tokenRevoked).length;
  const criticalAlerts = alerts.filter((a) => a.severity === 'Critical' || a.severity === 'Warning').length;

  if (demoState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-200 dark:bg-slate-700 rounded"></div>
        <div className="grid grid-cols-4 gap-4">
          {[1, 2, 3, 4].map((i) => <div key={i} className="h-20 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>)}
        </div>
        <div className="grid grid-cols-3 gap-4">
          <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl col-span-2"></div>
          <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
        </div>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 animate-fadeIn">

      {/* Hero Summary Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        
        {/* Dynamic Sandbox Counters */}
        <div className="p-4 rounded-xl border flex items-center justify-between bg-app-card border-app-border text-app-text-main hover:bg-app-bg/50 transition-all">
          <div className="space-y-1">
            <span className="text-[10px] font-bold text-app-text-muted uppercase tracking-wider">Active Simulators</span>
            <p className="text-xl font-bold">{activeSbs} / {sandboxes.length}</p>
            <p className="text-[10px] text-app-text-muted">Isolated replay nodes</p>
          </div>
          <div className="p-2.5 rounded-lg bg-app-accent/10 text-app-accent">
            <Box size={18} />
          </div>
        </div>

        {/* Account Keys */}
        <div className="p-4 rounded-xl border flex items-center justify-between bg-app-card border-app-border text-app-text-main hover:bg-app-bg/50 transition-all">
          <div className="space-y-1">
            <span className="text-[10px] font-bold text-app-text-muted uppercase tracking-wider">Connected Accounts</span>
            <p className="text-xl font-bold">{connectedKeys} Nodes</p>
            <p className="text-[10px] text-app-text-muted">Sim brokerage balances</p>
          </div>
          <div className="p-2.5 rounded-lg bg-app-success/10 text-app-success">
            <Users size={18} />
          </div>
        </div>

        {/* Active Open Orders placeholder */}
        <div className="p-4 rounded-xl border flex items-center justify-between bg-app-card border-app-border text-app-text-main hover:bg-app-bg/50 transition-all">
          <div className="space-y-1">
            <span className="text-[10px] font-bold text-app-text-muted uppercase tracking-wider">Open Orders (Sim)</span>
            <p className="text-xl font-bold">2 Orders</p>
            <p className="text-[10px] text-app-text-muted">Limit and stop triggers</p>
          </div>
          <div className="p-2.5 rounded-lg bg-app-accent/10 text-app-accent">
            <Activity size={18} />
          </div>
        </div>

        {/* Warning System */}
        <div className="p-4 rounded-xl border flex items-center justify-between bg-app-card border-app-border text-app-text-main hover:bg-app-bg/50 transition-all">
          <div className="space-y-1">
            <span className="text-[10px] font-bold text-app-text-muted uppercase tracking-wider">System Exceptions</span>
            <p className={`text-xl font-bold ${criticalAlerts > 0 ? 'text-app-warning animate-pulse' : 'text-app-success'}`}>
              {criticalAlerts} Active
            </p>
            <p className="text-[10px] text-app-text-muted">Latency & boundary locks</p>
          </div>
          <div className={`p-2.5 rounded-lg ${criticalAlerts > 0 ? 'bg-app-warning/10 text-app-warning' : 'bg-app-success/10 text-app-success'}`}>
            <AlertOctagon size={18} />
          </div>
        </div>

      </div>

      {/* Dual Pane Layout split */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">

        {/* Left Side: Active sandboxes monitoring & controls summary */}
        <div className="lg:col-span-2 space-y-4">
          <div className="flex justify-between items-center">
            <div className="flex items-center space-x-1.5">
              <Layers size={14} className="text-app-accent" />
              <h3 className="text-xs font-bold uppercase tracking-wider text-app-text-main">
                Active Simulators Ingestion Overview
              </h3>
            </div>
            <button
              onClick={() => onNavigate('sandboxes')}
              className="text-xs text-app-accent hover:underline flex items-center space-x-0.5 cursor-pointer"
            >
              <span>Manage All</span>
              <ArrowUpRight size={13} />
            </button>
          </div>

          <div className="border rounded-xl p-4 space-y-3 bg-app-card border-app-border text-app-text-main">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              {sandboxes.slice(0, 4).map((sb) => {
                const isRunning = sb.status === 'Running';
                const isPaused = sb.status === 'Paused';

                return (
                  <div
                    key={sb.id}
                    className="p-3 rounded-lg border transition-all bg-app-bg hover:bg-app-card border-app-border text-app-text-main"
                  >
                    <div className="flex items-center justify-between mb-2">
                       <div className="flex items-center space-x-2">
                        <PlayCircle size={14} className={isRunning ? 'text-app-success' : 'text-app-text-muted'} />
                        <span className="font-semibold text-xs text-app-text-main">
                          {sb.name}
                        </span>
                      </div>
                      <span className={`px-1.5 py-0.5 rounded-full text-[9px] font-bold ${
                        isRunning
                          ? 'bg-app-success/20 text-app-success'
                          : isPaused
                          ? 'bg-app-warning/20 text-app-warning'
                          : 'bg-app-bg border border-app-border text-app-text-muted'
                      }`}>
                        {sb.status}
                      </span>
                    </div>

                    <div className="grid grid-cols-2 gap-2 text-[10px] font-mono border-t pt-2 border-app-border">
                      <div>
                        <p className="text-app-text-muted">Assigned Ingest:</p>
                        <p className="font-semibold text-app-accent truncate" title={sb.datasetName}>{sb.datasetName || 'None'}</p>
                      </div>
                      <div className="text-right">
                        <p className="text-app-text-muted">Replay Time:</p>
                        <p className="font-semibold text-app-text-main">{sb.replayTime}</p>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
            
            {/* Warning segment inside the card */}
            <div className="pt-2 flex items-center space-x-2 text-[11px] text-app-text-muted border-t border-app-border">
              <TrendingUp size={13} className="text-app-accent" />
              <span>Mean simulation playback rate is currently scaled at <strong>50x milliseconds/update</strong>.</span>
            </div>
          </div>
        </div>

        {/* Right Side Alert and Telemetry preview */}
        <div className="space-y-4">
          <div className="flex justify-between items-center">
            <div className="flex items-center space-x-1.5">
              <History size={14} className="text-app-accent" />
              <h3 className="text-xs font-bold uppercase tracking-wider text-app-text-main">
                Security Exception Feeds
              </h3>
            </div>
            <button
              onClick={() => onNavigate('alerts')}
              className="text-xs text-app-accent hover:underline flex items-center space-x-0.5 cursor-pointer"
            >
              <span>Explore Logs</span>
              <ArrowUpRight size={13} />
            </button>
          </div>

          <div className="border rounded-xl p-4 space-y-3 max-h-[295px] overflow-y-auto bg-app-card border-app-border text-app-text-main">
            {alerts.slice(0, 3).map((al) => {
              const isCrit = al.severity === 'Critical';
              const isWarning = al.severity === 'Warning';

              return (
                <div
                  key={al.id}
                  className={`p-2.5 rounded-lg border text-xs space-y-1 ${
                    isCrit
                      ? 'bg-app-error/5 border-app-error/10 text-app-error'
                      : isWarning
                      ? 'bg-app-warning/5 border-app-warning/10 text-app-warning'
                      : 'bg-app-text-muted/5 border-app-text-muted/10 text-app-text-muted'
                  }`}
                >
                  <div className="flex items-center justify-between font-semibold">
                    <span className="text-[11px] truncate flex items-center space-x-1">
                      <AlertTriangle size={11} className="flex-shrink-0" />
                      <span>{al.title}</span>
                    </span>
                    <span className="text-[9px] text-app-text-muted font-mono font-normal">
                      {al.timestamp.slice(-8)}
                    </span>
                  </div>
                  <p className="text-[10px] text-app-text-muted leading-snug font-sans">
                    {al.explanation}
                  </p>
                </div>
              );
            })}
          </div>
        </div>

      </div>

    </div>
  );
}
