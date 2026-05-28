import { ApiClient } from './client';
import type { ReplayControlStatus, SandboxSummary } from './sandboxes';

export interface IndicatorPoint {
  readonly ts: string;
  readonly value: number;
}

interface IndicatorPointResponse {
  readonly at?: string;
  readonly ts?: string;
  readonly value: number;
}

export interface SandboxMonitorSnapshotResponse {
  readonly sandbox: SandboxSummary;
  readonly replay_control?: ReplayControlStatus;
  readonly dataset?: unknown;
  readonly accounts: readonly unknown[];
  readonly orders: readonly unknown[];
  readonly trades: readonly unknown[];
  readonly positions: readonly unknown[];
  readonly freshness_at: string;
  readonly indicators?: Readonly<Record<string, readonly IndicatorPointResponse[]>>;
}

export interface SandboxMonitorSnapshot {
  readonly sandboxId: string;
  readonly replayCurrentTime: string;
  readonly replaySpeed: number;
  readonly status: string;
  readonly accounts: readonly unknown[];
  readonly orders: readonly unknown[];
  readonly trades: readonly unknown[];
  readonly positions: readonly unknown[];
  readonly indicators: Readonly<Record<string, readonly IndicatorPoint[]>>;
  readonly freshnessAt: string;
  readonly equityTrend?: readonly { readonly time: string; readonly value: number }[];
}

type MonitorClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export async function getSandboxMonitorSnapshot(
  sandboxId: string,
  client: MonitorClient = defaultClient,
): Promise<SandboxMonitorSnapshot> {
  const response = await client.request<SandboxMonitorSnapshotResponse>(
    `/admin/monitor/sandboxes/${sandboxId}/snapshot`,
  );
  return mapSandboxMonitorSnapshot(response);
}

export function mapSandboxMonitorSnapshot(response: SandboxMonitorSnapshotResponse): SandboxMonitorSnapshot {
  return {
    sandboxId: response.replay_control?.sandbox_id || response.sandbox.id,
    replayCurrentTime: response.replay_control?.current_time || response.sandbox.replay_current_time,
    replaySpeed: response.replay_control?.speed ?? response.sandbox.replay_speed,
    status: response.replay_control?.status || response.sandbox.status,
    accounts: response.accounts,
    orders: response.orders,
    trades: response.trades,
    positions: response.positions,
    indicators: mapIndicators(response.indicators ?? {}),
    freshnessAt: response.freshness_at,
    equityTrend: undefined,
  };
}

function mapIndicators(
  indicators: Readonly<Record<string, readonly IndicatorPointResponse[]>>,
): Readonly<Record<string, readonly IndicatorPoint[]>> {
  return Object.fromEntries(
    Object.entries(indicators).map(([name, points]) => [
      name,
      points.map((point) => ({
        ts: point.ts ?? point.at ?? '',
        value: point.value,
      })),
    ]),
  );
}
