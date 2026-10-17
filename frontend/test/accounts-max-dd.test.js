import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import AccountsPage from '../src/pages/accounts/Page.vue'
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

const accountFixture = (over = {}) => ({
  id: 'acc_1',
  user_name: 'Research Agent',
  initial_balance: 10000,
  balance: 10000,
  status: 'active',
  created_at: '2026-09-23T00:00:00Z',
  token: 'account-token-abcdef',
  max_dd: null,
  max_dd_baseline_equity: null,
  max_dd_stopped_at: null,
  max_dd_loss_limit: null,
  max_dd_equity_floor: null,
  ...over,
})

async function mountPage() {
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
}

// 開啟建立 Modal 並填入表單；maxDD 為 null 時留空。
async function fillCreateForm(userName, maxDD) {
  ;[...document.querySelectorAll('button')].find((b) => b.textContent.includes('建立帳號')).click()
  await nextTick()
  const inputs = document.querySelector('form').querySelectorAll('input')
  inputs[0].value = userName
  inputs[0].dispatchEvent(new Event('input', { bubbles: true }))
  if (maxDD != null) {
    inputs[2].value = String(maxDD)
    inputs[2].dispatchEvent(new Event('input', { bubbles: true }))
  }
  await nextTick()
}

async function submitCreate(posts) {
  document.querySelector('form button[data-tone=acc]').click()
  await vi.waitFor(() =>
    expect(document.body.textContent.includes('max_dd 必須大於 0、不超過 100') || posts.length > 0).toBe(true),
  )
}

function mockFetch({ initialAccounts = [], postResponse = null } = {}) {
  let created = false
  const posts = []
  const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request = {}) => {
    if (url.startsWith('/agent/')) return respond({ sessions: [] })
    if (url === '/v1/accounts' && request.method === 'POST') {
      const body = JSON.parse(request.body)
      posts.push(body)
      created = true
      return respond(postResponse ?? accountFixture({ user_name: body.user_name }), 201)
    }
    if (url === '/v1/accounts') return respond({ accounts: created ? [postResponse ?? accountFixture({ user_name: 'created' }), ...initialAccounts] : initialAccounts })
    if (url === '/v1/account') return respond({ ...accountFixture(), locked_margin: 0, available: 10000, unrealized_pnl: 0, equity: 10000 })
    return respond({})
  })
  return { fetchMock, posts }
}

const seeded = [accountFixture()]

it('max_dd 留空時 request body 不含 max_dd 欄位', async () => {
  session.token = 'user-token'
  const { posts } = mockFetch({ initialAccounts: seeded })
  await mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Research Agent'))
  await fillCreateForm('New Agent', null)
  await submitCreate(posts)
  expect(posts).toHaveLength(1)
  expect('max_dd' in posts[0]).toBe(false)
})

it('輸入 10 與 0.5 時送出 JSON number', async () => {
  for (const value of [10, 0.5]) {
    document.body.innerHTML = ''
    app?.unmount()
    app = null
    toast.text = ''
    session.token = 'user-token'
    const { posts } = mockFetch({ initialAccounts: seeded })
    await mountPage()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Research Agent'))
    await fillCreateForm(`Agent-${value}`, value)
    await submitCreate(posts)
    expect(posts).toHaveLength(1)
    expect(posts[0].max_dd).toBe(value)
    expect(typeof posts[0].max_dd).toBe('number')
  }
})

it('0、負、>100 不送 request 並顯示錯誤', async () => {
  for (const value of [0, -5, 101]) {
    document.body.innerHTML = ''
    app?.unmount()
    app = null
    toast.text = ''
    session.token = 'user-token'
    const { posts } = mockFetch({ initialAccounts: seeded })
    await mountPage()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Research Agent'))
    await fillCreateForm('Bad Agent', value)
    await submitCreate(posts)
    await vi.waitFor(() => expect(document.body.textContent).toContain('max_dd 必須大於 0、不超過 100'))
    expect(posts).toHaveLength(0)
  }
})

it('建立後 reload 顯示該戶頭的 percent 與門檻，未設定戶顯示未設定', async () => {
  session.token = 'user-token'
  const created = accountFixture({
    id: 'acc_new',
    user_name: 'New Agent',
    max_dd: 10,
    max_dd_baseline_equity: 10000,
    max_dd_loss_limit: 1000,
    max_dd_equity_floor: 9000,
  })
  const legacy = accountFixture({ id: 'acc_old', user_name: 'Legacy Agent' })
  const { posts } = mockFetch({ initialAccounts: [legacy], postResponse: created })
  await mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Legacy Agent'))
  await fillCreateForm('New Agent', 10)
  await submitCreate(posts)
  expect(posts).toHaveLength(1)
  await vi.waitFor(() => expect(document.body.textContent).toContain('New Agent'))
  await vi.waitFor(() => expect(document.body.textContent).toContain('10%'))
  // Detail 顯示該戶自己的 baseline／loss_limit／floor
  await vi.waitFor(() => expect(document.body.textContent).toContain('loss_limit 1,000.00'))
  expect(document.body.textContent).toContain('floor 9,000.00')
  // 未設定戶不能殘留新戶的 10%
  expect(document.body.textContent).toContain('未設定')
})

it('風控停止的帳號：列表顯示風控停止標記，Detail 顯示停止時間', async () => {
  session.token = 'user-token'
  const stopped = accountFixture({
    id: 'acc_stop',
    user_name: 'Stopped Agent',
    max_dd: 10,
    max_dd_baseline_equity: 10000,
    max_dd_loss_limit: 1000,
    max_dd_equity_floor: 9000,
    balance: 8996,
    max_dd_stopped_at: '2026-09-24T08:30:00Z',
  })
  const normal = accountFixture({ id: 'acc_ok', user_name: 'Normal Agent', max_dd: 10 })
  mockFetch({ initialAccounts: [stopped, normal] })
  await mountPage()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Stopped Agent'))
  // 列表行：10% 旁有風控停止 tag；未停止戶沒有
  await vi.waitFor(() => expect(document.body.textContent).toContain('風控停止'))
  const row = [...document.querySelectorAll('tr[data-click]')].find((tr) => tr.textContent.includes('Stopped Agent'))
  expect(row.textContent).toContain('10%')
  expect(row.querySelector('.tag[data-tone=neg]')?.textContent).toBe('風控停止')
  const okRow = [...document.querySelectorAll('tr[data-click]')].find((tr) => tr.textContent.includes('Normal Agent'))
  expect(okRow.querySelector('.tag[data-tone=neg]')).toBeNull()
  // 點開 Detail：顯示停止時間與說明
  row.click()
  await vi.waitFor(() => expect(document.body.textContent).toContain('風控停止：'))
  expect(document.body.textContent).toContain('Account Token 與讀取不受影響')
})
