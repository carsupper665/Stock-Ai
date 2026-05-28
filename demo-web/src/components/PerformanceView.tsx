import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer
} from 'recharts';
import { Trophy, TrendingUp, Info, Award, Percent, TrendingDown } from 'lucide-react';
import { SandboxAccount, Theme, DemoVisualState } from '../types';

interface PerformanceViewProps {
  theme: Theme;
  demoState: DemoVisualState;
  accounts: SandboxAccount[];
}

export default function PerformanceView({
  theme,
  demoState,
  accounts
}: PerformanceViewProps) {
  const isDark = theme === 'dark';

  // Sort accounts by equity to formulate ranks
  const rankedAccounts = [...accounts]
    .filter((a) => !a.tokenRevoked)
    .sort((a, b) => b.equity - a.equity);

  // Formulate data for the comparative bar chart
  const barChartData = rankedAccounts.map((a) => {
    const rate = ((a.equity - a.balance) / a.balance) * 100;
    return {
      name: a.name.split(' ').slice(1).join(' ') || a.name, // Shorten name
      pnlPercent: parseFloat(rate.toFixed(2)),
      equityVal: a.equity
    };
  });

  // KPI constants
  const peakAccount = rankedAccounts[0];
  const peakROI = peakAccount ? ((peakAccount.equity - peakAccount.balance) / peakAccount.balance) * 100 : 0;

  if (demoState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 bg-slate-200 dark:bg-slate-700 w-1/4 rounded"></div>
        <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* KPI Overviews */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        
        <div className={`p-4 rounded-xl border ${isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'}`}>
          <div className="flex items-center space-x-1.5 text-blue-500 mb-2">
            <Trophy size={14} />
            <span className="text-[10px] uppercase font-bold tracking-wider text-slate-400">Top Sandbox Account</span>
          </div>
          <p className="text-xl font-bold">{peakAccount ? peakAccount.name : 'None Listed'}</p>
          <span className="text-[10px] text-emerald-500 font-semibold font-mono">
            {peakROI >= 0 ? '+' : ''}{peakROI.toFixed(2)}% ROI Sim
          </span>
        </div>

        <div className={`p-4 rounded-xl border ${isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'}`}>
          <div className="flex items-center space-x-1.5 text-sky-500 mb-2">
            <Percent size={14} />
            <span className="text-[10px] uppercase font-bold tracking-wider text-slate-400">Mean Strategy Drawdown</span>
          </div>
          <p className="text-xl font-bold font-mono">0.45% Drawdown</p>
          <span className="text-[10px] text-gray-500">Extremely safe simulation boundaries</span>
        </div>

        <div className={`p-4 rounded-xl border ${isDark ? 'bg-[#1b1e25] border-slate-800 text-white' : 'bg-white border-slate-100 text-slate-800'}`}>
          <div className="flex items-center space-x-1.5 text-amber-500 mb-2">
            <TrendingDown size={14} />
            <span className="text-[10px] uppercase font-bold tracking-wider text-slate-400">Risk Assessment Zone</span>
          </div>
          <p className="text-xl font-bold uppercase text-slate-400 font-mono">Sim-Only Reference</p>
          <span className="text-[10px] text-slate-500 leading-snug">No live capital risk parameters configured</span>
        </div>

      </div>

      {/* Main performance grids */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">

        {/* Bar comparison chart using recharts */}
        <div className="lg:col-span-2 space-y-4">
          <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
            Simulator ROI Comparison Percentage (%)
          </h4>

          <div className={`border p-4 rounded-xl h-64 ${
            isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
          }`}>
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={barChartData}>
                <CartesianGrid strokeDasharray="3 3" stroke={isDark ? '#334155' : '#f1f5f9'} />
                <XAxis dataKey="name" stroke={isDark ? '#94a3b8' : '#64748b'} />
                <YAxis stroke={isDark ? '#94a3b8' : '#64748b'} />
                <Tooltip
                  contentStyle={{
                    backgroundColor: isDark ? '#1e293b' : '#ffffff',
                    borderColor: isDark ? '#475569' : '#cbd5e1',
                    borderRadius: '8px'
                  }}
                />
                <Bar dataKey="pnlPercent" fill="#2563eb" radius={[4, 4, 0, 0]} name="Sim ROI %" />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </div>

        {/* Ranked Leaderboard table list */}
        <div className="space-y-4">
          <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
            Sandbox Ranking Leaderboard
          </h4>

          <div className={`border rounded-xl overflow-hidden shadow-sm text-xs ${
            isDark ? 'bg-[#1d2027] border-slate-800 text-gray-200' : 'bg-white border-slate-200 text-slate-700'
          }`}>
            <div className="divide-y divide-slate-100 dark:divide-slate-800/60">
              {rankedAccounts.map((acc, index) => {
                const isWinner = index === 0;
                const roi = ((acc.equity - acc.balance) / acc.balance) * 100;

                return (
                  <div key={acc.id} className="p-3.5 flex items-center justify-between hover:bg-slate-50/50 dark:hover:bg-slate-800/20">
                    <div className="flex items-center space-x-3">
                      <span className={`w-5 h-5 rounded-full flex items-center justify-center font-bold text-[10px] ${
                        isWinner 
                          ? 'bg-amber-500/15 text-amber-500 border border-amber-500/30' 
                          : 'bg-slate-100 dark:bg-slate-800'
                      }`}>
                        {index + 1}
                      </span>
                      <div>
                        <p className="font-semibold">{acc.name}</p>
                        <p className="text-[10px] text-gray-500 font-sans">{acc.sandboxName.split(' ')[0]} Sandbox</p>
                      </div>
                    </div>

                    <div className="text-right font-mono text-[11px]">
                      <p className="font-bold">${acc.equity.toLocaleString()}</p>
                      <p className={`font-semibold ${roi >= 0 ? 'text-emerald-500' : 'text-red-500'}`}>
                        {roi >= 0 ? '+' : ''}{roi.toFixed(2)}% ROI
                      </p>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        </div>

      </div>

    </div>
  );
}
