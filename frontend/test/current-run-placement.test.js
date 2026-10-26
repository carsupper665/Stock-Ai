import { afterEach, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import Page from '../src/pages/session-detail/Page.vue'

let app
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.restoreAllMocks() })
const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const run = { run_id: 2, status: 'running', trigger: 'event', start_time: '2026-10-26T00:00:00Z', decision_count: 3, model_call_count: 4, tool_call_count: 2, progress: [{ type: 'model_call', context_tokens: 72614, context_window: 1000000, usage: { input_tokens: 72000, output_tokens: 614 } }] }
async function mountPage(status, current, tab = 'runs') {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (url.endsWith('/runs/current')) return current ? response(current) : response({ error: 'CURRENT_RUN_NOT_FOUND' }, 404)
    if (url.includes('/runs?')) return response({ runs: [run] })
    if (url.endsWith('/conversation')) return response({ messages: [], estimated_tokens: 0 })
    if (url.endsWith('/memories')) return response({ memories: [], capacity: 60 })
    if (url.endsWith('/usage')) return response({ by_model: [], unknown_attempts: {} })
    if (url.endsWith('/tools/stats')) return response({ tools: [] })
    if (url.endsWith('/ledger')) return response({ entries: [] })
    if (url.endsWith('/positions')) return response({ positions: [] })
    return response({ id: 's1', name: 'Session', status, current_run_id: 2, max_loop: 10, max_tool_call: 20, active_event_count: 1, pending_trigger_count: 2 })
  })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: {} }] })
  await router.push(`/sessions/s1?tab=${tab}`); await router.isReady()
  const root = document.createElement('div'); document.body.append(root)
  app = createApp(Page, { id: 's1' }); app.use(router); app.mount(root)
  await vi.waitFor(() => expect(root.querySelector('.band')).not.toBeNull())
  return root
}

it.each([
  ['running', run, '#2'], ['running', null, 'running · 沒有進行中的 Run'],
  ['stopped', null, '未執行 · 沒有進行中的 Run'], ['deleted', null, '已刪除 · 不能 start'],
])('puts one current Run card above the left list: %s %o', async (status, current, text) => {
  const root = await mountPage(status, current)
  expect(root.querySelectorAll('.band')).toHaveLength(1)
  const band = root.querySelector('.run-left .band')
  expect(band).not.toBeNull()
  expect(band.textContent).toContain(text)
  expect(band.compareDocumentPosition(root.querySelector('.list')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  if (current) {
    expect(band.querySelectorAll('.bar')).toHaveLength(2)
    expect([...band.querySelectorAll('.tokens b')].map((b) => b.textContent)).toEqual(['72.6K / 1000K（7%）'])
  }
})

it.each(['overview', 'conversation', 'memory', 'monitor'])('keeps the full-width current Run before tabs: %s', async (tab) => {
  const root = await mountPage('running', run, tab)
  const band = root.querySelector('.band')
  expect(root.querySelectorAll('.band')).toHaveLength(1)
  expect(band.closest('.run-left')).toBeNull()
  expect(band.compareDocumentPosition(root.querySelector('.tabs')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})
