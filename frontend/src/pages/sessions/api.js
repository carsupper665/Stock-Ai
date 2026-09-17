import { api } from '../../core/api.js'

// Agent Server 管理 API：憑證由代理注入，token 傳 null
const agent = (method, path, body) => api(method, `/agent/api/v1${path}`, body, null)

export const listSessions = (page = 1, includeDeleted = false) => agent('GET', `/sessions?page=${page}&include_deleted=${includeDeleted}`)

// 綁定在 audit 紀錄的生命週期內都不釋放（A-14），所以要判斷帳號能不能綁，必須連已刪除的 Session 一起看
export async function listBindings() {
  const sessions = []
  for (let page = 1; ; page++) {
    const result = await listSessions(page, true)
    sessions.push(...result.sessions)
    if (result.sessions.length < 100) return { sessions }
  }
}
export const createSession = (body) => agent('POST', '/sessions', body)
export const startSession = (id) => agent('POST', `/sessions/${id}/start`)
export const stopSession = (id) => agent('POST', `/sessions/${id}/stop`)
export const listRuns = (id, page = 1) => agent('GET', `/sessions/${id}/runs?page=${page}`)
export const currentRun = (id) => agent('GET', `/sessions/${id}/runs/current`)

export const listAccounts = () => api('GET', '/v1/accounts')
export const listPositions = (accountId) => api('GET', `/v1/accounts/${accountId}/positions`)
export const listOrders = (accountId) => api('GET', `/v1/accounts/${accountId}/orders?limit=200`)

export const listModels = () => api('GET', '/llm/v1/models', undefined, null)
