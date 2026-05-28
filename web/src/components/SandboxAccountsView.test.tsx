import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createSandboxAccount } from '../api/accounts';
import { createToken, revokeToken } from '../api/tokens';
import type { SandboxAccount } from '../types';
import SandboxAccountsView from './SandboxAccountsView';

vi.mock('../api/accounts', () => ({
  createSandboxAccount: vi.fn(),
}));

vi.mock('../api/tokens', () => ({
  createToken: vi.fn(),
  revokeToken: vi.fn(),
}));

const createSandboxAccountMock = vi.mocked(createSandboxAccount);
const createTokenMock = vi.mocked(createToken);
const revokeTokenMock = vi.mocked(revokeToken);

describe('SandboxAccountsView backend actions', () => {
  beforeEach(() => {
    createSandboxAccountMock.mockReset();
    createTokenMock.mockReset();
    revokeTokenMock.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it('provisions a sandbox account and creates a backend token before adding it locally', async () => {
    const user = userEvent.setup();
    const onAddAccount = vi.fn();
    createSandboxAccountMock.mockResolvedValueOnce({ ...accountFixture, id: 'account-2', token: undefined });
    createTokenMock.mockResolvedValueOnce({
      id: 'token-1',
      accountId: 'account-2',
      scopes: ['market:read', 'trade:write'],
      token: 'token-1.secret',
    });
    renderSandboxAccountsView({ onAddAccount });

    await user.click(screen.getByRole('button', { name: /provision account/i }));
    await user.type(screen.getByRole('textbox'), 'Backend Agent');
    await user.click(screen.getByRole('button', { name: /generate backend credentials/i }));

    expect(createSandboxAccountMock).toHaveBeenCalledWith('sandbox-1', {
      name: 'Backend Agent',
      initialBalance: 10000,
    });
    expect(createTokenMock).toHaveBeenCalledWith({
      accountId: 'account-2',
      name: 'Backend Agent Token',
      scopes: ['market:read', 'trade:write'],
    });
    expect(onAddAccount).toHaveBeenCalledWith(expect.objectContaining({ id: 'account-2', token: 'token-1.secret', tokenId: 'token-1' }));
  });

  it('shows backend errors and does not add fake account rows when provisioning fails', async () => {
    const user = userEvent.setup();
    const onAddAccount = vi.fn();
    createSandboxAccountMock.mockRejectedValueOnce(new Error('initial balance must be positive'));
    renderSandboxAccountsView({ onAddAccount });

    await user.click(screen.getByRole('button', { name: /provision account/i }));
    await user.type(screen.getByRole('textbox'), 'Backend Agent');
    await user.click(screen.getByRole('button', { name: /generate backend credentials/i }));

    expect(await screen.findByText('initial balance must be positive')).toBeVisible();
    expect(onAddAccount).not.toHaveBeenCalled();
  });

  it('disables provision submit while backend requests are pending', async () => {
    const user = userEvent.setup();
    createSandboxAccountMock.mockImplementationOnce(() => new Promise(() => undefined));
    renderSandboxAccountsView();

    await user.click(screen.getByRole('button', { name: /provision account/i }));
    await user.type(screen.getByRole('textbox'), 'Backend Agent');
    await user.click(screen.getByRole('button', { name: /generate backend credentials/i }));

    await waitFor(() => expect(screen.getByRole('button', { name: /generating credentials/i })).toBeDisabled());
  });

  it('revokes a backend token before marking the local account inactive', async () => {
    const user = userEvent.setup();
    const onUpdateAccount = vi.fn();
    revokeTokenMock.mockResolvedValueOnce(undefined);
    renderSandboxAccountsView({ onUpdateAccount });

    await user.click(screen.getByTitle('Revoke Token Access'));
    await user.click(screen.getByRole('button', { name: /confirm revocation/i }));

    expect(revokeTokenMock).toHaveBeenCalledWith('token-1');
    expect(onUpdateAccount).toHaveBeenCalledWith(expect.objectContaining({ id: 'account-1', tokenRevoked: true, status: 'Inactive' }));
  });
});

function renderSandboxAccountsView(
  options: {
    readonly onAddAccount?: (account: SandboxAccount) => void;
    readonly onUpdateAccount?: (account: SandboxAccount) => void;
  } = {},
) {
  return render(
    <SandboxAccountsView
      theme="light"
      viewState="normal"
      accounts={[accountFixture]}
      onAddAccount={options.onAddAccount ?? vi.fn()}
      onUpdateAccount={options.onUpdateAccount ?? vi.fn()}
      sandboxes={[{ id: 'sandbox-1', name: 'Sandbox One' }]}
    />,
  );
}

const accountFixture: SandboxAccount = {
  id: 'account-1',
  name: 'Primary Account',
  sandboxId: 'sandbox-1',
  sandboxName: 'Sandbox One',
  balance: 10000,
  equity: 10000,
  margin: 0,
  status: 'Active',
  token: 'token-1.secret',
  tokenId: 'token-1',
  updated: '2026-01-01T00:00:00Z',
};

describe('SandboxAccountsView empty state regression', () => {
  afterEach(() => {
    cleanup();
  });

  it('shows provision form after clicking provision button in empty state', async () => {
    const user = userEvent.setup();
    render(
      <SandboxAccountsView
        theme="light"
        viewState="normal"
        accounts={[]}
        onAddAccount={vi.fn()}
        onUpdateAccount={vi.fn()}
        sandboxes={[{ id: 'sandbox-1', name: 'Sandbox One' }]}
      />,
    );

    expect(screen.getByText(/no accounts provisioned/i)).toBeVisible();
    await user.click(screen.getByRole('button', { name: /provision virtual account/i }));

    expect(screen.getByRole('button', { name: /generate backend credentials/i })).toBeVisible();
    expect(screen.getByRole('button', { name: /cancel/i })).toBeVisible();
    expect(screen.getByText(/account label/i)).toBeVisible();
  });
});
