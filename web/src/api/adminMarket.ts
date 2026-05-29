import { ApiClient } from './client';
import type { Kline, IndicatorsResponse } from '../types';

type AdminMarketClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

// ── Price snapshot ────────────────────────────────────────────────────────────

export interface LivePriceSnapshot {
  readonly symbol: string;
  readonly last_price: number;
  readonly bid?: number;
  readonly ask?: number;
  readonly at: string;
  readonly freshness_ms: number;
  readonly state: string;
}

export async function getLivePrice(
  symbol: string,
  client: AdminMarketClient = defaultClient,
): Promise<LivePriceSnapshot> {
  return client.request<LivePriceSnapshot>(`/admin/live/price?symbol=${encodeURIComponent(symbol)}`);
}

// ── Klines ────────────────────────────────────────────────────────────────────

interface KlineListResponse {
  readonly items: Kline[];
}

export async function getLiveKlines(
  symbol: string,
  interval: string = '1h',
  client: AdminMarketClient = defaultClient,
): Promise<Kline[]> {
  const params = new URLSearchParams({ symbol, interval });
  const response = await client.request<KlineListResponse>(`/admin/live/klines?${params.toString()}`);
  return response.items;
}

// ── Technical indicators ──────────────────────────────────────────────────────

export async function getLiveIndicators(
  symbol: string,
  interval: string = '1h',
  client: AdminMarketClient = defaultClient,
): Promise<IndicatorsResponse> {
  const params = new URLSearchParams({ symbol, interval });
  return client.request<IndicatorsResponse>(`/admin/live/indicators?${params.toString()}`);
}
