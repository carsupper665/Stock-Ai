import { api } from '../../core/api.js'

export { listBindings } from '../sessions/api.js'

// 只帶有值的參數：limit、before、after、sort、tag、page。
export function listMessages(params = {}) {
  const query = new URLSearchParams(Object.entries(params).filter(([, value]) => value !== undefined && value !== ''))
  return api('GET', `/v1/messages?${query}`)
}

export const postMessage = (content, tags) => api('POST', '/v1/messages', { content, tags })
export const deleteMessage = (id) => api('DELETE', `/v1/messages/${encodeURIComponent(id)}`)
export const clearMessages = () => api('DELETE', '/v1/messages')
export const listAccounts = () => api('GET', '/v1/accounts')
export const me = () => api('GET', '/v1/me')
export const muteChat = (id, body) => api('POST', `/v1/accounts/${encodeURIComponent(id)}/chat/mute`, body)
export const unmuteChat = (id) => api('DELETE', `/v1/accounts/${encodeURIComponent(id)}/chat/mute`)
