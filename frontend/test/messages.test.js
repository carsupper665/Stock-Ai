import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

import { dialog, settle } from '../src/shared/confirm.js'
import MessagesPage from '../src/pages/messages/Page.vue'
import { session } from '../src/core/session.js'
import { toast } from '../src/shared/toast.js'

let app
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  toast.text = ''
  vi.restoreAllMocks()
})

const respond = (body, status = 200) => new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function mountPage() {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(MessagesPage)
  app.mount(root)
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

const button = (label) => [...document.querySelectorAll('button')].find((item) => item.textContent === label)
const card = (content) => [...document.querySelectorAll('.msg')].find((item) => item.textContent.includes(content))

function enterSelection() {
  expect(button('選取留言').disabled).toBe(false)
  button('選取留言').click()
}

it('以 USER token 發布含帳號名稱 tags 的 500 Unicode 字元內訊息', async () => {
  session.token = 'user-token'
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [{ id: 'acc_1', user_name: 'Research Agent' }] })
    if (request.method === 'POST') return respond({ id: 'msg_new', user_name: 'you', content: 'hello', tags: ['Research Agent'], created_at: '2026-09-16T00:00:00Z' }, 201)
    return respond({ messages: [], page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Research Agent'))
  const textarea = document.querySelector('textarea')
  textarea.value = '😀 hello'
  textarea.dispatchEvent(new Event('input', { bubbles: true }))
  document.querySelector('input[type=checkbox]').click()
  await nextTick()
  ;[...document.querySelectorAll('button')].find((button) => button.textContent === '發佈').click()
  await vi.waitFor(() => expect(fetch.mock.calls.some(([, request]) => request.method === 'POST')).toBe(true))
  const [, request] = fetch.mock.calls.find(([, options]) => options.method === 'POST')
  expect(request.headers.Authorization).toBe('Bearer user-token')
  expect(JSON.parse(request.body)).toEqual({ content: '😀 hello', tags: ['Research Agent'] })
})

it('群聊的 @ 提及是選填，「給我的」查寄給 USER 的留言', async () => {
  session.token = 'user-token'
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (url === '/v1/accounts') return respond({ accounts: [{ id: 'acc_1', user_name: 'Research Agent' }] })
    return respond({ messages: [], page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Research Agent'))
  const textarea = document.querySelector('textarea')
  textarea.value = 'hello'
  textarea.dispatchEvent(new Event('input', { bubbles: true }))
  await nextTick()
  expect(button('發佈').disabled).toBe(false)

  const filter = document.querySelectorAll('select')[0]
  filter.value = 'USER'
  filter.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(fetch.mock.calls.some(([url]) => url === '/v1/messages?page=1&sort=desc&tag=USER')).toBe(true))
})

it('使用固定 page/sort/tag 查詢並經確認刪除留言', async () => {
  session.token = 'user-token'
  const message = { id: 'msg_1', user_name: 'Agent A', content: 'action needed', tags: ['Agent B'], created_at: '2026-09-16T00:00:00Z' }
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [{ id: 'a', user_name: 'Agent A' }, { id: 'b', user_name: 'Agent B' }] })
    if (request.method === 'DELETE') return respond(null, 204)
    return respond({ messages: [message], page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('action needed'))
  const filter = document.querySelectorAll('select')[0]
  filter.value = 'Agent B'
  filter.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(fetch.mock.calls.some(([url]) => url === '/v1/messages?page=1&sort=desc&tag=Agent+B')).toBe(true))
  ;[...document.querySelectorAll('button')].find((button) => button.textContent === 'DELETE').click()
  await nextTick()
  settle(true)
  await vi.waitFor(() => expect(fetch.mock.calls.some(([url, request]) => url === '/v1/messages/msg_1' && request.method === 'DELETE')).toBe(true))
  const [, request] = fetch.mock.calls.find(([url, options]) => url === '/v1/messages/msg_1' && options.method === 'DELETE')
  expect(request.headers.Authorization).toBe('Bearer user-token')
  await vi.waitFor(() => expect(fetch.mock.calls.filter(([url]) => url.startsWith('/v1/messages?')).length).toBeGreaterThanOrEqual(3))
})

