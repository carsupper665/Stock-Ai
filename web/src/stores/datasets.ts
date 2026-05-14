import { ref } from 'vue'
import { defineStore } from 'pinia'
import { apiClient } from '@/services/api/client'
import type { DatasetImportJob, DatasetSummary, ReplayDataset, ListResponse } from '@/types/domain'

export const useDatasetsStore = defineStore('datasets', () => {
  const items = ref<DatasetSummary[]>([])
  const selected = ref<DatasetSummary | null>(null)
  const loading = ref(false)
  const activeJobs = ref<Record<string, DatasetImportJob>>({})

  async function loadList() {
    loading.value = true
    try {
      const response = await apiClient.get<ListResponse<DatasetSummary>>('/admin/replay-datasets')
      items.value = response.items
      return response.items
    } finally {
      loading.value = false
    }
  }

  async function loadOne(datasetId: string) {
    const dataset = await apiClient.get<DatasetSummary>(`/admin/replay-datasets/${datasetId}`)
    selected.value = dataset
    return dataset
  }

  async function createDataset(payload: Partial<ReplayDataset>) {
    const created = await apiClient.post<ReplayDataset>('/admin/replay-datasets', payload)
    await loadList()
    return created
  }

  async function importDataset(datasetId: string, file: File) {
    const formData = new FormData()
    formData.append('file', file)
    const job = await apiClient.upload<DatasetImportJob>(`/admin/replay-datasets/${datasetId}/import`, formData)
    activeJobs.value[datasetId] = job
    await loadOne(datasetId)
    await loadList()
    return job
  }

  return { items, selected, loading, activeJobs, loadList, loadOne, createDataset, importDataset }
})
