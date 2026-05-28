import {
  AlertTriangle,
  Flame,
  CheckCircle2,
  AlertOctagon,
  Clock,
  ShieldAlert,
  ChevronRight
} from 'lucide-react';
import { SystemAlert, Theme, ViewState } from '../types';

interface AlertsViewProps {
  theme: Theme;
  viewState: ViewState;
  alerts: SystemAlert[];
  onClearAll: () => void;
}

export default function AlertsView({
  theme,
  viewState,
  alerts,
  onClearAll
}: AlertsViewProps) {
  const isDark = theme === 'dark';

  // Group alerts
  const criticals = alerts.filter((a) => a.severity === 'Critical');
  const warnings = alerts.filter((a) => a.severity === 'Warning');
  const infos = alerts.filter((a) => a.severity === 'Info');
  const resolveds = alerts.filter((a) => a.severity === 'Resolved');

  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-300 dark:bg-slate-700 rounded"></div>
        <div className="h-28 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (alerts.length === 0) {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12 space-y-3">
        <div className="inline-flex p-4 rounded-full bg-emerald-500/10 text-emerald-500">
          <CheckCircle2 size={32} />
        </div>
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Zero alerts currently active</h3>
        <p className="text-xs text-gray-400">
          All sandbox pipelines and agent tokens are operating inside permitted SLA boundaries successfully.
        </p>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Counters layout block */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        
        <div className={`p-4 rounded-xl border flex items-center justify-between ${
          isDark ? 'bg-slate-900/40 border-slate-800 text-red-400' : 'bg-red-50/50 border-red-200 text-red-600'
        }`}>
          <div>
            <p className="text-[10px] uppercase font-bold text-slate-400 tracking-wider">Critical Failures</p>
            <p className="text-xl font-bold font-mono">{criticals.length}</p>
          </div>
          <Flame size={16} className="text-red-500 animate-bounce" />
        </div>

        <div className={`p-4 rounded-xl border flex items-center justify-between ${
          isDark ? 'bg-slate-900/40 border-slate-800 text-amber-400' : 'bg-amber-50/50 border-amber-200 text-amber-600'
        }`}>
          <div>
            <p className="text-[10px] uppercase font-bold text-slate-400 tracking-wider">Warnings Issues</p>
            <p className="text-xl font-bold font-mono">{warnings.length}</p>
          </div>
          <AlertOctagon size={16} className="text-amber-500" />
        </div>

        <div className={`p-4 rounded-xl border flex items-center justify-between ${
          isDark ? 'bg-slate-900/40 border-slate-800 text-blue-400' : 'bg-blue-50/50 border-blue-200 text-blue-600'
        }`}>
          <div>
            <p className="text-[10px] uppercase font-bold text-slate-400 tracking-wider">Operator Info</p>
            <p className="text-xl font-bold font-mono">{infos.length}</p>
          </div>
          <ShieldAlert size={16} />
        </div>

        <div className={`p-4 rounded-xl border flex items-center justify-between ${
          isDark ? 'bg-slate-900/40 border-slate-800 text-emerald-400' : 'bg-emerald-50/50 border-emerald-200 text-emerald-600'
        }`}>
          <div>
            <p className="text-[10px] uppercase font-bold text-slate-400 tracking-wider">Resolved Conditions</p>
            <p className="text-xl font-bold font-mono">{resolveds.length}</p>
          </div>
          <CheckCircle2 size={16} className="text-emerald-500" />
        </div>

      </div>

      {/* Dynamic Group lists */}
      <div className="space-y-6">
        
        {/* Actions bar */}
        <div className="flex justify-between items-center pb-2 border-b dark:border-slate-800">
          <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
            Trigger Alert Logs
          </h4>
          <button
            onClick={onClearAll}
            className="px-2.5 py-1 text-[10px] font-semibold bg-red-600/10 hover:bg-red-600/20 text-red-500 rounded border border-red-500/10 cursor-pointer"
          >
            Resolve Unflagged Alert Conditions
          </button>
        </div>

        {/* Group lists panels render */}
        <div className="space-y-4">
          
          {/* Critical blocks */}
          {criticals.length > 0 && (
            <div className="space-y-2">
              <span className="text-[10px] font-bold text-red-500 uppercase tracking-wider block">Critical (Hardware Lock Action Required)</span>
              {criticals.map((al) => (
                <div key={al.id} className="p-4 rounded-xl border border-red-500/20 bg-red-500/5 flex items-start space-x-3">
                  <Flame size={16} className="text-red-500 flex-shrink-0 mt-0.5" />
                  <div className="text-xs">
                    <div className="flex items-center space-x-2 font-bold text-red-500">
                      <span>{al.title}</span>
                      {al.sandbox && <span className="text-[9px] px-1 bg-red-500/10 border border-red-500/20 rounded font-normal">{al.sandbox}</span>}
                    </div>
                    <p className="text-gray-400 mt-1 font-mono">{al.explanation}</p>
                    <span className="text-[9px] text-gray-500 block mt-2 font-mono">Reported On: {al.timestamp}</span>
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* Warning blocks */}
          {warnings.length > 0 && (
            <div className="space-y-2 pt-2">
              <span className="text-[10px] font-bold text-amber-500 uppercase tracking-wider block">System Latency Warnings</span>
              {warnings.map((al) => (
                <div key={al.id} className="p-4 rounded-xl border border-amber-500/20 bg-amber-500/5 flex items-start space-x-3">
                  <AlertTriangle size={16} className="text-amber-500 flex-shrink-0 mt-0.5" />
                  <div className="text-xs">
                    <div className="flex items-center space-x-2 font-bold text-amber-500">
                      <span>{al.title}</span>
                    </div>
                    <p className="text-gray-400 mt-1 font-mono">{al.explanation}</p>
                    <span className="text-[9px] text-gray-500 block mt-2 font-mono">Reported On: {al.timestamp}</span>
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* Resolved list */}
          {resolveds.length > 0 && (
            <div className="space-y-2 pt-2">
              <span className="text-[10px] font-bold text-emerald-500 uppercase tracking-wider block">Resolved Anomalies</span>
              {resolveds.map((al) => (
                <div key={al.id} className="p-4 rounded-xl border border-emerald-500/20 bg-emerald-500/5 flex items-start space-x-3">
                  <CheckCircle2 size={16} className="text-emerald-500 flex-shrink-0 mt-0.5" />
                  <div className="text-xs">
                    <div className="flex items-center space-x-2 font-bold text-emerald-500">
                      <span>{al.title}</span>
                    </div>
                    <p className="text-gray-400 mt-1 font-mono">{al.explanation}</p>
                    <span className="text-[9px] text-gray-500 block mt-2 font-mono">Resolved On: {al.timestamp}</span>
                  </div>
                </div>
              ))}
            </div>
          )}

        </div>

      </div>

    </div>
  );
}
