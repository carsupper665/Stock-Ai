<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">System Health</p>
        <h2>Live Symbol Cache</h2>
        <p>Inspect provider freshness, subscriber counts, and degraded/healthy symbol states exposed by the P1 live market cache.</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh cache' }}</button>
    </header>

    <ErrorState v-if="errorMessage" title="System health unavailable" :description="errorMessage" retry-label="Retry" @retry="refresh" />

    <template v-else-if="monitor.liveSymbols">
      <div class="kpi-grid">
        <SummaryCard label="Active symbols" :value="monitor.liveSymbols.summary.active_symbols" detail="Symbols currently tracked in memory." />
        <SummaryCard label="Stale" :value="monitor.liveSymbols.summary.stale_symbols" detail="Symbols whose freshness has degraded beyond the healthy threshold." />
        <SummaryCard label="Degraded" :value="monitor.liveSymbols.summary.degraded_symbols" detail="Symbols with provider or connection issues." />
        <SummaryCard label="GC removals" :value="monitor.liveSymbols.summary.gc_removals" detail="Evictions performed by the idle garbage collector." />
      </div>

      <section class="panel page-panel stack">
        <div class="page-header compact-header">
          <div>
            <p class="eyebrow">Cache Table</p>
            <h3>Symbol State</h3>
          </div>
          <FreshnessLabel :value="freshestLiveUpdate" />
        </div>
        <DenseTable v-if="monitor.liveSymbols.items.length">
          <table>
            <thead>
              <tr>
                <th>Symbol</th>
                <th>State</th>
                <th>Price</th>
                <th>Provider</th>
                <th>Updated</th>
                <th>Subscribers</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in monitor.liveSymbols.items" :key="item.symbol">
                <td class="code">{{ item.symbol }}</td>
                <td><StatusBadge :label="item.state" /></td>
                <td class="metric">{{ formatCurrency(item.price) }}</td>
                <td>{{ item.provider }}</td>
                <td>
                  <div>{{ formatDateTime(item.last_updated) }}</div>
                  <div class="muted"><FreshnessLabel :value="item.last_updated" /></div>
                </td>
                <td class="metric">{{ formatNumber(item.subscriber_count, 0) }}</td>
              </tr>
            </tbody>
          </table>
        </DenseTable>
        <EmptyState v-else title="No symbols cached" description="No live market lookups have populated the cache yet." />
      </section>
    </template>

    <EmptyState v-else title="No live symbol data yet" description="Refresh the page after live accounts or live market reads populate the cache." />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import FreshnessLabel from '@/components/common/FreshnessLabel.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useFallbackPolling } from '@/composables/useFallbackPolling'
import { useMonitorStore } from '@/stores/monitor'
import { formatCurrency, formatDateTime, formatNumber } from '@/utils/formatters'

const monitor = useMonitorStore()
const loading = ref(false)
const errorMessage = ref('')

const freshestLiveUpdate = computed(() => monitor.liveSymbols?.items[0]?.last_updated ?? null)

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    await monitor.loadLiveSymbols()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load live symbol cache.'
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
</style>
