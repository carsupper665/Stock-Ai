import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

// 群聊快取在模組層級：每個測試重新載入模組，才不會帶著上一個測試的快取。
let page, confirmModule, toast, app, root

const respond = (body, status = 200) => new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const minute = 60000
const at = (index) => new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString()

class FakeEventSource {
  static all = []
  constructor(url) {
    this.url = url
    this.readyState = 0
    this.listeners = {}
    FakeEventSource.all.push(this)
  }
  addEventListener(name, handler) { (this.listeners[name] ??= []).push(handler) }
  open() { this.readyState = 1; this.onopen?.() }
  emit(name, data) { for (const handler of this.listeners[name] ?? []) handler({ data: JSON.stringify(data) }) }
  drop(closed) { this.readyState = closed ? 2 : 0; this.onerror?.() }
  close() { this.readyState = 2 }
}

class FakeObserver {
  static current
  constructor(callback) { this.callback = callback; this.observed = new Set(); FakeObserver.current = this }
  observe(el) { this.observed.add(el) }
  unobserve(el) { this.observed.delete(el) }
  disconnect() { this.observed.clear() }
  show(el) { this.callback([{ isIntersecting: true, target: el }]) }
}

// 瀏覽器在內容尺寸改變後通知；測試裡由 render 後手動觸發
class FakeResizeObserver {
  static current
  constructor(callback) { this.callback = callback; FakeResizeObserver.current = this }
  observe() {}
  disconnect() {}
  static fire() { FakeResizeObserver.current.callback([]) }
}

// 假的 Backend：照 /v1/messages 的 limit／before／after／sort 規則回應
let backend
function fakeBackend() {
  backend = {
    accounts: [
      { id: 'a1', user_name: 'acct-run', status: 'active', chat_read: true, chat_post: true, chat_muted_until: null, chat_mute_reason: null, chat_rate_minutes: 1, chat_rate_count: 3 },
      { id: 'a2', user_name: 'acct-wait', status: 'active', chat_read: true, chat_post: true, chat_muted_until: null, chat_mute_reason: null, chat_rate_minutes: 1, chat_rate_count: 3 },
      { id: 'a3', user_name: 'acct-free', status: 'disabled', chat_read: true, chat_post: true, chat_muted_until: null, chat_mute_reason: null, chat_rate_minutes: 1, chat_rate_count: 3 },
    ],
    sessions: [
      { id: 's1', account_id: 'a1', name: 'Runner', status: 'running', current_run: { run_id: 1 }, deleted_at: null },
      { id: 's2', account_id: 'a2', name: 'Waiter', status: 'running', current_run: null, deleted_at: null },
    ],
    messages: Array.from({ length: 40 }, (_, index) => ({
      id: `m${String(index).padStart(2, '0')}`, user_name: index % 3 ? 'acct-run' : 'you',
      content: `message ${index}`, tags: index === 30 || index === 36 ? ['USER'] : [], created_at: at(index),
    })),
    fail: {},
  }
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request = {}) => {
    const method = request.method ?? 'GET'
    const { pathname, searchParams } = new URL(url, 'http://test')
    const body = request.body ? JSON.parse(request.body) : undefined
    const failure = backend.fail[`${method} ${pathname}`]
    if (failure) return respond(failure.body, failure.status)
    if (pathname === '/v1/me') return respond({ kind: 'user', user_name: 'Bless' })
    if (pathname === '/v1/accounts') return respond({ accounts: backend.accounts })
    if (pathname === '/agent/api/v1/sessions') return respond({ sessions: backend.sessions })
    if (pathname === '/v1/messages' && method === 'GET') {
      let list = [...backend.messages]
      const limit = Number(searchParams.get('limit') ?? 10)
      for (const [key, keep] of [['before', (i, a) => i < a], ['after', (i, a) => i > a]]) {
        if (!searchParams.has(key)) continue
        const anchor = list.findIndex((message) => message.id === searchParams.get(key))
        if (anchor < 0) return respond({ error: 'invalid_request', message: `${key} 必須是存在的留言 id` }, 400)
        list = list.filter((_, index) => keep(index, anchor))
      }
      if (searchParams.get('sort') !== 'asc') list.reverse()
      return respond({ messages: list.slice(0, limit), page: 1, has_more: list.length > limit })
    }
    if (pathname === '/v1/messages' && method === 'POST') {
      const created = { id: `new${backend.messages.length}`, user_name: 'you', content: body.content.trim(), tags: body.tags, created_at: new Date().toISOString() }
      backend.messages.push(created)
      return respond(created, 201)
    }
    if (pathname === '/v1/messages' && method === 'DELETE') {
      backend.messages = []
      return respond(null, 204)
    }
    if (pathname.startsWith('/v1/messages/') && method === 'DELETE') {
      backend.messages = backend.messages.filter((message) => message.id !== pathname.split('/').pop())
      return respond(null, 204)
    }
    const accountMatch = pathname.match(/^\/v1\/accounts\/([^/]+)(\/chat\/mute)?$/)
    if (accountMatch) {
      const account = backend.accounts.find((item) => item.id === accountMatch[1])
      if (method === 'PATCH') Object.assign(account, body)
      if (method === 'POST') Object.assign(account, { chat_muted_until: new Date(Date.now() + body.minutes * minute).toISOString(), chat_mute_reason: body.reason })
      if (method === 'DELETE') Object.assign(account, { chat_muted_until: null, chat_mute_reason: null })
      return respond({ ...account })
    }
    return respond({ error: 'not_found', message: `${method} ${url}` }, 404)
  })
}

