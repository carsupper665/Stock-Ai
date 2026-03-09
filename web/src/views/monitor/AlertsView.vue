<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Risk / Alert Feed</p>
        <h2>Alerts</h2>
        <p>Explicit degraded-state view so websocket incidents, stop-loss triggers, and replay control events do not silently disappear into raw logs.</p>
      </div>
      <div class="actions filters">
        <select v-model="severityFilter" class="control compact-control">
          <option value="">All severities</option>
          <option value="critical">critical</option>
          <option value="warning">warning</option>
          <option value="info">info</option>
        </select>
        <select v-model="sandboxFilter" class="control compact-control">
          <option value="">All sandboxes</option>
          <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">{{ sandbox.name }}</option>
        </select>
      </div>
    </header>

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">Filtered Feed</p>
        <h3>Alert Cards</h3>
      </div>
      <div v-if="filteredAlerts.length" class="stack">
        <article v-for="alert in filteredAlerts" :key="alert.id" class="panel alert-card" :class="alert.severity">
          <div class="page-header compact-header">
            <div>
              <strong>{{ alert.title }}</strong>
              <p class="muted">{{ alert.topic }} · {{ formatDateTime(alert.created_at) }}</p>
            </div>
            <StatusBadge :label="alert.severity" />
          </div>
          <p class="muted">{{ alert.message }}</p>
          <p class="muted" v-if="alert.sandbox_id">Sandbox: {{ sandboxName(alert.sandbox_id) }}</p>
        </article>
      </div>
      <EmptyState v-else title="No alerts match" description="Adjust the filters or wait for new websocket-derived alerts." />
    </section>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import EmptyState from '@/components/common/EmptyState.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import { useMonitorStore } from '@/stores/monitor'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatDateTime } from '@/utils/formatters'

const monitor = useMonitorStore()
const sandboxes = useSandboxesStore()
const severityFilter = ref('')
const sandboxFilter = ref('')

const filteredAlerts = computed(() => monitor.alerts.filter((alert) => {
  const severityOk = !severityFilter.value || severityFilter.value === alert.severity
  const sandboxOk = !sandboxFilter.value || sandboxFilter.value === alert.sandbox_id
  return severityOk && sandboxOk
}))

function sandboxName(sandboxId?: string) {
  return sandboxes.items.find((item) => item.id === sandboxId)?.name ?? sandboxId ?? '--'
}

onMounted(() => {
  if (!sandboxes.items.length) {
    void sandboxes.loadList()
  }
})
</script>

<style scoped>
.page-panel {
  padding: 1rem;
}
.eyebrow {
  margin: 0 0 0.35rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--muted);
  font-size: 0.75rem;
}
.filters {
  align-items: center;
}
.compact-control {
  min-width: 180px;
}
.alert-card {
  padding: 1rem;
}
.alert-card.warning {
  border-color: rgba(251, 191, 36, 0.32);
}
.alert-card.critical {
  border-color: rgba(248, 113, 113, 0.32);
}
</style>
