<template>
  <section class="banner" :class="stateClass">
    <div>
      <strong>{{ title }}</strong>
      <p>{{ subtitle }}</p>
    </div>
    <small v-if="lastMessageAt">Last message {{ lastMessageAt }}</small>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  state: 'connected' | 'lagging' | 'disconnected'
  lastMessageAt?: string | null
}>()

const title = computed(() => {
  if (props.state === 'connected') return 'Realtime connected'
  if (props.state === 'lagging') return 'Data delayed'
  return 'Realtime disconnected'
})

const subtitle = computed(() => {
  if (props.state === 'connected') return 'Streaming monitor events normally.'
  if (props.state === 'lagging') return 'Realtime is still open, but updates are older than expected.'
  return 'Falling back to periodic refresh for the current page.'
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
