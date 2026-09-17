import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

import Monitor from '../src/pages/session-detail/Monitor.vue'

let app
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

const response = (body) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(Monitor, { session: { id: 'session_1' } })
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.mount(root)
}

it('uses independent monitor stacks with Positions before Ledger and locally bounded tables', async () => {
  const tools = Array.from({ length: 80 }, (_, index) => ({
    tool_name: `tool_${index}`, call_count: index, success_count: index, error_count: 0, in_flight_count: 0,
    avg_latency_ms: 11, max_latency_ms: 22, last_latency_ms: 17,
  }))
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (url.endsWith('/usage')) return response({ by_model: [], unknown_attempts: { input_tokens: 0, output_tokens: 1, cached_tokens: 2, total_tokens: 3 } })
    if (url.endsWith('/tools/stats')) return response({ tools })
    if (url.endsWith('/positions')) return response({ positions: [], observed_at: '2026-09-17T08:09:10Z' })
    return response({ entries: [], observed_at: '2026-09-17T08:09:11Z' })
  })
  mount()
  await vi.waitFor(() => expect(document.body.textContent).toContain('tool_79'))

  const tradingSections = [...document.querySelectorAll('.trading-stack > section')]
  const metricSections = [...document.querySelectorAll('.metric-stack > section')]
  expect(tradingSections.map((item) => [...item.classList].find((name) => ['positions', 'ledger'].includes(name)))).toEqual(['positions', 'ledger'])
  expect(metricSections.map((item) => [...item.classList].find((name) => ['usage', 'tools'].includes(name)))).toEqual(['usage', 'tools'])
  expect(document.body.textContent).toContain('目前沒有未平倉部位。')
  expect(document.body.textContent).toContain('沒有帳戶事件。')
  expect(document.body.textContent).toContain('尚無模型呼叫用量。')
  expect(document.body.textContent).toContain('最後 ms')
  expect(document.body.textContent).toContain('17')
  expect(document.body.textContent).toContain('Output 未回報次數')
  expect(document.body.textContent).not.toContain('"output_tokens"')
  expect(document.querySelectorAll('.tools-scroll tr')).toHaveLength(81)
  expect(document.body.textContent).toContain('上次成功')
  expect(document.querySelector('.positions').textContent).toContain('資料 ')
  expect(document.querySelector('.positions').textContent).toContain(' · 取得 ')
  expect(document.querySelector('.ledger').textContent).toContain('資料 ')
})

it('renders resolved Positions while unrelated monitoring requests are still deferred', async () => {
  const requests = {
    usage: deferred(), tools: deferred(), positions: deferred(), ledger: deferred(),
  }
  vi.spyOn(globalThis, 'fetch').mockImplementation((url) => {
    if (url.endsWith('/usage')) return requests.usage.promise
    if (url.endsWith('/tools/stats')) return requests.tools.promise
    if (url.endsWith('/positions')) return requests.positions.promise
    return requests.ledger.promise
  })
  mount()
  requests.positions.resolve(response({ positions: [{ id: 'p1', symbol: 'BTCUSDT', side: 'long', quantity: 1, entry_price: 60000, mark_price: 61000, stop_loss: null, take_profit: null, unrealized_pnl: 1000 }] }))
  await vi.waitFor(() => expect(document.body.textContent).toContain('BTCUSDT'))

  expect(document.querySelector('.positions').getAttribute('aria-busy')).toBe('false')
  expect(document.querySelector('.tools').getAttribute('aria-busy')).toBe('true')
  expect(document.querySelector('.usage').getAttribute('aria-busy')).toBe('true')
  expect(document.body.textContent).toContain('載入 Tool 統計中…')

  requests.usage.resolve(response({ by_model: [], unknown_attempts: {} }))
  requests.tools.resolve(response({ tools: [] }))
  requests.ledger.resolve(response({ entries: [] }))
  await nextTick()
})

it('keeps the last successful Tool data visible on refresh error and provides another refresh action', async () => {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (url.endsWith('/tools/stats')) {
      return response({ tools: [{ tool_name: 'get_price', call_count: 1, success_count: 1, error_count: 0, in_flight_count: 0, avg_latency_ms: 10, max_latency_ms: 10, last_latency_ms: 10 }] })
    }
    if (url.endsWith('/usage')) return response({ by_model: [], unknown_attempts: {} })
    if (url.endsWith('/positions')) return response({ positions: [] })
    return response({ entries: [] })
  })
  mount()
  await vi.waitFor(() => expect(document.body.textContent).toContain('get_price'))
  const refresh = document.querySelector('.tools button')
  const failed = new Response(JSON.stringify({ error: 'UNAVAILABLE', msg: 'temporary failure' }), { status: 503, headers: { 'Content-Type': 'application/json' } })
  globalThis.fetch.mockImplementationOnce(async () => failed)
  refresh.click()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Tool 統計載入失敗：temporary failure'))

  expect(document.body.textContent).toContain('get_price')
  expect(refresh.disabled).toBe(false)
})
