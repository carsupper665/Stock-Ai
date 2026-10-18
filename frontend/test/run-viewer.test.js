import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

import { conversation } from '../src/shared/run.js'
import Runs from '../src/pages/session-detail/Runs.vue'
import { session } from '../src/core/session.js'

let app
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function mountRuns(run, sessionValue = { id: 'session_1' }, runs = [run]) {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(Runs, { session: sessionValue, runs, page: 1 })
  app.mount(root)
  return root
}

it('groups each tool call under the decision that requested it, keeping arguments and results for the details', () => {
  const items = conversation({ status: 'completed', progress: [
    { type: 'model_call', iteration: 1, model_name: 'analysis-model', content: 'Visible answer', finish_reason: 'tool_call', ended_at: 'x',
      tool_calls: [{ id: 'c1', name: 'get_ohlcv', arguments: { symbol: 'BTCUSDT', interval: '1h' } }] },
    { type: 'tool_call', iteration: 1, tool_call_id: 'c1', tool_name: 'get_ohlcv', arguments: { symbol: 'BTCUSDT', interval: '1h' },
      result: { ok: true, result: { candle_count: 2 } }, duration_ms: 42, ended_at: 'x' },
    { type: 'model_call', iteration: 2, reasoning: 'Provider supplied reasoning', content: null, tool_calls: [], ended_at: 'x' },
  ] })

  expect(items).toHaveLength(2)
  expect(items[0]).toMatchObject({ kind: 'model', title: '決策 1', content: 'Visible answer', thinking: false })
  expect(items[0].tools).toHaveLength(1)
  expect(items[0].tools[0]).toMatchObject({ name: 'get_ohlcv', label: '查 K 線', status: 'ok', duration: 42, action: false })
  expect(items[0].tools[0].args).toContainEqual({ label: 'symbol', value: 'BTCUSDT' })
  expect(items[0].tools[0].result).toEqual([{ label: 'candle_count', value: '2' }])
  expect(items[1].reasoning).toBe('Provider supplied reasoning')
})

it('shows a failed model attempt by code only without leaking provider error details', () => {
  const [attempt] = conversation({ progress: [{
    type: 'model_call', iteration: 1, model_name: 'analysis-model', error: 'PROVIDER_UNAVAILABLE: upstream included secret-token', tool_calls: [],
  }] })

  expect(attempt).toMatchObject({ kind: 'model', error: { code: 'PROVIDER_UNAVAILABLE' }, thinking: false })
  expect(JSON.stringify(attempt)).not.toContain('secret-token')
})

it('renders tool calls collapsed showing only what was called while model text stays readable', () => {
  const run = {
    run_id: 1, status: 'completed', trigger: 'session_start', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z',
    tool_call_count: 2, model_call_count: 1,
    progress: [
      { type: 'model_call', iteration: 1, reasoning: 'first thought\nsecond thought', content: 'Visible model content', ended_at: 'x',
        tool_calls: [{ id: 'a', name: 'get_market_snapshot' }, { id: 'b', name: 'place_order' }] },
      { type: 'tool_call', iteration: 1, tool_call_id: 'a', tool_name: 'get_market_snapshot', arguments: { symbol: 'BTCUSDT' }, result: { ok: true, result: { price: 60000 } }, ended_at: 'x' },
      { type: 'tool_call', iteration: 1, tool_call_id: 'b', tool_name: 'place_order', arguments: { side: 'buy' }, result: { ok: false, error: { code: 'MAX_DD_STOPPED', message: 'stopped', outcome: 'not_executed' } }, ended_at: 'x' },
    ],
  }
  mountRuns(run)

  const tools = document.querySelectorAll('details.tool')
  expect(tools).toHaveLength(2)
  expect([...tools].every((tool) => !tool.open)).toBe(true)
  expect(tools[0].querySelector('summary').textContent).toContain('查即時價')
  expect(tools[0].querySelector('summary').textContent).toContain('get_market_snapshot')
  expect(tools[0].querySelector('summary').textContent).not.toContain('BTCUSDT')
  expect(tools[1].hasAttribute('data-action')).toBe(true)
  expect(tools[1].dataset.status).toBe('error')
  tools[1].querySelector('summary').click()
  expect(tools[1].open).toBe(true)
  expect(tools[1].textContent).toContain('MAX_DD_STOPPED')
  expect(document.querySelector('.message > .text').textContent).toBe('Visible model content')
  const reasoning = document.querySelector('details.reasoning')
  expect(reasoning.open).toBe(false)
  expect(reasoning.querySelector('.preview').textContent).toBe('first thought')
})

