import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '../src/core/api.js'
import { session } from '../src/core/session.js'

function respond(status, body) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  session.token = 'ut_me'
  vi.spyOn(location, 'assign').mockImplementation(() => {})
})

afterEach(() => vi.restoreAllMocks())

it('帶 Authorization 與 JSON body，成功直接回後端 JSON', async () => {
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(201, { id: 'acc_1' }))
  expect(await api('POST', '/v1/accounts', { user_name: 'A' })).toEqual({ id: 'acc_1' })

  const [url, init] = fetch.mock.calls[0]
  expect(url).toBe('/v1/accounts')
  expect(init.method).toBe('POST')
  expect(init.headers.Authorization).toBe('Bearer ut_me')
  expect(init.headers['Content-Type']).toBe('application/json')
  expect(init.body).toBe('{"user_name":"A"}')
})

it('沒有 token 就不帶 Authorization，也不帶 Content-Type', async () => {
  session.token = null
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(200, { messages: [] }))
  await api('GET', '/v1/messages')
  expect(fetch.mock.calls[0][1].headers).toEqual({})
})

it('204 回 null', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
  expect(await api('DELETE', '/v1/accounts/acc_1')).toBeNull()
})

it('錯誤信封變成帶 status、code、message 的 Error', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(409, { error: 'name_taken', message: '名稱已被使用' }))
  const err = await api('POST', '/v1/accounts', {}).catch((e) => e)
  expect(err).toBeInstanceOf(Error)
  expect(err.status).toBe(409)
  expect(err.code).toBe('name_taken')
  expect(err.message).toBe('名稱已被使用')
  expect(location.assign).not.toHaveBeenCalled()
})

it('自己的 token 收到 401 會登出', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(401, { error: 'unauthorized', message: 'token 無效' }))
  await api('GET', '/v1/accounts').catch(() => {})
  expect(location.assign).toHaveBeenCalledWith('/login')
  expect(sessionStorage.getItem('token')).toBeNull()
})

it('代打用的帳號 token 收到 401 只丟錯，不登出', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(401, { error: 'unauthorized', message: 'token 無效' }))
  const err = await api('GET', '/v1/account', undefined, 'at_other').catch((e) => e)
  expect(err.status).toBe(401)
  expect(location.assign).not.toHaveBeenCalled()
})
