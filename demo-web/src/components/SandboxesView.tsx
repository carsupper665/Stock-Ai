import React, { useState } from 'react';
import {
  Box,
  Plus,
  Play,
  Pause,
  StopCircle,
  Eye,
  Trash2,
  AlertTriangle,
  RefreshCw,
  FolderSync,
  Layers,
  HelpCircle
} from 'lucide-react';
import { Sandbox, Theme, DemoVisualState } from '../types';

interface SandboxesViewProps {
  theme: Theme;
  demoState: DemoVisualState;
  sandboxes: Sandbox[];
  onAddSandbox: (sb: Sandbox) => void;
  onUpdateSandbox: (sb: Sandbox) => void;
  onSelectSandbox: (id: string) => void;
  datasets: { id: string; name: string; symbol: string }[];
}

export default function SandboxesView({
  theme,
  demoState,
  sandboxes,
  onAddSandbox,
  onUpdateSandbox,
  onSelectSandbox,
  datasets
}: SandboxesViewProps) {
  const isDark = theme === 'dark';
  const [showForm, setShowForm] = useState(false);
  const [sbConfirmDeleteId, setSbConfirmDeleteId] = useState<string | null>(null);

  // Form states
  const [formName, setFormName] = useState('Sandbox Epsilon');
  const [formMode, setFormMode] = useState<'Replay' | 'Live'>('Replay');
  const [selectedDatasetId, setSelectedDatasetId] = useState(datasets[0]?.id || 'ds-01');

  // Actions
  const handleLifecycle = (id: string, action: 'Start' | 'Pause' | 'Stop') => {
    const sb = sandboxes.find((s) => s.id === id);
    if (sb) {
      let nextStatus = sb.status;
      if (action === 'Start') nextStatus = 'Running';
      if (action === 'Pause') nextStatus = 'Paused';
      if (action === 'Stop') nextStatus = 'Stopped';

      const updated = {
        ...sb,
        status: nextStatus,
        updated: 'Just now',
        freshness: action === 'Start' ? 'Running simulation rate: 10x' : 'Simulation paused'
      };
      onUpdateSandbox(updated);
    }
  };

  const handleCreateSandbox = (e: React.FormEvent) => {
    e.preventDefault();
    const dsObj = datasets.find((d) => d.id === selectedDatasetId);
    const newSb: Sandbox = {
      id: `sb-${Date.now().toString().slice(-4)}`,
      name: formName,
      mode: formMode,
      status: 'Stopped',
      datasetId: selectedDatasetId,
      datasetName: dsObj ? dsObj.name : 'Unknown',
      accountsCount: 0,
      replayTime: formMode === 'Replay' ? '2026-01-01 00:00:00 UTC' : 'Live stream feed sync',
      freshness: 'Static Archive (Stopped)',
      updated: 'Just now'
    };
    onAddSandbox(newSb);
    setShowForm(false);
  };

  const handleDeleteSandbox = (id: string) => {
    // For visual demo, we will label it Stopped/Deleted
    const sb = sandboxes.find((s) => s.id === id);
    if (sb) {
      const updated = {
        ...sb,
        status: 'Stopped' as const,
        accountsCount: 0,
        replayTime: 'Terminated',
        freshness: 'Deleted/Decommissioned'
      };
      onUpdateSandbox(updated);
    }
    setSbConfirmDeleteId(null);
  };

  // Derive counts
  const runningCount = sandboxes.filter((s) => s.status === 'Running').length;
  const pausedCount = sandboxes.filter((s) => s.status === 'Paused').length;

  if (demoState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-300 dark:bg-slate-700 rounded mb-4"></div>
        <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (demoState === 'empty' || sandboxes.length === 0) {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12">
        <Box size={32} className={`mx-auto text-slate-400 mb-2`} />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>No sandboxes created</h3>
        <p className="text-xs text-gray-500 mt-1 mb-4">
          Create a sandbox state machine to begin simulation analysis.
        </p>
        <button
          onClick={() => setShowForm(true)}
          className="px-3.5 py-1.5 bg-blue-600 text-white rounded text-xs font-semibold hover:bg-blue-500"
        >
          Build Simulation Sandbox
        </button>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Metrics Row */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        
        <div className="p-4 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Environment Simulators</p>
          <p className="text-2xl font-bold mt-1">{sandboxes.length}</p>
          <span className="text-[9px] text-app-text-muted">Total isolated nodes active</span>
        </div>

        <div className="p-4 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Running Replays</p>
          <p className="text-2xl font-bold text-app-success mt-1">{runningCount} Core Engines</p>
          <span className="text-[9px] text-app-text-muted">Continuously advancing timeline pointers</span>
        </div>

        <div className="p-4 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Suspended / Paused</p>
          <p className="text-2xl font-bold text-app-warning mt-1">{pausedCount} Staged</p>
          <span className="text-[9px] text-app-text-muted">Tick queue frozen and waiting</span>
        </div>

      </div>

      {/* Main layout row */}
      <div className="flex flex-col lg:flex-row gap-6">

        {/* List Table */}
        <div className="flex-grow space-y-4">
          <div className="flex justify-between items-center">
            <h3 className={`text-sm font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Operational Simulation Registry
            </h3>
            <button
              onClick={() => setShowForm(!showForm)}
              className="px-3 py-1.5 bg-app-accent hover:bg-app-accent/90 text-white text-xs font-semibold rounded-lg flex items-center space-x-1 cursor-pointer transition-colors"
            >
              <Plus size={13} />
              <span>Create Sandbox</span>
            </button>
          </div>

          <div className="border rounded-xl spill-x-auto overflow-hidden shadow-sm bg-app-card border-app-border text-app-text-main">
            <div className="overflow-x-auto">
              <table className="w-full text-left border-collapse b-0 text-xs">
                <thead>
                  <tr className="border-b text-[10px] uppercase font-bold tracking-wider bg-app-bg/50 border-app-border text-app-text-muted">
                    <th className="p-3">Sandbox Machine Name</th>
                    <th className="p-3">Ingest Mode</th>
                    <th className="p-3">Execution Status</th>
                    <th className="p-3">Assigned Dataset</th>
                    <th className="p-3">Attached Accounts</th>
                    <th className="p-3">Current Playback Time (UTC)</th>
                    <th className="p-3">Playback Rate / Latency</th>
                    <th className="p-3 text-right">Interactive Lifecycle Controls</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
                  {sandboxes.map((sb) => (
                    <tr key={sb.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30 transition-colors">
                      <td className="p-3 font-semibold text-slate-900 dark:text-gray-100 flex items-center space-x-2">
                        <Box size={13} className="text-blue-500" />
                        <span>{sb.name}</span>
                      </td>
                      <td className="p-3">
                        <span className={`px-2 py-0.5 rounded text-[10px] font-semibold border ${
                          sb.mode === 'Replay' 
                            ? 'bg-purple-500/10 text-purple-500 border-purple-500/20' 
                            : 'bg-sky-500/10 text-sky-500 border-sky-500/20'
                        }`}>
                          {sb.mode}
                        </span>
                      </td>
                      <td className="p-3">
                        <span className={`px-2.5 py-0.5 rounded-full text-[10px] font-bold ${
                          sb.status === 'Running'
                            ? 'bg-emerald-500/15 text-emerald-500 dark:text-emerald-400'
                            : sb.status === 'Paused'
                            ? 'bg-amber-500/15 text-amber-500 dark:text-amber-400'
                            : 'bg-slate-500/15 text-slate-400'
                        }`}>
                          {sb.status}
                        </span>
                      </td>
                      <td className="p-3 text-gray-500 truncate max-w-[120px]" title={sb.datasetName}>{sb.datasetName || 'None'}</td>
                      <td className="p-3 font-mono font-medium text-center">{sb.accountsCount}</td>
                      <td className="p-3 font-mono text-[11px] text-sky-500 font-bold">{sb.replayTime}</td>
                      <td className="p-3 text-gray-400 text-[10px]">{sb.freshness}</td>
                      
                      {/* Live controls buttons in rows */}
                      <td className="p-3 text-right space-x-1">
                        <button
                          onClick={() => handleLifecycle(sb.id, 'Start')}
                          className={`p-1 rounded hover:bg-slate-100 dark:hover:bg-slate-800 cursor-pointer ${
                            sb.status === 'Running' ? 'text-gray-300' : 'text-emerald-500'
                          }`}
                          title="Start / Resume Simulation Tickers"
                          disabled={sb.status === 'Running'}
                        >
                          <Play size={12} fill="currentColor" className="inline" />
                        </button>
                        <button
                          onClick={() => handleLifecycle(sb.id, 'Pause')}
                          className={`p-1 rounded hover:bg-slate-100 dark:hover:bg-slate-800 cursor-pointer ${
                            sb.status === 'Paused' || sb.status === 'Stopped' ? 'text-gray-300' : 'text-amber-500'
                          }`}
                          title="Pause Playback Sim"
                          disabled={sb.status === 'Paused' || sb.status === 'Stopped'}
                        >
                          <Pause size={12} fill="currentColor" className="inline" />
                        </button>
                        <button
                          onClick={() => handleLifecycle(sb.id, 'Stop')}
                          className={`p-1 rounded hover:bg-slate-100 dark:hover:bg-slate-800 cursor-pointer ${
                            sb.status === 'Stopped' ? 'text-gray-300' : 'text-rose-500'
                          }`}
                          title="Stop/Reset Sim State"
                          disabled={sb.status === 'Stopped'}
                        >
                          <StopCircle size={12} className="inline" />
                        </button>
                        <button
                          onClick={() => onSelectSandbox(sb.id)}
                          className="p-1 rounded hover:bg-blue-500/10 text-blue-500 inline-flex items-center cursor-pointer"
                          title="Open Analytical Monitor Deck"
                        >
                          <Eye size={12} className="mr-0.5" />
                          <span className="text-[10px]">Inspect</span>
                        </button>
                        <button
                          onClick={() => setSbConfirmDeleteId(sb.id)}
                          className="p-1 rounded hover:bg-red-500/10 text-red-500 inline-flex cursor-pointer"
                          title="Decommission Sandbox"
                        >
                          <Trash2 size={12} />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>

        {/* Sandbox Creation modal/form side block */}
        {(showForm || sbConfirmDeleteId) && (
          <div className="w-full lg:w-80 flex-shrink-0 space-y-4">
            
            {showForm && (
              <div className="p-5 rounded-xl border shadow-sm bg-app-card border-app-border text-app-text-main">
                <div className="flex items-center justify-between pb-2 mb-3 border-b border-slate-200 dark:border-slate-800">
                  <h4 className="text-xs font-bold uppercase tracking-wider text-slate-700 dark:text-white">Build Sandbox Node</h4>
                  <button onClick={() => setShowForm(false)} className="text-[10px] text-gray-400 hover:text-black dark:hover:text-white">
                    CLOSE
                  </button>
                </div>
                <form onSubmit={handleCreateSandbox} className="space-y-3">
                  <div>
                    <label className="block text-[10px] font-semibold text-gray-400 uppercase">Sandbox Label</label>
                    <input
                      type="text"
                      value={formName}
                      onChange={(e) => setFormName(e.target.value)}
                      className="w-full p-2 border text-xs rounded font-mono bg-app-bg border-app-border text-app-text-main focus:outline-hidden focus:ring-1 focus:ring-app-accent"
                      required
                    />
                  </div>

                  <div>
                    <label className="block text-[10px] font-semibold text-gray-400 uppercase">Input Registry Mode</label>
                    <div className="grid grid-cols-2 gap-2 mt-1">
                      <button
                        type="button"
                        onClick={() => setFormMode('Replay')}
                        className={`py-1.5 rounded text-xs font-bold border transition-all ${
                          formMode === 'Replay'
                            ? 'bg-purple-600/10 border-purple-500 text-purple-500 font-bold'
                            : 'bg-white text-slate-600 border-slate-200 dark:bg-slate-900 dark:border-slate-800 dark:text-gray-400'
                        }`}
                      >
                        Historical Replay
                      </button>
                      <button
                        type="button"
                        onClick={() => setFormMode('Live')}
                        className={`py-1.5 rounded text-xs font-bold border transition-all ${
                          formMode === 'Live'
                            ? 'bg-sky-600/10 border-sky-400 text-sky-500 font-bold'
                            : 'bg-white text-slate-600 border-slate-200 dark:bg-slate-900 dark:border-slate-800 dark:text-gray-400'
                        }`}
                      >
                        Live Proxy Segment
                      </button>
                    </div>
                  </div>

                  {formMode === 'Replay' && (
                    <div>
                      <label className="block text-[10px] font-semibold text-gray-400 uppercase">Assigned Dataset File</label>
                      <select
                        value={selectedDatasetId}
                        onChange={(e) => setSelectedDatasetId(e.target.value)}
                        className="w-full p-2 border text-xs rounded bg-app-bg border-app-border text-app-text-main focus:outline-hidden focus:ring-1 focus:ring-app-accent"
                      >
                        {datasets.map((d) => (
                          <option key={d.id} value={d.id}>{d.name} ({d.symbol})</option>
                        ))}
                      </select>
                    </div>
                  )}

                  <button
                    type="submit"
                    className="w-full py-2 bg-app-accent hover:bg-app-accent/90 text-white font-semibold rounded text-xs transition-colors cursor-pointer"
                  >
                    Assemble Sandbox Node
                  </button>
                </form>
              </div>
            )}

            {/* Delete Confirmation Box */}
            {sbConfirmDeleteId && (
              <div className="p-5 rounded-xl border border-red-500/20 bg-red-400/5 shadow-md space-y-4 animate-slideIn">
                <div className="flex items-center space-x-1.5 text-red-500">
                  <AlertTriangle size={15} />
                  <h4 className="text-xs font-bold uppercase tracking-wider">Decommission Sandbox</h4>
                </div>
                <p className="text-xs text-slate-500 leading-relaxed">
                  Are you of absolute operational authority to terminate sandbox <strong>{sandboxes.find((s) => s.id === sbConfirmDeleteId)?.name}</strong>? Virtual transactions and audit records for children accounts will be frozen.
                </p>
                <div className="flex gap-2">
                  <button
                    onClick={() => handleDeleteSandbox(sbConfirmDeleteId)}
                    className="flex-1 py-1.5 bg-red-600 text-white hover:bg-red-500 text-xs font-semibold rounded"
                  >
                    Terminate Sandbox
                  </button>
                  <button
                    onClick={() => setSbConfirmDeleteId(null)}
                    className="flex-1 py-1.5 bg-slate-200 dark:bg-slate-800 text-slate-600 dark:text-gray-300 text-xs font-semibold rounded"
                  >
                    Cancel
                  </button>
                </div>
              </div>
            )}

          </div>
        )}

      </div>
    </div>
  );
}
