import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuthStore } from '@/stores/auth'
import { apiClient } from '@/services/api/client'

vi.mock('@/services/api/client', () => ({
  apiClient: {
    post: vi.fn(),
  },
}))

describe('auth store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('stores the admin session after a successful login', async () => {
    vi.mocked(apiClient.post).mockResolvedValue({
      id: 1,
      username: 'root',
      role: 6,
    })

    const store = useAuthStore()
    await store.login({ username: 'root', password: 'root-pass' })

    expect(store.isAuthenticated).toBe(true)
    expect(store.user?.username).toBe('root')
  })

  it('clears session state on unauthorized handling', () => {
    const store = useAuthStore()
    store.user = { id: 1, username: 'root', role: 6 }

    store.handleUnauthorized()

    expect(store.isAuthenticated).toBe(false)
    expect(store.user).toBeNull()
  })
})
