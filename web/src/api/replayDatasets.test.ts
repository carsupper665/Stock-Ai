import { describe, expect, it, vi } from 'vitest';

import { ApiError } from './client';
import { listReplayDatasets, mapReplayDatasetSummary, type ReplayDatasetSummary } from './replayDatasets';

describe('listReplayDatasets', () => {
  it('requests replay datasets from the backend admin endpoint', async () => {
    const request = vi.fn().mockResolvedValueOnce({ items: [] });

    await listReplayDatasets({ request });

    expect(request).toHaveBeenCalledWith('/admin/replay-datasets');
  });

  it('maps an empty replay dataset response to an empty list', async () => {
    const request = vi.fn().mockResolvedValueOnce({ items: [] });

    await expect(listReplayDatasets({ request })).resolves.toEqual([]);
  });

  it('maps backend replay dataset summaries into visible dataset fields', async () => {
    const request = vi.fn().mockResolvedValueOnce({
      items: [
        replayDatasetSummary({
          id: 'dataset-1',
          name: 'BTC Replay',
          symbol: 'BTC-USDT',
          interval: '5m',
          source: 'Uploaded CSV',
          start_at: '2026-01-01T00:00:00Z',
          end_at: '2026-01-02T00:00:00Z',
          created_at: '2026-01-01T01:00:00Z',
          row_count: 42,
          latest_import_job: {
            status: 'completed',
            rows_total: 42,
            rows_imported: 42,
            updated_at: '2026-01-02T01:00:00Z',
          },
        }),
      ],
    });

    await expect(listReplayDatasets({ request })).resolves.toEqual([
      {
        id: 'dataset-1',
        name: 'BTC Replay',
        symbol: 'BTC-USDT',
        interval: '5m',
        source: 'Uploaded CSV',
        timeRange: '2026-01-01T00:00:00Z to 2026-01-02T00:00:00Z',
        status: 'Imported',
        updated: '2026-01-02T01:00:00Z',
        size: '42 rows',
      },
    ]);
  });

  it('propagates backend ApiError failures without returning fallback datasets', async () => {
    const apiError = new ApiError(401, 'UNAUTHORIZED', 'admin login required', null);
    const request = vi.fn().mockRejectedValueOnce(apiError);

    await expect(listReplayDatasets({ request })).rejects.toBe(apiError);
  });
});

describe('mapReplayDatasetSummary', () => {
  it.each([
    ['running', 'Importing'],
    ['pending', 'Importing'],
    ['failed', 'Failed'],
  ] as const)('maps latest import job status %s to %s', (jobStatus, datasetStatus) => {
    expect(
      mapReplayDatasetSummary(
        replayDatasetSummary({
          row_count: 0,
          latest_import_job: { status: jobStatus },
        }),
      ).status,
    ).toBe(datasetStatus);
  });

  it('maps row counts without running or failed jobs to Imported', () => {
    expect(mapReplayDatasetSummary(replayDatasetSummary({ row_count: 10 })).status).toBe('Imported');
  });

  it('maps summaries without imported rows to Metadata Only', () => {
    expect(mapReplayDatasetSummary(replayDatasetSummary({ row_count: 0 })).status).toBe('Metadata Only');
  });

  it('uses default mapper values for missing optional backend fields', () => {
    expect(
      mapReplayDatasetSummary(
        replayDatasetSummary({
          interval: '',
          source: '',
          start_at: null,
          end_at: null,
          created_at: null,
          row_count: 0,
        }),
      ),
    ).toEqual({
      id: 'dataset-id',
      name: 'Dataset Name',
      symbol: 'BTC-USDT',
      interval: '1m',
      source: 'Backend',
      timeRange: 'No rows imported',
      status: 'Metadata Only',
      updated: 'Unknown',
      size: undefined,
    });
  });

  it.each([
    [{ updated_at: '2026-01-05T00:00:00Z', finished_at: '2026-01-04T00:00:00Z', created_at: '2026-01-03T00:00:00Z' }, '2026-01-05T00:00:00Z'],
    [{ finished_at: '2026-01-04T00:00:00Z', created_at: '2026-01-03T00:00:00Z' }, '2026-01-04T00:00:00Z'],
    [{ created_at: '2026-01-03T00:00:00Z' }, '2026-01-03T00:00:00Z'],
  ] as const)('uses latest import job timestamp precedence %#', (latestImportJob, expectedUpdated) => {
    expect(
      mapReplayDatasetSummary(
        replayDatasetSummary({
          created_at: '2026-01-02T00:00:00Z',
          latest_import_job: latestImportJob,
        }),
      ).updated,
    ).toBe(expectedUpdated);
  });

  it('falls back from latest import job timestamps to summary created_at then Unknown', () => {
    expect(mapReplayDatasetSummary(replayDatasetSummary({ created_at: '2026-01-02T00:00:00Z' })).updated).toBe(
      '2026-01-02T00:00:00Z',
    );
    expect(mapReplayDatasetSummary(replayDatasetSummary({ created_at: null })).updated).toBe('Unknown');
  });
});

function replayDatasetSummary(overrides: Partial<ReplayDatasetSummary> = {}): ReplayDatasetSummary {
  return {
    id: 'dataset-id',
    name: 'Dataset Name',
    symbol: 'BTC-USDT',
    interval: '1m',
    source: 'Backend',
    start_at: '2026-01-01T00:00:00Z',
    end_at: '2026-01-01T01:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    row_count: 0,
    ...overrides,
  };
}
