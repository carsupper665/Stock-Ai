<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">風險 / 告警串流</p>
        <h2>告警</h2>
        <p>明確呈現降級狀態，避免 websocket 事件、停損觸發與回放控制事件只消失在原始日誌中。</p>
      </div>
      <div class="actions filters">
        <select v-model="severityFilter" class="control compact-control">
          <option value="">所有嚴重度</option>
          <option value="critical">嚴重</option>
          <option value="warning">警告</option>
          <option value="info">資訊</option>
        </select>
        <select v-model="sandboxFilter" class="control compact-control">
          <option value="">所有沙盒</option>
          <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">{{ sandbox.name }}</option>
        </select>
      </div>
    </header>

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">篩選串流</p>
        <h3>告警卡片</h3>
      </div>
      <div v-if="filteredAlerts.length" class="stack">
        <article v-for="alert in filteredAlerts" :key="alert.id" class="panel alert-card" :class="alert.severity">
          <div class="page-header compact-header">
            <div>
              <strong>{{ alert.title }}</strong>
              <p class="muted">{{ formatEventTopic(alert.topic) }} · {{ formatDateTime(alert.created_at) }}</p>
            </div>
            <StatusBadge :label="alert.severity" />
          </div>
          <p class="muted">{{ alert.message }}</p>
          <p class="muted" v-if="alert.sandbox_id">沙盒：{{ sandboxName(alert.sandbox_id) }}</p>
        </article>
      </div>
      <EmptyState v-else title="沒有符合條件的告警" description="調整篩選條件，或等待新的 websocket 告警。" />
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
import { formatEventTopic } from '@/utils/localization'

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
