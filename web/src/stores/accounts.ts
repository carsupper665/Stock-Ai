import { ref } from 'vue'
import { defineStore } from 'pinia'
import { apiClient } from '@/services/api/client'
import type { Account, ListResponse, TokenReveal } from '@/types/domain'

interface KnownTokenMeta {
  id: string
  accountId: string
  scopes: string[]
}

export const useAccountsStore = defineStore('accounts', () => {
  const sandboxAccounts = ref<Record<string, Account[]>>({})
  const liveAccounts = ref<Account[]>([])
  const tokenReveal = ref<TokenReveal | null>(null)
  const knownTokens = ref<Record<string, KnownTokenMeta[]>>({})
  const loading = ref(false)

  function showIssuedToken(payload: TokenReveal) {
    tokenReveal.value = payload
  }

  function dismissTokenReveal() {
    tokenReveal.value = null
  }

  function rememberToken(token: KnownTokenMeta) {
    const current = knownTokens.value[token.accountId] ?? []
    const filtered = current.filter((item) => item.id !== token.id)
    knownTokens.value[token.accountId] = [{ ...token }, ...filtered]
  }

  async function loadSandboxAccounts(sandboxId: string) {
    loading.value = true
    try {
      const response = await apiClient.get<ListResponse<Account>>(`/admin/sandboxes/${sandboxId}/accounts`)
      sandboxAccounts.value[sandboxId] = response.items
      return response.items
    } finally {
      loading.value = false
    }
  }

  async function loadLiveAccounts() {
    loading.value = true
    try {
      const response = await apiClient.get<ListResponse<Account>>('/admin/live-accounts')
      liveAccounts.value = response.items
      return response.items
    } finally {
      loading.value = false
    }
  }

  async function createLiveAccount(payload: Partial<Account>) {
    const created = await apiClient.post<Account>('/admin/live-accounts', payload)
    liveAccounts.value = [created, ...liveAccounts.value]
    return created
  }

  async function updateLiveAccount(accountId: string, payload: Partial<Account>) {
    const updated = await apiClient.patch<Account>(`/admin/live-accounts/${accountId}`, payload)
    liveAccounts.value = liveAccounts.value.map((item) => item.id === accountId ? updated : item)
    return updated
  }

  async function deleteLiveAccount(accountId: string) {
    await apiClient.delete(`/admin/live-accounts/${accountId}`)
    liveAccounts.value = liveAccounts.value.filter((item) => item.id !== accountId)
  }

  async function updateAccount(accountId: string, payload: Partial<Account>) {
    return apiClient.patch<Account>(`/admin/accounts/${accountId}`, payload)
  }

  async function deleteAccount(accountId: string, sandboxId?: string) {
    await apiClient.delete(`/admin/accounts/${accountId}`)
    if (sandboxId && sandboxAccounts.value[sandboxId]) {
      sandboxAccounts.value[sandboxId] = sandboxAccounts.value[sandboxId].filter((item) => item.id !== accountId)
    }
  }

  async function createToken(accountId: string, scopes: string[], name = 'web-issued-token') {
    const response = await apiClient.post<{ id: string; account_id: string; scopes: string[]; token: string }>('/tokens', {
      account_id: accountId,
      name,
      scopes,
    })
    showIssuedToken({ tokenId: response.id, token: response.token, accountId: response.account_id, scopes: response.scopes })
    rememberToken({ id: response.id, accountId: response.account_id, scopes: response.scopes })
    return response
  }

  async function rotateKnownToken(tokenId: string, accountId: string) {
    const response = await apiClient.post<{ id: string; account_id: string; scopes: string[]; token: string }>(`/tokens/${tokenId}/rotate`)
    showIssuedToken({ tokenId: response.id, token: response.token, accountId: response.account_id, scopes: response.scopes })
    rememberToken({ id: response.id, accountId: response.account_id, scopes: response.scopes })
    return response
  }

  async function revokeKnownToken(tokenId: string, accountId: string) {
    await apiClient.delete(`/tokens/${tokenId}`)
    knownTokens.value[accountId] = (knownTokens.value[accountId] ?? []).filter((item) => item.id !== tokenId)
    if (tokenReveal.value?.tokenId === tokenId) {
      dismissTokenReveal()
    }
  }

  return {
    sandboxAccounts,
    liveAccounts,
    tokenReveal,
    knownTokens,
    loading,
    showIssuedToken,
    dismissTokenReveal,
    loadSandboxAccounts,
    loadLiveAccounts,
    createLiveAccount,
    updateLiveAccount,
    deleteLiveAccount,
    updateAccount,
    deleteAccount,
    createToken,
    rotateKnownToken,
    revokeKnownToken,
  }
})
