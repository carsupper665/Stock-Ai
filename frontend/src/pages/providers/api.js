import { api } from '../../core/api.js'

// LLM Provider Server 管理 API：admin 憑證由代理注入；/v1/models 由代理改注入 runtime 憑證
const llm = (method, path, body) => api(method, `/llm/v1${path}`, body, null)

export const listProviders = () => llm('GET', '/providers')
export const createProvider = (body) => llm('POST', '/providers', body)
export const updateProvider = (id, body) => llm('PATCH', `/providers/${id}`, body)
export const deleteProvider = (id) => llm('DELETE', `/providers/${id}`)
export const listTokens = (id) => llm('GET', `/providers/${id}/tokens`)
export const createToken = (id, body) => llm('POST', `/providers/${id}/tokens`, body)
export const updateToken = (id, tokenId, body) => llm('PATCH', `/providers/${id}/tokens/${tokenId}`, body)
export const deleteToken = (id, tokenId) => llm('DELETE', `/providers/${id}/tokens/${tokenId}`)
export const listModels = () => llm('GET', '/models')
export const createModel = (body) => llm('POST', '/model-catalog', body)
export const updateModel = (name, body) => llm('PUT', `/model-catalog/${encodeURIComponent(name)}`, body)
export const deleteModel = (name) => llm('DELETE', `/model-catalog/${encodeURIComponent(name)}`)
export const harnessStatus = () => llm('GET', '/harnesses')
export const testProvider = (id, body) => llm('POST', `/providers/${encodeURIComponent(id)}/test`, body)
