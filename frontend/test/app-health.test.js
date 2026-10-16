import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import App from '../src/App.vue'
import { routes } from '../src/core/pages.js'
import { clear, saveGatewayUser } from '../src/core/session.js'

let app
afterEach(() => { app?.unmount(); app = null; document.body.innerHTML = ''; clear(); vi.restoreAllMocks() })

async function mountAt(path) {
  // 只用 nav 的具名路由讓側欄連結可解析；目前頁面是空元件，不會觸發頁面自己的請求
  const router = createRouter({ history: createMemoryHistory(), routes: [...routes, { path: '/probe-target', component: { render: () => null }, meta: { public: true } }] })
  await router.push(path)
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(App).use(router)
  app.mount(root)
  await nextTick()
}

const healthCalls = (fetch) => fetch.mock.calls.map(([url]) => url).filter((url) => /\/(v1\/health|agent\/health|llm\/healthz)$/.test(url))

it('未登入時不查詢服務健康端點，避免 401 觸發登出重載循環', async () => {
  clear()
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{}', { status: 401 }))
  await mountAt('/probe-target')
  expect(healthCalls(fetch)).toEqual([])
})

it('登入後才查詢服務健康端點', async () => {
  saveGatewayUser({ user_name: 'Alice', expires_at: new Date(Date.now() + 3600e3).toISOString() })
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{"status":"ok"}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  await mountAt('/probe-target')
  expect(healthCalls(fetch).sort()).toEqual(['/agent/health', '/llm/healthz', '/v1/health'])
})
