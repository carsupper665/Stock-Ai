<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">監控工作區</p>
        <h2>全域總覽</h2>
        <p>桌面優先的控制室，顯示目前沙盒狀態、即時快取健康度與最新告警串流。</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? '重新整理中…' : '重新整理總覽' }}</button>
    </header>

    <ErrorState v-if="errorMessage" title="總覽不可用" :description="errorMessage" retry-label="重試" @retry="refresh" />

    <div class="kpi-grid">
      <SummaryCard label="沙盒" :value="sandboxes.items.length" detail="目前可監控的回放工作區。" />
      <SummaryCard label="運行中" :value="runningCount" detail="正在推進回放時間的沙盒。" />
      <SummaryCard label="總權益" :value="formatCurrency(totalEquity)" detail="所有已載入沙盒帳戶的權益總和。" />
      <SummaryCard label="未處理告警" :value="monitor.alerts.length" detail="由管理監控 websocket 產生的告警項目。" />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">沙盒群組</p>
          <h3>目前狀態</h3>
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
                <span class="muted">回放時間</span>
                <strong>{{ formatDateTime(snapshot.replay_control?.current_time ?? snapshot.sandbox.replay_current_time) }}</strong>
              </div>
              <div>
                <span class="muted">帳戶</span>
                <strong>{{ snapshot.accounts.length }}</strong>
              </div>
              <div>
                <span class="muted">持倉</span>
                <strong>{{ snapshot.positions.length }}</strong>
              </div>
              <div>
                <span class="muted">新鮮度</span>
                <strong><FreshnessLabel :value="snapshot.freshness_at" /></strong>
              </div>
            </div>
          </RouterLink>
        </div>
        <EmptyState v-else title="尚無沙盒快照" description="後端運行且管理員登入有效後，請重新整理。" />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">即時快取</p>
          <h3>交易對健康</h3>
        </div>
        <div class="three-column compact-grid" v-if="monitor.liveSymbols">
          <SummaryCard label="作用中" :value="monitor.liveSymbols.summary.active_symbols" detail="目前在記憶體中的交易對。" />
          <SummaryCard label="過期" :value="monitor.liveSymbols.summary.stale_symbols" detail="近期未更新的交易對。" />
          <SummaryCard label="降級" :value="monitor.liveSymbols.summary.degraded_symbols" detail="有連線或供應商問題的交易對。" />
        </div>
        <DenseTable v-if="monitor.liveSymbols?.items.length">
          <table>
            <thead>
              <tr>
                <th>交易對</th>
                <th>狀態</th>
                <th>價格</th>
                <th>更新</th>
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
        <EmptyState v-else title="尚無即時快取資料" description="即時市場讀取填入供應商快取後，交易對活動會顯示在這裡。" />
      </section>
    </div>

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">風險串流</p>
        <h3>近期告警</h3>
      </div>
      <div v-if="monitor.alerts.length" class="stack">
        <article v-for="alert in monitor.alerts.slice(0, 12)" :key="alert.id" class="panel alert-card" :class="alert.severity">
          <div class="page-header compact-header">
            <div>
              <strong>{{ alert.title }}</strong>
              <p class="muted">{{ formatEventTopic(alert.topic) }} · {{ formatDateTime(alert.created_at) }}</p>
            </div>
            <StatusBadge :label="alert.severity" />
          </div>
          <p class="muted">{{ alert.message }}</p>
        </article>
      </div>
      <EmptyState v-else title="尚無告警" description="由 websocket 產生的風險與系統告警會累積在這裡。" />
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
import { formatEventTopic } from '@/utils/localization'

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
    errorMessage.value = error instanceof Error ? error.message : '無法重新整理監控總覽。'
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
