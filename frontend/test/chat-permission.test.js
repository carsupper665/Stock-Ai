import { afterEach, expect, it, vi } from 'vitest'
import { createApp } from 'vue'

import ChatPermission from '../src/shared/ChatPermission.vue'
import { session } from '../src/core/session.js'

let app
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

const respond = (body) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

it('shows the account chat permission and patches both switches when changed', async () => {
  session.token = 'user-token'
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => respond({ id: 'acc_1', chat_read: true, chat_post: false }))
  const changed = vi.fn()
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(ChatPermission, { account: { id: 'acc_1', chat_read: true, chat_post: true }, onChanged: changed })
  app.mount(root)

  const select = root.querySelector('select')
  expect(select.value).toBe('full')
  select.value = 'readonly'
  select.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(changed).toHaveBeenCalled())
  const [url, request] = fetch.mock.calls[0]
  expect(url).toBe('/v1/accounts/acc_1')
  expect(request.method).toBe('PATCH')
  expect(JSON.parse(request.body)).toEqual({ chat_read: true, chat_post: false })
})
