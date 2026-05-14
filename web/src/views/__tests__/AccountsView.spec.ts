import { nextTick, reactive } from 'vue'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AccountsView from '@/views/admin/AccountsView.vue'

let sandboxesStore: any
let accountsStore: any

const sandbox = {
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
}

const account = {
  id: 'acct-1',
  sandbox_id: 'sbx-1',
  name: 'Agent 1',
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

describe('AccountsView', () => {
  beforeEach(() => {
    sandboxesStore = reactive({
      items: [sandbox],
      loadList: vi.fn().mockResolvedValue([sandbox]),
      createSandboxAccount: vi.fn(),
    })

    accountsStore = reactive({
      sandboxAccounts: {},
      knownTokens: {},
      tokenReveal: null,
      loadSandboxAccounts: vi.fn().mockImplementation(async (sandboxId: string) => {
        accountsStore.sandboxAccounts[sandboxId] = [account]
        await Promise.resolve()
        return [account]
      }),
      updateAccount: vi.fn(),
      deleteAccount: vi.fn(),
      createToken: vi.fn(),
      rotateKnownToken: vi.fn(),
      dismissTokenReveal: vi.fn(),
    })
  })

  it('renders sandbox accounts even when draft state is initialized after the account list updates', async () => {
    const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    const wrapper = shallowMount(AccountsView, {
      global: {
        stubs: {
          DenseTable: { template: '<div><slot /></div>' },
          EmptyState: { template: '<div><slot /></div>' },
          ErrorState: { template: '<div><slot /></div>' },
          SummaryCard: true,
        },
      },
    })

    await flushPromises()
    await nextTick()

    expect(wrapper.text()).toContain('acct-1')
    expect(wrapper.findAll('input').some((input) => (input.element as HTMLInputElement).value === 'Agent 1')).toBe(true)
    expect(consoleWarn).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('rotates a pasted token id without needing an account id', async () => {
    const wrapper = shallowMount(AccountsView, {
      global: {
        stubs: {
          DenseTable: { template: '<div><slot /></div>' },
          EmptyState: { template: '<div><slot /></div>' },
          ErrorState: { template: '<div><slot /></div>' },
          SummaryCard: true,
        },
      },
    })

    await flushPromises()
    await nextTick()

    await wrapper.get('input[placeholder="tok_..."]').setValue('tok_old')
    await wrapper.get('form.token-id-form').trigger('submit')

    expect(accountsStore.rotateKnownToken).toHaveBeenCalledWith('tok_old')
  })
})


