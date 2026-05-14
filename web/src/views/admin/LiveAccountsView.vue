<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">即時帳戶管理</p>
        <h2>即時帳戶</h2>
        <p>管理即時市場 metadata、健康指標與權杖發行。此介面刻意不涵蓋交易執行。</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? '重新整理中…' : '重新整理' }}</button>
    </header>

    <div class="kpi-grid">
      <SummaryCard label="即時帳戶" :value="accounts.liveAccounts.length" detail="目前後端中的 metadata-only 即時帳戶。" />
      <SummaryCard label="啟用" :value="activeLiveAccounts" detail="目前標記為啟用的帳戶。" />
      <SummaryCard label="憑證健康" :value="healthyCredentials" detail="回報憑證健康的帳戶。" />
      <SummaryCard label="工作階段已知權杖" :value="knownLiveTokens" detail="此瀏覽器工作階段可見的已發行或已輪替即時權杖。" />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">建立即時帳戶</p>
          <h3>Metadata 配置</h3>
        </div>
        <form class="form-grid" @submit.prevent="createLiveAccount">
          <label>
            名稱
            <input v-model="createForm.name" class="control" placeholder="binance-sim-1" />
          </label>
          <label>
            初始餘額
            <input v-model.number="createForm.initial_balance" class="control" min="1" step="0.01" type="number" />
          </label>
          <label>
            供應商
            <input v-model="createForm.provider" class="control code" placeholder="binance" />
          </label>
          <label>
            環境
            <input v-model="createForm.environment" class="control code" placeholder="paper" />
          </label>
          <label>
            價格模式
            <input v-model="createForm.price_mode" class="control code" placeholder="live" />
          </label>
          <label>
            憑證狀態
            <select v-model="createForm.credentials_status" class="control">
              <option value="healthy">健康</option>
              <option value="degraded">降級</option>
              <option value="missing">缺失</option>
            </select>
          </label>
          <label class="full-width">
            支援交易對
            <input v-model="createForm.supported_symbols" class="control code" placeholder="BTCUSDT, ETHUSDT" />
          </label>
          <div class="form-actions">
            <button class="btn primary" :disabled="creating">{{ creating ? '建立中…' : '建立即時帳戶' }}</button>
          </div>
        </form>

        <article class="panel token-card" v-if="accounts.tokenReveal">
          <div class="page-header compact-header">
            <div>
              <strong>一次性權杖顯示</strong>
              <p class="muted">請立即保存。目前後端沒有權杖讀取/列表 API。</p>
            </div>
            <button class="btn ghost" @click="accounts.dismissTokenReveal()">關閉</button>
          </div>
          <pre class="token-value">{{ accounts.tokenReveal.token }}</pre>
        </article>
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">P1 邊界</p>
          <h3>執行防護</h3>
        </div>
        <ul class="muted notes-list">
          <li>即時帳戶可讀取即時市場資料並接收權杖，但刻意禁止下單。</li>
          <li>此後端的憑證狀態僅為 metadata；介面會顯示但不驗證 secret。</li>
          <li>啟用帳戶前，請用系統健康檢查即時交易對快取與新鮮度。</li>
        </ul>
      </section>
    </div>

    <section v-if="accounts.liveAccounts.length" class="panel page-panel stack">
      <div>
        <p class="eyebrow">即時帳戶清冊</p>
        <h3>已設定帳戶</h3>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>帳戶</th>
              <th>狀態</th>
              <th>供應商</th>
              <th>支援交易對</th>
              <th>權杖</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="account in accounts.liveAccounts" :key="account.id">
              <td>
                <label>
                  名稱
                  <input v-model="drafts[account.id].name" class="control" />
                </label>
                <div class="muted code">{{ account.id }}</div>
              </td>
              <td>
                <label>
                  狀態
                  <select v-model="drafts[account.id].status" class="control">
                    <option value="active">啟用</option>
                    <option value="disabled">停用</option>
                  </select>
                </label>
                <div class="status-line">
                  <StatusBadge :label="account.credentials_status ?? 'unknown'" />
                </div>
              </td>
              <td>
                <label>
                  供應商
                  <input v-model="drafts[account.id].provider" class="control code" />
                </label>
                <label>
                  環境
                  <input v-model="drafts[account.id].environment" class="control code" />
                </label>
              </td>
              <td>
                <label>
                  交易對
                  <input v-model="drafts[account.id].supportedSymbols" class="control code" />
                </label>
                <div class="muted">價格模式 {{ account.price_mode || 'live' }}</div>
              </td>
              <td>
                <div v-if="accounts.knownTokens[account.id]?.length" class="stack small-gap">
                  <div v-for="token in accounts.knownTokens[account.id]" :key="token.id" class="panel token-chip">
                    <strong class="code">{{ token.id }}</strong>
                    <span class="muted">{{ token.scopes.join(', ') }}</span>
                  </div>
                </div>
                <span v-else class="muted">保留空狀態：後端沒有權杖登錄表。</span>
              </td>
              <td>
                <div class="actions">
                  <button class="btn ghost" @click="saveLiveAccount(account.id)">儲存</button>
                  <button class="btn primary" @click="issueToken(account.id)">發行權杖</button>
                  <button class="btn ghost" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="rotateKnown(account.id)">輪替最新</button>
                  <button class="btn danger" @click="removeLiveAccount(account.id)">刪除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>
    <EmptyState v-else title="尚無即時帳戶" description="建立第一筆即時帳戶 metadata，以測試即時市場讀取路徑與權杖發行。" />

    <ErrorState v-if="errorMessage" title="即時帳戶操作失敗" :description="errorMessage" />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useAccountsStore } from '@/stores/accounts'
