<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">績效看板</p>
        <h2>帳戶績效</h2>
        <p>以已載入快照與 websocket 更新建立的工作階段績效視圖，不依賴歷史管理 API。</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? '重新整理中…' : '重新整理看板' }}</button>
    </header>

    <ErrorState v-if="errorMessage" title="績效看板不可用" :description="errorMessage" retry-label="重試" @retry="refresh" />

    <div class="kpi-grid">
      <SummaryCard label="追蹤帳戶" :value="topAccounts.length" detail="目前已載入快照中的帳戶。" />
      <SummaryCard label="最高權益" :value="formatCurrency(topAccounts[0]?.equity ?? 0)" detail="已載入帳戶中的最高權益。" />
      <SummaryCard label="正損益" :value="positiveAccounts" detail="已實現加未實現損益為正的帳戶。" />
      <SummaryCard label="序列點數" :value="seriesPoints" detail="此工作階段累積的前端時間序列點數。" />
    </div>

    <AccountEquityChart :series="chartSeries" />

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">排名</p>
        <h3>目前名次</h3>
      </div>
      <DenseTable v-if="topAccounts.length">
        <table>
          <thead>
            <tr>
              <th>帳戶</th>
              <th>沙盒</th>
              <th>權益</th>
              <th>已實現</th>
              <th>未實現</th>
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
      <EmptyState v-else title="尚無績效資料" description="載入沙盒快照後，會開始建立本工作階段的權益歷史。" />
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
    errorMessage.value = error instanceof Error ? error.message : '無法重新整理績效看板。'
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
