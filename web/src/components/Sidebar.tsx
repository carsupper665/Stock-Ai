import {
  LayoutDashboard,
  Database,
  Users,
  ShieldAlert,
  HeartPulse,
  Box,
  LineChart,
  Trophy,
  History,
  Bell,
  Shield,
  Lock,
  Unlock,
  Terminal
} from 'lucide-react';
import { ActivePage, Theme } from '../types';

interface SidebarProps {
  activePage: ActivePage;
  setActivePage: (page: ActivePage) => void;
  theme: Theme;
  isLoggedIn: boolean;
  setIsLoggedIn: (status: boolean) => void;
  // Badges counts
  datasetCount: number;
  sandboxCount: number;
  alertCount: number;
}

export default function Sidebar({
  activePage,
  setActivePage,
  theme,
  isLoggedIn,
  setIsLoggedIn,
  datasetCount,
  sandboxCount,
  alertCount
}: SidebarProps) {
  const isDark = theme === 'dark';

  const handleAuthControl = () => {
    if (isLoggedIn) {
      setIsLoggedIn(false);
    }
  };

  const groups = [
    {
      label: 'Admin Ops',
      items: [
        { id: 'datasets' as ActivePage, label: 'Replay Datasets', icon: Database, badge: datasetCount > 0 ? String(datasetCount) : undefined },
        { id: 'sandbox-accounts' as ActivePage, label: 'Sandbox Accounts', icon: Users },
        { id: 'live-accounts' as ActivePage, label: 'Live Metadata', icon: ShieldAlert, badge: 'Info' },
        { id: 'system-health' as ActivePage, label: 'Operator System Health', icon: HeartPulse, badge: 'OK' }
      ]
    },
    {
      label: 'Sandbox Control & Monitor',
      items: [
        { id: 'sandboxes' as ActivePage, label: 'Sandboxes', icon: Box, badge: sandboxCount > 0 ? String(sandboxCount) : undefined },
        { id: 'overview' as ActivePage, label: 'Global Overview', icon: LayoutDashboard },
        { id: 'sandbox-monitor' as ActivePage, label: 'Sandbox Monitor', icon: LineChart },
        { id: 'performance' as ActivePage, label: 'Performance Rank', icon: Trophy },
        { id: 'activity' as ActivePage, label: 'Operational Activity', icon: History },
        { id: 'alerts' as ActivePage, label: 'Alert Center', icon: Bell, badge: alertCount > 0 ? String(alertCount) : undefined, badgeStyle: 'error' }
      ]
    },
    {
      label: 'Agent Rules',
      items: [
        { id: 'agent-boundary' as ActivePage, label: 'Agent Boundary', icon: Shield }
      ]
    }
  ];

  return (
    <aside
      className="w-64 flex flex-col flex-shrink-0 transition-colors duration-200 border-r bg-app-sidebar text-app-text-main border-app-border"
    >
      {/* Brand Header */}
      <div className="p-5 flex items-center space-x-3 border-b border-app-border">
        <div className="p-2 rounded-lg bg-app-accent text-white flex items-center justify-center">
          <Terminal size={18} />
        </div>
        <div>
          <h2 className="text-sm font-bold tracking-tight text-app-accent uppercase">
            Sandbox Kernel
          </h2>
          <p className="text-[10px] text-app-text-muted">
            Operations Console v2.8
          </p>
        </div>
      </div>

      {/* Navigation Groups */}
      <div className="flex-1 overflow-y-auto px-3 py-4 space-y-6">
        {groups.map((group) => (
          <div key={group.label} className="space-y-1">
            <span className="px-3 text-[10px] font-semibold tracking-wider uppercase block mb-2 text-app-text-muted">
              {group.label}
            </span>
            {group.items.map((item) => {
              const IconComponent = item.icon;
              const isActive = activePage === item.id;
              
              // Define style based on theme and active status
              let itemClass = 'flex items-center justify-between px-3 py-2 rounded-lg text-xs font-medium transition-all group duration-150 border ';
              if (isActive) {
                itemClass += 'bg-app-accent/10 text-app-accent border-app-accent/20 font-semibold';
              } else {
                itemClass += 'border-transparent hover:bg-app-bg hover:text-app-text-main text-app-text-muted';
              }

              return (
                <button
                  key={item.id}
                  onClick={() => setActivePage(item.id)}
                  className={`w-full text-left ${itemClass}`}
                >
                  <div className="flex items-center space-x-2.5">
                    <IconComponent size={14} className={isActive ? 'text-app-accent' : 'text-app-text-muted group-hover:text-app-text-main'} />
                    <span>{item.label}</span>
                  </div>
                  {item.badge && (
                    <span className={`px-1.5 py-0.5 text-[9px] rounded font-bold ${
                      item.badgeStyle === 'error'
                        ? 'bg-app-error text-white'
                        : item.badge === 'OK'
                        ? 'bg-app-success/20 text-app-success'
                        : item.badge === 'Info'
                        ? 'bg-app-accent/20 text-app-accent'
                        : 'bg-app-bg border border-app-border text-app-text-muted'
                    }`}>
                      {item.badge}
                    </span>
                  )}
                </button>
              );
            })}
          </div>
        ))}
      </div>

      {/* Auth Status & Locker */}
      <div className="p-4 border-t border-app-border bg-app-bg/40">
        <div className="flex items-center justify-between">
          <div className="flex items-center space-x-2">
            <span className={`w-2 h-2 rounded-full ${isLoggedIn ? 'bg-app-success' : 'bg-app-warning animate-pulse'}`}></span>
            <div className="text-[11px]">
              <p className="font-semibold text-app-text-main">
                {isLoggedIn ? 'Operator Active' : 'Access Restricted'}
              </p>
              <p className="text-[9px] text-app-text-muted">
                {isLoggedIn ? 'Full Controls' : 'Login Required'}
              </p>
            </div>
          </div>
          <button
            onClick={handleAuthControl}
            className="p-1.5 rounded-md hover:bg-app-border/50 text-app-text-muted hover:text-app-text-main transition-colors"
            title={isLoggedIn ? 'Lock Console' : 'Access requires backend login'}
          >
            {isLoggedIn ? <Unlock size={14} /> : <Lock size={14} />}
          </button>
        </div>
      </div>
    </aside>
  );
}
