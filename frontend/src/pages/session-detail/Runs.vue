<script setup>
import { computed, onUnmounted, ref, watch } from 'vue'
import { downloadHistory, getRun, retryRun } from './api.js'

import { duration, fmtClock, tone } from '../../shared/format.js'
import { explainError, steps } from '../../shared/run.js'
import { say } from '../../shared/toast.js'

const props = defineProps({ session: Object, runs: Array, page: Number, initial: String })
const emit = defineEmits(['page', 'retried'])

// 選中的 Run 只放本地狀態：放進 query 會讓整頁重建重抓（殼的 RouterView 以 fullPath 為 key）
const selected = ref(Number(props.initial) || props.runs.at(-1)?.run_id)
const loadedRun = ref(null)
const runError = ref('')
const run = computed(() => props.runs.find((r) => r.run_id === selected.value) ?? loadedRun.value)
let selectionRequest = 0
watch([selected, () => props.runs], async () => {
  const request = ++selectionRequest
  runError.value = ''
  if (!selected.value || props.runs.some((r) => r.run_id === selected.value)) { loadedRun.value = null; return }
  if (loadedRun.value?.run_id === selected.value) return
  loadedRun.value = null
  try { const result = await getRun(props.session.id, selected.value); if (request === selectionRequest) loadedRun.value = result }
  catch (e) { if (request === selectionRequest) runError.value = e.message }
}, { immediate: true })
const err = computed(() => explainError(run.value?.error))
const history = computed(() => steps(run.value))
const retrying = ref(false)
const retryReason = computed(() => {
  if (!run.value) return '尚未選擇 Run'
  if (run.value.status === 'running') return 'Run 仍在執行'
  if (run.value.status !== 'failed') return '只有 failed Run 可以重試'
  if (props.session.status !== 'running') return 'Session 必須是 running 且正在等待工作'
  if (run.value.run_id !== props.session.current_run_id) return '只能重試最新的 failed Run'
  if ((run.value.retry_attempt ?? 0) >= 3) return '已達 3 次人工重試上限'
  return ''
})
async function retry() {
  if (retryReason.value || retrying.value) return
  retrying.value = true
  try {
    const next = await retryRun(props.session.id, run.value.run_id)
    loadedRun.value = next
    selected.value = next.run_id
    say(`已建立 Retry Run #${next.run_id}（第 ${next.retry_attempt} 次）`)
    emit('retried', next)
  } catch (e) {
    say(e.message, 'neg')
  } finally {
    retrying.value = false
  }
}
const historyError = ref('')
const historyLoading = ref(false)
let historyRequest = 0
watch(selected, () => { historyRequest++; historyError.value = ''; historyLoading.value = false })
onUnmounted(() => historyRequest++)
const historyReason = computed(() => run.value?.status === 'running' ? 'Run 結束後才能下載 History 分片' : '')
function historyFilename(sessionID, runID) {
  const safeSession = String(sessionID).replace(/[^a-zA-Z0-9_-]+/g, '_').slice(0, 80) || 'session'
  const start = Math.floor((runID - 1) / 20) * 20 + 1
  return `history-${safeSession}-runs-${start}-${start + 19}.json`
}
async function saveHistory() {
  if (!run.value || historyLoading.value || historyReason.value) return
  const sessionID = props.session.id
  const runID = run.value.run_id
  const request = ++historyRequest
  historyError.value = ''
  historyLoading.value = true
  try {
    const blob = await downloadHistory(sessionID, runID)
    if (request !== historyRequest || selected.value !== runID) return
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    try {
      link.href = url
      link.download = historyFilename(sessionID, runID)
      document.body.append(link)
      link.click()
    } finally {
      link.remove()
      setTimeout(() => URL.revokeObjectURL(url), 0)
    }
  }
  catch (e) { if (request === historyRequest) historyError.value = e.message }
  finally { if (request === historyRequest) historyLoading.value = false }
}
// event Run 帶 triggers[]（最多 10 個）；其他 Run 只有一個 trigger 字串
const triggersOf = (r) => (Array.isArray(r.triggers) && r.triggers.length ? r.triggers : null)
const triggerLabel = (r) => triggersOf(r)?.map((t) => t.type).join(' · ') ?? r.trigger
const meanings = { session_start: '第一次 start', session_restart: 'stop 後再 start（帶 restart_context）', server_recovery: 'Server 重啟後恢復', manual_retry: '使用者對 failed Run 建立的新 Run', event: 'Event 觸發' }
function matchedText(t) {
  const m = t.matched ?? {}
  if (t.type === 'timer') return `at ${fmtClock(m.at)}`
  if (t.type === 'position_close') return [`position ${m.position_id}`, m.symbol, m.product, m.side, m.close_reason ? `reason ${m.close_reason}` : null].filter(Boolean).join(' · ')
  const parts = [m.condition, m.price != null ? `price ${m.price}` : null, m.change_pct != null ? `change ${m.change_pct.toFixed(2)}%` : null]
  return parts.filter(Boolean).join(' · ')
}
</script>