import type { Account } from '@/types/domain'
import { joinSymbols, splitSymbols } from '@/utils/formatters'

const accounts = useAccountsStore()

const loading = ref(false)
const creating = ref(false)
const errorMessage = ref('')
const drafts = reactive<Record<string, { name: string; status: string; provider: string; environment: string; supportedSymbols: string }>>({})
const createForm = reactive({
  name: '',
  initial_balance: 1000,
  provider: 'binance',
  environment: 'paper',
  price_mode: 'live',
  credentials_status: 'healthy',
  supported_symbols: 'BTCUSDT, ETHUSDT',
})

const activeLiveAccounts = computed(() => accounts.liveAccounts.filter((account) => account.status === 'active').length)
const healthyCredentials = computed(() => accounts.liveAccounts.filter((account) => account.credentials_status === 'healthy').length)
const knownLiveTokens = computed(() => accounts.liveAccounts.reduce((sum, account) => sum + (accounts.knownTokens[account.id]?.length ?? 0), 0))

function syncDrafts(items: Account[]) {
  items.forEach((account) => {
    drafts[account.id] = {
      name: account.name,
      status: account.status,
      provider: account.provider ?? '',
      environment: account.environment ?? '',
      supportedSymbols: joinSymbols(account.supported_symbols),
    }
  })
}

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    const items = await accounts.loadLiveAccounts()
    syncDrafts(items)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法載入即時帳戶。'
  } finally {
    loading.value = false
  }
}

async function createLiveAccount() {
  creating.value = true
  errorMessage.value = ''
  try {
    await accounts.createLiveAccount({
      name: createForm.name,
      initial_balance: createForm.initial_balance,
      provider: createForm.provider,
      environment: createForm.environment,
      price_mode: createForm.price_mode,
      credentials_status: createForm.credentials_status,
      supported_symbols: splitSymbols(createForm.supported_symbols),
      type: 'live',
    })
    createForm.name = ''
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法建立即時帳戶。'
  } finally {
    creating.value = false
  }
}

async function saveLiveAccount(accountId: string) {
  errorMessage.value = ''
  try {
    await accounts.updateLiveAccount(accountId, {
      name: drafts[accountId].name,
      status: drafts[accountId].status,
      provider: drafts[accountId].provider,
      environment: drafts[accountId].environment,
      supported_symbols: splitSymbols(drafts[accountId].supportedSymbols),
    })
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法更新即時帳戶。'
  }
}

async function removeLiveAccount(accountId: string) {
  if (!window.confirm(`要刪除即時帳戶 ${accountId} 嗎？`)) {
    return
  }
  errorMessage.value = ''
  try {
    await accounts.deleteLiveAccount(accountId)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法刪除即時帳戶。'
  }
}

async function issueToken(accountId: string) {
  try {
    await accounts.createToken(accountId, ['market:read', 'account:read'])
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法發行即時帳戶權杖。'
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
.full-width,
.form-actions {
  grid-column: 1 / -1;
}
.notes-list {
  margin: 0;
  padding-left: 1.1rem;
  display: grid;
  gap: 0.75rem;
}
.status-line {
  margin-top: 0.75rem;
}
.token-card,
.token-chip {
  padding: 1rem;
}
.token-chip {
  padding: 0.65rem 0.8rem;
}
.small-gap {
  gap: 0.6rem;
}
.token-value {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-all;
  font-family: Consolas, 'Courier New', monospace;
  color: #d9e4ff;
}
</style>

