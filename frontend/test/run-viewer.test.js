import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

import { steps } from '../src/shared/run.js'
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

it('run viewer omits call arguments and only exposes actual model content or reasoning', () => {
  const history = steps({ progress: [
    {
      type: 'model_call', iteration: 1, model_name: 'analysis-model', provider_id: 'provider', model: 'actual-model',
      content: 'Visible answer', finish_reason: 'tool_call',
      tool_calls: [{ name: 'get_price', arguments: { token: 'must-not-render', symbol: 'BTCUSDT' } }],
    },
    { type: 'model_call', iteration: 2, reasoning: 'Provider supplied reasoning', content: null, tool_calls: [] },
  ] })

  expect(history[0]).toMatchObject({ kind: 'model', content: 'Visible answer', reasoning: '', tools: ['get_price'] })
  expect(history[1].reasoning).toBe('Provider supplied reasoning')
	 expect(history[0].status).toBe('completed')
  expect(JSON.stringify(history)).not.toContain('must-not-render')
  expect(JSON.stringify(history)).not.toContain('BTCUSDT')
})

it('shows a sanitized failed model-attempt status without leaking error details or arguments', () => {
  const [attempt] = steps({ progress: [{
    type: 'model_call', iteration: 1, model_name: 'analysis-model',
    error: 'PROVIDER_UNAVAILABLE: upstream included secret-token',
    tool_calls: [{ name: 'get_price', arguments: { token: 'hidden-argument' } }],
  }] })

  expect(attempt).toMatchObject({ kind: 'model', status: 'failed', tone: 'neg', error: { code: 'PROVIDER_UNAVAILABLE' } })
  expect(JSON.stringify(attempt)).not.toContain('secret-token')
  expect(JSON.stringify(attempt)).not.toContain('hidden-argument')
})

it('run viewer projects tool results into human-readable fields without raw JSON or arguments', () => {
  const [tool] = steps({ progress: [{
    type: 'tool_call', iteration: 3, tool_name: 'get_positions', arguments: { account_id: 'secret-account' },
    result: { ok: true, result: { positions: [{ symbol: 'BTCUSDT', quantity: 2 }], has_more: false } },
  }] })

  expect(tool).toMatchObject({ kind: 'tool', title: 'tool · get_positions', meta: 'ok', filtered: true })
  expect(tool.fields).toEqual([
    { label: 'positions [1] · symbol', value: 'BTCUSDT' },
    { label: 'positions [1] · quantity', value: '2' },
    { label: 'has_more', value: 'false' },
  ])
  expect(JSON.stringify(tool)).not.toContain('secret-account')
})

it('renders tool results and model text as independently collapsed sections', () => {
  const run = {
    run_id: 1, status: 'completed', trigger: 'session_start', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z',
    tool_call_count: 1, model_call_count: 1,
    progress: [
      { type: 'model_call', iteration: 1, content: 'Visible model content', tool_calls: [{ name: 'get_price', arguments: { secret: 'hidden-call-argument' } }] },
      { type: 'tool_call', iteration: 1, tool_name: 'get_price', arguments: { secret: 'hidden-tool-argument' }, result: { ok: true, result: { symbol: 'BTCUSDT', price: 60000 } } },
      { type: 'tool_call', iteration: 1, tool_name: 'get_account_state', arguments: { secret: 'other-hidden-argument' }, result: { ok: true, result: { balance: 97500 } } },
    ],
  }
  mountRuns(run)

  const results = document.querySelectorAll('details.tool-result')
  expect(results).toHaveLength(2)
  expect(results[0].open).toBe(false)
  expect(results[1].open).toBe(false)
  const modelText = document.querySelectorAll('details.model-text')
  expect(modelText).toHaveLength(1)
  expect(modelText[0].open).toBe(false)
  results[0].querySelector('summary').click()
  expect(results[0].open).toBe(true)
  expect(results[1].open).toBe(false)
  expect(document.body.textContent).toContain('Visible model content')
  expect(document.body.textContent).toContain('BTCUSDT')
  expect(document.body.textContent).not.toContain('hidden-call-argument')
  expect(document.body.textContent).not.toContain('hidden-tool-argument')
  expect(document.body.textContent).not.toContain('other-hidden-argument')
})

