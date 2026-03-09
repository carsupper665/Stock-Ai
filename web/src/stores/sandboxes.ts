import { ref } from 'vue'
import { defineStore } from 'pinia'
import { apiClient } from '@/services/api/client'
import type { Account, ListResponse, ReplayControlStatus, Sandbox, SandboxSnapshot } from '@/types/domain'

export const useSandboxesStore = defineStore('sandboxes', () => {
  const items = ref<Sandbox[]>([])
  const byId = ref<Record<string, SandboxSnapshot>>({})
  const loading = ref(false)
  const pendingReplayAction = ref<Record<string, boolean>>({})

  function upsertSandbox(sandbox: Sandbox) {
    const existing = items.value.findIndex((item) => item.id === sandbox.id)
    if (existing >= 0) {
      items.value.splice(existing, 1, sandbox)
    } else {
      items.value.unshift(sandbox)
    }
  }

  function applyReplayControl(sandboxId: string, replayControl: ReplayControlStatus) {
    const snapshot = byId.value[sandboxId]
    if (snapshot) {
      snapshot.replay_control = replayControl
      snapshot.sandbox.replay_current_time = replayControl.current_time
      snapshot.sandbox.replay_speed = replayControl.speed
      snapshot.sandbox.status = replayControl.status
    }
    const sandbox = items.value.find((item) => item.id === sandboxId)
    if (sandbox) {
      sandbox.replay_current_time = replayControl.current_time
      sandbox.replay_speed = replayControl.speed
      sandbox.status = replayControl.status
    }
  }

  async function loadList() {
    loading.value = true
    try {
      const response = await apiClient.get<ListResponse<Sandbox>>('/admin/sandboxes')
      items.value = response.items
      return response.items
    } finally {
      loading.value = false
    }
  }

  async function loadSnapshot(sandboxId: string) {
    const snapshot = await apiClient.get<SandboxSnapshot>(`/admin/monitor/sandboxes/${sandboxId}/snapshot`)
    byId.value[sandboxId] = snapshot
    upsertSandbox(snapshot.sandbox)
    return snapshot
  }

  async function createSandbox(payload: Partial<Sandbox>) {
    const created = await apiClient.post<Sandbox>('/admin/sandboxes', payload)
    upsertSandbox(created)
    return created
  }

  async function updateSandbox(sandboxId: string, payload: Partial<Sandbox>) {
    const updated = await apiClient.patch<Sandbox>(`/admin/sandboxes/${sandboxId}`, payload)
    upsertSandbox(updated)
    if (byId.value[sandboxId]) {
      byId.value[sandboxId].sandbox = updated
    }
    return updated
  }

  async function startSandbox(sandboxId: string) {
    const sandbox = await apiClient.post<Sandbox>(`/admin/sandboxes/${sandboxId}/start`, {})
    upsertSandbox(sandbox)
    await loadSnapshot(sandboxId)
    return sandbox
  }

  async function pauseSandbox(sandboxId: string) {
    const sandbox = await apiClient.post<Sandbox>(`/admin/sandboxes/${sandboxId}/pause`, {})
    upsertSandbox(sandbox)
    await loadSnapshot(sandboxId)
    return sandbox
  }

  async function stopSandbox(sandboxId: string) {
    const sandbox = await apiClient.post<Sandbox>(`/admin/sandboxes/${sandboxId}/stop`, {})
    upsertSandbox(sandbox)
    await loadSnapshot(sandboxId)
    return sandbox
  }

  async function deleteSandbox(sandboxId: string) {
    await apiClient.delete(`/admin/sandboxes/${sandboxId}`)
    items.value = items.value.filter((item) => item.id !== sandboxId)
    delete byId.value[sandboxId]
  }

  async function createSandboxAccount(sandboxId: string, payload: { name: string; initial_balance: number }) {
    const account = await apiClient.post<Account>(`/admin/sandboxes/${sandboxId}/accounts`, payload)
    if (byId.value[sandboxId]) {
      byId.value[sandboxId].accounts = [account, ...byId.value[sandboxId].accounts]
    }
    return account
  }

  async function seekReplay(sandboxId: string, replayCurrentTime: string) {
    const snapshot = byId.value[sandboxId]
    const previous = snapshot?.replay_control ? { ...snapshot.replay_control } : null
    pendingReplayAction.value[sandboxId] = true
    if (snapshot?.replay_control) {
      snapshot.replay_control.current_time = replayCurrentTime
    }
    try {
      const updated = await apiClient.post<ReplayControlStatus>(`/admin/sandboxes/${sandboxId}/replay/seek`, {
        replay_current_time: replayCurrentTime,
      })
      applyReplayControl(sandboxId, updated)
      return updated
    } catch (error) {
      if (previous && snapshot) {
        snapshot.replay_control = previous
      }
      throw error
    } finally {
      pendingReplayAction.value[sandboxId] = false
    }
  }

  async function setReplaySpeed(sandboxId: string, replaySpeed: number) {
    pendingReplayAction.value[sandboxId] = true
    try {
      const updated = await apiClient.post<ReplayControlStatus>(`/admin/sandboxes/${sandboxId}/replay/speed`, { replay_speed: replaySpeed })
      applyReplayControl(sandboxId, updated)
      return updated
    } finally {
      pendingReplayAction.value[sandboxId] = false
    }
  }

  async function resumeReplay(sandboxId: string) {
    pendingReplayAction.value[sandboxId] = true
    try {
      const updated = await apiClient.post<ReplayControlStatus>(`/admin/sandboxes/${sandboxId}/replay/resume`, {})
      applyReplayControl(sandboxId, updated)
      return updated
    } finally {
      pendingReplayAction.value[sandboxId] = false
    }
  }

  return {
    items,
    byId,
    loading,
    pendingReplayAction,
    loadList,
    loadSnapshot,
    createSandbox,
    updateSandbox,
    startSandbox,
    pauseSandbox,
    stopSandbox,
    deleteSandbox,
    createSandboxAccount,
    seekReplay,
    setReplaySpeed,
    resumeReplay,
  }
})
