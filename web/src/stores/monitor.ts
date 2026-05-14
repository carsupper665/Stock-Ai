import { computed, customRef, ref } from 'vue'
import { defineStore } from 'pinia'
import { apiClient } from '@/services/api/client'
import { deriveConnectionState, type ConnectionState } from '@/services/freshness'
import { connectAdminMonitorSocket } from '@/services/realtime/adminMonitorSocket'
import type { AlertItem, ChartPoint, DomainEvent, LiveSymbolsSnapshot, SandboxSnapshot } from '@/types/domain'

function toAlert(event: DomainEvent): AlertItem | null {
  const topic = event.topic
  const titleMap: Record<string, { severity: AlertItem['severity']; title: string }> = {
    'stoploss.triggered': { severity: 'critical', title: 'Stop-loss triggered' },
    'dataset.import.failed': { severity: 'critical', title: 'Dataset import failed' },
    'live.connection.degraded': { severity: 'warning', title: 'Live connection degraded' },
    'live.connection.recovered': { severity: 'info', title: 'Live connection recovered' },
    'sandbox.replay.seek': { severity: 'info', title: 'Replay time changed' },
    'sandbox.replay.speed': { severity: 'info', title: 'Replay speed changed' },
    'sandbox.replay.resume': { severity: 'info', title: 'Replay resumed' },
    'order.updated': { severity: 'info', title: 'Order updated' },
    'trade.executed': { severity: 'info', title: 'Trade executed' },
  }
  const config = titleMap[topic]
  if (!config) return null
  return {
    id: `${topic}-${event.aggregate_id ?? event.created_at}`,
    severity: config.severity,
    title: config.title,
    message: JSON.stringify(event.payload ?? {}),
    topic,
    created_at: event.created_at,
    sandbox_id: event.sandbox_id,
  }
}

type SocketChannel = 'global' | 'sandbox'
type SocketStatus = 'idle' | 'connected' | 'disconnected'

export const useMonitorStore = defineStore('monitor', () => {
  const socketStates = ref<Record<SocketChannel, SocketStatus>>({
    global: 'idle',
    sandbox: 'idle',
  })
  const lastMessageAt = ref<string | null>(null)
  const events = ref<DomainEvent[]>([])
  const alerts = ref<AlertItem[]>([])
  const liveSymbols = ref<LiveSymbolsSnapshot | null>(null)
  const accountSeries = ref<Record<string, ChartPoint[]>>({})
  const sandboxSnapshots = ref<Record<string, SandboxSnapshot>>({})

  let globalTeardown: null | (() => void) = null
  let reconnectTimer: number | null = null
  let notifyConnectionState = () => {}

  const socketStatus = computed<SocketStatus>(() => {
    if (Object.values(socketStates.value).includes('connected')) {
      return 'connected'
    }
    if (Object.values(socketStates.value).includes('disconnected')) {
      return 'disconnected'
    }
    return 'idle'
  })

  const connectionState = customRef<ConnectionState>((track, trigger) => {
    notifyConnectionState = trigger
    return {
      get() {
        track()
        return deriveConnectionState(socketStatus.value, lastMessageAt.value)
      },
      set() {
        trigger()
      },
    }
  })

  function setSocketState(channel: SocketChannel, status: SocketStatus) {
    socketStates.value = {
      ...socketStates.value,
      [channel]: status,
    }
    notifyConnectionState()
  }

  function markSocketConnected(channel: SocketChannel = 'global') {
    setSocketState(channel, 'connected')
  }

  function markSocketDisconnected(channel: SocketChannel = 'global') {
    setSocketState(channel, 'disconnected')
  }

  function scheduleReconnect() {
    if (reconnectTimer !== null) return
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null
      void connectGlobal()
    }, 3000)
  }

  function recordEvent(event: Partial<DomainEvent>) {
    const normalized: DomainEvent = {
      topic: event.topic ?? 'system.unknown',
      aggregate_id: event.aggregate_id,
      account_id: event.account_id,
      sandbox_id: event.sandbox_id,
      payload: event.payload ?? {},
      created_at: event.created_at ?? new Date().toISOString(),
    }
    lastMessageAt.value = normalized.created_at
    notifyConnectionState()
    events.value = [normalized, ...events.value].slice(0, 80)
    const alert = toAlert(normalized)
    if (alert) {
      alerts.value = [alert, ...alerts.value].slice(0, 60)
    }
  }

  function ingestSandboxSnapshot(snapshot: SandboxSnapshot) {
    lastMessageAt.value = snapshot.freshness_at
    notifyConnectionState()
    sandboxSnapshots.value[snapshot.sandbox.id] = snapshot
    snapshot.accounts.forEach((account) => {
      const series = accountSeries.value[account.id] ?? []
      const point = { timestamp: snapshot.freshness_at, value: account.equity }
      const nextSeries = [...series, point].slice(-40)
      accountSeries.value[account.id] = nextSeries
    })
  }

  async function loadLiveSymbols() {
    liveSymbols.value = await apiClient.get<LiveSymbolsSnapshot>('/admin/monitor/live-symbols')
    return liveSymbols.value
  }

  async function connectGlobal() {
    if (globalTeardown) {
      return
    }
    globalTeardown = connectAdminMonitorSocket({
      onOpen: () => {
        markSocketConnected('global')
      },
      onClose: () => {
        globalTeardown = null
        markSocketDisconnected('global')
        scheduleReconnect()
      },
      onError: () => {
        markSocketDisconnected('global')
      },
      onEvent: (event) => {
        recordEvent(event)
      },
    })
  }

  function disconnectGlobal() {
    globalTeardown?.()
    globalTeardown = null
    if (reconnectTimer !== null) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    markSocketDisconnected('global')
  }

  function connectSandbox(sandboxId: string, onSnapshot?: (snapshot: SandboxSnapshot) => void) {
    return connectAdminMonitorSocket({
      sandboxId,
      onOpen: () => markSocketConnected('sandbox'),
      onClose: () => markSocketDisconnected('sandbox'),
      onError: () => markSocketDisconnected('sandbox'),
      onSnapshot: (snapshot) => {
        ingestSandboxSnapshot(snapshot)
        onSnapshot?.(snapshot)
      },
      onEvent: (event) => {
        recordEvent(event)
      },
    })
  }

  const filteredAlerts = computed(() => alerts.value)

  return {
    socketStatus,
    lastMessageAt,
    connectionState,
    events,
    alerts,
    filteredAlerts,
    liveSymbols,
    accountSeries,
    sandboxSnapshots,
    markSocketConnected,
    markSocketDisconnected,
    recordEvent,
    ingestSandboxSnapshot,
    loadLiveSymbols,
    connectGlobal,
    disconnectGlobal,
    connectSandbox,
  }
})
