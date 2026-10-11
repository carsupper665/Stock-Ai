import { expire, session } from './session.js'

const base = import.meta.env.VITE_API_BASE ?? ''

// 三個服務三種錯誤信封：交易後端 {error, message}、Agent Server {status_code, error, msg}、LLM Server {error: {code, message}}
const codeOf = (d) => (typeof d.error === 'string' ? d.error : d.error?.code)
const messageOf = (d) => d.message ?? d.msg ?? d.error?.message

// In production the Gateway cookie authenticates the browser at the Console boundary.
// Explicit bearer values here are Account tokens; private-service credentials never enter the bundle.
async function request(method, path, body, token, sessionBound) {
  const consoleRoute = path.startsWith('/agent/') || path.startsWith('/llm/')
  if (consoleRoute) {
    token = session.token
    sessionBound = true
  }
  const headers = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(base + path, { method, headers, credentials: 'same-origin', body: body === undefined ? undefined : JSON.stringify(body) })
  return { res, sessionBound }
}

async function responseError(res, sessionBound) {
  // 後端掛了時代理回的是空的或 HTML 的 502，不能讓 JSON 解析錯誤蓋掉真正的狀態碼
  const data = await res.json().catch(() => ({}))
  // Gateway session request 的 401 才結束登入；明確傳入的 Account token 401 只回報錯誤。
  if (res.status === 401 && sessionBound) expire()
  throw Object.assign(new Error(messageOf(data) ?? `${res.status} ${res.statusText}`), { status: res.status, code: codeOf(data) })
}

export async function api(method, path, body, token) {
  const sessionBound = token === undefined
  if (token === undefined) token = session.token
  const response = await request(method, path, body, token, sessionBound)
  if (response.res.status === 204) return null
  if (!response.res.ok) return responseError(response.res, response.sessionBound)
  return response.res.json()
}

export async function apiFile(path, token) {
  const sessionBound = token === undefined
  if (token === undefined) token = session.token
  const response = await request('GET', path, undefined, token, sessionBound)
  if (!response.res.ok) return responseError(response.res, response.sessionBound)
  return response.res.blob()
}
