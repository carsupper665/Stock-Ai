<script setup>
import { computed, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { say } from '../../shared/toast.js'
import { listSessions, startSession, stopSession } from './api.js'
import Cards from './Cards.vue'
import NewSession from './NewSession.vue'
import Table from './Table.vue'

const router = useRouter()

const page = ref(1)
const includeDeleted = ref(false)
const { data, error, loading, reload } = useLoad(() => listSessions(page.value, includeDeleted.value).then((r) => r.sessions))
function changePage(delta) { page.value += delta; data.value = null; reload() }

// running 的 Session 每 3 秒輪詢 runs/current：200 有進行中的 Run、404 表示上一個 Run 已結束在等 Event
const now = ref(Date.now())
async function poll() {
  now.value = Date.now()
  if (!loading.value && data.value?.some((s) => s.status === 'running')) await reload()
}
const timer = setInterval(poll, 3000)
onUnmounted(() => clearInterval(timer))

const variant = ref(localStorage.getItem('variant') ?? 'bold')
function setVariant(v) {
  variant.value = v
  localStorage.setItem('variant', v)
}

const q = ref('')
const status = ref('all')
const sortKey = ref('last')
const sortDir = ref(-1)
function sortBy(key) {
  sortDir.value = sortKey.value === key ? -sortDir.value : -1
  sortKey.value = key
}

const rows = computed(() => {
  let list = (data.value ?? []).map((s) => {
    const last = s.last_run
    return {
      ...s,
      last,
      current: s.current_run,
      runCount: s.total_runs,
      tools: s.total_tool_calls,
      positions: s.trading?.positions,
      pnl: s.trading?.pnl?.net_realized ?? null,
      lastAt: last?.end_time ?? last?.start_time ?? null,
    }
  })
  if (status.value !== 'all') list = list.filter((s) => s.status === status.value)
  const needle = q.value.trim().toLowerCase()
  if (needle) list = list.filter((s) => `${s.name} ${s.id} ${s.model_name}`.toLowerCase().includes(needle))
  const pick = { name: (s) => s.name, runs: (s) => s.runCount ?? -1, pnl: (s) => s.pnl ?? -Infinity, last: (s) => s.lastAt ?? '' }[sortKey.value]
  list.sort((a, b) => (pick(a) < pick(b) ? sortDir.value : pick(a) > pick(b) ? -sortDir.value : 0))
  // 大膽版：running 一律置頂（穩定排序）
  if (variant.value === 'bold') {
    const rank = (s) => ({ running: 0, stopped: 1 }[s.status] ?? 2)
    list.sort((a, b) => rank(a) - rank(b))
  }
  return list
})

const open = (s) => router.push({ name: 'session-detail', params: { id: s.id } })

async function toggle(s) {
  try {
    if (s.status === 'running') {
      const ok = await confirm({
        title: `stop Session「${s.name}」？`,
        tone: 'warn',
        ok: '確認 stop',
        lines: [
          `這是 hard stop：${s.current ? `當前 Run #${s.current.run_id} 會變成 interrupted` : '沒有進行中的 Run，只是把狀態改回 stopped'}`,
          '所有 active Event 立即清空，不會自動重建',
          '進行中的模型／Tool 請求會被取消',
          '下次 start 會以 session_restart 開一個新 Run',
        ],
      })
      if (!ok) return
      const r = await stopSession(s.id)
      say(r.cancellation_pending ? `已送出 stop「${s.name}」，Run 取消中` : `已 stop「${s.name}」`)
    } else {
      const run = await startSession(s.id)
      say(`已 start「${s.name}」→ Run #${run.run_id}（trigger: ${run.trigger}）`)
    }
  } catch (e) {
    say(e.message, 'neg')
  }
  reload()
}

const creating = ref(false)
</script>

<template>
  <article>
    <Teleport to="#header-tools">
      <span class="chips">
        <button :aria-pressed="variant === 'steady'" @click="setVariant('steady')">穩健</button>
        <button :aria-pressed="variant === 'bold'" @click="setVariant('bold')">大膽</button>
      </span>
    </Teleport>

    <div class="row">
      <input v-model="q" placeholder="搜尋 name / id / model" class="search" />
      <span class="chips">
        <button v-for="v in ['all', 'running', 'stopped', 'deleted']" :key="v" :aria-pressed="status === v" @click="status = v">{{ v === 'all' ? '本頁全部' : v }}</button>
      </span>
      <button data-tone="acc" @click="creating = true">+ 建立 Session</button>
      <span class="grow"></span>
      <span class="mono" data-tone="mute">本頁篩選與排序 · {{ rows.length }} / {{ data?.length ?? 0 }}</span>
      <label class="row"><input v-model="includeDeleted" type="checkbox" @change="page = 1; data = null; reload()" />包含已刪除</label>
    </div>

    <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
    <p v-else-if="loading && !data" class="note">載入中…</p>
    <div v-else-if="data && !data.length" class="empty">還沒有 Session。先在「Provider」建好模型來源、在「交易帳號」建一個帳號，再回來「+ 建立 Session」。</div>
    <template v-else-if="data">
      <Cards v-if="variant === 'bold'" :rows="rows" :now="now" @open="open" @toggle="toggle" />
      <Table v-else :rows="rows" :now="now" :sort-key="sortKey" :sort-dir="sortDir" @open="open" @toggle="toggle" @sort="sortBy" />
      <p class="mono" data-tone="mute">統計來自 Server 全部 Runs；金融數字為帳戶範圍，— 表示資料未知。點列進明細。</p>
    </template>
    <div class="row"><button :disabled="page === 1 || loading" @click="changePage(-1)">上一頁</button><span>第 {{ page }} 頁 · 每頁 100</span><button :disabled="loading || !data || data.length < 100" @click="changePage(1)">下一頁</button></div>

    <NewSession v-if="creating" @close="creating = false" @created="(s) => open(s)" />
  </article>
</template>

<style scoped>
.search {
  width: 230px;
}
</style>
