export interface AdminUser {
  id: number
  username: string
  role: number
}

export interface ListResponse<T> {
  items: T[]
}

export interface ReplayControlStatus {
  sandbox_id: string
  current_time: string
  speed: number
  status: string
}

export interface Sandbox {
  id: string
  name: string
  mode: string
  status: string
  start_datetime: string
  replay_current_time: string
  replay_speed: number
  dataset_id: string
  runtime_anchor_at?: string | null
  created_at: string
  updated_at: string
}

export interface DatasetImportJob {
  id: string
  dataset_id: string
  file_name: string
  status: string
  error_summary?: string
  rows_total: number
  rows_imported: number
  started_at?: string | null
  finished_at?: string | null
  created_at: string
  updated_at: string
}

export interface ReplayDataset {
  id: string
  name: string
  symbol: string
  interval: string
  start_at: string
  end_at: string
  source: string
  created_at: string
}

export interface DatasetSummary extends ReplayDataset {
  row_count: number
  latest_import_job?: DatasetImportJob | null
}

export interface Account {
  id: string
  sandbox_id?: string | null
  name: string
  type: 'virtual' | 'live'
  provider?: string
  environment?: string
  price_mode?: string
  credentials_status?: string
  supported_symbols?: string[]
  last_health_check_at?: string | null
  base_currency: string
  initial_balance: number
  wallet_balance: number
  available_balance: number
  locked_margin: number
  realized_pnl: number
  unrealized_pnl: number
  equity: number
  status: string
  created_at: string
  updated_at: string
}

export interface Order {
  id: string
  sandbox_id: string
  account_id: string
  symbol: string
  side: string
  position_side: string
  order_type: string
  qty: number
  price?: number
  stop_price?: number
  leverage: number
  status: string
  filled_qty: number
  avg_fill_price: number
  reduce_only: boolean
  triggered_at?: string | null
  rejection?: string
  created_at: string
  updated_at: string
}

export interface Trade {
  id: string
  order_id: string
  account_id: string
  sandbox_id: string
  symbol: string
  qty: number
  price: number
  fee: number
  side: string
  position_side: string
  executed_at: string
}

export interface Position {
  id: string
  account_id: string
  sandbox_id: string
  symbol: string
  position_side: string
  qty: number
  entry_price: number
  mark_price: number
  leverage: number
  margin_used: number
  unrealized_pnl: number
  updated_at: string
}

export interface SandboxSnapshot {
  sandbox: Sandbox
  replay_control?: ReplayControlStatus | null
  dataset?: DatasetSummary | null
  accounts: Account[]
  orders: Order[]
  trades: Trade[]
  positions: Position[]
  freshness_at: string
}

export interface LivePriceSnapshot {
  symbol: string
  state: string
  price: number
  last_updated?: string
  last_accessed?: string
  subscriber_count: number
  provider: string
  error?: string
}

export interface LiveSymbolsSummary {
  active_symbols: number
  stale_symbols: number
  degraded_symbols: number
  gc_removals: number
}

export interface LiveSymbolsSnapshot {
  summary: LiveSymbolsSummary
  items: LivePriceSnapshot[]
}

export interface DomainEvent {
  topic: string
  sandbox_id?: string
  account_id?: string
  aggregate_id?: string
  payload?: Record<string, unknown>
  created_at: string
}

export interface ApiErrorShape {
  code: string
  message: string
  details?: Record<string, unknown>
}

export interface TokenReveal {
  tokenId: string
  token: string
  accountId: string
  scopes?: string[]
}

export interface AlertItem {
  id: string
  severity: 'info' | 'warning' | 'critical'
  title: string
  message: string
  topic: string
  created_at: string
  sandbox_id?: string
}

export interface ChartPoint {
  timestamp: string
  value: number
}
