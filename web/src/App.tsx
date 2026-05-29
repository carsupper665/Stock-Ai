import { useState, useEffect } from 'react';
import {
  ActivePage,
  Theme,
  ConnectionState,
  ViewState,
  Dataset,
  SandboxAccount,
  LiveAccount,
  Sandbox,
  SystemSymbol,
  OperationalLog,
  SystemAlert
} from './types';

// Component imports
import Sidebar from './components/Sidebar';
import Header from './components/Header';
import LoginForm from './components/LoginForm';
import DatasetsView from './components/DatasetsView';
import SandboxAccountsView from './components/SandboxAccountsView';
import LiveAccountsView from './components/LiveAccountsView';
import SystemHealthView from './components/SystemHealthView';
import SandboxesView from './components/SandboxesView';
import SandboxDetailView from './components/SandboxDetailView';
import GlobalOverview from './components/GlobalOverview';
import SandboxMonitor from './components/SandboxMonitor';
import PerformanceView from './components/PerformanceView';
import ActivityView from './components/ActivityView';
import AlertsView from './components/AlertsView';
import AgentBoundaryView from './components/AgentBoundaryView';
import TradingConsoleView from './components/TradingConsoleView';
import TechnicalAnalysisView from './components/TechnicalAnalysisView';
import { loginAdmin } from './api/auth';
import { listSandboxAccounts } from './api/accounts';
import { listDatasets } from './api/datasets';
import { listLiveAccounts } from './api/liveAccounts';
import { listSandboxes } from './api/sandboxes';
import { listLiveSymbols } from './api/systemHealth';
import { getSandboxMonitorSnapshot, type SandboxMonitorSnapshot } from './api/monitor';

