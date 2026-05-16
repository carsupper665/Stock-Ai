import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import MonitorLayout from '@/layouts/MonitorLayout.vue'

const connectGlobal = vi.fn().mockResolvedValue(undefined)

vi.mock('@/stores/monitor', () => ({
  useMonitorStore: () => ({
    connectionState: 'connected',
    lastMessageAt: '2026-01-01T00:00:00Z',
    connectGlobal,
  }),
}))

describe('MonitorLayout', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    connectGlobal.mockClear()
  })

  it('renders monitor nav links and connects monitor stream on mount', () => {
    const wrapper = mount(MonitorLayout, {
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

    expect(wrapper.findAll('[data-to]').length).toBe(4)
    expect(wrapper.find('[data-test="router-view"]').exists()).toBe(true)
    expect(connectGlobal).toHaveBeenCalledTimes(1)
  })
})
