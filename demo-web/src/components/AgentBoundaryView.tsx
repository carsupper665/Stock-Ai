import { useState } from 'react';
import {
  Shield,
  ShieldAlert,
  ShieldCheck,
  Code,
  Clock,
  BookOpen,
  Terminal,
  ChevronDown,
  Info
} from 'lucide-react';
import { Theme } from '../types';

interface AgentBoundaryViewProps {
  theme: Theme;
}

export default function AgentBoundaryView({ theme }: AgentBoundaryViewProps) {
  const isDark = theme === 'dark';
  const [activeTriageStep, setActiveTriageStep] = useState<number | null>(0);

  const codeExample = `// Example of permitted connection handshake query
const connection = new SandboxClient({
  endpoint: 'internal://sandbox-kernel-01',
  accessToken: process.env.SANDBOX_TOKEN, // Safe
  marginLimitPct: 0.10 // Limit parameter check
});

// Handshake verifies policies
const session = await connection.handshake({
  symbol: "BTCUSDT",
  allowedModes: ["Replay", "Mock"]
});

console.log("Session verified under rule node #A1-9");
`;

  const forbiddenExample = `// ❌ THIS ACTION WILL BE BLOCKED BY THE KERNEL WITH 403 FORBIDDEN
const action = await connection.requestWithdrawal({
  recipient: "0xFE8A...",
  amount: 25.40,
  network: "Live ERC20" 
});
// Response: Error Code: EX_BOUNDARY_VIOLATION
`;

  const triageSteps = [
    {
      title: 'A1: 401 Unauthorized Handshake Failure',
      desc: 'Verify that the authorization token exists inside the Sandbox Accounts registry. Inactive or revoked sandbox account connection tokens instantly return this code.'
    },
    {
      title: 'A2: 403 Forbidden - Boundary Override Blocked',
      desc: 'This occurs when an autonomous agent attempts a transaction exceeding margin limits (>10%), attempts multiple symbols simultaneously without dual scopes, or invokes any financial withdrawal triggers.'
    },
    {
      title: 'A3: 408 Simulation Timeout - Replay Stall',
      desc: 'Replay is currently paused or stopped by an admin operator. Ensure that the target sandbox status is set to "Running" and the timeline slider has remaining time indices.'
    }
  ];

  return (
    <div className="p-6 space-y-6 max-w-4xl mx-auto pb-12 animate-fadeIn">

      {/* Intro Header */}
      <div className="flex items-start space-x-3 pb-4 border-b dark:border-slate-800">
        <Shield size={24} className="text-blue-500 flex-shrink-0 mt-1" />
        <div className="space-y-1">
          <h3 className={`text-sm font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
            Boundary Agent Ruleset Specification (BAR-4)
          </h3>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Governing rulesets enforced natively within the sandbox system core. Rules are immutable for all child connection tokens.
          </p>
        </div>
      </div>

      {/* Dual Column allowed vs forbidden layout card */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">

        {/* Permitted card */}
        <div className="p-5 rounded-xl border border-emerald-500/20 bg-emerald-500/5 space-y-3 shadow-sm">
          <div className="flex items-center space-x-2 text-emerald-500">
            <ShieldCheck size={16} />
            <h4 className="text-xs font-bold uppercase tracking-wider">Allowed Sandbox Scopes</h4>
          </div>

          <ul className="text-xs space-y-2.5 text-slate-700 dark:text-gray-300 list-disc list-inside leading-relaxed">
            <li>
              <strong>Virtual Placements:</strong> Access to place mock Limit and Market orders against loaded datasets (e.g. BTCUSDT, ETHUSDT).
            </li>
            <li>
              <strong>Equity Tracking:</strong> Read balance, active positions, and margin records inside the target sandbox node.
            </li>
            <li>
              <strong>Simulator Ingestion:</strong> Sync strategy updates matching playback speeds from 1x up to 50x rate.
            </li>
            <li>
              <strong>Heartbeats:</strong> Request operational health metrics or system symbol latency checks.
            </li>
          </ul>
        </div>

        {/* Forbidden card */}
        <div className="p-5 rounded-xl border border-red-500/20 bg-red-400/5 space-y-3 shadow-sm">
          <div className="flex items-center space-x-2 text-red-500">
            <ShieldAlert size={16} />
            <h4 className="text-xs font-bold uppercase tracking-wider">Blocked Boundary Handbrakes</h4>
          </div>

          <ul className="text-xs space-y-2.5 text-slate-700 dark:text-gray-300 list-disc list-inside leading-relaxed">
            <li>
              <strong>Withdrawals Blocked:</strong> Any function requesting block transfers, crypto blockchain registers, or monetary outputs is strictly forbidden.
            </li>
            <li>
              <strong>No Production Access:</strong> Direct interactions with live exchange tickers or live trading keys are systematically isolated.
            </li>
            <li>
              <strong>Excessive Leverage:</strong> Simulation margin usage approaching over 10.0% is flagged with a warning banner immediately.
            </li>
          </ul>
        </div>

      </div>

      {/* Code illustrations */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">

        {/* Allowed Code */}
        <div className="space-y-2">
          <div className="flex items-center space-x-1 px-1 text-slate-400">
            <Code size={13} className="text-emerald-500" />
            <span className="text-[10px] font-bold uppercase">Safe Handshake Example</span>
          </div>
          <pre className={`p-3 rounded-lg border text-[10px] font-mono leading-relaxed overflow-x-auto ${
            isDark ? 'bg-[#15171c] border-slate-800 text-gray-300' : 'bg-slate-50 border-slate-200 text-slate-700'
          }`}>
            {codeExample}
          </pre>
        </div>

        {/* Forbidden Code */}
        <div className="space-y-2">
          <div className="flex items-center space-x-1 px-1 text-slate-400">
            <Code size={13} className="text-red-500" />
            <span className="text-[10px] font-bold uppercase">Disallowed Breach Attempt</span>
          </div>
          <pre className={`p-3 rounded-lg border text-[10px] font-mono leading-relaxed overflow-x-auto ${
            isDark ? 'bg-[#15171c] border-slate-800 text-red-400' : 'bg-slate-50 border-slate-200 text-red-600'
          }`}>
            {forbiddenExample}
          </pre>
        </div>

      </div>

      {/* Failure Triage accordion */}
      <div className="space-y-3">
        <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
          Failure Triage Flowchart
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
