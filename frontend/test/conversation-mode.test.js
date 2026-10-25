import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import NewSession from '../src/pages/sessions/NewSession.vue'
import Overview from '../src/pages/session-detail/Overview.vue'

let app
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.restoreAllMocks() })
const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
function mount(component, props) {
  const root = document.createElement('div'); document.body.append(root)
  app = createApp(component, props); app.mount(root)
  return root
}

it.each([undefined, { name: 'original', conversation_mode: 'verbatim', model_name: 'model' }])('creates Sessions with the selected or copied immutable mode: %o', async (preset) => {
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, options) => {
    if (options.method === 'POST') return response({ id: 'new', name: 'new' }, 201)
    if (url.includes('/accounts')) return response({ accounts: [{ id: 'acc_1', user_name: 'one', status: 'active', chat_read: true, chat_post: true }] })
    if (url.includes('/models')) return response({ models: [] })
    return response({ sessions: [] })
  })
  const created = vi.fn()
  const root = mount(NewSession, { preset, onCreated: created })
  await vi.waitFor(() => expect(root.querySelector('option[value="acc_1"]')).not.toBeNull())
  const mode = root.querySelector('select[name=conversation_mode]')
  expect(mode.value).toBe(preset ? 'verbatim' : 'semantic')
  expect(mode.textContent).toContain('語意保留')
  expect(mode.textContent).toContain('原文接續')
  const labels = [...root.querySelectorAll('label')]
  for (const [prefix, value] of [['name ', 'new'], ['account_id', 'acc_1'], ['model_name', 'model']]) {
    const field = labels.find((label) => label.textContent.trim().startsWith(prefix.trim())).querySelector('input, select')
    field.value = value; field.dispatchEvent(new Event(field.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
  }
  if (!preset) { mode.value = 'verbatim'; mode.dispatchEvent(new Event('change', { bubbles: true })) }
  await nextTick()
  root.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await vi.waitFor(() => expect(created).toHaveBeenCalled())
  const [, options] = fetch.mock.calls.find(([, options]) => options.method === 'POST')
  expect(JSON.parse(options.body)).toMatchObject({ name: 'new', account_id: 'acc_1', model_name: 'model', conversation_mode: 'verbatim' })
})

it.each([['semantic', '語意保留'], ['verbatim', '原文接續']])('shows the Session conversation mode in the overview: %s', (mode, label) => {
  const root = mount(Overview, { session: { id: 's', conversation_mode: mode }, runs: [] })
  expect(root.querySelector('.config').textContent).toContain(`Session 對話模式${label}`)
  expect(root.querySelector('.config').textContent).toContain('建立後不可改')
})