let fetch
beforeEach(async () => {
  vi.resetModules()
  FakeEventSource.all = []
  globalThis.EventSource = FakeEventSource
  globalThis.IntersectionObserver = FakeObserver
  globalThis.ResizeObserver = FakeResizeObserver
  Element.prototype.scrollIntoView = vi.fn()
  fetch = fakeBackend()
  ;(await import('../src/core/session.js')).session.token = 'user-token'
  confirmModule = await import('../src/shared/confirm.js')
  toast = (await import('../src/shared/toast.js')).toast
  page = (await import('../src/pages/messages/Page.vue')).default
})

afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.useRealTimers()
  vi.restoreAllMocks()
})

async function mount() {
  const Confirm = (await import('../src/shared/Confirm.vue')).default
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/messages', name: 'messages', component: page },
    { path: '/sessions/:id', name: 'session-detail', component: { render: () => null } },
  ] })
  router.push('/messages')
  await router.isReady()
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(defineComponent({ render: () => [h(page), h(Confirm)] }))
  app.use(router)
  app.mount(root)
  await vi.waitFor(() => expect(document.querySelectorAll('.msg').length).toBeGreaterThan(0))
  await vi.waitFor(() => expect(document.querySelector('.member-pane .member')).toBeTruthy())
}

const messageCalls = () => fetch.mock.calls.filter(([url, request = {}]) => url.startsWith('/v1/messages?') && (request.method ?? 'GET') === 'GET').map(([url]) => url)
const requests = (method, path) => fetch.mock.calls.filter(([url, request = {}]) => (request.method ?? 'GET') === method && url === path).map(([, request]) => request.body ? JSON.parse(request.body) : undefined)
const contents = () => [...document.querySelectorAll('.msg .bubble .pre')].map((el) => el.textContent)
const buttonByText = (text, scope = document) => [...scope.querySelectorAll('button')].find((button) => button.textContent.trim() === text)
const byLabel = (label, scope = document) => scope.querySelector(`[aria-label="${label}"]`)
const source = () => FakeEventSource.all.at(-1)
const settle = async (result) => {
  await vi.waitFor(() => expect(confirmModule.dialog.open).toBe(true))
  confirmModule.settle(result)
  await new Promise((resolve) => setTimeout(resolve, 0))
}
const type = async (el, value, caret = value.length) => {
  el.value = value
  if (el.tagName === 'TEXTAREA') el.setSelectionRange(caret, caret)
  el.dispatchEvent(new Event('input', { bubbles: true }))
  await nextTick()
}
const key = (el, name) => el.dispatchEvent(new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true }))

