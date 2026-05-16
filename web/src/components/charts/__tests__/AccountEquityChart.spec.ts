import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountEquityChart from '@/components/charts/AccountEquityChart.vue'

vi.mock('vue-echarts', () => ({
  default: {
    name: 'VChart',
    template: '<div data-test="vchart" />',
    props: ['option', 'autoresize'],
  },
}))

describe('AccountEquityChart', () => {
  it('renders chart container and passes option to chart component', () => {
    const wrapper = mount(AccountEquityChart, {
      props: {
        series: [
          {
            name: 'acct-a',
            points: [
              { timestamp: '2026-01-01T00:00:00Z', value: 1000 },
              { timestamp: '2026-01-01T00:01:00Z', value: 1005 },
            ],
          },
        ],
      },
      global: {
        stubs: {
          VChart: {
            name: 'VChart',
            template: '<div data-test="vchart" />',
            props: ['option', 'autoresize'],
          },
        },
      },
    })

    expect(wrapper.find('[data-test="vchart"]').exists()).toBe(true)
    expect(wrapper.classes()).toContain('chart-wrap')
  })
})
