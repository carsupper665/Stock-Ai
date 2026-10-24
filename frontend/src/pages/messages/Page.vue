<script setup>
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'

import { useLoad } from '../../core/load.js'
import Avatar from '../../shared/Avatar.vue'
import { confirm } from '../../shared/confirm.js'
import { fmtTime } from '../../shared/format.js'
import { say } from '../../shared/toast.js'
import Composer from './Composer.vue'
import MemberDialog from './MemberDialog.vue'
import MemberList from './MemberList.vue'
import { clearMessages, deleteMessage, listAccounts, listBindings, me } from './api.js'
import { authorOf, buildMembers, mentionsUser, tagLabel } from './members.js'
import { chat, clear, loadOlder, remove, start } from './store.js'

const openSidebar = inject('openSidebar', () => {})

const { data, error } = useLoad(async () => {
  const [accounts, bindings, identity] = await Promise.all([listAccounts(), listBindings(), me()])
  return { accounts: accounts.accounts ?? [], sessions: bindings.sessions, userName: identity.user_name }
})
const userName = computed(() => data.value?.userName ?? 'USER')
const directory = computed(() => buildMembers(data.value?.accounts ?? [], data.value?.sessions ?? []))
const everyone = computed(() => [...directory.value.members, ...directory.value.unbound])
function accountChanged(account) {
  data.value.accounts = data.value.accounts.map((item) => item.id === account.id ? account : item)
}

const dialogId = ref('')
const dialogMember = computed(() => everyone.value.find((member) => member.id === dialogId.value))
const membersOpen = ref(false)
function openMember(member) {
  membersOpen.value = false
  dialogId.value = member.id
}
const composer = ref(null)
function mention(member) {
  dialogId.value = ''
  composer.value.addTag(member.account.user_name)
  composer.value.focus()
}

// 搜尋與「@我」只過濾前端已載入的訊息
const searchOpen = ref(false)
const query = ref('')
const mentionsOnly = ref(false)
const view = (message) => ({ ...message, author: authorOf(message, directory.value, userName.value), mention: mentionsUser(message) })
const shown = computed(() => {
  const text = query.value.trim().toLowerCase()
  return chat.messages.map(view).filter((message) => (!mentionsOnly.value || message.mention) &&
    (!text || message.content.toLowerCase().includes(text) || message.author.name.toLowerCase().includes(text)))
})
function toggleSearch() {
  searchOpen.value = !searchOpen.value
  if (!searchOpen.value) query.value = ''
}

// 捲動：一打開捲到最底；內容或可視區改變大小時（新訊息、字型、成員名稱、版面）原本在底部才跟著捲；往上捲到頂才載入更早的
const scroller = ref(null)
const content = ref(null)
let atBottom = true
// 程式捲到底後的 scroll 事件是非同步的，期間內容可能又變高；scrollTop 沒比這個值小就不是使用者往上捲
let pinnedTop = null
function toBottom() {
  if (!scroller.value) return
  scroller.value.scrollTop = scroller.value.scrollHeight
  pinnedTop = scroller.value.scrollTop || null
}
async function older() {
  const el = scroller.value
  const height = el.scrollHeight
  try {
    await loadOlder()
  } catch (e) {
    say(e.message, 'neg')
    return
  }
  await nextTick()
  el.scrollTop += el.scrollHeight - height
}
function onScroll() {
  const el = scroller.value
  atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40 || (pinnedTop !== null && el.scrollTop >= pinnedTop)
  if (!atBottom) pinnedTop = null
  if (el.scrollTop <= 0 && chat.hasOlder && !chat.loadingOlder) older()
}
// 資料變動在 render 後直接處理；ResizeObserver 處理只有版面變的情況（字型、視窗大小）
watch(() => [chat.messages.at(-1)?.id, chat.ready, directory.value], () => { if (atBottom) toBottom() }, { flush: 'post' })
const resized = new ResizeObserver(() => { if (atBottom) toBottom() })
const stickToBottom = () => { atBottom = true }

// 未讀 @你：捲進畫面就算已讀，只記在記憶體
const unread = computed(() => chat.messages.filter((message) => mentionsUser(message) && !chat.seen.has(message.id)))
let observer
function observeUnread() {
  if (!observer) return
  for (const message of unread.value) {
    const el = scroller.value?.querySelector(`[data-id="${CSS.escape(message.id)}"]`)
    if (el) observer.observe(el)
  }
}
watch(() => [unread.value.length, shown.value.length], observeUnread, { flush: 'post' })
async function jumpToUnread() {
  const oldest = unread.value[0]
  if (!oldest) return
  query.value = ''
  await nextTick()
  scroller.value.querySelector(`[data-id="${CSS.escape(oldest.id)}"]`)?.scrollIntoView({ block: 'center' })
}

