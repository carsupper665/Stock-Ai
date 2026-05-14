import { reactive } from 'vue'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AgentBoundaryView from '@/views/admin/AgentBoundaryView.vue'

let sandboxesStore: any
let accountsStore: any

const runningSandbox = {
  id: 'sbx-running',
  name: 'Runtime Sandbox',
  mode: 'replay',
  status: 'running',
  start_datetime: '2025-01-01T00:00:00Z',
  replay_current_time: '2025-01-01T00:03:00Z',
  replay_speed: 3,
  dataset_id: 'dts-1',
  runtime_anchor_at: '2025-01-01T00:02:00Z',
  created_at: '2025-01-01T00:00:00Z',
  updated_at: '2025-01-01T00:03:00Z',
}

const stoppedSandbox = {
  ...runningSandbox,
  id: 'sbx-stopped',
  name: 'Stopped Sandbox',
  status: 'stopped',
  runtime_anchor_at: null,
}

const virtualAccount = {
  id: 'acct-virtual',
  sandbox_id: 'sbx-running',
  name: 'Agent Account',
  type: 'virtual',
  base_currency: 'USD',
  initial_balance: 1000,
  wallet_balance: 1000,
  available_balance: 1000,
  locked_margin: 0,
  realized_pnl: 0,
  unrealized_pnl: 0,
  equity: 1000,
  status: 'active',
  created_at: '2025-01-01T00:00:00Z',
  updated_at: '2025-01-01T00:00:00Z',
}

vi.mock('@/stores/sandboxes', () => ({
  useSandboxesStore: () => sandboxesStore,
}))

vi.mock('@/stores/accounts', () => ({
  useAccountsStore: () => accountsStore,
}))

describe('AgentBoundaryView', () => {
  beforeEach(() => {
    sandboxesStore = reactive({
      items: [runningSandbox, stoppedSandbox],
      loadList: vi.fn().mockResolvedValue([runningSandbox, stoppedSandbox]),
    })

    accountsStore = reactive({
      sandboxAccounts: {
        'sbx-running': [virtualAccount],
      },
      knownTokens: {
        'acct-virtual': [{ id: 'tok-1', accountId: 'acct-virtual', scopes: ['market:read', 'trade:read'] }],
      },
      loadSandboxAccounts: vi.fn().mockResolvedValue([virtualAccount]),
    })
  })

  it('renders runtime state and the agent HTTP boundary policy', async () => {
    const wrapper = shallowMount(AgentBoundaryView, {
      global: {
        stubs: {
          DenseTable: { template: '<div><slot /></div>' },
          EmptyState: { template: '<div><slot /></div>' },
          ErrorState: { template: '<div><slot /></div>' },
          StatusBadge: true,
          SummaryCard: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('Agent 沙盒邊界')
    expect(wrapper.text()).toContain('Runtime Sandbox')
    expect(wrapper.text()).toContain('自動 tick')
    expect(wrapper.text()).toContain('GET /sandbox/time')
    expect(wrapper.text()).toContain('GET /market/klines')
    expect(wrapper.text()).toContain('to <= sandbox current_time')
    expect(wrapper.text()).toContain('禁止 /admin/*')
    expect(wrapper.text()).toContain('stopped 沙盒不可重新啟動')
    expect(accountsStore.loadSandboxAccounts).toHaveBeenCalledWith('sbx-running')
  })
})