// 讓 happy-dom 有捲動量：每則訊息 100px 高，視窗 400px
function scrollable(el) {
  const client = 400
  const box = { top: 0, get height() { return el.querySelectorAll('.msg').length * 100 } }
  Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => box.height })
  Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => client })
  Object.defineProperty(el, 'scrollTop', { configurable: true, get: () => box.top, set: (value) => { box.top = Math.max(0, Math.min(value, box.height - client)) } })
  return box
}

it('F1 第一次只發一次 limit=15，捲到頂才發 before=最舊&limit=15，離開再回來不重抓列表', async () => {
  await mount()
  expect(messageCalls()).toEqual(['/v1/messages?limit=15'])
  expect(contents()).toHaveLength(15)
  expect(contents()[0]).toBe('message 25')
  expect(contents().at(-1)).toBe('message 39')

  const scroller = document.querySelector('.msgs')
  const box = scrollable(scroller)
  box.top = 0
  scroller.dispatchEvent(new Event('scroll'))
  await vi.waitFor(() => expect(contents()).toHaveLength(30))
  expect(messageCalls()).toEqual(['/v1/messages?limit=15', '/v1/messages?before=m25&limit=15'])
  expect(contents()[0]).toBe('message 10')
  expect(box.top).toBe(1500)

  app.unmount()
  app = null
  document.body.innerHTML = ''
  await mount()
  expect(contents()).toHaveLength(30)
  expect(messageCalls()).toHaveLength(2)
  expect(FakeEventSource.all).toHaveLength(1)
  expect(source().url).toBe('/v1/messages/stream')
})

it('F2 SSE 事件下一次 render 就更新；重連只發一次 after= 補齊；連線被拒才自己重連；沒有任何定時輪詢', async () => {
  const intervals = vi.spyOn(globalThis, 'setInterval')
  await mount()
  source().open()
  source().emit('created', { id: 'live', user_name: 'acct-wait', content: '即時訊息', tags: [], created_at: at(100) })
  await nextTick()
  expect(contents().at(-1)).toBe('即時訊息')
  source().emit('deleted', { id: 'm39' })
  await nextTick()
  expect(contents()).not.toContain('message 39')

  backend.messages.push({ id: 'missed', user_name: 'acct-run', content: '斷線時的訊息', tags: [], created_at: at(101) })
  backend.messages.push({ id: 'live', user_name: 'acct-wait', content: '即時訊息', tags: [], created_at: at(100) })
  backend.messages.sort((a, b) => a.created_at.localeCompare(b.created_at))
  source().drop(false)
  source().open()
  await vi.waitFor(() => expect(contents().at(-1)).toBe('斷線時的訊息'))
  expect(messageCalls().filter((url) => url.includes('after='))).toEqual(['/v1/messages?after=live&sort=asc&limit=50'])
  expect(contents().filter((text) => text === '即時訊息')).toHaveLength(1)

  vi.useFakeTimers()
  source().drop(true)
  expect(FakeEventSource.all).toHaveLength(1)
  vi.advanceTimersByTime(3000)
  vi.useRealTimers()
  expect(FakeEventSource.all).toHaveLength(2)

  source().emit('cleared', {})
  await nextTick()
  expect(document.querySelectorAll('.msg')).toHaveLength(0)
  expect(document.body.textContent).toContain('群聊還沒有訊息')
  expect(intervals).not.toHaveBeenCalled()
})

it('F3 原本在底部才跟著新訊息捲，不在底部時捲動位置不變', async () => {
  await mount()
  const scroller = document.querySelector('.msgs')
  const box = scrollable(scroller)
  const scrollTo = (top) => { box.top = top; scroller.dispatchEvent(new Event('scroll')) }
  scrollTo(box.height - 400)
  source().emit('created', { id: 'n1', user_name: 'acct-run', content: 'n1', tags: [], created_at: at(200) })
  await nextTick()
  FakeResizeObserver.fire()
  expect(box.top).toBe(box.height - 400)
  expect(contents().at(-1)).toBe('n1')

  scrollTo(300)
  source().emit('created', { id: 'n2', user_name: 'acct-run', content: 'n2', tags: [], created_at: at(201) })
  await nextTick()
  FakeResizeObserver.fire()
  expect(box.top).toBe(300)

  await type(byLabel('輸入訊息'), '自己發的')
  byLabel('傳送').click()
  await vi.waitFor(() => expect(contents().at(-1)).toBe('自己發的'))
  FakeResizeObserver.fire()
  expect(box.top).toBe(box.height - 400)
})

