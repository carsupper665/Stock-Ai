<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">Replay Dataset Library</p>
        <h2>Datasets</h2>
        <p>Create metadata, validate imports, and bind datasets to sandboxes without exposing unsupported edit/delete actions.</p>
      </div>
      <div class="actions">
        <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
        <button class="btn primary" :disabled="!selectedDataset" @click="openImportWizard = true">Import CSV</button>
      </div>
    </header>

    <div class="kpi-grid">
      <SummaryCard label="Datasets" :value="datasets.items.length" detail="Replay datasets registered in the backend." />
      <SummaryCard label="Rows imported" :value="totalRows" detail="Current kline rows across all datasets." />
      <SummaryCard label="Completed jobs" :value="completedJobs" detail="Datasets with a successful latest import job." />
      <SummaryCard label="Pending / running" :value="runningJobs" detail="Imports still being processed or awaiting completion." />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Create Dataset</p>
          <h3>Metadata Only</h3>
          <p class="muted">Edit and delete are intentionally not shown because the current backend does not expose those operations.</p>
        </div>
        <form class="form-grid" @submit.prevent="createDataset">
          <label>
            Dataset ID
            <input v-model="createForm.id" class="control code" placeholder="dts-btc-202501" />
          </label>
          <label>
            Name
            <input v-model="createForm.name" class="control" placeholder="BTC 1m Jan 2025" />
          </label>
          <label>
            Symbol
            <input v-model="createForm.symbol" class="control code" placeholder="BTCUSDT" />
          </label>
          <label>
            Interval
            <input v-model="createForm.interval" class="control code" placeholder="1m" />
          </label>
          <label class="full-width">
            Source
            <input v-model="createForm.source" class="control" placeholder="csv-manual" />
          </label>
          <div class="form-actions">
            <button class="btn primary" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create dataset' }}</button>
          </div>
        </form>
        <ErrorState v-if="errorMessage" title="Dataset action failed" :description="errorMessage" />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">Bind Dataset</p>
          <h3>Sandbox Attachment</h3>
          <p class="muted">Select a sandbox and apply the currently selected dataset as its replay source.</p>
        </div>
        <label>
          Target sandbox
          <select v-model="bindSandboxId" class="control">
            <option value="">Select sandbox</option>
            <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">
              {{ sandbox.name }} · {{ sandbox.status }}
            </option>
          </select>
        </label>
        <div class="actions">
          <button class="btn ghost" :disabled="!selectedDataset || !bindSandboxId || binding" @click="bindSelectedDataset">
            {{ binding ? 'Binding…' : 'Bind selected dataset' }}
          </button>
        </div>
        <EmptyState
          v-if="!selectedDataset"
          title="No dataset selected"
          description="Pick a dataset from the table below to inspect its import status and bind it to a sandbox."
        />
        <article v-else class="panel detail-card">
          <div class="page-header compact-header">
            <div>
              <strong>{{ selectedDataset.name }}</strong>
              <p class="muted code">{{ selectedDataset.id }}</p>
            </div>
            <StatusBadge :label="selectedDataset.latest_import_job?.status ?? 'draft'" />
          </div>
          <p class="muted">Symbol {{ selectedDataset.symbol }} · Interval {{ selectedDataset.interval }} · Rows {{ formatNumber(selectedDataset.row_count, 0) }}</p>
          <p class="muted">Metadata edit unavailable in current backend.</p>
          <div class="stack">
            <div>
              <strong>Latest import job</strong>
              <p class="muted">
                {{ selectedDataset.latest_import_job?.file_name ?? 'No import job yet' }}
                <span v-if="selectedDataset.latest_import_job"> · {{ selectedDataset.latest_import_job?.status }}</span>
              </p>
              <p class="muted" v-if="selectedDataset.latest_import_job?.error_summary">{{ selectedDataset.latest_import_job?.error_summary }}</p>
            </div>
            <div>
              <strong>Coverage</strong>
              <p class="muted">{{ formatDateTime(selectedDataset.start_at) }} to {{ formatDateTime(selectedDataset.end_at) }}</p>
            </div>
          </div>
        </article>
      </section>
    </div>

    <section v-if="datasets.items.length" class="panel page-panel stack">
      <div>
        <p class="eyebrow">Library</p>
        <h3>Registered Datasets</h3>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>Dataset</th>
              <th>Symbol</th>
              <th>Rows</th>
              <th>Latest Job</th>
              <th>Coverage</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="dataset in datasets.items" :key="dataset.id">
              <td>
                <strong>{{ dataset.name }}</strong>
                <div class="muted code">{{ dataset.id }}</div>
              </td>
              <td>{{ dataset.symbol }} · {{ dataset.interval }}</td>
              <td class="metric">{{ formatNumber(dataset.row_count, 0) }}</td>
              <td>
                <StatusBadge :label="dataset.latest_import_job?.status ?? 'draft'" />
                <div class="muted">{{ dataset.latest_import_job?.file_name ?? 'No import yet' }}</div>
              </td>
              <td class="metric">{{ formatDateTime(dataset.start_at) }}</td>
              <td>
                <div class="actions">
                  <button class="btn ghost" @click="selectDataset(dataset.id)">Inspect</button>
                  <button class="btn primary" @click="openForImport(dataset.id)">Import</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>
    <EmptyState v-else title="No datasets yet" description="Create a dataset metadata record, then import a CSV to make it usable." />

    <DatasetUploadWizard
      :open="openImportWizard"
      :loading="importing"
      @close="openImportWizard = false"
      @submit="submitImport"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import DatasetUploadWizard from '@/components/common/DatasetUploadWizard.vue'
