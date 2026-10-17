import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import AccountsPage from '../src/pages/accounts/Page.vue'
import { dialog, settle } from '../src/shared/confirm.js'
import { session } from '../src/core/session.js'
import { toast } from '../src/shared/toast.js'

let app
let router
afterEach(() => {
  app?.unmount()
  app = null
  router = null
  document.body.innerHTML = ''
  toast.text = ''
  vi.restoreAllMocks()
})

const respond = (body, status = 200) => new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const stopped = (over = {}) => ({
  id: 'acc_stop',
  user_name: 'Stopped Agent',
  initial_balance: 10000,
  balance: 8991,
  status: 'active',
  created_at: '2026-09-23T00:00:00Z',
  token: 'account-token-abcdef',
  max_dd: 10,
  max_dd_baseline_equity: 10000,
  max_dd_stopped_at: '2026-09-23T01:00:00Z',
  max_dd_loss_limit: 1000,
  max_dd_equity_floor: 9000,
  ...over,
})

async function mountPage(accounts) {
  const root = document.createElement('div')
  document.body.append(root)
  router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }],
  })
  app = createApp(AccountsPage)
  app.use(router)
  router.push('/accounts')
  await router.isReady()
  app.mount(root)
  await vi.waitFor(() => expect(document.body.textContent).toContain('Stopped Agent'))
  const row = [...document.querySelectorAll('tr[data-click]')].find((tr) => tr.textContent.includes('Stopped Agent'))
  row.click()
  await vi.waitFor(() => expect(document.body.textContent).toContain('風控停止：'))
}

function mockFetch({ accounts, onResume, onAccounts }) {
  const calls = []
  let reads = 0
  const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request = {}) => {
    if (url.startsWith('/agent/')) return respond({ sessions: [] })
    if (url === '/v1/accounts' && request.method === 'GET') {
      reads++
      return onAccounts ? onAccounts(reads) : respond({ accounts })
    }
    if (url === '/v1/accounts') return respond(stopped({ user_name: 'created' }), 201)
    if (url === '/v1/account') return respond({ ...stopped(), locked_margin: 0, available: 8991, unrealized_pnl: 0, equity: 8991 })
    if (url === '/v1/accounts/acc_stop/trading/resume' && request.method === 'POST') {
      calls.push(request.body ?? null)
      return Promise.resolve(onResume(request.body ?? null))
    }
    return respond({})
  })
  return { fetchMock, calls, readCount: () => reads }
}

function clickResumeButton() {
  const button = [...document.querySelectorAll('button')].find((b) => b.textContent.includes('解除風控停止') || b.textContent.includes('解除中'))
  if (!button) throw new Error('resume button not found')
  button.click()
  return button
}

it('確認文案說明重新起算；確認後 POST 無 payload，成功 reload 清掉停止標記', async () => {
  session.token = 'user-token'
  const accounts = [stopped()]
  const resumed = stopped({ max_dd_stopped_at: null, max_dd_baseline_equity: 8991, max_dd_equity_floor: 7991 })
  const { calls } = mockFetch({
    accounts,
    onResume: (body) => {
      expect(body).toBeNull()
      accounts[0] = resumed
      return respond(resumed, 200)
    },
  })
  await mountPage(accounts)
  clickResumeButton()
  await nextTick()
  await vi.waitFor(() => expect(dialog.open).toBe(true))
  await vi.waitFor(() => expect(dialog.lines.join(' ')).toContain('沿用 max_dd 和原始額度，從本次全平後權益重新起算'))
  settle(true)
  await vi.waitFor(() => expect(calls.length).toBe(1))
  // 成功後 reload：Detail 停止說明、列表 tag、解除按鈕都消失
  await vi.waitFor(() => expect(document.body.textContent).not.toContain('風控停止：'))
  const row = [...document.querySelectorAll('tr[data-click]')].find((tr) => tr.textContent.includes('Stopped Agent'))
  expect(row.querySelector('.tag[data-tone=neg]')).toBeNull()
  expect([...document.querySelectorAll('button')].some((b) => b.textContent.includes('解除風控停止'))).toBe(false)
})

