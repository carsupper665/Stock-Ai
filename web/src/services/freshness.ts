export type ConnectionState = 'connected' | 'lagging' | 'disconnected'

export function deriveConnectionState(socketStatus: 'idle' | 'connected' | 'disconnected', lastMessageAt?: string | null, now = Date.now()): ConnectionState {
  if (socketStatus === 'disconnected') {
    return 'disconnected'
  }
  if (!lastMessageAt) {
    return socketStatus === 'connected' ? 'connected' : 'lagging'
  }

  const ageMs = now - new Date(lastMessageAt).getTime()
  if (ageMs > 10000) {
    return 'lagging'
  }
  return 'connected'
}

export function formatTimestamp(value?: string | null) {
  if (!value) {
    return 'Never'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return date.toLocaleString()
}

export function formatRelativeFreshness(value?: string | null, now = Date.now()) {
  if (!value) {
    return 'No data'
  }
  const delta = Math.max(0, Math.floor((now - new Date(value).getTime()) / 1000))
  if (delta < 60) {
    return `${delta}s ago`
  }
  const minutes = Math.floor(delta / 60)
  if (minutes < 60) {
    return `${minutes}m ago`
  }
  const hours = Math.floor(minutes / 60)
  return `${hours}h ago`
}
