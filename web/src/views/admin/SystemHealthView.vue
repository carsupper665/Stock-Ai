<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">系統健康</p>
        <h2>即時交易對快取</h2>
        <p>檢查 P1 即時市場快取暴露的供應商新鮮度、訂閱者數量，以及降級/健康的交易對狀態。</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? '重新整理中…' : '重新整理快取' }}</button>
    </header>

    <ErrorState v-if="errorMessage" title="系統健康不可用" :description="errorMessage" retry-label="重試" @retry="refresh" />

    <template v-else-if="monitor.liveSymbols">
      <div class="kpi-grid">
        <SummaryCard label="作用中交易對" :value="monitor.liveSymbols.summary.active_symbols" detail="目前在記憶體中追蹤的交易對。" />
        <SummaryCard label="過期" :value="monitor.liveSymbols.summary.stale_symbols" detail="新鮮度已低於健康門檻的交易對。" />
        <SummaryCard label="降級" :value="monitor.liveSymbols.summary.degraded_symbols" detail="有供應商或連線問題的交易對。" />
        <SummaryCard label="GC 移除" :value="monitor.liveSymbols.summary.gc_removals" detail="閒置垃圾回收器執行的移除次數。" />
      </div>

      <section class="panel page-panel stack">
        <div class="page-header compact-header">
          <div>
            <p class="eyebrow">快取表格</p>
            <h3>交易對狀態</h3>
          </div>
          <FreshnessLabel :value="freshestLiveUpdate" />
        </div>
        <DenseTable v-if="monitor.liveSymbols.items.length">
          <table>
            <thead>
              <tr>
                <th>交易對</th>
                <th>狀態</th>
                <th>價格</th>
                <th>供應商</th>
                <th>更新時間</th>
                <th>訂閱者</th>
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
        <EmptyState v-else title="尚無快取交易對" description="尚未有即時市場查詢填入快取。" />
      </section>
    </template>

    <EmptyState v-else title="尚無即時交易對資料" description="即時帳戶或即時市場讀取填入快取後，再重新整理頁面。" />
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
    errorMessage.value = error instanceof Error ? error.message : '無法載入即時交易對快取。'
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