it('renders a finalization envelope as readable fields instead of raw JSON', () => {
  const content = JSON.stringify({ output: 'finished', summary: 'short summary', candidate_memories: [], expire_memories: [] })
  const [attempt] = steps({ progress: [{ type: 'model_call', attempt_type: 'terminal', attempt: 2, iteration: 3, content, finish_reason: 'stop', tool_calls: [] }] })
  expect(attempt.content).toBe('')
  expect(attempt.step).toBe('terminal')
  expect(attempt.meta).toContain('終結 attempt 2 / 4')
  expect(attempt.envelope).toContainEqual({ label: 'output', value: 'finished' })
  expect(attempt.envelope).toContainEqual({ label: 'summary', value: 'short summary' })
  expect(JSON.stringify(attempt)).not.toContain('candidate_memories":[]')
})

it('keeps terminal attempts separate from decisions and only renders supplied model diagnostics', () => {
  const history = steps({ progress: [
    { type: 'model_call', iteration: 3, attempt: 2, content: '', tool_calls: [], finish_reason: 'tool_call' },
    { type: 'model_call', iteration: 3, attempt_type: 'terminal', attempt: 1, error: 'INVALID_RESPONSE: repair', session_mode: 'resume', invocation_count: 2 },
    { type: 'model_call', iteration: 3, attempt_type: 'terminal', attempt: 2, content: 'done', tool_calls: [], finish_reason: 'stop', fallback_reason: 'resume_not_found' },
  ] })

  expect(history.map((entry) => entry.step)).toEqual(['decision 3', 'terminal', 'terminal'])
  expect(history[1].meta).toContain('終結 attempt 1 / 4')
  expect(history[2].meta).toContain('終結 attempt 2 / 4')
  expect(history[1].diagnostics).toEqual([
    { label: 'session mode', value: 'resume' },
    { label: 'invocations', value: '2' },
  ])
  expect(history[2].diagnostics).toEqual([{ label: 'fallback reason', value: 'resume_not_found' }])
  expect(JSON.stringify(history[0].diagnostics)).not.toContain('session mode')
})

it('bounds long Run lists and timelines while keeping every large payload collapsed', () => {
  const text = 'long trace '.repeat(4000)
  const progress = Array.from({ length: 120 }, (_, index) => index % 2
    ? { type: 'tool_call', iteration: index, tool_name: 'get_price', result: { ok: true, result: { value: text } } }
    : { type: 'model_call', iteration: index, reasoning: text, content: text, tool_calls: [] })
  const run = { run_id: 120, status: 'completed', trigger: 'event', start_time: '2026-09-16T00:00:00Z', end_time: '2026-09-16T00:00:01Z', progress, output: text }
  const runs = Array.from({ length: 100 }, (_, index) => ({ ...run, run_id: index + 21, progress: index === 99 ? progress : [] }))
  mountRuns(runs.at(-1), { id: 'session_1' }, runs)

  expect(document.querySelector('.list-scroll')).not.toBeNull()
  expect(document.querySelector('.timeline')).not.toBeNull()
  expect(document.querySelectorAll('details.tool-result')).toHaveLength(60)
  expect(document.querySelectorAll('details.model-text')).toHaveLength(120)
  expect([...document.querySelectorAll('details')].every((item) => !item.open)).toBe(true)
  expect(document.querySelector('.final-output').open).toBe(false)
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
    method: 'GET', headers: { Authorization: 'Bearer console-user' },
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
  expect(request.headers.Authorization).toBe('Bearer console-user')
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
