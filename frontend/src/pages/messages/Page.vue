<script setup>
import { computed, onUnmounted, ref } from 'vue'

import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { fmtTime } from '../../shared/format.js'
import { say } from '../../shared/toast.js'
import { deleteMessage, listAccounts, listMessages, postMessage } from './api.js'

const tagFilter = ref('')
const sort = ref('desc')
const extra = ref([])
const page = ref(1)
const loadingMore = ref(false)
let listGeneration = 0
let disposed = false
const { data, error, loading, reload } = useLoad(async () => {
  const [messages, accounts] = await Promise.all([listMessages({ tag: tagFilter.value, sort: sort.value }), listAccounts()])
  return { ...messages, accounts: accounts.accounts ?? [] }
})
const all = computed(() => [...(data.value?.messages ?? []), ...extra.value])

async function resetList() {
  listGeneration++
  loadingMore.value = false
  page.value = 1
  extra.value = []
  await reload()
}

async function loadMore() {
  if (!data.value?.has_more || loadingMore.value || loading.value || disposed) return
  const generation = listGeneration
  const query = { page: page.value + 1, sort: sort.value, tag: tagFilter.value }
  loadingMore.value = true
  try {
    const next = await listMessages(query)
    if (disposed || generation !== listGeneration) return
    page.value = next.page
    extra.value.push(...next.messages)
    data.value.has_more = next.has_more
  } catch (e) {
    if (!disposed && generation === listGeneration) say(e.message, 'neg')
  } finally {
    if (!disposed && generation === listGeneration) loadingMore.value = false
  }
}

onUnmounted(() => { disposed = true; listGeneration++ })

function kind(message) {
  if (message.user_name === 'you') return { label: 'you · USER', tone: 'acc', mark: 'acc' }
  if (message.user_name === '[deleted]') return { label: '作者已刪除', tone: 'mute', mark: 'line2' }
  return { label: 'Agent', tone: 'dim', mark: 'surf3' }
}

const draft = ref('')
const selectedTags = ref([])
const characterCount = computed(() => Array.from(draft.value).length)
const busy = ref(false)
const deleting = ref('')
async function post() {
  if (!draft.value.trim() || characterCount.value > 500) return
  busy.value = true
  listGeneration++
  loadingMore.value = false
  try {
    await postMessage(draft.value, selectedTags.value)
    draft.value = ''
    selectedTags.value = []
    await resetList()
    say('已發佈')
  } catch (e) {
    say(e.message, 'neg')
  } finally {
    busy.value = false
  }
}

async function remove(message) {
  const ok = await confirm({ title: '刪除這則留言？', ok: '確認刪除', lines: ['留言與標籤會永久刪除', '不可復原'] })
  if (!ok) return
  listGeneration++
  loadingMore.value = false
  deleting.value = message.id
  try {
    await deleteMessage(message.id)
    await resetList()
    say('已刪除')
  } catch (e) {
    say(e.message, 'neg')
  } finally {
    deleting.value = ''
  }
}
</script>

<template>
  <article>
    <div class="col board">
      <section class="card col compose">
        <textarea v-model="draft" placeholder="以 USER 身分留話給 Agent…"></textarea>
        <div v-if="data?.accounts.length" class="row tags">
          <span class="note">標記帳號</span>
          <label v-for="account in data.accounts" :key="account.id" class="row">
            <input v-model="selectedTags" type="checkbox" :value="account.user_name" />
            {{ account.user_name }}
          </label>
        </div>
        <div class="row">
          <span class="mono" :data-tone="characterCount > 500 ? 'neg' : 'mute'">{{ characterCount }} / 500</span>
          <span class="note grow">只發布需要協作的訊息；避免例行進度與重複日誌。</span>
          <button data-tone="acc" :disabled="busy || !draft.trim() || characterCount > 500" @click="post">發佈</button>
        </div>
      </section>

      <div class="row">
        <label class="row">標籤
          <select v-model="tagFilter" @change="resetList">
            <option value="">全部</option>
            <option v-for="account in data?.accounts ?? []" :key="account.id" :value="account.user_name">{{ account.user_name }}</option>
          </select>
        </label>
        <label class="row">排序
          <select v-model="sort" @change="resetList">
            <option value="desc">最新優先</option>
            <option value="asc">最舊優先</option>
          </select>
        </label>
      </div>

      <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
      <p v-else-if="loading && !data" class="note">載入中…</p>
      <template v-else-if="data">
        <section v-for="message in all" :key="message.id" class="card col msg" :data-mark="kind(message).mark">
          <div class="row">
            <b :data-tone="kind(message).tone === 'dim' ? null : kind(message).tone">{{ message.user_name }}</b>
            <span class="tag outline" data-tone="mute">{{ kind(message).label }}</span>
            <span v-for="tag in message.tags" :key="tag" class="tag">@{{ tag }}</span>
            <span class="grow"></span>
            <span class="mono" data-tone="mute">{{ fmtTime(message.created_at) }}</span>
            <button class="text" data-tone="neg" :disabled="deleting === message.id" @click="remove(message)">{{ deleting === message.id ? 'DELETING…' : 'DELETE' }}</button>
          </div>
          <p class="pre">{{ message.content }}</p>
        </section>
        <p v-if="!all.length" class="empty">還沒有留言。</p>
        <button v-if="data.has_more" :disabled="loadingMore || loading" @click="loadMore">{{ loadingMore ? '載入中…' : '載入下一頁（每頁 10 則）' }}</button>
      </template>
    </div>
  </article>
</template>

<style scoped>
.board {
  max-width: 760px;
  width: 100%;
  gap: 11px;
}
.compose,
.msg {
  gap: 8px;
}
.tags {
  gap: 10px;
}
.msg b {
  font-size: 12.5px;
}
</style>
