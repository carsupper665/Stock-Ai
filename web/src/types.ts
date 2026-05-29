export type Theme = 'light' | 'dark';

export type ConnectionState = 'Connected' | 'Reconnecting' | 'Disconnected' | 'Fallback Active' | 'Stale Data';

export interface Dataset {
  id: string;
  name: string;
  symbol: string;
  interval: string;
  timeRange: string;
  source: string;
  status: 'Imported' | 'Importing' | 'Failed' | 'Metadata Only';
  updated: string;
  size?: string;
}

export interface SandboxAccount {
  id: string;
  name: string;
  sandboxId: string;
  sandboxName: string;
  balance: number;
  equity: number;
  margin: number;
  status: 'Active' | 'Inactive' | 'Pending';
  token?: string;
  tokenId?: string;
  tokenRevealed?: boolean;
  tokenRevoked?: boolean;
  updated: string;
}

export interface LiveAccount {
  id: string;
  name: string;
  provider: string;
  environment?: string;
  priceMode: string;
  credentialsStatus?: string;
  supportedSymbols: readonly string[];
  baseCurrency: string;
  initialBalance: number;
  walletBalance: number;
  availableBalance: number;
  lockedMargin: number;
  realizedPnL: number;
  unrealizedPnL: number;
  equity: number;
  liveTradingEnabled: boolean;
  status: 'Connected' | 'Inactive' | 'Stale';
  created: string;
  updated: string;
}

export interface Sandbox {
  id: string;
  name: string;
  mode: 'Replay' | 'Live';
  status: 'Running' | 'Paused' | 'Stopped' | 'Stale' | 'Failed';
  datasetId?: string;
  datasetName?: string;
  accountsCount: number;
  replayTime: string;
  freshness: string;
  updated: string;
  equityTrend?: { time: string; value: number }[];
}

export interface SystemSymbol {
  symbol: string;
  feedSource: string;
  latencyMs: number;
  status: 'Healthy' | 'Stale' | 'Critical';
  rate: number;
  updated: string;
}

export interface OperationalLog {
  id: string;
  timestamp: string;
  topic: 'System' | 'Sandbox' | 'Order' | 'Trade' | 'Agent';
  severity: 'Info' | 'Warning' | 'Critical' | 'Success';
  title: string;
  sandboxLabel?: string;
  details: string;
}

export interface SystemAlert {
  id: string;
  title: string;
  explanation: string;
  severity: 'Critical' | 'Warning' | 'Info' | 'Resolved';
  timestamp: string;
  sandbox?: string;
}

export type ActivePage =
  | 'overview'
  | 'datasets'
  | 'sandbox-accounts'
  | 'live-accounts'
  | 'system-health'
  | 'sandboxes'
  | 'sandbox-detail'
  | 'sandbox-monitor'
  | 'performance'
  | 'activity'
  | 'alerts'
  | 'agent-boundary'
  | 'trading-console'
  | 'technical-analysis';

export type ViewState = 'normal' | 'loading' | 'error';

// ─── Trading domain types ────────────────────────────────────────────────────

export interface AdminAccount {
  readonly id: string;
  readonly name: string;
  readonly type: 'live' | 'virtual';
  readonly status: string;
  readonly base_currency: string;
  readonly wallet_balance: number;
  readonly available_balance: number;
  readonly equity: number;
  readonly sandbox_id?: string;
  readonly environment?: string;
  readonly provider?: string;
  readonly price_mode?: string;
}

export interface Order {
  readonly id: string;
  readonly account_id: string;
  readonly symbol: string;
  readonly side: 'buy' | 'sell';
  readonly position_side: 'long' | 'short';
  readonly type: 'market' | 'limit' | 'stop';
  readonly quantity: number;
  readonly price?: number;
  readonly stop_price?: number;
  readonly leverage: number;
  readonly status: string;
  readonly filled_quantity: number;
  readonly fill_price?: number;
  readonly created_at: string;
  readonly updated_at: string;
}

export interface Position {
  readonly id: string;
  readonly account_id: string;
  readonly symbol: string;
  readonly position_side: 'long' | 'short';
  readonly quantity: number;
  readonly avg_entry_price: number;
  readonly unrealized_pnl: number;
  readonly realized_pnl: number;
  readonly leverage: number;
  readonly updated_at: string;
}

export interface Trade {
  readonly id: string;
  readonly account_id: string;
  readonly order_id: string;
  readonly symbol: string;
  readonly side: 'buy' | 'sell';
  readonly position_side: 'long' | 'short';
  readonly quantity: number;
  readonly price: number;
  readonly realized_pnl: number;
  readonly executed_at: string;
}

export interface AccountToken {
  readonly id: string;
  readonly account_id: string;
  readonly token_name: string;
  readonly scope: string;
  readonly expires_at?: string;
  readonly last_used_at?: string;
}

export interface Kline {
  readonly symbol: string;
  readonly at: string;
  readonly open: number;
  readonly high: number;
  readonly low: number;
  readonly close: number;
  readonly volume: number;
}

export interface IndicatorPoint {
  readonly time: string;
  readonly value: number;
}

export interface IndicatorsResponse {
  readonly symbol: string;
  readonly interval: string;
  readonly indicators: Record<string, IndicatorPoint[]>;
}