it('F4 搜尋只過濾已載入的訊息並提示；「@我」只顯示 tag USER 的訊息', async () => {
  await mount()
  byLabel('搜尋訊息').click()
  await nextTick()
  const search = document.querySelector('.search input')
  await type(search, 'message 3')
  expect(contents()).toEqual(['message 30', 'message 31', 'message 32', 'message 33', 'message 34', 'message 35', 'message 36', 'message 37', 'message 38', 'message 39'])
  expect(document.querySelector('.search').textContent).toContain('只搜尋已載入的 15 則，往上捲可載入更早的')
  await type(search, 'message 1')
  expect(contents()).toEqual([])
  expect(messageCalls()).toHaveLength(1)
  await type(search, '')

  byLabel('只看 @我 的訊息').click()
  await nextTick()
  expect(contents()).toEqual(['message 30', 'message 36'])
  byLabel('只看 @我 的訊息').click()
  await nextTick()
  expect(contents()).toHaveLength(15)
})

it('F5 @你 有強調樣式；未讀數捲進畫面就減少；點未讀跳到最舊的一則', async () => {
  await mount()
  const mentions = [...document.querySelectorAll('.msg.mention')]
  expect(mentions.map((el) => el.dataset.id)).toEqual(['m30', 'm36'])
  expect(mentions[0].querySelector('.mention-label').textContent).toBe('@你')
  expect(document.querySelector('.msg:not(.mention) .mention-label')).toBeNull()
  expect(buttonByText('未讀 @你 2')).toBeTruthy()

  buttonByText('未讀 @你 2').click()
  await nextTick()
  expect(Element.prototype.scrollIntoView.mock.contexts[0].dataset.id).toBe('m30')

  FakeObserver.current.show(document.querySelector('[data-id="m36"]'))
  await nextTick()
  expect(buttonByText('未讀 @你 1')).toBeTruthy()
  buttonByText('未讀 @你 1').click()
  await nextTick()
  expect(Element.prototype.scrollIntoView.mock.contexts[1].dataset.id).toBe('m30')
  FakeObserver.current.show(document.querySelector('[data-id="m30"]'))
  await nextTick()
  expect(document.querySelector('.unread')).toBeNull()
})

it('F6 打 @ 跳出成員選單，可打字篩選，上下鍵、Enter、Esc 操作；送出時當作 tags', async () => {
  await mount()
  const input = byLabel('輸入訊息')
  await type(input, '嗨 @')
  const options = () => [...document.querySelectorAll('.mentions [role=option]')].map((el) => el.querySelector('b').textContent)
  expect(options()).toEqual(['Runner', 'Waiter', 'acct-free'])
  await type(input, '嗨 @wa')
  expect(options()).toEqual(['Waiter'])
  await type(input, '嗨 @acct')
  expect(options()).toEqual(['Runner', 'Waiter', 'acct-free'])
  key(input, 'ArrowDown')
  await nextTick()
  expect(document.querySelector('[aria-selected=true] b').textContent).toBe('Waiter')
  key(input, 'ArrowUp')
  key(input, 'ArrowUp')
  await nextTick()
  expect(document.querySelector('[aria-selected=true] b').textContent).toBe('acct-free')
  key(input, 'Enter')
  await nextTick()
  expect(input.value).toBe('嗨 ')
  expect([...document.querySelectorAll('.chip')].map((el) => el.textContent.trim().replace('✕', ''))).toEqual(['@acct-free'])
  expect(document.querySelector('.mentions')).toBeNull()

  await type(input, '嗨 @R')
  key(input, 'Escape')
  await nextTick()
  expect(document.querySelector('.mentions')).toBeNull()
  await type(input, '嗨 ')
  expect(requests('POST', '/v1/messages')).toHaveLength(0)

  key(input, 'Enter')
  await vi.waitFor(() => expect(requests('POST', '/v1/messages')).toEqual([{ content: '嗨 ', tags: ['acct-free'] }]))
  await vi.waitFor(() => expect(contents().at(-1)).toBe('嗨'))
  expect(input.value).toBe('')
  expect(document.querySelector('.chip')).toBeNull()

  await type(input, 'x'.repeat(501))
  expect(document.querySelector('.count').textContent).toBe('501/500')
  expect(byLabel('傳送').disabled).toBe(true)
})

