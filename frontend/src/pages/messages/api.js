import { api } from '../../core/api.js'

export function listMessages({ page = 1, sort = 'desc', tag = '' } = {}) {
  const query = new URLSearchParams({ page: String(page), sort })
  if (tag) query.set('tag', tag)
  return api('GET', `/v1/messages?${query}`)
}

export const postMessage = (content, tags) => api('POST', '/v1/messages', { content, tags })
export const deleteMessage = (id) => api('DELETE', `/v1/messages/${encodeURIComponent(id)}`)
export const listAccounts = () => api('GET', '/v1/accounts')
export const muteChat = (id, body) => api('POST', `/v1/accounts/${encodeURIComponent(id)}/chat/mute`, body)
export const unmuteChat = (id) => api('DELETE', `/v1/accounts/${encodeURIComponent(id)}/chat/mute`)
