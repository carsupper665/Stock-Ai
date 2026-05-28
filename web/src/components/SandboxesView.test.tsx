import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createSandbox, deleteSandbox, pauseSandbox, startSandbox, stopSandbox } from '../api/sandboxes';
import type { Dataset, Sandbox } from '../types';
import SandboxesView from './SandboxesView';

vi.mock('../api/sandboxes', () => ({
  createSandbox: vi.fn(),
  deleteSandbox: vi.fn(),
  pauseSandbox: vi.fn(),
  startSandbox: vi.fn(),
  stopSandbox: vi.fn(),
}));

const createSandboxMock = vi.mocked(createSandbox);
const deleteSandboxMock = vi.mocked(deleteSandbox);
const pauseSandboxMock = vi.mocked(pauseSandbox);
const startSandboxMock = vi.mocked(startSandbox);
const stopSandboxMock = vi.mocked(stopSandbox);

describe('SandboxesView backend actions', () => {
  beforeEach(() => {
    createSandboxMock.mockReset();
    deleteSandboxMock.mockReset();
    pauseSandboxMock.mockReset();
    startSandboxMock.mockReset();
    stopSandboxMock.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it('creates a sandbox through the backend service', async () => {
    const user = userEvent.setup();
    const onAddSandbox = vi.fn();
    createSandboxMock.mockResolvedValueOnce({ ...sandboxFixture, id: 'created-sandbox', name: 'Backend Sandbox' });
    renderSandboxesView({ onAddSandbox });

    await user.click(screen.getByRole('button', { name: /create sandbox/i }));
    await user.type(screen.getByRole('textbox'), 'Backend Sandbox');
    await user.click(screen.getByRole('button', { name: /assemble sandbox node/i }));

    expect(createSandboxMock).toHaveBeenCalledWith({
      name: 'Backend Sandbox',
      datasetId: 'dataset-1',
      replaySpeed: 1,
    });
    expect(onAddSandbox).toHaveBeenCalledWith(expect.objectContaining({ id: 'created-sandbox' }));
  });

  it('opens the backend sandbox form from an empty backend sandbox list', async () => {
    const user = userEvent.setup();
    renderSandboxesView({ sandboxes: [] });

    await user.click(screen.getByRole('button', { name: /build sandbox/i }));

    expect(screen.getByText('Build Sandbox Node')).toBeVisible();
    expect(screen.getByRole('button', { name: /assemble sandbox node/i })).toBeVisible();
  });

  it('uses dedicated backend lifecycle endpoints and updates from backend response', async () => {
    const user = userEvent.setup();
    const onUpdateSandbox = vi.fn();
    startSandboxMock.mockResolvedValueOnce({ ...sandboxFixture, status: 'Running' });
    renderSandboxesView({ onUpdateSandbox });

    await user.click(screen.getByTitle('Start / Resume Simulation Tickers'));

    expect(startSandboxMock).toHaveBeenCalledWith('sandbox-1');
    expect(onUpdateSandbox).toHaveBeenCalledWith(expect.objectContaining({ status: 'Running' }));
  });

  it('shows backend lifecycle errors without applying fake state updates', async () => {
    const user = userEvent.setup();
    const onUpdateSandbox = vi.fn();
    startSandboxMock.mockRejectedValueOnce(new Error('sandbox cannot start'));
    renderSandboxesView({ onUpdateSandbox });

    await user.click(screen.getByTitle('Start / Resume Simulation Tickers'));

    expect(await screen.findByText('sandbox cannot start')).toBeVisible();
    expect(onUpdateSandbox).not.toHaveBeenCalled();
  });

  it('disables lifecycle buttons while a backend request is pending', async () => {
    const user = userEvent.setup();
    startSandboxMock.mockImplementationOnce(() => new Promise(() => undefined));
    renderSandboxesView();

    await user.click(screen.getByTitle('Start / Resume Simulation Tickers'));

    await waitFor(() => expect(screen.getByTitle('Start / Resume Simulation Tickers')).toBeDisabled());
  });

  it('deletes a sandbox through the backend before updating local state', async () => {
    const user = userEvent.setup();
    const onDeleteSandbox = vi.fn();
    deleteSandboxMock.mockResolvedValueOnce(undefined);
    renderSandboxesView({ onDeleteSandbox });

    await user.click(screen.getByTitle('Decommission Sandbox'));
    await user.click(screen.getByRole('button', { name: /terminate sandbox/i }));

    expect(deleteSandboxMock).toHaveBeenCalledWith('sandbox-1');
    expect(onDeleteSandbox).toHaveBeenCalledWith('sandbox-1');
  });
});

function renderSandboxesView(
  options: {
    readonly onAddSandbox?: (sandbox: Sandbox) => void;
    readonly sandboxes?: Sandbox[];
    readonly onUpdateSandbox?: (sandbox: Sandbox) => void;
    readonly onDeleteSandbox?: (id: string) => void;
  } = {},
) {
  return render(
    <SandboxesView
      theme="light"
      viewState="normal"
      sandboxes={options.sandboxes ?? [sandboxFixture]}
      onAddSandbox={options.onAddSandbox ?? vi.fn()}
      onUpdateSandbox={options.onUpdateSandbox ?? vi.fn()}
      onDeleteSandbox={options.onDeleteSandbox ?? vi.fn()}
      onSelectSandbox={vi.fn()}
      datasets={[datasetOption]}
    />,
  );
}

const sandboxFixture: Sandbox = {
  id: 'sandbox-1',
  name: 'Sandbox One',
  mode: 'Replay',
  status: 'Stopped',
  datasetId: 'dataset-1',
  datasetName: 'Dataset One',
  accountsCount: 0,
  replayTime: '2026-01-01T00:00:00Z',
  freshness: 'ready',
  updated: '2026-01-01T00:00:00Z',
};

const datasetOption: Pick<Dataset, 'id' | 'name' | 'symbol'> = {
  id: 'dataset-1',
  name: 'Dataset One',
  symbol: 'BTCUSDT',
};
