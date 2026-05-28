import { describe, expect, it, vi } from 'vitest';

import {
  createSandboxAccount,
  deleteAdminAccount,
  getAdminAccount,
  listSandboxAccounts,
  mapSandboxAccountSummary,
  updateAdminAccount,
  type AccountSummary,
} from './accounts';

describe('account API service', () => {
  it('lists sandbox accounts through the sandbox-scoped endpoint', async () => {
    const request = vi.fn().mockResolvedValueOnce({ items: [] });

    await expect(listSandboxAccounts('sandbox-1', { request })).resolves.toEqual([]);

    expect(request).toHaveBeenCalledWith('/admin/sandboxes/sandbox-1/accounts');
    expect(request).not.toHaveBeenCalledWith('/admin/accounts');
  });

  it('creates sandbox accounts through the sandbox-scoped endpoint', async () => {
    const request = vi.fn().mockResolvedValueOnce(accountSummary({ id: 'account-1', name: 'Primary' }));

    const account = await createSandboxAccount(
      'sandbox-1',
      { name: 'Primary', initialBalance: 10_000 },
      { request },
    );

    expect(request).toHaveBeenCalledWith('/admin/sandboxes/sandbox-1/accounts', {
      method: 'POST',
      body: { name: 'Primary', initial_balance: 10_000 },
    });
    expect(account).toMatchObject({ id: 'account-1', name: 'Primary', sandboxId: 'sandbox-1' });
  });

  it('gets, updates, and deletes individual admin accounts without using a global account list', async () => {
    const request = vi.fn().mockResolvedValue(accountSummary());

    await getAdminAccount('account-1', { request });
    await updateAdminAccount('account-1', { name: 'Renamed', status: 'disabled' }, { request });
    await deleteAdminAccount('account-1', { request });

    expect(request).toHaveBeenNthCalledWith(1, '/admin/accounts/account-1');
    expect(request).toHaveBeenNthCalledWith(2, '/admin/accounts/account-1', {
      method: 'PATCH',
      body: { name: 'Renamed', status: 'disabled' },
    });
    expect(request).toHaveBeenNthCalledWith(3, '/admin/accounts/account-1', { method: 'DELETE' });
    expect(request).not.toHaveBeenCalledWith('/admin/accounts');
  });
});

describe('mapSandboxAccountSummary', () => {
  it.each([
    ['active', 'Active'],
    ['disabled', 'Inactive'],
    ['pending', 'Pending'],
  ] as const)('maps account status %s to %s', (backendStatus, uiStatus) => {
    expect(mapSandboxAccountSummary(accountSummary({ status: backendStatus })).status).toBe(uiStatus);
  });
});

function accountSummary(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'account-id',
    sandbox_id: 'sandbox-1',
    name: 'Sandbox Account',
    type: 'virtual',
    initial_balance: 10_000,
    wallet_balance: 9_900,
    available_balance: 9_500,
    locked_margin: 400,
    equity: 10_100,
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:30:00Z',
    ...overrides,
  };
}
