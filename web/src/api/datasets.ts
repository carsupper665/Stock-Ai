import { ApiClient } from './client';
import { listReplayDatasets, mapReplayDatasetSummary, type ReplayDatasetSummary } from './replayDatasets';
import type { Dataset } from '../types';

export interface CreateReplayDatasetInput {
  readonly name: string;
  readonly symbol: string;
  readonly interval: string;
  readonly source: string;
}

export interface ReplayDatasetImportJob {
  readonly id: string;
  readonly status: string;
}

type DatasetClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export function listDatasets(client: DatasetClient = defaultClient): Promise<Dataset[]> {
  return listReplayDatasets(client);
}

export async function createReplayDataset(
  input: CreateReplayDatasetInput,
  client: DatasetClient = defaultClient,
): Promise<Dataset> {
  const response = await client.request<ReplayDatasetSummary>('/admin/replay-datasets', {
    method: 'POST',
    body: {
      name: input.name,
      symbol: input.symbol,
      interval: input.interval,
      source: input.source,
    },
  });

  return mapReplayDatasetSummary(response);
}

export function importReplayDataset(
  datasetId: string,
  file: File,
  client: DatasetClient = defaultClient,
): Promise<ReplayDatasetImportJob> {
  const formData = new FormData();
  formData.append('file', file);

  return client.request<ReplayDatasetImportJob>(`/admin/replay-datasets/${datasetId}/import`, {
    method: 'POST',
    body: formData,
  });
}
