import { Dataset, SandboxAccount, LiveAccount, Sandbox, SystemSymbol, OperationalLog, SystemAlert } from '../types';

export const initialDatasets: Dataset[] = [
  {
    id: 'ds-01',
    name: 'BTC-USDT-Q1-Replay',
    symbol: 'BTCUSDT',
    interval: '1m',
    timeRange: '2026-01-01 to 2026-03-31',
    source: 'Binance Historical',
    status: 'Imported',
    updated: '2026-05-24 12:00 UTC',
    size: '1.2 GB'
  },
  {
    id: 'ds-02',
    name: 'ETH-USDT-May-Micro',
    symbol: 'ETHUSDT',
    interval: '1s',
    timeRange: '2026-05-01 to 2026-05-15',
    source: 'Coinbase HighFreq',
    status: 'Imported',
    updated: '2026-05-24 14:15 UTC',
    size: '800 MB'
  },
  {
    id: 'ds-03',
    name: 'SOL-USDT-Active-Vol',
    symbol: 'SOLUSDT',
    interval: '5m',
    timeRange: '2026-04-10 to 2026-05-10',
    source: 'OKX Archive',
    status: 'Importing',
    updated: '2026-05-24 19:30 UTC',
    size: '450 MB'
  },
  {
    id: 'ds-04',
    name: 'XRP-USDT-Erroneous-Ticks',
    symbol: 'XRPUSDT',
    interval: '1m',
    timeRange: '2026-02-01 to 2026-02-05',
    source: 'Kraken Local',
    status: 'Failed',
    updated: '2026-05-24 10:05 UTC',
    size: '12 MB'
  }
];

export const initialSandboxAccounts: SandboxAccount[] = [
  {
    id: 'acc-alpha-01',
    name: 'Alpha Replay Exec',
    sandboxId: 'sb-01',
    sandboxName: 'Sandbox Alpha (Replay)',
    balance: 10000.00,
    equity: 10245.82,
    margin: 150.00,
    status: 'Active',
    token: 'sb_tok_9F82A1B7D504E2',
    tokenRevealed: false,
    updated: '2026-05-24 19:33 UTC'
  },
  {
    id: 'acc-alpha-02',
    name: 'Alpha Grid Tester',
    sandboxId: 'sb-01',
    sandboxName: 'Sandbox Alpha (Replay)',
    balance: 50000.00,
    equity: 49812.40,
    margin: 2400.00,
    status: 'Active',
    token: 'sb_tok_2B01E2E4A9F202',
    tokenRevealed: false,
    updated: '2026-05-24 19:32 UTC'
  },
  {
    id: 'acc-beta-01',
    name: 'Beta Live-Replay Dual',
    sandboxId: 'sb-02',
    sandboxName: 'Sandbox Beta (Live)',
    balance: 5000.00,
    equity: 5120.35,
    margin: 0.00,
    status: 'Active',
    token: 'sb_tok_1E5A5A9C0D2F3E',
    tokenRevealed: false,
    updated: '2026-05-24 19:30 UTC'
  },
  {
    id: 'acc-gamma-01',
    name: 'Gamma Inert Shadow',
    sandboxId: 'sb-03',
    sandboxName: 'Sandbox Gamma (Stopped)',
    balance: 25000.00,
    equity: 25000.00,
    margin: 0.00,
    status: 'Inactive',
    token: 'sb_tok_8A2C7D4E5B1A2C',
    tokenRevealed: false,
    updated: '2026-05-24 18:00 UTC'
  }
];

export const initialLiveAccounts: LiveAccount[] = [
  {
    id: 'live-bin-01',
    name: 'Binance Primary-Sub01',
    exchange: 'Binance Futures',
    label: 'PROD_MIGRATE_A',
    status: 'Connected',
    created: '2026-01-10',
    updated: '2026-05-24 19:30 UTC'
  },
  {
    id: 'live-okx-01',
    name: 'OKX Operations Active',
    exchange: 'OKX Spot',
    label: 'CORP_OPS_MAIN',
    status: 'Connected',
    created: '2026-02-14',
    updated: '2026-05-24 19:31 UTC'
  },
  {
    id: 'live-cby-01',
    name: 'Coinbase Institutional Ref',
    exchange: 'Coinbase Advanced',
    label: 'METADATA_ONLY_VAL',
    status: 'Stale',
    created: '2026-04-01',
    updated: '2026-05-24 18:45 UTC'
  }
];

