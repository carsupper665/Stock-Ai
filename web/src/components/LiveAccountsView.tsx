import React, { useState, useCallback, useEffect } from 'react';
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
  Power,
  ChevronDown,
  ChevronUp,
  RefreshCw,
  Key,
  Copy,
  Check,
} from 'lucide-react';
import { LiveAccount, Theme, ViewState, Order, Position, Trade, AccountToken } from '../types';
import { createLiveAccount, deleteLiveAccount, updateLiveAccount } from '../api/liveAccounts';
import {
  listOrders,
  listPositions,
  listTrades,
  listTokens,
  createAdminToken,
  revokeAdminToken,
  type CreatedToken,
} from '../api/adminTrading';

interface LiveAccountsViewProps {
  theme: Theme;
  viewState: ViewState;
  liveAccounts: LiveAccount[];
  onAddLiveAccount: (la: LiveAccount) => void;
  onDeleteLiveAccount: (id: string) => void;
  onUpdateLiveAccount: (la: LiveAccount) => void;
}

type DetailTab = 'positions' | 'orders' | 'trades' | 'tokens';

interface AccountDetail {
  positions: Position[];
  orders: Order[];
  trades: Trade[];
  tokens: AccountToken[];
  loading: boolean;
  error: string | null;
}

const AVAILABLE_SCOPES = ['market:read', 'trade:write', 'trade:read', 'account:read'] as const;

