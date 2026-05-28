import React, { useState } from 'react';
import {
  Database,
  Plus,
  ArrowUpRight,
  RefreshCw,
  FolderDown,
  AlertCircle,
  Clock,
  CheckCircle,
  Compass,
  FileSpreadsheet
} from 'lucide-react';
import { createReplayDataset, importReplayDataset } from '../api/datasets';
import { Dataset, Theme, ViewState } from '../types';

interface DatasetsViewProps {
  theme: Theme;
  viewState: ViewState;
  datasets: Dataset[];
  onAddDataset: (newDs: Dataset) => void;
  datasetActionsEnabled?: boolean;
  unsupportedActionMessage?: string;
}

const defaultUnsupportedActionMessage =
  'Dataset create/import is not connected in this MVP. Dataset rows come from the backend only.';

export default function DatasetsView({
  theme,
  viewState,
  datasets,
  onAddDataset,
  datasetActionsEnabled = false,
  unsupportedActionMessage = defaultUnsupportedActionMessage
}: DatasetsViewProps) {
  const isDark = theme === 'dark';
  const [showForm, setShowForm] = useState(false);
  
  // Create dataset form state
  const [formName, setFormName] = useState('');
  const [formSymbol, setFormSymbol] = useState('');
  const [formInterval, setFormInterval] = useState('1m');
  const [formStart, setFormStart] = useState('');
  const [formEnd, setFormEnd] = useState('');
  const [formSource, setFormSource] = useState('');
  const [isDragging, setIsDragging] = useState(false);
  const [actionMessage, setActionMessage] = useState<string | null>(null);
  const [droppedFile, setDroppedFile] = useState<File | null>(null);
  const [isSaving, setIsSaving] = useState(false);

  const showUnsupportedActionMessage = () => {
    setActionMessage(unsupportedActionMessage);
  };

  // Form submission
  const handleCreateDataset = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!datasetActionsEnabled) {
      showUnsupportedActionMessage();
      return;
    }

    setIsSaving(true);
    setActionMessage(null);
    try {
      const newDataset = await createReplayDataset({
        name: formName,
        symbol: formSymbol,
        interval: formInterval,
        source: formSource,
      });

      if (droppedFile !== null) {
        await importReplayDataset(newDataset.id, droppedFile);
      }

      onAddDataset(newDataset);
      setDroppedFile(null);
      setShowForm(false);
    } catch (error) {
      setActionMessage(error instanceof Error ? error.message : 'Unable to save dataset metadata.');
    } finally {
      setIsSaving(false);
    }
  };

  // Drag events only stage the selected file; rows are created by backend APIs.
  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(true);
  };

  const handleDragLeave = () => {
    setIsDragging(false);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    if (!datasetActionsEnabled) {
      showUnsupportedActionMessage();
      return;
    }

    const file = e.dataTransfer.files.length > 0 ? e.dataTransfer.files[0] : null;
    setDroppedFile(file ?? null);
    if (file) {
      setFormName(file.name.replace(/\.[^.]+$/, ''));
      setFormSource('Uploaded file');
    }
    setShowForm(true);
  };

  // Helpers
  const totalDatasets = datasets.length;
  const importedSuccess = datasets.filter((d) => d.status === 'Imported').length;
  const importingCount = datasets.filter((d) => d.status === 'Importing').length;

  // Render State Injector outcomes
  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-6 animate-pulse">
        <div className="h-6 w-48 bg-slate-300 dark:bg-slate-700 rounded mb-4"></div>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          {[1, 2, 3].map((n) => (
            <div key={n} className="p-5 h-24 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
          ))}
        </div>
        <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (viewState === 'error') {
    return (
      <div className="p-6 text-center max-w-lg mx-auto mt-12">
        <div className="inline-flex p-4 rounded-full bg-red-100 text-red-500 mb-4 dark:bg-red-950/40">
          <AlertCircle size={32} />
        </div>
        <h3 className={`text-base font-bold mb-2 ${isDark ? 'text-white' : 'text-slate-900'}`}>
          Failed to Sync Dataset Registry
        </h3>
        <p className="text-xs text-gray-500 mb-4">
          An operational exception occurred. DB connection timed out while querying binary dataset indexes.
        </p>
        <button className="px-3.5 py-1.5 p-2 bg-blue-600 text-white rounded-lg text-xs font-semibold hover:bg-blue-500">
          Retry Sync Sequence
        </button>
      </div>
    );
  }

  if (totalDatasets === 0 && !showForm) {
    return (
      <div className="p-6 text-center max-w-lg mx-auto mt-12">
        <div className="inline-flex p-4 rounded-full bg-slate-100 text-slate-400 mb-4 dark:bg-slate-800/50">
          <FolderDown size={32} />
        </div>
        <h3 className={`text-base font-bold mb-2 ${isDark ? 'text-white' : 'text-slate-900'}`}>
          No Replay Datasets Created
        </h3>
        <p className="text-xs text-gray-500 mb-4">
          You have not imported any backtest historical data. Create or drop a dataset to enable sandbox playback.
        </p>
        {actionMessage && (
          <p className="text-xs text-blue-500 mb-4">
            {actionMessage}
          </p>
        )}
        <button
          onClick={() => (datasetActionsEnabled ? setShowForm(true) : showUnsupportedActionMessage())}
          className="px-3.5 py-1.5 p-2 bg-blue-600 text-white rounded-lg text-xs font-semibold hover:bg-blue-500 flex items-center justify-center mx-auto space-x-1"
        >
          <Plus size={13} />
          <span>Upload Dataset Record</span>
        </button>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">
      
      {/* Metrics Summary Rows */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        
        {/* Total Metric Card */}
        <div className={`p-5 rounded-xl border transition-all ${
          isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'
        }`}>
          <p className="text-[10px] font-bold uppercase tracking-wider text-slate-400">Total Datasets Registered</p>
          <div className="flex items-baseline space-x-2 mt-2">
            <span className="text-2xl font-bold tracking-tight">{totalDatasets}</span>
            <span className="text-[10px] text-emerald-500 font-semibold uppercase font-mono">Synced</span>
          </div>
          <p className="text-[10px] text-gray-400 mt-1">Available for broker simulators</p>
        </div>

        {/* Importing state card */}
        <div className={`p-5 rounded-xl border transition-all ${
          isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'
        }`}>
          <p className="text-[10px] font-bold uppercase tracking-wider text-slate-400">Import Threads Running</p>
          <div className="flex items-baseline space-x-2 mt-2">
            <span className="text-2xl font-bold tracking-tight text-blue-500">
              {importingCount}
            </span>
            {importingCount > 0 && (
              <span className="text-[9px] bg-blue-500/10 text-blue-400 px-1 rounded animate-pulse">Hashing</span>
            )}
          </div>
          <p className="text-[10px] text-gray-400 mt-1">Backend import jobs in progress</p>
        </div>

        {/* Success percentage card */}
        <div className={`p-5 rounded-xl border transition-all ${
          isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'
        }`}>
          <p className="text-[10px] font-bold uppercase tracking-wider text-slate-400">Dataset Archive Status</p>
          <div className="flex items-baseline space-x-2 mt-2">
            <span className="text-2xl font-bold tracking-tight text-emerald-500">
              {importedSuccess}
            </span>
            <span className="text-[10px] text-slate-400">Healthy Volumes</span>
          </div>
          <p className="text-[10px] text-gray-400 mt-1">Binary index coverage verified</p>
        </div>

      </div>

      {/* Main Container Row + Optional Creator Panel */}
      <div className="flex flex-col lg:flex-row gap-6">
        {/* Table list */}
        <div className="flex-1 space-y-4">
          <div className="flex justify-between items-center">
            <h3 className={`text-sm font-bold tracking-tight ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Available Archive Volumes
            </h3>
            <button
              onClick={() => setShowForm(!showForm)}
              className="px-3 py-1.5 rounded-lg bg-blue-600 text-white hover:bg-blue-500 text-xs font-semibold flex items-center space-x-1 cursor-pointer"
            >
              <Plus size={13} />
              <span>Import Dataset</span>
            </button>
          </div>

          <div className={`border rounded-xl overflow-hidden shadow-sm ${
            isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
          }`}>
            <div className="overflow-x-auto">
              <table className="w-full text-left border-collapse text-xs">
                <thead>
                  <tr className={`border-b text-[10px] uppercase font-bold tracking-wider ${
                    isDark ? 'bg-slate-800/40 border-slate-800 text-gray-400' : 'bg-slate-50/50 border-slate-100 text-slate-500'
                  }`}>
                    <th className="p-3">Dataset Name</th>
                    <th className="p-3">Ticker</th>
                    <th className="p-3">Interval</th>
                    <th className="p-3">Timeline Range</th>
                    <th className="p-3">Source Channel</th>
                    <th className="p-3">Capacity/Size</th>
                    <th className="p-3">Status Badge</th>
                    <th className="p-3">Updated</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/60">
                  {datasets.map((ds) => (
                    <tr
                      key={ds.id}
                      className={`hover:bg-slate-50/40 dark:hover:bg-slate-800/30 transition-colors`}
                    >
                      <td className="p-3 font-semibold text-slate-900 dark:text-gray-100 flex items-center space-x-2">
                        <FileSpreadsheet size={13} className="text-gray-400 group-hover:text-sky-500" />
                        <span>{ds.name}</span>
                      </td>
                      <td className="p-3 font-mono">{ds.symbol}</td>
                      <td className="p-3 text-slate-500 dark:text-gray-400 font-mono">{ds.interval}</td>
                      <td className="p-3 font-mono text-[11px] text-gray-500 dark:text-gray-400">{ds.timeRange}</td>
                      <td className="p-3 text-slate-600 dark:text-gray-300">{ds.source}</td>
                      <td className="p-3 font-mono text-gray-400">{ds.size || 'N/A'}</td>
                      <td className="p-3">
                        <span className={`px-2 py-0.5 rounded pill text-[10px] font-bold ${
                          ds.status === 'Imported'
                            ? 'bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
                            : ds.status === 'Importing'
                            ? 'bg-blue-500/10 text-blue-500 dark:text-blue-400'
                            : ds.status === 'Failed'
                            ? 'bg-red-500/10 text-red-500 dark:text-red-400'
                            : 'bg-slate-500/10 text-slate-400'
                        }`}>
                          {ds.status}
                        </span>
                      </td>
                      <td className="p-3 text-gray-400 text-[11px]">{ds.updated}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>

        {/* Create/Import drawer or panel */}
        {showForm && (
          <div className={`w-full lg:w-80 rounded-xl border p-5 space-y-4 shadow-sm h-fit ${
            isDark ? 'bg-[#181a1e] border-slate-800' : 'bg-slate-50 border-slate-200'
          }`}>
            {actionMessage && (
              <div className="text-xs text-blue-500 border border-blue-500/20 bg-blue-500/10 rounded-lg p-3">
                {actionMessage}
              </div>
            )}
            <div className="flex items-center justify-between pb-2 border-b border-slate-200 dark:border-slate-800">
              <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
                Register Custom Dataset
              </h4>
              <button onClick={() => setShowForm(false)} className="text-[10px] text-gray-500 hover:text-black dark:hover:text-white">
                CLOSE
              </button>
            </div>

            {/* Drag & Drop Area */}
            <div
              onDragOver={handleDragOver}
              onDragLeave={handleDragLeave}
              onDrop={handleDrop}
              className={`border-2 border-dashed rounded-lg p-4 text-center cursor-pointer transition-all ${
                isDragging
                  ? 'border-blue-500 bg-blue-500/10 scale-[1.02]'
                  : isDark
                  ? 'border-slate-800 hover:border-slate-700 bg-slate-900/40 text-gray-400'
                  : 'border-slate-300 hover:border-slate-400 bg-white text-slate-500'
              }`}
            >
              <FileSpreadsheet size={22} className="mx-auto text-blue-500 animate-pulse mb-1.5" />
              <p className="text-xs font-semibold">Drag & Drop CSV / JSON</p>
              <p className="text-[10px] text-gray-400 mt-0.5">Loads column parameters instantly</p>
            </div>

            <form onSubmit={handleCreateDataset} className="space-y-3" noValidate={!datasetActionsEnabled}>
              <div>
                <label htmlFor="dataset-name" className="block text-[10px] font-semibold text-gray-400 uppercase">Dataset Name</label>
                <input
                  id="dataset-name"
                  type="text"
                  value={formName}
                  onChange={(e) => setFormName(e.target.value)}
                  className={`w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
                  required
                />
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label htmlFor="dataset-symbol" className="block text-[10px] font-semibold text-gray-400 uppercase">Symbol Pair</label>
                  <input
                    id="dataset-symbol"
                    type="text"
                    value={formSymbol}
                    onChange={(e) => setFormSymbol(e.target.value)}
                    className={`w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
                    required
                  />
                </div>
                <div>
                  <label className="block text-[10px] font-semibold text-gray-400 uppercase">Bar Interval</label>
                  <select
                    value={formInterval}
                    onChange={(e) => setFormInterval(e.target.value)}
                    className={`w-full p-2 border text-xs rounded ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
                  >
                    <option value="1s">1 second</option>
                    <option value="1m">1 minute</option>
                    <option value="5m">5 minute</option>
                    <option value="1h">1 hour</option>
                  </select>
                </div>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="block text-[10px] font-semibold text-gray-400 uppercase">Start Date</label>
                  <input
                    type="text"
                    value={formStart}
                    onChange={(e) => setFormStart(e.target.value)}
                    className="w-full p-2 border text-xs rounded font-mono bg-slate-100 dark:bg-slate-900 dark:border-slate-800 dark:text-white"
                  />
                </div>
                <div>
                  <label className="block text-[10px] font-semibold text-gray-400 uppercase">End Date</label>
                  <input
                    type="text"
                    value={formEnd}
                    onChange={(e) => setFormEnd(e.target.value)}
                    className="w-full p-2 border text-xs rounded font-mono bg-slate-100 dark:bg-slate-900 dark:border-slate-800 dark:text-white"
                  />
                </div>
              </div>

              <div>
                <label htmlFor="dataset-source" className="block text-[10px] font-semibold text-gray-400 uppercase">Data Feed Source</label>
                <input
                  id="dataset-source"
                  type="text"
                  value={formSource}
                  onChange={(e) => setFormSource(e.target.value)}
                  className={`w-full p-2 border text-xs rounded ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
                />
              </div>

              <button
                type="submit"
                disabled={isSaving}
                className="w-full py-2 bg-blue-600 hover:bg-blue-500 disabled:bg-blue-400 disabled:cursor-not-allowed text-white font-semibold rounded text-xs transition-colors cursor-pointer"
              >
                {isSaving ? 'Saving Dataset' : 'Assemble Dataset Metadata'}
              </button>
            </form>
          </div>
        )}

      </div>
    </div>
  );
}
