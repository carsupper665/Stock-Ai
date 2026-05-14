import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useAccountsStore } from '@/stores/accounts'
import { apiClient } from '@/services/api/client'

vi.mock('@/services/api/client', () => ({
  apiClient: {
    post: vi.fn(),
  },
}))

describe('accounts store token reveal', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('keeps token plaintext only until dismissed', () => {
    const store = useAccountsStore()
    store.showIssuedToken({ tokenId: 'tok_1', token: 'plain.secret', accountId: 'acct_1' })

    expect(store.tokenReveal?.token).toBe('plain.secret')

    store.dismissTokenReveal()

    expect(store.tokenReveal).toBeNull()
  })

  it('rotates by token id and stores the token under the backend account id', async () => {
    vi.mocked(apiClient.post).mockResolvedValue({
      id: 'tok_old',
      account_id: 'acct_real',
      scopes: ['trade:write'],
      token: 'tok_old.sec_new',
    })

    const store = useAccountsStore()
    await store.rotateKnownToken('tok_old')

    expect(apiClient.post).toHaveBeenCalledWith('/tokens/tok_old/rotate')
    expect(store.tokenReveal?.token).toBe('tok_old.sec_new')
    expect(store.tokenReveal?.accountId).toBe('acct_real')
    expect(store.knownTokens.acct_real[0].id).toBe('tok_old')
    expect(store.knownTokens.acct_wrong).toBeUndefined()
  })
})
