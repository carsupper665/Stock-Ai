import type { DomainEvent, SandboxSnapshot } from '@/types/domain'

export interface AdminMonitorSocketOptions {
  sandboxId?: string
  onOpen?: () => void
  onClose?: () => void
  onError?: () => void
  onEvent?: (event: DomainEvent) => void
  onSnapshot?: (snapshot: SandboxSnapshot) => void
}

function resolveWsBase() {
  const explicitBase = (import.meta.env.VITE_WS_BASE_URL as string | undefined)?.replace(/\/$/, '')
  if (explicitBase) {
    return explicitBase
  }
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}`
}

function buildSocketUrl(path: string, query?: Record<string, string | undefined>) {
  const url = new URL(`${resolveWsBase()}${path}`)
  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value) {
      url.searchParams.set(key, value)
    }
  })
  return url.toString()
}

function isSnapshotEnvelope(payload: DomainEvent | { type: 'snapshot'; payload: SandboxSnapshot }): payload is { type: 'snapshot'; payload: SandboxSnapshot } {
  return 'type' in payload && payload.type === 'snapshot'
}

export function connectAdminMonitorSocket(options: AdminMonitorSocketOptions) {
  const socket = new WebSocket(buildSocketUrl('/ws/admin/monitor', { sandbox_id: options.sandboxId }))

  socket.addEventListener('open', () => options.onOpen?.())
  socket.addEventListener('close', () => options.onClose?.())
  socket.addEventListener('error', () => options.onError?.())
  socket.addEventListener('message', (message) => {
    const payload = JSON.parse(message.data) as DomainEvent | { type: 'snapshot'; payload: SandboxSnapshot }
    if (isSnapshotEnvelope(payload)) {
      options.onSnapshot?.(payload.payload)
      return
    }
    options.onEvent?.(payload)
  })

  return () => {
    if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
      socket.close()
    }
  }
}