export default function App() {
  // Global Workspace states
  const [theme, setTheme] = useState<Theme>(() => {
    const saved = localStorage.getItem('theme');
    if (saved === 'dark' || saved === 'light') return saved;
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  });

  // Synchronize theme globally to document element and persist in localStorage
  useEffect(() => {
    localStorage.setItem('theme', theme);
    document.documentElement.setAttribute('data-theme', theme);
    if (theme === 'dark') {
      document.documentElement.classList.add('dark');
    } else {
      document.documentElement.classList.remove('dark');
    }
  }, [theme]);

  const [activePage, setActivePage] = useState<ActivePage>('overview');
  const [selectedSandboxId, setSelectedSandboxId] = useState<string>('');
  const [isLoggedIn, setIsLoggedIn] = useState<boolean>(false);

  // Dashboard parameters
  const [connectionState, setConnectionState] = useState<ConnectionState>('Disconnected');
  const [playbackSpeed, setPlaybackSpeed] = useState<number>(1);
  const [viewState, setViewState] = useState<ViewState>('normal');

  // Dynamic Workspace collection databases
  const [datasets, setDatasets] = useState<Dataset[]>([]);
  const [, setDatasetLoadError] = useState<string | null>(null);
  const [sandboxAccounts, setSandboxAccounts] = useState<SandboxAccount[]>([]);
  const [liveAccounts, setLiveAccounts] = useState<LiveAccount[]>([]);
  const [sandboxes, setSandboxes] = useState<Sandbox[]>([]);
  const [symbols, setSymbols] = useState<SystemSymbol[]>([]);
  const [logs, setLogs] = useState<OperationalLog[]>([]);
  const [alerts, setAlerts] = useState<SystemAlert[]>([]);
  const [snapshotsBySandboxId, setSnapshotsBySandboxId] = useState<Record<string, SandboxMonitorSnapshot>>({});
  const sandboxIds = sandboxes.map((sandbox) => sandbox.id).join('|');

  // Sync dataset and sandbox statistics statefully
  useEffect(() => {
    // Dynamically query account numbers for each sandbox
    const updated = sandboxes.map((sb) => {
      const matchAccounts = sandboxAccounts.filter((a) => a.sandboxId === sb.id && !a.tokenRevoked);
      return {
        ...sb,
        accountsCount: matchAccounts.length
      };
    });
    // Prevent infinite cycles
    if (JSON.stringify(updated.map(s => s.accountsCount)) !== JSON.stringify(sandboxes.map(s => s.accountsCount))) {
      setSandboxes(updated);
    }
  }, [sandboxAccounts, sandboxes]);

  useEffect(() => {
    if (!isLoggedIn || sandboxes.length === 0) {
      return;
    }

    const missingSnapshotSandboxes = sandboxes.filter((sandbox) => snapshotsBySandboxId[sandbox.id] === undefined);
    if (missingSnapshotSandboxes.length === 0) {
      return;
    }

    let cancelled = false;
    Promise.all(missingSnapshotSandboxes.map((sandbox) => getSandboxMonitorSnapshot(sandbox.id)))
      .then((snapshots) => {
        if (cancelled) {
          return;
        }
        const snapshotMap = indexSnapshotsBySandboxId(snapshots);
        setSnapshotsBySandboxId((prev) => ({ ...prev, ...snapshotMap }));
        setSandboxes((prev) => withSandboxBackendData(prev, { ...snapshotsBySandboxId, ...snapshotMap }, datasets));
      })
      .catch(() => {
        if (!cancelled) {
          setConnectionState('Stale Data');
        }
      });

    return () => {
      cancelled = true;
    };
  }, [datasets, isLoggedIn, sandboxIds, snapshotsBySandboxId, sandboxes]);

  const clearWorkspaceData = () => {
    setDatasets([]);
    setDatasetLoadError(null);
    setSandboxAccounts([]);
    setLiveAccounts([]);
    setSandboxes([]);
    setSymbols([]);
    setLogs([]);
    setAlerts([]);
    setSnapshotsBySandboxId({});
    setSelectedSandboxId('');
    setActivePage('overview');
    setConnectionState('Disconnected');
  };

  const setAuthenticated = (status: boolean) => {
    if (!status) {
      clearWorkspaceData();
    }
    setIsLoggedIn(status);
  };

  // Handle addition callbacks statefully so user additions render in real-time
  const handleAddDataset = (newDs: Dataset) => {
    setDatasets((prev) => [newDs, ...prev]);
  };

  const handleAddAccount = (newAcc: SandboxAccount) => {
    setSandboxAccounts((prev) => [newAcc, ...prev]);
  };

  const handleUpdateAccount = (updatedAcc: SandboxAccount) => {
    setSandboxAccounts((prev) => prev.map((a) => (a.id === updatedAcc.id ? updatedAcc : a)));
  };

  const handleAddLiveAccount = (newLa: LiveAccount) => {
    setLiveAccounts((prev) => [newLa, ...prev]);
  };

  const handleDeleteLiveAccount = (id: string) => {
    setLiveAccounts((prev) => prev.filter((la) => la.id !== id));
  };

  const handleUpdateLiveAccount = (updated: LiveAccount) => {
    setLiveAccounts((prev) => prev.map((la) => (la.id === updated.id ? updated : la)));
  };

  const handleAddSandbox = (newSb: Sandbox) => {
    setSandboxes((prev) => [...prev, newSb]);
  };

  const handleUpdateSandbox = (updatedSb: Sandbox) => {
    setSandboxes((prev) => prev.map((s) => (s.id === updatedSb.id ? updatedSb : s)));
  };

  const handleDeleteSandbox = (sandboxId: string) => {
    setSandboxes((prev) => prev.filter((sandbox) => sandbox.id !== sandboxId));
    setSnapshotsBySandboxId((prev) => {
      const { [sandboxId]: _removedSnapshot, ...remainingSnapshots } = prev;
      return remainingSnapshots;
    });
    if (selectedSandboxId === sandboxId) {
      setSelectedSandboxId('');
    }
  };

  const handleClearAlerts = () => {
    // Soft clear (keep resolved)
    setAlerts((prev) => prev.filter((a) => a.severity === 'Resolved'));
  };

  // Select a sandbox and switch to monitor automatically
  const handleSelectSandbox = (id: string) => {
    setSelectedSandboxId(id);
    setActivePage('sandbox-detail');
  };

  // Nav helper for components
  const handleNavigate = (page: ActivePage) => {
    setActivePage(page);
  };

  const handleLogin = async (username: string, password: string) => {
    try {
      await loginAdmin({ username, password });
      setViewState('loading');
      const [backendDatasets, backendSandboxes, backendLiveAccounts, backendSymbols] = await Promise.all([
        listDatasets(),
        listSandboxes(),
        listLiveAccounts(),
        listLiveSymbols(),
      ]);
      const backendSnapshots = await Promise.all(
        backendSandboxes.map((sandbox) => getSandboxMonitorSnapshot(sandbox.id)),
      );
      const snapshotMap = indexSnapshotsBySandboxId(backendSnapshots);
      const backendAccounts = enrichSandboxAccountNames(
        (await Promise.all(backendSandboxes.map((sandbox) => listSandboxAccounts(sandbox.id)))).flat(),
        backendSandboxes,
      );
      setDatasets(backendDatasets);
      setSandboxes(withSandboxBackendData(withSandboxAccountCounts(backendSandboxes, backendAccounts), snapshotMap, backendDatasets));
      setSelectedSandboxId(backendSandboxes[0]?.id || '');
      setSandboxAccounts(backendAccounts);
      setLiveAccounts(backendLiveAccounts);
      setSymbols(backendSymbols);
      setSnapshotsBySandboxId(snapshotMap);
      setLogs([]);
      setAlerts(alertsFromLiveSymbols(backendSymbols));
      setDatasetLoadError(null);
      setConnectionState('Connected');
      setViewState('normal');
      setIsLoggedIn(true);
    } catch (error) {
      setIsLoggedIn(false);
      setDatasetLoadError(error instanceof Error ? error.message : 'Unable to load replay datasets.');
      setConnectionState('Disconnected');
      setViewState('normal');
      throw error;
    }
  };

  // Render proper body content
  const renderPageBody = () => {
    switch (activePage) {
      case 'overview':
        return (
          <GlobalOverview
            theme={theme}
            viewState={viewState}
            sandboxes={sandboxes}
            accounts={sandboxAccounts}
            logs={logs}
            alerts={alerts}
            openOrderCount={countSnapshotRecords(snapshotsBySandboxId, 'orders')}
            onNavigate={handleNavigate}
          />
        );
      case 'datasets':
        return (
          <DatasetsView
            theme={theme}
            viewState={viewState}
            datasets={datasets}
            onAddDataset={handleAddDataset}
            datasetActionsEnabled={true}
          />
        );
      case 'sandbox-accounts':
        return (
          <SandboxAccountsView
            theme={theme}
            viewState={viewState}
            accounts={sandboxAccounts}
            onAddAccount={handleAddAccount}
            onUpdateAccount={handleUpdateAccount}
            sandboxes={sandboxes.map((s) => ({ id: s.id, name: s.name }))}
          />
        );
      case 'live-accounts':
        return (
          <LiveAccountsView
            theme={theme}
            viewState={viewState}
            liveAccounts={liveAccounts}
            onAddLiveAccount={handleAddLiveAccount}
            onDeleteLiveAccount={handleDeleteLiveAccount}
            onUpdateLiveAccount={handleUpdateLiveAccount}
          />
        );
      case 'system-health':
        return (
          <SystemHealthView
            theme={theme}
            viewState={viewState}
            symbols={symbols}
          />
        );
      case 'sandboxes':
        return (
          <SandboxesView
            theme={theme}
            viewState={viewState}
            sandboxes={sandboxes}
            onAddSandbox={handleAddSandbox}
            onUpdateSandbox={handleUpdateSandbox}
            onDeleteSandbox={handleDeleteSandbox}
            onSelectSandbox={handleSelectSandbox}
            datasets={datasets.map((d) => ({ id: d.id, name: d.name, symbol: d.symbol }))}
          />
        );
      case 'sandbox-detail':
        return (
          <SandboxDetailView
            theme={theme}
            viewState={viewState}
            sandbox={sandboxes.find((sandbox) => sandbox.id === selectedSandboxId)}
            accounts={sandboxAccounts}
            logs={logs}
            snapshotsBySandboxId={snapshotsBySandboxId}
            playbackSpeed={playbackSpeed}
            setPlaybackSpeed={setPlaybackSpeed}
            onOpenMonitor={() => setActivePage('sandbox-monitor')}
          />
        );
      case 'sandbox-monitor':
        return (
          <SandboxMonitor
            theme={theme}
            viewState={viewState}
            sandboxes={sandboxes}
            accounts={sandboxAccounts}
            logs={logs}
            snapshotsBySandboxId={snapshotsBySandboxId}
            playbackSpeed={playbackSpeed}
            setPlaybackSpeed={setPlaybackSpeed}
          />
        );
      case 'performance':
        return (
          <PerformanceView
            theme={theme}
            viewState={viewState}
            accounts={sandboxAccounts}
          />
        );
      case 'activity':
        return (
          <ActivityView
            theme={theme}
            viewState={viewState}
            logs={logs}
          />
        );
      case 'alerts':
        return (
          <AlertsView
            theme={theme}
            viewState={viewState}
            alerts={alerts}
            onClearAll={handleClearAlerts}
          />
        );
      case 'agent-boundary':
        return (
          <AgentBoundaryView
            theme={theme}
          />
        );
      case 'trading-console':
        return (
          <TradingConsoleView theme={theme} />
        );
      case 'technical-analysis':
        return (
          <TechnicalAnalysisView theme={theme} />
        );
      default:
        return (
          <div className="p-6 text-center text-slate-500">
            Navigation node not active. Set pages via sidebar index.
          </div>
        );
    }
  };

  return (
    <div
      className={`min-h-screen flex transition-colors duration-200 ${
        theme === 'dark' ? 'dark bg-app-bg text-app-text-main' : 'bg-app-bg text-app-text-main'
      }`}
    >
      {/* Sidebar Navigation */}
      <Sidebar
        activePage={activePage}
        setActivePage={setActivePage}
        theme={theme}
        isLoggedIn={isLoggedIn}
        setIsLoggedIn={setAuthenticated}
        datasetCount={datasets.length}
        sandboxCount={sandboxes.filter((s) => s.status === 'Running').length}
        alertCount={alerts.filter((a) => a.severity === 'Critical').length}
      />

      {/* Main Container Content */}
      <div className="flex-1 flex flex-col min-w-0">
        <Header
          activePage={activePage}
          theme={theme}
          setTheme={setTheme}
          connectionState={connectionState}
          replayTimeLabel={currentReplayTimeLabel(selectedSandboxId, sandboxes, snapshotsBySandboxId)}
        />

        <main className="flex-1 overflow-y-auto">
          {/* Locked gateway auth gate */}
          {!isLoggedIn ? (
            <LoginForm
              theme={theme}
              onLogin={handleLogin}
            />
          ) : (
            renderPageBody()
          )}
        </main>
      </div>
    </div>
  );
}

