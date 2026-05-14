import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/LoginView.vue'
import { apiClient, ApiError } from '@/services/api/client'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRouter: () => ({ push }),
}))

vi.mock('@/services/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/services/api/client')>('@/services/api/client')
  return {
    ...actual,
    apiClient: {
      post: vi.fn(),
    },
  }
})

describe('LoginView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    push.mockReset()
  })

  it('does not prefill local credentials by default', () => {
    const wrapper = mount(LoginView, {
      global: { plugins: [createPinia()] },
    })

    expect((wrapper.get('input[autocomplete="username"]').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('input[type="password"]').element as HTMLInputElement).value).toBe('')
  })

  it('shows invalid credential errors without throwing from the submit handler', async () => {
    vi.mocked(apiClient.post).mockRejectedValue(new ApiError(401, { code: 'UNAUTHORIZED', message: 'invalid credentials' }))

    const wrapper = mount(LoginView, {
      global: { plugins: [createPinia()] },
    })

    await wrapper.get('input[autocomplete="username"]').setValue('root')
    await wrapper.get('input[type="password"]').setValue('wrong')

    await expect(wrapper.get('form').trigger('submit')).resolves.toBeUndefined()
    expect(wrapper.text()).toContain('invalid credentials')
    expect(push).not.toHaveBeenCalled()
  })
})
