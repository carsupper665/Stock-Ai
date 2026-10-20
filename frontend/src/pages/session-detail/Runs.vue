<script setup>
import { computed, nextTick, onUnmounted, reactive, ref, watch } from 'vue'
import { downloadHistory, getRun, retryRun, runStreamURL } from './api.js'

import { duration, fmtClock, tone } from '../../shared/format.js'
import { conversation, explainError } from '../../shared/run.js'
import { say } from '../../shared/toast.js'

const props = defineProps({ session: Object, runs: Array, page: Number, initial: String })
const emit = defineEmits(['page', 'retried', 'ended'])

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
  // 不在這一頁的 Run 跑完就不再變；還在跑就跟著每次輪詢重抓，舊畫面留著直到新的到
  if (loadedRun.value?.run_id === selected.value) { if (loadedRun.value.status !== 'running') return }
  else loadedRun.value = null
  try { const result = await getRun(props.session.id, selected.value); if (request === selectionRequest) loadedRun.value = result }
  catch (e) { if (request === selectionRequest) runError.value = e.message }
}, { immediate: true })
// 進行中的 Run 用 SSE 收模型正在吐的字（Agent Server 只放在記憶體）；工具、狀態等結構照舊靠輪詢。
// Run 結束時通知頁面立刻重抓，最終內容以 progress 為準。
const live = reactive({ calls: {}, current: null })
let stream = null
let streamedRun = null
function closeStream() {
  stream?.close()
  stream = null
  streamedRun = null
}
watch(() => (run.value?.status === 'running' ? run.value.run_id : null), (runID) => {
  if (runID === streamedRun) return
  closeStream()
  live.calls = {}
  live.current = null
  if (runID == null || typeof EventSource === 'undefined') return
  streamedRun = runID
  stream = new EventSource(runStreamURL(props.session.id, runID))
  stream.addEventListener('call', (e) => {
    const { index } = JSON.parse(e.data)
    live.current = index
    live.calls[index] = { content: '', reasoning: '' }
  })
  stream.addEventListener('delta', (e) => {
    const delta = JSON.parse(e.data)
    const call = live.calls[live.current]
    if (!call) return
    call.content += delta.content ?? ''
    call.reasoning += delta.reasoning ?? ''
  })
  stream.addEventListener('end', () => {
    closeStream()
    emit('ended')
  })
}, { immediate: true })
onUnmounted(closeStream)
const err = computed(() => explainError(run.value?.error))
const history = computed(() => conversation(run.value, live))
// 有新內容時，本來停在底部才跟著捲；輪詢帶回同樣的內容不動捲動位置。
// 「在不在底部」在 DOM 更新前當場量：展開／收合 <details> 不會觸發 scroll 事件，事先記的值會過期。
const chat = ref(null)
const contentKey = computed(() => {
  const progress = run.value?.progress ?? []
  const last = progress.at(-1)
  const streaming = live.calls[live.current]
  return [run.value?.run_id, progress.length, last?.content?.length ?? 0, last?.reasoning?.length ?? 0, last?.ended_at ?? '',
    streaming?.content.length ?? 0, streaming?.reasoning.length ?? 0].join('|')
})
let shownRun = null
watch(contentKey, async () => {
  const el = chat.value
  const switched = run.value?.run_id !== shownRun
  shownRun = run.value?.run_id
  const atBottom = switched || !el || el.scrollHeight - el.scrollTop - el.clientHeight < 80
  await nextTick()
  if (atBottom && chat.value) chat.value.scrollTop = chat.value.scrollHeight
}, { immediate: true })
const firstLine = (text) => text.split('\n').find((line) => line.trim())?.trim() ?? ''
// 串流中收合的思考過程顯示最新一行，看得出模型還在動
const lastLine = (text) => text.split('\n').findLast((line) => line.trim())?.trim() ?? ''
const icons = { ok: '✓', error: '✗', denied: '⊘' }
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
const meanings = { session_start: '第一次啟動', session_restart: '停止後重新啟動', server_recovery: 'Server 重啟後恢復', manual_retry: '使用者對失敗的 Run 重試', event: 'Event 觸發' }
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
        <span>第 {{ page }} 頁</span>
        <span class="grow"></span>
        <button :disabled="page <= 1" @click="$emit('page', page - 1)">‹ 上一頁</button>
        <button :disabled="runs.length < 100" @click="$emit('page', page + 1)">下一頁 ›</button>
      </div>
    </div>

    <section v-if="run" class="card col detail">
      <div class="row run-actions">
        <span class="mono title" data-tone="acc">#{{ run.run_id }}</span>
        <span class="tag" :data-tone="tone(run.status)">{{ run.status }}</span>
        <span class="mono" data-tone="mute">{{ fmtClock(run.start_time) }} → {{ run.end_time ? fmtClock(run.end_time) : '進行中' }} · {{ run.model_call_count ?? 0 }} 次模型呼叫 · {{ run.tool_call_count ?? 0 }} 次工具</span>
        <span v-if="run.retry_attempt" class="tag outline" data-tone="warn">Retry {{ run.retry_attempt }} / 3 · source #{{ run.retry_of_run_id }}</span>
        <span class="grow"></span>
        <button :disabled="historyLoading || !!historyReason" :title="historyReason" @click="saveHistory">{{ historyLoading ? '下載中…' : '下載 History 分片' }}</button>
        <button data-tone="warn" :disabled="retrying || !!retryReason" :title="retryReason" @click="retry">{{ retrying ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <p v-if="historyError" role="alert" data-tone="neg">History 下載失敗：{{ historyError }}</p>
      <p v-if="run.status === 'failed'" class="note">Retry 會建立新的 Run，不會重送舊的工具呼叫。</p>
      <div v-if="err" class="callout" data-tone="neg">
        <div class="mono" data-tone="neg">{{ err.code }}</div>
        <div>{{ err.message }}</div>
        <div v-if="err.hint" class="note">{{ err.hint }}</div>
      </div>

      <div ref="chat" class="chat">
        <div class="wake">
          <span class="eyebrow">喚醒原因</span>
          <template v-if="triggersOf(run)">
            <div v-for="(t, i) in triggersOf(run)" :key="i" class="row trigger">
              <span class="mono type" data-tone="acc">{{ t.type }}</span>
              <span class="mono grow" data-tone="dim">{{ matchedText(t) }} · 由 Run #{{ t.created_run_id }} 建立 · {{ fmtClock(t.fired_at) }}</span>
            </div>
          </template>
          <div v-else class="row trigger">
            <span class="mono type" data-tone="acc">{{ run.trigger }}</span>
            <span data-tone="dim">{{ meanings[run.trigger] ?? '' }}</span>
          </div>
        </div>

        <template v-for="item in history" :key="item.key">
          <p v-if="item.kind === 'notice'" class="notice" :data-tone="item.tone">{{ item.text }}</p>
          <article v-else class="message">
            <div class="who"><span class="avatar" aria-hidden="true">AI</span><b>{{ item.title }}</b><span data-tone="mute">{{ item.meta }}</span></div>
            <div v-if="item.error" class="callout" data-tone="neg"><span class="mono">{{ item.error.code }}</span> · {{ item.error.message }}</div>
            <details v-if="item.reasoning" class="reasoning">
              <summary><span class="label">思考過程</span><span class="preview">{{ item.thinking ? lastLine(item.reasoning) : firstLine(item.reasoning) }}</span></summary>
              <div class="text">{{ item.reasoning }}</div>
            </details>
            <div v-if="item.content" class="text">{{ item.content }}</div>
            <div v-if="item.envelope" class="final">
              <span class="eyebrow">最終決策</span>
              <dl class="kv"><template v-for="(field, index) in item.envelope" :key="index"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl>
            </div>
            <details v-for="tool in item.tools" :key="tool.key" class="tool" :data-status="tool.status" :data-action="tool.action ? '' : null">
              <summary>
                <span v-if="tool.status === 'running'" class="spinner" aria-label="執行中"></span>
                <span v-else class="icon" aria-hidden="true">{{ icons[tool.status] }}</span>
                <b>{{ tool.label }}</b>
                <span class="mono" data-tone="mute">{{ tool.name }}</span>
                <span class="grow"></span>
                <span v-if="tool.duration != null" class="mono small" data-tone="mute">{{ tool.duration }} ms</span>
              </summary>
              <div class="tool-body">
                <div v-if="tool.args.length"><span class="eyebrow">參數</span><dl class="kv"><template v-for="(field, index) in tool.args" :key="index"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl></div>
                <div v-if="tool.result.length"><span class="eyebrow">結果</span><dl class="kv"><template v-for="(field, index) in tool.result" :key="index"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl></div>
              </div>
            </details>
            <div v-if="item.thinking" class="thinking" role="status"><span>思考中</span><i></i><i></i><i></i></div>
            <p v-if="!item.thinking && !item.error && !item.reasoning && !item.content && !item.envelope && !item.tools.length" class="note">這次模型回應沒有可顯示的內容。</p>
          </article>
        </template>
        <p v-if="!history.length" class="note">還沒有任何模型呼叫。</p>
      </div>
    </section>
    <div v-else class="card detail note" :role="runError ? 'alert' : null">{{ runError || (selected ? '載入選定 Run…' : '左邊選一個 Run 看明細') }}</div>
  </div>
</template>

<style scoped>
.run-layout {
  display: grid;
  grid-template-columns: minmax(360px, 0.7fr) minmax(520px, 1.6fr);
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
.chat {
  display: flex;
  flex-direction: column;
  gap: 16px;
  max-height: min(74vh, 860px);
  overflow: auto;
  padding: 4px 8px 12px 2px;
}
.wake {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 12px;
  border: 1px dashed var(--line2);
  border-radius: 8px;
}
.trigger {
  padding: 2px 0;
}
.type {
  width: 132px;
  flex: none;
}
.message {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 780px;
}
.who {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
}
.avatar {
  display: inline-grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--accw);
  color: var(--acc);
  font-size: 10.5px;
  font-weight: 600;
}
.text {
  font-size: 15px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}
.reasoning {
  border-left: 2px solid var(--line2);
  padding-left: 12px;
}
.reasoning summary {
  display: flex;
  gap: 8px;
  cursor: pointer;
  color: var(--dim);
  font-size: 13px;
}
.reasoning .label {
  flex: none;
}
.reasoning .preview {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  color: var(--mute);
}
.reasoning[open] .preview {
  display: none;
}
.reasoning .text {
  margin-top: 6px;
  color: var(--dim);
  font-size: 14px;
}
.final {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 14px;
  border: 1px solid var(--pos);
  border-radius: 8px;
  font-size: 14px;
}
.tool {
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--surf2);
}
.tool[data-action] {
  border-color: var(--acc);
  background: var(--accw);
}
.tool[data-status='error'],
.tool[data-status='denied'] {
  border-color: var(--neg);
}
.tool summary {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 10px;
  cursor: pointer;
  font-size: 13px;
}
.tool .icon {
  width: 14px;
  text-align: center;
}
.tool[data-status='ok'] .icon {
  color: var(--pos);
}
.tool[data-status='error'] .icon,
.tool[data-status='denied'] .icon {
  color: var(--neg);
}
.tool-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 4px 12px 10px;
}
.notice {
  align-self: center;
  max-width: 640px;
  padding: 3px 12px;
  border-radius: 999px;
  background: var(--surf2);
  color: var(--dim);
  font-size: 12px;
  text-align: center;
}
.notice[data-tone='neg'] {
  color: var(--neg);
}
.notice[data-tone='warn'] {
  color: var(--warn);
}
.thinking {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--dim);
  font-size: 14px;
}
.thinking i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--acc);
  animation: thinking 1.2s infinite ease-in-out;
}
.thinking i:nth-of-type(2) {
  animation-delay: 0.15s;
}
.thinking i:nth-of-type(3) {
  animation-delay: 0.3s;
}
.spinner {
  width: 12px;
  height: 12px;
  border: 2px solid var(--line2);
  border-top-color: var(--acc);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
@keyframes thinking {
  0%, 80%, 100% { opacity: 0.25; transform: translateY(0); }
  40% { opacity: 1; transform: translateY(-3px); }
}
@keyframes spin {
  to { transform: rotate(360deg); }
}
@media (prefers-reduced-motion: reduce) {
  .thinking i,
  .spinner {
    animation: none;
  }
}
@media (max-width: 1100px) {
  .run-layout {
    grid-template-columns: 1fr;
  }
}
</style>
