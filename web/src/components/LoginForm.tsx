import React, { useState } from 'react';
import { Lock, LogIn, ShieldCheck, Terminal, AlertCircle } from 'lucide-react';
import { Theme } from '../types';

interface LoginFormProps {
  theme: Theme;
  onLogin: (username: string, password: string) => Promise<void>;
}

export default function LoginForm({ theme, onLogin }: LoginFormProps) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setErrorText(null);

    try {
      await onLogin(username, password);
    } catch (error) {
      setErrorText(error instanceof Error ? error.message : 'Unable to authenticate operator session.');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      data-auth-theme={theme}
      className="min-h-[80vh] flex flex-col justify-center items-center p-6 bg-app-bg"
    >
      <div className="w-full max-w-sm">
        {/* Top Product Logo / Header */}
        <div className="text-center mb-6">
          <div className="inline-flex p-3 rounded-xl bg-app-accent/10 text-app-accent mb-3 border border-app-accent/20">
            <Terminal size={28} />
          </div>
          <h2 className="text-xl font-bold tracking-tight text-app-text-main">
            Sandbox Operations Console
          </h2>
          <p className="text-xs mt-1 text-app-text-muted">
            Operator Authentication & Policy Boundary Portal
          </p>
        </div>

        {/* Unified Login Card */}
        <div
          className="border p-6 rounded-xl shadow-md bg-app-card border-app-border"
        >
          <div className="flex items-center space-x-2 pb-4 mb-4 border-b border-dashed border-app-border">
            <Lock size={15} className="text-app-accent" />
            <span className="text-xs font-semibold text-app-text-main">
              Admin Control Lockout
            </span>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label
                htmlFor="admin-username"
                className="block text-[10px] font-semibold uppercase tracking-wider mb-1.5 text-app-text-muted"
              >
                Operator Username
              </label>
              <input
                type="text"
                id="admin-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="e.g. ops_manager"
                autoComplete="username"
                className="w-full p-2.5 rounded-lg text-xs font-mono border transition-all bg-app-bg border-app-border text-app-text-main focus:border-app-accent focus:outline-none"
              />
            </div>

            <div>
              <label
                htmlFor="admin-password"
                className="block text-[10px] font-semibold uppercase tracking-wider mb-1.5 text-app-text-muted"
              >
                Security Access Token / Password
              </label>
              <input
                type="password"
                id="admin-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                className="w-full p-2.5 rounded-lg text-xs font-mono border transition-all bg-app-bg border-app-border text-app-text-main focus:border-app-accent focus:outline-none"
              />
            </div>

            {errorText && (
              <div className="p-2.5 rounded-lg bg-app-error/10 border border-app-error/25 flex items-start space-x-2 text-[11px] text-app-error">
                <AlertCircle size={14} className="flex-shrink-0 mt-0.5" />
                <span>{errorText}</span>
              </div>
            )}

            <button
              type="submit"
              disabled={submitting}
              className={`w-full py-2.5 p-2 rounded-lg text-xs font-semibold flex items-center justify-center space-x-2 transition-all cursor-pointer ${
                submitting
                  ? 'bg-app-accent/50 text-white/55 cursor-not-allowed'
                  : 'bg-app-accent text-white hover:bg-app-accent/85 shadow-sm'
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
          <div className="mt-5 pt-4 border-t text-[10px] leading-relaxed border-app-border text-app-text-muted">
            <p className="flex items-center space-x-1.5 mb-1 text-app-accent font-semibold">
              <ShieldCheck size={11} />
              <span>Sandbox Console Security</span>
            </p>
            This portal grants authenticated access to replay and sandbox monitoring surfaces only. Live execution controls are intentionally unavailable.
          </div>
        </div>

        <div className="text-center mt-5">
          <p className="text-[11px] text-app-text-muted">
            Enter backend admin credentials to unlock the operations dashboard.
          </p>
        </div>
      </div>
    </div>
  );
}