it('刪除失敗會保留留言並顯示後端錯誤', async () => {
  session.token = 'user-token'
  const message = { id: 'msg_denied', user_name: 'Agent A', content: 'keep me', tags: [], created_at: '2026-09-16T00:00:00Z' }
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (request.method === 'DELETE') return respond({ error: 'forbidden', message: '只能刪除自己的留言' }, 403)
    return respond({ messages: [message], page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('keep me'))
  ;[...document.querySelectorAll('button')].find((button) => button.textContent === 'DELETE').click()
  await nextTick()
  settle(true)
  await vi.waitFor(() => expect(toast.text).toBe('只能刪除自己的留言'))
  expect(document.body.textContent).toContain('keep me')
})

it('篩選重載會丟棄延遲的 loadMore 結果與錯誤', async () => {
  session.token = 'user-token'
  const more = deferred()
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (url === '/v1/accounts') return respond({ accounts: [{ id: 'a', user_name: 'Agent A' }] })
    if (url.includes('page=2')) return more.promise
    if (url.includes('tag=Agent+A')) return respond({ messages: [{ id: 'filtered', user_name: 'Agent A', content: 'filtered result', tags: [], created_at: '2026-09-16T00:00:00Z' }], page: 1, has_more: true })
    return respond({ messages: [{ id: 'initial', user_name: 'Agent A', content: 'initial result', tags: [], created_at: '2026-09-16T00:00:00Z' }], page: 1, has_more: true })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('initial result'))
  ;[...document.querySelectorAll('button')].find((button) => button.textContent.includes('載入下一頁')).click()
  await vi.waitFor(() => expect(fetch.mock.calls.some(([url]) => url.includes('page=2'))).toBe(true))

  const filter = document.querySelectorAll('select')[0]
  filter.value = 'Agent A'
  filter.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(document.body.textContent).toContain('filtered result'))
  more.reject(new Error('stale pagination failure'))
  await new Promise((resolve) => setTimeout(resolve, 0))

  expect(document.body.textContent).not.toContain('stale pagination failure')
  expect(document.body.textContent).not.toContain('initial result')
  const loadButton = [...document.querySelectorAll('button')].find((button) => button.textContent.includes('載入下一頁'))
  expect(loadButton.disabled).toBe(false)
})

it('選取模式可點整張卡片、全選已載入留言，取消後恢復單則刪除', async () => {
  session.token = 'user-token'
  const first = { id: 'first', user_name: 'you', content: 'first note', tags: [], created_at: '2026-09-16T00:00:00Z' }
  const second = { id: 'second', user_name: 'Agent', content: 'second note', tags: [], created_at: '2026-09-15T00:00:00Z' }
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (url.includes('page=2')) return respond({ messages: [second], page: 2, has_more: false })
    return respond({ messages: [first], page: 1, has_more: true })
  })
  mountPage()
  await vi.waitFor(() => expect(card('first note')).toBeTruthy())

  enterSelection()
  await nextTick()
  expect(button('DELETE')).toBeUndefined()
  expect(button('刪除所選').disabled).toBe(true)
  card('first note').click()
  await nextTick()
  expect(card('first note').querySelector('input[type=checkbox]').checked).toBe(true)
  expect(document.body.textContent).toContain('已選 1 則')

  button('載入下一頁（每頁 10 則）').click()
  await vi.waitFor(() => expect(card('second note')).toBeTruthy())
  button('全選已載入').click()
  await nextTick()
  expect([...document.querySelectorAll('.msg input[type=checkbox]')].every((input) => input.checked)).toBe(true)
  expect(document.body.textContent).toContain('已選 2 則')
  button('清除選取').click()
  await nextTick()
  expect(button('刪除所選').disabled).toBe(true)
  button('取消').click()
  await nextTick()
  expect(document.querySelectorAll('.msg input[type=checkbox]')).toHaveLength(0)
  expect([...document.querySelectorAll('button')].filter((item) => item.textContent === 'DELETE')).toHaveLength(2)
})

