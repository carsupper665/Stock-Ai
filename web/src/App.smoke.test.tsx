import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from './api/client';
import { loginAdmin } from './api/auth';
import { listSandboxAccounts } from './api/accounts';
import { listDatasets } from './api/datasets';
import { listLiveAccounts } from './api/liveAccounts';
import { listSandboxes } from './api/sandboxes';
import { listLiveSymbols } from './api/systemHealth';
import { getSandboxMonitorSnapshot, type SandboxMonitorSnapshot } from './api/monitor';
import type { Sandbox, SandboxAccount } from './types';
import App from './App';

vi.mock('./api/auth', () => ({
  loginAdmin: vi.fn(),
}));

vi.mock('./api/datasets', () => ({
  listDatasets: vi.fn(),
}));

vi.mock('./api/accounts', () => ({
  listSandboxAccounts: vi.fn(),
}));

vi.mock('./api/liveAccounts', () => ({
  listLiveAccounts: vi.fn(),
}));

vi.mock('./api/sandboxes', () => ({
  listSandboxes: vi.fn(),
}));

vi.mock('./api/systemHealth', () => ({
  listLiveSymbols: vi.fn(),
}));

vi.mock('./api/monitor', () => ({
  getSandboxMonitorSnapshot: vi.fn(),
}));

const loginAdminMock = vi.mocked(loginAdmin);
const listSandboxAccountsMock = vi.mocked(listSandboxAccounts);
const listDatasetsMock = vi.mocked(listDatasets);
const listLiveAccountsMock = vi.mocked(listLiveAccounts);
const listSandboxesMock = vi.mocked(listSandboxes);
const listLiveSymbolsMock = vi.mocked(listLiveSymbols);
const getSandboxMonitorSnapshotMock = vi.mocked(getSandboxMonitorSnapshot);

