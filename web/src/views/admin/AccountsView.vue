<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">帳戶與權杖</p>
        <h2>虛擬帳戶管理</h2>
        <p>建立沙盒範圍內的虛擬帳戶，並發行一次性代理權杖。歷史權杖登錄刻意不提供。</p>
      </div>
      <div class="actions">
        <select v-model="selectedSandboxId" class="control compact-control">
          <option value="">選擇沙盒</option>
          <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">
            {{ sandbox.name }} · {{ sandbox.status }}
          </option>
        </select>
        <button class="btn ghost" :disabled="loading || !selectedSandboxId" @click="refresh">{{ loading ? '重新整理中…' : '重新整理' }}</button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="帳戶操作失敗" :description="errorMessage" />
    <EmptyState v-else-if="!selectedSandboxId" title="選擇沙盒" description="虛擬帳戶 CRUD 以沙盒為範圍；請選擇沙盒後繼續。" />

    <template v-else>
      <div class="kpi-grid">
        <SummaryCard label="沙盒帳戶" :value="currentAccounts.length" detail="目前已在選取沙盒中建立的帳戶。" />
        <SummaryCard label="已知權杖" :value="knownTokenCount" detail="僅包含此瀏覽器工作階段建立或輪替的權杖。" />
        <SummaryCard label="啟用帳戶" :value="activeAccounts" detail="狀態為啟用的帳戶。" />
        <SummaryCard label="總權益" :value="formatCurrency(totalEquity)" detail="目前載入的沙盒帳戶總權益。" />
      </div>

      <div class="two-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">建立帳戶</p>
            <h3>配置虛擬帳戶</h3>
          </div>
          <form class="form-grid" @submit.prevent="createAccount">
            <label>
              帳戶名稱
              <input v-model="createForm.name" class="control" placeholder="research-agent-1" />
            </label>
            <label>
              初始餘額
              <input v-model.number="createForm.initial_balance" class="control" min="1" step="0.01" type="number" />
            </label>
            <div class="form-actions">
              <button class="btn primary" :disabled="creating">{{ creating ? '建立中…' : '建立帳戶' }}</button>
            </div>
          </form>

          <article class="panel token-card" v-if="accounts.tokenReveal">
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
          </article>
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">權杖限制</p>
            <h3>保留狀態處理</h3>
          </div>
          <ul class="muted notes-list">
            <li>此處只能操作目前瀏覽器工作階段建立或輪替的權杖。</li>
            <li>若帳戶沒有已知權杖，介面會顯示保留空狀態，而不假裝存在登錄表。</li>
            <li>發行後可到沙盒詳情查看訂單、交易與持倉的完整監控脈絡。</li>
          </ul>
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
        </section>
      </div>

      <section class="panel page-panel stack" v-if="currentAccounts.length">
        <div>
          <p class="eyebrow">沙盒帳戶</p>
          <h3>{{ selectedSandbox?.name ?? selectedSandboxId }}</h3>
        </div>
        <DenseTable>
          <table>
            <thead>
              <tr>
                <th>帳戶</th>
                <th>狀態</th>
                <th>餘額</th>
                <th>權杖狀態</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="account in currentAccounts" :key="account.id">
                <td>
                  <label>
                    顯示名稱
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
                </td>
                <td>
                  <div>{{ formatCurrency(account.wallet_balance, account.base_currency) }}</div>
                  <div class="muted">權益 {{ formatCurrency(account.equity, account.base_currency) }}</div>
                </td>
                <td>
                  <div v-if="accounts.knownTokens[account.id]?.length" class="stack small-gap">
                    <div v-for="token in accounts.knownTokens[account.id]" :key="token.id" class="panel token-chip">
                      <span class="muted">權杖 ID（不能當作 Bearer token）</span>
                      <strong class="code">{{ token.id }}</strong>
                      <span class="muted">{{ token.scopes.join(', ') }}</span>
                    </div>
                  </div>
                  <span v-else class="muted">保留空狀態：目前後端沒有權杖登錄表。</span>
                </td>
                <td>
                  <div class="actions">
                    <button class="btn ghost" @click="saveAccount(account.id)">儲存</button>
                    <button class="btn primary" @click="issueToken(account.id)">發行權杖</button>
                    <button class="btn ghost" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="rotateKnown(account.id)">輪替最新</button>
                    <button class="btn danger" @click="removeAccount(account.id)">刪除</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </DenseTable>
      </section>
      <EmptyState v-else title="尚無虛擬帳戶" description="建立第一個沙盒範圍帳戶，然後發行代理存取權杖。" />
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useAccountsStore } from '@/stores/accounts'
import { useSandboxesStore } from '@/stores/sandboxes'
import type { Account } from '@/types/domain'
import { formatCurrency } from '@/utils/formatters'

