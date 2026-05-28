import { describe, expect, it, vi } from 'vitest';

import { listLiveSymbols, mapLiveSymbolSnapshot, type LiveSymbolSnapshot } from './systemHealth';

describe('system health API service', () => {
  it('loads live symbol snapshots from the backend monitor endpoint', async () => {
    const request = vi.fn().mockResolvedValueOnce({ summary: { active_symbols: 0 }, items: [] });

    await expect(listLiveSymbols({ request })).resolves.toEqual([]);

    expect(request).toHaveBeenCalledWith('/admin/monitor/live-symbols');
  });

  it('maps live symbol freshness and provider into UI health rows', async () => {
    const request = vi.fn().mockResolvedValueOnce({
      summary: { active_symbols: 1, stale_symbols: 0, degraded_symbols: 0, gc_removals: 0 },
      items: [
        liveSymbolSnapshot({
          symbol: 'BTCUSDT',
          provider: 'binance',
          state: 'active',
          freshness_ms: 42,
          last_updated: '2026-01-01T00:00:00Z',
          subscriber_count: 3,
        }),
      ],
    });

    await expect(listLiveSymbols({ request })).resolves.toEqual([
      {
        symbol: 'BTCUSDT',
        feedSource: 'binance',
        latencyMs: 42,
        status: 'Healthy',
        rate: 3,
        updated: '2026-01-01T00:00:00Z',
      },
    ]);
  });
});

describe('mapLiveSymbolSnapshot', () => {
  it.each([
    ['active', 'Healthy'],
    ['stale', 'Stale'],
    ['degraded', 'Critical'],
  ] as const)('maps live symbol state %s to %s', (state, status) => {
    expect(mapLiveSymbolSnapshot(liveSymbolSnapshot({ state })).status).toBe(status);
  });
});

function liveSymbolSnapshot(overrides: Partial<LiveSymbolSnapshot> = {}): LiveSymbolSnapshot {
  return {
    symbol: 'ETHUSDT',
    state: 'active',
    price: 100,
    last_price: 100,
    freshness_ms: 0,
    subscriber_count: 1,
    provider: 'static',
    last_updated: '2026-01-01T00:00:00Z',
    received_at: '2026-01-01T00:00:00Z',
    last_accessed: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}
