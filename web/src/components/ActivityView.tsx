import { useState } from 'react';
import { History, Search, Filter, HelpCircle, Archive, ClipboardCheck, AlertCircle } from 'lucide-react';
import { OperationalLog, Theme, ViewState } from '../types';

interface ActivityViewProps {
  theme: Theme;
  viewState: ViewState;
  logs: OperationalLog[];
}

export default function ActivityView({
  theme,
  viewState,
  logs
}: ActivityViewProps) {
  const isDark = theme === 'dark';

  // State filters
  const [selectedTopic, setSelectedTopic] = useState<string>('All');
  const [selectedSeverity, setSelectedSeverity] = useState<string>('All');
  const [searchQuery, setSearchQuery] = useState<string>('');

  // Handle filter action
  const filteredLogs = logs.filter((log) => {
    const matchesTopic = selectedTopic === 'All' || log.topic === selectedTopic;
    const matchesSeverity = selectedSeverity === 'All' || log.severity === selectedSeverity;
    const matchesSearch =
      searchQuery.trim() === '' ||
      log.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
      log.details.toLowerCase().includes(searchQuery.toLowerCase());

    return matchesTopic && matchesSeverity && matchesSearch;
  });

  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-4 bg-slate-300 dark:bg-slate-700 w-1/4 rounded"></div>
        <div className="h-44 bg-slate-100 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (filteredLogs.length === 0) {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12">
        <Archive size={32} className="mx-auto text-slate-400 mb-2" />
        <h4 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>No logs found</h4>
        <p className="text-xs text-gray-500 mt-1 mb-4">
          Try resetting the search terms or adding more active sandbox records.
        </p>
        <button
          onClick={() => {
            setSelectedTopic('All');
            setSelectedSeverity('All');
            setSearchQuery('');
          }}
          className="px-3 py-1 bg-blue-600 text-white text-xs font-semibold rounded"
        >
          Reset Filters
        </button>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Filter Toolbar */}
      <div className={`p-4 rounded-xl border flex flex-col md:flex-row gap-3 items-center justify-between ${
        isDark ? 'bg-[#1b1e25] border-slate-800' : 'bg-white border-slate-200'
      }`}>
        
        {/* Search Input Box */}
        <div className="relative w-full md:w-64">
          <Search size={14} className="absolute left-3 top-2.5 text-slate-400" />
          <input
            type="text"
            placeholder="Search operational logs..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className={`w-full pl-9 pr-3 py-1.5 border text-xs rounded-lg focus:outline-none focus:border-blue-500 ${
              isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'
            }`}
          />
        </div>

        {/* Category Toggles */}
        <div className="flex flex-wrap items-center gap-3 w-full md:w-auto">
          
          <div className="flex items-center space-x-1">
            <span className="text-[10px] uppercase font-bold text-gray-400">Topic:</span>
            <select
              value={selectedTopic}
              onChange={(e) => setSelectedTopic(e.target.value)}
              className={`p-1 border rounded text-xs font-semibold focus:outline-none ${
                isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'
              }`}
            >
              <option value="All">All Topics</option>
              <option value="System">System</option>
              <option value="Sandbox">Sandbox</option>
              <option value="Order">Order</option>
              <option value="Trade">Trade</option>
              <option value="Agent">Agent Limits</option>
            </select>
          </div>

          <div className="flex items-center space-x-1">
            <span className="text-[10px] uppercase font-bold text-gray-400">Severity:</span>
            <select
              value={selectedSeverity}
              onChange={(e) => setSelectedSeverity(e.target.value)}
              className={`p-1 border rounded text-xs font-semibold focus:outline-none ${
                isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'
              }`}
            >
              <option value="All">All Severities</option>
              <option value="Success">Success (Green)</option>
              <option value="Info">Info (Blue)</option>
              <option value="Warning">Warning (Amber)</option>
              <option value="Critical">Critical (Red)</option>
            </select>
          </div>

        </div>

      </div>

      {/* Structured Log Grid List */}
      <div className="space-y-3">
        <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
          Operational Log Registry Stream ({filteredLogs.length} Records)
        </h4>

        <div className={`border rounded-xl divide-y overflow-hidden shadow-sm ${
          isDark ? 'bg-[#1d2027] border-slate-800 divide-slate-800' : 'bg-white border-slate-200 divide-slate-100'
        }`}>
          {filteredLogs.map((log) => {
            const isCrit = log.severity === 'Critical';
            const isWarning = log.severity === 'Warning';
            const isSuccess = log.severity === 'Success';

            return (
              <div key={log.id} className="p-4 flex flex-col md:flex-row md:items-start justify-between gap-3 text-xs leading-relaxed transition-all hover:bg-slate-50/40 dark:hover:bg-slate-800/10">
                <div className="space-y-1.5 flex-grow">
                  <div className="flex flex-wrap items-center gap-2">
                    {/* Timestamp */}
                    <span className="font-mono text-[10px] text-gray-400">{log.timestamp}</span>
                    
                    {/* Topic Indicator badge */}
                    <span className={`px-1.5 py-0.5 rounded text-[9px] uppercase font-bold tracking-wider ${
                      isDark ? 'bg-slate-800 text-gray-300' : 'bg-slate-100 text-slate-600'
                    }`}>
                      {log.topic}
                    </span>

                    {/* Severity badge */}
                    <span className={`px-1 rounded text-[9px] font-bold uppercase ${
                      isCrit 
                        ? 'bg-red-500/15 text-red-500' 
                        : isWarning 
                        ? 'bg-amber-500/15 text-amber-500' 
                        : isSuccess 
                        ? 'bg-emerald-500/15 text-emerald-500' 
                        : 'bg-blue-500/15 text-blue-500'
                    }`}>
                      {log.severity}
                    </span>

                    {/* Linked Sandbox */}
                    {log.sandboxLabel && (
                      <span className="text-[10px] font-sans font-semibold text-sky-500">
                        ({log.sandboxLabel})
                      </span>
                    )}
                  </div>
                  <h5 className={`font-bold ${isCrit ? 'text-red-500' : isWarning ? 'text-amber-500' : isDark ? 'text-white' : 'text-slate-800'}`}>
                    {log.title}
                  </h5>
                  <p className="text-[11px] text-gray-500 dark:text-gray-400 font-mono">
                    {log.details}
                  </p>
                </div>
              </div>
            );
          })}
        </div>
      </div>

    </div>
  );
}