onMounted(() => {
  start()
  resized.observe(content.value)
  resized.observe(scroller.value)
  if (typeof IntersectionObserver === 'function') {
    observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue
        chat.seen.add(entry.target.dataset.id)
        observer.unobserve(entry.target)
      }
    }, { root: scroller.value })
  }
  observeUnread()
})
onUnmounted(() => {
  observer?.disconnect()
  resized.disconnect()
})

// ⋮ 選單：多選刪除、清空群聊
const moreOpen = ref(false)
const selecting = ref(false)
const selectedIds = ref([])
const bulkDeleting = ref(false)
function startSelection() {
  moreOpen.value = false
  selecting.value = true
}
function exitSelection() {
  selecting.value = false
  selectedIds.value = []
}

async function removeSelected() {
  const ids = [...selectedIds.value]
  if (bulkDeleting.value || !ids.length) return
  if (!await confirm({ title: `刪除所選的 ${ids.length} 則訊息？`, ok: '確認刪除', lines: ['訊息與標籤會永久刪除', '不可復原'] })) return
  bulkDeleting.value = true
  const failed = []
  for (const id of ids) {
    try {
      await deleteMessage(id)
      remove(id)
    } catch (error) {
      failed.push({ id, error })
    }
  }
  bulkDeleting.value = false
  selectedIds.value = failed.map(({ id }) => id)
  if (!failed.length) {
    exitSelection()
    say(`已刪除 ${ids.length} 則訊息`)
  } else {
    say(`已刪除 ${ids.length - failed.length} 則，失敗 ${failed.length} 則：${failed[0].error.message}`, 'neg')
  }
}

const deleting = ref('')
async function removeOne(message) {
  if (!await confirm({ title: '刪除這則訊息？', ok: '確認刪除', lines: ['訊息與標籤會永久刪除', '不可復原'] })) return
  deleting.value = message.id
  try {
    await deleteMessage(message.id)
    remove(message.id)
    say('已刪除')
  } catch (e) {
    say(e.message, 'neg')
  } finally {
    deleting.value = ''
  }
}

async function clearAll() {
  moreOpen.value = false
  const typed = await confirm({
    title: '清空群聊？', ok: '清空',
    lines: ['所有訊息與 tag 會永久刪除', 'Agent 下次讀群聊會從最新的訊息開始', '不可復原'],
    input: { label: '輸入「清空」確認', placeholder: '清空' },
  })
  if (typed === null) return
  if (typed.trim() !== '清空') {
    say('沒有輸入「清空」，群聊沒有清空', 'neg')
    return
  }
  try {
    await clearMessages()
    clear()
    exitSelection()
    say('已清空群聊')
  } catch (e) {
    say(e.message, 'neg')
  }
}
</script>

