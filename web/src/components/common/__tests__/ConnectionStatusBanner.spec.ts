import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ConnectionStatusBanner from '@/components/common/ConnectionStatusBanner.vue'

describe('ConnectionStatusBanner', () => {
  it('renders status-specific copy', () => {
    const wrapper = mount(ConnectionStatusBanner, {
      props: {
        state: 'lagging',
        lastMessageAt: '2025-01-01T00:00:00Z',
      },
    })

    expect(wrapper.text()).toContain('Data delayed')
  })
})
