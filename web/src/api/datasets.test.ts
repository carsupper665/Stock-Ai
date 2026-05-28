import { describe, expect, it, vi } from 'vitest';

import { createReplayDataset, importReplayDataset, listDatasets } from './datasets';

describe('dataset API service', () => {
  it('lists replay datasets through the admin endpoint', async () => {
    const request = vi.fn().mockResolvedValueOnce({ items: [] });

    await expect(listDatasets({ request })).resolves.toEqual([]);

    expect(request).toHaveBeenCalledWith('/admin/replay-datasets');
  });

  it('creates replay dataset metadata with the backend payload', async () => {
    const request = vi.fn().mockResolvedValueOnce({
      id: 'dataset-1',
      name: 'BTC Replay',
      symbol: 'BTCUSDT',
      interval: '1m',
      source: 'Backend CSV',
      start_at: null,
      end_at: null,
      created_at: '2026-01-01T00:00:00Z',
      row_count: 0,
    });

    const dataset = await createReplayDataset(
      { name: 'BTC Replay', symbol: 'BTCUSDT', interval: '1m', source: 'Backend CSV' },
      { request },
    );

    expect(request).toHaveBeenCalledWith('/admin/replay-datasets', {
      method: 'POST',
      body: { name: 'BTC Replay', symbol: 'BTCUSDT', interval: '1m', source: 'Backend CSV' },
    });
    expect(dataset).toMatchObject({
      id: 'dataset-1',
      name: 'BTC Replay',
      status: 'Metadata Only',
      updated: '2026-01-01T00:00:00Z',
    });
  });

  it('imports a replay dataset file as FormData without JSON encoding the file', async () => {
    const request = vi.fn().mockResolvedValueOnce({ id: 'job-1', status: 'running' });
    const file = new File(['timestamp,open,high,low,close'], 'dataset.csv', { type: 'text/csv' });

    const job = await importReplayDataset('dataset-1', file, { request });

    expect(request).toHaveBeenCalledTimes(1);
    const [path, options] = request.mock.calls[0];
    expect(path).toBe('/admin/replay-datasets/dataset-1/import');
    expect(options).toMatchObject({ method: 'POST' });
    expect(options.body).toBeInstanceOf(FormData);
    expect((options.body as FormData).get('file')).toBe(file);
    expect(job).toEqual({ id: 'job-1', status: 'running' });
  });
});