it('F7 單則刪除、多選刪除與清空（要輸入「清空」才送出），清空後快取也清空', async () => {
  await mount()
  byLabel('刪除這則訊息', document.querySelector('[data-id="m39"]')).click()
  await settle(null)
  expect(requests('DELETE', '/v1/messages/m39')).toHaveLength(0)
  byLabel('刪除這則訊息', document.querySelector('[data-id="m39"]')).click()
  await settle(true)
  await vi.waitFor(() => expect(contents()).not.toContain('message 39'))

  backend.fail['DELETE /v1/messages/m38'] = { status: 403, body: { error: 'forbidden', message: '只能刪除自己的留言' } }
  byLabel('刪除這則訊息', document.querySelector('[data-id="m38"]')).click()
  await settle(true)
  await vi.waitFor(() => expect(toast.text).toBe('只能刪除自己的留言'))
  expect(contents()).toContain('message 38')
  delete backend.fail['DELETE /v1/messages/m38']

  byLabel('更多操作').click()
  await nextTick()
  buttonByText('多選刪除').click()
  await nextTick()
  backend.fail['DELETE /v1/messages/m36'] = { status: 500, body: { error: 'internal_error', message: '留言操作失敗' } }
  for (const id of ['m37', 'm36']) {
    document.querySelector(`[data-id="${id}"]`).click()
    await nextTick()
  }
  expect(document.querySelector('.selection').textContent).toContain('已選 2 則')
  buttonByText('刪除所選').click()
  await settle(true)
  await vi.waitFor(() => expect(contents()).not.toContain('message 37'))
  expect(contents()).toContain('message 36')
  expect(toast.text).toContain('失敗 1 則')
  expect(document.querySelector('.selection').textContent).toContain('已選 1 則')
  buttonByText('取消').click()
  await nextTick()
  expect(document.querySelector('.selection')).toBeNull()

  byLabel('更多操作').click()
  await nextTick()
  buttonByText('清空群聊').click()
  await settle('清除')
  expect(requests('DELETE', '/v1/messages')).toHaveLength(0)
  expect(toast.text).toContain('沒有輸入「清空」')
  byLabel('更多操作').click()
  await nextTick()
  buttonByText('清空群聊').click()
  await settle('清空')
  await vi.waitFor(() => expect(document.querySelectorAll('.msg')).toHaveLength(0))
  expect(requests('DELETE', '/v1/messages')).toHaveLength(1)

  app.unmount()
  app = null
  document.body.innerHTML = ''
  root = document.createElement('div')
  const Confirm = (await import('../src/shared/Confirm.vue')).default
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: page }, { path: '/sessions/:id', name: 'session-detail', component: { render: () => null } }] })
  app = createApp(defineComponent({ render: () => [h(page), h(Confirm)] }))
  app.use(router)
  document.body.append(root)
  app.mount(root)
  await nextTick()
  expect(document.querySelectorAll('.msg')).toHaveLength(0)
  expect(messageCalls()).toHaveLength(1)
})

it('成員資料還沒回來時顯示載入中，不會先說沒有帳戶', async () => {
  let release
  const accounts = new Promise((resolve) => { release = resolve })
  const original = fetch.getMockImplementation()
  fetch.mockImplementation((url, request) => url === '/v1/accounts' ? accounts.then(() => original(url, request)) : original(url, request))
  const Confirm = (await import('../src/shared/Confirm.vue')).default
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: page }, { path: '/sessions/:id', name: 'session-detail', component: { render: () => null } }] })
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(defineComponent({ render: () => [h(page), h(Confirm)] }))
  app.use(router)
  app.mount(root)
  await nextTick()
  expect(document.querySelector('.member-pane').textContent).toContain('載入中…')
  expect(document.querySelector('.member-pane').textContent).not.toContain('還沒有交易帳戶')
  release()
  await vi.waitFor(() => expect(document.querySelector('.member-pane .member')).toBeTruthy())
})

