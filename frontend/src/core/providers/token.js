import { api } from '../api.js'

export async function login({ token }) {
  // 只有 USER Token 能列帳號：200 就是對的，401／403 由 api() 丟出
  await api('GET', '/v1/accounts', undefined, token)
  return { token, name: 'USER' }
}
