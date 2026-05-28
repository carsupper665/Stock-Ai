import { ApiClient, type JsonObject } from './client';
import type { SandboxAccount } from '../types';

export interface AccountSummary {
  readonly id: string;
  readonly sandbox_id?: string;
  readonly name: string;
  readonly type: string;
  readonly initial_balance: number;
  readonly wallet_balance: number;
  readonly available_balance: number;
  readonly locked_margin: number;
  readonly equity: number;
  readonly status: string;
  readonly created_at?: string;
  readonly updated_at?: string;
}

interface AccountListResponse {
  readonly items: AccountSummary[];
}

export interface CreateSandboxAccountInput {
  readonly name: string;
  readonly initialBalance: number;
}

export interface UpdateAdminAccountInput {
  readonly name?: string;
  readonly status?: string;
}

type AccountClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export async function listSandboxAccounts(
  sandboxId: string,
  client: AccountClient = defaultClient,
): Promise<SandboxAccount[]> {
  const response = await client.request<AccountListResponse>(`/admin/sandboxes/${sandboxId}/accounts`);
  return response.items.map((account) => mapSandboxAccountSummary(account, sandboxId));
}

export async function createSandboxAccount(
  sandboxId: string,
  input: CreateSandboxAccountInput,
  client: AccountClient = defaultClient,
): Promise<SandboxAccount> {
  const response = await client.request<AccountSummary>(`/admin/sandboxes/${sandboxId}/accounts`, {
    method: 'POST',
    body: {
      name: input.name,
      initial_balance: input.initialBalance,
    },
  });
  return mapSandboxAccountSummary(response, sandboxId);
}

export async function getAdminAccount(accountId: string, client: AccountClient = defaultClient): Promise<SandboxAccount> {
  const response = await client.request<AccountSummary>(`/admin/accounts/${accountId}`);
  return mapSandboxAccountSummary(response);
}

export async function updateAdminAccount(
  accountId: string,
  input: UpdateAdminAccountInput,
  client: AccountClient = defaultClient,
): Promise<SandboxAccount> {
  const response = await client.request<AccountSummary>(`/admin/accounts/${accountId}`, {
    method: 'PATCH',
    body: adminAccountUpdateBody(input),
  });
  return mapSandboxAccountSummary(response);
}

export function deleteAdminAccount(accountId: string, client: AccountClient = defaultClient): Promise<void> {
  return client.request<void>(`/admin/accounts/${accountId}`, { method: 'DELETE' });
}

export function mapSandboxAccountSummary(summary: AccountSummary, fallbackSandboxId = ''): SandboxAccount {
  const sandboxId = summary.sandbox_id || fallbackSandboxId;
  return {
    id: summary.id,
    name: summary.name,
    sandboxId,
    sandboxName: sandboxId || 'Unassigned sandbox',
    balance: summary.wallet_balance,
    equity: summary.equity,
    margin: summary.locked_margin,
    status: mapAccountStatus(summary.status),
    updated: summary.updated_at || summary.created_at || 'Unknown',
  };
}

function adminAccountUpdateBody(input: UpdateAdminAccountInput): JsonObject {
  const body: Record<string, string> = {};
  if (input.name !== undefined) {
    body.name = input.name;
  }
  if (input.status !== undefined) {
    body.status = input.status;
  }
  return body;
}

function mapAccountStatus(status: string): SandboxAccount['status'] {
  if (status === 'active') {
    return 'Active';
  }
  if (status === 'pending') {
    return 'Pending';
  }
  return 'Inactive';
}