const sandboxes = useSandboxesStore()
const accounts = useAccountsStore()

const loading = ref(false)
const creating = ref(false)
const errorMessage = ref('')
const selectedSandboxId = ref('')
const createForm = reactive({ name: '', initial_balance: 1000 })
const tokenIdForm = reactive({ id: '' })
const drafts = reactive<Record<string, { name: string; status: string }>>({})
const rotatingTokenId = ref(false)

const selectedSandbox = computed(() => sandboxes.items.find((item) => item.id === selectedSandboxId.value))
const currentAccounts = computed(() => accounts.sandboxAccounts[selectedSandboxId.value] ?? [])
const knownTokenCount = computed(() => currentAccounts.value.reduce((sum, account) => sum + (accounts.knownTokens[account.id]?.length ?? 0), 0))
const activeAccounts = computed(() => currentAccounts.value.filter((account) => account.status === 'active').length)
const totalEquity = computed(() => currentAccounts.value.reduce((sum, account) => sum + account.equity, 0))

function syncDrafts(items: Account[]) {
  items.forEach((account) => {
    drafts[account.id] = drafts[account.id] ?? { name: account.name, status: account.status }
    drafts[account.id].name = account.name
    drafts[account.id].status = account.status
  })
}

async function refresh() {
  if (!selectedSandboxId.value) {
    return
  }
  loading.value = true
  errorMessage.value = ''
  try {
    const items = await accounts.loadSandboxAccounts(selectedSandboxId.value)
    syncDrafts(items)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法載入沙盒帳戶。'
  } finally {
    loading.value = false
  }
}

async function createAccount() {
  if (!selectedSandboxId.value) return
  creating.value = true
  errorMessage.value = ''
  try {
    await sandboxes.createSandboxAccount(selectedSandboxId.value, { ...createForm })
    createForm.name = ''
    createForm.initial_balance = 1000
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法建立虛擬帳戶。'
  } finally {
    creating.value = false
  }
}

async function saveAccount(accountId: string) {
  errorMessage.value = ''
  try {
    await accounts.updateAccount(accountId, drafts[accountId])
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法更新帳戶。'
  }
}

async function removeAccount(accountId: string) {
  if (!window.confirm(`要刪除帳戶 ${accountId} 嗎？`)) {
    return
  }
  errorMessage.value = ''
  try {
    await accounts.deleteAccount(accountId, selectedSandboxId.value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '無法刪除帳戶。'
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

watch(currentAccounts, (items) => {
  syncDrafts(items)
}, { immediate: true })

watch(selectedSandboxId, () => {
  if (selectedSandboxId.value) {
    void refresh()
  }
})

onMounted(async () => {
  await sandboxes.loadList()
  if (sandboxes.items.length) {
    selectedSandboxId.value = sandboxes.items[0].id
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
.compact-control {
  min-width: 280px;
}
.form-actions {
  grid-column: 1 / -1;
}
.full-width {
  grid-column: 1 / -1;
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
.notes-list {
  margin: 0;
  padding-left: 1.1rem;
  display: grid;
  gap: 0.75rem;
}
.token-value {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-all;
  font-family: Consolas, 'Courier New', monospace;
  color: #d9e4ff;
}
</style>

