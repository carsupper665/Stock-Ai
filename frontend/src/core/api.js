import { logout, session } from './session.js'

const base = import.meta.env.VITE_API_BASE ?? ''

export async function api(method, path, body, token = session.token) {
  const headers = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  if (res.status === 204) return null
  // 後端掛了時代理回的是空的或 HTML 的 502，不能讓 JSON 解析錯誤蓋掉真正的狀態碼
  const data = await res.json().catch(() => ({}))
  if (res.ok) return data

  // 只有自己的 session token 失效才登出；代打用的帳號 token 401 只回報錯誤
  if (res.status === 401 && token && token === session.token) logout()
  throw Object.assign(new Error(data.message ?? `${res.status} ${res.statusText}`), { status: res.status, code: data.error })
}
