<template>
  <span class="badge" :class="badgeClass"><slot>{{ label }}</slot></span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  label: string
}>()

const badgeClass = computed(() => {
  const normalized = props.label.toLowerCase()
  if (['running', 'active', 'filled', 'healthy', 'connected'].includes(normalized)) return 'success'
  if (['paused', 'lagging', 'stale', 'pending', 'queued'].includes(normalized)) return 'warning'
  if (['stopped', 'failed', 'disabled', 'rejected', 'disconnected', 'degraded'].includes(normalized)) return 'danger'
  return 'neutral'
})
</script>

<style scoped>
.badge {
  display: inline-flex;
  align-items: center;
  border-radius: 999px;
  padding: 0.28rem 0.65rem;
  font-size: 0.82rem;
  border: 1px solid var(--border);
}
.success { background: rgba(52, 211, 153, 0.12); color: #95f0c6; }
.warning { background: rgba(251, 191, 36, 0.12); color: #ffe39a; }
.danger { background: rgba(248, 113, 113, 0.14); color: #ffc6c6; }
.neutral { background: rgba(255, 255, 255, 0.06); color: var(--muted); }
</style>
