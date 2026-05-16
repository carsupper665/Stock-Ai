import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DenseTable from '@/components/common/DenseTable.vue'

describe('DenseTable', () => {
  it('renders slot table content', () => {
    const wrapper = mount(DenseTable, {
      slots: {
        default: '<table><tbody><tr><td>row</td></tr></tbody></table>',
      },
    })

    expect(wrapper.find('table').exists()).toBe(true)
    expect(wrapper.text()).toContain('row')
  })
})

