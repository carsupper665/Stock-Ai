import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { apiClient, ApiError } from '@/services/api/client'
import type { AdminUser, ListResponse, Sandbox } from '@/types/domain'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<AdminUser | null>(null)
  const status = ref<'unknown' | 'authenticated' | 'unauthenticated'>('unknown')
  const pending = ref(false)
  const errorMessage = ref('')

  const isAuthenticated = computed(() => status.value === 'authenticated')

  async function login(credentials: { username: string; password: string }) {
    pending.value = true
    errorMessage.value = ''
    try {
      const sessionUser = await apiClient.post<AdminUser>('/admin/login', credentials)
      user.value = sessionUser
      status.value = 'authenticated'
      return sessionUser
    } catch (error) {
      const message = error instanceof ApiError ? error.message : 'Login failed'
      errorMessage.value = message
      status.value = 'unauthenticated'
      throw error
    } finally {
      pending.value = false
    }
  }

  async function probeSession() {
    if (status.value === 'authenticated') {
      return true
    }
    try {
      await apiClient.get<ListResponse<Sandbox>>('/admin/sandboxes')
      user.value ??= { id: 0, username: 'Admin Session', role: 6 }
      status.value = 'authenticated'
      return true
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        handleUnauthorized()
        return false
      }
      throw error
    }
  }

  function handleUnauthorized() {
    user.value = null
    status.value = 'unauthenticated'
  }

  return { user, status, pending, errorMessage, isAuthenticated, login, probeSession, handleUnauthorized }
})
