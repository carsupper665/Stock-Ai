<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Orders & Trades Stream</p>
        <h2>Activity Feed</h2>
        <p>Global operations stream aggregated from loaded snapshots plus realtime websocket events.</p>
      </div>
      <div class="actions filters">
        <select v-model="sandboxFilter" class="control compact-control">
          <option value="">All sandboxes</option>
          <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">{{ sandbox.name }}</option>
        </select>
        <input v-model="symbolFilter" class="control compact-control" placeholder="Filter by symbol" />
        <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh activity' }}</button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="Activity feed unavailable" :description="errorMessage" retry-label="Retry" @retry="refresh" />

    <div class="three-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Trades</p>
          <h3>Executed Stream</h3>
        </div>
        <DenseTable v-if="filteredTrades.length">
          <table>
            <thead>
              <tr>
                <th>Sandbox</th>
                <th>Symbol</th>
                <th>Side</th>
                <th>Price</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="trade in filteredTrades.slice(0, 16)" :key="trade.id">
                <td>{{ sandboxName(trade.sandbox_id) }}</td>
                <td>{{ trade.symbol }}</td>
                <td>{{ trade.side }} / {{ trade.position_side }}</td>
                <td class="metric">{{ formatCurrency(trade.price) }}</td>
              </tr>
            </tbody>
          </table>
        </DenseTable>
        <EmptyState v-else title="No trades" description="Executed trades matching the current filters will appear here." />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Orders</p>
          <h3>Order Stream</h3>
        </div>
        <DenseTable v-if="filteredOrders.length">
          <table>
            <thead>
              <tr>
                <th>Sandbox</th>
                <th>Symbol</th>
                <th>Status</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="order in filteredOrders.slice(0, 16)" :key="order.id">
                <td>{{ sandboxName(order.sandbox_id) }}</td>
                <td>{{ order.symbol }}</td>
                <td>{{ order.status }}</td>
                <td class="metric">{{ formatDateTime(order.updated_at) }}</td>
              </tr>
            </tbody>
          </table>
        </DenseTable>
        <EmptyState v-else title="No orders" description="Order updates matching the current filters will appear here." />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Events</p>
          <h3>Realtime Feed</h3>
        </div>
        <div v-if="filteredEvents.length" class="stack">
          <EventFeedItem v-for="event in filteredEvents.slice(0, 14)" :key="`${event.topic}-${event.created_at}`" :event="event" />
        </div>
        <EmptyState v-else title="No events" description="Websocket events matching the active filters will appear here." />
      </section>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import EventFeedItem from '@/components/common/EventFeedItem.vue'
import { useFallbackPolling } from '@/composables/useFallbackPolling'
import { useMonitorStore } from '@/stores/monitor'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatCurrency, formatDateTime } from '@/utils/formatters'

const sandboxes = useSandboxesStore()
const monitor = useMonitorStore()
const loading = ref(false)
const errorMessage = ref('')
const sandboxFilter = ref('')
const symbolFilter = ref('')

const orders = computed(() => Object.values(sandboxes.byId).flatMap((snapshot) => snapshot.orders))
const trades = computed(() => Object.values(sandboxes.byId).flatMap((snapshot) => snapshot.trades))
const filteredOrders = computed(() => orders.value.filter((order) => matches(order.sandbox_id, order.symbol)))
const filteredTrades = computed(() => trades.value.filter((trade) => matches(trade.sandbox_id, trade.symbol)))
const filteredEvents = computed(() => monitor.events.filter((event) => matches(event.sandbox_id, String(event.payload?.symbol ?? ''))))

function matches(sandboxId?: string, symbol?: string) {
  const sandboxOk = !sandboxFilter.value || sandboxFilter.value === sandboxId
  const symbolOk = !symbolFilter.value || (symbol ?? '').toLowerCase().includes(symbolFilter.value.trim().toLowerCase())
  return sandboxOk && symbolOk
}

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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to refresh activity feed.'
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
.filters {
  align-items: center;
}
.compact-control {
  min-width: 180px;
}
</style>
