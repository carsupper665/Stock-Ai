import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Dataset } from '../types';
import { createReplayDataset, importReplayDataset } from '../api/datasets';
import DatasetsView from './DatasetsView';

vi.mock('../api/datasets', () => ({
  createReplayDataset: vi.fn(),
  importReplayDataset: vi.fn(),
}));

const createReplayDatasetMock = vi.mocked(createReplayDataset);
const importReplayDatasetMock = vi.mocked(importReplayDataset);

const unsupportedMessage = 'Dataset create/import is not connected in this MVP. Dataset rows come from the backend only.';

describe('DatasetsView MVP dataset actions', () => {
  beforeEach(() => {
    createReplayDatasetMock.mockReset();
    importReplayDatasetMock.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it('does not add a local dataset when create is submitted while actions are disabled', async () => {
    const user = userEvent.setup();
    const onAddDataset = vi.fn();
    renderDatasetsView({ onAddDataset });

    await user.click(screen.getByRole('button', { name: /import dataset/i }));
    await user.click(screen.getByRole('button', { name: /assemble dataset metadata/i }));

    expect(onAddDataset).not.toHaveBeenCalled();
    expect(screen.getByText(unsupportedMessage)).toBeVisible();
  });

  it('uses the exact unsupported message for disabled create/import actions', async () => {
    const user = userEvent.setup();
    renderDatasetsView();

    await user.click(screen.getByRole('button', { name: /import dataset/i }));
    await user.click(screen.getByRole('button', { name: /assemble dataset metadata/i }));

    expect(screen.getByText(unsupportedMessage)).toBeVisible();
  });

  it('does not add or prefill a fake dropped dataset while actions are disabled', async () => {
    const user = userEvent.setup();
    const onAddDataset = vi.fn();
    renderDatasetsView({ onAddDataset });

    await user.click(screen.getByRole('button', { name: /import dataset/i }));
    const dropTarget = screen.getByText('Drag & Drop CSV / JSON').closest('div');
    if (!(dropTarget instanceof HTMLDivElement)) {
      throw new Error('Expected drop target to render');
    }

    fireEvent.dragOver(dropTarget);
    fireEvent.drop(dropTarget, {
      dataTransfer: {
        files: [new File(['symbol,price'], 'dataset.csv', { type: 'text/csv' })],
      },
    });

    expect(onAddDataset).not.toHaveBeenCalled();
    expect(screen.getByText(unsupportedMessage)).toBeVisible();
    expect(screen.queryByDisplayValue('Dropped_CSV_Replay_Buffer')).toBeNull();
    expect(screen.queryByDisplayValue('Dropped File Local Metadata')).toBeNull();
  });

  it('opens the backend dataset form from an empty backend dataset list', async () => {
    const user = userEvent.setup();
    renderDatasetsView({ datasetActionsEnabled: true, datasets: [] });

    await user.click(screen.getByRole('button', { name: /upload dataset record/i }));

    expect(screen.getByLabelText(/dataset name/i)).toBeVisible();
    expect(screen.getByRole('button', { name: /assemble dataset metadata/i })).toBeVisible();
  });

  it('creates dataset metadata through the backend service when actions are enabled', async () => {
    const user = userEvent.setup();
    const onAddDataset = vi.fn();
    createReplayDatasetMock.mockResolvedValueOnce({ ...datasetFixture, id: 'created-dataset', name: 'Backend Dataset Import' });
    renderDatasetsView({ onAddDataset, datasetActionsEnabled: true });

    await user.click(screen.getByRole('button', { name: /import dataset/i }));
    await fillDatasetForm(user);
    await user.click(screen.getByRole('button', { name: /assemble dataset metadata/i }));

    expect(createReplayDatasetMock).toHaveBeenCalledWith({
      name: 'Backend Dataset Import',
      symbol: 'BTCUSDT',
      interval: '1m',
      source: 'Backend CSV Upload',
    });
    expect(onAddDataset).toHaveBeenCalledWith(expect.objectContaining({ id: 'created-dataset' }));
  });

  it('imports a dropped file after backend dataset metadata is created', async () => {
    const user = userEvent.setup();
    const file = new File(['symbol,price'], 'dataset.csv', { type: 'text/csv' });
    createReplayDatasetMock.mockResolvedValueOnce({ ...datasetFixture, id: 'created-dataset' });
    importReplayDatasetMock.mockResolvedValueOnce({ id: 'job-1', status: 'running' });
    renderDatasetsView({ datasetActionsEnabled: true });

    await user.click(screen.getByRole('button', { name: /import dataset/i }));
    const dropTarget = screen.getByText('Drag & Drop CSV / JSON').closest('div');
    if (!(dropTarget instanceof HTMLDivElement)) {
      throw new Error('Expected drop target to render');
    }

    fireEvent.drop(dropTarget, { dataTransfer: { files: [file] } });
    await user.type(screen.getByLabelText(/symbol pair/i), 'BTCUSDT');
    await user.click(screen.getByRole('button', { name: /assemble dataset metadata/i }));

    expect(importReplayDatasetMock).toHaveBeenCalledWith('created-dataset', file);
  });

  it('shows backend errors and does not add fake rows when create fails', async () => {
    const user = userEvent.setup();
    const onAddDataset = vi.fn();
    createReplayDatasetMock.mockRejectedValueOnce(new Error('dataset name already exists'));
    renderDatasetsView({ onAddDataset, datasetActionsEnabled: true });

    await user.click(screen.getByRole('button', { name: /import dataset/i }));
    await fillDatasetForm(user);
    await user.click(screen.getByRole('button', { name: /assemble dataset metadata/i }));

    expect(await screen.findByText('dataset name already exists')).toBeVisible();
    expect(onAddDataset).not.toHaveBeenCalled();
  });

  it('disables dataset submit while the backend request is pending', async () => {
    const user = userEvent.setup();
    createReplayDatasetMock.mockImplementationOnce(() => new Promise(() => undefined));
    renderDatasetsView({ datasetActionsEnabled: true });

    await user.click(screen.getByRole('button', { name: /import dataset/i }));
    await fillDatasetForm(user);
    await user.click(screen.getByRole('button', { name: /assemble dataset metadata/i }));

    await waitFor(() => expect(screen.getByRole('button', { name: /saving dataset/i })).toBeDisabled());
  });
});

async function fillDatasetForm(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/dataset name/i), 'Backend Dataset Import');
  await user.type(screen.getByLabelText(/symbol pair/i), 'BTCUSDT');
  await user.type(screen.getByLabelText(/data feed source/i), 'Backend CSV Upload');
}

function renderDatasetsView(
  options: {
    readonly datasetActionsEnabled?: boolean;
    readonly datasets?: Dataset[];
    readonly onAddDataset?: (dataset: Dataset) => void;
  } = {},
) {
  return render(
    <DatasetsView
      theme="light"
      viewState="normal"
      datasets={options.datasets ?? [datasetFixture]}
      onAddDataset={options.onAddDataset ?? vi.fn()}
      datasetActionsEnabled={options.datasetActionsEnabled}
    />,
  );
}

const datasetFixture: Dataset = {
  id: 'dataset-1',
  name: 'Backend Dataset',
  symbol: 'BTC-USDT',
  interval: '1m',
  timeRange: '2026-01-01T00:00:00Z to 2026-01-01T01:00:00Z',
  source: 'Backend',
  status: 'Imported',
  updated: '2026-01-01T01:00:00Z',
  size: '12 rows',
};
