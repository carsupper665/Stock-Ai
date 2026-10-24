import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import App from '../src/App.vue'
import { routes } from '../src/core/pages.js'
import { clear, saveGatewayUser } from '../src/core/session.js'

let app
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  clear()
  vi.restoreAllMocks()
  try { localStorage.clear() } catch { /* ignore */ }
})

async function mountApp() {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ status: 'ok', version: '0.1.2-b0r0' }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  saveGatewayUser({ user_name: 'Alice', expires_at: new Date(Date.now() + 3600e3).toISOString() })
  const router = createRouter({ history: createMemoryHistory(), routes: [...routes, { path: '/probe-target', component: { render: () => null }, meta: { public: true } }] })
  await router.push('/probe-target')
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(App).use(router)
  app.mount(root)
  await nextTick()
  return root
}

it('collapses and restores the sidebar and remembers the choice', async () => {
  const root = await mountApp()
  const toggle = root.querySelector('header button[aria-expanded]')
  expect(root.querySelector('aside').style.display).toBe('')
  toggle.click()
  await nextTick()
  expect(root.querySelector('aside').style.display).toBe('none')
  expect(localStorage.getItem('sidebar')).toBe('hidden')
  toggle.click()
  await nextTick()
  expect(root.querySelector('aside').style.display).toBe('')
})

it('still toggles when the browser refuses storage', async () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
  const root = await mountApp()
  root.querySelector('header button[aria-expanded]').click()
  await nextTick()
  expect(root.querySelector('aside').style.display).toBe('none')
})
