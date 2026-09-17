import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import ModelEditor from '../src/pages/providers/ModelEditor.vue'
import { session } from '../src/core/session.js'
import Memories from '../src/pages/session-detail/Memories.vue'
import ProvidersPage from '../src/pages/providers/Page.vue'
import ProviderDetail from '../src/pages/providers/Detail.vue'
import { createRouter, createMemoryHistory } from 'vue-router'
import { downloadHistory, getLedger, getToolStats, getUsage } from '../src/pages/session-detail/api.js'

let app
afterEach(() => { app?.unmount(); app = null; document.body.innerHTML = ''; vi.restoreAllMocks() })
const respond = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
function mount(component, props) { const root = document.createElement('div'); document.body.append(root); app = createApp(component, props); app.mount(root) }
async function fill(element, value) { element.value = value; element.dispatchEvent(new Event('input', { bubbles: true })); await nextTick() }

it('新增模型送到管理端點，保留 Provider 對應、模型及 level options', async () => {
  session.token = 'console-user'
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond({ model_name: 'new-choice' }, 201))
  const saved = vi.fn()
  mount(ModelEditor, { providers: [{ id: 'new-provider', name: 'New', enabled: true }], providerId: 'new-provider', onSaved: saved })
  const inputs = document.querySelectorAll('input')
  await fill(inputs[0], 'new-choice')
  await fill(inputs[1], 'actual-model')
  await fill(document.querySelectorAll('textarea')[1], '{"high":{"reasoning_effort":"high"}}')
  document.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await vi.waitFor(() => expect(saved).toHaveBeenCalledWith({ model_name: 'new-choice' }))
  const [url, request] = fetch.mock.calls[0]
  expect(url).toBe('/llm/v1/model-catalog')
  expect(request.headers.Authorization).toBe('Bearer console-user')
  expect(JSON.parse(request.body)).toMatchObject({ model_name: 'new-choice', provider_id: 'new-provider', model: 'actual-model', levels: { high: { reasoning_effort: 'high' } } })
})

it('模型 options 非物件時顯示錯誤，不發送請求', async () => {
  const fetch = vi.spyOn(globalThis, 'fetch')
  mount(ModelEditor, { providers: [] })
  await fill(document.querySelector('textarea'), 'null')
  document.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await nextTick()
  expect(document.querySelector('[role=alert]').textContent).toContain('必須是 JSON 物件')
  expect(fetch).not.toHaveBeenCalled()
})

it('Memory 列表不載入全文，選取後才呼叫 detail', async () => {
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => respond(url.includes('?') ? { memories: [{ id: 'm_1', summary: '可讀摘要', type: 'fact', importance: 2, run_id: 1 }] } : { id: 'm_1', summary: '可讀摘要', content: '完整記憶正文', run_id: 1 }))
  mount(Memories, { session: { id: 's_1', status: 'stopped' } })
  await vi.waitFor(() => expect(document.body.textContent).toContain('可讀摘要'))
  expect(fetch).toHaveBeenCalledTimes(1)
  const row = [...document.querySelectorAll('button')].find((b) => b.textContent.includes('可讀摘要'))
  row.click()
  await vi.waitFor(() => expect(document.body.textContent).toContain('完整記憶正文'))
  expect(fetch.mock.calls[1][0]).toBe('/agent/api/v1/sessions/s_1/memories/m_1')
})

it('History 與監控使用 Agent HTTP，不在瀏覽器取得 Account Token', async () => {
  session.token = 'console-user'
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => respond({}))
  await downloadHistory('s_1', 21)
  await getLedger('s_1', 99)
  await getUsage('s_1')
  await getToolStats('s_1')
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(['/agent/api/v1/sessions/s_1/runs/21/history', '/agent/api/v1/sessions/s_1/ledger?before_seq=99', '/agent/api/v1/sessions/s_1/usage', '/agent/api/v1/sessions/s_1/tools/stats'])
  expect(fetch.mock.calls.every(([, request]) => request.headers.Authorization === 'Bearer console-user')).toBe(true)
})

it('新增 Provider 可選 Codex／Claude Code，CLI 模式不要求 URL', async () => {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => respond(url.endsWith('/harnesses') ? { harnesses: [
    { type: 'codex', executable_configured: true, local_login_configured: true },
    { type: 'claude_code', executable_configured: true, local_login_configured: false },
  ] } : url.endsWith('/models') ? { models: [] } : []))
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: ProvidersPage }] })
  await router.push('/')
  await router.isReady()
  const root = document.createElement('div'); document.body.append(root)
  app = createApp(ProvidersPage); app.use(router); app.mount(root)
  await vi.waitFor(() => expect(document.body.textContent).toContain('Server CLI 狀態'))
  ;[...document.querySelectorAll('button')].find((b) => b.textContent === '+ 新增').click()
  await nextTick()
  const type = document.querySelector('form select')
  expect([...type.options].map((o) => o.value)).toContain('codex')
  expect([...type.options].map((o) => o.value)).toContain('claude_code')
  type.value = 'codex'; type.dispatchEvent(new Event('change')); await nextTick()
  expect(document.querySelector('form input[type=url]')).toBeNull()
  expect([...document.querySelectorAll('form input')].some((i) => i.value === 'cli-default')).toBe(true)
  expect(document.querySelectorAll('form select')[1].value).toBe('local_login')
})

it('Provider 測試按鈕實際發送一次請求並顯示模型回應', async () => {
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond({ content: 'CLI 已連通', tool_calls: [], finish_reason: 'stop', usage: null }))
  mount(ProviderDetail, { provider: { id:'codex-test', type:'codex', name:'Codex', enabled:true, default_model:'cli-default', tokens:[], config:{auth_mode:'local_login'} }, harnesses:[] })
  ;[...document.querySelectorAll('button')].find((b) => b.textContent === '發送一次測試請求').click()
  await vi.waitFor(() => expect(document.body.textContent).toContain('CLI 已連通'))
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(fetch.mock.calls[0][0]).toBe('/llm/v1/providers/codex-test/test')
})