function enrichSandboxAccountNames(accounts: SandboxAccount[], sandboxes: Sandbox[]): SandboxAccount[] {
  const sandboxNames = new Map(sandboxes.map((sandbox) => [sandbox.id, sandbox.name]));
  return accounts.map((account) => ({
    ...account,
    sandboxName: sandboxNames.get(account.sandboxId) ?? account.sandboxName,
  }));
}

function withSandboxAccountCounts(sandboxes: Sandbox[], accounts: SandboxAccount[]): Sandbox[] {
  return sandboxes.map((sandbox) => ({
    ...sandbox,
    accountsCount: accounts.filter((account) => account.sandboxId === sandbox.id && !account.tokenRevoked).length,
  }));
}

function indexSnapshotsBySandboxId(snapshots: readonly SandboxMonitorSnapshot[]): Record<string, SandboxMonitorSnapshot> {
  return Object.fromEntries(snapshots.map((snapshot) => [snapshot.sandboxId, snapshot]));
}

function withSandboxBackendData(
  sandboxes: Sandbox[],
  snapshotsBySandboxId: Record<string, SandboxMonitorSnapshot>,
  datasets: Dataset[],
): Sandbox[] {
  const datasetNames = new Map(datasets.map((dataset) => [dataset.id, dataset.name]));
  return sandboxes.map((sandbox) => {
    const snapshot = snapshotsBySandboxId[sandbox.id];
    return {
      ...sandbox,
      datasetName: sandbox.datasetId ? datasetNames.get(sandbox.datasetId) ?? sandbox.datasetName : sandbox.datasetName,
      replayTime: snapshot?.replayCurrentTime ?? sandbox.replayTime,
      freshness: snapshot?.freshnessAt ?? sandbox.freshness,
    };
  });
}

