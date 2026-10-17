<script setup>
import { computed, ref } from 'vue'
import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { fmtTime } from '../../shared/format.js'
import { deleteMemory, getMemory, listMemories } from './api.js'

const props = defineProps({ session: Object })
const selected = ref(null)
const detailError = ref('')
const busy = ref(false)
let selection = 0
const { data, error, loading, reload } = useLoad(() => listMemories(props.session.id))
const pinned = computed(() => (data.value?.memories ?? []).filter((m) => m.bring_back).length)
async function open(memory) {
  const request = ++selection
  selected.value = null
  detailError.value = ''
  try { const value = await getMemory(props.session.id, memory.id); if (request === selection) selected.value = value }
  catch (e) { if (request === selection) detailError.value = e.message }
}
async function remove() {
  const memory = selected.value
  if (!memory || busy.value || !await confirm({ title: `刪除記憶 #${memory.id}？`, ok: '刪除', lines: ['直接從記憶櫃移除，不可復原。'] })) return
  busy.value = true
  try { await deleteMemory(props.session.id, memory.id); selected.value = null; await reload() }
  catch (e) { detailError.value = e.message } finally { busy.value = false }
}
const runLink = (run) => ({ name: 'session-detail', params: { id: props.session.id }, query: { tab: 'runs', run } })
</script>

<template>
  <div class="cols">
    <section class="card col grow">
      <div class="row"><h2 class="grow">記憶櫃</h2><span class="note">{{ data?.memories.length ?? 0 }} / {{ data?.capacity ?? 60 }} 則 · {{ pinned }} 則每輪帶入</span></div>
      <p v-if="error" role="alert" data-tone="neg">{{ error.message }}</p>
      <p v-if="loading" class="note">載入中…</p>
      <button v-for="m in data?.memories ?? []" :key="m.id" :aria-pressed="selected?.id === m.id" @click="open(m)">
        <span class="mono">#{{ m.id }}</span> {{ m.title }} <span v-if="m.bring_back" class="tag">帶入</span> <span class="mono" data-tone="mute">{{ fmtTime(m.created_at) }}</span>
      </button>
      <p v-if="data && !data.memories.length" class="empty">記憶櫃是空的。</p>
    </section>
    <section class="card col grow">
      <p v-if="detailError" role="alert" data-tone="neg">{{ detailError }}</p>
      <template v-if="selected">
        <h2>{{ selected.title }}</h2>
        <span class="mono">#{{ selected.id }} · 建立 {{ fmtTime(selected.created_at) }}<template v-if="selected.created_run_id"> · <RouterLink :to="runLink(selected.created_run_id)">Run #{{ selected.created_run_id }}</RouterLink></template></span>
        <span class="mono">更新 {{ fmtTime(selected.updated_at) }}<template v-if="selected.updated_run_id"> · <RouterLink :to="runLink(selected.updated_run_id)">Run #{{ selected.updated_run_id }}</RouterLink></template></span>
        <span v-if="selected.bring_back" class="tag">每輪 Run 開始時自動帶入</span>
        <pre>{{ selected.msg }}</pre>
        <button :disabled="busy || session.status === 'deleted'" data-tone="neg" @click="remove">刪除</button>
      </template>
      <p v-else class="note">點左邊的標籤打開記憶。</p>
    </section>
  </div>
</template>