import DenseTable from '@/components/common/DenseTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import SummaryCard from '@/components/common/SummaryCard.vue'
import { useDatasetsStore } from '@/stores/datasets'
import { useSandboxesStore } from '@/stores/sandboxes'
import { formatDateTime, formatNumber } from '@/utils/formatters'

const datasets = useDatasetsStore()
const sandboxes = useSandboxesStore()

const loading = ref(false)
const submitting = ref(false)
const importing = ref(false)
const binding = ref(false)
const errorMessage = ref('')
const selectedDatasetId = ref('')
const bindSandboxId = ref('')
const openImportWizard = ref(false)
const createForm = reactive({
  id: '',
  name: '',
  symbol: 'BTCUSDT',
  interval: '1m',
  source: 'csv-manual',
})

const selectedDataset = computed(() => datasets.items.find((item) => item.id === selectedDatasetId.value) ?? datasets.selected)
const totalRows = computed(() => datasets.items.reduce((sum, item) => sum + item.row_count, 0))
const completedJobs = computed(() => datasets.items.filter((item) => item.latest_import_job?.status === 'completed').length)
const runningJobs = computed(() => datasets.items.filter((item) => ['running', 'pending'].includes(item.latest_import_job?.status ?? '')).length)

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    await Promise.all([datasets.loadList(), sandboxes.loadList()])
    if (!selectedDatasetId.value && datasets.items.length) {
      selectedDatasetId.value = datasets.items[0].id
      await datasets.loadOne(selectedDatasetId.value)
    }
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load datasets.'
  } finally {
    loading.value = false
  }
}

async function createDataset() {
  submitting.value = true
  errorMessage.value = ''
  try {
    const created = await datasets.createDataset({
      id: createForm.id || undefined,
      name: createForm.name,
      symbol: createForm.symbol,
      interval: createForm.interval,
      source: createForm.source,
    })
    createForm.id = ''
    createForm.name = ''
    selectedDatasetId.value = created.id
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to create dataset.'
  } finally {
    submitting.value = false
  }
}

async function selectDataset(datasetId: string) {
  selectedDatasetId.value = datasetId
  errorMessage.value = ''
  try {
    await datasets.loadOne(datasetId)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to load dataset details.'
  }
}

function openForImport(datasetId: string) {
  void selectDataset(datasetId)
  openImportWizard.value = true
}

async function submitImport(file: File) {
  if (!selectedDatasetId.value) {
    return
  }
  importing.value = true
  errorMessage.value = ''
  try {
    await datasets.importDataset(selectedDatasetId.value, file)
    await refresh()
    openImportWizard.value = false
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to import dataset.'
  } finally {
    importing.value = false
  }
}

async function bindSelectedDataset() {
  if (!selectedDataset.value || !bindSandboxId.value) {
    return
  }
  binding.value = true
  errorMessage.value = ''
  try {
    await sandboxes.updateSandbox(bindSandboxId.value, { dataset_id: selectedDataset.value.id })
    await sandboxes.loadSnapshot(bindSandboxId.value)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to bind dataset.'
  } finally {
    binding.value = false
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
.detail-card {
  padding: 1rem;
}
.compact-header {
  align-items: center;
}
</style>
