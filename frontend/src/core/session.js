import { reactive } from 'vue'

export const session = reactive({
  // Test seam only: the production OIDC flow never assigns an access token here.
  token: null,
  name: null,
  authenticated: false,
  expiresAt: null,
  initialized: false,
})

let expiryTimer

export function isAuthenticated() {
  return Boolean(session.token) || session.authenticated
}

export function saveGatewayUser(user) {
  clearTimeout(expiryTimer)
  const expiresAt = Date.parse(user.expires_at)
  Object.assign(session, { token: null, name: user.user_name, authenticated: true, expiresAt, initialized: true })
  if (Number.isFinite(expiresAt)) expiryTimer = setTimeout(() => expire(), Math.max(0, expiresAt - Date.now()))
}

export function clear() {
  clearTimeout(expiryTimer)
  Object.assign(session, { token: null, name: null, authenticated: false, expiresAt: null, initialized: true })
}

export function expire() {
  clear()
  location.assign('/login?expired=1')
}

export async function restore() {
  if (session.initialized) return isAuthenticated()
  try {
    const response = await fetch('/oauth2/me', { credentials: 'same-origin', headers: { Accept: 'application/json' } })
    if (!response.ok) throw new Error('unauthenticated')
    saveGatewayUser(await response.json())
    return true
  } catch {
    clear()
    return false
  }
}