it('E2 點成員與右鍵「管理使用者」打開同一個彈窗，顯示名稱、帳號、Session 與帳戶狀態', async () => {
  await mount()
  const names = [...document.querySelectorAll('.member-pane .member b')].map((el) => el.textContent)
  expect(names).toEqual(['Runner', 'Waiter', 'acct-free'])
  expect(document.querySelector('.member-pane .unbound').open).toBe(false)
  expect(document.querySelector('.member-pane .unbound summary').textContent).toBe('未綁 Session（1）')

  document.querySelector('.member-pane .member').click()
  await nextTick()
  const dialog = document.querySelector('.member-dialog')
  expect(dialog.open).toBe(true)
  expect(dialog.textContent).toContain('Runner')
  expect(dialog.textContent).toContain('acct-run')
  expect(dialog.querySelector('a').getAttribute('href')).toBe('/sessions/s1')
  expect(dialog.querySelector('a').textContent).toContain('正在執行 Run')
  expect(dialog.textContent).toContain('active')
  byLabel('關閉', dialog).click()
  await nextTick()
  expect(document.querySelector('.member-dialog')).toBeNull()

  const waiter = document.querySelectorAll('.member-pane .member')[1]
  waiter.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 50, clientY: 60 }))
  await nextTick()
  expect(document.querySelector('[role=menu]').getAttribute('aria-label')).toBe('Waiter的管理選單')
  buttonByText('管理使用者').click()
  await nextTick()
  expect(document.querySelector('[role=menu]')).toBeNull()
  expect(document.querySelector('.member-dialog').textContent).toContain('Waiter')
  expect(document.querySelector('.member-dialog a').textContent).toContain('等待下一個 Run')
})

it('E2 彈窗的權限（停權要確認）、禁言兩欄必填、解除、頻率只送改動欄位，錯誤時顯示訊息並還原', async () => {
  await mount()
  document.querySelector('.member-pane .member').click()
  await nextTick()
  const dialog = () => document.querySelector('.member-dialog')

  const select = dialog().querySelector('select')
  select.value = 'readonly'
  select.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(requests('PATCH', '/v1/accounts/a1')).toEqual([{ chat_read: true, chat_post: false }]))
  select.value = 'banned'
  select.dispatchEvent(new Event('change', { bubbles: true }))
  await settle(null)
  expect(requests('PATCH', '/v1/accounts/a1')).toHaveLength(1)
  expect(select.value).toBe('readonly')
  select.value = 'banned'
  select.dispatchEvent(new Event('change', { bubbles: true }))
  await settle(true)
  await vi.waitFor(() => expect(requests('PATCH', '/v1/accounts/a1').at(-1)).toEqual({ chat_read: false, chat_post: false }))

  const [minutes, reason] = dialog().querySelectorAll('.mute-form input')
  const muteButton = () => buttonByText('禁言', dialog())
  expect(muteButton().disabled).toBe(true)
  await type(minutes, '10')
  expect(muteButton().disabled).toBe(true)
  await type(reason, '   ')
  expect(muteButton().disabled).toBe(true)
  await type(minutes, '1.5')
  await type(reason, '洗版')
  expect(muteButton().disabled).toBe(true)
  await type(minutes, '43201')
  expect(muteButton().disabled).toBe(true)
  await type(minutes, '10')
  expect(muteButton().disabled).toBe(false)
  muteButton().click()
  await vi.waitFor(() => expect(dialog().textContent).toContain('禁言中，剩 10 分鐘'))
  expect(requests('POST', '/v1/accounts/a1/chat/mute')).toEqual([{ minutes: 10, reason: '洗版' }])
  expect(dialog().textContent).toContain('原因：洗版')
  buttonByText('解除禁言', dialog()).click()
  await vi.waitFor(() => expect(dialog().querySelector('.mute-form')).toBeTruthy())
  expect(requests('DELETE', '/v1/accounts/a1/chat/mute')).toHaveLength(1)

  backend.fail['POST /v1/accounts/a1/chat/mute'] = { status: 404, body: { error: 'not_found', message: '找不到帳號' } }
  await type(dialog().querySelectorAll('.mute-form input')[0], '5')
  await type(dialog().querySelectorAll('.mute-form input')[1], '再犯')
  buttonByText('禁言', dialog()).click()
  await vi.waitFor(() => expect(toast.text).toBe('找不到帳號'))
  expect(dialog().querySelector('.mute-form')).toBeTruthy()

  const patches = () => requests('PATCH', '/v1/accounts/a1').filter((body) => 'chat_rate_minutes' in body || 'chat_rate_count' in body)
  const [rateMinutes, rateCount] = dialog().querySelectorAll('.rate input')
  await type(rateMinutes, '2')
  expect(patches()).toHaveLength(0)
  rateMinutes.dispatchEvent(new FocusEvent('blur'))
  await vi.waitFor(() => expect(patches()).toEqual([{ chat_rate_minutes: 2 }]))
  await vi.waitFor(() => expect(rateMinutes.disabled).toBe(false))
  rateMinutes.dispatchEvent(new FocusEvent('blur'))
  await type(rateCount, '1')
  key(rateCount, 'Enter')
  rateCount.dispatchEvent(new FocusEvent('blur'))
  await vi.waitFor(() => expect(patches()).toEqual([{ chat_rate_minutes: 2 }, { chat_rate_count: 1 }]))
  await vi.waitFor(() => expect(rateCount.disabled).toBe(false))

  backend.fail['PATCH /v1/accounts/a1'] = { status: 400, body: { error: 'invalid_request', message: 'chat_rate_count 必須是 1～100 的整數' } }
  await type(rateCount, '101')
  rateCount.dispatchEvent(new FocusEvent('blur'))
  await vi.waitFor(() => expect(toast.text).toBe('chat_rate_count 必須是 1～100 的整數'))
  await vi.waitFor(() => expect(rateCount.value).toBe('1'))
})