export default function LiveAccountsView({
  theme,
  viewState,
  liveAccounts,
  onAddLiveAccount,
  onDeleteLiveAccount,
  onUpdateLiveAccount
}: LiveAccountsViewProps) {
  const isDark = theme === 'dark';

  // ── Add Account Form ─────────────────────────────────────────────────────
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [accName, setAccName] = useState('Binance Paper Trading');
  const [provider, setProvider] = useState('binance');
  const [environment, setEnvironment] = useState('production');
  const [initialBalance, setInitialBalance] = useState(10000);
  const [baseCurrency, setBaseCurrency] = useState('USDT');
  const [supportedSymbols, setSupportedSymbols] = useState('BTCUSDT,ETHUSDT');

  // ── Account Detail Panel ─────────────────────────────────────────────────
  const [selectedAccountId, setSelectedAccountId] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<DetailTab>('positions');
  const [detail, setDetail] = useState<AccountDetail>({
    positions: [],
    orders: [],
    trades: [],
    tokens: [],
    loading: false,
    error: null,
  });

  // ── Token Management ─────────────────────────────────────────────────────
  const [tokenName, setTokenName] = useState('');
  const [selectedScopes, setSelectedScopes] = useState<string[]>(['market:read', 'trade:read']);
  const [tokenExpiresAt, setTokenExpiresAt] = useState('');
  const [showTokenForm, setShowTokenForm] = useState(false);
  const [creatingToken, setCreatingToken] = useState(false);
  const [revealedToken, setRevealedToken] = useState<CreatedToken | null>(null);
  const [copiedTokenId, setCopiedTokenId] = useState<string | null>(null);
  const [revokingTokenId, setRevokingTokenId] = useState<string | null>(null);

  // ── Load Detail Data ─────────────────────────────────────────────────────
  const loadDetail = useCallback(async (accountId: string) => {
    setDetail(prev => ({ ...prev, loading: true, error: null }));
    try {
      const [positions, orders, trades, tokens] = await Promise.all([
        listPositions(accountId),
        listOrders(accountId),
        listTrades(accountId),
        listTokens(accountId),
      ]);
      setDetail({ positions, orders, trades, tokens, loading: false, error: null });
    } catch (err) {
      setDetail(prev => ({
        ...prev,
        loading: false,
        error: err instanceof Error ? err.message : 'Failed to load account details.',
      }));
    }
  }, []);

  useEffect(() => {
    if (selectedAccountId) {
      void loadDetail(selectedAccountId);
    }
  }, [selectedAccountId, loadDetail]);

  const handleSelectAccount = (accountId: string) => {
    if (selectedAccountId === accountId) {
      setSelectedAccountId(null);
    } else {
      setSelectedAccountId(accountId);
      setActiveTab('positions');
      setRevealedToken(null);
      setShowTokenForm(false);
    }
  };

  // ── Token Operations ─────────────────────────────────────────────────────
  const handleCreateToken = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!selectedAccountId) return;
    setCreatingToken(true);
    try {
      const created = await createAdminToken(selectedAccountId, {
        name: tokenName,
        scopes: selectedScopes,
        expiresAt: tokenExpiresAt || undefined,
      });
      setRevealedToken(created);
      setDetail(prev => ({ ...prev, tokens: [...prev.tokens, created] }));
      setShowTokenForm(false);
      setTokenName('');
      setSelectedScopes(['market:read', 'trade:read']);
      setTokenExpiresAt('');
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to create token.');
    } finally {
      setCreatingToken(false);
    }
  };

  const handleRevokeToken = async (tokenId: string) => {
    if (!selectedAccountId) return;
    if (!confirm('Revoke this token? This action cannot be undone.')) return;
    setRevokingTokenId(tokenId);
    try {
      await revokeAdminToken(selectedAccountId, tokenId);
      setDetail(prev => ({ ...prev, tokens: prev.tokens.filter(t => t.id !== tokenId) }));
      if (revealedToken?.id === tokenId) setRevealedToken(null);
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to revoke token.');
    } finally {
      setRevokingTokenId(null);
    }
  };

  const handleCopyToken = (text: string, id: string) => {
    void navigator.clipboard.writeText(text);
    setCopiedTokenId(id);
    setTimeout(() => setCopiedTokenId(null), 1500);
  };

  const toggleScope = (scope: string) => {
    setSelectedScopes(prev =>
      prev.includes(scope) ? prev.filter(s => s !== scope) : [...prev, scope]
    );
  };

  // ── Add Account ──────────────────────────────────────────────────────────
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    try {
      const symbolsList = supportedSymbols.split(',').map(s => s.trim()).filter(Boolean);
      const newAccount = await createLiveAccount({
        name: accName,
        initialBalance,
        baseCurrency,
        provider,
        environment,
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
    if (!confirm('Delete this live account? This action cannot be undone.')) return;
    try {
      await deleteLiveAccount(accountId);
      onDeleteLiveAccount(accountId);
      if (selectedAccountId === accountId) setSelectedAccountId(null);
    } catch (error) {
      console.error('Failed to delete live account:', error);
      alert('Failed to delete live account. Check console for details.');
    }
  };

  const handleToggleLiveTrading = async (account: LiveAccount) => {
    const newState = !account.liveTradingEnabled;
    if (newState) {
      if (!confirm(
        `⚠️ CRITICAL WARNING ⚠️\n\nYou are about to ENABLE LIVE TRADING for "${account.name}".\n\nThis will send REAL ORDERS to the exchange using REAL MONEY.\n\nAre you absolutely sure you want to proceed?`
      )) return;
      if (!confirm(
        `FINAL CONFIRMATION\n\nAccount: ${account.name}\nProvider: ${account.provider}\nEnvironment: ${account.environment || 'production'}\n\nEnabling live trading will execute real trades on the exchange.\n\nType OK to confirm or Cancel to abort.`
      )) return;
    } else {
      if (!confirm(
        `Disable live trading for "${account.name}"?\n\nThis will switch from real exchange execution to paper trading (simulation only).`
      )) return;
    }
    try {
      const updated = await updateLiveAccount(account.id, { liveTradingEnabled: newState });
      onUpdateLiveAccount(updated);
      alert(
        newState
          ? `✅ Live trading ENABLED for "${account.name}".`
          : `✅ Live trading DISABLED for "${account.name}". Switched to paper trading mode.`
      );
    } catch (error) {
      console.error('Failed to toggle live trading:', error);
      alert('Failed to update live trading status.');
    }
  };

  // ── Formatters ───────────────────────────────────────────────────────────
  const fmt = (v: number, currency = '') =>
    `${v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}${currency ? ' ' + currency : ''}`;

  const fmtPnL = (v: number) => (v >= 0 ? '+' : '') + v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });

  const fmtDate = (iso: string) => {
    try { return new Date(iso).toLocaleString(); } catch { return iso; }
  };

  // ── Styles ───────────────────────────────────────────────────────────────
  const cardCls = `rounded-xl border shadow-sm ${isDark ? 'bg-[#1d2027] border-slate-800' : 'bg-white border-slate-200'}`;
  const thCls = `p-3 text-[10px] uppercase font-bold tracking-wider border-b ${isDark ? 'bg-slate-800/40 border-slate-800 text-gray-200' : 'bg-slate-50/50 border-slate-100 text-slate-500'}`;
  const inputCls = `w-full p-2 border text-xs rounded font-mono ${isDark ? 'bg-slate-900 border-slate-800 text-white' : 'bg-white border-slate-200 text-slate-800'}`;
  const tabBase = 'px-4 py-2 text-xs font-semibold border-b-2 transition-colors cursor-pointer';
  const tabActive = `border-blue-500 text-blue-500`;
  const tabInactive = `border-transparent ${isDark ? 'text-gray-400 hover:text-white' : 'text-slate-500 hover:text-slate-800'}`;

  // ── Loading / Error States ───────────────────────────────────────────────
  if (viewState === 'loading') {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-6 w-1/4 bg-slate-300 dark:bg-slate-700 rounded"></div>
        <div className="h-20 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
        <div className="h-44 bg-slate-200 dark:bg-slate-800 rounded-xl"></div>
      </div>
    );
  }

  if (viewState === 'error') {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12 bg-red-500/5 border border-red-500/20 rounded-xl">
        <ShieldAlert className="mx-auto text-red-500 mb-2" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Failed to load live accounts</h3>
        <p className="text-xs text-slate-500 mt-1">Could not fetch live account data from backend.</p>
      </div>
    );
  }

  if (liveAccounts.length === 0 && !showForm) {
    return (
      <div className="p-6 text-center max-w-sm mx-auto mt-12">
        <Layers className="mx-auto text-slate-400 mb-2" />
        <h3 className={`font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>No Live Accounts Configured</h3>
        <p className="text-xs text-gray-500 mt-1 mb-4">
          Create live trading accounts for paper trading simulation with real-time market prices.
        </p>
        <button onClick={() => setShowForm(true)} className="px-3 py-1.5 bg-blue-600 text-white rounded text-xs font-semibold hover:bg-blue-500">
          Add Live Account
        </button>
      </div>
    );
  }

  if (liveAccounts.length === 0 && showForm) {
    return (
      <div className="p-6 space-y-6">
        <div className="flex items-center justify-between">
          <h3 className={`text-sm font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Add Live Account</h3>
          <button onClick={() => setShowForm(false)} className="text-xs text-gray-400 hover:text-black dark:hover:text-white">CANCEL</button>
        </div>
        <div className={`max-w-md rounded-xl border p-5 space-y-4 shadow-sm ${isDark ? 'bg-[#181a1e] border-slate-800' : 'bg-slate-50 border-slate-200'}`}>
          {renderForm()}
        </div>
      </div>
    );
  }

  function renderForm() {
    return (
      <>
        <div className="flex items-center justify-between pb-2 border-b border-gray-200 dark:border-slate-800">
          <h4 className={`text-xs font-bold uppercase tracking-wider ${isDark ? 'text-white' : 'text-slate-800'}`}>Live Account Configuration</h4>
        </div>
        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Account Name</label>
            <input type="text" value={accName} onChange={e => setAccName(e.target.value)} className={inputCls} required disabled={submitting} />
          </div>
          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Provider/Exchange</label>
            <select value={provider} onChange={e => setProvider(e.target.value)} className={inputCls} disabled={submitting}>
              <option value="binance">Binance</option>
              <option value="okx">OKX</option>
              <option value="bybit">Bybit</option>
              <option value="kraken">Kraken</option>
              <option value="coinbase">Coinbase</option>
            </select>
          </div>
          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Environment</label>
            <select value={environment} onChange={e => setEnvironment(e.target.value)} className={inputCls} disabled={submitting}>
              <option value="production">Production (Paper Trading)</option>
              <option value="testnet">Testnet</option>
              <option value="sandbox">Sandbox</option>
            </select>
          </div>
          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Initial Balance</label>
            <input type="number" value={initialBalance} onChange={e => setInitialBalance(Number(e.target.value))} className={inputCls} required min="0" step="0.01" disabled={submitting} />
          </div>
          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Base Currency</label>
            <select value={baseCurrency} onChange={e => setBaseCurrency(e.target.value)} className={inputCls} disabled={submitting}>
              <option value="USDT">USDT</option>
              <option value="USD">USD</option>
              <option value="BUSD">BUSD</option>
              <option value="USDC">USDC</option>
            </select>
          </div>
          <div>
            <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Supported Symbols</label>
            <input type="text" value={supportedSymbols} onChange={e => setSupportedSymbols(e.target.value)} className={inputCls} placeholder="BTCUSDT,ETHUSDT,..." required disabled={submitting} />
            <span className="text-[9px] text-slate-400 mt-1 block">Comma-separated list of trading pairs.</span>
          </div>
          <button type="submit" className="w-full py-2 bg-blue-600 text-white hover:bg-blue-500 text-xs font-semibold rounded cursor-pointer transition-colors disabled:bg-gray-400 disabled:cursor-not-allowed" disabled={submitting}>
            {submitting ? 'Creating...' : 'Create Live Account'}
          </button>
        </form>
      </>
    );
  }

  // ── Detail Panel ─────────────────────────────────────────────────────────
  function renderDetailPanel(accountId: string) {
    return (
      <div className={`mt-4 rounded-xl border shadow-sm ${isDark ? 'bg-[#181a1e] border-slate-800' : 'bg-slate-50 border-slate-200'}`}>
        {/* Tab Bar */}
        <div className={`flex items-center justify-between border-b ${isDark ? 'border-slate-800' : 'border-slate-200'}`}>
          <div className="flex">
            {(['positions', 'orders', 'trades', 'tokens'] as DetailTab[]).map(tab => (
              <button
                key={tab}
                onClick={() => setActiveTab(tab)}
                className={`${tabBase} ${activeTab === tab ? tabActive : tabInactive} capitalize`}
              >
                {tab}
              </button>
            ))}
          </div>
          <button
            onClick={() => void loadDetail(accountId)}
            className="mr-3 p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 text-gray-400 hover:text-blue-500 transition-colors"
            title="Refresh"
          >
            <RefreshCw size={12} className={detail.loading ? 'animate-spin' : ''} />
          </button>
        </div>

        {/* Panel Body */}
        <div className="p-4">
          {detail.loading && (
            <div className="flex items-center space-x-2 text-xs text-gray-400 py-4">
              <RefreshCw size={12} className="animate-spin" />
              <span>Loading...</span>
            </div>
          )}
          {detail.error && !detail.loading && (
            <div className="flex items-center space-x-2 text-xs text-red-500 py-2">
              <AlertTriangle size={12} />
              <span>{detail.error}</span>
            </div>
          )}
          {!detail.loading && !detail.error && (
            <>
              {activeTab === 'positions' && renderPositions()}
              {activeTab === 'orders' && renderOrders()}
              {activeTab === 'trades' && renderTrades()}
              {activeTab === 'tokens' && renderTokens(accountId)}
            </>
          )}
        </div>
      </div>
    );
  }

  function renderPositions() {
    if (detail.positions.length === 0) {
      return <p className="text-xs text-gray-400 py-2">No open positions.</p>;
    }
    return (
      <div className="overflow-x-auto">
        <table className="w-full text-left border-collapse text-xs">
          <thead>
            <tr className={thCls.replace('rounded-xl border shadow-sm', '')}>
              <th className="p-2">Symbol</th>
              <th className="p-2">Side</th>
              <th className="p-2 text-right">Qty</th>
              <th className="p-2 text-right">Entry Price</th>
              <th className="p-2 text-right">Unrealized PnL</th>
              <th className="p-2 text-right">Leverage</th>
              <th className="p-2">Updated</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
            {detail.positions.map(pos => (
              <tr key={pos.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30">
                <td className="p-2 font-mono font-semibold">{pos.symbol}</td>
                <td className="p-2">
                  <span className={`px-1.5 py-0.5 rounded text-[10px] font-bold ${pos.position_side === 'long' ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400' : 'bg-red-500/10 text-red-600 dark:text-red-400'}`}>
                    {pos.position_side.toUpperCase()}
                  </span>
                </td>
                <td className="p-2 text-right font-mono">{pos.quantity}</td>
                <td className="p-2 text-right font-mono">{fmt(pos.avg_entry_price)}</td>
                <td className={`p-2 text-right font-mono font-semibold ${pos.unrealized_pnl >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>
                  {fmtPnL(pos.unrealized_pnl)}
                </td>
                <td className="p-2 text-right font-mono">{pos.leverage}×</td>
                <td className="p-2 text-[10px] text-gray-400">{fmtDate(pos.updated_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  }

  function renderOrders() {
    if (detail.orders.length === 0) {
      return <p className="text-xs text-gray-400 py-2">No orders.</p>;
    }
    return (
      <div className="overflow-x-auto">
        <table className="w-full text-left border-collapse text-xs">
          <thead>
            <tr>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Symbol</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Side</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Type</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400 text-right">Qty</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400 text-right">Price</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Status</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Created</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
            {detail.orders.map(order => (
              <tr key={order.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30">
                <td className="p-2 font-mono font-semibold">{order.symbol}</td>
                <td className="p-2">
                  <span className={`px-1.5 py-0.5 rounded text-[10px] font-bold ${order.side === 'buy' ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400' : 'bg-red-500/10 text-red-600 dark:text-red-400'}`}>
                    {order.side.toUpperCase()}
                  </span>
                </td>
                <td className="p-2 text-gray-500 capitalize">{order.type}</td>
                <td className="p-2 text-right font-mono">{order.quantity}</td>
                <td className="p-2 text-right font-mono">{order.price ? fmt(order.price) : '—'}</td>
                <td className="p-2">
                  <span className={`px-1.5 py-0.5 rounded text-[10px] font-bold ${
                    order.status === 'FILLED' ? 'bg-emerald-500/10 text-emerald-500'
                    : order.status === 'CANCELLED' ? 'bg-gray-500/10 text-gray-400'
                    : 'bg-amber-500/10 text-amber-500'
                  }`}>
                    {order.status}
                  </span>
                </td>
                <td className="p-2 text-[10px] text-gray-400">{fmtDate(order.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  }

  function renderTrades() {
    if (detail.trades.length === 0) {
      return <p className="text-xs text-gray-400 py-2">No trades.</p>;
    }
    return (
      <div className="overflow-x-auto">
        <table className="w-full text-left border-collapse text-xs">
          <thead>
            <tr>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Symbol</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Side</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400 text-right">Qty</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400 text-right">Price</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400 text-right">Realized PnL</th>
              <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Executed</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
            {detail.trades.map(trade => (
              <tr key={trade.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30">
                <td className="p-2 font-mono font-semibold">{trade.symbol}</td>
                <td className="p-2">
                  <span className={`px-1.5 py-0.5 rounded text-[10px] font-bold ${trade.side === 'buy' ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400' : 'bg-red-500/10 text-red-600 dark:text-red-400'}`}>
                    {trade.side.toUpperCase()}
                  </span>
                </td>
                <td className="p-2 text-right font-mono">{trade.quantity}</td>
                <td className="p-2 text-right font-mono">{fmt(trade.price)}</td>
                <td className={`p-2 text-right font-mono font-semibold ${trade.realized_pnl >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>
                  {fmtPnL(trade.realized_pnl)}
                </td>
                <td className="p-2 text-[10px] text-gray-400">{fmtDate(trade.executed_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  }

  function renderTokens(accountId: string) {
    return (
      <div className="space-y-4">
        {/* Revealed Token Banner */}
        {revealedToken && (
          <div className="p-3 rounded-lg border border-emerald-500/30 bg-emerald-500/5">
            <div className="flex items-center space-x-2 mb-2">
              <CheckCircle size={13} className="text-emerald-500" />
              <span className="text-xs font-bold text-emerald-600 dark:text-emerald-400">Token Created — Copy Now (shown once only)</span>
            </div>
            <div className={`flex items-center space-x-2 font-mono text-xs p-2 rounded border ${isDark ? 'bg-slate-900 border-slate-700 text-green-400' : 'bg-white border-slate-200 text-green-700'}`}>
              <span className="flex-1 break-all">{revealedToken.token}</span>
              <button onClick={() => handleCopyToken(revealedToken.token, revealedToken.id)} className="flex-shrink-0 p-1 hover:text-blue-500 transition-colors">
                {copiedTokenId === revealedToken.id ? <Check size={13} /> : <Copy size={13} />}
              </button>
            </div>
            <button onClick={() => setRevealedToken(null)} className="mt-2 text-[10px] text-gray-400 hover:text-gray-600">Dismiss</button>
          </div>
        )}

        {/* Token List */}
        {detail.tokens.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse text-xs">
              <thead>
                <tr>
                  <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Name</th>
                  <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Scopes</th>
                  <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Created</th>
                  <th className="p-2 text-[10px] uppercase font-bold text-gray-400">Last Used</th>
                  <th className="p-2 text-center text-[10px] uppercase font-bold text-gray-400">Revoke</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/40">
                {detail.tokens.map(token => (
                  <tr key={token.id} className="hover:bg-slate-50/40 dark:hover:bg-slate-800/30">
                    <td className="p-2 font-semibold flex items-center space-x-1.5">
                      <Key size={11} className="text-blue-400 flex-shrink-0" />
                      <span>{token.token_name}</span>
                    </td>
                    <td className="p-2">
                      <div className="flex flex-wrap gap-1">
                        {token.scope.split(',').map(s => (
                          <span key={s} className="px-1.5 py-0.5 rounded text-[9px] font-mono bg-blue-500/10 text-blue-600 dark:text-blue-400">
                            {s.trim()}
                          </span>
                        ))}
                      </div>
                    </td>
                    <td className="p-2 text-[10px] text-gray-400">{fmtDate(token.expires_at ?? '')}</td>
                    <td className="p-2 text-[10px] text-gray-400">{token.last_used_at ? fmtDate(token.last_used_at) : '—'}</td>
                    <td className="p-2 text-center">
                      <button
                        onClick={() => void handleRevokeToken(token.id)}
                        disabled={revokingTokenId === token.id}
                        className="p-1.5 rounded hover:bg-red-500/10 text-gray-400 hover:text-red-500 transition-colors disabled:opacity-40"
                        title="Revoke token"
                      >
                        <Trash2 size={12} />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {detail.tokens.length === 0 && !showTokenForm && (
          <p className="text-xs text-gray-400 py-1">No active tokens for this account.</p>
        )}

        {/* Create Token Form */}
        {showTokenForm ? (
          <form onSubmit={e => void handleCreateToken(e)} className="space-y-3 pt-2 border-t border-slate-200 dark:border-slate-800">
            <h5 className={`text-xs font-bold ${isDark ? 'text-white' : 'text-slate-700'}`}>New Token</h5>
            <div>
              <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Token Name</label>
              <input type="text" value={tokenName} onChange={e => setTokenName(e.target.value)} className={inputCls} required placeholder="e.g. My Bot Key" />
            </div>
            <div>
              <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Scopes</label>
              <div className="flex flex-wrap gap-2">
                {AVAILABLE_SCOPES.map(scope => (
                  <label key={scope} className="flex items-center space-x-1.5 cursor-pointer">
                    <input
                      type="checkbox"
                      checked={selectedScopes.includes(scope)}
                      onChange={() => toggleScope(scope)}
                      className="rounded"
                    />
                    <span className="text-xs font-mono text-gray-600 dark:text-gray-300">{scope}</span>
                  </label>
                ))}
              </div>
            </div>
            <div>
              <label className="block text-[10px] font-semibold text-gray-400 uppercase mb-1">Expires At (optional)</label>
              <input type="datetime-local" value={tokenExpiresAt} onChange={e => setTokenExpiresAt(e.target.value)} className={inputCls} />
            </div>
            <div className="flex space-x-2">
              <button type="submit" disabled={creatingToken || selectedScopes.length === 0} className="px-3 py-1.5 bg-blue-600 text-white text-xs font-semibold rounded hover:bg-blue-500 disabled:bg-gray-400 disabled:cursor-not-allowed">
                {creatingToken ? 'Creating...' : 'Create Token'}
              </button>
              <button type="button" onClick={() => setShowTokenForm(false)} className="px-3 py-1.5 text-xs font-semibold text-gray-400 hover:text-gray-700 dark:hover:text-white">
                Cancel
              </button>
            </div>
          </form>
        ) : (
          <button
            onClick={() => { setShowTokenForm(true); void loadDetail(accountId); }}
            className="flex items-center space-x-1.5 px-3 py-1.5 bg-blue-600/10 text-blue-600 dark:text-blue-400 text-xs font-semibold rounded hover:bg-blue-600/20 transition-colors"
          >
            <Plus size={12} />
            <span>Create Token</span>
          </button>
        )}
      </div>
    );
  }

  // ── Main Render ──────────────────────────────────────────────────────────
  return (
    <div className="p-6 space-y-6">

      {/* Info Banner */}
      <div className="p-4 rounded-xl border border-blue-400 bg-blue-50 dark:bg-blue-950/20 dark:border-blue-800/60 text-blue-700 dark:text-blue-300">
        <div className="flex items-start space-x-3 text-xs leading-relaxed">
          <Info size={18} className="flex-shrink-0 text-blue-500 mt-0.5" />
          <div>
            <p className="font-bold uppercase tracking-wider text-[11px] mb-0.5">LIVE TRADING ACCOUNT CONFIGURATION</p>
            <p>
              Live accounts use real-time market prices for trading simulation (paper trading) or actual exchange execution.
              Click any row to expand orders, positions, trades, and token management.
            </p>
          </div>
        </div>
      </div>

      {/* Main Layout */}
      <div className="flex flex-col lg:flex-row gap-6">

        {/* Account List */}
        <div className="flex-grow space-y-4">
          <div className="flex justify-between items-center">
            <h3 className={`text-sm font-bold ${isDark ? 'text-white' : 'text-slate-800'}`}>Live Trading Accounts</h3>
            <button onClick={() => setShowForm(!showForm)} className="px-3 py-1.5 bg-blue-600 text-white text-xs font-semibold rounded-lg hover:bg-blue-500 flex items-center space-x-1 cursor-pointer">
              <Plus size={13} />
              <span>Add Live Account</span>
            </button>
          </div>

          <div className={`${cardCls} overflow-x-auto`}>
            <table className="w-full text-left border-collapse text-xs">
              <thead>
                <tr className={`border-b text-[10px] uppercase font-bold tracking-wider ${isDark ? 'bg-slate-800/40 border-slate-800 text-gray-200' : 'bg-slate-50/50 border-slate-100 text-slate-500'}`}>
                  <th className="p-3 w-4"></th>
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
                {liveAccounts.map(acc => {
                  const totalPnL = acc.realizedPnL + acc.unrealizedPnL;
                  const pnlPositive = totalPnL >= 0;
                  const isSelected = selectedAccountId === acc.id;
                  return (
                    <React.Fragment key={acc.id}>
                      <tr
                        className={`hover:bg-slate-50/40 dark:hover:bg-slate-800/30 transition-colors cursor-pointer ${isSelected ? (isDark ? 'bg-blue-900/20' : 'bg-blue-50/60') : ''}`}
                        onClick={() => handleSelectAccount(acc.id)}
                      >
                        <td className="p-3 text-gray-400">
                          {isSelected ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
                        </td>
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
                          {fmt(acc.walletBalance, acc.baseCurrency)}
                        </td>
                        <td className="p-3 text-right font-mono text-[11px] text-slate-600 dark:text-gray-300">
                          {fmt(acc.availableBalance, acc.baseCurrency)}
                        </td>
                        <td className="p-3 text-right font-mono text-[11px] text-slate-500 dark:text-gray-400">
                          {fmt(acc.lockedMargin, acc.baseCurrency)}
                        </td>
                        <td className={`p-3 text-right font-mono text-[11px] ${acc.realizedPnL >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>
                          {fmtPnL(acc.realizedPnL)}
                        </td>
                        <td className={`p-3 text-right font-mono text-[11px] ${acc.unrealizedPnL >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>
                          {fmtPnL(acc.unrealizedPnL)}
                        </td>
                        <td className="p-3 text-right font-mono text-[11px] font-bold text-slate-900 dark:text-gray-100">
                          <div className="flex items-center justify-end space-x-1">
                            {pnlPositive ? <TrendingUp size={12} className="text-emerald-500" /> : <TrendingDown size={12} className="text-red-500" />}
                            <span>{fmt(acc.equity, acc.baseCurrency)}</span>
                          </div>
                        </td>
                        <td className="p-3" onClick={e => e.stopPropagation()}>
                          <button
                            onClick={() => void handleToggleLiveTrading(acc)}
                            className={`px-2 py-0.5 rounded text-[10px] font-bold transition-colors hover:opacity-80 flex items-center space-x-1 ${acc.liveTradingEnabled ? 'bg-red-500/10 text-red-600 dark:text-red-400 hover:bg-red-500/20' : 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 hover:bg-emerald-500/20'}`}
                            title={acc.liveTradingEnabled ? 'Click to disable live trading' : 'Click to enable live trading'}
                          >
                            <Power size={10} />
                            <span>{acc.liveTradingEnabled ? 'LIVE' : 'PAPER'}</span>
                          </button>
                        </td>
                        <td className="p-3">
                          <span className={`px-2 py-0.5 rounded text-[10px] font-bold ${acc.status === 'Connected' ? 'bg-emerald-500/10 text-emerald-500 dark:text-emerald-400' : 'bg-amber-500/10 text-amber-500 dark:text-amber-400'}`}>
                            {acc.status}
                          </span>
                        </td>
                        <td className="p-3 text-center" onClick={e => e.stopPropagation()}>
                          <button onClick={() => void handleDelete(acc.id)} className="p-1.5 rounded hover:bg-red-500/10 text-gray-400 hover:text-red-500 transition-colors" title="Delete account">
                            <Trash2 size={14} />
                          </button>
                        </td>
                      </tr>
                      {isSelected && (
                        <tr>
                          <td colSpan={12} className="px-4 pb-4">
                            {renderDetailPanel(acc.id)}
                          </td>
                        </tr>
                      )}
                    </React.Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>

        {/* Add Account Form Drawer */}
        {showForm && (
          <div className={`w-full lg:w-96 rounded-xl border p-5 space-y-4 shadow-sm h-fit ${isDark ? 'bg-[#181a1e] border-slate-800' : 'bg-slate-50 border-slate-200'}`}>
            {renderForm()}
          </div>
        )}

      </div>
    </div>
  );
}
