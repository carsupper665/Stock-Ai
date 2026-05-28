import React, { useState } from 'react';
import {
  ShieldAlert,
  Plus,
  Compass,
  CheckCircle,
  HelpCircle,
  AlertTriangle,
  Info,
  Layers,
  FileText
} from 'lucide-react';
import { LiveAccount, Theme, DemoVisualState } from '../types';

interface LiveAccountsViewProps {
  theme: Theme;
  demoState: DemoVisualState;
  liveAccounts: LiveAccount[];
  onAddLiveAccount: (la: LiveAccount) => void;
}

export default function LiveAccountsView({
  theme,
  demoState,
  liveAccounts,
  onAddLiveAccount
}: LiveAccountsViewProps) {
  const isDark = theme === 'dark';
  const [showForm, setShowForm] = useState(false);
  const [accName, setAccName] = useState('Kraken Enterprise Hedge');
  const [exchange, setExchange] = useState('Kraken');
  const [label, setLabel] = useState('METADATA_READONLY_VAL_B');

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const newLa: LiveAccount = {
      id: `live-acc-${Date.now().toString().slice(-4)}`,
      name: accName,
      exchange: exchange,
      label: label || 'METADATA_UNASSIGNED',
      status: 'Connected',
      created: 'Just now',
      updated: 'Just now'
    };
    onAddLiveAccount(newLa);
    setShowForm(false);
  };

  if (demoState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-300 dark:bg-slate-700 rounded"></div>
        <div className="h-20 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
        <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (demoState === 'error') {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12 bg-red-500/5 border border-red-500/20 rounded-xl p-6">
        <ShieldAlert className="mx-auto text-red-500 mb-2" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Signature validation error</h3>
        <p className="text-xs text-slate-500 mt-1">
          Strict read-only security boundary validation failed to confirm identity checksum.
        </p>
      </div>
    );
  }

  if (demoState === 'empty' || liveAccounts.length === 0) {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12">
        <Layers className={`mx-auto text-slate-400 mb-2`} />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>No Live Metadata Registered</h3>
        <p className="text-xs text-gray-500 mt-1 mb-4">
          Register exchange metadata reference markers to align simulation bounds with physical tags.
        </p>
        <button
          onClick={() => setShowForm(true)}
          className="px-3 py-1.5 bg-blue-600 text-white rounded text-xs font-semibold hover:bg-blue-500"
        >
          Add Live Account Metadata
        </button>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Main warning/info banner - Highly crucial from spec */}
      <div className="p-4 rounded-xl border border-sky-400 bg-sky-50 dark:bg-sky-950/20 dark:border-sky-800/60 text-sky-700 dark:text-sky-300">
        <div className="flex items-start space-x-3 text-xs leading-relaxed">
          <ShieldAlert size={18} className="flex-shrink-0 text-sky-500 mt-0.5" />
          <div>
            <p className="font-bold uppercase tracking-wider text-[11px] mb-0.5">METADATA BOUNDARY - NO TRADE INGESTION ENABLED</p>
            <p>
              This screen registered production exchange labels for <strong>co-locational monitoring and safety reconciliation parameters</strong> only. Real-time keys, API keys, place-order buttons, and visual trading buttons are strictly unavailable in this operations console interface to prevent unintended live production deployment triggers.
            </p>
          </div>
        </div>
      </div>

      {/* Main layout */}
      <div className="flex flex-col lg:flex-row gap-6">

        {/* List of metadata */}
        <div className="flex-grow space-y-4">
          <div className="flex justify-between items-center">
            <h3 className={`text-sm font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Registered Production Metadata List
            </h3>
            <button
              onClick={() => setShowForm(!showForm)}
              className="px-3 py-1.5 bg-blue-600 text-white text-xs font-semibold rounded-lg hover:bg-blue-500 flex items-center space-x-1 cursor-pointer"
            >
              <Plus size={13} />
              <span>Add Live Account Metadata</span>
            </button>
          </div>

          <div className={`border rounded-xl xl:overflow-hidden shadow-sm ${
            isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
          }`}>
            <div className="overflow-x-auto">
              <table className="w-full text-left border-collapse text-xs">
                <thead>
                  <tr className={`border-b text-[10px] uppercase font-bold tracking-wider ${
                    isDark ? 'bg-slate-800/40 border-slate-800 text-gray-200' : 'bg-slate-50/50 border-slate-100 text-slate-500'
                  }`}>
                    <th className="p-3">Account Name Label</th>
                    <th className="p-3">Physical Exchange Location</th>
                    <th className="p-3">Assigned Policy Identifier Code</th>
                    <th className="p-3">Telemetry Sync Status</th>
                    <th className="p-3">Registered On</th>
                    <th className="p-3">Last Verified Update</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
                  {liveAccounts.map((acc) => (
                    <tr key={acc.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30 transition-colors">
                      <td className="p-3 font-semibold text-slate-900 dark:text-gray-100 flex items-center space-x-2">
                        <FileText size={12} className="text-sky-500" />
                        <span>{acc.name}</span>
                      </td>
                      <td className="p-3 text-slate-600 dark:text-gray-300">{acc.exchange}</td>
                      <td className="p-3 font-mono text-[11px] text-gray-500 dark:text-gray-400">
                        {acc.label}
                      </td>
                      <td className="p-3">
                        <span className={`px-2 py-0.5 rounded pill text-[10px] font-bold ${
                          acc.status === 'Connected'
                            ? 'bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
                            : 'bg-amber-500/10 text-amber-500 dark:text-amber-400'
                        }`}>
                          {acc.status}
                        </span>
                      </td>
                      <td className="p-3 text-gray-400 text-[10px]">{acc.created}</td>
                      <td className="p-3 text-gray-400 text-[10px]">{acc.updated}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>

        {/* Input pane side drawer */}
        {showForm && (
          <div className={`w-full lg:w-80 rounded-xl border p-5 space-y-4 shadow-sm h-fit ${
            isDark ? 'bg-[#181a1e] border-slate-800' : 'bg-slate-50 border-slate-200'
          }`}>
            <div className="flex items-center justify-between pb-2 border-b border-gray-200 dark:border-slate-800">
              <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
                Add Label Metadata
              </h4>
              <button onClick={() => setShowForm(false)} className="text-[10px] text-gray-400 hover:text-black dark:hover:text-white">
                CLOSE
              </button>
            </div>

            <form onSubmit={handleSubmit} className="space-y-3">
              <div>
                <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Account Display Name</label>
                <input
                  type="text"
                  value={accName}
                  onChange={(e) => setAccName(e.target.value)}
                  className={`w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
                  required
                />
              </div>

              <div>
                <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Exchange Location Segment</label>
                <select
                  value={exchange}
                  onChange={(e) => setExchange(e.target.value)}
                  className={`w-full p-2 border text-xs rounded ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
                >
                  <option value="Binance Futures">Binance Futures</option>
                  <option value="OKX Spot">OKX Spot</option>
                  <option value="Coinbase Advanced">Coinbase Advanced</option>
                  <option value="Kraken Institutional">Kraken Institutional</option>
                </select>
              </div>

              <div>
                <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Safety Label Code Tag</label>
                <input
                  type="text"
                  value={label}
                  onChange={(e) => setLabel(e.target.value)}
                  className={`w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
                  placeholder="e.g. PROD_SAFE_REGISTER_C"
                />
                <span className="text-[9px] text-slate-400 mt-1 block">
                  Assign tag boundaries matching the container cluster ruleset.
                </span>
              </div>

              <button
                type="submit"
                className="w-full py-2 bg-blue-600 text-white hover:bg-blue-500 text-xs font-semibold rounded cursor-pointer transition-colors"
              >
                Assemble Reference Metadata
              </button>
            </form>
          </div>
        )}

      </div>
    </div>
  );
}
