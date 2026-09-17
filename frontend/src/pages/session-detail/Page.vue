<script setup>
import { computed, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { mmss, num, tone } from '../../shared/format.js'
import { activity } from '../../shared/run.js'
import { copy, say } from '../../shared/toast.js'
import NewSession from '../sessions/NewSession.vue'
import { currentRun, deleteSession, getSession, listRuns, startSession, stopSession } from './api.js'
import Memories from './Memories.vue'
import Monitor from './Monitor.vue'
import Overview from './Overview.vue'
import Runs from './Runs.vue'

const props = defineProps({ id: String })
const route = useRoute()
const router = useRouter()
const tab = computed(() => route.query.tab ?? 'overview')

// running 才輪詢 runs/current（每 3 秒）；404 表示上一個 Run 已結束、在等 Event
const now = ref(Date.now())

const page = Number(route.query.page ?? 1)
const { data, error, loading, reload } = useLoad(async () => {
  const session = await getSession(props.id)
  const [runs, current] = await Promise.all([
    tab.value === 'runs' ? listRuns(props.id, page).then((r) => r.runs) : [],
    session.status === 'running' ? currentRun(props.id).catch((e) => { if (e.status === 404) return null; throw e }) : null,
  ])
  return { session, runs, current, account: session.trading?.account }
})
const current = computed(() => data.value?.current)

async function poll() {
  now.value = Date.now()
  if (!loading.value && data.value?.session.status === 'running') await reload()
}
const timer = setInterval(poll, 3000)
onUnmounted(() => clearInterval(timer))

const s = computed(() => data.value?.session)
const go = (query) => router.push({ query: { ...route.query, ...query } })
const pct = (n, max) => `${Math.min(100, Math.round((n / max) * 100))}%`

async function act(fn) {
  try {
    await fn()
  } catch (e) {
    say(e.message, 'neg')
  }
  reload()
}
async function start() {
  await act(async () => {
    const run = await startSession(props.id)
    say(`已 start → Run #${run.run_id}（trigger: ${run.trigger}）`)
  })
}
async function stop() {
  const ok = await confirm({
    title: `stop Session「${s.value.name}」？`,
    tone: 'warn',
    ok: '確認 stop',
    lines: [
      `這是 hard stop：${current.value ? `當前 Run #${current.value.run_id} 會變成 interrupted` : '沒有進行中的 Run，只是把狀態改回 stopped'}`,
      '所有 active Event 立即清空，不會自動重建',
      '進行中的模型／Tool 請求會被取消',
      '下次 start 會以 session_restart 開一個新 Run',
    ],
  })
  if (ok) await act(async () => say((await stopSession(props.id)).cancellation_pending ? '已送出 stop，Run 取消中' : '已 stop'))
}
async function remove() {
  const ok = await confirm({
    title: `刪除 Session「${s.value.name}」？`,
    ok: '確認刪除',
    lines: ['soft delete：不可復原、不可 restore', 'Run／Memory／Ledger 資料保留供跨 Session 比較', s.value.status === 'running' ? '目前 running：會先 hard stop 再刪' : '刪除後不能 start'],
  })
  if (!ok) return
  try {
    await deleteSession(props.id)
    say('已刪除')
    router.push({ name: 'sessions' })
  } catch (e) {
    say(e.message, 'neg')
  }
}
const cloning = ref(false)
</script>

<template>
  <article>
    <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
    <p v-else-if="loading && !data" class="note">載入中…</p>
    <template v-else-if="s">
      <div class="row head">
        <button @click="router.push({ name: 'sessions' })">← Session 總覽</button>
        <h1>{{ s.name }}</h1>
        <span class="tag" :data-tone="tone(s.status)">{{ s.status }}</span>
        <button class="mono dashed" @click="copy(s.id)">{{ s.id }} ⧉</button>
        <span class="grow"></span>
        <template v-if="s.status !== 'deleted'">
          <button v-if="s.status === 'running'" data-tone="neg" @click="stop">stop</button>
          <button v-else data-tone="acc" @click="start">start</button>
        </template>
        <button @click="cloning = true">用這組設定建新的</button>
        <button v-if="s.status !== 'deleted'" data-tone="neg" @click="remove">刪除</button>
      </div>

      <section v-if="s.status === 'running'" class="card row band" data-tone="acc">
        <div class="col grow">
          <div class="eyebrow">當前 Run · 每 3 秒輪詢</div>
          <template v-if="current">
            <div class="row">
              <span class="mono run-id" data-tone="acc">#{{ current.run_id }}</span>
              <span>{{ activity(current, s.model_name) }}</span>
              <span class="mono" data-tone="mute">已跑 {{ mmss(Math.max(0, (now - new Date(current.start_time)) / 1000)) }}</span>
            </div>
            <div class="mono" data-tone="mute">trigger：{{ current.trigger }}</div>
          </template>
          <div v-else class="col">
            <b data-tone="warn">running · 沒有進行中的 Run</b>
            <span class="note">正在等待 Event；Active {{ s.active_event_count }}，Pending {{ s.pending_trigger_count }}。</span>
          </div>
        </div>
        <div class="col bars">
          <div>
            <div class="row between mono small" data-tone="mute"><span>decision / max_loop</span><span>{{ current ? current.decision_count : '—' }} / {{ s.max_loop }}</span></div>
            <div class="bar"><i data-tone="acc" :style="{ width: current ? pct(current.decision_count, s.max_loop) : 0 }"></i></div>
            <div v-if="current" class="mono small" data-tone="mute">模型呼叫 {{ current.model_call_count }} 次（含重試與終結嘗試）</div>
          </div>
          <div>
            <div class="row between mono small" data-tone="mute"><span>tool_call / max_tool_call</span><span>{{ current ? current.tool_call_count : '—' }} / {{ s.max_tool_call }}</span></div>
            <div class="bar"><i data-tone="warn" :style="{ width: current ? pct(current.tool_call_count, s.max_tool_call) : 0 }"></i></div>
          </div>
          <div class="mono small" data-tone="mute">超限 → 該 Run 直接 failed（A-09）</div>
        </div>
        <div class="stats tokens">
          <div v-for="k in ['input', 'output', 'cached', 'total']" :key="k" class="stat"><small>{{ k }}</small><b>{{ num(current?.[`${k}_tokens`]) }}</b></div>
          <span class="note wide">未知用量顯示 —；cached 已包含於 input。</span>
        </div>
      </section>
      <section v-else class="card row band" :data-tone="s.status === 'deleted' ? 'mute' : 'warn'">
        <div class="col grow">
          <div class="eyebrow">當前 Run</div>
          <b :data-tone="s.status === 'deleted' ? 'mute' : 'warn'">{{ s.status === 'deleted' ? '已刪除 · 不能 start' : '未執行 · 沒有進行中的 Run' }}</b>
          <span class="note">
            <template v-if="s.status === 'deleted'">資料保留供跨 Session 比較，但不可 restore、不可 start。</template>
            <template v-else>按 start 會以 trigger {{ s.current_run_id ? 'session_restart' : 'session_start' }} 開一個新 Run{{ s.restart_context ? '；Agent 會拿到上次中斷在哪（見概覽的重啟脈絡）' : '' }}。</template>
          </span>
        </div>
        <div class="col limits mono small" data-tone="mute">
          <div class="row between"><span>max_loop</span><span>— / {{ s.max_loop }}</span></div>
          <div class="row between"><span>max_tool_call</span><span>— / {{ s.max_tool_call }}</span></div>
          <span>沒有輪詢在跑（無進行中的 Run）</span>
        </div>
      </section>

      <div class="tabs">
        <button :aria-selected="tab === 'overview'" @click="go({ tab: 'overview' })">概覽</button>
        <button :aria-selected="tab === 'runs'" @click="go({ tab: 'runs' })">Run<small>{{ s.total_runs }}</small></button>
        <button :aria-selected="tab === 'memory'" @click="go({ tab: 'memory' })">Memory<small>{{ s.memory_summary?.active_count ?? '—' }}</small></button>
        <button :aria-selected="tab === 'monitor'" @click="go({ tab: 'monitor' })">監控</button>
      </div>

      <Overview v-if="tab === 'overview'" :session="s" :account="data.account" @changed="reload" @go-run="(run) => go({ tab: 'runs', run, page: undefined })" />
      <Runs v-else-if="tab === 'runs'" :session="s" :runs="data.runs" :page="page" :initial="route.query.run" @page="(p) => go({ page: p, run: undefined })" @retried="reload" />
      <Memories v-else-if="tab === 'memory'" :session="s" />
      <Monitor v-else :session="s" :account="data.account" :runs="data.runs" @go-run="(run) => go({ tab: 'runs', run })" />
    </template>

    <NewSession v-if="cloning" :preset="s" @close="cloning = false" @created="(c) => router.push({ name: 'session-detail', params: { id: c.id } })" />
  </article>
</template>

<style scoped>
.head {
  gap: 9px;
}
.band {
  gap: 16px;
  align-items: center;
}
.band > .grow {
  flex: 1 1 260px;
  gap: 6px;
}
.run-id {
  font-size: 20px;
  font-weight: 500;
  line-height: 1;
}
.bars {
  flex: 1 1 200px;
  gap: 8px;
}
.limits {
  flex: 0 1 220px;
  gap: 6px;
}
.between {
  justify-content: space-between;
}
.tokens {
  flex: 1 1 190px;
  grid-template-columns: 1fr 1fr;
}
.wide {
  grid-column: 1 / -1;
}
</style>