describe('App smoke test', () => {
  beforeEach(() => {
    loginAdminMock.mockReset();
    listSandboxAccountsMock.mockReset();
    listDatasetsMock.mockReset();
    listLiveAccountsMock.mockReset();
    listSandboxesMock.mockReset();
    listLiveSymbolsMock.mockReset();
    getSandboxMonitorSnapshotMock.mockReset();
    listDatasetsMock.mockResolvedValue([]);
    listSandboxAccountsMock.mockResolvedValue([]);
    listLiveAccountsMock.mockResolvedValue([]);
    listSandboxesMock.mockResolvedValue([]);
    listLiveSymbolsMock.mockResolvedValue([]);
    getSandboxMonitorSnapshotMock.mockResolvedValue(monitorSnapshotFixture());
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
    document.documentElement.className = '';

    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });
  });

  afterEach(() => {
    cleanup();
  });

  it('renders the shell navigation labels', () => {
    expect(() => render(<App />)).not.toThrow();

    expect(screen.getByText('Sandbox Kernel')).not.toBeNull();
    expect(screen.getByText('Replay Datasets')).not.toBeNull();
    expect(screen.getByText('Agent Boundary')).not.toBeNull();
  });

  it('initializes the document theme after render', () => {
    render(<App />);

    expect(['light', 'dark']).toContain(document.documentElement.dataset.theme);
  });

  it('starts with the admin console locked behind login', () => {
    render(<App />);

    expect(screen.getByRole('button', { name: /authenticate operator session/i })).toBeVisible();
    expect(screen.queryByText('Active Simulators')).toBeNull();
  });

  it('does not unlock the console from the sidebar control without backend auth', async () => {
    const user = userEvent.setup();
    render(<App />);

    await user.click(screen.getByTitle(/access/i));

    expect(loginAdminMock).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: /authenticate operator session/i })).toBeVisible();
    expect(screen.queryByText('Active Simulators')).toBeNull();
  });

  it('logs in through the backend admin session endpoint before unlocking the dashboard', async () => {
    const user = userEvent.setup();
    loginAdminMock.mockResolvedValueOnce({ id: 1, username: 'operator', role: 'admin' });
    render(<App />);

    const usernameInput = screen.getByPlaceholderText('e.g. ops_manager');
    const passwordInput = document.querySelector('input[type="password"]');
    if (!(passwordInput instanceof HTMLInputElement)) {
      throw new Error('Expected password input to render');
    }

    await user.type(usernameInput, 'operator');
    await user.type(passwordInput, 'secret');
    await user.click(screen.getByRole('button', { name: /authenticate operator session/i }));

    expect(loginAdminMock).toHaveBeenCalledWith({ username: 'operator', password: 'secret' });
    expect(listDatasetsMock).toHaveBeenCalledOnce();
    expect(listSandboxesMock).toHaveBeenCalledOnce();
    expect(listLiveAccountsMock).toHaveBeenCalledOnce();
    expect(listLiveSymbolsMock).toHaveBeenCalledOnce();
    expect(await screen.findByText('Active Simulators')).toBeVisible();
  });

  it('loads sandbox accounts after login so overview counts render backend data', async () => {
    const user = userEvent.setup();
    loginAdminMock.mockResolvedValueOnce({ id: 1, username: 'operator', role: 'admin' });
    listSandboxesMock.mockResolvedValueOnce([sandboxFixture]);
    listSandboxAccountsMock.mockResolvedValueOnce([accountFixture]);
    render(<App />);

    await submitLogin(user);

    expect(listSandboxAccountsMock).toHaveBeenCalledWith('sandbox-1');
    expect(await screen.findByText('1 Nodes')).toBeVisible();
  });

  it('hydrates monitor snapshots from the backend and removes demo-only dashboard controls', async () => {
    const user = userEvent.setup();
    loginAdminMock.mockResolvedValueOnce({ id: 1, username: 'operator', role: 'admin' });
    listSandboxesMock.mockResolvedValueOnce([sandboxFixture]);
    listSandboxAccountsMock.mockResolvedValueOnce([accountFixture]);
    getSandboxMonitorSnapshotMock.mockResolvedValueOnce(
      monitorSnapshotFixture({ orders: [{ id: 'order-1', status: 'new' }] }),
    );
    render(<App />);

    await submitLogin(user);

    expect(getSandboxMonitorSnapshotMock).toHaveBeenCalledWith('sandbox-1');
    expect(await screen.findByText('1 Orders')).toBeVisible();
    expect(screen.queryByText(/Interactive Demo State Injector/i)).toBeNull();
    expect(screen.queryByText(/Simulate Loading/i)).toBeNull();
    expect(screen.queryByText('2 Orders')).toBeNull();
  });

  it('loads empty backend datasets after login without showing mock dataset rows', async () => {
    const user = userEvent.setup();
    loginAdminMock.mockResolvedValueOnce({ id: 1, username: 'operator', role: 'admin' });
    listDatasetsMock.mockResolvedValueOnce([]);
    render(<App />);

    await submitLogin(user);
    await screen.findByText('Active Simulators');
    await user.click(screen.getByRole('button', { name: /replay datasets/i }));

    expect(await screen.findByText('No Replay Datasets Created')).toBeVisible();
    expect(screen.queryByText('BTC-USDT-Q1-Replay')).toBeNull();
    expect(screen.queryByText('ETH-USDT-May-Micro')).toBeNull();
    expect(screen.queryByText('SOL-USDT-Active-Vol')).toBeNull();
    expect(screen.queryByText('XRP-USDT-Erroneous-Ticks')).toBeNull();
  });

  it('loads empty backend sandboxes after login without showing mock sandbox rows', async () => {
    const user = userEvent.setup();
    loginAdminMock.mockResolvedValueOnce({ id: 1, username: 'operator', role: 'admin' });
    listSandboxesMock.mockResolvedValueOnce([]);
    render(<App />);

    await submitLogin(user);
    await screen.findByText('Active Simulators');
    await user.click(screen.getByRole('button', { name: /^sandboxes/i }));

    expect(await screen.findByText('No sandboxes created')).toBeVisible();
    expect(screen.queryByText('Sandbox Alpha (Replay)')).toBeNull();
    expect(screen.queryByText('Sandbox Beta (Live Feed)')).toBeNull();
    expect(screen.queryByText('Sandbox Gamma (Archived)')).toBeNull();
  });

  it('loads empty backend live metadata and symbol health without mock rows', async () => {
    const user = userEvent.setup();
    loginAdminMock.mockResolvedValueOnce({ id: 1, username: 'operator', role: 'admin' });
    listLiveAccountsMock.mockResolvedValueOnce([]);
    listLiveSymbolsMock.mockResolvedValueOnce([]);
    render(<App />);

    await submitLogin(user);
    await screen.findByText('Active Simulators');

    await user.click(screen.getByRole('button', { name: /live metadata/i }));
    expect(await screen.findByText('No Live Accounts Configured')).toBeVisible();
    expect(screen.queryByText('Binance Primary-Sub01')).toBeNull();
    expect(screen.queryByText('OKX Operations Active')).toBeNull();

    await user.click(screen.getByRole('button', { name: /operator system health/i }));
    expect(screen.queryByText('BTCUSDT')).toBeNull();
    expect(screen.queryByText('ETHUSDT')).toBeNull();
    expect(screen.queryByText('ADAUSDT')).toBeNull();
  });

  it('keeps the console locked and displays the backend message when dataset bootstrap fails', async () => {
    const user = userEvent.setup();
    loginAdminMock.mockResolvedValueOnce({ id: 1, username: 'operator', role: 'admin' });
    listDatasetsMock.mockRejectedValueOnce(new ApiError(401, 'UNAUTHORIZED', 'admin login required', null));
    render(<App />);

    await submitLogin(user);

    expect(await screen.findByText('admin login required')).toBeVisible();
    expect(screen.getByRole('button', { name: /authenticate operator session/i })).toBeVisible();
    expect(screen.queryByText('Active Simulators')).toBeNull();
  });
});

async function submitLogin(user: ReturnType<typeof userEvent.setup>) {
  const usernameInput = screen.getByPlaceholderText('e.g. ops_manager');
  const passwordInput = document.querySelector('input[type="password"]');
  if (!(passwordInput instanceof HTMLInputElement)) {
    throw new Error('Expected password input to render');
  }

  await user.type(usernameInput, 'operator');
  await user.type(passwordInput, 'secret');
  await user.click(screen.getByRole('button', { name: /authenticate operator session/i }));
}

const sandboxFixture: Sandbox = {
  id: 'sandbox-1',
  name: 'Sandbox One',
  mode: 'Replay',
  status: 'Running',
  datasetId: 'dataset-1',
  datasetName: 'Dataset One',
  accountsCount: 0,
  replayTime: '2026-01-01T00:00:00Z',
  freshness: 'ready',
  updated: '2026-01-01T00:00:00Z',
};

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

function monitorSnapshotFixture(overrides: Partial<SandboxMonitorSnapshot> = {}): SandboxMonitorSnapshot {
  return {
    sandboxId: 'sandbox-1',
    replayCurrentTime: '2026-01-01T00:00:00Z',
    replaySpeed: 1,
    status: 'running',
    accounts: [],
    orders: [],
    trades: [],
    positions: [],
    indicators: {},
    freshnessAt: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}
