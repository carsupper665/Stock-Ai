<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">沙盒詳情</p>
        <h2>{{ snapshot?.sandbox.name ?? route.params.id }}</h2>
        <p>集中管理回放 metadata、資料集綁定、帳戶、快照與事件回饋。</p>
      </div>
      <div class="actions">
        <FreshnessLabel :value="snapshot?.freshness_at" />
        <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? '重新整理中…' : '重新整理快照' }}</button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="沙盒詳情載入失敗" :description="errorMessage" retry-label="重試" @retry="refresh" />
    <EmptyState v-else-if="!snapshot && !loading" title="沙盒快照不可用" description="無法從後端載入選取的沙盒。" />

    <template v-else-if="snapshot">
      <div class="kpi-grid">
        <SummaryCard label="帳戶" :value="snapshot.accounts.length" detail="此沙盒中已配置的虛擬帳戶。" />
        <SummaryCard label="未平倉持倉" :value="snapshot.positions.length" detail="所有沙盒帳戶目前的未平倉持倉。" />
        <SummaryCard label="訂單" :value="snapshot.orders.length" detail="此沙盒最新的訂單紀錄。" />
        <SummaryCard label="交易" :value="snapshot.trades.length" detail="用於監控與稽核的近期成交交易。" />
      </div>

      <div class="two-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Metadata + 回放控制</p>
            <h3>主要控制介面</h3>
          </div>
          <form class="form-grid" @submit.prevent="saveMetadata">
            <label>
              沙盒名稱
              <input v-model="metadataForm.name" class="control" />
            </label>
            <label>
              資料集綁定
              <select v-model="metadataForm.dataset_id" class="control">
                <option value="">選擇資料集</option>
                <option v-for="dataset in datasets.items" :key="dataset.id" :value="dataset.id">
                  {{ dataset.name }} · {{ dataset.symbol }}
                </option>
              </select>
            </label>
            <div class="form-actions">
              <button class="btn primary" :disabled="savingMetadata">{{ savingMetadata ? '儲存中…' : '儲存 metadata' }}</button>
              <StatusBadge :label="snapshot.sandbox.status" />
              <span class="muted code">{{ snapshot.sandbox.id }}</span>
            </div>
          </form>

          <div class="actions">
            <button class="btn primary" @click="runLifecycle(() => sandboxes.startSandbox(currentSandboxId))">啟動</button>
            <button class="btn warn" @click="runLifecycle(() => sandboxes.pauseSandbox(currentSandboxId))">暫停</button>
            <button class="btn ghost" @click="runLifecycle(() => sandboxes.stopSandbox(currentSandboxId))">停止</button>
            <button class="btn ghost" @click="resumeReplay">恢復回放</button>
          </div>

          <ReplayTimelineScrubber
            :current-time="snapshot.replay_control?.current_time ?? snapshot.sandbox.replay_current_time"
            :min-time="snapshot.dataset?.start_at ?? null"
            :max-time="snapshot.dataset?.end_at ?? null"
            :pending="isReplayPending"
            @seek="seekReplay"
          >
            <template #status>
              <FreshnessLabel :value="snapshot.freshness_at" />
            </template>
          </ReplayTimelineScrubber>

          <section class="stack">
            <div class="page-header compact-header">
              <div>
                <strong>回放速度</strong>
                <p class="muted">採用樂觀更新；API 失敗時會回復。</p>
              </div>
              <span class="metric">{{ selectedSpeed }}x</span>
            </div>
            <SpeedSelector v-model="selectedSpeed" :pending="isReplayPending" @update:model-value="setReplaySpeed" />
          </section>

          <article class="panel dataset-summary" v-if="snapshot.dataset">
            <div class="page-header compact-header">
              <div>
                <strong>{{ snapshot.dataset.name }}</strong>
                <p class="muted code">{{ snapshot.dataset.id }}</p>
              </div>
              <StatusBadge :label="snapshot.dataset.latest_import_job?.status ?? 'draft'" />
            </div>
            <p class="muted">{{ snapshot.dataset.symbol }} · {{ snapshot.dataset.interval }} · 列數 {{ formatNumber(snapshot.dataset.row_count, 0) }}</p>
            <p class="muted">{{ formatDateTime(snapshot.dataset.start_at) }} 至 {{ formatDateTime(snapshot.dataset.end_at) }}</p>
          </article>
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">帳戶 + 權杖</p>
            <h3>配置</h3>
          </div>
          <form class="form-grid" @submit.prevent="createAccount">
            <label>
              帳戶名稱
              <input v-model="accountForm.name" class="control" placeholder="agent-1" />
            </label>
            <label>
              初始餘額
              <input v-model.number="accountForm.initial_balance" class="control" min="1" step="0.01" type="number" />
            </label>
            <div class="form-actions">
              <button class="btn primary" :disabled="creatingAccount">{{ creatingAccount ? '建立中…' : '建立沙盒帳戶' }}</button>
            </div>
          </form>
          <form class="form-grid token-id-form" @submit.prevent="rotateTokenById">
            <label class="full-width">
              只有權杖 ID？
              <input v-model="tokenIdForm.id" class="control code" placeholder="tok_..." />
            </label>
            <p class="muted full-width">貼上 tok_... 權杖 ID 來輪替，系統會顯示新的完整 Bearer token。</p>
            <div class="form-actions">
              <button class="btn primary" :disabled="rotatingTokenId || !tokenIdForm.id.trim()">
                {{ rotatingTokenId ? '輪替中…' : '輪替並顯示完整 token' }}
              </button>
            </div>
          </form>

          <div v-if="accounts.tokenReveal" class="panel token-card">
            <div class="page-header compact-header">
              <div>
                <strong>完整 Bearer Token</strong>
                <p class="muted">這段才是代理程式下單要放進 Authorization: Bearer 的完整 token。</p>
              </div>
              <div class="actions">
                <button class="btn primary" @click="copyTokenReveal">複製完整 token</button>
                <button class="btn ghost" @click="accounts.dismissTokenReveal()">關閉</button>
              </div>
            </div>
            <pre class="token-value">{{ accounts.tokenReveal.token }}</pre>
            <p class="muted">請立即保存；關閉後後端不會再顯示 secret。`tok_...` 只是權杖 ID，不能單獨用來交易。</p>
          </div>

          <DenseTable v-if="snapshot.accounts.length">
            <table>
              <thead>
                <tr>
                  <th>帳戶</th>
                  <th>狀態</th>
                  <th>餘額</th>
                  <th>權杖</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="account in snapshot.accounts" :key="account.id">
                  <td>
                    <strong>{{ account.name }}</strong>
                    <div class="muted code">{{ account.id }}</div>
                  </td>
                  <td><StatusBadge :label="account.status" /></td>
                  <td>
                    <div>{{ formatCurrency(account.wallet_balance, account.base_currency) }}</div>
                    <div class="muted">權益 {{ formatCurrency(account.equity, account.base_currency) }}</div>
                  </td>
                  <td>
                    <div class="muted" v-if="!accounts.knownTokens[account.id]?.length">保留狀態：目前後端沒有權杖登錄表。</div>
                    <div v-else class="token-stack">
                      <div v-for="token in accounts.knownTokens[account.id]" :key="token.id" class="token-meta">
                        <span class="muted">權杖 ID（不能當作 Bearer token）</span>
                        <span class="code">{{ token.id }}</span>
                        <span class="muted">{{ token.scopes.join(', ') }}</span>
                      </div>
                    </div>
                  </td>
                  <td>
                    <div class="actions">
                      <button class="btn primary" @click="issueToken(account.id)">發行權杖</button>
                      <button class="btn ghost" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="rotateKnown(account.id)">輪替最新</button>
                      <button class="btn danger" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="revokeKnown(account.id)">撤銷最新</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="尚無沙盒帳戶" description="先在此建立虛擬帳戶，再為代理流程發行權杖。" />
        </section>
      </div>

      <AccountEquityChart :series="equitySeries" />

      <div class="three-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">訂單</p>
            <h3>最新訂單</h3>
          </div>
          <DenseTable v-if="snapshot.orders.length">
            <table>
              <thead>
                <tr>
                  <th>交易對</th>
                  <th>方向</th>
                  <th>狀態</th>
                  <th>數量</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="order in snapshot.orders.slice(0, 10)" :key="order.id">
                  <td>{{ order.symbol }}</td>
                  <td>{{ formatSideLabel(order.side) }} / {{ formatSideLabel(order.position_side) }}</td>
                  <td><StatusBadge :label="order.status" /></td>
                  <td class="metric">{{ formatNumber(order.qty) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="尚無訂單" description="代理開始交易後，訂單會顯示在這裡。" />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">交易</p>
            <h3>最新交易</h3>
          </div>
          <DenseTable v-if="snapshot.trades.length">
            <table>
              <thead>
                <tr>
                  <th>交易對</th>
                  <th>方向</th>
                  <th>價格</th>
                  <th>成交時間</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="trade in snapshot.trades.slice(0, 10)" :key="trade.id">
                  <td>{{ trade.symbol }}</td>
                  <td>{{ formatSideLabel(trade.side) }} / {{ formatSideLabel(trade.position_side) }}</td>
                  <td class="metric">{{ formatCurrency(trade.price) }}</td>
                  <td class="metric">{{ formatDateTime(trade.executed_at) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="尚無交易" description="監控快照中的成交交易會串流到此面板。" />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">持倉</p>
            <h3>未平倉持倉</h3>
          </div>
          <DenseTable v-if="snapshot.positions.length">
            <table>
              <thead>
                <tr>
                  <th>交易對</th>
                  <th>方向</th>
                  <th>數量</th>
                  <th>未實現損益</th>
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
          <EmptyState v-else title="尚無持倉" description="交易建立曝險後，未平倉持倉會顯示在這裡。" />
        </section>
      </div>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">事件串流</p>
          <h3>即時沙盒事件</h3>
        </div>
        <div v-if="sandboxEvents.length" class="stack">
          <EventFeedItem v-for="event in sandboxEvents.slice(0, 20)" :key="`${event.topic}-${event.created_at}`" :event="event" />
        </div>
        <EmptyState v-else title="尚無事件" description="回放控制、成交與匯入結果會顯示在這裡。" />
      </section>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AccountEquityChart from '@/components/charts/AccountEquityChart.vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import EventFeedItem from '@/components/common/EventFeedItem.vue'
import FreshnessLabel from '@/components/common/FreshnessLabel.vue'
import ReplayTimelineScrubber from '@/components/common/ReplayTimelineScrubber.vue'
import SpeedSelector from '@/components/common/SpeedSelector.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useFallbackPolling } from '@/composables/useFallbackPolling'
import { useAccountsStore } from '@/stores/accounts'
import { useDatasetsStore } from '@/stores/datasets'
import { useMonitorStore } from '@/stores/monitor'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatCurrency, formatDateTime, formatNumber } from '@/utils/formatters'
import { formatSideLabel } from '@/utils/localization'

const route = useRoute()
const sandboxes = useSandboxesStore()
const datasets = useDatasetsStore()
const monitor = useMonitorStore()
const accounts = useAccountsStore()

const loading = ref(false)
const savingMetadata = ref(false)
const creatingAccount = ref(false)
const errorMessage = ref('')
const selectedSpeed = ref(1)
const metadataForm = reactive({ name: '', dataset_id: '' })
const accountForm = reactive({ name: '', initial_balance: 1000 })
const tokenIdForm = reactive({ id: '' })
const rotatingTokenId = ref(false)
let stopSandboxSocket: null | (() => void) = null

const currentSandboxId = computed(() => String(route.params.id))
const snapshot = computed(() => sandboxes.byId[currentSandboxId.value] ?? monitor.sandboxSnapshots[currentSandboxId.value])
const sandboxEvents = computed(() => monitor.events.filter((event) => event.sandbox_id === currentSandboxId.value))
const isReplayPending = computed(() => sandboxes.pendingReplayAction[currentSandboxId.value] ?? false)
const equitySeries = computed(() => snapshot.value?.accounts.slice(0, 6).map((account) => ({
  name: account.name,
  points: monitor.accountSeries[account.id] ?? [{ timestamp: snapshot.value?.freshness_at ?? new Date().toISOString(), value: account.equity }],
})) ?? [])

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    await Promise.all([sandboxes.loadSnapshot(currentSandboxId.value), datasets.loadList()])
    syncForms()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法載入沙盒詳情。'
  } finally {
    loading.value = false
  }
}

function syncForms() {
  if (!snapshot.value) {
    return
  }
  metadataForm.name = snapshot.value.sandbox.name
  metadataForm.dataset_id = snapshot.value.sandbox.dataset_id
  selectedSpeed.value = snapshot.value.replay_control?.speed ?? snapshot.value.sandbox.replay_speed
}

function attachSandboxSocket() {
  stopSandboxSocket?.()
  stopSandboxSocket = monitor.connectSandbox(currentSandboxId.value, (nextSnapshot) => {
    sandboxes.byId[currentSandboxId.value] = nextSnapshot
    syncForms()
  })
}

async function saveMetadata() {
  savingMetadata.value = true
  errorMessage.value = ''
  try {
    await sandboxes.updateSandbox(currentSandboxId.value, {
      name: metadataForm.name,
      dataset_id: metadataForm.dataset_id,
    })
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法儲存沙盒 metadata。'
  } finally {
    savingMetadata.value = false
  }
}

async function runLifecycle(action: () => Promise<unknown>) {
  errorMessage.value = ''
  try {
    await action()
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '沙盒生命週期操作失敗。'
  }
}

async function seekReplay(value: string) {
  try {
    await sandboxes.seekReplay(currentSandboxId.value, value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '回放跳轉失敗。'
  }
}

async function setReplaySpeed(value: number) {
  selectedSpeed.value = value
  try {
    await sandboxes.setReplaySpeed(currentSandboxId.value, value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '回放速度更新失敗。'
  }
}

async function resumeReplay() {
  try {
    await sandboxes.resumeReplay(currentSandboxId.value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '回放恢復失敗。'
  }
}

async function createAccount() {
  creatingAccount.value = true
  errorMessage.value = ''
  try {
    await sandboxes.createSandboxAccount(currentSandboxId.value, { ...accountForm })
    accountForm.name = ''
    accountForm.initial_balance = 1000
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法建立沙盒帳戶。'
  } finally {
    creatingAccount.value = false
  }
}

async function issueToken(accountId: string) {
  try {
    await accounts.createToken(accountId, ['market:read', 'trade:read', 'trade:write', 'account:read'])
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法發行帳戶權杖。'
  }
}

async function copyTokenReveal() {
  if (!accounts.tokenReveal?.token) {
    return
  }
  await navigator.clipboard?.writeText(accounts.tokenReveal.token)
}

async function rotateTokenById() {
  const tokenId = tokenIdForm.id.trim()
  if (!tokenId) {
    return
  }
  rotatingTokenId.value = true
  errorMessage.value = ''
  try {
    await accounts.rotateKnownToken(tokenId)
    tokenIdForm.id = ''
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法輪替權杖。'
  } finally {
    rotatingTokenId.value = false
  }
}

async function rotateKnown(accountId: string) {
  const token = accounts.knownTokens[accountId]?.[0]
  if (!token) return
  try {
    await accounts.rotateKnownToken(token.id)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法輪替權杖。'
  }
}

async function revokeKnown(accountId: string) {
  const token = accounts.knownTokens[accountId]?.[0]
  if (!token) return
  if (!window.confirm(`要撤銷權杖 ${token.id} 嗎？`)) {
    return
  }
  try {
    await accounts.revokeKnownToken(token.id, accountId)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法撤銷權杖。'
  }
}

useFallbackPolling(() => monitor.connectionState === 'disconnected', async () => {
  await refresh()
})

watch(currentSandboxId, async () => {
  await refresh()
  attachSandboxSocket()
})

onMounted(async () => {
  await refresh()
  attachSandboxSocket()
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
.form-actions {
  grid-column: 1 / -1;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.75rem;
}
.full-width {
  grid-column: 1 / -1;
}
.dataset-summary,
.token-card {
  padding: 1rem;
}
.compact-header {
  align-items: center;
}
.token-value {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-all;
  font-family: Consolas, 'Courier New', monospace;
  color: #d9e4ff;
}
.token-stack {
  display: grid;
  gap: 0.35rem;
}
.token-meta {
  display: grid;
  gap: 0.2rem;
}
</style>

