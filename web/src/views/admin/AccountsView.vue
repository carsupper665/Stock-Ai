<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Accounts & Tokens</p>
        <h2>Virtual Account Manager</h2>
        <p>Provision sandbox-scoped virtual accounts and issue one-time agent tokens. Historical token registry is intentionally unavailable.</p>
      </div>
      <div class="actions">
        <select v-model="selectedSandboxId" class="control compact-control">
          <option value="">Select sandbox</option>
          <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">
            {{ sandbox.name }} · {{ sandbox.status }}
          </option>
        </select>
        <button class="btn ghost" :disabled="loading || !selectedSandboxId" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="Account action failed" :description="errorMessage" />
    <EmptyState v-else-if="!selectedSandboxId" title="Select a sandbox" description="Virtual account CRUD is sandbox-scoped. Pick a sandbox to continue." />

    <template v-else>
      <div class="kpi-grid">
        <SummaryCard label="Sandbox accounts" :value="currentAccounts.length" detail="Accounts currently provisioned in the selected sandbox." />
        <SummaryCard label="Known tokens" :value="knownTokenCount" detail="Tokens created or rotated during this UI session only." />
        <SummaryCard label="Active accounts" :value="activeAccounts" detail="Accounts whose status is active." />
        <SummaryCard label="Total equity" :value="formatCurrency(totalEquity)" detail="Aggregate equity across currently loaded sandbox accounts." />
      </div>

      <div class="two-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Create Account</p>
            <h3>Provision Virtual Account</h3>
          </div>
          <form class="form-grid" @submit.prevent="createAccount">
            <label>
              Account name
              <input v-model="createForm.name" class="control" placeholder="research-agent-1" />
            </label>
            <label>
              Initial balance
              <input v-model.number="createForm.initial_balance" class="control" min="1" step="0.01" type="number" />
            </label>
            <div class="form-actions">
              <button class="btn primary" :disabled="creating">{{ creating ? 'Creating…' : 'Create account' }}</button>
            </div>
          </form>

          <article class="panel token-card" v-if="accounts.tokenReveal">
            <div class="page-header compact-header">
              <div>
                <strong>One-time token reveal</strong>
                <p class="muted">Copy this token now. The backend intentionally does not support token read/list.</p>
              </div>
              <button class="btn ghost" @click="accounts.dismissTokenReveal()">Dismiss</button>
            </div>
            <pre class="token-value">{{ accounts.tokenReveal.token }}</pre>
          </article>
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Token Constraints</p>
            <h3>Reserved State Handling</h3>
          </div>
          <ul class="muted notes-list">
            <li>Only tokens created or rotated in the current browser session are actionable here.</li>
            <li>If an account has no known token, the UI shows a reserved empty state instead of pretending a registry exists.</li>
            <li>Use Sandbox Detail for higher-context monitoring of orders, trades, and positions after issuance.</li>
          </ul>
        </section>
      </div>

      <section class="panel page-panel stack" v-if="currentAccounts.length">
        <div>
          <p class="eyebrow">Sandbox Accounts</p>
          <h3>{{ selectedSandbox?.name ?? selectedSandboxId }}</h3>
        </div>
        <DenseTable>
          <table>
            <thead>
              <tr>
                <th>Account</th>
                <th>Status</th>
                <th>Balance</th>
                <th>Token State</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="account in currentAccounts" :key="account.id">
                <td>
                  <label>
                    Display name
                    <input v-model="drafts[account.id].name" class="control" />
                  </label>
                  <div class="muted code">{{ account.id }}</div>
                </td>
                <td>
                  <label>
                    Status
                    <select v-model="drafts[account.id].status" class="control">
                      <option value="active">active</option>
                      <option value="disabled">disabled</option>
                    </select>
                  </label>
                </td>
                <td>
                  <div>{{ formatCurrency(account.wallet_balance, account.base_currency) }}</div>
                  <div class="muted">Equity {{ formatCurrency(account.equity, account.base_currency) }}</div>
                </td>
                <td>
                  <div v-if="accounts.knownTokens[account.id]?.length" class="stack small-gap">
                    <div v-for="token in accounts.knownTokens[account.id]" :key="token.id" class="panel token-chip">
                      <strong class="code">{{ token.id }}</strong>
                      <span class="muted">{{ token.scopes.join(', ') }}</span>
                    </div>
                  </div>
                  <span v-else class="muted">Reserved empty state: no token registry in current backend.</span>
                </td>
                <td>
                  <div class="actions">
                    <button class="btn ghost" @click="saveAccount(account.id)">Save</button>
                    <button class="btn primary" @click="issueToken(account.id)">Issue token</button>
                    <button class="btn ghost" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="rotateKnown(account.id)">Rotate latest</button>
                    <button class="btn danger" @click="removeAccount(account.id)">Delete</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </DenseTable>
      </section>
      <EmptyState v-else title="No virtual accounts yet" description="Create the first sandbox-scoped account, then issue a token for agent access." />
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
const drafts = reactive<Record<string, { name: string; status: string }>>({})

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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load sandbox accounts.'
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to create virtual account.'
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to update account.'
  }
}

async function removeAccount(accountId: string) {
  if (!window.confirm(`Delete account ${accountId}?`)) {
    return
  }
  errorMessage.value = ''
  try {
    await accounts.deleteAccount(accountId, selectedSandboxId.value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to delete account.'
  }
}

async function issueToken(accountId: string) {
  try {
    await accounts.createToken(accountId, ['market:read', 'trade:read', 'trade:write', 'account:read'])
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to issue account token.'
  }
}

async function rotateKnown(accountId: string) {
  const token = accounts.knownTokens[accountId]?.[0]
  if (!token) return
  try {
    await accounts.rotateKnownToken(token.id, accountId)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to rotate token.'
  }
}

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

