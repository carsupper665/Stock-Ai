<template>
  <section class="banner" :class="stateClass">
    <div>
      <strong>{{ title }}</strong>
      <p>{{ subtitle }}</p>
    </div>
    <small v-if="lastMessageAt">最後訊息 {{ lastMessageAt }}</small>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  state: 'connected' | 'lagging' | 'disconnected'
  lastMessageAt?: string | null
}>()

const title = computed(() => {
  if (props.state === 'connected') return '即時連線正常'
  if (props.state === 'lagging') return '資料延遲'
  return '即時連線中斷'
})

const subtitle = computed(() => {
  if (props.state === 'connected') return '監控事件正在正常串流。'
  if (props.state === 'lagging') return '即時連線仍開啟，但更新時間比預期更舊。'
  return '目前頁面會改用定期重新整理。'
})

const stateClass = computed(() => `is-${props.state}`)
</script>

<style scoped>
.banner {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  align-items: center;
  padding: 0.85rem 1rem;
  border-radius: 16px;
  border: 1px solid var(--border);
  background: rgba(19, 26, 36, 0.92);
}

.banner p,
.banner small {
  margin: 0.2rem 0 0;
  color: var(--muted);
}

.is-connected {
  border-color: rgba(52, 211, 153, 0.28);
}

.is-lagging {
  border-color: rgba(251, 191, 36, 0.34);
  background: rgba(251, 191, 36, 0.08);
}

.is-disconnected {
  border-color: rgba(248, 113, 113, 0.4);
  background: rgba(248, 113, 113, 0.1);
}
</style>
