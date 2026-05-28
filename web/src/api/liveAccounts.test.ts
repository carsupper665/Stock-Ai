import { describe, expect, it, vi } from 'vitest';

import {
  createLiveAccount,
  deleteLiveAccount,
  getLiveAccount,
  listLiveAccounts,
  mapLiveAccountSummary,
  updateLiveAccount,
  type LiveAccountSummary,
} from './liveAccounts';

describe('live account API service', () => {
  it('lists live account metadata from the backend without mock fallback', async () => {
    const request = vi.fn().mockResolvedValueOnce({ items: [] });

    await expect(listLiveAccounts({ request })).resolves.toEqual([]);

    expect(request).toHaveBeenCalledWith('/admin/live-accounts');
  });

  it('creates live account metadata and forces the backend live account route', async () => {
    const request = vi.fn().mockResolvedValueOnce(liveAccountSummary({ id: 'live-1', name: 'Binance Readonly' }));

    const account = await createLiveAccount(
      {
        name: 'Binance Readonly',
        initialBalance: 10000,
        baseCurrency: 'USDT',
        provider: 'binance',
        environment: 'prod-readonly',
        priceMode: 'live',
        credentialsStatus: 'metadata-only',
        supportedSymbols: ['BTCUSDT'],
      },
      { request },
    );

    expect(request).toHaveBeenCalledWith('/admin/live-accounts', {
      method: 'POST',
      body: {
        name: 'Binance Readonly',
        initial_balance: 10000,
        base_currency: 'USDT',
        provider: 'binance',
        environment: 'prod-readonly',
        price_mode: 'live',
        credentials_status: 'metadata-only',
        supported_symbols: ['BTCUSDT'],
      },
    });
    expect(account).toMatchObject({ id: 'live-1', provider: 'binance', credentialsStatus: 'metadata-only' });
  });

  it('gets, updates, and deletes live account metadata through live-account routes', async () => {
    const request = vi.fn().mockResolvedValue(liveAccountSummary());

    await getLiveAccount('live-1', { request });
    await updateLiveAccount('live-1', { name: 'Renamed', credentialsStatus: 'verified' }, { request });
    await deleteLiveAccount('live-1', { request });

    expect(request).toHaveBeenNthCalledWith(1, '/admin/live-accounts/live-1');
    expect(request).toHaveBeenNthCalledWith(2, '/admin/live-accounts/live-1', {
      method: 'PATCH',
      body: { name: 'Renamed', credentials_status: 'verified' },
    });
    expect(request).toHaveBeenNthCalledWith(3, '/admin/live-accounts/live-1', { method: 'DELETE' });
  });
});

describe('mapLiveAccountSummary', () => {
  it.each([
    ['active', 'Connected'],
    ['disabled', 'Inactive'],
    ['stale', 'Stale'],
  ] as const)('maps backend status %s to %s', (backendStatus, uiStatus) => {
    expect(mapLiveAccountSummary(liveAccountSummary({ status: backendStatus })).status).toBe(uiStatus);
  });
});

function liveAccountSummary(overrides: Partial<LiveAccountSummary> = {}): LiveAccountSummary {
  return {
    id: 'live-id',
    name: 'Live Metadata',
    type: 'live',
    provider: 'binance',
    environment: 'prod-readonly',
    price_mode: 'live',
    credentials_status: 'metadata-only',
    supported_symbols: ['BTCUSDT'],
    base_currency: 'USDT',
    initial_balance: 10000,
    wallet_balance: 10000,
    available_balance: 10000,
    locked_margin: 0,
    realized_pnl: 0,
    unrealized_pnl: 0,
    equity: 10000,
    live_trading_enabled: false,
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:30:00Z',
    ...overrides,
  };
}
