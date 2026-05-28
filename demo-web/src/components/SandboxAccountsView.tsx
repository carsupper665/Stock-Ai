import React, { useState } from 'react';
import {
  Users,
  Copy,
  Check,
  AlertTriangle,
  Trash2,
  Lock,
  Plus,
  Compass,
  Key,
  Database,
  ArrowRightLeft
} from 'lucide-react';
import { SandboxAccount, Theme, DemoVisualState } from '../types';

interface SandboxAccountsViewProps {
  theme: Theme;
  demoState: DemoVisualState;
  accounts: SandboxAccount[];
  onAddAccount: (acc: SandboxAccount) => void;
  onUpdateAccount: (acc: SandboxAccount) => void;
  sandboxes: { id: string; name: string }[];
}

export default function SandboxAccountsView({
  theme,
  demoState,
  accounts,
  onAddAccount,
  onUpdateAccount,
  sandboxes
}: SandboxAccountsViewProps) {
  const isDark = theme === 'dark';
  
  // Interactive view state variables
  const [showForm, setShowForm] = useState(false);
  const [activeTokenReveal, setActiveTokenReveal] = useState<SandboxAccount | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [revokeConfirmationId, setRevokeConfirmationId] = useState<string | null>(null);

  // Form states
  const [accName, setAccName] = useState('Delta HFT Highspeed');
  const [accBalance, setAccBalance] = useState('100000.00');
  const [targetSandbox, setTargetSandbox] = useState(sandboxes[0]?.id || 'sb-01');

  // Trigger copy feedback
  const handleCopy = (text: string, id: string) => {
    navigator.clipboard.writeText(text);
    setCopiedId(id);
    setTimeout(() => setCopiedId(null), 1500);
  };

  // Submit new Account
  const handleProvision = (e: React.FormEvent) => {
    e.preventDefault();
    const sbObj = sandboxes.find((s) => s.id === targetSandbox);
    const newAcc: SandboxAccount = {
      id: `acc-${Date.now().toString().slice(-4)}`,
      name: accName,
      sandboxId: targetSandbox,
      sandboxName: sbObj ? sbObj.name : 'Unknown',
      balance: parseFloat(accBalance) || 10000.00,
      equity: parseFloat(accBalance) || 10000.00,
      margin: 0.00,
      status: 'Active',
      token: `sb_tok_${Math.random().toString(36).substr(2, 14).toUpperCase()}`,
      tokenRevealed: false,
      updated: 'Just now'
    };
    onAddAccount(newAcc);
    setShowForm(false);
    
    // Auto-open secret token warning panel for user verification
    setActiveTokenReveal(newAcc);
  };

  // Turn off or revoke a token
  const handleRevokeToken = (id: string) => {
    const acc = accounts.find((a) => a.id === id);
    if (acc) {
      const updated = {
        ...acc,
        token: undefined,
        tokenRevoked: true,
        status: 'Inactive' as const
      };
      onUpdateAccount(updated);
    }
    setRevokeConfirmationId(null);
  };

  const handleRevealToken = (acc: SandboxAccount) => {
    setActiveTokenReveal(acc);
  };

  // Stats
  const totalBalance = accounts.reduce((sum, a) => sum + (a.tokenRevoked ? 0 : a.balance), 0);
  const activeKeys = accounts.filter((a) => a.token && !a.tokenRevoked).length;

  if (demoState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/3 bg-slate-300 dark:bg-slate-700 rounded"></div>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          <div className="h-24 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
          <div className="h-24 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
          <div className="h-24 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
        </div>
        <div className="h-48 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (demoState === 'error') {
    return (
      <div className="p-6 text-center max-w-md mx-auto mt-12 bg-red-500/5 border border-red-500/20 rounded-xl p-8">
        <AlertTriangle size={32} className="mx-auto text-red-500 mb-3" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Token Decryption Blocked</h3>
        <p className="text-xs text-slate-500 mt-1 mb-4">
          Kernel Security isolated decryption pipeline. Host signatures failed workspace integrity inspection.
        </p>
        <button className="px-3 py-1.5 bg-red-600 text-white rounded text-xs font-semibold hover:bg-red-500">
          Reset Integrity Signatures
        </button>
      </div>
    );
  }

  if (demoState === 'empty' || accounts.length === 0) {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12">
        <Users size={32} className="mx-auto text-slate-400 mb-2 animate-bounce" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>No accounts provisioned</h3>
        <p className="text-xs text-gray-500 mt-1 mb-4">
          Broker simulators require safe virtual accounting entries to book trades.
        </p>
        <button
          onClick={() => setShowForm(true)}
          className="px-3.5 py-1.5 bg-blue-600 text-white rounded text-xs font-semibold hover:bg-blue-500"
        >
          Provision Virtual Account
        </button>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Metrics Header */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        
        <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Total Virtual Accounts</p>
          <p className="text-2xl font-bold tracking-tight mt-1">{accounts.length}</p>
          <span className="text-[10px] text-app-text-muted">Divided across active sandboxes</span>
        </div>

        <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Total Virtual Capital</p>
          <p className="text-2xl font-bold tracking-tight text-app-accent mt-1">
            ${totalBalance.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
          </p>
          <span className="text-[10px] text-app-text-muted">Replay assets (USD parity test balance)</span>
        </div>

        <div className="p-5 rounded-xl border bg-app-card border-app-border text-app-text-main">
          <p className="text-[10px] uppercase font-bold tracking-wider text-app-text-muted">Authorized Agent Keys</p>
          <p className="text-2xl font-bold tracking-tight text-app-success mt-1">
            {activeKeys} <span className="text-xs text-app-text-muted font-normal">Active Tokens</span>
          </p>
          <span className="text-[10px] text-app-text-muted">Restricted boundary tokens running</span>
        </div>

      </div>

      {/* Main layout */}
      <div className="flex flex-col xl:flex-row gap-6">
        
        {/* Core accounts tables */}
        <div className="flex-grow space-y-4">
          <div className="flex justify-between items-center">
            <h3 className={`text-sm font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Virtual Account Directory
            </h3>
            <button
              onClick={() => setShowForm(!showForm)}
              className="px-3 py-1.5 rounded-lg bg-app-accent text-white text-xs font-semibold flex items-center space-x-1 cursor-pointer hover:bg-app-accent/90 transition-colors"
            >
              <Plus size={13} />
              <span>Provision Account</span>
            </button>
          </div>

          <div className="border rounded-xl spill-x-auto overflow-hidden shadow-sm bg-app-card border-app-border text-app-text-main">
            <div className="overflow-x-auto animate-fadeIn">
              <table className="w-full text-left border-collapse text-xs">
                <thead>
                  <tr className="border-b text-[10px] uppercase font-bold tracking-wider bg-app-bg/50 border-app-border text-app-text-muted">
                    <th className="p-3">Account Alias</th>
                    <th className="p-3">Attached Sandbox</th>
                    <th className="p-3 text-right">Balance (USD)</th>
                    <th className="p-3 text-right">Equity (USD)</th>
                    <th className="p-3 text-right">Margin Level</th>
                    <th className="p-3">Connection Token</th>
                    <th className="p-3">Account Status</th>
                    <th className="p-3">Updated</th>
                    <th className="p-3 text-right">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
                  {accounts.map((acc) => (
                    <tr key={acc.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30 transition-colors">
                      <td className="p-3 font-semibold text-slate-900 dark:text-gray-100 flex items-center space-x-2">
                        <Users size={12} className="text-gray-400" />
                        <span>{acc.name}</span>
                      </td>
                      <td className="p-3 text-sky-500 font-medium">{acc.sandboxName}</td>
                      <td className="p-3 font-mono text-right text-slate-700 dark:text-gray-300">
                        ${acc.balance.toLocaleString(undefined, { minimumFractionDigits: 2 })}
                      </td>
                      <td className="p-3 font-mono text-right font-medium dark:text-white">
                        ${acc.equity.toLocaleString(undefined, { minimumFractionDigits: 2 })}
                      </td>
                      <td className="p-3 font-mono text-right">
                        <span className={`px-1 rounded ${
                          acc.margin > 1500 ? 'text-amber-500 font-semibold' : 'text-gray-500 dark:text-gray-400'
                        }`}>
                          ${acc.margin.toLocaleString()}
                        </span>
                      </td>
                      <td className="p-3 font-mono text-[11px]">
                        {acc.tokenRevoked ? (
                          <span className="text-red-500 flex items-center space-x-1">
                            <span>Revoked</span>
                          </span>
                        ) : acc.token ? (
                          <div className="flex items-center space-x-1.5">
                            <span className="text-gray-400">•••••••••••</span>
                            <button
                              onClick={() => handleRevealToken(acc)}
                              className="text-[10px] text-blue-500 hover:underline cursor-pointer"
                            >
                              Show
                            </button>
                          </div>
                        ) : (
                          <span className="text-gray-400">No Key Issued</span>
                        )}
                      </td>
                      <td className="p-3">
                        <span className={`px-2 py-0.5 rounded pill text-[10px] font-bold ${
                          acc.status === 'Active'
                            ? 'bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
                            : 'bg-slate-500/10 text-slate-400'
                        }`}>
                          {acc.status}
                        </span>
                      </td>
                      <td className="p-3 text-gray-400 text-[10px]">{acc.updated}</td>
                      <td className="p-3 text-right">
                        {acc.token && !acc.tokenRevoked && (
                          <button
                            onClick={() => setRevokeConfirmationId(acc.id)}
                            className="text-red-500 hover:text-red-600 p-1 rounded hover:bg-red-500/10 inline-flex items-center"
                            title="Revoke Token Access"
                          >
                            <Trash2 size={13} />
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>

        {/* Dynamic Warning Card / One-Time Reveal Panel Demo */}
        {(activeTokenReveal || showForm || revokeConfirmationId) && (
          <div className="w-full xl:w-80 flex-shrink-0 space-y-4">
            
            {/* Form Creator */}
            {showForm && (
              <div className="p-5 rounded-xl border shadow-sm bg-app-card border-app-border text-app-text-main">
                <div className="flex items-center justify-between pb-2 mb-3 border-b border-slate-200 dark:border-slate-800">
                  <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
                    Provision Account
                  </h4>
                  <button onClick={() => setShowForm(false)} className="text-[10px] text-slate-400 hover:text-black dark:hover:text-white">
                    CANCEL
                  </button>
                </div>
                <form onSubmit={handleProvision} className="space-y-3">
                  <div>
                    <label className="block text-[10px] font-semibold text-gray-400 uppercase">Account Label</label>
                    <input
                      type="text"
                      value={accName}
                      onChange={(e) => setAccName(e.target.value)}
                      className="w-full p-2 border text-xs rounded font-mono bg-app-bg border-app-border text-app-text-main focus:outline-hidden focus:ring-1 focus:ring-app-accent"
                      required
                    />
                  </div>
                  <div>
                    <label className="block text-[10px] font-semibold text-gray-400 uppercase">Interactive Sandbox Target</label>
                    <select
                      value={targetSandbox}
                      onChange={(e) => setTargetSandbox(e.target.value)}
                      className="w-full p-2 border text-xs rounded bg-app-bg border-app-border text-app-text-main focus:outline-hidden focus:ring-1 focus:ring-app-accent"
                    >
                      {sandboxes.map((s) => (
                        <option key={s.id} value={s.id}>{s.name}</option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="block text-[10px] font-semibold text-gray-400 uppercase">Simulated Balance (USD)</label>
                    <input
                      type="number"
                      value={accBalance}
                      onChange={(e) => setAccBalance(e.target.value)}
                      className="w-full p-2 border text-xs rounded font-mono bg-app-bg border-app-border text-app-text-main focus:outline-hidden focus:ring-1 focus:ring-app-accent"
                    />
                  </div>
                  <button
                    type="submit"
                    className="w-full py-2 bg-app-accent text-white text-xs font-semibold rounded cursor-pointer hover:bg-app-accent/90 transition-colors"
                  >
                    Generate Virtual Credentials
                  </button>
                </form>
              </div>
            )}

            {/* Token Reveal Panel Detail */}
            {activeTokenReveal && (
              <div className="p-5 rounded-xl border border-blue-500/20 bg-blue-500/5 shadow-md space-y-4 animate-fadeIn">
                <div className="flex items-center space-x-1.5 text-blue-500">
                  <Key size={16} />
                  <h4 className="text-xs font-bold uppercase tracking-wider">One-Time Secret Token</h4>
                </div>
                
                <div className="p-3 rounded-lg border border-amber-500/20 bg-amber-500/5 text-amber-500 text-[10px] leading-relaxed">
                  <div className="flex items-start space-x-1.5">
                    <AlertTriangle size={14} className="flex-shrink-0 mt-0.5" />
                    <span>
                      <strong>SECURITY WARNING:</strong> This secret key will not be readable again. Store it securely or use with the agent terminal ruleset engine immediately.
                    </span>
                  </div>
                </div>

                <div className="space-y-1">
                  <p className="text-[10px] font-semibold uppercase text-gray-400">Owner Account:</p>
                  <p className="text-xs font-mono font-bold text-slate-800 dark:text-gray-100">{activeTokenReveal.name}</p>
                </div>

                <div className="space-y-1">
                  <p className="text-[10px] font-semibold uppercase text-gray-400 text-sky-500">SECRET CONNECTION TOKEN:</p>
                  <div className="p-2 rounded font-mono text-[11px] break-all border flex items-center justify-between bg-app-bg border-app-border text-app-success">
                    <span>{activeTokenReveal.token}</span>
                    <button
                      onClick={() => handleCopy(activeTokenReveal.token || '', activeTokenReveal.id)}
                      className="p-1 rounded hover:bg-slate-200 dark:hover:bg-slate-800 ml-1.5 text-gray-400 cursor-pointer"
                      title="Copy Key Buffer"
                    >
                      {copiedId === activeTokenReveal.id ? <Check size={12} className="text-green-500" /> : <Copy size={12} />}
                    </button>
                  </div>
                  {copiedId === activeTokenReveal.id && (
                    <span className="text-[10px] text-emerald-500 font-semibold animate-pulse block">
                      Copied! Token written to clip.
                    </span>
                  )}
                </div>

                <button
                  onClick={() => setActiveTokenReveal(null)}
                  className="w-full py-1.5 bg-slate-200 hover:bg-slate-300 dark:bg-slate-800 dark:hover:bg-slate-700 text-slate-700 dark:text-gray-300 text-xs font-semibold rounded cursor-pointer transition-colors text-center block"
                >
                  Close Key Portal
                </button>
              </div>
            )}

            {/* Revoke confirmation block */}
            {revokeConfirmationId && (
              <div className="p-5 rounded-xl border border-red-500/25 bg-red-500/5 shadow-md space-y-4 animate-slideIn">
                <div className="flex items-center space-x-1.5 text-red-500">
                  <AlertTriangle size={15} />
                  <h4 className="text-xs font-bold uppercase tracking-wider">Revoke Confirmation</h4>
                </div>

                <p className="text-xs text-slate-500 leading-relaxed">
                  Are you absolutely sure you want to revoke credentials for{' '}
                  <strong>{accounts.find((a) => a.id === revokeConfirmationId)?.name}</strong>? Any active autonomous agent pipelines will fail with 401 Unauthorized instantly.
                </p>

                <div className="flex items-center gap-2">
                  <button
                    onClick={() => handleRevokeToken(revokeConfirmationId)}
                    className="flex-1 py-1.5 bg-red-600 hover:bg-red-500 text-white text-xs font-semibold rounded cursor-pointer transition-colors text-center"
                  >
                    Confirm Revocation
                  </button>
                  <button
                    onClick={() => setRevokeConfirmationId(null)}
                    className="flex-1 py-1.5 bg-slate-200 dark:bg-slate-800 text-slate-600 dark:text-gray-300 hover:bg-slate-300 text-xs font-semibold rounded cursor-pointer transition-colors text-center"
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
