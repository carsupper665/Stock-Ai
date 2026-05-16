import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AuthLayout from '@/layouts/AuthLayout.vue'

describe('AuthLayout', () => {
  it('renders router view container', () => {
    const wrapper = mount(AuthLayout, {
      global: {
        stubs: {
          RouterView: { template: '<div data-test="router-view" />' },
        },
      },
    })

    expect(wrapper.find('[data-test="router-view"]').exists()).toBe(true)
  })
})
