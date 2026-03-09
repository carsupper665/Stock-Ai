import { reactive } from 'vue'
import { shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SandboxDetailView from '@/views/admin/SandboxDetailView.vue'

const sandboxSnapshot = {
  sandbox: {
    id: 'sbx-1',
    name: 'Sandbox 1',
    mode: 'replay',
    status: 'ready',
    start_datetime: '2025-01-01T00:00:00Z',
    replay_current_time: '2025-01-01T00:00:00Z',
    replay_speed: 1,
    dataset_id: 'dts-1',
    created_at: '2025-01-01T00:00:00Z',
    updated_at: '2025-01-01T00:00:00Z',
  },
  replay_control: {
    sandbox_id: 'sbx-1',
    current_time: '2025-01-01T00:00:00Z',
    speed: 1,
    status: 'ready',
  },
  dataset: {
    id: 'dts-1',
    name: 'Dataset 1',
    symbol: 'BTCUSDT',
    interval: '1m',
    source: 'test',
    start_at: '2025-01-01T00:00:00Z',
    end_at: '2025-01-01T00:02:00Z',
    created_at: '2025-01-01T00:00:00Z',
    row_count: 3,
    latest_import_job: null,
  },
  accounts: [],
  orders: [],
  trades: [],
  positions: [],
  freshness_at: '2025-01-01T00:00:00Z',
}

const sandboxesStore = reactive({
  byId: { 'sbx-1': sandboxSnapshot },
  pendingReplayAction: {},
  loadSnapshot: vi.fn().mockResolvedValue(sandboxSnapshot),
  updateSandbox: vi.fn(),
  startSandbox: vi.fn(),
  pauseSandbox: vi.fn(),
  stopSandbox: vi.fn(),
  resumeReplay: vi.fn(),
  seekReplay: vi.fn(),
  setReplaySpeed: vi.fn(),
  createSandboxAccount: vi.fn(),
})

const datasetsStore = reactive({
  items: [sandboxSnapshot.dataset],
  loadList: vi.fn().mockResolvedValue([sandboxSnapshot.dataset]),
})

const monitorStore = reactive({
  sandboxSnapshots: {},
  events: [],
  accountSeries: {},
  connectionState: 'connected',
  connectSandbox: vi.fn(() => () => {}),
  ingestSandboxSnapshot: vi.fn(),
})

const accountsStore = reactive({
  tokenReveal: null,
  knownTokens: {},
  dismissTokenReveal: vi.fn(),
  createToken: vi.fn(),
  rotateKnownToken: vi.fn(),
  revokeKnownToken: vi.fn(),
})

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 'sbx-1' } }),
}))

vi.mock('@/stores/sandboxes', () => ({
  useSandboxesStore: () => sandboxesStore,
}))

vi.mock('@/stores/datasets', () => ({
  useDatasetsStore: () => datasetsStore,
}))

vi.mock('@/stores/monitor', () => ({
  useMonitorStore: () => monitorStore,
}))

vi.mock('@/stores/accounts', () => ({
  useAccountsStore: () => accountsStore,
}))

vi.mock('@/composables/useFallbackPolling', () => ({
  useFallbackPolling: vi.fn(),
}))

describe('SandboxDetailView account form validity', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders a valid default initial balance for the sandbox account form', () => {
    const wrapper = shallowMount(SandboxDetailView, {
      global: {
        stubs: {
          AccountEquityChart: true,
          DenseTable: { template: '<div><slot /></div>' },
          EmptyState: { template: '<div><slot /></div>' },
          ErrorState: { template: '<div><slot /></div>' },
          EventFeedItem: true,
          FreshnessLabel: true,
          ReplayTimelineScrubber: true,
          SpeedSelector: true,
          StatusBadge: true,
          SummaryCard: true,
        },
      },
    })

    const initialBalance = wrapper.get('input[type="number"]').element as HTMLInputElement

    expect(initialBalance.value).toBe('1000')
    expect(initialBalance.validity.stepMismatch).toBe(false)
    expect(initialBalance.checkValidity()).toBe(true)
  })
})