it('renders the finalization envelope as a final decision card', () => {
  const content = JSON.stringify({ status: 'failed', output: 'finished', summary: 'short summary' })
  const [attempt] = conversation({ progress: [{ type: 'model_call', attempt_type: 'terminal', attempt: 2, iteration: 3, content, finish_reason: 'stop', tool_calls: [] }] })
  expect(attempt).toMatchObject({ title: '收尾', content: '' })
  expect(attempt.meta).toContain('第 2 次嘗試')
  expect(attempt.envelope).toContainEqual({ label: 'status', value: 'failed' })
  expect(attempt.envelope).toContainEqual({ label: 'output', value: 'finished' })
  expect(attempt.envelope).toContainEqual({ label: 'summary', value: 'short summary' })
})

it('animates the model that is thinking and the tool that is running only while the Run runs', () => {
  const progress = [
    { type: 'model_call', iteration: 1, content: 'checking', ended_at: 'x', tool_calls: [{ id: 'a', name: 'get_ohlcv' }] },
    { type: 'tool_call', iteration: 1, tool_call_id: 'a', tool_name: 'get_ohlcv', result: { ok: false, error: { code: 'TOOL_INTERRUPTED' } } },
    { type: 'model_call', iteration: 2, started_at: 'x' },
  ]
  const run = { run_id: 7, status: 'running', trigger: 'event', start_time: '2026-09-16T00:00:00Z', progress }
  mountRuns(run)
  expect(document.querySelectorAll('.thinking[role=status]')).toHaveLength(1)
  expect(document.querySelector('details.tool .spinner')).not.toBeNull()
  app.unmount(); document.body.innerHTML = ''
  mountRuns({ ...run, status: 'interrupted', end_time: '2026-09-16T00:00:05Z' })
  expect(document.querySelector('.thinking')).toBeNull()
  expect(document.querySelector('.spinner')).toBeNull()
  expect(document.querySelector('details.tool').dataset.status).toBe('error')
})

it('shows repairs and context compaction as short notices between messages', () => {
  const items = conversation({ progress: [
    { type: 'context_compaction', tool_name: 'context_compaction', result: { ok: true, result: { stage: 'llm' } } },
    { type: 'instruction', attempt_type: 'terminal', content: 'Terminal instruction' },
    { type: 'model_resolution', error: 'MODEL_UNAVAILABLE: model_name is currently unavailable' },
  ] })
  expect(items.map((item) => item.kind)).toEqual(['notice', 'notice', 'notice'])
  expect(items[0].text).toContain('模型摘要')
  expect(items[1].text).toContain('最終結果')
  expect(items[2]).toMatchObject({ tone: 'neg' })
  expect(items[2].text).toContain('MODEL_UNAVAILABLE')
})

it('bounds long Run lists and conversations while keeping every tool and reasoning collapsed', () => {
  const text = 'long trace '.repeat(4000)
  const progress = Array.from({ length: 120 }, (_, index) => index % 2
    ? { type: 'tool_call', iteration: index, tool_call_id: `c${index - 1}`, tool_name: 'get_market_snapshot', result: { ok: true, result: { value: text } }, ended_at: 'x' }
    : { type: 'model_call', iteration: index, reasoning: text, content: text, ended_at: 'x', tool_calls: [{ id: `c${index}`, name: 'get_market_snapshot' }] })
  const run = { run_id: 120, status: 'completed', trigger: 'event', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z', progress, output: text }
  const runs = Array.from({ length: 100 }, (_, index) => ({ ...run, run_id: index + 21, progress: index === 99 ? progress : [] }))
  mountRuns(runs.at(-1), { id: 'session_1' }, runs)

  expect(document.querySelector('.list-scroll')).not.toBeNull()
  expect(document.querySelector('.chat')).not.toBeNull()
  expect(document.querySelectorAll('details.tool')).toHaveLength(60)
  expect(document.querySelectorAll('details.reasoning')).toHaveLength(60)
  expect([...document.querySelectorAll('details')].every((item) => !item.open)).toBe(true)
})

it('downloads the authenticated whole History shard with a safe filename and revokes its URL', async () => {
  session.token = 'console-user'
  const run = { run_id: 21, status: 'completed', trigger: 'event', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z', progress: [] }
  const body = JSON.stringify({ session_id: 'unsafe', start_run_id: 21, end_run_id: 40, runs: [run] })
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(body, { headers: { 'Content-Type': 'application/json' } }))
  const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:history')
  const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  let clicked
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function () { clicked = { href: this.href, download: this.download } })
  mountRuns(run, { id: 'unsafe/../<session>' })

  ;[...document.querySelectorAll('button')].find((button) => button.textContent === '下載 History 分片').click()
  await vi.waitFor(() => expect(clicked).toBeTruthy())

  expect(fetch).toHaveBeenCalledWith('/agent/api/v1/sessions/unsafe%2F..%2F%3Csession%3E/runs/21/history', expect.objectContaining({
    method: 'GET', headers: {},
  }))
  expect(clicked).toEqual({ href: 'blob:history', download: 'history-unsafe_session_-runs-21-40.json' })
  expect(createObjectURL).toHaveBeenCalledWith(expect.any(Blob))
  await vi.waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith('blob:history'))
})

