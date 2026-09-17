<script setup>
import { ref } from 'vue'
import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { expireMemory, getMemory, listMemories } from './api.js'

const props = defineProps({ session: Object })
const page = ref(1)
const includeExpired = ref(false)
const selected = ref(null)
const detailError = ref('')
const busy = ref(false)
let selection = 0
const { data, error, loading, reload } = useLoad(() => listMemories(props.session.id, page.value, includeExpired.value))
async function choose(memory) {
  const request = ++selection
  selected.value = null
  detailError.value = ''
  try { const value = await getMemory(props.session.id, memory.id); if (request === selection) selected.value = value }
  catch (e) { if (request === selection) detailError.value = e.message }
}
async function remove() {
  const memory = selected.value
  if (!memory || busy.value || !await confirm({ title: '標記記憶失效？', ok: '標記失效', lines: ['保留原文與來源供稽核，之後不再自動送入 Agent context。'] })) return
  busy.value = true
  try { selected.value = await expireMemory(props.session.id, memory.id); await reload() }
  catch (e) { detailError.value = e.message } finally { busy.value = false }
}
function turn(delta) { page.value += delta; selected.value = null; selection++; reload() }
</script>

<template>
  <div class="cols">
    <section class="card col grow">
      <div class="row"><h2 class="grow">Memory</h2><label class="row"><input v-model="includeExpired" type="checkbox" @change="page = 1; reload()" />含失效記憶</label></div>
      <p v-if="error" role="alert" data-tone="neg">{{ error.message }}</p>
      <p v-if="loading" class="note">載入中…</p>
      <button v-for="m in data?.memories ?? []" :key="m.id" @click="choose(m)"><span class="tag">{{ m.type }}</span> {{ m.summary }} · importance {{ m.importance }} · Run #{{ m.run_id }} {{ m.is_expired ? '（已失效）' : '' }}</button>
      <p v-if="data && !data.memories.length" class="empty">沒有符合條件的記憶。</p>
      <div class="row"><button :disabled="page === 1 || loading" @click="turn(-1)">上一頁</button><span>第 {{ page }} 頁 · 每頁 100</span><button :disabled="!data || data.memories.length < 100 || loading" @click="turn(1)">下一頁</button></div>
    </section>
    <section class="card col grow">
      <p v-if="detailError" role="alert" data-tone="neg">{{ detailError }}</p>
      <template v-if="selected">
        <h2>{{ selected.summary }}</h2><span class="mono">{{ selected.id }} · <RouterLink :to="{ name: 'session-detail', params: { id: session.id }, query: { tab: 'runs', run: selected.run_id } }">Run #{{ selected.run_id }}</RouterLink></span>
        <pre>{{ selected.content }}</pre>
        <p v-if="selected.is_expired">失效時間 {{ selected.expired_at }} · {{ selected.expired_source }} · {{ selected.expiration_reason }} <RouterLink v-if="selected.expired_run_id" :to="{ name: 'session-detail', params: { id: session.id }, query: { tab: 'runs', run: selected.expired_run_id } }">Run #{{ selected.expired_run_id }}</RouterLink></p>
        <button v-else :disabled="busy || session.status === 'deleted'" data-tone="neg" @click="remove">標記失效</button>
      </template>
      <p v-else class="note">選擇記憶後才載入全文。</p>
    </section>
  </div>
</template>
