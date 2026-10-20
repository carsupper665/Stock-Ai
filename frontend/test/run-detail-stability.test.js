import { afterEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import Page from '../src/pages/session-detail/Page.vue'
import Runs from '../src/pages/session-detail/Runs.vue'

let app
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const respond = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const completedRun = (over = {}) => ({
  run_id: 3, status: 'completed', trigger: 'event', start_time: '2026-10-20T00:00:00Z', end_time: '2026-10-20T00:00:09Z',
  progress: [
    { type: 'model_call', iteration: 1, reasoning: 'thinking about the price', content: 'Checked the market.', ended_at: 'x', tool_calls: [] },
  ],
  ...over,
})

// happy-dom does no layout: the .chat box grows 400px per message, plus whatever a test expands.
function geometry(element, sizes) {
  Object.defineProperty(element, 'scrollHeight', { configurable: true, get: () => 1600 + 400 * element.querySelectorAll('.message').length + sizes.expanded })
  Object.defineProperty(element, 'clientHeight', { configurable: true, get: () => 500 })
}

function mountRuns(state) {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(Runs, { session: { id: 'session_1' }, runs: state.runs, page: 1, initial: state.initial }) })
  app.mount(root)
  return nextTick()
}

it('leaves the reader where they scrolled or expanded when a poll brings no new Run content', async () => {
  const state = reactive({ runs: [completedRun()] })
  await mountRuns(state)
  const chat = document.querySelector('.chat')
  const sizes = { expanded: 0 }
  geometry(chat, sizes)

  // Scrolled up a little from the bottom, as a trackpad does.
  chat.scrollTop = 1450
  chat.dispatchEvent(new Event('scroll'))
  state.runs = [completedRun()]
  await nextTick(); await nextTick()
  expect(chat.scrollTop).toBe(1450)

  // At the bottom, then a <details> opens: the box grows without any scroll event.
  chat.scrollTop = 1500
  chat.dispatchEvent(new Event('scroll'))
  sizes.expanded = 1000
  state.runs = [completedRun()]
  await nextTick(); await nextTick()
  expect(chat.scrollTop).toBe(1500)
})

it('follows new Run content only while the reader is at the bottom', async () => {
  const progress = [{ type: 'model_call', iteration: 1, content: 'first', ended_at: 'x', tool_calls: [] }]
  const running = (entries) => completedRun({ status: 'running', end_time: null, progress: entries })
  const state = reactive({ runs: [running(progress)] })
  await mountRuns(state)
  const chat = document.querySelector('.chat')
  geometry(chat, { expanded: 0 })

  chat.scrollTop = 1500
  chat.dispatchEvent(new Event('scroll'))
  state.runs = [running([...progress, { type: 'model_call', iteration: 2, content: 'second', ended_at: 'x', tool_calls: [] }])]
  await nextTick(); await nextTick()
  expect(chat.scrollTop).toBe(2400)

  chat.scrollTop = 600
  chat.dispatchEvent(new Event('scroll'))
  state.runs = [running([...progress, { type: 'model_call', iteration: 2, content: 'second', ended_at: 'x', tool_calls: [] }, { type: 'model_call', iteration: 3, content: 'third', ended_at: 'x', tool_calls: [] }])]
  await nextTick(); await nextTick()
  expect(chat.scrollTop).toBe(600)
})

it('keeps refreshing a running Run that is not on the current page', async () => {
  let version = 0
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
    version++
    return respond(completedRun({ run_id: 5, status: 'running', end_time: null, progress: [
      { type: 'model_call', iteration: version, content: `step ${version}`, ended_at: 'x', tool_calls: [] },
    ] }))
  })
  const state = reactive({ runs: [], initial: '5' })
  mountRuns(state)
  await vi.waitFor(() => expect(document.querySelector('.message .text')?.textContent).toBe('step 1'))

  state.runs = []
  await vi.waitFor(() => expect(document.querySelector('.message .text')?.textContent).toBe('step 2'))
  expect(fetch).toHaveBeenCalledTimes(2)
})

it('keeps the Run view mounted with its open details when one poll fails', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  let fail = false
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (fail) return respond({ error: 'upstream_unavailable', message: 'Service is unavailable.' }, 502)
    if (url.endsWith('/runs/current')) return respond({ status_code: 404, error: 'CURRENT_RUN_NOT_FOUND', msg: 'none' }, 404)
    if (url.includes('/runs')) return respond({ runs: [completedRun()], page: 1, page_size: 100 })
    return respond({ id: 's1', name: 'Session', status: 'running', model_name: 'm', max_loop: 10, max_tool_call: 10, current_run_id: 3, total_runs: 1 })
  })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  router.push('/sessions/s1?tab=runs')
  await router.isReady()
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(Page, { id: 's1' })
  app.use(router)
  app.mount(root)
  await vi.waitFor(() => expect(document.querySelector('details.reasoning')).not.toBeNull())
  const reasoning = document.querySelector('details.reasoning')
  reasoning.querySelector('summary').click()
  expect(reasoning.open).toBe(true)

  fail = true
  vi.advanceTimersByTime(3000)
  await vi.waitFor(() => expect(document.body.textContent).toContain('Service is unavailable.'))
  expect(document.querySelector('details.reasoning')).toBe(reasoning)
  expect(reasoning.open).toBe(true)

  fail = false
  vi.advanceTimersByTime(3000)
  await vi.waitFor(() => expect(document.body.textContent).not.toContain('Service is unavailable.'))
  expect(document.querySelector('details.reasoning')).toBe(reasoning)
  expect(reasoning.open).toBe(true)
})
