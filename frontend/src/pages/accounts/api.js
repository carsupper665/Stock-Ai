import { api } from '../../core/api.js'

export const listAccounts = () => api('GET', '/v1/accounts')
export const createAccount = (body) => api('POST', '/v1/accounts', body)
export const updateAccount = (id, body) => api('PATCH', `/v1/accounts/${id}`, body)
export const deleteAccount = (id) => api('DELETE', `/v1/accounts/${id}`)
export const resetToken = (id) => api('POST', `/v1/accounts/${id}/token/reset`)
export const listPositions = (id, product) => api('GET', `/v1/accounts/${id}/positions${product ? `?product=${product}` : ''}`)
export const listOrders = (id, status, limit) => api('GET', `/v1/accounts/${id}/orders?limit=${limit}${status ? `&status=${status}` : ''}`)
export const listTrades = (id, limit) => api('GET', `/v1/accounts/${id}/trades?limit=${limit}`)

// 待定 6：equity 等四個數字只有 Account Token 自查拿得到，用該帳號的 token 代查
export const selfAccount = (token) => api('GET', '/v1/account', undefined, token)

// 待定 2：「被哪個 Session 用」由前端拿 Agent Server 的 Session 列表反查 account_id。
// 含已刪除：綁定在審計紀錄的生命週期內不釋放，漏掉的話帳號會顯示成「未綁定」但實際不能再綁。
export { listBindings as listSessions } from '../sessions/api.js'
export const listLedger = (id, limit) => api('GET', `/v1/accounts/${id}/ledger?limit=${limit}`)
