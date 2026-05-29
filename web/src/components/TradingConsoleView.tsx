import React, { useCallback, useEffect, useState } from 'react';
import {
  TrendingUp,
  TrendingDown,
  RefreshCw,
  AlertTriangle,
  CheckCircle,
  ChevronDown,
  Loader,
  XCircle,
} from 'lucide-react';
import type { Theme, AdminAccount, Order, Position, Trade } from '../types';
import {
  listAllAccounts,
  placeOrder,
  listOrders,
  listPositions,
  listTrades,
  cancelOrder,
  type PlaceOrderInput,
} from '../api/adminTrading';
import { getLivePrice } from '../api/adminMarket';

interface TradingConsoleViewProps {
  theme: Theme;
}

type OrderTab = 'open' | 'positions' | 'trades';

const SUPPORTED_SYMBOLS = [
  'BTCUSDT', 'ETHUSDT', 'BNBUSDT', 'SOLUSDT', 'ADAUSDT',
  'XRPUSDT', 'DOGEUSDT', 'LTCUSDT', 'AVAXUSDT', 'DOTUSDT',
];

const INTERVALS = ['1m', '5m', '15m', '1h', '4h', '1d'];

export default function TradingConsoleView({ theme }: TradingConsoleViewProps) {
  const isDark = theme === 'dark';

  // Account selection
  const [accounts, setAccounts] = useState<AdminAccount[]>([]);
  const [selectedAccountId, setSelectedAccountId] = useState('');
  const [accountsLoading, setAccountsLoading] = useState(true);

  // Order form state
  const [symbol, setSymbol] = useState('BTCUSDT');
  const [side, setSide] = useState<'buy' | 'sell'>('buy');
  const [positionSide, setPositionSide] = useState<'long' | 'short'>('long');
  const [orderType, setOrderType] = useState<'market' | 'limit' | 'stop'>('market');
  const [quantity, setQuantity] = useState('');
  const [price, setPrice] = useState('');
  const [stopPrice, setStopPrice] = useState('');
  const [leverage, setLeverage] = useState('1');

  // Live price
  const [livePrice, setLivePrice] = useState<number | null>(null);
  const [priceLoading, setPriceLoading] = useState(false);

  // Submission
  const [submitting, setSubmitting] = useState(false);
  const [submitResult, setSubmitResult] = useState<{ ok: boolean; message: string } | null>(null);

  // Account data tabs
  const [activeTab, setActiveTab] = useState<OrderTab>('open');
  const [orders, setOrders] = useState<Order[]>([]);
  const [positions, setPositions] = useState<Position[]>([]);
  const [trades, setTrades] = useState<Trade[]>([]);
  const [dataLoading, setDataLoading] = useState(false);
  const [cancellingId, setCancellingId] = useState<string | null>(null);

  // Load accounts
  useEffect(() => {
    setAccountsLoading(true);
    listAllAccounts()
      .then((all) => {
        setAccounts(all);
        if (all.length > 0) setSelectedAccountId(all[0].id);
      })
      .catch(() => {/* swallow */})
      .finally(() => setAccountsLoading(false));
  }, []);

  // Fetch live price
  const fetchPrice = useCallback(async () => {
    if (!symbol) return;
    setPriceLoading(true);
    try {
      const snap = await getLivePrice(symbol);
      setLivePrice(snap.last_price);
    } catch {
      setLivePrice(null);
    } finally {
      setPriceLoading(false);
    }
  }, [symbol]);

  useEffect(() => {
    void fetchPrice();
    const id = setInterval(() => { void fetchPrice(); }, 10_000);
    return () => clearInterval(id);
  }, [fetchPrice]);

  // Load account data when account changes
  const loadAccountData = useCallback(async () => {
    if (!selectedAccountId) return;
    setDataLoading(true);
    try {
      const [o, p, t] = await Promise.all([
        listOrders(selectedAccountId),
        listPositions(selectedAccountId),
        listTrades(selectedAccountId),
      ]);
      setOrders(o);
      setPositions(p);
      setTrades(t);
    } catch {/* swallow */}
    finally { setDataLoading(false); }
  }, [selectedAccountId]);

  useEffect(() => { void loadAccountData(); }, [loadAccountData]);

  // Place order
  const handlePlaceOrder = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!selectedAccountId) return;
    setSubmitting(true);
    setSubmitResult(null);
    const input: PlaceOrderInput = {
      symbol,
      side,
      position_side: positionSide,
      type: orderType,
      quantity: parseFloat(quantity) || 0,
      leverage: parseFloat(leverage) || 1,
      ...(orderType === 'limit' && price ? { price: parseFloat(price) } : {}),
      ...(orderType === 'stop' && stopPrice ? { stop_price: parseFloat(stopPrice) } : {}),
    };
    try {
      await placeOrder(selectedAccountId, input);
      setSubmitResult({ ok: true, message: 'Order placed successfully.' });
      setQuantity('');
      void loadAccountData();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to place order.';
      setSubmitResult({ ok: false, message: msg });
    } finally {
      setSubmitting(false);
    }
  };

  // Cancel order
  const handleCancel = async (orderId: string) => {
    if (!selectedAccountId) return;
    setCancellingId(orderId);
    try {
      await cancelOrder(selectedAccountId, orderId);
      void loadAccountData();
    } catch {/* swallow */}
    finally { setCancellingId(null); }
  };

  const selectedAccount = accounts.find((a) => a.id === selectedAccountId);
  const openOrders = orders.filter((o) => o.status === 'new' || o.status === 'triggered');

  const inputClass = `w-full px-3 py-2 rounded-lg text-xs border focus:outline-none focus:ring-1 focus:ring-app-accent
    ${isDark ? 'bg-gray-800 border-gray-700 text-gray-100 placeholder-gray-500' : 'bg-white border-gray-200 text-gray-900 placeholder-gray-400'}`;

  const labelClass = `block text-[10px] font-semibold uppercase tracking-wider mb-1 text-app-text-muted`;

  return (
    <div className="p-6 space-y-6">
      {/* Header */}
      <div className="flex items-center space-x-3">
        <div className="p-2 rounded-lg bg-app-accent/10">
          <TrendingUp size={18} className="text-app-accent" />
        </div>
        <div>
          <h1 className="text-lg font-bold text-app-text-main">Trading Console</h1>
          <p className="text-xs text-app-text-muted">Place orders on any account (live or virtual)</p>
        </div>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-3 gap-6">
        {/* ── Left: Order Form ── */}
        <div className={`xl:col-span-1 rounded-xl border p-5 space-y-4
          ${isDark ? 'bg-gray-900 border-gray-800' : 'bg-white border-gray-200'}`}>
          <h2 className="text-sm font-semibold text-app-text-main">Place Order</h2>

          {/* Account selector */}
          <div>
            <label className={labelClass}>Account</label>
            {accountsLoading ? (
              <div className="text-xs text-app-text-muted animate-pulse">Loading accounts…</div>
            ) : (
              <div className="relative">
                <select
                  value={selectedAccountId}
                  onChange={(e) => setSelectedAccountId(e.target.value)}
                  className={`${inputClass} appearance-none pr-8`}
                >
                  {accounts.map((a) => (
                    <option key={a.id} value={a.id}>
                      [{a.type.toUpperCase()}] {a.name} — {a.base_currency}
                    </option>
                  ))}
                </select>
                <ChevronDown size={12} className="absolute right-2 top-2.5 text-app-text-muted pointer-events-none" />
              </div>
            )}
            {selectedAccount && (
              <p className="mt-1 text-[10px] text-app-text-muted">
                Wallet: <span className="font-semibold text-app-text-main">{selectedAccount.wallet_balance.toFixed(2)} {selectedAccount.base_currency}</span>
                {' · '}
                Equity: <span className="font-semibold text-app-text-main">{selectedAccount.equity.toFixed(2)}</span>
              </p>
            )}
          </div>

          <form onSubmit={(e) => { void handlePlaceOrder(e); }} className="space-y-3">
            {/* Symbol */}
            <div>
              <label className={labelClass}>Symbol</label>
              <div className="flex items-center space-x-2">
                <div className="relative flex-1">
                  <select
                    value={symbol}
                    onChange={(e) => setSymbol(e.target.value)}
                    className={`${inputClass} appearance-none pr-8`}
                  >
                    {SUPPORTED_SYMBOLS.map((s) => (
                      <option key={s} value={s}>{s}</option>
                    ))}
                  </select>
                  <ChevronDown size={12} className="absolute right-2 top-2.5 text-app-text-muted pointer-events-none" />
                </div>
                <button type="button" onClick={() => { void fetchPrice(); }}
                  className="p-2 rounded-lg border border-app-border hover:bg-app-accent/10 text-app-text-muted hover:text-app-accent transition-colors"
                  title="Refresh price">
                  <RefreshCw size={12} className={priceLoading ? 'animate-spin' : ''} />
                </button>
              </div>
              {livePrice !== null && (
                <p className="mt-1 text-[10px] text-app-text-muted">
                  Live: <span className="font-bold text-app-accent">${livePrice.toLocaleString()}</span>
                </p>
              )}
            </div>

            {/* Side + Position Side */}
            <div className="grid grid-cols-2 gap-2">
              <div>
                <label className={labelClass}>Side</label>
                <div className="flex rounded-lg overflow-hidden border border-app-border text-xs">
                  {(['buy', 'sell'] as const).map((s) => (
                    <button
                      key={s}
                      type="button"
                      onClick={() => {
                        setSide(s);
                        setPositionSide(s === 'buy' ? 'long' : 'short');
                      }}
                      className={`flex-1 py-1.5 font-semibold transition-colors ${
                        side === s
                          ? s === 'buy'
                            ? 'bg-green-500 text-white'
                            : 'bg-red-500 text-white'
                          : isDark ? 'bg-gray-800 text-gray-400 hover:text-gray-200' : 'bg-gray-50 text-gray-500 hover:text-gray-800'
                      }`}
                    >
                      {s === 'buy' ? <TrendingUp size={10} className="inline mr-1" /> : <TrendingDown size={10} className="inline mr-1" />}
                      {s.toUpperCase()}
                    </button>
                  ))}
                </div>
              </div>
              <div>
                <label className={labelClass}>Direction</label>
                <div className="flex rounded-lg overflow-hidden border border-app-border text-xs">
                  {(['long', 'short'] as const).map((p) => (
                    <button
                      key={p}
                      type="button"
                      onClick={() => setPositionSide(p)}
                      className={`flex-1 py-1.5 font-semibold transition-colors ${
                        positionSide === p
                          ? 'bg-app-accent text-white'
                          : isDark ? 'bg-gray-800 text-gray-400 hover:text-gray-200' : 'bg-gray-50 text-gray-500 hover:text-gray-800'
                      }`}
                    >
                      {p.toUpperCase()}
                    </button>
                  ))}
                </div>
              </div>
            </div>

            {/* Order type */}
            <div>
              <label className={labelClass}>Order Type</label>
              <div className="relative">
                <select
                  value={orderType}
                  onChange={(e) => setOrderType(e.target.value as 'market' | 'limit' | 'stop')}
                  className={`${inputClass} appearance-none pr-8`}
                >
                  <option value="market">Market</option>
                  <option value="limit">Limit</option>
                  <option value="stop">Stop</option>
                </select>
                <ChevronDown size={12} className="absolute right-2 top-2.5 text-app-text-muted pointer-events-none" />
              </div>
            </div>

            {/* Quantity */}
            <div>
              <label className={labelClass}>Quantity</label>
              <input
                type="number"
                min="0"
                step="any"
                value={quantity}
                onChange={(e) => setQuantity(e.target.value)}
                placeholder="0.001"
                required
                className={inputClass}
              />
            </div>

            {/* Limit price */}
            {orderType === 'limit' && (
              <div>
                <label className={labelClass}>Limit Price (USDT)</label>
                <input
                  type="number"
                  min="0"
                  step="any"
                  value={price}
                  onChange={(e) => setPrice(e.target.value)}
                  placeholder={livePrice ? String(livePrice) : '0.00'}
                  className={inputClass}
                />
              </div>
            )}

            {/* Stop price */}
            {orderType === 'stop' && (
              <div>
                <label className={labelClass}>Stop Trigger Price (USDT)</label>
                <input
                  type="number"
                  min="0"
                  step="any"
                  value={stopPrice}
                  onChange={(e) => setStopPrice(e.target.value)}
                  placeholder="0.00"
                  className={inputClass}
                />
              </div>
            )}

            {/* Leverage */}
            <div>
              <label className={labelClass}>Leverage ×{leverage}</label>
              <input
                type="range"
                min="1"
                max="100"
                value={leverage}
                onChange={(e) => setLeverage(e.target.value)}
                className="w-full h-1 accent-app-accent cursor-pointer"
              />
              <div className="flex justify-between text-[9px] text-app-text-muted mt-0.5">
                <span>×1</span><span>×25</span><span>×50</span><span>×100</span>
              </div>
            </div>

            {/* Submit result */}
            {submitResult && (
              <div className={`flex items-center space-x-2 px-3 py-2 rounded-lg text-xs ${
                submitResult.ok
                  ? 'bg-green-500/10 text-green-600 dark:text-green-400'
                  : 'bg-red-500/10 text-red-600 dark:text-red-400'
              }`}>
                {submitResult.ok
                  ? <CheckCircle size={12} />
                  : <AlertTriangle size={12} />}
                <span>{submitResult.message}</span>
              </div>
            )}

            <button
              type="submit"
              disabled={submitting || !selectedAccountId}
              className={`w-full py-2.5 rounded-lg text-xs font-bold transition-colors flex items-center justify-center space-x-2 ${
                side === 'buy'
                  ? 'bg-green-500 hover:bg-green-600 text-white disabled:opacity-50'
                  : 'bg-red-500 hover:bg-red-600 text-white disabled:opacity-50'
              }`}
            >
              {submitting && <Loader size={12} className="animate-spin" />}
              <span>{submitting ? 'Placing…' : `${side.toUpperCase()} ${symbol}`}</span>
            </button>
          </form>
        </div>

        {/* ── Right: Account data ── */}
        <div className={`xl:col-span-2 rounded-xl border
          ${isDark ? 'bg-gray-900 border-gray-800' : 'bg-white border-gray-200'}`}>
          {/* Tab header */}
          <div className="flex border-b border-app-border">
            {([
              { id: 'open', label: `Open Orders (${openOrders.length})` },
              { id: 'positions', label: `Positions (${positions.length})` },
              { id: 'trades', label: `Trades (${trades.length})` },
            ] as { id: OrderTab; label: string }[]).map((tab) => (
              <button
                key={tab.id}
                onClick={() => setActiveTab(tab.id)}
                className={`px-4 py-3 text-xs font-semibold transition-colors border-b-2 -mb-px ${
                  activeTab === tab.id
                    ? 'border-app-accent text-app-accent'
                    : 'border-transparent text-app-text-muted hover:text-app-text-main'
                }`}
              >
                {tab.label}
              </button>
            ))}
            <div className="flex-1" />
            <button
              onClick={() => { void loadAccountData(); }}
              className="px-3 py-2 text-app-text-muted hover:text-app-accent transition-colors"
              title="Refresh"
            >
              <RefreshCw size={12} className={dataLoading ? 'animate-spin' : ''} />
            </button>
          </div>

          {/* Tab content */}
          <div className="p-4 overflow-x-auto">
            {dataLoading ? (
              <div className="flex items-center justify-center py-12 text-app-text-muted text-xs">
                <Loader size={16} className="animate-spin mr-2" /> Loading…
              </div>
            ) : (
              <>
                {activeTab === 'open' && (
                  openOrders.length === 0
                    ? <EmptyState message="No open orders." />
                    : (
                      <table className="w-full text-xs">
                        <thead>
                          <tr className="text-app-text-muted text-left border-b border-app-border">
                            <Th>Symbol</Th><Th>Side</Th><Th>Type</Th><Th>Qty</Th><Th>Price</Th><Th>Status</Th><Th>Action</Th>
                          </tr>
                        </thead>
                        <tbody>
                          {openOrders.map((o) => (
                            <tr key={o.id} className="border-b border-app-border/50 hover:bg-app-bg/40">
                              <Td>{o.symbol}</Td>
                              <Td>
                                <span className={`font-bold ${o.side === 'buy' ? 'text-green-500' : 'text-red-500'}`}>
                                  {o.side.toUpperCase()}
                                </span>
                              </Td>
                              <Td>{o.type}</Td>
                              <Td>{o.quantity}</Td>
                              <Td>{o.price ?? o.fill_price ?? '—'}</Td>
                              <Td>{o.status}</Td>
                              <Td>
                                <button
                                  onClick={() => { void handleCancel(o.id); }}
                                  disabled={cancellingId === o.id}
                                  className="p-1 rounded hover:bg-red-500/10 text-red-500 disabled:opacity-50 transition-colors"
                                  title="Cancel order"
                                >
                                  {cancellingId === o.id
                                    ? <Loader size={10} className="animate-spin" />
                                    : <XCircle size={10} />}
                                </button>
                              </Td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    )
                )}

                {activeTab === 'positions' && (
                  positions.length === 0
                    ? <EmptyState message="No open positions." />
                    : (
                      <table className="w-full text-xs">
                        <thead>
                          <tr className="text-app-text-muted text-left border-b border-app-border">
                            <Th>Symbol</Th><Th>Direction</Th><Th>Qty</Th><Th>Entry</Th><Th>Leverage</Th><Th>Unr. PnL</Th><Th>Real. PnL</Th>
                          </tr>
                        </thead>
                        <tbody>
                          {positions.map((p) => (
                            <tr key={p.id} className="border-b border-app-border/50 hover:bg-app-bg/40">
                              <Td>{p.symbol}</Td>
                              <Td>
                                <span className={`font-bold ${p.position_side === 'long' ? 'text-green-500' : 'text-red-500'}`}>
                                  {p.position_side.toUpperCase()}
                                </span>
                              </Td>
                              <Td>{p.quantity}</Td>
                              <Td>{p.avg_entry_price.toFixed(4)}</Td>
                              <Td>×{p.leverage}</Td>
                              <Td className={p.unrealized_pnl >= 0 ? 'text-green-500' : 'text-red-500'}>
                                {p.unrealized_pnl >= 0 ? '+' : ''}{p.unrealized_pnl.toFixed(4)}
                              </Td>
                              <Td className={p.realized_pnl >= 0 ? 'text-green-500' : 'text-red-500'}>
                                {p.realized_pnl >= 0 ? '+' : ''}{p.realized_pnl.toFixed(4)}
                              </Td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    )
                )}

                {activeTab === 'trades' && (
                  trades.length === 0
                    ? <EmptyState message="No trade history." />
                    : (
                      <table className="w-full text-xs">
                        <thead>
                          <tr className="text-app-text-muted text-left border-b border-app-border">
                            <Th>Symbol</Th><Th>Side</Th><Th>Qty</Th><Th>Fill Price</Th><Th>PnL</Th><Th>Time</Th>
                          </tr>
                        </thead>
                        <tbody>
                          {trades.map((t) => (
                            <tr key={t.id} className="border-b border-app-border/50 hover:bg-app-bg/40">
                              <Td>{t.symbol}</Td>
                              <Td>
                                <span className={`font-bold ${t.side === 'buy' ? 'text-green-500' : 'text-red-500'}`}>
                                  {t.side.toUpperCase()}
                                </span>
                              </Td>
                              <Td>{t.quantity}</Td>
                              <Td>{t.price.toFixed(4)}</Td>
                              <Td className={t.realized_pnl >= 0 ? 'text-green-500' : 'text-red-500'}>
                                {t.realized_pnl >= 0 ? '+' : ''}{t.realized_pnl.toFixed(4)}
                              </Td>
                              <Td>{new Date(t.executed_at).toLocaleString()}</Td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    )
                )}
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

// ── Tiny helper components ────────────────────────────────────────────────────

function Th({ children }: { children: React.ReactNode }) {
  return <th className="py-2 pr-4 font-semibold text-[10px] uppercase tracking-wider">{children}</th>;
}

function Td({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return <td className={`py-2 pr-4 text-app-text-main ${className}`}>{children}</td>;
}

function EmptyState({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-12 text-app-text-muted space-y-2">
      <AlertTriangle size={24} className="opacity-30" />
      <span className="text-xs">{message}</span>
    </div>
  );
}

// Keep INTERVALS in scope to silence unused import lint warnings — used in future iteration
void INTERVALS;
