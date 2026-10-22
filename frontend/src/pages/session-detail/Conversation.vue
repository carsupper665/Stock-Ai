<script setup>
import { computed } from 'vue'
import { useLoad } from '../../core/load.js'
import { fmtTime } from '../../shared/format.js'
import { getConversation } from './api.js'

// 下一個 Run 會接著的 Session 對話（不含 system prompt）：較早的 Run 已經語意化或壓縮過
const props = defineProps({ session: Object })
const { data, error, loading } = useLoad(() => getConversation(props.session.id))
const usage = computed(() => {
  const view = data.value
  if (!view?.context_window) return null
  return Math.round((view.estimated_tokens / view.context_window) * 100)
})
const roleLabel = { user: '喚醒／系統訊息', assistant: 'Agent', tool: '工具結果' }
function kind(message) {
  if (message.role === 'user' && message.content?.startsWith('[Context compacted]')) return '已壓縮的較早對話'
  return roleLabel[message.role] ?? message.role
}
</script>

<template>
  <section class="card col">
    <div class="row">
      <h2 class="grow">Session 對話</h2>
      <span v-if="data" class="note mono">約 {{ data.estimated_tokens }} tokens{{ data.context_window ? ` / ${data.context_window}（${usage}%）` : '' }} · 到 Run #{{ data.run_id || '—' }} · {{ data.updated_at ? fmtTime(data.updated_at) : '尚未開始' }}</span>
    </div>
    <p class="note">下一個 Run 會接在這段對話後面。用量超過 context window 的 80% 會自動壓縮成約 50%。</p>
    <p v-if="error" role="alert" data-tone="neg">{{ error.message }}</p>
    <p v-if="loading" class="note">載入中…</p>
    <p v-if="data && !data.messages.length" class="note">這個 Session 還沒有累積任何對話。</p>
    <details v-for="(m, i) in data?.messages ?? []" :key="i" class="message">
      <summary>
        <span class="tag">{{ kind(m) }}</span>
        <span v-for="call in m.tool_calls ?? []" :key="call.id" class="mono small" data-tone="acc">{{ call.name }}</span>
        <span class="preview mono small" data-tone="mute">{{ m.content }}</span>
      </summary>
      <pre v-if="m.tool_calls?.length" class="mono small">{{ JSON.stringify(m.tool_calls, null, 2) }}</pre>
      <pre v-if="m.content" class="mono small">{{ m.content }}</pre>
    </details>
  </section>
</template>

<style scoped>
.message {
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 6px 10px;
}
.message summary {
  display: flex;
  gap: 8px;
  align-items: center;
  cursor: pointer;
  min-width: 0;
}
.preview {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  min-width: 0;
}
.message[open] .preview {
  display: none;
}
pre {
  margin: 6px 0 0;
  max-height: 320px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
