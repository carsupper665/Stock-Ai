import React, { useState } from 'react';
import { Lock, LogIn, ShieldCheck, Terminal, AlertCircle } from 'lucide-react';
import { Theme } from '../types';

interface AuthDemoProps {
  theme: Theme;
  onLoginSuccess: () => void;
}

export default function AuthDemo({ theme, onLoginSuccess }: AuthDemoProps) {
  const isDark = theme === 'dark';
  const [username, setUsername] = useState('ops_admin_ref');
  const [password, setPassword] = useState('••••••••••••');
  const [submitting, setSubmitting] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setErrorText(null);

    // Simulate standard verification timeout
    setTimeout(() => {
      setSubmitting(false);
      if (username.trim() === '') {
        setErrorText('Operator username cannot be blank.');
      } else {
        onLoginSuccess();
      }
    }, 700);
  };

  return (
    <div
      className={`min-h-[80vh] flex flex-col justify-center items-center p-6 ${
        isDark ? 'bg-[#14161a]' : 'bg-slate-50'
      }`}
    >
      <div className="w-full max-w-sm">
        {/* Top Product Logo / Header */}
        <div className="text-center mb-6">
          <div className="inline-flex p-3 rounded-xl bg-blue-600/10 text-blue-500 mb-3 border border-blue-500/20">
            <Terminal size={28} />
          </div>
          <h2 className={`text-xl font-bold tracking-tight ${isDark ? 'text-white' : 'text-slate-900'}`}>
            Sandbox Operations Console
          </h2>
          <p className={`text-xs mt-1 ${isDark ? 'text-gray-400' : 'text-slate-500'}`}>
            Operator Authentication & Policy Boundary Portal
          </p>
        </div>

        {/* Unified Login Card */}
        <div
          className={`border p-6 rounded-xl shadow-md ${
            isDark ? 'bg-[#1d2026] border-slate-800' : 'bg-white border-slate-200'
          }`}
        >
          <div className="flex items-center space-x-2 pb-4 mb-4 border-b border-dashed border-slate-200 dark:border-slate-800">
            <Lock size={15} className="text-blue-500" />
            <span className={`text-xs font-semibold ${isDark ? 'text-gray-200' : 'text-slate-700'}`}>
              Admin Control Lockout
            </span>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label
                className={`block text-[10px] font-semibold uppercase tracking-wider mb-1.5 ${
                  isDark ? 'text-gray-400' : 'text-slate-500'
                }`}
              >
                Operator Username
              </label>
              <input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="e.g. ops_manager"
                className={`w-full p-2.5 rounded-lg text-xs font-mono border transition-all ${
                  isDark
                    ? 'bg-slate-900/50 border-slate-800 text-white focus:border-blue-500 focus:outline-none'
                    : 'bg-white border-slate-200 text-slate-800 focus:border-blue-500 focus:outline-none'
                }`}
              />
            </div>

            <div>
              <label
                className={`block text-[10px] font-semibold uppercase tracking-wider mb-1.5 ${
                  isDark ? 'text-gray-400' : 'text-slate-500'
                }`}
              >
                Security Access Token / Password
              </label>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className={`w-full p-2.5 rounded-lg text-xs font-mono border transition-all ${
                  isDark
                    ? 'bg-slate-900/50 border-slate-800 text-white focus:border-blue-500 focus:outline-none'
                    : 'bg-white border-slate-200 text-slate-800 focus:border-blue-500 focus:outline-none'
                }`}
              />
            </div>

            {errorText && (
              <div className="p-2.5 rounded-lg bg-red-500/10 border border-red-500/25 flex items-start space-x-2 text-[11px] text-red-500">
                <AlertCircle size={14} className="flex-shrink-0 mt-0.5" />
                <span>{errorText}</span>
              </div>
            )}

            <button
              type="submit"
              disabled={submitting}
              className={`w-full py-2.5 p-2 rounded-lg text-xs font-semibold flex items-center justify-center space-x-2 transition-all cursor-pointer ${
                submitting
                  ? 'bg-blue-600/50 text-white/55 cursor-not-allowed'
                  : 'bg-blue-600 text-white hover:bg-blue-500 shadow-sm'
              }`}
            >
              {submitting ? (
                <>
                  <div className="w-3.5 h-3.5 border-2 border-white border-t-transparent rounded-full animate-spin"></div>
                  <span>Verifying Credentials...</span>
                </>
              ) : (
                <>
                  <LogIn size={14} />
                  <span>Authenticate Operator Session</span>
                </>
              )}
            </button>
          </form>

          {/* Secure Policy Disclaimer */}
          <div className={`mt-5 pt-4 border-t text-[10px] leading-relaxed ${isDark ? 'border-slate-800 text-gray-500' : 'border-slate-100 text-slate-400'}`}>
            <p className="flex items-center space-x-1.5 mb-1 text-sky-500 font-semibold">
              <ShieldCheck size={11} />
              <span>Broker Kernel Level-4 Security</span>
            </p>
            This portal grants safe replay execution access to simulated trading boundaries. Live market parameters require active hardware signing.
          </div>
        </div>

        {/* Return helper link */}
        <div className="text-center mt-5">
          <p className={`text-[11px] ${isDark ? 'text-gray-500' : 'text-slate-400'}`}>
            Demo Credentials Pre-configured. Click button to unlock dashboard.
          </p>
        </div>
      </div>
    </div>
  );
}