it('does not download or show a stale History result after the selected Run changes', async () => {
  session.token = 'console-user'
  const pending = deferred()
  vi.spyOn(globalThis, 'fetch').mockReturnValue(pending.promise)
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  const run1 = { run_id: 1, status: 'completed', trigger: 'event', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z', progress: [] }
  const run2 = { ...run1, run_id: 2 }
  mountRuns(run2, { id: 'session_1' }, [run1, run2])
  document.querySelectorAll('tbody tr')[0].click()
  await nextTick()
  ;[...document.querySelectorAll('button')].find((button) => button.textContent === '下載 History 分片').click()
  await vi.waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1))
  document.querySelectorAll('tbody tr')[1].click()
  pending.resolve(new Response(JSON.stringify({ error: 'HISTORY_UNAVAILABLE', msg: 'old selection failed' }), { status: 503, headers: { 'Content-Type': 'application/json' } }))
  await pending.promise
  await new Promise((resolve) => setTimeout(resolve, 10))

  expect(click).not.toHaveBeenCalled()
  expect(document.querySelector('[role=alert]')).toBeNull()
  expect(document.body.textContent).not.toContain('old selection failed')
})

it('marks running History unavailable without issuing a request', () => {
  const fetch = vi.spyOn(globalThis, 'fetch')
  const run = { run_id: 3, status: 'running', trigger: 'event', start_time: '2026-09-16T00:00:00Z', progress: [] }
  mountRuns(run)
  const button = [...document.querySelectorAll('button')].find((item) => item.textContent === '下載 History 分片')
  expect(button.disabled).toBe(true)
  expect(button.title).toContain('Run 結束後')
  button.click()
  expect(fetch).not.toHaveBeenCalled()
})

it('Retry button sends the failed Run retry action and reports the new retry Run', async () => {
  session.token = 'console-user'
  const failed = {
    run_id: 4, status: 'failed', trigger: 'event', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z',
    tool_call_count: 1, decision_count: 1, model_call_count: 4, error: 'GATEWAY_ERROR: PROVIDER_UNAVAILABLE: temporary', progress: [],
  }
  const retried = { ...failed, run_id: 5, status: 'running', trigger: 'manual_retry', end_time: null, retry_of_run_id: 4, retry_attempt: 1, error: null }
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(retried), { status: 202, headers: { 'Content-Type': 'application/json' } }))
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(Runs, { session: { id: 'session_1', status: 'running', current_run_id: 4 }, runs: [failed], page: 1 })
  app.mount(root)

  const button = [...document.querySelectorAll('button')].find((item) => item.textContent === 'Retry')
  expect(button.disabled).toBe(false)
  button.click()
  await vi.waitFor(() => expect(fetch).toHaveBeenCalled())
  const [url, request] = fetch.mock.calls.find(([, options]) => options.method === 'POST')
  expect(url).toBe('/agent/api/v1/sessions/session_1/runs/4/retry')
  expect(request.headers.Authorization).toBeUndefined()
  await nextTick()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Retry 1 / 3'))
})

it('labels a unified position_close trigger with identity and only an evidenced reason', () => {
  const run = {
    run_id: 2, status: 'completed', trigger: 'event', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z',
    tool_call_count: 0, model_call_count: 1, progress: [],
    triggers: [{
      type: 'position_close', created_run_id: 1, fired_at: '2026-09-16T00:00:00Z',
      matched: { position_id: 'pos_1', symbol: 'BTCUSDT', product: 'futures', side: 'long', close_reason: 'take_profit' },
    }],
  }
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(Runs, { session: { id: 'session_1' }, runs: [run], page: 1 })
  app.mount(root)

  expect(document.body.textContent).toContain('position_close')
  expect(document.body.textContent).toContain('position pos_1 · BTCUSDT · futures · long · reason take_profit')
  expect(document.body.textContent).not.toContain('SL_hit')
  expect(document.body.textContent).not.toContain('TP_hit')
})