function countSnapshotRecords(
  snapshotsBySandboxId: Record<string, SandboxMonitorSnapshot>,
  key: 'accounts' | 'orders' | 'positions' | 'trades',
): number {
  return Object.values(snapshotsBySandboxId).reduce((sum, snapshot) => sum + snapshot[key].length, 0);
}

function alertsFromLiveSymbols(symbols: readonly SystemSymbol[]): SystemAlert[] {
  return symbols
    .filter((symbol) => symbol.status !== 'Healthy')
    .map((symbol) => ({
      id: `symbol-${symbol.symbol}`,
      title: `${symbol.symbol} feed ${symbol.status.toLowerCase()}`,
      explanation: `${symbol.feedSource} reported ${symbol.latencyMs}ms freshness at ${symbol.updated}.`,
      severity: symbol.status === 'Critical' ? 'Critical' : 'Warning',
      timestamp: symbol.updated,
    }));
}

function currentReplayTimeLabel(
  selectedSandboxId: string,
  sandboxes: readonly Sandbox[],
  snapshotsBySandboxId: Record<string, SandboxMonitorSnapshot>,
): string {
  const selectedId = selectedSandboxId || sandboxes[0]?.id || '';
  if (selectedId !== '' && snapshotsBySandboxId[selectedId]?.replayCurrentTime) {
    return snapshotsBySandboxId[selectedId].replayCurrentTime;
  }
  const selectedSandbox = sandboxes.find((sandbox) => sandbox.id === selectedId) ?? sandboxes[0];
  return selectedSandbox?.replayTime ?? 'No sandbox loaded';
}
