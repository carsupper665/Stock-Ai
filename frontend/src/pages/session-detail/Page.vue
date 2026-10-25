<script setup>
import { computed, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { tone } from '../../shared/format.js'
import { copy, say } from '../../shared/toast.js'
import NewSession from '../sessions/NewSession.vue'
import { currentRun, deleteSession, getSession, listRuns, startSession, stopSession } from './api.js'
import CurrentRun from './CurrentRun.vue'
import Conversation from './Conversation.vue'
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
    <!-- 輪詢失敗只加一行錯誤，上一次的內容留著：卸載會把選中的 Run、展開狀態和捲動位置全部重置 -->
    <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
    <p v-if="loading && !data" class="note">載入中…</p>
    <template v-if="s">
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

      <CurrentRun v-if="tab !== 'runs'" :session="s" :current="current" :now="now" />

      <div class="tabs">
        <button :aria-selected="tab === 'overview'" @click="go({ tab: 'overview' })">概覽</button>
        <button :aria-selected="tab === 'runs'" @click="go({ tab: 'runs' })">Run<small>{{ s.total_runs }}</small></button>
        <button :aria-selected="tab === 'conversation'" @click="go({ tab: 'conversation' })">對話</button>
        <button :aria-selected="tab === 'memory'" @click="go({ tab: 'memory' })">Memory<small>{{ s.memory_summary?.count ?? '—' }}</small></button>
        <button :aria-selected="tab === 'monitor'" @click="go({ tab: 'monitor' })">監控</button>
      </div>

      <Overview v-if="tab === 'overview'" :session="s" :account="data.account" @changed="reload" @go-run="(run) => go({ tab: 'runs', run, page: undefined })" />
      <Runs v-else-if="tab === 'runs'" :session="s" :runs="data.runs" :page="page" :initial="route.query.run" @page="(p) => go({ page: p, run: undefined })" @retried="reload" @ended="reload">
        <template #current><CurrentRun :session="s" :current="current" :now="now" compact /></template>
      </Runs>
      <Conversation v-else-if="tab === 'conversation'" :session="s" />
      <Memories v-else-if="tab === 'memory'" :session="s" />
      <Monitor v-else :session="s" />
    </template>

    <NewSession v-if="cloning" :preset="s" @close="cloning = false" @created="(c) => router.push({ name: 'session-detail', params: { id: c.id } })" />
  </article>
</template>

<style scoped>
.head {
  gap: 9px;
}
</style>