it('切換排序會退出選取模式並清除選取', async () => {
  session.token = 'user-token'
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => url === '/v1/accounts'
    ? respond({ accounts: [] })
    : respond({ messages: [{ id: 'a', user_name: 'you', content: 'note', tags: [], created_at: '2026-09-16T00:00:00Z' }], page: 1, has_more: false }))
  mountPage()
  await vi.waitFor(() => expect(card('note')).toBeTruthy())
  enterSelection()
  await nextTick()
  card('note').click()
  const sort = document.querySelectorAll('select')[1]
  sort.value = 'asc'
  sort.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(button('選取留言')).toBeTruthy())
  enterSelection()
  await nextTick()
  expect(button('刪除所選').disabled).toBe(true)
})

it('批量刪除取消確認不送請求；確認後只刪所選並退出模式', async () => {
  session.token = 'user-token'
  const messages = ['a', 'b', 'c'].map((id) => ({ id, user_name: 'you', content: `note ${id}`, tags: [], created_at: '2026-09-16T00:00:00Z' }))
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (request.method === 'DELETE') {
      messages.splice(messages.findIndex((message) => url.endsWith(`/${message.id}`)), 1)
      return respond(null, 204)
    }
    return respond({ messages, page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(card('note a')).toBeTruthy())
  enterSelection()
  await nextTick()
  card('note a').click()
  await nextTick()
  card('note c').click()
  await nextTick()
  button('刪除所選').click()
  await nextTick()
  expect(dialog.title).toContain('2 則')
  settle(false)
  await nextTick()
  expect(fetch.mock.calls.filter(([, request]) => request.method === 'DELETE')).toHaveLength(0)
  button('刪除所選').click()
  await nextTick()
  settle(true)
  await vi.waitFor(() => expect(button('選取留言')).toBeTruthy())
  expect(fetch.mock.calls.filter(([, request]) => request.method === 'DELETE').map(([url]) => url)).toEqual(['/v1/messages/a', '/v1/messages/c'])
  expect(card('note b')).toBeTruthy()
  expect(card('note a')).toBeUndefined()
})

it('部分刪除失敗時保留模式與失敗項目，成功項目從畫面移除', async () => {
  session.token = 'user-token'
  const messages = ['a', 'b'].map((id) => ({ id, user_name: 'you', content: `note ${id}`, tags: [], created_at: '2026-09-16T00:00:00Z' }))
  const pending = deferred()
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (request.method === 'DELETE' && url.endsWith('/a')) return pending.promise
    if (request.method === 'DELETE') return respond({ error: 'forbidden', message: '刪除失敗' }, 403)
    return respond({ messages, page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(card('note a')).toBeTruthy())
  enterSelection()
  await nextTick()
  button('全選已載入').click()
  await nextTick()
  button('刪除所選').click()
  await nextTick()
  settle(true)
  await vi.waitFor(() => expect(button('刪除中…')).toBeTruthy())
  expect(button('取消').disabled).toBe(true)
  expect(card('note a').querySelector('input').disabled).toBe(true)
  pending.resolve(respond(null, 204))
  messages.splice(0, 1)
  await vi.waitFor(() => expect(toast.text).toContain('失敗 1 則'))
  expect(card('note a')).toBeUndefined()
  expect(card('note b').querySelector('input').checked).toBe(true)
  expect(button('選取留言')).toBeUndefined()
  expect(fetch.mock.calls.filter(([, request]) => request.method === 'DELETE')).toHaveLength(2)
})
