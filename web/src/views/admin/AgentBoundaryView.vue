<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Agent 沙盒邊界</p>
        <h2>代理執行防護</h2>
        <p>檢查沙盒 runtime、代理 token 能力範圍，以及 candle 查詢必須遵守的 sandbox time 上限。</p>
      </div>
      <div class="actions">
        <select v-model="selectedSandboxId" class="control compact-control">
          <option value="">選擇沙盒</option>
          <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">
            {{ sandbox.name }} · {{ sandbox.status }}
          </option>
        </select>
        <button class="btn ghost" :disabled="loading" @click="refresh">
          {{ loading ? '重新整理中…' : '重新整理帳戶' }}
        </button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="Agent 邊界資料不可用" :description="errorMessage" retry-label="重試" @retry="refresh" />

    <div class="kpi-grid">
      <SummaryCard label="Running runtime" :value="runningSandboxes" detail="會由後端 SandboxRuntimeManager 自動 tick 的沙盒。" />
      <SummaryCard label="Stopped 沙盒" :value="stoppedSandboxes" detail="stopped 沙盒不可重新啟動，也不能 resume/pause。" />
      <SummaryCard label="已載入 Agent 帳戶" :value="currentAccounts.length" detail="目前選取沙盒中的虛擬代理帳戶。" />
      <SummaryCard label="工作階段權杖" :value="knownTokenCount" detail="此瀏覽器工作階段已知的 token id，不代表可再讀取 secret。" />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div class="page-header compact-header">
          <div>
            <p class="eyebrow">Runtime 狀態</p>
            <h3>{{ selectedSandbox?.name ?? '尚未選擇沙盒' }}</h3>
          </div>
          <StatusBadge v-if="selectedSandbox" :label="selectedSandbox.status" />
        </div>

        <template v-if="selectedSandbox">
          <dl class="runtime-grid">
            <div>
              <dt>Replay current_time</dt>
              <dd class="code">{{ selectedSandbox.replay_current_time }}</dd>
            </div>
            <div>
              <dt>Replay speed</dt>
              <dd class="metric">{{ selectedSandbox.replay_speed }}x</dd>
            </div>
            <div>
              <dt>Runtime anchor</dt>
              <dd class="code">{{ selectedSandbox.runtime_anchor_at ?? '未啟動' }}</dd>
            </div>
          </dl>
          <ul class="notes-list">
            <li>running 沙盒會自動 tick，後端會推進 replay_current_time 並處理掛單成交與帳戶重估。</li>
            <li>paused 沙盒可 seek，但 replay cursor 不能往回移動。</li>
            <li>stopped 沙盒不可重新啟動；介面應避免引導使用者再 start、resume 或 pause。</li>
          </ul>
        </template>
        <EmptyState v-else title="尚未選擇沙盒" description="請選擇沙盒以檢查 runtime 與代理帳戶邊界。" />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Future candle policy</p>
          <h3>市場資料時間上限</h3>
        </div>
        <p class="muted">
          Agent 在呼叫 GET /market/klines 前必須先讀 GET /sandbox/time，並以 to &lt;= sandbox current_time 作為硬上限。
        </p>
        <div class="policy-callout">
          <strong>to &lt;= sandbox current_time</strong>
          <span class="muted">若資料不可見，代理應停止並回報 fixture 不足，不應 seek、改速或查 future candle。</span>
        </div>
        <ul class="notes-list">
          <li>即時帳戶可以讀 live market，但 live account trading 目前不啟用。</li>
          <li>代理 token 是 account-scoped，不是 admin session。</li>
          <li>禁止 /admin/*、replay seek/speed、market price mutation 與 direct DB mutation。</li>
        </ul>
      </section>
    </div>

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">允許清單</p>
        <h3>Agent HTTP 能力邊界</h3>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>方法</th>
              <th>Endpoint</th>
              <th>用途</th>
              <th>限制</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in allowedEndpoints" :key="`${item.method}-${item.path}`">
              <td class="code">{{ item.method }}</td>
              <td class="code">{{ item.path }}</td>
              <td>{{ item.purpose }}</td>
              <td class="muted">{{ item.constraint }}</td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">禁止清單</p>
        <h3>Agent 不可跨越的管理面</h3>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>禁止項目</th>
              <th>原因</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in forbiddenActions" :key="item.target">
              <td class="code">{{ item.target }}</td>
              <td>{{ item.reason }}</td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>

    <section class="panel page-panel stack">
      <div>
        <p class="eyebrow">選取沙盒帳戶</p>
        <h3>{{ selectedSandbox?.name ?? '未選擇' }} 的 Agent 帳戶</h3>
      </div>
      <DenseTable v-if="currentAccounts.length">
        <table>
          <thead>
            <tr>
              <th>帳戶</th>
              <th>狀態</th>
              <th>權杖</th>
              <th>邊界提示</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="account in currentAccounts" :key="account.id">
              <td>
                <strong>{{ account.name }}</strong>
                <div class="muted code">{{ account.id }}</div>
              </td>
              <td><StatusBadge :label="account.status" /></td>
              <td>
                <span v-if="accounts.knownTokens[account.id]?.length" class="muted">
                  {{ accounts.knownTokens[account.id].length }} 個工作階段已知 token id
                </span>
                <span v-else class="muted">尚無工作階段已知 token id</span>
              </td>
              <td>交易前先讀 GET /sandbox/time，再用 current_time 限制 candle 視窗。</td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
      <EmptyState v-else title="尚無 Agent 帳戶" description="到帳戶與權杖頁建立沙盒虛擬帳戶並發行代理 token。" />
    </section>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useAccountsStore } from '@/stores/accounts'
import { useSandboxesStore } from '@/stores/sandboxes'

const sandboxes = useSandboxesStore()
const accounts = useAccountsStore()

const loading = ref(false)
const errorMessage = ref('')
const selectedSandboxId = ref('')

const allowedEndpoints = [
  { method: 'GET', path: '/sandbox/time', purpose: '讀取沙盒時間', constraint: 'candle 查詢前必須先讀取' },
  { method: 'GET', path: '/market/price', purpose: '讀取沙盒價格', constraint: 'account-scoped market:read' },
  { method: 'GET', path: '/market/ticker', purpose: '讀取 ticker', constraint: 'account-scoped market:read' },
  { method: 'GET', path: '/market/klines', purpose: '讀取 candle', constraint: 'to <= sandbox current_time' },
  { method: 'GET', path: '/account', purpose: '讀取帳戶摘要', constraint: 'account-scoped account:read' },
  { method: 'GET', path: '/account/performance', purpose: '讀取績效', constraint: 'account-scoped account:read' },
  { method: 'GET', path: '/orders', purpose: '列出訂單', constraint: 'account-scoped trade:read' },
  { method: 'GET', path: '/positions', purpose: '列出持倉', constraint: 'account-scoped trade:read' },
  { method: 'GET', path: '/trades', purpose: '列出成交', constraint: 'account-scoped trade:read' },
  { method: 'POST', path: '/orders', purpose: '建立訂單', constraint: '僅虛擬 sandbox account 支援' },
  { method: 'POST', path: '/orders/:id/cancel', purpose: '取消訂單', constraint: 'account-scoped trade:write' },
]

const forbiddenActions = [
  { target: '/admin/*', reason: '禁止 /admin/*，代理 token 不能使用管理面 API。' },
  { target: '/admin/sandboxes/:id/replay/seek', reason: 'replay cursor control 只能由管理員操作。' },
  { target: '/admin/sandboxes/:id/replay/speed', reason: 'replay speed control 只能由管理員操作。' },
  { target: 'future candle request', reason: '不得查詢 sandbox current_time 之後的市場資料。' },
  { target: 'market price mutation', reason: '代理只能讀市場資料與下單，不能變造價格。' },
  { target: 'direct database mutation', reason: '代理必須走 HTTP API，不能直接寫資料庫。' },
]

const selectedSandbox = computed(() => sandboxes.items.find((item) => item.id === selectedSandboxId.value))
const currentAccounts = computed(() => accounts.sandboxAccounts[selectedSandboxId.value] ?? [])
const runningSandboxes = computed(() => sandboxes.items.filter((sandbox) => sandbox.status === 'running').length)
const stoppedSandboxes = computed(() => sandboxes.items.filter((sandbox) => sandbox.status === 'stopped').length)
const knownTokenCount = computed(() => currentAccounts.value.reduce((sum, account) => sum + (accounts.knownTokens[account.id]?.length ?? 0), 0))

async function refresh() {
  if (!selectedSandboxId.value) {
    return
  }
  loading.value = true
  errorMessage.value = ''
  try {
    await accounts.loadSandboxAccounts(selectedSandboxId.value)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法載入沙盒代理帳戶。'
  } finally {
    loading.value = false
  }
}

watch(selectedSandboxId, () => {
  void refresh()
})

onMounted(async () => {
  try {
    await sandboxes.loadList()
    selectedSandboxId.value = sandboxes.items.find((sandbox) => sandbox.status === 'running')?.id ?? sandboxes.items[0]?.id ?? ''
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法載入沙盒清單。'
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

.compact-header {
  align-items: center;
}

.compact-control {
  min-width: 280px;
}

.runtime-grid {
  margin: 0;
  display: grid;
  gap: 1rem;
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.runtime-grid div {
  display: grid;
  gap: 0.35rem;
}

.runtime-grid dt {
  color: var(--muted);
  font-size: 0.82rem;
}

.runtime-grid dd {
  margin: 0;
}

.notes-list {
  margin: 0;
  padding-left: 1.1rem;
  display: grid;
  gap: 0.75rem;
  color: var(--muted);
}

.policy-callout {
  display: grid;
  gap: 0.45rem;
  padding: 1rem;
  border: 1px solid rgba(251, 191, 36, 0.24);
  border-radius: var(--radius-sm);
  background: rgba(251, 191, 36, 0.08);
}

@media (max-width: 900px) {
  .runtime-grid {
    grid-template-columns: 1fr;
  }
}
</style>
