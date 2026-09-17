import { onScopeDispose, reactive, ref } from 'vue'

// 殼用：任何一次載入成功都更新 at（右上角「更新於」）；key 一變整頁重建＝全域「重新整理」
export const sync = reactive({ at: null, key: 0 })

export function useLoad(fn) {
  const data = ref(null)
  const error = ref(null)
  const loading = ref(false)
  const at = ref(null)
  let generation = 0
  let disposed = false
  onScopeDispose(() => { disposed = true; generation++ })

  async function reload() {
    if (disposed) return
    const request = ++generation
    loading.value = true
    error.value = null
    try {
      const result = await fn()
      if (request !== generation || disposed) return
      data.value = result
      at.value = sync.at = new Date()
    } catch (e) {
      if (request === generation && !disposed) error.value = e
    } finally {
      if (request === generation && !disposed) loading.value = false
    }
  }

  reload()
  return { data, error, loading, at, reload }
}
