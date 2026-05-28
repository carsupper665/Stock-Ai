import { ApiClient, type JsonObject } from './client';
import type { Sandbox } from '../types';

export interface SandboxSummary {
  readonly id: string;
  readonly name: string;
  readonly mode: string;
  readonly status: string;
  readonly start_datetime: string;
  readonly replay_current_time: string;
  readonly replay_speed: number;
  readonly dataset_id?: string;
  readonly created_at?: string;
  readonly updated_at?: string;
}

interface SandboxListResponse {
  readonly items: SandboxSummary[];
}

export interface CreateSandboxInput {
  readonly name: string;
  readonly datasetId: string;
  readonly startDatetime?: string;
  readonly replayCurrentTime?: string;
  readonly replaySpeed?: number;
}

export interface UpdateSandboxInput {
  readonly name?: string;
  readonly datasetId?: string;
  readonly status?: string;
}

export interface ReplayControlStatus {
  readonly sandbox_id: string;
  readonly current_time: string;
  readonly speed: number;
  readonly status: string;
}

type SandboxClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export async function listSandboxes(client: SandboxClient = defaultClient): Promise<Sandbox[]> {
  const response = await client.request<SandboxListResponse>('/admin/sandboxes');
  return response.items.map(mapSandboxSummary);
}

export async function createSandbox(input: CreateSandboxInput, client: SandboxClient = defaultClient): Promise<Sandbox> {
  const response = await client.request<SandboxSummary>('/admin/sandboxes', {
    method: 'POST',
    body: sandboxCreateBody(input),
  });
  return mapSandboxSummary(response);
}

export async function getSandbox(sandboxId: string, client: SandboxClient = defaultClient): Promise<Sandbox> {
  const response = await client.request<SandboxSummary>(`/admin/sandboxes/${sandboxId}`);
  return mapSandboxSummary(response);
}

export async function updateSandbox(
  sandboxId: string,
  input: UpdateSandboxInput,
  client: SandboxClient = defaultClient,
): Promise<Sandbox> {
  const response = await client.request<SandboxSummary>(`/admin/sandboxes/${sandboxId}`, {
    method: 'PATCH',
    body: sandboxUpdateBody(input),
  });
  return mapSandboxSummary(response);
}

export function deleteSandbox(sandboxId: string, client: SandboxClient = defaultClient): Promise<void> {
  return client.request<void>(`/admin/sandboxes/${sandboxId}`, { method: 'DELETE' });
}

export async function startSandbox(sandboxId: string, client: SandboxClient = defaultClient): Promise<Sandbox> {
  const response = await client.request<SandboxSummary>(`/admin/sandboxes/${sandboxId}/start`, { method: 'POST' });
  return mapSandboxSummary(response);
}

export async function pauseSandbox(sandboxId: string, client: SandboxClient = defaultClient): Promise<Sandbox> {
  const response = await client.request<SandboxSummary>(`/admin/sandboxes/${sandboxId}/pause`, { method: 'POST' });
  return mapSandboxSummary(response);
}

export async function stopSandbox(sandboxId: string, client: SandboxClient = defaultClient): Promise<Sandbox> {
  const response = await client.request<SandboxSummary>(`/admin/sandboxes/${sandboxId}/stop`, { method: 'POST' });
  return mapSandboxSummary(response);
}

export function seekSandboxReplay(
  sandboxId: string,
  replayCurrentTime: string,
  client: SandboxClient = defaultClient,
): Promise<ReplayControlStatus> {
  return client.request<ReplayControlStatus>(`/admin/sandboxes/${sandboxId}/replay/seek`, {
    method: 'POST',
    body: { replay_current_time: replayCurrentTime },
  });
}

export function setSandboxReplaySpeed(
  sandboxId: string,
  replaySpeed: number,
  client: SandboxClient = defaultClient,
): Promise<ReplayControlStatus> {
  return client.request<ReplayControlStatus>(`/admin/sandboxes/${sandboxId}/replay/speed`, {
    method: 'POST',
    body: { replay_speed: replaySpeed },
  });
}

export function resumeSandboxReplay(
  sandboxId: string,
  client: SandboxClient = defaultClient,
): Promise<ReplayControlStatus> {
  return client.request<ReplayControlStatus>(`/admin/sandboxes/${sandboxId}/replay/resume`, { method: 'POST' });
}

export function mapSandboxSummary(summary: SandboxSummary): Sandbox {
  return {
    id: summary.id,
    name: summary.name,
    mode: summary.mode === 'live' ? 'Live' : 'Replay',
    status: mapSandboxStatus(summary.status),
    datasetId: summary.dataset_id || undefined,
    accountsCount: 0,
    replayTime: summary.replay_current_time || summary.start_datetime || 'Unknown',
    freshness: summary.status,
    updated: summary.updated_at || summary.created_at || 'Unknown',
  };
}

function sandboxCreateBody(input: CreateSandboxInput): JsonObject {
  const body: Record<string, string | number> = {
    name: input.name,
    dataset_id: input.datasetId,
  };
  if (input.startDatetime !== undefined) {
    body.start_datetime = input.startDatetime;
  }
  if (input.replayCurrentTime !== undefined) {
    body.replay_current_time = input.replayCurrentTime;
  }
  if (input.replaySpeed !== undefined) {
    body.replay_speed = input.replaySpeed;
  }
  return body;
}

function sandboxUpdateBody(input: UpdateSandboxInput): JsonObject {
  const body: Record<string, string> = {};
  if (input.name !== undefined) {
    body.name = input.name;
  }
  if (input.datasetId !== undefined) {
    body.dataset_id = input.datasetId;
  }
  if (input.status !== undefined) {
    body.status = input.status;
  }
  return body;
}

function mapSandboxStatus(status: string): Sandbox['status'] {
  if (status === 'running') {
    return 'Running';
  }
  if (status === 'paused') {
    return 'Paused';
  }
  if (status === 'failed') {
    return 'Failed';
  }
  return 'Stopped';
}