<template>
  <article class="messages-page">
    <div class="member-pane">
      <div class="pane-title">成員 <span class="mono" data-tone="mute">{{ everyone.length }}</span></div>
      <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
      <MemberList v-else :members="directory.members" :unbound="directory.unbound" @open="openMember" />
    </div>

    <section class="chat">
      <header class="chat-head">
        <button type="button" class="ib round narrow" aria-label="打開側欄" title="打開側欄" @click="openSidebar">
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" d="M15 5l-7 7 7 7" /></svg>
        </button>
        <div class="title">
          <b>群聊</b>
          <span class="sub">{{ everyone.length }} 位成員</span>
        </div>
        <button v-if="unread.length" type="button" class="unread" @click="jumpToUnread">未讀 @你 {{ unread.length }}</button>
        <button type="button" class="ib" :class="{ on: searchOpen }" aria-label="搜尋訊息" title="搜尋訊息" :aria-pressed="searchOpen" @click="toggleSearch">
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><circle cx="11" cy="11" r="6.5" fill="none" stroke="currentColor" stroke-width="2" /><path stroke="currentColor" stroke-width="2" stroke-linecap="round" d="M16 16l4.5 4.5" /></svg>
        </button>
        <button type="button" class="ib at" :class="{ on: mentionsOnly }" aria-label="只看 @我 的訊息" title="只看 @我 的訊息" :aria-pressed="mentionsOnly" @click="mentionsOnly = !mentionsOnly">@我</button>
        <div class="more">
          <button type="button" class="ib" aria-label="更多操作" title="更多操作" aria-haspopup="menu" :aria-expanded="moreOpen" @click="moreOpen = !moreOpen">
            <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><g fill="currentColor"><circle cx="12" cy="5" r="2" /><circle cx="12" cy="12" r="2" /><circle cx="12" cy="19" r="2" /></g></svg>
          </button>
          <template v-if="moreOpen">
            <div class="backdrop" @click="moreOpen = false"></div>
            <div class="menu" role="menu">
              <button type="button" role="menuitem" class="menu-item" :disabled="!chat.messages.length" @click="startSelection">多選刪除</button>
              <button type="button" role="menuitem" class="menu-item" data-tone="neg" @click="clearAll">清空群聊</button>
            </div>
          </template>
        </div>
        <button type="button" class="ib round narrow" aria-label="成員" title="成員" :aria-expanded="membersOpen" @click="membersOpen = !membersOpen">
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><g fill="currentColor"><circle cx="5" cy="12" r="2" /><circle cx="12" cy="12" r="2" /><circle cx="19" cy="12" r="2" /></g></svg>
        </button>
      </header>

      <div v-if="membersOpen" class="member-sheet narrow-only">
        <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
        <MemberList v-else :members="directory.members" :unbound="directory.unbound" @open="openMember" />
      </div>

      <div v-if="searchOpen" class="search">
        <input v-model="query" type="search" placeholder="搜尋已載入的訊息" aria-label="搜尋訊息" autofocus />
        <p class="note">{{ query.trim() ? `符合 ${shown.length} 則 · ` : '' }}只搜尋已載入的 {{ chat.messages.length }} 則，往上捲可載入更早的</p>
      </div>

      <div v-if="selecting" class="selection" role="toolbar" aria-label="訊息選取操作">
        <span class="grow">已選 {{ selectedIds.length }} 則</span>
        <button type="button" :disabled="bulkDeleting" @click="selectedIds = shown.map((message) => message.id)">全選已載入</button>
        <button type="button" :disabled="bulkDeleting" @click="exitSelection">取消</button>
        <button type="button" data-tone="neg" :disabled="bulkDeleting || !selectedIds.length" @click="removeSelected">{{ bulkDeleting ? '刪除中…' : '刪除所選' }}</button>
      </div>

      <div ref="scroller" class="msgs" @scroll="onScroll">
        <div ref="content" class="msgs-content">
        <button v-if="chat.hasOlder" type="button" class="older" :disabled="chat.loadingOlder" @click="older">{{ chat.loadingOlder ? '載入中…' : '載入更早的訊息' }}</button>
        <p v-if="chat.error && !chat.ready" class="callout" data-tone="neg">{{ chat.error.message }}</p>
        <p v-else-if="!chat.ready" class="note center">載入中…</p>
        <component :is="selecting ? 'label' : 'div'" v-for="message in shown" :key="message.id" class="msg" :class="{ mine: message.author.user, mention: message.mention }" :data-id="message.id">
          <input v-if="selecting" v-model="selectedIds" type="checkbox" :value="message.id" :disabled="bulkDeleting" :aria-label="`選取訊息：${message.content.slice(0, 40)}`" />
          <Avatar v-if="!message.author.user" :size="32" />
          <div class="body">
            <div class="who">
              <b class="ellipsis">{{ message.author.name }}</b>
              <span v-if="message.author.sub" class="sub ellipsis">{{ message.author.sub }}</span>
              <span v-if="message.author.user" class="tag">USER</span>
            </div>
            <div class="bubble">
              <span v-if="message.mention" class="mention-label">@你</span>
              <p class="pre">{{ message.content }}</p>
              <p v-if="message.tags.length" class="tags">
                <span v-for="tag in message.tags" :key="tag">@{{ tagLabel(tag, directory, userName) }}</span>
              </p>
            </div>
            <time class="time" :datetime="message.created_at">{{ fmtTime(message.created_at) }}</time>
          </div>
          <div v-if="!selecting" class="acts">
            <button type="button" aria-label="刪除這則訊息" title="刪除" :disabled="deleting === message.id" @click="removeOne(message)">
              <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13" /></svg>
            </button>
          </div>
        </component>
        <p v-if="chat.ready && !shown.length" class="note center">{{ chat.messages.length ? '沒有符合的訊息' : '群聊還沒有訊息。' }}</p>
        </div>
      </div>

      <Composer ref="composer" :members="everyone" :disabled="bulkDeleting" @sent="stickToBottom" />
    </section>

    <MemberDialog v-if="dialogMember" :member="dialogMember" @changed="accountChanged" @mention="mention" @close="dialogId = ''" />
  </article>
</template>