it('E3 「@ 他」把成員加進輸入框的 tag 並關閉彈窗', async () => {
  await mount()
  document.querySelectorAll('.member-pane .member')[1].click()
  await nextTick()
  buttonByText('@ 他', document.querySelector('.member-dialog')).click()
  await nextTick()
  expect(document.querySelector('.member-dialog')).toBeNull()
  expect(document.querySelector('.chip').textContent).toContain('@Waiter')
  await type(byLabel('輸入訊息'), 'hi')
  byLabel('傳送').click()
  await vi.waitFor(() => expect(requests('POST', '/v1/messages')).toEqual([{ content: 'hi', tags: ['acct-wait'] }]))
})

it('訊息顯示：USER 靠右並標 USER_NAME 與 USER 標籤，Session 已刪除的作者顯示「被刪除用戶」', async () => {
  backend.accounts.push({ id: 'a4', user_name: 'acct-gone', status: 'active', chat_read: true, chat_post: true, chat_rate_minutes: 1, chat_rate_count: 3 })
  backend.sessions.push({ id: 's4', account_id: 'a4', name: 'Gone', status: 'deleted', current_run: null, deleted_at: at(0) })
  backend.messages.push({ id: 'z1', user_name: 'acct-gone', content: '舊訊息', tags: ['acct-run'], created_at: at(300) })
  backend.messages.push({ id: 'z2', user_name: '[deleted]', content: '更舊', tags: [], created_at: at(301) })
  await mount()
  const mine = document.querySelector('[data-id="m39"]')
  expect(mine.classList.contains('mine')).toBe(true)
  expect(mine.querySelector('.who').textContent).toBe('BlessUSER')
  const gone = document.querySelector('[data-id="z1"]')
  expect(gone.querySelector('.who b').textContent).toBe('被刪除用戶')
  expect(gone.querySelector('.who .sub').textContent).toBe('acct-gone')
  expect(gone.querySelector('.tags').textContent).toBe('@Runner')
  expect(document.querySelector('[data-id="z2"] .who').textContent).toBe('被刪除用戶')
  expect([...document.querySelectorAll('.member-pane .member b')].map((el) => el.textContent)).not.toContain('Gone')
  expect(document.querySelector('[data-id="m38"] .who').textContent).toBe('Runneracct-run')
})
