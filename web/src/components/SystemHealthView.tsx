import {
  HeartPulse,
  Signal,
  Clock,
  Gauge,
  Activity,
  AlertTriangle,
  Zap,
  CheckCircle,
  TrendingUp,
  XCircle
} from 'lucide-react';
import { SystemSymbol, Theme, ViewState } from '../types';

interface SystemHealthViewProps {
  theme: Theme;
  viewState: ViewState;
  symbols: SystemSymbol[];
}

export default function SystemHealthView({
  theme,
  viewState,
  symbols
}: SystemHealthViewProps) {
  const isDark = theme === 'dark';
  const targetSymbols = symbols;

  const healthyCount = targetSymbols.filter((s) => s.status === 'Healthy').length;
  const staleCount = targetSymbols.filter((s) => s.status === 'Stale' || s.status === 'Critical').length;
  const meanLatency = targetSymbols.length === 0
    ? 0
    : Math.round(targetSymbols.reduce((sum, symbol) => sum + symbol.latencyMs, 0) / targetSymbols.length);

  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-300 dark:bg-slate-700 rounded mb-4"></div>
        <div className="h-24 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
        <div className="h-32 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Overview Metric Toggles */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
        
        <div className={`p-4 rounded-xl border ${isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'}`}>
          <div className="flex items-center justify-between mb-1.5">
            <span className="text-[10px] uppercase font-bold tracking-wider text-slate-400">Backend Feed Records</span>
            <CheckCircle size={14} className="text-emerald-500" />
          </div>
          <p className="text-xl font-bold font-mono">{targetSymbols.length} Symbols</p>
          <span className="text-[9px] text-gray-400">Returned by /admin/monitor/live-symbols</span>
        </div>

        <div className={`p-4 rounded-xl border ${isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'}`}>
          <div className="flex items-center justify-between mb-1.5">
            <span className="text-[10px] uppercase font-bold tracking-wider text-slate-400">Total Symbol Feeds</span>
            <Activity size={14} className="text-blue-500" />
          </div>
          <p className="text-xl font-bold font-mono">{targetSymbols.length} Streams</p>
          <span className="text-[9px] text-gray-400">Provider snapshots currently tracked</span>
        </div>

        <div className={`p-4 rounded-xl border ${isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'}`}>
          <div className="flex items-center justify-between mb-1.5">
            <span className="text-[10px] uppercase font-bold tracking-wider text-slate-400">Active Pipelines</span>
            <Zap size={14} className="text-amber-500" />
          </div>
          <p className="font-mono text-xl font-bold text-sky-500">{healthyCount} Online</p>
          <span className="text-[9px] text-gray-400">{staleCount} currently degraded/paused</span>
        </div>

        <div className={`p-4 rounded-xl border ${isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'}`}>
          <div className="flex items-center justify-between mb-1.5">
            <span className="text-[10px] uppercase font-bold tracking-wider text-slate-400">Mean Freshness</span>
            <Signal size={14} className="text-purple-500" />
          </div>
          <p className="text-xl font-bold font-mono">{meanLatency}ms</p>
          <span className="text-[9px] text-gray-400">Computed from backend freshness_ms</span>
        </div>

      </div>

      {/* Dense Symbols Latency Table */}
      <div className="space-y-4">
        
        <div className="flex items-center justify-between">
          <div className="flex items-center space-x-1.5">
            <HeartPulse size={15} className="text-rose-500 animate-pulse" />
            <h3 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Telemetry Pipelines (Symbols Sync Registry)
            </h3>
          </div>
          
          <span className="text-[11px] font-semibold text-app-text-muted">
            Loaded from backend live-symbol monitor endpoint
          </span>
        </div>

        <div className={`border rounded-xl spill-x-auto overflow-hidden shadow-sm ${
          isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
        }`}>
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse text-xs">
              <thead>
                <tr className={`border-b text-[10px] uppercase font-bold tracking-wider ${
                  isDark ? 'bg-slate-800/40 border-slate-800 text-gray-200' : 'bg-slate-50/50 border-slate-100 text-slate-500'
                }`}>
                  <th className="p-3">Asset Symbol</th>
                  <th className="p-3">Ingestion Source & Stream Pipe</th>
                  <th className="p-3 text-right">Raw Buffer Latency</th>
                  <th className="p-3 text-right">Tick Rate (ticks/sec)</th>
                  <th className="p-3">Integrity State</th>
                  <th className="p-3 text-right">Last Heartbeat Frame</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/50 font-mono">
                {targetSymbols.map((sym) => (
                  <tr key={sym.symbol} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30 transition-colors">
                    <td className="p-3 font-semibold text-slate-900 dark:text-white flex items-center space-x-2">
                      <TrendingUp size={12} className="text-gray-400" />
                      <span>{sym.symbol}</span>
                    </td>
                    <td className="p-3 text-slate-600 dark:text-gray-300 font-sans">{sym.feedSource}</td>
                    <td className="p-3 text-right font-medium">
                      <span className={`${
                        sym.latencyMs > 500 ? 'text-red-500' : sym.latencyMs > 100 ? 'text-amber-500' : 'text-emerald-500'
                      }`}>
                        {sym.latencyMs}ms
                      </span>
                    </td>
                    <td className="p-3 text-right">{sym.rate} Hz</td>
                    <td className="p-3">
                      <div className="flex items-center space-x-1.5 font-sans">
                        <span className={`w-2 h-2 rounded-full ${
                          sym.status === 'Healthy'
                            ? 'bg-emerald-500'
                            : sym.status === 'Stale'
                            ? 'bg-amber-400'
                            : 'bg-red-500 animate-pulse'
                        }`}></span>
                        <span className={`text-[10px] font-bold ${
                          sym.status === 'Healthy'
                            ? 'text-emerald-500'
                            : sym.status === 'Stale'
                            ? 'text-amber-500'
                            : 'text-red-500'
                        }`}>
                          {sym.status}
                        </span>
                      </div>
                    </td>
                    <td className="p-3 text-right text-gray-400 font-sans text-[11px]">{sym.updated}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

      </div>
    </div>
  );
}
