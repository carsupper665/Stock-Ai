import { ApiClient, type JsonObject } from './client';
import type { LiveAccount } from '../types';

export interface LiveAccountSummary {
  readonly id: string;
  readonly name: string;
  readonly type: string;
  readonly provider?: string;
  readonly environment?: string;
  readonly price_mode?: string;
  readonly credentials_status?: string;
  readonly supported_symbols?: readonly string[];
  readonly base_currency: string;
  readonly initial_balance: number;
  readonly wallet_balance: number;
  readonly available_balance: number;
  readonly locked_margin: number;
  readonly realized_pnl: number;
  readonly unrealized_pnl: number;
  readonly equity: number;
  readonly live_trading_enabled: boolean;
  readonly status: string;
  readonly created_at?: string;
  readonly updated_at?: string;
}

interface LiveAccountListResponse {
  readonly items: LiveAccountSummary[];
}

export interface CreateLiveAccountInput {
  readonly name: string;
  readonly initialBalance: number;
  readonly baseCurrency?: string;
  readonly provider?: string;
  readonly environment?: string;
  readonly priceMode?: string;
  readonly credentialsStatus?: string;
  readonly supportedSymbols?: readonly string[];
}

export interface UpdateLiveAccountInput {
  readonly name?: string;
  readonly status?: string;
  readonly provider?: string;
  readonly environment?: string;
  readonly priceMode?: string;
  readonly credentialsStatus?: string;
  readonly supportedSymbols?: readonly string[];
  readonly liveTradingEnabled?: boolean;
}

type LiveAccountClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export async function listLiveAccounts(client: LiveAccountClient = defaultClient): Promise<LiveAccount[]> {
  const response = await client.request<LiveAccountListResponse>('/admin/live-accounts');
  return response.items.map(mapLiveAccountSummary);
}

export async function createLiveAccount(
  input: CreateLiveAccountInput,
  client: LiveAccountClient = defaultClient,
): Promise<LiveAccount> {
  const response = await client.request<LiveAccountSummary>('/admin/live-accounts', {
    method: 'POST',
    body: liveAccountCreateBody(input),
  });
  return mapLiveAccountSummary(response);
}

export async function getLiveAccount(accountId: string, client: LiveAccountClient = defaultClient): Promise<LiveAccount> {
  const response = await client.request<LiveAccountSummary>(`/admin/live-accounts/${accountId}`);
  return mapLiveAccountSummary(response);
}

export async function updateLiveAccount(
  accountId: string,
  input: UpdateLiveAccountInput,
  client: LiveAccountClient = defaultClient,
): Promise<LiveAccount> {
  const response = await client.request<LiveAccountSummary>(`/admin/live-accounts/${accountId}`, {
    method: 'PATCH',
    body: liveAccountUpdateBody(input),
  });
  return mapLiveAccountSummary(response);
}

export function deleteLiveAccount(accountId: string, client: LiveAccountClient = defaultClient): Promise<void> {
  return client.request<void>(`/admin/live-accounts/${accountId}`, { method: 'DELETE' });
}

export function mapLiveAccountSummary(summary: LiveAccountSummary): LiveAccount {
  return {
    id: summary.id,
    name: summary.name,
    provider: summary.provider || 'Unknown',
    environment: summary.environment,
    priceMode: summary.price_mode || 'live',
    credentialsStatus: summary.credentials_status,
    supportedSymbols: summary.supported_symbols || [],
    baseCurrency: summary.base_currency,
    initialBalance: summary.initial_balance,
    walletBalance: summary.wallet_balance,
    availableBalance: summary.available_balance,
    lockedMargin: summary.locked_margin,
    realizedPnL: summary.realized_pnl,
    unrealizedPnL: summary.unrealized_pnl,
    equity: summary.equity,
    liveTradingEnabled: summary.live_trading_enabled,
    status: mapLiveAccountStatus(summary.status),
    created: summary.created_at || 'Unknown',
    updated: summary.updated_at || summary.created_at || 'Unknown',
  };
}

function liveAccountCreateBody(input: CreateLiveAccountInput): JsonObject {
  const body: Record<string, number | string | readonly string[]> = {
    name: input.name,
    initial_balance: input.initialBalance,
  };
  if (input.baseCurrency !== undefined) {
    body.base_currency = input.baseCurrency;
  }
  assignOptionalLiveFields(body, input);
  return body;
}

function liveAccountUpdateBody(input: UpdateLiveAccountInput): JsonObject {
  const body: Record<string, string | readonly string[] | boolean> = {};
  if (input.name !== undefined) {
    body.name = input.name;
  }
  if (input.status !== undefined) {
    body.status = input.status;
  }
  if (input.liveTradingEnabled !== undefined) {
    body.live_trading_enabled = input.liveTradingEnabled;
  }
  assignOptionalLiveFields(body, input);
  return body;
}

function assignOptionalLiveFields(
  body: Record<string, string | number | boolean | readonly string[]>,
  input: CreateLiveAccountInput | UpdateLiveAccountInput,
): void {
  if (input.provider !== undefined) {
    body.provider = input.provider;
  }
  if (input.environment !== undefined) {
    body.environment = input.environment;
  }
  if (input.priceMode !== undefined) {
    body.price_mode = input.priceMode;
  }
  if (input.credentialsStatus !== undefined) {
    body.credentials_status = input.credentialsStatus;
  }
  if (input.supportedSymbols !== undefined) {
    body.supported_symbols = input.supportedSymbols;
  }
}

function mapLiveAccountStatus(status: string): LiveAccount['status'] {
  if (status === 'active') {
    return 'Connected';
  }
  if (status === 'stale') {
    return 'Stale';
  }
  return 'Inactive';
}
