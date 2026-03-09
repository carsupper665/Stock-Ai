<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Live Account Manager</p>
        <h2>Live Accounts</h2>
        <p>Manage live-market metadata, health indicators, and token issuance. Trade execution is intentionally out of scope for this UI.</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
    </header>

    <div class="kpi-grid">
      <SummaryCard label="Live accounts" :value="accounts.liveAccounts.length" detail="Metadata-only live accounts in the current backend." />
      <SummaryCard label="Active" :value="activeLiveAccounts" detail="Accounts currently marked active." />
      <SummaryCard label="Healthy credentials" :value="healthyCredentials" detail="Accounts reporting healthy credentials status." />
      <SummaryCard label="Session-known tokens" :value="knownLiveTokens" detail="Issued or rotated live tokens visible to this browser session only." />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Create Live Account</p>
          <h3>Metadata Provisioning</h3>
        </div>
        <form class="form-grid" @submit.prevent="createLiveAccount">
          <label>
            Name
            <input v-model="createForm.name" class="control" placeholder="binance-sim-1" />
          </label>
          <label>
            Initial balance
            <input v-model.number="createForm.initial_balance" class="control" min="1" step="0.01" type="number" />
          </label>
          <label>
            Provider
            <input v-model="createForm.provider" class="control code" placeholder="binance" />
          </label>
          <label>
            Environment
            <input v-model="createForm.environment" class="control code" placeholder="paper" />
          </label>
          <label>
            Price mode
            <input v-model="createForm.price_mode" class="control code" placeholder="live" />
          </label>
          <label>
            Credentials status
            <select v-model="createForm.credentials_status" class="control">
              <option value="healthy">healthy</option>
              <option value="degraded">degraded</option>
              <option value="missing">missing</option>
            </select>
          </label>
          <label class="full-width">
            Supported symbols
            <input v-model="createForm.supported_symbols" class="control code" placeholder="BTCUSDT, ETHUSDT" />
          </label>
          <div class="form-actions">
            <button class="btn primary" :disabled="creating">{{ creating ? 'Creating…' : 'Create live account' }}</button>
          </div>
        </form>

        <article class="panel token-card" v-if="accounts.tokenReveal">
          <div class="page-header compact-header">
            <div>
              <strong>One-time token reveal</strong>
              <p class="muted">Copy immediately. There is no token read/list API in the current backend.</p>
            </div>
            <button class="btn ghost" @click="accounts.dismissTokenReveal()">Dismiss</button>
          </div>
          <pre class="token-value">{{ accounts.tokenReveal.token }}</pre>
        </article>
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">P1 Boundary</p>
          <h3>Execution Guardrails</h3>
        </div>
        <ul class="muted notes-list">
          <li>Live accounts can read live market data and receive tokens, but order placement is intentionally blocked.</li>
          <li>Credentials status is metadata only in this backend; the UI surfaces it but does not validate secrets.</li>
          <li>Use System Health to inspect the live symbol cache and freshness before enabling an account.</li>
        </ul>
      </section>
    </div>

    <section v-if="accounts.liveAccounts.length" class="panel page-panel stack">
      <div>
        <p class="eyebrow">Live Account Inventory</p>
        <h3>Configured Accounts</h3>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>Account</th>
              <th>Status</th>
              <th>Provider</th>
              <th>Supported Symbols</th>
              <th>Tokens</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="account in accounts.liveAccounts" :key="account.id">
              <td>
                <label>
                  Name
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
                <div class="status-line">
                  <StatusBadge :label="account.credentials_status ?? 'unknown'" />
                </div>
              </td>
              <td>
                <label>
                  Provider
                  <input v-model="drafts[account.id].provider" class="control code" />
                </label>
                <label>
                  Environment
                  <input v-model="drafts[account.id].environment" class="control code" />
                </label>
              </td>
              <td>
                <label>
                  Symbols
                  <input v-model="drafts[account.id].supportedSymbols" class="control code" />
                </label>
                <div class="muted">Price mode {{ account.price_mode || 'live' }}</div>
              </td>
              <td>
                <div v-if="accounts.knownTokens[account.id]?.length" class="stack small-gap">
                  <div v-for="token in accounts.knownTokens[account.id]" :key="token.id" class="panel token-chip">
                    <strong class="code">{{ token.id }}</strong>
                    <span class="muted">{{ token.scopes.join(', ') }}</span>
                  </div>
                </div>
                <span v-else class="muted">Reserved empty state: no backend token registry.</span>
              </td>
              <td>
                <div class="actions">
                  <button class="btn ghost" @click="saveLiveAccount(account.id)">Save</button>
                  <button class="btn primary" @click="issueToken(account.id)">Issue token</button>
                  <button class="btn ghost" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="rotateKnown(account.id)">Rotate latest</button>
                  <button class="btn danger" @click="removeLiveAccount(account.id)">Delete</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>
    <EmptyState v-else title="No live accounts" description="Create the first live account metadata entry to test live market read paths and token issuance." />

    <ErrorState v-if="errorMessage" title="Live account action failed" :description="errorMessage" />
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load live accounts.'
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to create live account.'
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to update live account.'
  }
}

async function removeLiveAccount(accountId: string) {
  if (!window.confirm(`Delete live account ${accountId}?`)) {
    return
  }
  errorMessage.value = ''
  try {
    await accounts.deleteLiveAccount(accountId)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to delete live account.'
  }
}

async function issueToken(accountId: string) {
  try {
    await accounts.createToken(accountId, ['market:read', 'account:read'])
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to issue live account token.'
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

