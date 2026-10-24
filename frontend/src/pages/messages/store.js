import { reactive } from 'vue'

import { base } from '../../core/api.js'
import { listMessages } from './api.js'

const PAGE = 15
const CATCH_UP = 50
const RECONNECT_MS = 3000

// 群聊快取放在模組層級：換頁回來不重抓，重新整理瀏覽器才清掉，不寫進瀏覽器儲存空間。
// messages 由舊到新；seen 是已捲進畫面的 @你 訊息，只記在記憶體。
export const chat = reactive({ messages: [], hasOlder: false, loadingOlder: false, ready: false, error: null, seen: new Set() })

let started = false
let loading = false
let connectedOnce = false

const byTime = (a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || (a.id < b.id ? -1 : 1)

export function add(list) {
  const known = new Set(chat.messages.map((message) => message.id))
  const added = list.filter((message) => !known.has(message.id))
  if (added.length) chat.messages = [...chat.messages, ...added].sort(byTime)
}

export function remove(id) {
  chat.messages = chat.messages.filter((message) => message.id !== id)
}

export function clear() {
  chat.messages = []
  chat.hasOlder = false
}

// start 在群聊頁每次打開時呼叫；只有第一次會連線，之後只在第一次載入失敗時重試。
export function start() {
  if (!started) {
    started = true
    connect()
  }
  if (!chat.ready) loadLatest()
}

async function loadLatest() {
  if (loading) return
  loading = true
  try {
    const page = await listMessages({ limit: PAGE })
    add(page.messages)
    chat.hasOlder = page.has_more
    chat.ready = true
    chat.error = null
  } catch (error) {
    chat.error = error
  } finally {
    loading = false
  }
}

export async function loadOlder() {
  const oldest = chat.messages[0]
  if (!chat.hasOlder || chat.loadingOlder || !oldest) return
  chat.loadingOlder = true
  try {
    const page = await listMessages({ before: oldest.id, limit: PAGE })
    add(page.messages)
    chat.hasOlder = page.has_more
  } finally {
    chat.loadingOlder = false
  }
}

// 先連 SSE 再載入第一頁，載入期間推來的事件不會漏；重複的訊息由 add 去掉。
function connect() {
  const source = new EventSource(`${base}/v1/messages/stream`)
  source.onopen = () => {
    if (connectedOnce) catchUp()
    connectedOnce = true
  }
  source.onerror = () => {
    // 斷線時瀏覽器會自己重連；只有連線被拒（readyState CLOSED）才要自己再連。
    if (source.readyState === 2) setTimeout(connect, RECONNECT_MS)
  }
  source.addEventListener('created', (event) => add([JSON.parse(event.data)]))
  source.addEventListener('deleted', (event) => remove(JSON.parse(event.data).id))
  source.addEventListener('cleared', clear)
}

async function catchUp() {
  const newest = chat.messages.at(-1)
  if (!newest) return loadLatest()
  try {
    const page = await listMessages({ after: newest.id, sort: 'asc', limit: CATCH_UP })
    add(page.messages)
    if (page.has_more) await catchUp()
  } catch (error) {
    // 快取中最新的一則在斷線期間被刪掉了：整份重新載入。
    if (error.code !== 'invalid_request') return
    chat.messages = []
    chat.ready = false
    await loadLatest()
  }
}