export const initialSandboxes: Sandbox[] = [
  {
    id: 'sb-01',
    name: 'Sandbox Alpha (Replay)',
    mode: 'Replay',
    status: 'Running',
    datasetId: 'ds-01',
    datasetName: 'BTC-USDT-Q1-Replay',
    accountsCount: 2,
    replayTime: '2026-01-05 08:24:00 UTC',
    freshness: '2.5s latency (replay rate: 50x)',
    updated: '2026-05-24 19:33 UTC',
    equityTrend: [
      { time: '08:00', value: 10000 },
      { time: '08:05', value: 10050 },
      { time: '08:10', value: 10120 },
      { time: '08:15', value: 10090 },
      { time: '08:20', value: 10210 },
      { time: '08:24', value: 10245 }
    ]
  },
  {
    id: 'sb-02',
    name: 'Sandbox Beta (Live Feed)',
    mode: 'Live',
    status: 'Paused',
    datasetId: 'ds-02',
    datasetName: 'ETH-USDT-May-Micro',
    accountsCount: 1,
    replayTime: 'Realtime Live Proxy feed',
    freshness: '120ms network latency (Paused)',
    updated: '2026-05-24 19:30 UTC',
    equityTrend: [
      { time: '19:00', value: 5000 },
      { time: '19:10', value: 5020 },
      { time: '19:20', value: 5015 },
      { time: '19:30', value: 5120 }
    ]
  },
  {
    id: 'sb-03',
    name: 'Sandbox Gamma (Archived)',
    mode: 'Replay',
    status: 'Stopped',
    datasetId: 'ds-01',
    datasetName: 'BTC-USDT-Q1-Replay',
    accountsCount: 1,
    replayTime: '2026-01-01 02:00:00 UTC',
    freshness: 'Static Archive',
    updated: '2026-05-24 18:00 UTC',
    equityTrend: [
      { time: '01:00', value: 25000 },
      { time: '02:00', value: 25000 }
    ]
  }
];

export const initialSymbols: SystemSymbol[] = [
  { symbol: 'BTCUSDT', feedSource: 'Binance Live-Feed', latencyMs: 22, status: 'Healthy', rate: 245, updated: '1s ago' },
  { symbol: 'ETHUSDT', feedSource: 'Coinbase Live-Feed', latencyMs: 34, status: 'Healthy', rate: 112, updated: 'Just now' },
  { symbol: 'SOLUSDT', feedSource: 'Kraken Live-Feed', latencyMs: 147, status: 'Healthy', rate: 98, updated: '3s ago' },
  { symbol: 'XRPUSDT', feedSource: 'OKX Archive-Cache', latencyMs: 981, status: 'Stale', rate: 5, updated: '18m ago' },
  { symbol: 'ADAUSDT', feedSource: 'Binance Live-Feed', latencyMs: 0, status: 'Critical', rate: 0, updated: '2h ago' }
];

export const initialLogs: OperationalLog[] = [
  {
    id: 'log-1',
    timestamp: '2026-05-24 19:33:45',
    topic: 'Sandbox',
    severity: 'Success',
    title: 'Sandbox Alpha (Replay) advanced 50 cycles',
    sandboxLabel: 'Sandbox Alpha',
    details: 'Replay current pointer successfully synced to timestamp 2026-01-05 08:24:00 UTC'
  },
  {
    id: 'log-2',
    timestamp: '2026-05-24 19:33:10',
    topic: 'Agent',
    severity: 'Info',
    title: 'Agent token authorized order execution',
    sandboxLabel: 'Sandbox Alpha',
    details: 'Token sb_tok_9F82A1B7D504E2 matched scope boundaries. Max size parameters valid.'
  },
  {
    id: 'log-3',
    timestamp: '2026-05-24 19:32:00',
    topic: 'Order',
    severity: 'Success',
    title: 'Limit Buy Order Filled — 0.45 BTC',
    sandboxLabel: 'Sandbox Alpha',
    details: 'Price: $67,240.50 | Balance updated for account alpha-01'
  },
  {
    id: 'log-4',
    timestamp: '2026-05-24 19:25:22',
    topic: 'Agent',
    severity: 'Warning',
    title: 'Agent rule trigger: Margin threshold approached',
    sandboxLabel: 'Sandbox Alpha',
    details: 'Account alpha-02 margin utilization hit 4.8%. Under the allowed safety bound of 10.0% max.'
  },
  {
    id: 'log-5',
    timestamp: '2026-05-24 19:15:00',
    topic: 'System',
    severity: 'Warning',
    title: 'XRPUSDT stream lag detected',
    details: 'High latency (981ms) on OKX Archive-Cache feed source. Replaced with local state backup.'
  },
  {
    id: 'log-6',
    timestamp: '2026-05-24 18:30:11',
    topic: 'Agent',
    severity: 'Critical',
    title: 'Boundary Violations Prevented',
    sandboxLabel: 'Sandbox Beta',
    details: 'Agent token sb_tok_1E5A5A9C0D2F3E attempted to register a live webhook withdrawal. Blocked by Kernel Agent Boundary Module.'
  }
];

export const initialAlerts: SystemAlert[] = [
  {
    id: 'al-1',
    title: 'Agent Boundary Violation Blocked',
    explanation: 'Agent token tried to call a non-sandboxed withdrawal. Blocked successfully.',
    severity: 'Critical',
    timestamp: '2026-05-24 18:30:11',
    sandbox: 'Sandbox Beta (Live Feed)'
  },
  {
    id: 'al-2',
    title: 'Feed latency warning: OKX Archive-Cache',
    explanation: 'Staleness detected on OKX feed ticker XRPUSDT (>900ms). Operations degraded to local DB.',
    severity: 'Warning',
    timestamp: '2026-05-24 19:15:00'
  },
  {
    id: 'al-3',
    title: 'Sandbox Alpha (Replay) Margin warning',
    explanation: 'Interactive grid accounts approached boundary parameters for margin rules (>4.5%).',
    severity: 'Info',
    timestamp: '2026-05-24 19:32:00',
    sandbox: 'Sandbox Alpha (Replay)'
  },
  {
    id: 'al-4',
    title: 'XRP Dataset Cache Sync completed',
    explanation: 'Recovered background file headers after temporary network split.',
    severity: 'Resolved',
    timestamp: '2026-05-24 17:10:00'
  }
];
