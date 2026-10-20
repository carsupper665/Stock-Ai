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
const selectionMode = ref(false)
const selectedIds = ref([])
const bulkDeleting = ref(false)
let listGeneration = 0
let disposed = false
const { data, error, loading, reload } = useLoad(async () => {
  const [messages, accounts] = await Promise.all([listMessages({ tag: tagFilter.value, sort: sort.value }), listAccounts()])
  return { ...messages, accounts: accounts.accounts ?? [] }
})
const all = computed(() => [...(data.value?.messages ?? []), ...extra.value])

function exitSelection() {
  selectionMode.value = false
  selectedIds.value = []
}

async function resetList() {
  exitSelection()
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
function selectAllLoaded() {
  selectedIds.value = all.value.map((message) => message.id)
}

async function removeSelected() {
  if (bulkDeleting.value || !selectedIds.value.length || !data.value) return
  const ids = [...selectedIds.value]
  const ok = await confirm({
    title: `刪除所選的 ${ids.length} 則留言？`,
    ok: '確認刪除',
    lines: ['留言與標籤會永久刪除', '不可復原'],
  })
  if (!ok || bulkDeleting.value) return

  bulkDeleting.value = true
  listGeneration++
  loadingMore.value = false
  const deleted = []
  const failed = []
  for (const id of ids) {
    try {
      await deleteMessage(id)
      deleted.push(id)
    } catch (error) {
      failed.push({ id, error })
    }
  }

  const deletedIds = new Set(deleted)
  data.value.messages = data.value.messages.filter((message) => !deletedIds.has(message.id))
  extra.value = extra.value.filter((message) => !deletedIds.has(message.id))
  selectedIds.value = failed.map(({ id }) => id)

  if (!failed.length) {
    await resetList()
    say(error.value ? `已刪除 ${deleted.length} 則留言，重新載入失敗：${error.value.message}` : `已刪除 ${deleted.length} 則留言`, error.value ? 'neg' : undefined)
  } else {
    // Deleting shifts page offsets; refill every previously loaded page so the
    // next "load more" request cannot skip messages.
    try {
      const pages = await Promise.all(Array.from({ length: page.value }, (_, index) =>
        listMessages({ page: index + 1, sort: sort.value, tag: tagFilter.value })))
      data.value.messages = pages[0].messages
      extra.value = pages.slice(1).flatMap((result) => result.messages)
      data.value.has_more = pages.at(-1).has_more
      selectedIds.value = failed.map(({ id }) => id).filter((id) => all.value.some((message) => message.id === id))
    } catch (error) {
      say(`刪除 ${deleted.length} 則、失敗 ${failed.length} 則；重新載入失敗：${error.message}`, 'neg')
      bulkDeleting.value = false
      return
    }
    say(`已刪除 ${deleted.length} 則，失敗 ${failed.length} 則：${failed[0].error.message}`, 'neg')
  }
  bulkDeleting.value = false
}

async function post() {
  if (!draft.value.trim() || characterCount.value > 500 || !selectedTags.value.length) return
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
        <textarea v-model="draft" :disabled="bulkDeleting" placeholder="以 USER 身分留話給 Agent…"></textarea>
        <div v-if="data?.accounts.length" class="row tags">
          <span class="note">寄給（至少一個）</span>
          <label v-for="account in data.accounts" :key="account.id" class="row">
            <input v-model="selectedTags" type="checkbox" :value="account.user_name" :disabled="bulkDeleting" />
            {{ account.user_name }}
          </label>
        </div>
        <div class="row">
          <span class="mono" :data-tone="characterCount > 500 ? 'neg' : 'mute'">{{ characterCount }} / 500</span>
          <span class="note grow">只發布需要協作的訊息；避免例行進度與重複日誌。</span>
          <button data-tone="acc" :disabled="busy || bulkDeleting || !draft.trim() || characterCount > 500 || !selectedTags.length" @click="post">發佈</button>
        </div>
      </section>

      <div class="row">
        <label class="row">標籤
          <select v-model="tagFilter" :disabled="bulkDeleting" @change="resetList">
            <option value="">全部</option>
            <option value="USER">給我的</option>
            <option v-for="account in data?.accounts ?? []" :key="account.id" :value="account.user_name">{{ account.user_name }}</option>
          </select>
        </label>
        <label class="row">排序
          <select v-model="sort" :disabled="bulkDeleting" @change="resetList">
            <option value="desc">最新優先</option>
            <option value="asc">最舊優先</option>
          </select>
        </label>
      </div>

      <div v-if="!selectionMode" class="row selection-actions">
        <button :disabled="!all.length || !!deleting" @click="selectionMode = true">選取留言</button>
      </div>
      <div v-else class="row selection-actions" role="toolbar" aria-label="留言選取操作">
        <span class="note grow">已選 {{ selectedIds.length }} 則</span>
        <button :disabled="bulkDeleting || !all.length" @click="selectAllLoaded">全選已載入</button>
        <button :disabled="bulkDeleting || !selectedIds.length" @click="selectedIds = []">清除選取</button>
        <button :disabled="bulkDeleting" @click="exitSelection">取消</button>
        <button data-tone="neg" :disabled="bulkDeleting || !selectedIds.length" @click="removeSelected">{{ bulkDeleting ? '刪除中…' : '刪除所選' }}</button>
      </div>

      <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
      <p v-else-if="loading && !data" class="note">載入中…</p>
      <template v-else-if="data">
        <component :is="selectionMode ? 'label' : 'section'" v-for="message in all" :key="message.id" class="card col msg" :class="{ selectable: selectionMode }" :data-mark="kind(message).mark" :data-selected="selectionMode && selectedIds.includes(message.id) ? '' : null">
          <div class="row">
            <input v-if="selectionMode" v-model="selectedIds" class="select-box" type="checkbox" :value="message.id" :disabled="bulkDeleting" :aria-label="`選取留言：${message.content.slice(0, 40)}`" />
            <b :data-tone="kind(message).tone === 'dim' ? null : kind(message).tone">{{ message.user_name }}</b>
            <span class="tag outline" data-tone="mute">{{ kind(message).label }}</span>
            <span v-for="tag in message.tags" :key="tag" class="tag">@{{ tag }}</span>
            <span class="grow"></span>
            <span class="mono" data-tone="mute">{{ fmtTime(message.created_at) }}</span>
            <button v-if="!selectionMode" class="text" data-tone="neg" :disabled="deleting === message.id" @click="remove(message)">{{ deleting === message.id ? 'DELETING…' : 'DELETE' }}</button>
          </div>
          <p class="pre">{{ message.content }}</p>
        </component>
        <p v-if="!all.length" class="empty">還沒有留言。</p>
        <button v-if="data.has_more" :disabled="loadingMore || loading || bulkDeleting" @click="loadMore">{{ loadingMore ? '載入中…' : '載入下一頁（每頁 10 則）' }}</button>
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
.selection-actions {
  justify-content: flex-end;
  flex-wrap: wrap;
}
.msg.selectable {
  color: var(--ink);
  cursor: pointer;
  font: inherit;
}
.msg.selectable[data-selected] {
  border-color: var(--acc);
  background: var(--accw);
}
.msg.selectable:has(input:disabled) {
  cursor: default;
}
.select-box {
  flex: none;
  margin: 0;
}
</style>
