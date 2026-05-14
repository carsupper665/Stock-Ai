<template>
  <section class="page">
    <header class="page-header panel page-panel">
      <div>
        <p class="eyebrow">管理控制台</p>
        <h2>沙盒清單</h2>
        <p>在同一個介面建立回放沙盒、綁定資料集並控制生命週期。</p>
      </div>
      <div class="header-tools">
        <input v-model="search" class="control" placeholder="依沙盒 ID 或名稱搜尋" />
        <button class="btn ghost" :disabled="refreshing" @click="refresh">{{ refreshing ? '重新整理中…' : '重新整理' }}</button>
      </div>
    </header>

    <div class="kpi-grid">
      <SummaryCard label="沙盒總數" :value="sandboxes.items.length" detail="後端目前記錄的全部回放環境。" />
      <SummaryCard label="運行中" :value="runningCount" detail="正在推進回放時間的沙盒。" />
      <SummaryCard label="就緒 / 暫停" :value="readyCount" detail="可啟動或恢復的工作區。" />
      <SummaryCard label="可用資料集" :value="datasets.items.length" detail="建立新沙盒時可選用的回放資料集。" />
    </div>

    <div class="two-column">
      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">建立沙盒</p>
          <h3>新增回放工作區</h3>
        </div>
        <form class="form-grid" @submit.prevent="createSandbox">
          <label>
            沙盒 ID
            <input v-model="createForm.id" class="control code" placeholder="sandbox-alpha" />
          </label>
          <label>
            名稱
            <input v-model="createForm.name" class="control" placeholder="BTC Morning Session" />
          </label>
          <label>
            資料集
            <select v-model="createForm.dataset_id" class="control">
              <option value="">選擇資料集</option>
              <option v-for="dataset in datasets.items" :key="dataset.id" :value="dataset.id">
                {{ dataset.name }} · {{ dataset.symbol }}
              </option>
            </select>
          </label>
          <label>
            回放速度
            <input v-model.number="createForm.replay_speed" class="control" min="0.5" step="0.5" type="number" />
          </label>
          <label>
            開始時間
            <input v-model="createForm.start_datetime" class="control" type="datetime-local" />
          </label>
          <label>
            初始回放時間
            <input v-model="createForm.replay_current_time" class="control" type="datetime-local" />
          </label>
          <div class="form-actions">
            <button class="btn primary" :disabled="submitting">{{ submitting ? '建立中…' : '建立沙盒' }}</button>
            <span class="muted">新沙盒會以 `ready` 狀態建立，完成帳戶與權杖配置後即可啟動。</span>
          </div>
        </form>
        <ErrorState v-if="errorMessage" title="沙盒操作失敗" :description="errorMessage" />
      </section>

      <section class="panel page-panel stack">
        <div>
          <p class="eyebrow">操作備註</p>
          <h3>目前限制</h3>
        </div>
        <ul class="notes-list muted">
          <li>回放時間與速度必須透過專用回放控制端點修改，不能用 metadata patch。</li>
          <li>新沙盒必須選擇資料集，因為回放市場資料由資料集提供。</li>
          <li>建立後請到沙盒詳情建立虛擬帳戶、發行權杖並監控交易。</li>
        </ul>
      </section>
    </div>

    <section v-if="filteredSandboxes.length" class="panel page-panel stack">
      <div class="page-header compact-header">
        <div>
          <p class="eyebrow">清冊</p>
          <h3>作用中沙盒</h3>
        </div>
      </div>
      <DenseTable>
        <table>
          <thead>
            <tr>
              <th>沙盒</th>
              <th>狀態</th>
              <th>資料集</th>
              <th>回放時間</th>
              <th>速度</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="sandbox in filteredSandboxes" :key="sandbox.id">
              <td>
                <strong>{{ sandbox.name }}</strong>
                <div class="muted code">{{ sandbox.id }}</div>
              </td>
              <td><StatusBadge :label="sandbox.status" /></td>
              <td>{{ sandbox.dataset_id || '未綁定' }}</td>
              <td class="metric">{{ formatDateTime(sandbox.replay_current_time) }}</td>
              <td class="metric">{{ formatNumber(sandbox.replay_speed, 1) }}x</td>
              <td>
                <div class="actions">
                  <button class="btn ghost" @click="openDetail(sandbox.id)">開啟</button>
                  <button class="btn primary" @click="runAction(() => sandboxes.startSandbox(sandbox.id))">啟動</button>
                  <button class="btn warn" @click="runAction(() => sandboxes.pauseSandbox(sandbox.id))">暫停</button>
                  <button class="btn ghost" @click="runAction(() => sandboxes.stopSandbox(sandbox.id))">停止</button>
                  <button class="btn danger" @click="removeSandbox(sandbox.id)">刪除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </DenseTable>
    </section>
    <EmptyState v-else title="尚無沙盒" description="建立第一個回放沙盒以開始管理流程。" />
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
    errorMessage.value = error instanceof Error ? error.message : '無法載入管理資料。'
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
    errorMessage.value = error instanceof Error ? error.message : '無法建立沙盒。'
  } finally {
    submitting.value = false
  }
}

async function runAction(action: () => Promise<unknown>) {
  errorMessage.value = ''
  try {
    await action()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '沙盒操作失敗。'
  }
}

async function removeSandbox(sandboxId: string) {
  if (!window.confirm(`要刪除沙盒 ${sandboxId} 嗎？`)) {
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
