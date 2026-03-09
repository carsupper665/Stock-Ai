<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Performance Board</p>
        <h2>Account Performance</h2>
        <p>Session-scoped performance view built from loaded snapshots and websocket updates, without relying on a historical admin API.</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh board' }}</button>
    </header>

    <ErrorState v-if="errorMessage" title="Performance board unavailable" :description="errorMessage" retry-label="Retry" @retry="refresh" />

    <div class="kpi-grid">
      <SummaryCard label="Tracked accounts" :value="topAccounts.length" detail="Accounts currently represented in loaded snapshots." />
      <SummaryCard label="Best equity" :value="formatCurrency(topAccounts[0]?.equity ?? 0)" detail="Highest equity among loaded accounts." />
      <SummaryCard label="Positive PnL" :value="positiveAccounts" detail="Accounts with positive realized plus unrealized PnL." />
      <SummaryCard label="Series points" :value="seriesPoints" detail="Client-side time series points accumulated during this session." />
    </div>

    <AccountEquityChart :series="chartSeries" />

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">Ranking</p>
        <h3>Current Standings</h3>
      </div>
      <DenseTable v-if="topAccounts.length">
        <table>
          <thead>
            <tr>
              <th>Account</th>
              <th>Sandbox</th>
              <th>Equity</th>
              <th>Realized</th>
              <th>Unrealized</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="account in topAccounts" :key="account.id">
              <td>
                <strong>{{ account.name }}</strong>
                <div class="muted code">{{ account.id }}</div>
              </td>
              <td>{{ sandboxName(account.sandbox_id) }}</td>
              <td class="metric">{{ formatCurrency(account.equity, account.base_currency) }}</td>
              <td class="metric">{{ formatCurrency(account.realized_pnl, account.base_currency) }}</td>
              <td class="metric">{{ formatCurrency(account.unrealized_pnl, account.base_currency) }}</td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
      <EmptyState v-else title="No performance data" description="Load sandbox snapshots to start building session-local equity history." />
    </section>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AccountEquityChart from '@/components/charts/AccountEquityChart.vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useFallbackPolling } from '@/composables/useFallbackPolling'
import { useMonitorStore } from '@/stores/monitor'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatCurrency } from '@/utils/formatters'

const sandboxes = useSandboxesStore()
const monitor = useMonitorStore()
const loading = ref(false)
const errorMessage = ref('')

const topAccounts = computed(() => Object.values(sandboxes.byId).flatMap((snapshot) => snapshot.accounts).sort((left, right) => right.equity - left.equity))
const positiveAccounts = computed(() => topAccounts.value.filter((account) => account.realized_pnl + account.unrealized_pnl > 0).length)
const seriesPoints = computed(() => topAccounts.value.reduce((sum, account) => sum + (monitor.accountSeries[account.id]?.length ?? 0), 0))
const chartSeries = computed(() => topAccounts.value.slice(0, 6).map((account) => ({
  name: account.name,
  points: monitor.accountSeries[account.id] ?? [],
})))

function sandboxName(sandboxId?: string | null) {
  return sandboxes.items.find((item) => item.id === sandboxId)?.name ?? sandboxId ?? '--'
}

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    await sandboxes.loadList()
    await Promise.all(sandboxes.items.map(async (sandbox) => {
      const snapshot = await sandboxes.loadSnapshot(sandbox.id)
      monitor.ingestSandboxSnapshot(snapshot)
    }))
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to refresh performance board.'
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
</style>
