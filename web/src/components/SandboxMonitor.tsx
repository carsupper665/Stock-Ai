import { useState, useEffect } from 'react';
import {
  ResponsiveContainer,
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip
} from 'recharts';
import {
  LineChart as ChartIcon,
  Play,
  Pause,
  ArrowRight,
  TrendingUp,
  Sliders,
  DollarSign,
  Info,
  Layers,
  CheckCircle,
  Clock,
  ChevronRight,
  History,
  Activity
} from 'lucide-react';
import { Sandbox, SandboxAccount, OperationalLog, Theme, ViewState } from '../types';
import type { SandboxMonitorSnapshot } from '../api/monitor';

interface SandboxMonitorProps {
  theme: Theme;
  viewState: ViewState;
  sandboxes: Sandbox[];
  accounts: SandboxAccount[];
  logs: OperationalLog[];
  snapshotsBySandboxId: Record<string, SandboxMonitorSnapshot>;
  playbackSpeed: number;
  setPlaybackSpeed: (speed: number) => void;
}

export default function SandboxMonitor({
  theme,
  viewState,
  sandboxes,
  accounts,
  logs,
  snapshotsBySandboxId,
  playbackSpeed,
  setPlaybackSpeed
}: SandboxMonitorProps) {
  const isDark = theme === 'dark';

  // Selected Sandbox state
  const [selectedSbId, setSelectedSbId] = useState<string>(sandboxes[0]?.id || '');
  const [timelineScrubPercent, setTimelineScrubPercent] = useState<number>(45);

  const activeSandbox = sandboxes.find((s) => s.id === selectedSbId) || sandboxes[0];
  const activeSnapshot = activeSandbox ? snapshotsBySandboxId[activeSandbox.id] : undefined;

  useEffect(() => {
    if (selectedSbId === '' && sandboxes[0]?.id) {
      setSelectedSbId(sandboxes[0].id);
    }
  }, [selectedSbId, sandboxes]);

  // Increment scrub line gradually in real-time when playback is playing!
  useEffect(() => {
    if (playbackSpeed === 0) return;
    const interval = setInterval(() => {
      setTimelineScrubPercent((prev) => (prev >= 100 ? 5 : prev + 1));
    }, 1500);
    return () => clearInterval(interval);
  }, [playbackSpeed]);

  // Derive sandbox-specific accounts and logs
  const relatedAccounts = accounts.filter((a) => a.sandboxId === selectedSbId);
  const relatedLogs = logs.filter((l) => l.sandboxLabel && activeSandbox?.name.includes(l.sandboxLabel));

  // Visual Theme configurations for Recharts
  const gridColor = isDark ? '#334155' : '#e2e8f0';
  const strokeColor = isDark ? '#60a5fa' : '#2563eb';
  const axisColor = isDark ? '#94a3b8' : '#64748b';

  const trendData = activeSandbox?.equityTrend;
  const indicatorEntries = activeSnapshot ? Object.entries(activeSnapshot.indicators) : [];

  if (viewState === 'error') {
    return (
      <div className="p-6 text-center max-w-md mx-auto mt-12 rounded-xl border border-red-500/20 bg-red-500/5">
        <Info size={32} className="mx-auto text-red-500 mb-3" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Monitor Snapshot Error</h3>
        <p className="text-xs text-slate-500 mt-1">Monitor could not load the selected sandbox snapshot.</p>
      </div>
    );
  }

  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-300 dark:bg-slate-700 rounded mb-4"></div>
        <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
        <div className="h-28 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Control Toolbar Panel */}
      <div className={`p-4 rounded-xl border flex flex-col md:flex-row md:items-center justify-between gap-4 ${
        isDark ? 'bg-[#1b1e25] border-slate-800' : 'bg-white border-slate-100'
      }`}>
        <div className="flex items-center space-x-3">
          <label className="text-[10px] font-bold uppercase tracking-wider text-slate-400">Selected Sandbox</label>
          <select
            value={selectedSbId}
            onChange={(e) => setSelectedSbId(e.target.value)}
            className={`p-2 border rounded-lg text-xs font-semibold focus:outline-none ${
              isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'
            }`}
          >
            {sandboxes.map((s) => (
              <option key={s.id} value={s.id}>{s.name} ({s.status})</option>
            ))}
          </select>
        </div>

        {/* Replay Control Scrubber timeline bar */}
        <div className="flex-grow flex items-center space-x-4 max-w-lg">
          <span className="text-[10px] font-mono font-semibold text-gray-400">START</span>
          <div className="flex-1 relative py-1">
            <input
              type="range"
              min="0"
              max="100"
              value={timelineScrubPercent}
              onChange={(e) => setTimelineScrubPercent(Number(e.target.value))}
              className="w-full h-1.5 bg-slate-200 dark:bg-slate-700 hover:bg-slate-300 rounded-lg appearance-none cursor-pointer accent-blue-600"
            />
            {/* Position pointer marker */}
            <span
              style={{ left: `${timelineScrubPercent}%` }}
              className="absolute top-[-4px] transform -translate-x-1/2 w-3 h-3 bg-blue-500 rounded-full border-2 border-white dark:border-slate-900 shadow-sm"
            ></span>
          </div>
          <span className="text-[10px] font-mono font-semibold text-sky-500">{timelineScrubPercent}% Scrubbed</span>
        </div>

        {/* Live Pause Controller triggers */}
        <div className="flex items-center space-x-2">
          <button
            onClick={() => setPlaybackSpeed(playbackSpeed === 0 ? 10 : 0)}
            className={`p-2 rounded-lg border flex items-center space-x-1.5 text-xs font-semibold cursor-pointer ${
              playbackSpeed === 0
                ? 'bg-blue-600/10 text-blue-500 border-blue-500/20 hover:bg-blue-600/20'
                : 'bg-emerald-600/10 text-emerald-500 border-emerald-500/20 hover:bg-emerald-600/20'
            }`}
          >
            {playbackSpeed === 0 ? (
              <>
                <Play fill="currentColor" size={12} />
                <span>Play Simulation</span>
              </>
            ) : (
              <>
                <Pause fill="currentColor" size={12} />
                <span>Pause</span>
              </>
            )}
          </button>
        </div>
      </div>

      {/* Main split grid layout */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">

        {/* Left main charts column */}
        <div className="lg:col-span-2 space-y-6">
          
          {/* Performance chart block */}
          <div className={`p-5 rounded-xl border space-y-4 ${
            isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
          }`}>
            <div className="flex items-center justify-between">
              <div className="flex items-center space-x-1.5">
                <ChartIcon size={14} className="text-blue-500" />
                <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
                  Dynamic Simulator Equity Curve
                </h4>
              </div>
              <span className="text-[10px] font-mono text-gray-400">Reference: {activeSandbox?.replayTime}</span>
            </div>

            {/* Recharts chart or empty state */}
            <div className="h-56 w-full text-xs font-mono">
              {trendData && trendData.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={trendData}>
                    <CartesianGrid strokeDasharray="3 3" stroke={gridColor} />
                    <XAxis dataKey="time" stroke={axisColor} />
                    <YAxis stroke={axisColor} domain={['auto', 'auto']} />
                    <Tooltip
                      contentStyle={{
                        backgroundColor: isDark ? '#1e293b' : '#ffffff',
                        borderColor: isDark ? '#475569' : '#cbd5e1',
                        borderRadius: '8px',
                        color: isDark ? '#f1f5f9' : '#0f172a'
                      }}
                    />
                    <Line
                      type="monotone"
                      dataKey="value"
                      stroke={strokeColor}
                      strokeWidth={2.5}
                      dot={{ r: 3 }}
                      activeDot={{ r: 5 }}
                    />
                  </LineChart>
                </ResponsiveContainer>
              ) : (
                <div className="flex h-full items-center justify-center rounded-lg border border-app-border bg-app-bg text-xs text-app-text-muted">
                  No equity trend data from backend. Start the sandbox to record equity history.
                </div>
              )}
            </div>
          </div>

          {/* Connected Accounts mini grid */}
          <div className="space-y-3">
            <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Virtual Account Performance Inside Sandbox
            </h4>
            
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              {relatedAccounts.length === 0 ? (
                <div className={`p-4 text-center rounded-xl border text-xs text-gray-400 ${
                  isDark ? 'bg-slate-900/30 border-slate-800' : 'bg-slate-50 border-slate-100'
                }`}>
                  No sandbox accounts attached to this sandbox.
                </div>
              ) : (
                relatedAccounts.map((acc) => {
                  const pnl = acc.equity - acc.balance;
                  const isGain = pnl >= 0;

                  return (
                    <div
                      key={acc.id}
                      className={`p-4 rounded-xl border flex justify-between items-center ${
                        isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
                      }`}
                    >
                      <div className="space-y-1">
                        <p className="font-semibold text-xs text-slate-900 dark:text-white">{acc.name}</p>
                        <p className="text-[10px] text-gray-400 font-mono">Margin util: ${acc.margin}</p>
                      </div>
                      <div className="text-right font-mono">
                        <p className="font-bold text-xs">${acc.equity.toLocaleString()}</p>
                        <p className={`text-[10px] font-semibold ${isGain ? 'text-emerald-500' : 'text-red-500'}`}>
                          {isGain ? '+' : ''}${pnl.toFixed(2)}
                        </p>
                      </div>
                    </div>
                  );
                })
              )}
            </div>
          </div>

          {/* Indicator summary block - Informational disclaimer required */}
          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
                Archival Technical Indicators
              </h4>
              <span className="text-[10px] text-amber-500 font-semibold uppercase flex items-center space-x-1">
                <Info size={11} />
                <span>Reference Parameters Only</span>
              </span>
            </div>

            <div className="p-3 bg-amber-500/5 border border-amber-500/10 text-amber-600 dark:text-amber-500 rounded-lg text-[10px] leading-relaxed mb-3">
              Indicators are rendered only when the backend monitor snapshot includes calculated replay indicators. They do not constitute trading recommendations, signals, or strategy builders.
            </div>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              {indicatorEntries.length === 0 ? (
                <div className={`md:col-span-3 p-4 rounded-lg border text-xs text-center ${
                  isDark ? 'bg-slate-900/40 border-slate-800 text-slate-400' : 'bg-slate-50 border-slate-200 text-slate-500'
                }`}>
                  No indicator values returned by backend for this sandbox snapshot.
                </div>
              ) : indicatorEntries.slice(0, 6).map(([name, points]) => {
                const latestPoint = points[points.length - 1];
                return (
                  <div key={name} className={`p-3 rounded-lg border ${
                    isDark ? 'bg-slate-900/40 border-slate-800 text-white' : 'bg-slate-50 border-slate-200 text-slate-800'
                  }`}>
                    <p className="text-[9px] uppercase font-bold text-slate-400">{formatIndicatorName(name)}</p>
                    <p className="text-sm font-bold font-mono mt-1 text-sky-500">
                      {latestPoint ? latestPoint.value.toFixed(4) : 'No points'}
                    </p>
                    <span className="text-[9px] text-gray-400">{latestPoint?.ts ?? 'No timestamp'}</span>
                  </div>
                );
              })}
            </div>
          </div>

        </div>

        {/* Right timeline feed column */}
        <div className="space-y-4">
          <div className="flex items-center space-x-1.5">
            <History size={14} className="text-blue-500" />
            <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Sandbox Activity Stream
            </h4>
          </div>

          <div className={`border rounded-xl p-4 space-y-4 max-h-[500px] overflow-y-auto ${
            isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
          }`}>
            {relatedLogs.length === 0 ? (
              <div className="text-center p-6 text-xs text-gray-500 font-mono">
                No active events logged for simulator: {activeSandbox?.name}
              </div>
            ) : (
              relatedLogs.map((log) => {
                const isCrit = log.severity === 'Critical';
                const isWarning = log.severity === 'Warning';
                const isSuccess = log.severity === 'Success';

                return (
                  <div key={log.id} className="relative pl-5 border-l border-slate-200 dark:border-slate-800 pb-3 last:pb-0 text-xs text-gray-600 dark:text-gray-300">
                    {/* Circle Node indicator */}
                    <span className={`absolute left-[-4.5px] top-1 w-2.5 h-2.5 rounded-full border-2 ${
                      isCrit
                        ? 'bg-red-500 border-red-500'
                        : isWarning
                        ? 'bg-amber-400 border-amber-400'
                        : isSuccess
                        ? 'bg-emerald-500 border-emerald-500'
                        : 'bg-blue-500 border-blue-500'
                    }`}></span>

                    <div className="space-y-1">
                      <div className="flex items-center justify-between font-semibold">
                        <span className={`text-[11px] ${
                          isCrit ? 'text-red-500' : isWarning ? 'text-amber-500' : 'text-slate-800 dark:text-white'
                        }`}>
                          {log.title}
                        </span>
                        <span className="text-[9px] text-gray-400 font-mono">{log.timestamp.slice(-8)}</span>
                      </div>
                      <p className="text-[10px] text-gray-400 leading-relaxed font-sans">
                        {log.details}
                      </p>
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </div>

      </div>

    </div>
  );
}

function formatIndicatorName(name: string): string {
  return name.replace(/_/g, ' ').toUpperCase();
}
