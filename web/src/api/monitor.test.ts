import { describe, expect, it, vi } from 'vitest';

import { ApiClient, ApiError } from './client';
import { getSandboxMonitorSnapshot, mapSandboxMonitorSnapshot, type SandboxMonitorSnapshotResponse } from './monitor';

describe('monitor API service', () => {
  it('loads a sandbox monitor snapshot through the exact admin monitor route', async () => {
    const request = vi.fn().mockResolvedValueOnce(snapshotResponse());

    await getSandboxMonitorSnapshot('sandbox-1', { request });

    expect(request).toHaveBeenCalledWith('/admin/monitor/sandboxes/sandbox-1/snapshot');
  });

  it('maps empty backend snapshot arrays without synthesizing fake monitor data', async () => {
    const request = vi.fn().mockResolvedValueOnce(snapshotResponse());

    await expect(getSandboxMonitorSnapshot('sandbox-1', { request })).resolves.toEqual({
      sandboxId: 'sandbox-1',
      replayCurrentTime: '2026-01-01T00:30:00Z',
      replaySpeed: 1,
      status: 'ready',
      accounts: [],
      orders: [],
      trades: [],
      positions: [],
      indicators: {},
      freshnessAt: '2026-01-01T00:30:00Z',
      equityTrend: undefined,
    });
  });

  it('lets ApiClient trigger onUnauthorized for unauthorized monitor snapshots', async () => {
    const onUnauthorized = vi.fn();
    const client = new ApiClient({
      onUnauthorized,
      fetchImpl: vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'UNAUTHORIZED', message: 'admin login required', details: null }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    });

    await expect(getSandboxMonitorSnapshot('sandbox-1', client)).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });
});

describe('mapSandboxMonitorSnapshot', () => {
  it('preserves backend indicators when present but does not invent equity trend points', () => {
    expect(
      mapSandboxMonitorSnapshot(
        snapshotResponse({
          indicators: {
            SMA: [{ ts: '2026-01-01T00:00:00Z', value: 100 }],
          },
        }),
      ),
    ).toMatchObject({
      indicators: { SMA: [{ ts: '2026-01-01T00:00:00Z', value: 100 }] },
      equityTrend: undefined,
    });
  });
});

function snapshotResponse(
  overrides: Partial<SandboxMonitorSnapshotResponse> = {},
): SandboxMonitorSnapshotResponse {
  return {
    sandbox: {
      id: 'sandbox-1',
      name: 'Replay Sandbox',
      mode: 'replay',
      status: 'ready',
      start_datetime: '2026-01-01T00:00:00Z',
      replay_current_time: '2026-01-01T00:30:00Z',
      replay_speed: 1,
      dataset_id: 'dataset-1',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:30:00Z',
    },
    replay_control: {
      sandbox_id: 'sandbox-1',
      current_time: '2026-01-01T00:30:00Z',
      speed: 1,
      status: 'ready',
    },
    accounts: [],
    orders: [],
    trades: [],
    positions: [],
    freshness_at: '2026-01-01T00:30:00Z',
    ...overrides,
  };
}
