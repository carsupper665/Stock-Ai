import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useMonitorStore } from '@/stores/monitor'

vi.useFakeTimers()

describe('monitor store freshness', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.setSystemTime(new Date('2025-01-01T00:00:00Z'))
  })

  it('transitions from connected to lagging to disconnected based on freshness', () => {
    const store = useMonitorStore()
    store.markSocketConnected('global')
    store.recordEvent({ topic: 'sandbox.updated', created_at: '2025-01-01T00:00:00Z' })

    expect(store.connectionState).toBe('connected')

    vi.setSystemTime(new Date('2025-01-01T00:00:12Z'))
    expect(store.connectionState).toBe('lagging')

    store.markSocketDisconnected('global')
    expect(store.connectionState).toBe('disconnected')
  })

  it('stays connected when the scoped sandbox socket closes but the global socket is still open', () => {
    const store = useMonitorStore()
    store.markSocketConnected('global')
    store.markSocketConnected('sandbox')
    store.recordEvent({ topic: 'sandbox.updated', created_at: '2025-01-01T00:00:00Z' })

    store.markSocketDisconnected('sandbox')

    expect(store.connectionState).toBe('connected')

    store.markSocketDisconnected('global')
    expect(store.connectionState).toBe('disconnected')
  })
})
