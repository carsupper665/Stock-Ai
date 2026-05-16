import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import SummaryCard from '@/components/common/SummaryCard.vue'

describe('SummaryCard', () => {
  it('renders label/value/detail and badge slot', () => {
    const wrapper = mount(SummaryCard, {
      props: {
        label: '沙盒總數',
        value: 42,
        detail: '描述文案',
      },
      slots: {
        badge: '<span data-test="badge">NEW</span>',
      },
    })

    expect(wrapper.text()).toContain('沙盒總數')
    expect(wrapper.text()).toContain('42')
    expect(wrapper.text()).toContain('描述文案')
    expect(wrapper.get('[data-test="badge"]').text()).toBe('NEW')
  })
})

