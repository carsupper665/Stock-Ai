import { useState } from 'react';
import {
  BookOpen,
  ChevronDown,
  Code,
  Copy,
  Shield,
  ShieldAlert,
  ShieldCheck
} from 'lucide-react';
import { Theme } from '../types';

interface AgentBoundaryViewProps {
  theme: Theme;
}

const tokenScopes = ['market:read', 'trade:write', 'trade:read', 'account:read'];

const allowedEndpoints = [
  'GET /sandbox/time',
  'GET /market/price',
  'GET /market/ticker',
  'GET /market/klines?to=<sandbox_current_time>',
  'GET /account',
  'GET /account/performance',
  'GET /orders',
  'GET /orders/:id',
  'GET /positions',
  'GET /trades',
  'POST /orders',
  'POST /orders/:id/cancel'
];

const forbiddenItems = [
  'strategy generation',
  'signal generation',
  'autonomous loops',
  'replay controls',
  'admin APIs',
  'dataset APIs',
  'future candles',
  'live execution',
  'backtest expansion'
];

export default function AgentBoundaryView({ theme }: AgentBoundaryViewProps) {
  const isDark = theme === 'dark';
  const [activeTriageStep, setActiveTriageStep] = useState<number | null>(0);

  const codeExample = `// Safe smoke-check: read sandbox time before market data
const sandboxTime = await client.get('/sandbox/time');
const candles = await client.get('/market/klines', {
  symbol: 'BTCUSDT',
  to: sandboxTime.current_time
});

console.log(candles.length); // Read-only backend smoke-check
`;

  const forbiddenExample = `// Blocked by sandbox boundary
await client.get('/market/klines', {
  symbol: 'BTCUSDT',
  to: 'future_timestamp_after_sandbox_time'
});

// Response: 403 future candles forbidden
`;

  const triageSteps = [
    {
      title: 'A1: 401 Unauthorized Handshake Failure',
      desc: 'Verify that the authorization token exists inside the Sandbox Accounts registry. Inactive or revoked sandbox account connection tokens return this code.'
    },
    {
      title: 'A2: 403 Forbidden - Boundary Override Blocked',
      desc: 'This occurs when a token calls forbidden scopes, requests future candles, touches admin APIs, or attempts live execution.'
    },
    {
      title: 'A3: 408 Simulation Timeout - Replay Stall',
      desc: 'Replay is currently paused or stopped by an admin operator. Confirm the target sandbox status and current sandbox time before retrying reads.'
    }
  ];

  return (
    <div className="p-6 space-y-6 max-w-4xl mx-auto pb-12 animate-fadeIn">
      <div className="flex items-start space-x-3 pb-4 border-b dark:border-slate-800">
        <Shield size={24} className="text-blue-500 flex-shrink-0 mt-1" />
        <div className="space-y-1">
          <h3 className={`text-sm font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
            Agent Boundary Ruleset
          </h3>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Demo rules for what sandbox account tokens may read or mutate. These cards document boundaries without adding live trading controls.
          </p>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="p-5 rounded-xl border border-blue-500/20 bg-blue-500/5 space-y-3 shadow-sm">
          <div className="flex items-center space-x-2 text-blue-500">
            <ShieldCheck size={16} />
            <h4 className="text-xs font-bold uppercase tracking-wider">Allowed Scope</h4>
          </div>
          <ul className="text-xs space-y-2.5 text-slate-700 dark:text-gray-300 list-disc list-inside leading-relaxed">
            <li><strong>Token Scopes:</strong> {tokenScopes.join(', ')}.</li>
            <li><strong>Allowed Account Actions:</strong> Read account state, list orders, list positions, list trades, submit sandbox orders, and cancel sandbox orders.</li>
            <li><strong>Sandbox Time Rule:</strong> Market candle reads must use to &lt;= sandbox current_time.</li>
            <li><strong>Safe Smoke Check:</strong> Read sandbox time, then request klines capped at that timestamp.</li>
          </ul>
        </div>

        <div className="p-5 rounded-xl border border-red-500/20 bg-red-400/5 space-y-3 shadow-sm">
          <div className="flex items-center space-x-2 text-red-500">
            <ShieldAlert size={16} />
            <h4 className="text-xs font-bold uppercase tracking-wider">Forbidden Scope</h4>
          </div>
          <ul className="text-xs space-y-2.5 text-slate-700 dark:text-gray-300 list-disc list-inside leading-relaxed">
            <li><strong>Blocked Categories:</strong> {forbiddenItems.join(', ')}.</li>
            <li><strong>No Live Execution:</strong> Live exchange controls, production keys, and admin endpoints are outside agent token scope.</li>
            <li><strong>No Future Data:</strong> Requests beyond sandbox current time are blocked and shown as boundary failures.</li>
          </ul>
        </div>
      </div>

      <div className={`p-5 rounded-xl border ${isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'}`}>
        <div className="flex items-center space-x-2 text-blue-500 mb-3">
          <BookOpen size={16} />
          <h4 className="text-xs font-bold uppercase tracking-wider">Allowed Endpoints</h4>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
          {allowedEndpoints.map((endpoint) => (
            <div key={endpoint} className="flex items-center justify-between gap-3 rounded-lg border border-blue-500/10 bg-blue-500/5 px-3 py-2 text-[11px] font-mono text-slate-700 dark:text-gray-300">
              <span>{endpoint}</span>
              <Copy size={11} className="text-blue-500" />
            </div>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="space-y-2">
          <div className="flex items-center space-x-1 px-1 text-slate-400">
            <Code size={13} className="text-blue-500" />
            <span className="text-[10px] font-bold uppercase">Safe Smoke-Check Example</span>
          </div>
          <pre className={`p-3 rounded-lg border text-[10px] font-mono leading-relaxed overflow-x-auto ${
            isDark ? 'bg-[#15171c] border-slate-800 text-gray-300' : 'bg-slate-50 border-slate-200 text-slate-700'
          }`}>
            {codeExample}
          </pre>
        </div>

        <div className="space-y-2">
          <div className="flex items-center space-x-1 px-1 text-slate-400">
            <Code size={13} className="text-red-500" />
            <span className="text-[10px] font-bold uppercase">Disallowed Future Data Attempt</span>
          </div>
          <pre className={`p-3 rounded-lg border text-[10px] font-mono leading-relaxed overflow-x-auto ${
            isDark ? 'bg-[#15171c] border-slate-800 text-red-400' : 'bg-slate-50 border-slate-200 text-red-600'
          }`}>
            {forbiddenExample}
          </pre>
        </div>
      </div>

      <div className="space-y-3">
        <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
          Failure Triage
        </h4>
        <div className={`border rounded-xl divide-y ${
          isDark ? 'bg-[#1d2027] border-slate-800 divide-slate-800' : 'bg-white border-slate-200 divide-slate-100'
        }`}>
          {triageSteps.map((step, index) => {
            const isOpen = activeTriageStep === index;
            return (
              <div key={step.title} className="p-3 text-xs">
                <button
                  type="button"
                  onClick={() => setActiveTriageStep(isOpen ? null : index)}
                  className="w-full text-left font-bold flex items-center justify-between py-1 hover:text-blue-500 transition-colors"
                >
                  <span>{step.title}</span>
                  <ChevronDown size={14} className={`transform transition-transform ${isOpen ? 'rotate-180' : ''}`} />
                </button>
                {isOpen && (
                  <p className="mt-2 text-gray-400 leading-relaxed font-sans border-t pt-2 dark:border-slate-800/60 pl-2">
                    {step.desc}
                  </p>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