<style scoped>
.messages-page { flex: 1; min-height: 0; padding: 0; gap: 0; display: grid; grid-template-columns: 340px minmax(0, 1fr); overflow: hidden; }
.member-pane { display: flex; flex-direction: column; min-height: 0; overflow-y: auto; background: var(--surf); border-right: 1px solid var(--line); }
.pane-title { padding: 16px 16px 8px; font-weight: 700; font-size: 17px; }
.member-pane .callout { margin: 0 16px; }
.chat { display: flex; flex-direction: column; min-width: 0; min-height: 0; background: var(--bg); position: relative; }
.chat-head { display: flex; align-items: center; gap: 8px; padding: 10px 12px; background: var(--surf); border-bottom: 1px solid var(--line); }
.title { flex: 1; min-width: 0; display: flex; flex-direction: column; margin-left: 4px; }
.title b { font-weight: 700; font-size: 16px; }
.title .sub { font-size: 13px; color: var(--dim); }
.ib { width: 44px; height: 44px; border-radius: 12px; border: 0; padding: 0; background: transparent; display: inline-flex; align-items: center; justify-content: center; color: var(--dim); flex: none; }
.ib:hover:not(:disabled) { background: var(--surf2); }
.ib.on { background: var(--accw); color: var(--acc); }
.ib.at { font-size: 13px; font-weight: 700; }
.unread { flex: none; height: 32px; padding: 0 12px; border-radius: 16px; border: 1px solid var(--acc); background: var(--accw); color: var(--acc); font-size: 13px; font-weight: 700; }
.narrow { display: none; }
.more { position: relative; flex: none; }
.backdrop { position: fixed; inset: 0; z-index: 40; }
.menu { position: absolute; right: 0; top: calc(100% + 4px); z-index: 41; width: 200px; background: var(--surf); border: 1px solid var(--line); border-radius: 14px; padding: 6px; box-shadow: var(--modal-shadow); }
.menu-item { width: 100%; min-height: 44px; border: 0; border-radius: 10px; padding: 0 10px; text-align: left; font-size: 15px; color: var(--ink); }
.menu-item[data-tone='neg'] { color: var(--neg); }
.menu-item:hover:not(:disabled) { background: var(--surf2); }
.member-sheet { max-height: 50%; overflow-y: auto; background: var(--surf); border-bottom: 1px solid var(--line); }
.search { display: flex; flex-direction: column; gap: 4px; padding: 8px 12px; background: var(--surf); border-bottom: 1px solid var(--line); }
.search input { height: 44px; border-radius: 12px; border-color: var(--line); padding: 0 14px; background: var(--surf2); font-size: 14px; }
.selection { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 8px 12px; background: var(--surf); border-bottom: 1px solid var(--line); font-size: 13px; }
.msgs { flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden; }
.msgs-content { display: flex; flex-direction: column; gap: 12px; padding: 16px 28px; }
.older { align-self: center; border-radius: 12px; }
.center { align-self: center; padding: 24px; }
.msg { display: flex; align-items: flex-end; gap: 8px; color: var(--ink); font: inherit; }
.msg.mine { flex-direction: row-reverse; }
label.msg { cursor: pointer; }
.msg > input { align-self: center; flex: none; }
.body { display: flex; flex-direction: column; align-items: flex-start; gap: 3px; max-width: 70%; min-width: 0; }
.mine .body { align-items: flex-end; }
.who { display: flex; align-items: baseline; gap: 6px; max-width: 100%; padding: 0 4px; font-size: 12px; color: var(--dim); }
.who b { color: var(--ink); font-weight: 700; }
.bubble { position: relative; max-width: 100%; padding: 9px 14px; border-radius: 18px; line-height: 1.5; word-break: break-word; font-size: 15px; background: var(--surf); border: 1px solid var(--line); border-bottom-left-radius: 6px; }
.mine .bubble { background: var(--acc); color: var(--surf); border-color: var(--acc); border-radius: 18px 18px 6px 18px; }
.mention .bubble { border-color: var(--acc); background: var(--accw); padding-right: 46px; }
.mention-label { position: absolute; top: 6px; right: 8px; font-size: 11px; font-weight: 700; color: var(--acc); }
.tags { display: flex; flex-wrap: wrap; gap: 0 8px; margin-top: 4px; font-size: 13px; opacity: 0.85; }
.time { font-size: 12px; color: var(--dim); padding: 0 4px; }
.acts { align-self: center; opacity: 0; flex: none; }
.msg:hover .acts, .msg:focus-within .acts { opacity: 1; }
.acts button { width: 40px; height: 40px; border: 0; border-radius: 10px; padding: 0; display: flex; align-items: center; justify-content: center; color: var(--mute); }
.acts button:hover:not(:disabled) { background: var(--surf); color: var(--neg); }
.narrow-only { display: none; }

@media (max-width: 900px) {
  .messages-page { grid-template-columns: minmax(0, 1fr); }
  .member-pane { display: none; }
  .narrow, .narrow-only { display: inline-flex; }
  .member-sheet.narrow-only { display: block; }
  .round { border-radius: 50%; background: color-mix(in srgb, var(--surf2) 72%, transparent); backdrop-filter: blur(6px); }
  .msgs-content { padding: 12px; }
  .body { max-width: 82%; }
}
</style>
