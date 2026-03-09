<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Sandbox Detail</p>
        <h2>{{ snapshot?.sandbox.name ?? route.params.id }}</h2>
        <p>Single control surface for replay metadata, dataset binding, accounts, snapshots, and event feedback.</p>
      </div>
      <div class="actions">
        <FreshnessLabel :value="snapshot?.freshness_at" />
        <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh snapshot' }}</button>
      </div>
    </header>

    <ErrorState v-if="errorMessage" title="Sandbox detail failed" :description="errorMessage" retry-label="Retry" @retry="refresh" />
    <EmptyState v-else-if="!snapshot && !loading" title="Sandbox snapshot unavailable" description="The selected sandbox could not be loaded from the backend." />

    <template v-else-if="snapshot">
      <div class="kpi-grid">
        <SummaryCard label="Accounts" :value="snapshot.accounts.length" detail="Virtual accounts provisioned in this sandbox." />
        <SummaryCard label="Open positions" :value="snapshot.positions.length" detail="Currently open positions across all sandbox accounts." />
        <SummaryCard label="Orders" :value="snapshot.orders.length" detail="Latest order records in this sandbox." />
        <SummaryCard label="Trades" :value="snapshot.trades.length" detail="Recent executed trades for monitoring and audit." />
      </div>

      <div class="two-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Metadata + Replay Control</p>
            <h3>Main Control Surface</h3>
          </div>
          <form class="form-grid" @submit.prevent="saveMetadata">
            <label>
              Sandbox name
              <input v-model="metadataForm.name" class="control" />
            </label>
            <label>
              Dataset binding
              <select v-model="metadataForm.dataset_id" class="control">
                <option value="">Select dataset</option>
                <option v-for="dataset in datasets.items" :key="dataset.id" :value="dataset.id">
                  {{ dataset.name }} · {{ dataset.symbol }}
                </option>
              </select>
            </label>
            <div class="form-actions">
              <button class="btn primary" :disabled="savingMetadata">{{ savingMetadata ? 'Saving…' : 'Save metadata' }}</button>
              <StatusBadge :label="snapshot.sandbox.status" />
              <span class="muted code">{{ snapshot.sandbox.id }}</span>
            </div>
          </form>

          <div class="actions">
            <button class="btn primary" @click="runLifecycle(() => sandboxes.startSandbox(currentSandboxId))">Start</button>
            <button class="btn warn" @click="runLifecycle(() => sandboxes.pauseSandbox(currentSandboxId))">Pause</button>
            <button class="btn ghost" @click="runLifecycle(() => sandboxes.stopSandbox(currentSandboxId))">Stop</button>
            <button class="btn ghost" @click="resumeReplay">Resume replay</button>
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
                <strong>Replay speed</strong>
                <p class="muted">Optimistic UI with rollback on API failure.</p>
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
            <p class="muted">{{ snapshot.dataset.symbol }} · {{ snapshot.dataset.interval }} · Rows {{ formatNumber(snapshot.dataset.row_count, 0) }}</p>
            <p class="muted">{{ formatDateTime(snapshot.dataset.start_at) }} to {{ formatDateTime(snapshot.dataset.end_at) }}</p>
          </article>
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Accounts + Tokens</p>
            <h3>Provisioning</h3>
          </div>
          <form class="form-grid" @submit.prevent="createAccount">
            <label>
              Account name
              <input v-model="accountForm.name" class="control" placeholder="agent-1" />
            </label>
            <label>
              Initial balance
              <input v-model.number="accountForm.initial_balance" class="control" min="1" step="0.01" type="number" />
            </label>
            <div class="form-actions">
              <button class="btn primary" :disabled="creatingAccount">{{ creatingAccount ? 'Creating…' : 'Create sandbox account' }}</button>
            </div>
          </form>

          <div v-if="accounts.tokenReveal" class="panel token-card">
            <div class="page-header compact-header">
              <div>
                <strong>One-time token reveal</strong>
                <p class="muted">Store it now. The backend does not expose token read/list APIs.</p>
              </div>
              <button class="btn ghost" @click="accounts.dismissTokenReveal()">Dismiss</button>
            </div>
            <pre class="token-value">{{ accounts.tokenReveal.token }}</pre>
          </div>

          <DenseTable v-if="snapshot.accounts.length">
            <table>
              <thead>
                <tr>
                  <th>Account</th>
                  <th>Status</th>
                  <th>Balances</th>
                  <th>Tokens</th>
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
                    <div class="muted">Equity {{ formatCurrency(account.equity, account.base_currency) }}</div>
                  </td>
                  <td>
                    <div class="muted" v-if="!accounts.knownTokens[account.id]?.length">Reserved: no token registry in current backend.</div>
                    <div v-else class="token-stack">
                      <div v-for="token in accounts.knownTokens[account.id]" :key="token.id" class="token-meta">
                        <span class="code">{{ token.id }}</span>
                        <span class="muted">{{ token.scopes.join(', ') }}</span>
                      </div>
                    </div>
                  </td>
                  <td>
                    <div class="actions">
                      <button class="btn primary" @click="issueToken(account.id)">Issue token</button>
                      <button class="btn ghost" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="rotateKnown(account.id)">Rotate latest</button>
                      <button class="btn danger" :disabled="!accounts.knownTokens[account.id]?.[0]" @click="revokeKnown(account.id)">Revoke latest</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="No sandbox accounts" description="Create a virtual account here, then issue a token for the agent flow." />
        </section>
      </div>

      <AccountEquityChart :series="equitySeries" />

      <div class="three-column">
        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Orders</p>
            <h3>Latest Orders</h3>
          </div>
          <DenseTable v-if="snapshot.orders.length">
            <table>
              <thead>
                <tr>
                  <th>Symbol</th>
                  <th>Side</th>
                  <th>Status</th>
                  <th>Qty</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="order in snapshot.orders.slice(0, 10)" :key="order.id">
                  <td>{{ order.symbol }}</td>
                  <td>{{ order.side }} / {{ order.position_side }}</td>
                  <td><StatusBadge :label="order.status" /></td>
                  <td class="metric">{{ formatNumber(order.qty) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="No orders yet" description="Orders appear here after the agent starts trading." />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Trades</p>
            <h3>Latest Trades</h3>
          </div>
          <DenseTable v-if="snapshot.trades.length">
            <table>
              <thead>
                <tr>
                  <th>Symbol</th>
                  <th>Side</th>
                  <th>Price</th>
                  <th>Executed</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="trade in snapshot.trades.slice(0, 10)" :key="trade.id">
                  <td>{{ trade.symbol }}</td>
                  <td>{{ trade.side }} / {{ trade.position_side }}</td>
                  <td class="metric">{{ formatCurrency(trade.price) }}</td>
                  <td class="metric">{{ formatDateTime(trade.executed_at) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="No trades yet" description="Executed trades stream into this panel from the monitor snapshot." />
        </section>

        <section class="panel page-panel stack">
          <div>
            <p class="eyebrow">Positions</p>
            <h3>Open Positions</h3>
          </div>
          <DenseTable v-if="snapshot.positions.length">
            <table>
              <thead>
                <tr>
                  <th>Symbol</th>
                  <th>Side</th>
                  <th>Qty</th>
                  <th>Unrealized PnL</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="position in snapshot.positions.slice(0, 10)" :key="position.id">
                  <td>{{ position.symbol }}</td>
                  <td>{{ position.position_side }}</td>
                  <td class="metric">{{ formatNumber(position.qty) }}</td>
                  <td class="metric">{{ formatCurrency(position.unrealized_pnl) }}</td>
                </tr>
              </tbody>
            </table>
          </DenseTable>
          <EmptyState v-else title="No positions" description="Open positions will appear here once trades establish exposure." />
        </section>
      </div>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Event Feed</p>
          <h3>Realtime Sandbox Feed</h3>
        </div>
        <div v-if="sandboxEvents.length" class="stack">
          <EventFeedItem v-for="event in sandboxEvents.slice(0, 20)" :key="`${event.topic}-${event.created_at}`" :event="event" />
        </div>
        <EmptyState v-else title="No events yet" description="Replay controls, fills, and import outcomes will appear here." />
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load sandbox detail.'
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to save sandbox metadata.'
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
    errorMessage.value = error instanceof Error ? error.message : 'Sandbox lifecycle action failed.'
  }
}

async function seekReplay(value: string) {
  try {
    await sandboxes.seekReplay(currentSandboxId.value, value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Replay seek failed.'
  }
}

async function setReplaySpeed(value: number) {
  selectedSpeed.value = value
  try {
    await sandboxes.setReplaySpeed(currentSandboxId.value, value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Replay speed update failed.'
  }
}

async function resumeReplay() {
  try {
    await sandboxes.resumeReplay(currentSandboxId.value)
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Replay resume failed.'
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to create sandbox account.'
  } finally {
    creatingAccount.value = false
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

async function revokeKnown(accountId: string) {
  const token = accounts.knownTokens[accountId]?.[0]
  if (!token) return
  if (!window.confirm(`Revoke token ${token.id}?`)) {
    return
  }
  try {
    await accounts.revokeKnownToken(token.id, accountId)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to revoke token.'
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

