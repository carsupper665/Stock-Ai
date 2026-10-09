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

it('勾選後單則刪除會清除選取且不會再送批次 DELETE', async () => {
  session.token = 'user-token'
  let messages = [{ id: 'msg_selected', user_name: 'Agent A', content: 'remove once', tags: [], created_at: '2026-09-16T00:00:00Z' }]
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (request.method === 'DELETE') {
      messages = []
      return respond(null, 204)
    }
    return respond({ messages, page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('remove once'))
  document.querySelector('input[aria-label="選取留言 msg_selected"]').click()
  await nextTick()
  ;[...document.querySelectorAll('button')].find((button) => button.textContent === 'DELETE').click()
  await nextTick()
  settle(true)

  await vi.waitFor(() => expect(document.body.textContent).toContain('還沒有留言'))
  expect(document.body.textContent).toContain('已選 0 則')
  const batchButton = [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '刪除選取')
  expect(batchButton.disabled).toBe(true)
  batchButton.click()
  await nextTick()
  expect(fetch.mock.calls.filter(([, request]) => request.method === 'DELETE')).toHaveLength(1)
})

it('取消批次刪除會保留選取且不送出 DELETE', async () => {
  session.token = 'user-token'
  const messages = [
    { id: 'msg_1', user_name: 'Agent A', content: 'first', tags: [], created_at: '2026-09-16T00:00:00Z' },
    { id: 'msg_2', user_name: 'Agent B', content: 'second', tags: [], created_at: '2026-09-16T00:01:00Z' },
  ]
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    return respond({ messages, page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('second'))
  const selected = document.querySelector('input[aria-label="選取留言 msg_1"]')
  selected.click()
  await nextTick()
  ;[...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '刪除選取').click()
  await nextTick()
  settle(false)
  await nextTick()

  expect(fetch.mock.calls.some(([, request]) => request.method === 'DELETE')).toBe(false)
  expect(selected.checked).toBe(true)
  expect(document.body.textContent).toContain('已選 1 則')
})

it('全選目前顯示留言後可批次刪除', async () => {
  session.token = 'user-token'
  let messages = [
    { id: 'msg_1', user_name: 'Agent A', content: 'first', tags: [], created_at: '2026-09-16T00:00:00Z' },
    { id: 'msg_2', user_name: 'Agent B', content: 'second', tags: [], created_at: '2026-09-16T00:01:00Z' },
  ]
  const deleted = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (request.method === 'DELETE') {
      const id = url.split('/').at(-1)
      deleted.push(id)
      messages = messages.filter((message) => message.id !== id)
      return respond(null, 204)
    }
    return respond({ messages, page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('second'))
  document.querySelector('input[aria-label="全選目前顯示留言"]').click()
  await nextTick()

  expect([...document.querySelectorAll('input[aria-label^="選取留言"]')].every((input) => input.checked)).toBe(true)
  expect(document.body.textContent).toContain('已選 2 則')
  ;[...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '刪除選取').click()
  await nextTick()
  settle(true)

  await vi.waitFor(() => expect(deleted.sort()).toEqual(['msg_1', 'msg_2']))
  await vi.waitFor(() => expect(toast.text).toBe('已刪除 2 則'))
  expect(document.body.textContent).toContain('還沒有留言')
})

it('批次刪除部分失敗時重載並回報成功與失敗數量', async () => {
  session.token = 'user-token'
  let messages = [
    { id: 'msg_ok', user_name: 'Agent A', content: 'remove me', tags: [], created_at: '2026-09-16T00:00:00Z' },
    { id: 'msg_denied', user_name: 'Agent B', content: 'keep me', tags: [], created_at: '2026-09-16T00:01:00Z' },
  ]
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (request.method === 'DELETE' && url.endsWith('/msg_denied')) return respond({ error: 'forbidden', message: '只能刪除自己的留言' }, 403)
    if (request.method === 'DELETE') {
      messages = messages.filter((message) => message.id !== 'msg_ok')
      return respond(null, 204)
    }
    return respond({ messages, page: 1, has_more: false })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('keep me'))
  document.querySelector('input[aria-label="全選目前顯示留言"]').click()
  await nextTick()
  ;[...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '刪除選取').click()
  await nextTick()
  settle(true)

  await vi.waitFor(() => expect(toast.text).toBe('刪除完成：1 則成功，1 則失敗'))
  expect(document.body.textContent).not.toContain('remove me')
  expect(document.body.textContent).toContain('keep me')
})

it('批次刪除載入的分頁留言後會清空額外頁並重載第一頁', async () => {
  session.token = 'user-token'
  let deleted = false
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    if (url === '/v1/accounts') return respond({ accounts: [] })
    if (request.method === 'DELETE') {
      deleted = true
      return respond(null, 204)
    }
    if (url.includes('page=2')) {
      return respond({ messages: [{ id: 'page_2', user_name: 'Agent B', content: 'second page', tags: [], created_at: '2026-09-16T00:01:00Z' }], page: 2, has_more: false })
    }
    if (deleted) {
      return respond({ messages: [{ id: 'fresh', user_name: 'Agent C', content: 'fresh result', tags: [], created_at: '2026-09-16T00:02:00Z' }], page: 1, has_more: false })
    }
    return respond({ messages: [{ id: 'page_1', user_name: 'Agent A', content: 'first page', tags: [], created_at: '2026-09-16T00:00:00Z' }], page: 1, has_more: true })
  })
  mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('first page'))
  ;[...document.querySelectorAll('button')].find((button) => button.textContent.includes('載入下一頁')).click()
  await vi.waitFor(() => expect(document.body.textContent).toContain('second page'))
  document.querySelector('input[aria-label="選取留言 page_2"]').click()
  await nextTick()
  ;[...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '刪除選取').click()
  await nextTick()
  settle(true)

  await vi.waitFor(() => expect(document.body.textContent).toContain('fresh result'))
  expect(document.body.textContent).not.toContain('first page')
  expect(document.body.textContent).not.toContain('second page')
  expect(fetch.mock.calls.filter(([url]) => url === '/v1/messages?page=1&sort=desc').length).toBe(2)
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
