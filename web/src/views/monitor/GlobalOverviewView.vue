<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Monitor Workspace</p>
        <h2>Global Overview</h2>
        <p>Desktop-first control room showing current sandbox state, live cache health, and the most recent alert stream.</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh overview' }}</button>
    </header>

    <ErrorState v-if="errorMessage" title="Overview unavailable" :description="errorMessage" retry-label="Retry" @retry="refresh" />

    <div class="kpi-grid">
      <SummaryCard label="Sandboxes" :value="sandboxes.items.length" detail="Replay workspaces currently available to monitor." />
      <SummaryCard label="Running" :value="runningCount" detail="Sandboxes actively advancing replay time." />
      <SummaryCard label="Total equity" :value="formatCurrency(totalEquity)" detail="Aggregate equity across all loaded sandbox accounts." />
      <SummaryCard label="Open alerts" :value="monitor.alerts.length" detail="Alert items derived from the admin monitor websocket." />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Sandbox Fleet</p>
          <h3>Current State</h3>
        </div>
        <div v-if="snapshots.length" class="stack">
          <RouterLink v-for="snapshot in snapshots" :key="snapshot.sandbox.id" :to="`/monitor/sandboxes/${snapshot.sandbox.id}`" class="panel sandbox-card">
            <div class="page-header compact-header">
              <div>
                <strong>{{ snapshot.sandbox.name }}</strong>
                <p class="muted code">{{ snapshot.sandbox.id }}</p>
              </div>
              <StatusBadge :label="snapshot.sandbox.status" />
            </div>
            <div class="card-grid">
              <div>
                <span class="muted">Replay time</span>
                <strong>{{ formatDateTime(snapshot.replay_control?.current_time ?? snapshot.sandbox.replay_current_time) }}</strong>
              </div>
              <div>
                <span class="muted">Accounts</span>
                <strong>{{ snapshot.accounts.length }}</strong>
              </div>
              <div>
                <span class="muted">Positions</span>
                <strong>{{ snapshot.positions.length }}</strong>
              </div>
              <div>
                <span class="muted">Freshness</span>
                <strong><FreshnessLabel :value="snapshot.freshness_at" /></strong>
              </div>
            </div>
          </RouterLink>
        </div>
        <EmptyState v-else title="No sandbox snapshots" description="Refresh after the backend is running and admin login is active." />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Live Cache</p>
          <h3>Symbol Health</h3>
        </div>
        <div class="three-column compact-grid" v-if="monitor.liveSymbols">
          <SummaryCard label="Active" :value="monitor.liveSymbols.summary.active_symbols" detail="Symbols currently in memory." />
          <SummaryCard label="Stale" :value="monitor.liveSymbols.summary.stale_symbols" detail="Symbols not recently refreshed." />
          <SummaryCard label="Degraded" :value="monitor.liveSymbols.summary.degraded_symbols" detail="Symbols with connection or provider issues." />
        </div>
        <DenseTable v-if="monitor.liveSymbols?.items.length">
          <table>
            <thead>
              <tr>
                <th>Symbol</th>
                <th>State</th>
                <th>Price</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in monitor.liveSymbols.items.slice(0, 8)" :key="item.symbol">
                <td class="code">{{ item.symbol }}</td>
                <td><StatusBadge :label="item.state" /></td>
                <td class="metric">{{ formatCurrency(item.price) }}</td>
                <td><FreshnessLabel :value="item.last_updated" /></td>
              </tr>
            </tbody>
          </table>
        </DenseTable>
        <EmptyState v-else title="No live cache data" description="Live symbol activity will appear here after live market reads populate the provider cache." />
      </section>
    </div>

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">Risk Feed</p>
        <h3>Recent Alerts</h3>
      </div>
      <div v-if="monitor.alerts.length" class="stack">
        <article v-for="alert in monitor.alerts.slice(0, 12)" :key="alert.id" class="panel alert-card" :class="alert.severity">
          <div class="page-header compact-header">
            <div>
              <strong>{{ alert.title }}</strong>
              <p class="muted">{{ alert.topic }} · {{ formatDateTime(alert.created_at) }}</p>
            </div>
            <StatusBadge :label="alert.severity" />
          </div>
          <p class="muted">{{ alert.message }}</p>
        </article>
      </div>
      <EmptyState v-else title="No alerts yet" description="Websocket-derived risk and system alerts will accumulate here." />
    </section>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import FreshnessLabel from '@/components/common/FreshnessLabel.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useFallbackPolling } from '@/composables/useFallbackPolling'
import { useMonitorStore } from '@/stores/monitor'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatCurrency, formatDateTime } from '@/utils/formatters'

const sandboxes = useSandboxesStore()
const monitor = useMonitorStore()
const loading = ref(false)
const errorMessage = ref('')

const snapshots = computed(() => sandboxes.items.map((sandbox) => sandboxes.byId[sandbox.id]).filter(Boolean))
const runningCount = computed(() => sandboxes.items.filter((item) => item.status === 'running').length)
const totalEquity = computed(() => snapshots.value.reduce((sum, snapshot) => sum + snapshot.accounts.reduce((inner, account) => inner + account.equity, 0), 0))

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    await sandboxes.loadList()
    await Promise.all(
      sandboxes.items.map(async (sandbox) => {
        const snapshot = await sandboxes.loadSnapshot(sandbox.id)
        monitor.ingestSandboxSnapshot(snapshot)
      }),
    )
    await monitor.loadLiveSymbols()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to refresh monitor overview.'
  } finally {
    loading.value = false
  }
}

useFallbackPolling(() => monitor.connectionState === 'disconnected', async () => {
  await refresh()
})

onMounted(() => {
  void refresh()
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
.compact-header {
  align-items: center;
}
.sandbox-card {
  padding: 1rem;
  display: grid;
  gap: 1rem;
}
.card-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
}
.card-grid strong,
.card-grid span {
  display: block;
}
.compact-grid {
  grid-template-columns: repeat(3, minmax(0, 1fr));
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