<template>
  <div class="run-layout">
    <div class="card flush list">
      <div class="list-scroll">
        <table>
          <thead>
            <tr><th>Run</th><th>Status</th><th>Trigger</th><th class="num">Dur</th><th class="num">Tool</th><th class="num">Decision</th><th class="num">Model attempts</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in runs" :key="r.run_id" data-click :data-active="r.run_id === selected ? '' : null" @click="selected = r.run_id">
              <td class="mono" data-tone="acc">#{{ r.run_id }}</td>
              <td><span class="tag" :data-tone="tone(r.status)">{{ r.status }}</span></td>
              <td class="mono small" data-tone="dim">{{ triggerLabel(r) }}</td>
              <td class="num" data-tone="dim">{{ duration(r.start_time, r.end_time) }}</td>
              <td class="num" data-tone="dim">{{ r.tool_call_count }}</td>
              <td class="num" data-tone="dim">{{ r.decision_count }}</td>
              <td class="num" data-tone="dim">{{ r.model_call_count }}</td>
            </tr>
            <tr v-if="!runs.length"><td colspan="7" class="note">這一頁沒有 Run</td></tr>
          </tbody>
        </table>
      </div>
      <div class="row pager mono" data-tone="mute">
        <span>page {{ page }} · 每頁 100 · run_id 遞增即時間順序</span>
        <span class="grow"></span>
        <button :disabled="page <= 1" @click="$emit('page', page - 1)">‹ 上一頁</button>
        <button :disabled="runs.length < 100" @click="$emit('page', page + 1)">下一頁 ›</button>
      </div>
    </div>

    <section v-if="run" class="card col detail">
      <div class="row run-actions">
        <span class="mono title" data-tone="acc">#{{ run.run_id }}</span>
        <span class="tag" :data-tone="tone(run.status)">{{ run.status }}</span>
        <span class="mono" data-tone="mute">{{ fmtClock(run.start_time) }} → {{ run.end_time ? fmtClock(run.end_time) : '進行中' }}</span>
        <span v-if="run.retry_attempt" class="tag outline" data-tone="warn">Retry {{ run.retry_attempt }} / 3 · source #{{ run.retry_of_run_id }}</span>
        <span class="grow"></span>
        <button :disabled="historyLoading || !!historyReason" :title="historyReason" @click="saveHistory">{{ historyLoading ? '下載中…' : '下載 History 分片' }}</button>
        <button data-tone="warn" :disabled="retrying || !!retryReason" :title="retryReason" @click="retry">{{ retrying ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <p v-if="historyReason" class="note">{{ historyReason }}</p>
      <p v-if="historyError" role="alert" data-tone="neg">History 下載失敗：{{ historyError }}</p>
      <p class="note">{{ retryReason || 'Retry 會建立新 Run；不重送舊 Tool call，交易前必須重新查核帳戶與 Ledger。' }}</p>

      <div v-if="err" class="callout" data-tone="neg">
        <div class="mono" data-tone="neg">{{ err.code }}</div>
        <div>{{ err.message }}</div>
        <div v-if="err.hint" class="note">{{ err.hint }}</div>
      </div>

      <div>
        <div class="eyebrow">Triggers（一個 Run 最多 10 個）</div>
        <div v-if="triggersOf(run)" class="rows">
          <div v-for="(t, i) in triggersOf(run)" :key="i" class="row trigger">
            <span class="mono type" data-tone="acc">{{ t.type }}</span>
            <span class="mono grow" data-tone="dim">{{ matchedText(t) }} · 由 Run #{{ t.created_run_id }} 建立 · {{ fmtClock(t.fired_at) }}</span>
          </div>
        </div>
        <div v-else class="row trigger">
          <span class="mono type" data-tone="acc">{{ run.trigger }}</span>
          <span class="mono" data-tone="dim">{{ meanings[run.trigger] ?? '' }}</span>
        </div>
      </div>

      <div class="col">
        <div class="eyebrow">執行進度與歷史</div>
        <ol v-if="history.length" class="timeline">
          <li v-for="h in history" :key="h.key">
            <details v-if="h.kind === 'tool'" class="tool-result">
              <summary>
                <span class="mono" data-tone="mute">{{ h.step }}</span>
                <span class="step-title" :data-tone="h.tone">{{ h.title }}</span>
                <span class="mono small" data-tone="mute">{{ h.meta }}</span>
              </summary>
              <dl class="kv result-fields">
                <template v-for="(field, index) in h.fields" :key="index"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template>
              </dl>
              <span v-if="h.filtered" class="tag outline" data-tone="warn">工具結果已過濾 — 缺的欄位不是 bug</span>
            </details>
            <template v-else>
              <div class="row">
                <span class="mono" data-tone="mute">{{ h.step }}</span>
                <span class="step-title">{{ h.title }}</span>
                <span class="mono small" data-tone="mute">{{ h.meta }}</span>
                <span class="tag outline" :data-tone="h.tone">{{ h.status }}</span>
              </div>
              <div v-if="h.error" class="callout" data-tone="neg"><span class="mono">{{ h.error.code }}</span> · {{ h.error.message }}</div>
              <details v-if="h.reasoning" class="inset trace-text model-text"><summary>模型 reasoning · {{ h.reasoning.length }} 字元</summary><pre>{{ h.reasoning }}</pre></details>
              <details v-if="h.content" class="inset trace-text model-text"><summary>模型內容 · {{ h.content.length }} 字元</summary><pre>{{ h.content }}</pre></details>
              <dl v-if="h.envelope" class="kv result-fields"><template v-for="(field, index) in h.envelope" :key="index"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl>
              <dl v-if="h.diagnostics.length" class="kv result-fields"><template v-for="field in h.diagnostics" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl>
              <p v-if="h.tools.length" class="note">要求呼叫：{{ h.tools.join('、') }}（呼叫參數不在 UI 顯示）</p>
              <p v-if="!h.reasoning && !h.content && !h.envelope && !h.tools.length" class="note">這次模型回應沒有可顯示的內容。</p>
            </template>
          </li>
        </ol>
        <p v-else class="note">還沒有任何模型／工具呼叫。</p>
        <details v-if="run.output" class="inset final-output"><summary>最終輸出 · {{ run.output.length }} 字元</summary><pre class="body">{{ run.output }}</pre></details>
        <p class="note">只顯示 Provider 實際回傳並由 Server 保存的模型內容；不推測或補造 hidden chain-of-thought。Tool 呼叫參數不在 UI 顯示。</p>
      </div>
    </section>
    <div v-else class="card detail note" :role="runError ? 'alert' : null">{{ runError || (selected ? '載入選定 Run…' : '左邊選一個 Run 看明細') }}</div>
  </div>
</template>

<style scoped>
.run-layout {
  display: grid;
  grid-template-columns: minmax(420px, 0.9fr) minmax(500px, 1.3fr);
  gap: 12px;
  align-items: start;
}
.list {
  min-width: 0;
  overflow: hidden;
}
.list-scroll {
  max-height: min(70vh, 720px);
  overflow: auto;
}
.list table {
  min-width: 520px;
}
.list thead {
  position: sticky;
  top: 0;
  z-index: 1;
}
.pager {
  padding: 7px 10px;
}
.detail {
  min-width: 0;
  gap: 11px;
}
.run-actions {
  position: sticky;
  top: 8px;
  z-index: 2;
  background: var(--surf);
  border-bottom: 1px solid var(--line);
  padding-bottom: 8px;
}
.title {
  font-size: 15px;
  font-weight: 500;
}
.trigger {
  padding: 4px 0;
}
.type {
  width: 132px;
  flex: none;
}
.step-title {
  font-size: 12.5px;
  font-weight: 500;
}
.body {
  font-size: 12px;
  margin-top: 2px;
}
.tool-result summary {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.timeline {
  max-height: min(64vh, 680px);
  overflow: auto;
  padding-right: 6px;
}
.result-fields {
  margin-top: 8px;
  max-height: 46vh;
  overflow: auto;
}
.trace-text {
  margin-top: 6px;
}
.trace-text summary,
.final-output summary {
  cursor: pointer;
  color: var(--dim);
  font-size: 11.5px;
}
.trace-text pre,
.final-output pre {
  max-height: 46vh;
  overflow: auto;
  margin-top: 8px;
}
@media (max-width: 1100px) {
  .run-layout {
    grid-template-columns: 1fr;
  }
}
</style>
