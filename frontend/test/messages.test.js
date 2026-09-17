import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

import { settle } from '../src/shared/confirm.js'
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
