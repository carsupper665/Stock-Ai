import { onBeforeUnmount, onMounted, watch } from 'vue'

export function useFallbackPolling(disconnected: () => boolean, callback: () => Promise<void> | void, intervalMs = 10000) {
  let timer: number | null = null

  const stop = () => {
    if (timer !== null) {
      window.clearInterval(timer)
      timer = null
    }
  }

  const start = () => {
    stop()
    if (!disconnected()) {
      return
    }
    timer = window.setInterval(() => {
      void callback()
    }, intervalMs)
  }

  onMounted(start)
  onBeforeUnmount(stop)
  watch(disconnected, () => start())
}
