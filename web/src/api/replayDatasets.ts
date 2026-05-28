import { ApiClient } from './client';
import type { Dataset } from '../types';

export interface ReplayDatasetImportJobSummary {
  readonly status?: string;
  readonly rows_total?: number;
  readonly rows_imported?: number;
  readonly error_summary?: string;
  readonly updated_at?: string | null;
  readonly finished_at?: string | null;
  readonly created_at?: string | null;
}

export interface ReplayDatasetSummary {
  readonly id: string;
  readonly name: string;
  readonly symbol: string;
  readonly interval: string;
  readonly source?: string;
  readonly start_at?: string | null;
  readonly end_at?: string | null;
  readonly created_at?: string | null;
  readonly row_count?: number;
  readonly latest_import_job?: ReplayDatasetImportJobSummary | null;
}

interface ReplayDatasetListResponse {
  readonly items: ReplayDatasetSummary[];
}

type ReplayDatasetClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export async function listReplayDatasets(client: ReplayDatasetClient = defaultClient): Promise<Dataset[]> {
  const response = await client.request<ReplayDatasetListResponse>('/admin/replay-datasets');

  return response.items.map(mapReplayDatasetSummary);
}

export function mapReplayDatasetSummary(summary: ReplayDatasetSummary): Dataset {
  const rowCount = summary.row_count ?? 0;
  const dataset: Dataset = {
    id: summary.id,
    name: summary.name,
    symbol: summary.symbol,
    interval: summary.interval || '1m',
    source: summary.source || 'Backend',
    timeRange: summary.start_at && summary.end_at ? `${summary.start_at} to ${summary.end_at}` : 'No rows imported',
    status: mapReplayDatasetStatus(summary.latest_import_job?.status, rowCount),
    updated:
      summary.latest_import_job?.updated_at ||
      summary.latest_import_job?.finished_at ||
      summary.latest_import_job?.created_at ||
      summary.created_at ||
      'Unknown',
  };

  if (rowCount > 0) {
    dataset.size = `${rowCount} rows`;
  }

  return dataset;
}

function mapReplayDatasetStatus(latestJobStatus: string | undefined, rowCount: number): Dataset['status'] {
  if (latestJobStatus === 'running' || latestJobStatus === 'pending') {
    return 'Importing';
  }

  if (latestJobStatus === 'failed') {
    return 'Failed';
  }

  if (rowCount > 0) {
    return 'Imported';
  }

  return 'Metadata Only';
}
