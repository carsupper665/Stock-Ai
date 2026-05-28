import React, { useState } from 'react';
import {
  ShieldAlert,
  Plus,
  CheckCircle,
  AlertTriangle,
  Info,
  Layers,
  FileText,
  Trash2,
  TrendingUp,
  TrendingDown,
  Power
} from 'lucide-react';
import { LiveAccount, Theme, ViewState } from '../types';
import { createLiveAccount, deleteLiveAccount, updateLiveAccount } from '../api/liveAccounts';

interface LiveAccountsViewProps {
  theme: Theme;
  viewState: ViewState;
  liveAccounts: LiveAccount[];
  onAddLiveAccount: (la: LiveAccount) => void;
  onDeleteLiveAccount: (id: string) => void;
  onUpdateLiveAccount: (la: LiveAccount) => void;
}

export default function LiveAccountsView({
  theme,
  viewState,
  liveAccounts,
  onAddLiveAccount,
  onDeleteLiveAccount,
  onUpdateLiveAccount
}: LiveAccountsViewProps) {
  const isDark = theme === 'dark';
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [accName, setAccName] = useState('Binance Paper Trading');
  const [provider, setProvider] = useState('binance');
  const [environment, setEnvironment] = useState('production');
  const [initialBalance, setInitialBalance] = useState(10000);
  const [baseCurrency, setBaseCurrency] = useState('USDT');
  const [supportedSymbols, setSupportedSymbols] = useState('BTCUSDT,ETHUSDT');

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    try {
      const symbolsList = supportedSymbols.split(',').map(s => s.trim()).filter(Boolean);
      const newAccount = await createLiveAccount({
        name: accName,
        initialBalance: initialBalance,
        baseCurrency: baseCurrency,
        provider: provider,
        environment: environment,
        credentialsStatus: 'configured',
        supportedSymbols: symbolsList
      });
      onAddLiveAccount(newAccount);
      setShowForm(false);
      setAccName('Binance Paper Trading');
      setProvider('binance');
      setEnvironment('production');
      setInitialBalance(10000);
      setBaseCurrency('USDT');
      setSupportedSymbols('BTCUSDT,ETHUSDT');
    } catch (error) {
      console.error('Failed to create live account:', error);
      alert('Failed to create live account. Check console for details.');
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (accountId: string) => {
    if (!confirm('Delete this live account? This action cannot be undone.')) {
      return;
    }
    try {
      await deleteLiveAccount(accountId);
      onDeleteLiveAccount(accountId);
    } catch (error) {
      console.error('Failed to delete live account:', error);
      alert('Failed to delete live account. Check console for details.');
    }
  };

  const handleToggleLiveTrading = async (account: LiveAccount) => {
    const newState = !account.liveTradingEnabled;
    
    if (newState) {
      // Enabling LIVE trading - show strong warning
      const confirmed = confirm(
        `⚠️ CRITICAL WARNING ⚠️\n\n` +
        `You are about to ENABLE LIVE TRADING for "${account.name}".\n\n` +
        `This will send REAL ORDERS to the exchange using REAL MONEY.\n\n` +
        `Current mode: Paper Trading (simulation)\n` +
        `New mode: LIVE TRADING (real exchange)\n\n` +
        `Are you absolutely sure you want to proceed?`
      );
      
      if (!confirmed) {
        return;
      }
      
      // Double confirmation for live trading
      const doubleConfirm = confirm(
        `FINAL CONFIRMATION\n\n` +
        `Account: ${account.name}\n` +
        `Provider: ${account.provider}\n` +
        `Environment: ${account.environment || 'production'}\n\n` +
        `Enabling live trading will execute real trades on the exchange.\n\n` +
        `Type OK to confirm or Cancel to abort.`
      );
      
      if (!doubleConfirm) {
        return;
      }
    } else {
      // Disabling live trading - simple confirmation
      const confirmed = confirm(
        `Disable live trading for "${account.name}"?\n\n` +
        `This will switch from real exchange execution to paper trading (simulation only).`
      );
      
      if (!confirmed) {
        return;
      }
    }
    
    try {
      const updated = await updateLiveAccount(account.id, {
        liveTradingEnabled: newState
      });
      onUpdateLiveAccount(updated);
      alert(
        newState 
          ? `✅ Live trading ENABLED for "${account.name}". Orders will now execute on the real exchange.`
          : `✅ Live trading DISABLED for "${account.name}". Switched to paper trading mode.`
      );
    } catch (error) {
      console.error('Failed to toggle live trading:', error);
      alert('Failed to update live trading status. Check console for details.');
    }
  };

  const formatCurrency = (value: number, currency: string) => {
    return `${value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${currency}`;
  };

  const formatPnL = (value: number) => {
    const prefix = value >= 0 ? '+' : '';
    return `${prefix}${value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  };

  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-300 dark:bg-slate-700 rounded"></div>
        <div className="h-20 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
        <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  };

  if (viewState === 'error') {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12 bg-red-500/5 border border-red-500/20 rounded-xl p-6">
        <ShieldAlert className="mx-auto text-red-500 mb-2" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Failed to load live accounts</h3>
        <p className="text-xs text-slate-500 mt-1">
          Could not fetch live account data from backend.
        </p>
      </div>
    );
  }

  if (liveAccounts.length === 0 && !showForm) {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12">
        <Layers className={`mx-auto text-slate-400 mb-2`} />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>No Live Accounts Configured</h3>
        <p className="text-xs text-gray-500 mt-1 mb-4">
          Create live trading accounts for paper trading simulation with real-time market prices, or connect exchange credentials for testnet/mainnet execution.
        </p>
        <button
          onClick={() => setShowForm(true)}
          className="px-3 py-1.5 bg-blue-600 text-white rounded text-xs font-semibold hover:bg-blue-500"
        >
          Add Live Account
        </button>
      </div>
    );
  }

  if (liveAccounts.length === 0 && showForm) {
    return (
      <div className="p-6 space-y-6">
        <div className="flex items-center justify-between">
          <h3 className={`text-sm font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>
            Add Live Account
          </h3>
          <button
            onClick={() => setShowForm(false)}
            className="text-xs text-gray-400 hover:text-black dark:hover:text-white"
          >
            CANCEL
          </button>
        </div>

        <div className={`max-w-md rounded-xl border p-5 space-y-4 shadow-sm ${
          isDark ? 'bg-[#181a1e] border-slate-800' : 'bg-slate-50 border-slate-200'
        }`}>
          {renderForm()}
        </div>
      </div>
    );
  }

  function renderForm() {
    return (
      <>
        <div className="flex items-center justify-between pb-2 border-b border-gray-200 dark:border-slate-800">
          <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>
            Live Account Configuration
          </h4>
        </div>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Account Name</label>
            <input
              type="text"
              value={accName}
              onChange={(e) => setAccName(e.target.value)}
              className={`w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
              required
              disabled={submitting}
            />
          </div>

          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Provider/Exchange</label>
            <select
              value={provider}
              onChange={(e) => setProvider(e.target.value)}
              className={`w-full p-2 border text-xs rounded ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
              disabled={submitting}
            >
              <option value="binance">Binance</option>
              <option value="okx">OKX</option>
              <option value="bybit">Bybit</option>
              <option value="kraken">Kraken</option>
              <option value="coinbase">Coinbase</option>
            </select>
          </div>

          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Environment</label>
            <select
              value={environment}
              onChange={(e) => setEnvironment(e.target.value)}
              className={`w-full p-2 border text-xs rounded ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
              disabled={submitting}
            >
              <option value="production">Production (Paper Trading)</option>
              <option value="testnet">Testnet</option>
              <option value="sandbox">Sandbox</option>
            </select>
            <span className="text-[9px] text-slate-400 mt-1 block">
              Production environment uses real market prices with simulated execution (paper trading).
            </span>
          </div>

          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Initial Balance</label>
            <input
              type="number"
              value={initialBalance}
              onChange={(e) => setInitialBalance(Number(e.target.value))}
              className={`w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
              required
              min="0"
              step="0.01"
              disabled={submitting}
            />
          </div>

          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Base Currency</label>
            <select
              value={baseCurrency}
              onChange={(e) => setBaseCurrency(e.target.value)}
              className={`w-full p-2 border text-xs rounded ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
              disabled={submitting}
            >
              <option value="USDT">USDT</option>
              <option value="USD">USD</option>
              <option value="BUSD">BUSD</option>
              <option value="USDC">USDC</option>
            </select>
          </div>

          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Supported Symbols</label>
            <input
              type="text"
              value={supportedSymbols}
              onChange={(e) => setSupportedSymbols(e.target.value)}
              className={`w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`}
              placeholder="BTCUSDT,ETHUSDT,..."
              required
              disabled={submitting}
            />
            <span className="text-[9px] text-slate-400 mt-1 block">
              Comma-separated list of trading pairs (e.g., BTCUSDT,ETHUSDT).
            </span>
          </div>

          <button
            type="submit"
            className="w-full py-2 bg-blue-600 text-white hover:bg-blue-500 text-xs font-semibold rounded cursor-pointer transition-colors disabled:bg-gray-400 disabled:cursor-not-allowed"
            disabled={submitting}
          >
            {submitting ? 'Creating...' : 'Create Live Account'}
          </button>
        </form>
      </>
    );
  }

  return (
    <div className="p-6 space-y-6">

      {/* Info banner */}
      <div className="p-4 rounded-xl border border-blue-400 bg-blue-50 dark:bg-blue-950/20 dark:border-blue-800/60 text-blue-700 dark:text-blue-300">
        <div className="flex items-start space-x-3 text-xs leading-relaxed">
          <Info size={18} className="flex-shrink-0 text-blue-500 mt-0.5" />
          <div>
            <p className="font-bold uppercase tracking-wider text-[11px] mb-0.5">LIVE TRADING ACCOUNT CONFIGURATION</p>
            <p>
              Live accounts use real-time market prices for trading simulation (paper trading) or actual exchange execution (testnet/mainnet). Configure exchange connection, initial balance, and supported symbols here. View detailed trading activity, orders, positions, and PnL in the Trading Dashboard (coming soon).
            </p>
          </div>
        </div>
      </div>

      {/* Main layout */}
      <div className="flex flex-col lg:flex-row gap-6">

        {/* List of live accounts */}
        <div className="flex-grow space-y-4">
          <div className="flex justify-between items-center">
            <h3 className={`text-sm font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>
              Live Trading Accounts
            </h3>
            <button
              onClick={() => setShowForm(!showForm)}
              className="px-3 py-1.5 bg-blue-600 text-white text-xs font-semibold rounded-lg hover:bg-blue-500 flex items-center space-x-1 cursor-pointer"
            >
              <Plus size={13} />
              <span>Add Live Account</span>
            </button>
          </div>

          <div className={`border rounded-xl overflow-x-auto shadow-sm ${
            isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'
          }`}>
            <table className="w-full text-left border-collapse text-xs">
              <thead>
                <tr className={`border-b text-[10px] uppercase font-bold tracking-wider ${
                  isDark ? 'bg-slate-800/40 border-slate-800 text-gray-200' : 'bg-slate-50/50 border-slate-100 text-slate-500'
                }`}>
                  <th className="p-3">Account</th>
                  <th className="p-3">Provider</th>
                  <th className="p-3 text-right">Wallet Balance</th>
                  <th className="p-3 text-right">Available</th>
                  <th className="p-3 text-right">Locked</th>
                  <th className="p-3 text-right">Realized PnL</th>
                  <th className="p-3 text-right">Unrealized PnL</th>
                  <th className="p-3 text-right">Equity</th>
                  <th className="p-3">Mode</th>
                  <th className="p-3">Status</th>
                  <th className="p-3 text-center">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
                {liveAccounts.map((acc) => {
                  const totalPnL = acc.realizedPnL + acc.unrealizedPnL;
                  const pnlPositive = totalPnL >= 0;
                  return (
                    <tr key={acc.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30 transition-colors">
                      <td className="p-3 font-semibold text-slate-900 dark:text-gray-100">
                        <div className="flex items-center space-x-2">
                          <FileText size={12} className="text-blue-500" />
                          <span>{acc.name}</span>
                        </div>
                        <div className="text-[9px] text-gray-400 mt-0.5 font-mono">
                          {acc.supportedSymbols.slice(0, 3).join(', ')}
                          {acc.supportedSymbols.length > 3 && ` +${acc.supportedSymbols.length - 3}`}
                        </div>
                      </td>
                      <td className="p-3 text-slate-600 dark:text-gray-300 capitalize">{acc.provider}</td>
                      <td className="p-3 text-right font-mono text-[11px] text-slate-700 dark:text-gray-200">
                        {formatCurrency(acc.walletBalance, acc.baseCurrency)}
                      </td>
                      <td className="p-3 text-right font-mono text-[11px] text-slate-600 dark:text-gray-300">
                        {formatCurrency(acc.availableBalance, acc.baseCurrency)}
                      </td>
                      <td className="p-3 text-right font-mono text-[11px] text-slate-500 dark:text-gray-400">
                        {formatCurrency(acc.lockedMargin, acc.baseCurrency)}
                      </td>
                      <td className={`p-3 text-right font-mono text-[11px] ${acc.realizedPnL >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>
                        {formatPnL(acc.realizedPnL)}
                      </td>
                      <td className={`p-3 text-right font-mono text-[11px] ${acc.unrealizedPnL >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>
                        {formatPnL(acc.unrealizedPnL)}
                      </td>
                      <td className="p-3 text-right font-mono text-[11px] font-bold text-slate-900 dark:text-gray-100">
                        <div className="flex items-center justify-end space-x-1">
                          {pnlPositive ? (
                            <TrendingUp size={12} className="text-emerald-500" />
                          ) : (
                            <TrendingDown size={12} className="text-red-500" />
                          )}
                          <span>{formatCurrency(acc.equity, acc.baseCurrency)}</span>
                        </div>
                      </td>
                      <td className="p-3">
                        <button
                          onClick={() => handleToggleLiveTrading(acc)}
                          className={`px-2 py-0.5 rounded text-[10px] font-bold transition-colors hover:opacity-80 flex items-center space-x-1 ${
                            acc.liveTradingEnabled
                              ? 'bg-red-500/10 text-red-600 dark:text-red-400 hover:bg-red-500/20'
                              : 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 hover:bg-emerald-500/20'
                          }`}
                          title={acc.liveTradingEnabled ? 'Click to disable live trading (switch to paper)' : 'Click to enable live trading (real exchange)'}
                        >
                          <Power size={10} />
                          <span>{acc.liveTradingEnabled ? 'LIVE' : 'PAPER'}</span>
                        </button>
                      </td>
                      <td className="p-3">
                        <span className={`px-2 py-0.5 rounded text-[10px] font-bold ${
                          acc.status === 'Connected'
                            ? 'bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
                            : 'bg-amber-500/10 text-amber-500 dark:text-amber-400'
                        }`}>
                          {acc.status}
                        </span>
                      </td>
                      <td className="p-3 text-center">
                        <button
                          onClick={() => handleDelete(acc.id)}
                          className="p-1.5 rounded hover:bg-red-500/10 text-gray-400 hover:text-red-500 transition-colors"
                          title="Delete account"
                        >
                          <Trash2 size={14} />
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>

        {/* Input pane side drawer */}
        {showForm && (
          <div className={`w-full lg:w-96 rounded-xl border p-5 space-y-4 shadow-sm h-fit ${
            isDark ? 'bg-[#181a1e] border-slate-800' : 'bg-slate-50 border-slate-200'
          }`}>
            {renderForm()}
          </div>
        )}

      </div>
    </div>
  );
}
