import { clear, saveGatewayUser } from '../session.js'

export async function login({ returnTo = '/' } = {}) {
	location.assign(`/oauth2/start?return=${encodeURIComponent(returnTo)}`)
}

export async function currentUser() {
	const response = await fetch('/oauth2/me', { credentials: 'same-origin', headers: { Accept: 'application/json' } })
	if (!response.ok) throw new Error('登入已失效，請重新登入')
	const user = await response.json()
	saveGatewayUser(user)
	return user
}

export function logout() {
	clear()
	const form = document.createElement('form')
	form.method = 'POST'
	form.action = '/oauth2/logout'
	form.hidden = true
	document.body.append(form)
	form.submit()
}
