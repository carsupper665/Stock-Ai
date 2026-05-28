import { ApiClient } from './client';
import type { SystemSymbol } from '../types';

export interface LiveSymbolSnapshot {
  readonly symbol: string;
  readonly state: string;
  readonly price: number;
  readonly last_price: number;
  readonly freshness_ms: number;
  readonly subscriber_count: number;
  readonly provider: string;
  readonly last_updated?: string;
  readonly received_at?: string;
  readonly last_accessed?: string;
}

interface LiveSymbolsResponse {
  readonly summary?: {
    readonly active_symbols?: number;
    readonly stale_symbols?: number;
    readonly degraded_symbols?: number;
    readonly gc_removals?: number;
  };
  readonly items?: LiveSymbolSnapshot[];
}

type SystemHealthClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export async function listLiveSymbols(client: SystemHealthClient = defaultClient): Promise<SystemSymbol[]> {
  const response = await client.request<LiveSymbolsResponse>('/admin/monitor/live-symbols');
  return (response.items ?? []).map(mapLiveSymbolSnapshot);
}

export function mapLiveSymbolSnapshot(snapshot: LiveSymbolSnapshot): SystemSymbol {
  return {
    symbol: snapshot.symbol,
    feedSource: snapshot.provider || 'unknown',
    latencyMs: snapshot.freshness_ms,
    status: mapSymbolStatus(snapshot.state),
    rate: snapshot.subscriber_count,
    updated: snapshot.last_updated || snapshot.received_at || snapshot.last_accessed || 'Unknown',
  };
}

function mapSymbolStatus(state: string): SystemSymbol['status'] {
  if (state === 'active') {
    return 'Healthy';
  }
  if (state === 'degraded') {
    return 'Critical';
  }
  return 'Stale';
}
