import { logout, session } from './session.js'

const base = import.meta.env.VITE_API_BASE ?? ''

export async function api(method, path, body, token = session.token) {
  const headers = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  if (res.status === 204) return null
  const data = await res.json()
  if (res.ok) return data

  // 只有自己的 session token 失效才登出；代打用的帳號 token 401 只回報錯誤
  if (res.status === 401 && token && token === session.token) logout()
  throw Object.assign(new Error(data.message), { status: res.status, code: data.error })
}
