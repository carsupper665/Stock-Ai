import { ref } from 'vue'

export function useLoad(fn) {
  const data = ref(null)
  const error = ref(null)
  const loading = ref(false)
  const at = ref(null)

  async function reload() {
    loading.value = true
    error.value = null
    try {
      data.value = await fn()
      at.value = new Date()
    } catch (e) {
      error.value = e
    } finally {
      loading.value = false
    }
  }

  reload()
  return { data, error, loading, at, reload }
}
