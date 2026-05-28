import { useState, useEffect } from 'react';
import {
  ActivePage,
  Theme,
  DemoVisualState,
  Dataset,
  SandboxAccount,
  LiveAccount,
  Sandbox,
  SystemSymbol,
  OperationalLog,
  SystemAlert
} from './types';

// Mock Data imports
import {
  initialDatasets,
  initialSandboxAccounts,
  initialLiveAccounts,
  initialSandboxes,
  initialSymbols,
  initialLogs,
  initialAlerts
} from './data/mockData';

// Component imports
import Sidebar from './components/Sidebar';
import Header from './components/Header';
import AuthDemo from './components/AuthDemo';
import DatasetsView from './components/DatasetsView';
import SandboxAccountsView from './components/SandboxAccountsView';
import LiveAccountsView from './components/LiveAccountsView';
import SystemHealthView from './components/SystemHealthView';
import SandboxesView from './components/SandboxesView';
import GlobalOverview from './components/GlobalOverview';
import SandboxMonitor from './components/SandboxMonitor';
import PerformanceView from './components/PerformanceView';
import ActivityView from './components/ActivityView';
import AlertsView from './components/AlertsView';
import AgentBoundaryView from './components/AgentBoundaryView';

export default function App() {
  // Global Workspace states
  const [theme, setTheme] = useState<Theme>(() => {
    const saved = localStorage.getItem('theme');
    return (saved === 'dark' || saved === 'light') ? saved : 'light';
  });

  // Synchronize theme globally to document element and persist in localStorage
  useEffect(() => {
    localStorage.setItem('theme', theme);
    if (theme === 'dark') {
      document.documentElement.classList.add('dark');
    } else {
      document.documentElement.classList.remove('dark');
    }
  }, [theme]);

  const [activePage, setActivePage] = useState<ActivePage>('overview');
  const [isLoggedIn, setIsLoggedIn] = useState<boolean>(true); // Logged in by default for straightforward evaluation, lockable at bottom

  // Dashboard parameters
  const [connectionState, setConnectionState] = useState<'Connected' | 'Reconnecting' | 'Disconnected' | 'Fallback Active' | 'Stale Data'>('Connected');
  const [playbackSpeed, setPlaybackSpeed] = useState<number>(1);
  const [demoState, setDemoState] = useState<DemoVisualState>('normal');
  const [simulatedTime, setSimulatedTime] = useState<string>('2026-05-24 19:33:57 UTC');

  // Dynamic Workspace collection databases
  const [datasets, setDatasets] = useState<Dataset[]>(initialDatasets);
  const [sandboxAccounts, setSandboxAccounts] = useState<SandboxAccount[]>(initialSandboxAccounts);
  const [liveAccounts, setLiveAccounts] = useState<LiveAccount[]>(initialLiveAccounts);
  const [sandboxes, setSandboxes] = useState<Sandbox[]>(initialSandboxes);
  const [symbols] = useState<SystemSymbol[]>(initialSymbols);
  const [logs, setLogs] = useState<OperationalLog[]>(initialLogs);
  const [alerts, setAlerts] = useState<SystemAlert[]>(initialAlerts);

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
  }, [sandboxAccounts]);

  // Handle addition callbacks statefully so user additions render in real-time
  const handleAddDataset = (newDs: Dataset) => {
    setDatasets((prev) => [newDs, ...prev]);
    // Log telemetry
    setLogs((prev) => [
      {
        id: `log-${Date.now()}`,
        timestamp: '2026-05-24 19:34:00',
        topic: 'System',
        severity: 'Success',
        title: `Dataset metadata loaded: ${newDs.name}`,
        details: `Import scheduled successfully under historical archives schema.`
      },
      ...prev
    ]);
  };

  const handleAddAccount = (newAcc: SandboxAccount) => {
    setSandboxAccounts((prev) => [newAcc, ...prev]);
    setLogs((prev) => [
      {
        id: `log-${Date.now()}`,
        timestamp: '2026-05-24 19:34:01',
        topic: 'Sandbox',
        severity: 'Success',
        title: `Virtual account initialized: ${newAcc.name}`,
        sandboxLabel: newAcc.sandboxName.split(' ')[0],
        details: `Assigned initial balance: $${newAcc.balance.toLocaleString()} to ${newAcc.sandboxName}.`
      },
      ...prev
    ]);
  };

  const handleUpdateAccount = (updatedAcc: SandboxAccount) => {
    setSandboxAccounts((prev) => prev.map((a) => (a.id === updatedAcc.id ? updatedAcc : a)));
    if (updatedAcc.tokenRevoked) {
      setLogs((prev) => [
        {
          id: `log-${Date.now()}`,
          timestamp: '2026-05-24 19:34:02',
          topic: 'Agent',
          severity: 'Warning',
          title: `Simulator token revoked for: ${updatedAcc.name}`,
          details: `Session connection keys permanently marked expired.`
        },
        ...prev
      ]);
      // Inject safety warning
      setAlerts((prev) => [
        {
          id: `al-${Date.now()}`,
          title: `Operator Revocation: ${updatedAcc.name}`,
          explanation: `System-wide invalidation of agent token confirmed by operator log.`,
          severity: 'Info',
          timestamp: '2026-05-24 19:34:02'
        },
        ...prev
      ]);
    }
  };

  const handleAddLiveAccount = (newLa: LiveAccount) => {
    setLiveAccounts((prev) => [newLa, ...prev]);
    setLogs((prev) => [
      {
        id: `log-${Date.now()}`,
        timestamp: '2026-05-24 19:34:03',
        topic: 'System',
        severity: 'Info',
        title: `Reference label registered: ${newLa.name}`,
        details: `Saved exchange alignment checksum for physical co-locational latency syncing.`
      },
      ...prev
    ]);
  };

  const handleAddSandbox = (newSb: Sandbox) => {
    setSandboxes((prev) => [...prev, newSb]);
    setLogs((prev) => [
      {
        id: `log-${Date.now()}`,
        timestamp: '2026-05-24 19:34:04',
        topic: 'System',
        severity: 'Success',
        title: `Sandbox created: ${newSb.name}`,
        details: `Broker kernel assigned sandbox index #${newSb.id} in Stopped state.`
      },
      ...prev
    ]);
  };

  const handleUpdateSandbox = (updatedSb: Sandbox) => {
    setSandboxes((prev) => prev.map((s) => (s.id === updatedSb.id ? updatedSb : s)));
    setLogs((prev) => [
      {
        id: `log-${Date.now()}`,
        timestamp: '2026-05-24 19:34:05',
        topic: 'Sandbox',
        severity: updatedSb.status === 'Running' ? 'Success' : 'Success',
        title: `Sandbox status modified: ${updatedSb.name}`,
        sandboxLabel: updatedSb.name.split(' ')[0],
        details: `State changed to ${updatedSb.status}. Playback speed synced to ${playbackSpeed}x`
      },
      ...prev
    ]);
  };

  const handleClearAlerts = () => {
    // Soft clear (keep resolved)
    setAlerts((prev) => prev.filter((a) => a.severity === 'Resolved'));
  };

  // Select a sandbox and switch to monitor automatically
  const handleSelectSandbox = (id: string) => {
    setActivePage('sandbox-monitor');
  };

  // Nav helper for components
  const handleNavigate = (page: ActivePage) => {
    setActivePage(page);
  };

  // Render proper body content
  const renderPageBody = () => {
    switch (activePage) {
      case 'overview':
        return (
          <GlobalOverview
            theme={theme}
            demoState={demoState}
            sandboxes={sandboxes}
            accounts={sandboxAccounts}
            logs={logs}
            alerts={alerts}
            onNavigate={handleNavigate}
          />
        );
      case 'datasets':
        return (
          <DatasetsView
            theme={theme}
            demoState={demoState}
            datasets={datasets}
            onAddDataset={handleAddDataset}
          />
        );
      case 'sandbox-accounts':
        return (
          <SandboxAccountsView
            theme={theme}
            demoState={demoState}
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
            demoState={demoState}
            liveAccounts={liveAccounts}
            onAddLiveAccount={handleAddLiveAccount}
          />
        );
      case 'system-health':
        return (
          <SystemHealthView
            theme={theme}
            demoState={demoState}
            symbols={symbols}
          />
        );
      case 'sandboxes':
        return (
          <SandboxesView
            theme={theme}
            demoState={demoState}
            sandboxes={sandboxes}
            onAddSandbox={handleAddSandbox}
            onUpdateSandbox={handleUpdateSandbox}
            onSelectSandbox={handleSelectSandbox}
            datasets={datasets.map((d) => ({ id: d.id, name: d.name, symbol: d.symbol }))}
          />
        );
      case 'sandbox-monitor':
        return (
          <SandboxMonitor
            theme={theme}
            demoState={demoState}
            sandboxes={sandboxes}
            accounts={sandboxAccounts}
            logs={logs}
            playbackSpeed={playbackSpeed}
            setPlaybackSpeed={setPlaybackSpeed}
            simulatedTime={simulatedTime}
          />
        );
      case 'performance':
        return (
          <PerformanceView
            theme={theme}
            demoState={demoState}
            accounts={sandboxAccounts}
          />
        );
      case 'activity':
        return (
          <ActivityView
            theme={theme}
            demoState={demoState}
            logs={logs}
          />
        );
      case 'alerts':
        return (
          <AlertsView
            theme={theme}
            demoState={demoState}
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
        setIsLoggedIn={setIsLoggedIn}
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
          setConnectionState={setConnectionState}
          playbackSpeed={playbackSpeed}
          setPlaybackSpeed={setPlaybackSpeed}
          demoState={demoState}
          setDemoState={setDemoState}
          simulatedTime={simulatedTime}
          setSimulatedTime={setSimulatedTime}
        />

        <main className="flex-1 overflow-y-auto">
          {/* Locked gateway auth gate */}
          {!isLoggedIn ? (
            <AuthDemo
              theme={theme}
              onLoginSuccess={() => setIsLoggedIn(true)}
            />
          ) : (
            renderPageBody()
          )}
        </main>
      </div>
    </div>
  );
}
