import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useAccountsStore } from '@/stores/accounts'

describe('accounts store token reveal', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('keeps token plaintext only until dismissed', () => {
    const store = useAccountsStore()
    store.showIssuedToken({ tokenId: 'tok_1', token: 'plain.secret', accountId: 'acct_1' })

    expect(store.tokenReveal?.token).toBe('plain.secret')

    store.dismissTokenReveal()

    expect(store.tokenReveal).toBeNull()
  })
})
