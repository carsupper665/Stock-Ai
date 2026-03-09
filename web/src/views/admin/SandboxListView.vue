<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Admin Console</p>
        <h2>Sandbox List</h2>
        <p>Provision replay sandboxes, bind datasets, and control lifecycle from one surface.</p>
      </div>
      <div class="header-tools">
        <input v-model="search" class="control" placeholder="Search by sandbox id or name" />
        <button class="btn ghost" :disabled="refreshing" @click="refresh">{{ refreshing ? 'Refreshing…' : 'Refresh' }}</button>
      </div>
    </header>

    <div class="kpi-grid">
      <SummaryCard label="Total sandboxes" :value="sandboxes.items.length" detail="All replay environments currently known to the backend." />
      <SummaryCard label="Running" :value="runningCount" detail="Sandboxes currently advancing replay time." />
      <SummaryCard label="Ready / Paused" :value="readyCount" detail="Workspaces ready for start or resume." />
      <SummaryCard label="Datasets available" :value="datasets.items.length" detail="Selectable replay datasets for new sandboxes." />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Create Sandbox</p>
          <h3>New Replay Workspace</h3>
        </div>
        <form class="form-grid" @submit.prevent="createSandbox">
          <label>
            Sandbox ID
            <input v-model="createForm.id" class="control code" placeholder="sandbox-alpha" />
          </label>
          <label>
            Name
            <input v-model="createForm.name" class="control" placeholder="BTC Morning Session" />
          </label>
          <label>
            Dataset
            <select v-model="createForm.dataset_id" class="control">
              <option value="">Select dataset</option>
              <option v-for="dataset in datasets.items" :key="dataset.id" :value="dataset.id">
                {{ dataset.name }} · {{ dataset.symbol }}
              </option>
            </select>
          </label>
          <label>
            Replay speed
            <input v-model.number="createForm.replay_speed" class="control" min="0.5" step="0.5" type="number" />
          </label>
          <label>
            Start datetime
            <input v-model="createForm.start_datetime" class="control" type="datetime-local" />
          </label>
          <label>
            Initial replay time
            <input v-model="createForm.replay_current_time" class="control" type="datetime-local" />
          </label>
          <div class="form-actions">
            <button class="btn primary" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create sandbox' }}</button>
            <span class="muted">New sandboxes start in `ready` and can be launched after account/token provisioning.</span>
          </div>
        </form>
        <ErrorState v-if="errorMessage" title="Sandbox action failed" :description="errorMessage" />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Ops Notes</p>
          <h3>Current Constraints</h3>
        </div>
        <ul class="notes-list muted">
          <li>Replay time and speed must be changed from dedicated replay control endpoints, not metadata patch.</li>
          <li>Dataset selection is mandatory for new sandboxes because replay market data is dataset-backed.</li>
          <li>Use Sandbox Detail after creation to create virtual accounts, issue tokens, and monitor trades.</li>
        </ul>
      </section>
    </div>

    <section v-if="filteredSandboxes.length" class="panel page-panel stack">
      <div class="page-header compact-header">
        <div>
          <p class="eyebrow">Inventory</p>
          <h3>Active Sandboxes</h3>
        </div>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>Sandbox</th>
              <th>Status</th>
              <th>Dataset</th>
              <th>Replay Time</th>
              <th>Speed</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="sandbox in filteredSandboxes" :key="sandbox.id">
              <td>
                <strong>{{ sandbox.name }}</strong>
                <div class="muted code">{{ sandbox.id }}</div>
              </td>
              <td><StatusBadge :label="sandbox.status" /></td>
              <td>{{ sandbox.dataset_id || 'Unbound' }}</td>
              <td class="metric">{{ formatDateTime(sandbox.replay_current_time) }}</td>
              <td class="metric">{{ formatNumber(sandbox.replay_speed, 1) }}x</td>
              <td>
                <div class="actions">
                  <button class="btn ghost" @click="openDetail(sandbox.id)">Open</button>
                  <button class="btn primary" @click="runAction(() => sandboxes.startSandbox(sandbox.id))">Start</button>
                  <button class="btn warn" @click="runAction(() => sandboxes.pauseSandbox(sandbox.id))">Pause</button>
                  <button class="btn ghost" @click="runAction(() => sandboxes.stopSandbox(sandbox.id))">Stop</button>
                  <button class="btn danger" @click="removeSandbox(sandbox.id)">Delete</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>
    <EmptyState v-else title="No sandboxes yet" description="Create the first replay sandbox to start the admin workflow." />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useDatasetsStore } from '@/stores/datasets'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatDateTime, formatNumber, fromLocalDateTimeInput, toLocalDateTimeInput } from '@/utils/formatters'

const router = useRouter()
const sandboxes = useSandboxesStore()
const datasets = useDatasetsStore()

const refreshing = ref(false)
const submitting = ref(false)
const errorMessage = ref('')
const search = ref('')
const createForm = reactive({
  id: '',
  name: '',
  dataset_id: '',
  replay_speed: 1,
  start_datetime: toLocalDateTimeInput(new Date().toISOString()),
  replay_current_time: toLocalDateTimeInput(new Date().toISOString()),
})

const filteredSandboxes = computed(() => {
  const keyword = search.value.trim().toLowerCase()
  if (!keyword) {
    return sandboxes.items
  }
  return sandboxes.items.filter((sandbox) =>
    `${sandbox.id} ${sandbox.name}`.toLowerCase().includes(keyword),
  )
})

const runningCount = computed(() => sandboxes.items.filter((item) => item.status === 'running').length)
const readyCount = computed(() => sandboxes.items.filter((item) => ['ready', 'paused'].includes(item.status)).length)

async function refresh() {
  refreshing.value = true
  errorMessage.value = ''
  try {
    await Promise.all([sandboxes.loadList(), datasets.loadList()])
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load admin data.'
  } finally {
    refreshing.value = false
  }
}

async function createSandbox() {
  submitting.value = true
  errorMessage.value = ''
  try {
    await sandboxes.createSandbox({
      id: createForm.id || undefined,
      name: createForm.name,
      dataset_id: createForm.dataset_id,
      replay_speed: createForm.replay_speed,
      start_datetime: fromLocalDateTimeInput(createForm.start_datetime),
      replay_current_time: fromLocalDateTimeInput(createForm.replay_current_time),
    })
    createForm.id = ''
    createForm.name = ''
    createForm.dataset_id = ''
    createForm.replay_speed = 1
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to create sandbox.'
  } finally {
    submitting.value = false
  }
}

async function runAction(action: () => Promise<unknown>) {
  errorMessage.value = ''
  try {
    await action()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Sandbox action failed.'
  }
}

async function removeSandbox(sandboxId: string) {
  if (!window.confirm(`Delete sandbox ${sandboxId}?`)) {
    return
  }
  await runAction(() => sandboxes.deleteSandbox(sandboxId))
}

function openDetail(sandboxId: string) {
  void router.push(`/admin/sandboxes/${sandboxId}`)
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
.header-tools {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
}
.header-tools .control {
  min-width: 280px;
}
.form-actions {
  grid-column: 1 / -1;
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
}
.notes-list {
  margin: 0;
  padding-left: 1.1rem;
  display: grid;
  gap: 0.75rem;
}
.compact-header {
  align-items: center;
}
</style>
