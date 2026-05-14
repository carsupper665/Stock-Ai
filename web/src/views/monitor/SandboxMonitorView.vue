<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">沙盒監控室</p>
        <h2>{{ snapshot?.sandbox.name ?? route.params.id }}</h2>
        <p>聚焦單一沙盒的即時監控室，包含回放狀態、排名、活動與事件串流。</p>
      </div>
      <div class="actions">
        <FreshnessLabel :value="snapshot?.freshness_at" />
        <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? '重新整理中…' : '重新整理監控室' }}</button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="沙盒監控室不可用" :description="errorMessage" retry-label="重試" @retry="refresh" />
    <EmptyState v-else-if="!snapshot && !loading" title="尚無沙盒快照" description="此沙盒尚未回傳監控資料。" />

    <template v-else-if="snapshot">
      <div class="kpi-grid">
        <SummaryCard label="狀態" :value="formatStatusLabel(snapshot.sandbox.status)" detail="目前回放生命週期狀態。" />
        <SummaryCard label="回放速度" :value="`${snapshot.replay_control?.speed ?? snapshot.sandbox.replay_speed}x`" detail="沙盒服務目前套用的回放速度。" />
        <SummaryCard label="交易" :value="snapshot.trades.length" detail="此沙盒近期成交交易。" />
        <SummaryCard label="帳戶" :value="snapshot.accounts.length" detail="參與此沙盒的帳戶。" />
      </div>

      <div class="two-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">回放時間軸</p>
            <h3>目前位置</h3>
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
            <p class="muted">{{ formatDateTime(snapshot.dataset.start_at) }} 至 {{ formatDateTime(snapshot.dataset.end_at) }}</p>
          </article>
          <AccountEquityChart :series="equitySeries" />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">帳戶排名</p>
            <h3>績效看板</h3>
          </div>
          <DenseTable v-if="rankedAccounts.length">
            <table>
              <thead>
                <tr>
                  <th>帳戶</th>
                  <th>權益</th>
                  <th>已實現</th>
                  <th>未實現</th>
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
          <EmptyState v-else title="尚無帳戶" description="沙盒建立帳戶後，排名會顯示在這裡。" />
        </section>
      </div>

      <div class="three-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">未平倉持倉</p>
            <h3>曝險</h3>
          </div>
          <DenseTable v-if="snapshot.positions.length">
            <table>
              <thead>
                <tr>
                  <th>交易對</th>
                  <th>方向</th>
                  <th>數量</th>
                  <th>損益</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="position in snapshot.positions.slice(0, 10)" :key="position.id">
                  <td>{{ position.symbol }}</td>
                  <td>{{ formatSideLabel(position.position_side) }}</td>
                  <td class="metric">{{ formatNumber(position.qty) }}</td>
                  <td class="metric">{{ formatCurrency(position.unrealized_pnl) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="尚無持倉" description="成交後未平倉持倉會串流到此監控室。" />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">訂單與交易</p>
            <h3>最新活動</h3>
          </div>
          <DenseTable v-if="snapshot.trades.length || snapshot.orders.length">
            <table>
              <thead>
                <tr>
                  <th>類型</th>
                  <th>交易對</th>
                  <th>狀態</th>
                  <th>時間</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="trade in snapshot.trades.slice(0, 6)" :key="trade.id">
                  <td>交易</td>
                  <td>{{ trade.symbol }}</td>
                  <td>{{ formatSideLabel(trade.side) }} / {{ formatSideLabel(trade.position_side) }}</td>
                  <td class="metric">{{ formatDateTime(trade.executed_at) }}</td>
                </tr>
                <tr v-for="order in snapshot.orders.slice(0, 6)" :key="order.id">
                  <td>訂單</td>
                  <td>{{ order.symbol }}</td>
                  <td>{{ formatStatusLabel(order.status) }}</td>
                  <td class="metric">{{ formatDateTime(order.updated_at) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="尚無近期活動" description="代理開始執行後，訂單與交易會出現在這裡。" />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">事件串流</p>
            <h3>即時事件</h3>
          </div>
          <div v-if="sandboxEvents.length" class="stack">
            <EventFeedItem v-for="event in sandboxEvents.slice(0, 14)" :key="`${event.topic}-${event.created_at}`" :event="event" />
          </div>
          <EmptyState v-else title="尚無事件" description="websocket 的領域事件會顯示在這裡。" />
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
import { formatSideLabel, formatStatusLabel } from '@/utils/localization'

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
    errorMessage.value = error instanceof Error ? error.message : '無法載入沙盒監控室。'
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
