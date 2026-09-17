import { logout, session } from './session.js'

const base = import.meta.env.VITE_API_BASE ?? ''

// 三個服務三種錯誤信封：交易後端 {error, message}、Agent Server {status_code, error, msg}、LLM Server {error: {code, message}}
const codeOf = (d) => (typeof d.error === 'string' ? d.error : d.error?.code)
const messageOf = (d) => d.message ?? d.msg ?? d.error?.message

// Private-service routes authenticate the browser with its USER token. The console
// replaces it with the service credential; those credentials never enter the bundle.
async function request(method, path, body, token) {
  const consoleRoute = path.startsWith('/agent/') || path.startsWith('/llm/')
  if (consoleRoute) token = session.token
  const headers = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  return { res, token }
}

async function responseError(res, token) {
  // 後端掛了時代理回的是空的或 HTML 的 502，不能讓 JSON 解析錯誤蓋掉真正的狀態碼
  const data = await res.json().catch(() => ({}))
  // 只有自己的 session token 失效才登出；代打用的帳號 token 或代理注入憑證的 401 只回報錯誤
  if (res.status === 401 && token && token === session.token) logout()
  throw Object.assign(new Error(messageOf(data) ?? `${res.status} ${res.statusText}`), { status: res.status, code: codeOf(data) })
}

export async function api(method, path, body, token = session.token) {
  const response = await request(method, path, body, token)
  if (response.res.status === 204) return null
  if (!response.res.ok) return responseError(response.res, response.token)
  return response.res.json()
}

export async function apiFile(path, token = session.token) {
  const response = await request('GET', path, undefined, token)
  if (!response.res.ok) return responseError(response.res, response.token)
  return response.res.blob()
}
