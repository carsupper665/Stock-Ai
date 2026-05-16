import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AdminLayout from '@/layouts/AdminLayout.vue'

const probeSession = vi.fn().mockResolvedValue(true)
const connectGlobal = vi.fn().mockResolvedValue(undefined)

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => ({ fullPath: '/admin/sandboxes' }),
  }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { username: 'admin' },
    probeSession,
  }),
}))

vi.mock('@/stores/monitor', () => ({
  useMonitorStore: () => ({
    connectionState: 'connected',
    lastMessageAt: '2026-01-01T00:00:00Z',
    connectGlobal,
  }),
}))

describe('AdminLayout', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    probeSession.mockClear()
    connectGlobal.mockClear()
  })

  it('renders nav links and triggers auth/monitor probes on mount', () => {
    const wrapper = mount(AdminLayout, {
      global: {
        plugins: [createPinia()],
        stubs: {
          RouterView: { template: '<div data-test="router-view" />' },
          RouterLink: {
            props: ['to'],
            template: '<a :data-to="to"><slot /></a>',
          },
          ConnectionStatusBanner: { template: '<div data-test="banner" />' },
        },
      },
    })

    expect(wrapper.findAll('[data-to]').length).toBeGreaterThan(0)
    expect(wrapper.find('[data-test="router-view"]').exists()).toBe(true)
    expect(probeSession).toHaveBeenCalledTimes(1)
    expect(connectGlobal).toHaveBeenCalledTimes(1)
  })
})
