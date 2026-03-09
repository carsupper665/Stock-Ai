<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Sandbox Monitor Room</p>
        <h2>{{ snapshot?.sandbox.name ?? route.params.id }}</h2>
        <p>Realtime room focused on a single sandbox with replay status, rankings, activity, and event feed.</p>
      </div>
      <div class="actions">
        <FreshnessLabel :value="snapshot?.freshness_at" />
        <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh room' }}</button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="Sandbox room unavailable" :description="errorMessage" retry-label="Retry" @retry="refresh" />
    <EmptyState v-else-if="!snapshot && !loading" title="No sandbox snapshot" description="This sandbox has not returned monitor data yet." />

    <template v-else-if="snapshot">
      <div class="kpi-grid">
        <SummaryCard label="Status" :value="snapshot.sandbox.status" detail="Current replay lifecycle status." />
        <SummaryCard label="Replay speed" :value="`${snapshot.replay_control?.speed ?? snapshot.sandbox.replay_speed}x`" detail="Current replay speed applied by the sandbox service." />
        <SummaryCard label="Trades" :value="snapshot.trades.length" detail="Recent executed trades for this sandbox." />
        <SummaryCard label="Accounts" :value="snapshot.accounts.length" detail="Accounts participating in this sandbox." />
      </div>

      <div class="two-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Replay Timeline</p>
            <h3>Current Position</h3>
          </div>
          <ReplayTimelineScrubber
            readonly
            :current-time="snapshot.replay_control?.current_time ?? snapshot.sandbox.replay_current_time"
            :min-time="snapshot.dataset?.start_at ?? null"
            :max-time="snapshot.dataset?.end_at ?? null"
          >
            <template #status>
              <StatusBadge :label="snapshot.sandbox.status" />
            </template>
          </ReplayTimelineScrubber>
          <article class="panel dataset-summary" v-if="snapshot.dataset">
            <strong>{{ snapshot.dataset.name }}</strong>
            <p class="muted">{{ snapshot.dataset.symbol }} · {{ snapshot.dataset.interval }}</p>
            <p class="muted">{{ formatDateTime(snapshot.dataset.start_at) }} to {{ formatDateTime(snapshot.dataset.end_at) }}</p>
          </article>
          <AccountEquityChart :series="equitySeries" />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Account Ranking</p>
            <h3>Performance Board</h3>
          </div>
          <DenseTable v-if="rankedAccounts.length">
            <table>
              <thead>
                <tr>
                  <th>Account</th>
                  <th>Equity</th>
                  <th>Realized</th>
                  <th>Unrealized</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="account in rankedAccounts" :key="account.id">
                  <td>
                    <strong>{{ account.name }}</strong>
                    <div class="muted code">{{ account.id }}</div>
                  </td>
                  <td class="metric">{{ formatCurrency(account.equity, account.base_currency) }}</td>
                  <td class="metric">{{ formatCurrency(account.realized_pnl, account.base_currency) }}</td>
                  <td class="metric">{{ formatCurrency(account.unrealized_pnl, account.base_currency) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="No accounts" description="Ranking will appear once the sandbox has accounts." />
        </section>
      </div>

      <div class="three-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Open Positions</p>
            <h3>Exposure</h3>
          </div>
          <DenseTable v-if="snapshot.positions.length">
            <table>
              <thead>
                <tr>
                  <th>Symbol</th>
                  <th>Side</th>
                  <th>Qty</th>
                  <th>PnL</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="position in snapshot.positions.slice(0, 10)" :key="position.id">
                  <td>{{ position.symbol }}</td>
                  <td>{{ position.position_side }}</td>
                  <td class="metric">{{ formatNumber(position.qty) }}</td>
                  <td class="metric">{{ formatCurrency(position.unrealized_pnl) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="No positions" description="Open positions stream into this room after fills occur." />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Orders & Trades</p>
            <h3>Latest Activity</h3>
          </div>
          <DenseTable v-if="snapshot.trades.length || snapshot.orders.length">
            <table>
              <thead>
                <tr>
                  <th>Type</th>
                  <th>Symbol</th>
                  <th>Status</th>
                  <th>Time</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="trade in snapshot.trades.slice(0, 6)" :key="trade.id">
                  <td>trade</td>
                  <td>{{ trade.symbol }}</td>
                  <td>{{ trade.side }} / {{ trade.position_side }}</td>
                  <td class="metric">{{ formatDateTime(trade.executed_at) }}</td>
                </tr>
                <tr v-for="order in snapshot.orders.slice(0, 6)" :key="order.id">
                  <td>order</td>
                  <td>{{ order.symbol }}</td>
                  <td>{{ order.status }}</td>
                  <td class="metric">{{ formatDateTime(order.updated_at) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="No recent activity" description="Orders and trades will populate after agent execution begins." />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Event Feed</p>
            <h3>Realtime Events</h3>
          </div>
          <div v-if="sandboxEvents.length" class="stack">
            <EventFeedItem v-for="event in sandboxEvents.slice(0, 14)" :key="`${event.topic}-${event.created_at}`" :event="event" />
          </div>
          <EmptyState v-else title="No events yet" description="Domain events from the websocket will appear here." />
        </section>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AccountEquityChart from '@/components/charts/AccountEquityChart.vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import EventFeedItem from '@/components/common/EventFeedItem.vue'
import FreshnessLabel from '@/components/common/FreshnessLabel.vue'
import ReplayTimelineScrubber from '@/components/common/ReplayTimelineScrubber.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useFallbackPolling } from '@/composables/useFallbackPolling'
import { useMonitorStore } from '@/stores/monitor'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatCurrency, formatDateTime, formatNumber } from '@/utils/formatters'

const route = useRoute()
const monitor = useMonitorStore()
const sandboxes = useSandboxesStore()
const loading = ref(false)
const errorMessage = ref('')
let stopSandboxSocket: null | (() => void) = null

const sandboxId = computed(() => String(route.params.id))
const snapshot = computed(() => monitor.sandboxSnapshots[sandboxId.value] ?? sandboxes.byId[sandboxId.value])
const sandboxEvents = computed(() => monitor.events.filter((event) => event.sandbox_id === sandboxId.value))
const rankedAccounts = computed(() => [...(snapshot.value?.accounts ?? [])].sort((left, right) => right.equity - left.equity))
const equitySeries = computed(() => rankedAccounts.value.slice(0, 5).map((account) => ({
  name: account.name,
  points: monitor.accountSeries[account.id] ?? [{ timestamp: snapshot.value?.freshness_at ?? new Date().toISOString(), value: account.equity }],
})))

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    const nextSnapshot = await sandboxes.loadSnapshot(sandboxId.value)
    monitor.ingestSandboxSnapshot(nextSnapshot)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load sandbox monitor room.'
  } finally {
    loading.value = false
  }
}

function attachSocket() {
  stopSandboxSocket?.()
  stopSandboxSocket = monitor.connectSandbox(sandboxId.value, (nextSnapshot) => {
    sandboxes.byId[sandboxId.value] = nextSnapshot
    monitor.ingestSandboxSnapshot(nextSnapshot)
  })
}

useFallbackPolling(() => monitor.connectionState === 'disconnected', async () => {
  await refresh()
})

watch(sandboxId, async () => {
  await refresh()
  attachSocket()
})

onMounted(async () => {
  await refresh()
  attachSocket()
})

onBeforeUnmount(() => {
  stopSandboxSocket?.()
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
.dataset-summary {
  padding: 1rem;
}
</style>
