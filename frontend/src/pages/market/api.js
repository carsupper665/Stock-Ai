import { api } from '../../core/api.js'

export const getPrice = (market, symbol) => api('GET', `/v1/market/price?market=${market}&symbol=${encodeURIComponent(symbol)}`)
export const listSubscriptions = () => api('GET', '/v1/market/subscriptions')