it('取消確認不 POST，停止標記保留', async () => {
  session.token = 'user-token'
  const accounts = [stopped()]
  const { calls } = mockFetch({ accounts, onResume: () => respond(stopped(), 200) })
  await mountPage(accounts)
  clickResumeButton()
  await nextTick()
  await vi.waitFor(() => expect(dialog.open).toBe(true))
  await vi.waitFor(() => expect(dialog.lines.join(' ')).toContain('從本次全平後權益重新起算'))
  settle(null)
  await nextTick()
  expect(calls.length).toBe(0)
  expect(document.body.textContent).toContain('風控停止：')
})

it('pending 中按鈕 disabled 不可重送', async () => {
  session.token = 'user-token'
  const accounts = [stopped()]
  let resolvePending
  const pending = new Promise((resolve) => { resolvePending = resolve })
  const { calls } = mockFetch({ accounts, onResume: () => pending.then(() => respond(stopped(), 200)) })
  await mountPage(accounts)
  clickResumeButton()
  await nextTick()
  await vi.waitFor(() => expect(dialog.open).toBe(true))
  settle(true)
  await vi.waitFor(() => expect(calls.length).toBe(1))
  const button = [...document.querySelectorAll('button')].find((b) => b.textContent.includes('解除中'))
  expect(button).toBeTruthy()
  expect(button.disabled).toBe(true)
  button.click()
  expect(calls.length).toBe(1)
  resolvePending()
  await vi.waitFor(() => expect(toast.text).toContain('已解除風控停止'))
  expect(calls.length).toBe(1)
})

it('409/liquidation_pending 保留停止狀態與錯誤理由', async () => {
  const accounts = [stopped()]
  const { calls, readCount } = mockFetch({ accounts, onResume: () => respond({ error: 'liquidation_pending', message: '尚有部位或未成交掛單' }, 409) })
  await mountPage(accounts)
  clickResumeButton()
  await nextTick()
  await vi.waitFor(() => expect(dialog.open).toBe(true))
  settle(true)
  await vi.waitFor(() => expect(toast.text).toContain('尚有部位或未成交掛單'))
  expect(calls).toHaveLength(1)
  expect(readCount()).toBe(1)
  expect(document.body.textContent).toContain('風控停止：')
})

it('POST 已提交但回應遺失時先 GET；成功重查後解除按鈕消失', async () => {
  const accounts = [stopped()]
  const { calls, readCount } = mockFetch({ accounts, onResume: () => {
    accounts[0] = stopped({ max_dd_stopped_at: null, max_dd_baseline_equity: 8991 })
    throw new TypeError('network disconnected')
  } })
  await mountPage(accounts)
  clickResumeButton()
  await nextTick()
  await vi.waitFor(() => expect(dialog.open).toBe(true))
  settle(true)
  await vi.waitFor(() => expect(readCount()).toBe(2))
  await vi.waitFor(() => expect(document.body.textContent).not.toContain('風控停止：'))
  expect(calls).toHaveLength(1)
  expect([...document.querySelectorAll('button')].some((b) => b.textContent.includes('解除風控停止'))).toBe(false)
})

it('POST 未提交但斷網時 GET 後仍停止；不自動重送', async () => {
  const accounts = [stopped()]
  const { calls, readCount } = mockFetch({ accounts, onResume: () => { throw new TypeError('network disconnected') } })
  await mountPage(accounts)
  clickResumeButton()
  await nextTick()
  await vi.waitFor(() => expect(dialog.open).toBe(true))
  settle(true)
  await vi.waitFor(() => expect(readCount()).toBe(2))
  expect(document.body.textContent).toContain('風控停止：')
  expect(calls).toHaveLength(1)
})

it('不明結果後 GET 失敗時顯示載入錯誤', async () => {
  const accounts = [stopped()]
  const { calls, readCount } = mockFetch({ accounts, onResume: () => respond({}, 502), onAccounts: (reads) => reads === 1 ? respond({ accounts }) : respond({ error: 'internal_error', message: '載入失敗' }, 503) })
  await mountPage(accounts)
  clickResumeButton()
  await nextTick()
  await vi.waitFor(() => expect(dialog.open).toBe(true))
  settle(true)
  await vi.waitFor(() => expect(readCount()).toBe(2))
  await vi.waitFor(() => expect(document.body.textContent).toContain('載入失敗'))
  expect(calls).toHaveLength(1)
})
