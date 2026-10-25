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
let open = false
let loadSeq = 0
// epoch：清空或整份重載時加一；請求回來時 epoch 變了，結果就作廢，已清掉的訊息不會被舊回應加回去
let epoch = 0
// removed：已知被刪掉的 id。比刪除更早送出的請求回來時，不會把它加回去
const removed = new Set()

const byTime = (a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || (a.id < b.id ? -1 : 1)

export function add(list) {
  const known = new Set(chat.messages.map((message) => message.id))
  const added = list.filter((message) => !known.has(message.id) && !removed.has(message.id))
  if (added.length) chat.messages = [...chat.messages, ...added].sort(byTime)
}

export function remove(id) {
  removed.add(id)
  chat.messages = chat.messages.filter((message) => message.id !== id)
}

export function clear() {
  epoch++
  chat.messages = []
  chat.hasOlder = false
}

// start 在群聊頁每次打開時呼叫；只有第一次會連線，之後只在第一次載入失敗時重試。
export function start() {
  if (!started) {
    started = true
    connect()
  } else if (!chat.ready && open) {
    loadLatest()
  }
}

async function loadLatest() {
  const request = ++loadSeq
  const at = epoch
  try {
    const page = await listMessages({ limit: PAGE })
    if (request !== loadSeq) return
    // 載入期間被清空：回應裡的訊息已經不存在，清空後的空白才是對的
    if (at === epoch) {
      add(page.messages)
      chat.hasOlder = page.has_more
    }
    chat.ready = true
    chat.error = null
  } catch (error) {
    if (request === loadSeq) chat.error = error
  }
}

function reload() {
  epoch++
  chat.messages = []
  chat.hasOlder = false
  chat.ready = false
  return loadLatest()
}

export async function loadOlder() {
  const oldest = chat.messages[0]
  if (!chat.hasOlder || chat.loadingOlder || !oldest) return
  chat.loadingOlder = true
  const at = epoch
  try {
    const page = await listMessages({ before: oldest.id, limit: PAGE })
    if (at !== epoch) return
    add(page.messages)
    chat.hasOlder = page.has_more
  } finally {
    chat.loadingOlder = false
  }
}

// 第一次載入等 SSE 連上才開始：Backend 在送出回應標頭前就訂閱好了，讀取與推播之間不會漏訊息。
// 之後每次重連都重讀快取的範圍，補上斷線期間的新訊息、去掉被刪的。
function connect() {
  const source = new EventSource(`${base}/v1/messages/stream`)
  source.onopen = () => {
    open = true
    if (chat.ready) resync()
    else loadLatest()
  }
  source.onerror = () => {
    open = false
    // 斷線時瀏覽器會自己重連；只有連線被拒（readyState CLOSED）才要自己再連，期間先照常顯示訊息
    if (source.readyState !== 2) return
    if (!chat.ready) loadLatest()
    setTimeout(connect, RECONNECT_MS)
  }
  source.addEventListener('created', (event) => add([JSON.parse(event.data)]))
  source.addEventListener('deleted', (event) => remove(JSON.parse(event.data).id))
  source.addEventListener('cleared', clear)
}

// 從快取中最舊的一則往後整段重讀，用伺服器的結果取代快取；分頁游標取每次回應的最後一則，
// 不會被重讀期間推來的新訊息帶走。
async function resync() {
  const oldest = chat.messages[0]
  if (!oldest) return reload()
  const at = epoch
  const fresh = [oldest]
  try {
    for (let after = oldest.id; ;) {
      const page = await listMessages({ after, sort: 'asc', limit: CATCH_UP })
      if (at !== epoch) return
      fresh.push(...page.messages)
      if (!page.has_more || !page.messages.length) break
      after = page.messages.at(-1).id
    }
  } catch (error) {
    // 最舊的那則在斷線期間被刪了：整份重新載入
    if (at === epoch && error.code === 'invalid_request') await reload()
    return
  }
  const last = fresh.at(-1)
  const ids = new Set(fresh.map((message) => message.id))
  const pushed = chat.messages.filter((message) => !ids.has(message.id) && byTime(message, last) > 0)
  chat.messages = [...fresh, ...pushed].filter((message) => !removed.has(message.id))
}
