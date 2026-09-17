import { api, apiFile } from '../../core/api.js'

// Agent Server 管理 API：憑證由代理注入，token 傳 null
const agent = (method, path, body) => api(method, `/agent/api/v1${path}`, body, null)

export const getSession = (id) => agent('GET', `/sessions/${id}`)
export const startSession = (id) => agent('POST', `/sessions/${id}/start`)
export const stopSession = (id) => agent('POST', `/sessions/${id}/stop`)
export const deleteSession = (id) => agent('DELETE', `/sessions/${id}`)
export const listRuns = (id, page = 1) => agent('GET', `/sessions/${id}/runs?page=${page}`)
export const getRun = (id, run) => agent('GET', `/sessions/${id}/runs/${run}`)
export const retryRun = (id, run) => agent('POST', `/sessions/${id}/runs/${run}/retry`)
export const currentRun = (id) => agent('GET', `/sessions/${id}/runs/current`)
export const downloadHistory = (id, run) => apiFile(`/agent/api/v1/sessions/${encodeURIComponent(id)}/runs/${run}/history`, null)
export const listMemories = (id, page = 1, expired = false) => agent('GET', `/sessions/${id}/memories?page=${page}&include_expired=${expired}`)
export const getMemory = (id, memory) => agent('GET', `/sessions/${id}/memories/${encodeURIComponent(memory)}`)
export const expireMemory = (id, memory) => agent('DELETE', `/sessions/${id}/memories/${encodeURIComponent(memory)}`)
export const getUsage = (id) => agent('GET', `/sessions/${id}/usage`)
export const getToolStats = (id) => agent('GET', `/sessions/${id}/tools/stats`)
export const getPositions = (id) => agent('GET', `/sessions/${id}/positions`)
export const getLedger = (id, before) => agent('GET', `/sessions/${id}/ledger${before ? `?before_seq=${before}` : ''}`)

// 交易後端：帳號本體（含 token）用 USER；equity 等四個數字只有 Account Token 自查拿得到（待定 6）
export const getAccount = (id) => api('GET', `/v1/accounts/${id}`)
export const selfAccount = (token) => api('GET', '/v1/account', undefined, token)
export const listPositions = (accountId) => api('GET', `/v1/accounts/${accountId}/positions`)

// Event（tickets 27～31）：只有 Agent 能建立；USER 只能看與刪
export const listEvents = (id) => agent('GET', `/sessions/${id}/events`)
export const deleteEvent = (id, eventId) => agent('DELETE', `/sessions/${id}/events/${eventId}`)

// Ledger（tickets 22～24）：USER 端點，依 session_id 篩出這個 Session 造成的帳戶事件
export const listLedger = (accountId, sessionId) => api('GET', `/v1/accounts/${accountId}/ledger?limit=50&session_id=${encodeURIComponent(sessionId)}`)
