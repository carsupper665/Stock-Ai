export type Theme = 'light' | 'dark';

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
  tokenRevealed?: boolean;
  tokenRevoked?: boolean;
  updated: string;
}

export interface LiveAccount {
  id: string;
  name: string;
  exchange: string;
  label: string;
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
  | 'agent-boundary';

export type DemoVisualState = 'normal' | 'loading' | 'empty' | 'error' | 'disconnected';
