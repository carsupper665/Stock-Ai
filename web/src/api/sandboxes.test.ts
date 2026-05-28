import { describe, expect, it, vi } from 'vitest';

import {
  createSandbox,
  deleteSandbox,
  getSandbox,
  listSandboxes,
  mapSandboxSummary,
  pauseSandbox,
  resumeSandboxReplay,
  seekSandboxReplay,
  setSandboxReplaySpeed,
  startSandbox,
  stopSandbox,
  updateSandbox,
  type SandboxSummary,
} from './sandboxes';

describe('sandbox API service', () => {
  it('lists backend sandboxes without mock fallback', async () => {
    const request = vi.fn().mockResolvedValueOnce({ items: [] });

    await expect(listSandboxes({ request })).resolves.toEqual([]);

    expect(request).toHaveBeenCalledWith('/admin/sandboxes');
  });

  it('creates sandbox metadata with the backend payload', async () => {
    const request = vi.fn().mockResolvedValueOnce(sandboxSummary({ id: 'sandbox-1', name: 'Replay Sandbox' }));

    const sandbox = await createSandbox(
      {
        name: 'Replay Sandbox',
        datasetId: 'dataset-1',
        startDatetime: '2026-01-01T00:00:00Z',
        replayCurrentTime: '2026-01-01T00:00:00Z',
        replaySpeed: 2,
      },
      { request },
    );

    expect(request).toHaveBeenCalledWith('/admin/sandboxes', {
      method: 'POST',
      body: {
        name: 'Replay Sandbox',
        dataset_id: 'dataset-1',
        start_datetime: '2026-01-01T00:00:00Z',
        replay_current_time: '2026-01-01T00:00:00Z',
        replay_speed: 2,
      },
    });
    expect(sandbox).toMatchObject({ id: 'sandbox-1', name: 'Replay Sandbox', status: 'Stopped' });
  });

  it('uses exact sandbox CRUD endpoints', async () => {
    const request = vi.fn().mockResolvedValue(sandboxSummary());

    await getSandbox('sandbox-1', { request });
    await updateSandbox('sandbox-1', { name: 'Renamed', datasetId: 'dataset-2' }, { request });
    await deleteSandbox('sandbox-1', { request });

    expect(request).toHaveBeenNthCalledWith(1, '/admin/sandboxes/sandbox-1');
    expect(request).toHaveBeenNthCalledWith(2, '/admin/sandboxes/sandbox-1', {
      method: 'PATCH',
      body: { name: 'Renamed', dataset_id: 'dataset-2' },
    });
    expect(request).toHaveBeenNthCalledWith(3, '/admin/sandboxes/sandbox-1', { method: 'DELETE' });
  });

  it('uses dedicated lifecycle and replay control endpoints instead of generic PATCH', async () => {
    const request = vi.fn().mockResolvedValue(sandboxSummary());

    await startSandbox('sandbox-1', { request });
    await pauseSandbox('sandbox-1', { request });
    await stopSandbox('sandbox-1', { request });
    await seekSandboxReplay('sandbox-1', '2026-01-01T01:00:00Z', { request });
    await setSandboxReplaySpeed('sandbox-1', 3, { request });
    await resumeSandboxReplay('sandbox-1', { request });

    expect(request).toHaveBeenNthCalledWith(1, '/admin/sandboxes/sandbox-1/start', { method: 'POST' });
    expect(request).toHaveBeenNthCalledWith(2, '/admin/sandboxes/sandbox-1/pause', { method: 'POST' });
    expect(request).toHaveBeenNthCalledWith(3, '/admin/sandboxes/sandbox-1/stop', { method: 'POST' });
    expect(request).toHaveBeenNthCalledWith(4, '/admin/sandboxes/sandbox-1/replay/seek', {
      method: 'POST',
      body: { replay_current_time: '2026-01-01T01:00:00Z' },
    });
    expect(request).toHaveBeenNthCalledWith(5, '/admin/sandboxes/sandbox-1/replay/speed', {
      method: 'POST',
      body: { replay_speed: 3 },
    });
    expect(request).toHaveBeenNthCalledWith(6, '/admin/sandboxes/sandbox-1/replay/resume', { method: 'POST' });
  });
});

describe('mapSandboxSummary', () => {
  it.each([
    ['ready', 'Stopped'],
    ['draft', 'Stopped'],
    ['running', 'Running'],
    ['paused', 'Paused'],
    ['stopped', 'Stopped'],
    ['completed', 'Stopped'],
    ['failed', 'Failed'],
  ] as const)('maps backend status %s to UI status %s', (backendStatus, uiStatus) => {
    expect(mapSandboxSummary(sandboxSummary({ status: backendStatus })).status).toBe(uiStatus);
  });
});

function sandboxSummary(overrides: Partial<SandboxSummary> = {}): SandboxSummary {
  return {
    id: 'sandbox-id',
    name: 'Sandbox Name',
    mode: 'replay',
    status: 'ready',
    start_datetime: '2026-01-01T00:00:00Z',
    replay_current_time: '2026-01-01T00:30:00Z',
    replay_speed: 1,
    dataset_id: 'dataset-id',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:30:00Z',
    ...overrides,
  };
}
