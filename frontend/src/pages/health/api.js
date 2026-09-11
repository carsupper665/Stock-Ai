import { api } from '../../core/api.js'

export const getHealth = () => api('GET', '/v1/health')
