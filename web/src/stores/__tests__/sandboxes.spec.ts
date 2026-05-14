import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSandboxesStore } from '@/stores/sandboxes'
import { apiClient } from '@/services/api/client'

vi.mock('@/services/api/client', () => ({
  apiClient: {
    get: vi.fn(),
    post: vi.fn(),
  },
}))

function sandboxFixture(overrides: Record<string, unknown> = {}) {
  return {
    id: 'sandbox-1',
    name: 'Sandbox 1',
    mode: 'replay',
    status: 'running',
    start_datetime: '2025-01-01T00:00:00Z',
    replay_current_time: '2025-01-01T00:00:00Z',
    replay_speed: 1,
    dataset_id: 'dataset-1',
    created_at: '2025-01-01T00:00:00Z',
    updated_at: '2025-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('sandboxes store replay controls', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('hydrates replay control from the backend snapshot payload', async () => {
    vi.mocked(apiClient.get).mockResolvedValue({
      sandbox: sandboxFixture(),
      replay_control: {
        sandbox_id: 'sandbox-1',
        current_time: '2025-01-01T00:00:00Z',
        speed: 1,
        status: 'running',
      },
      dataset: null,
      accounts: [],
      orders: [],
      trades: [],
      positions: [],
      freshness_at: '2025-01-01T00:00:00Z',
    })

    const store = useSandboxesStore()
    const snapshot = await store.loadSnapshot('sandbox-1')

    expect(snapshot.replay_control?.current_time).toBe('2025-01-01T00:00:00Z')
    expect(store.byId['sandbox-1'].replay_control?.status).toBe('running')
  })

  it('applies replay control updates from the dedicated replay endpoints', async () => {
    vi.mocked(apiClient.post).mockResolvedValue({
      sandbox_id: 'sandbox-1',
      current_time: '2025-01-01T00:05:00Z',
      speed: 3,
      status: 'running',
    })

    const store = useSandboxesStore()
    store.items = [sandboxFixture({ status: 'paused' })] as any
    store.byId['sandbox-1'] = {
      sandbox: sandboxFixture({ status: 'paused' }),
      replay_control: {
        sandbox_id: 'sandbox-1',
        current_time: '2025-01-01T00:00:00Z',
        speed: 1,
        status: 'paused',
      },
      dataset: null,
      accounts: [],
      orders: [],
      trades: [],
      positions: [],
      freshness_at: '2025-01-01T00:00:00Z',
    }

    await store.setReplaySpeed('sandbox-1', 3)

    expect(store.byId['sandbox-1'].replay_control?.speed).toBe(3)
    expect(store.byId['sandbox-1'].sandbox.replay_speed).toBe(3)
    expect(store.items[0].status).toBe('running')
  })

  it('rolls back optimistic replay seek when the API fails', async () => {
    vi.mocked(apiClient.post).mockRejectedValue(new Error('boom'))

    const store = useSandboxesStore()
    store.byId['sandbox-1'] = {
      sandbox: sandboxFixture(),
      replay_control: {
        sandbox_id: 'sandbox-1',
        current_time: '2025-01-01T00:00:00Z',
        speed: 1,
        status: 'running',
      },
      dataset: null,
      accounts: [],
      orders: [],
      trades: [],
      positions: [],
      freshness_at: '2025-01-01T00:00:00Z',
    }

    await expect(store.seekReplay('sandbox-1', '2025-01-01T00:01:00Z')).rejects.toThrow('boom')
    expect(store.byId['sandbox-1'].replay_control?.current_time).toBe('2025-01-01T00:00:00Z')
  })
})
