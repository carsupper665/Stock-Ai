<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">回放資料集庫</p>
        <h2>資料集</h2>
        <p>建立 metadata、驗證匯入並將資料集綁定到沙盒，不顯示後端尚未支援的編輯/刪除操作。</p>
      </div>
      <div class="actions">
        <button class="btn ghost" :disabled="loading" @click="refresh">{{ loading ? '重新整理中…' : '重新整理' }}</button>
        <button class="btn primary" :disabled="!selectedDataset" @click="openImportWizard = true">匯入 CSV</button>
      </div>
    </header>

    <div class="kpi-grid">
      <SummaryCard label="資料集" :value="datasets.items.length" detail="後端已註冊的回放資料集。" />
      <SummaryCard label="已匯入列數" :value="totalRows" detail="所有資料集目前的 K 線列數。" />
      <SummaryCard label="已完成任務" :value="completedJobs" detail="最新匯入任務成功的資料集。" />
      <SummaryCard label="等待 / 執行中" :value="runningJobs" detail="仍在處理或等待完成的匯入。" />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">建立資料集</p>
          <h3>僅建立 Metadata</h3>
          <p class="muted">目前後端未提供編輯與刪除操作，所以介面刻意不顯示。</p>
        </div>
        <form class="form-grid" @submit.prevent="createDataset">
          <label>
            資料集 ID
            <input v-model="createForm.id" class="control code" placeholder="dts-btc-202501" />
          </label>
          <label>
            名稱
            <input v-model="createForm.name" class="control" placeholder="BTC 1m Jan 2025" />
          </label>
          <label>
            交易對
            <input v-model="createForm.symbol" class="control code" placeholder="BTCUSDT" />
          </label>
          <label>
            週期
            <input v-model="createForm.interval" class="control code" placeholder="1m" />
          </label>
          <label class="full-width">
            來源
            <input v-model="createForm.source" class="control" placeholder="csv-manual" />
          </label>
          <div class="form-actions">
            <button class="btn primary" :disabled="submitting">{{ submitting ? '建立中…' : '建立資料集' }}</button>
          </div>
        </form>
        <ErrorState v-if="errorMessage" title="資料集操作失敗" :description="errorMessage" />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">綁定資料集</p>
          <h3>沙盒附加</h3>
          <p class="muted">選擇沙盒，將目前選取的資料集套用為回放來源。</p>
        </div>
        <label>
          目標沙盒
          <select v-model="bindSandboxId" class="control">
            <option value="">選擇沙盒</option>
            <option v-for="sandbox in sandboxes.items" :key="sandbox.id" :value="sandbox.id">
              {{ sandbox.name }} · {{ sandbox.status }}
            </option>
          </select>
        </label>
        <div class="actions">
          <button class="btn ghost" :disabled="!selectedDataset || !bindSandboxId || binding" @click="bindSelectedDataset">
            {{ binding ? '綁定中…' : '綁定選取資料集' }}
          </button>
        </div>
        <EmptyState
          v-if="!selectedDataset"
          title="尚未選取資料集"
          description="從下方表格選取資料集，以查看匯入狀態並綁定到沙盒。"
        />
        <article v-else class="panel detail-card">
          <div class="page-header compact-header">
            <div>
              <strong>{{ selectedDataset.name }}</strong>
              <p class="muted code">{{ selectedDataset.id }}</p>
            </div>
            <StatusBadge :label="selectedDataset.latest_import_job?.status ?? 'draft'" />
          </div>
          <p class="muted">交易對 {{ selectedDataset.symbol }} · 週期 {{ selectedDataset.interval }} · 列數 {{ formatNumber(selectedDataset.row_count, 0) }}</p>
          <p class="muted">目前後端不支援 metadata 編輯。</p>
          <div class="stack">
            <div>
              <strong>最新匯入任務</strong>
              <p class="muted">
                {{ selectedDataset.latest_import_job?.file_name ?? '尚無匯入任務' }}
                <span v-if="selectedDataset.latest_import_job"> · {{ selectedDataset.latest_import_job?.status }}</span>
              </p>
              <p class="muted" v-if="selectedDataset.latest_import_job?.error_summary">{{ selectedDataset.latest_import_job?.error_summary }}</p>
            </div>
            <div>
              <strong>涵蓋範圍</strong>
              <p class="muted">{{ formatDateTime(selectedDataset.start_at) }} 至 {{ formatDateTime(selectedDataset.end_at) }}</p>
            </div>
          </div>
        </article>
      </section>
    </div>

    <section v-if="datasets.items.length" class="panel page-panel stack">
      <div>
        <p class="eyebrow">資料庫</p>
        <h3>已註冊資料集</h3>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>資料集</th>
              <th>交易對</th>
              <th>列數</th>
              <th>最新任務</th>
              <th>涵蓋範圍</th>
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
                <div class="muted">{{ dataset.latest_import_job?.file_name ?? '尚未匯入' }}</div>
              </td>
              <td class="metric">{{ formatDateTime(dataset.start_at) }}</td>
              <td>
                <div class="actions">
                  <button class="btn ghost" @click="selectDataset(dataset.id)">檢視</button>
                  <button class="btn primary" @click="openForImport(dataset.id)">匯入</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>
    <EmptyState v-else title="尚無資料集" description="先建立資料集 metadata，再匯入 CSV 讓它可被使用。" />

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
    errorMessage.value = error instanceof Error ? error.message : '無法載入資料集。'
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
    errorMessage.value = error instanceof Error ? error.message : '無法建立資料集。'
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
    errorMessage.value = error instanceof Error ? error.message : '無法載入資料集詳情。'
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
    errorMessage.value = error instanceof Error ? error.message : '無法匯入資料集。'
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
    errorMessage.value = error instanceof Error ? error.message : '無法綁定資料集。'
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
