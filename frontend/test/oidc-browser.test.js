import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import * as oidc from '../src/core/providers/oidc.js'
import { clear, saveGatewayUser, session } from '../src/core/session.js'

const user = { subject: 'subject-1', user_name: 'Alice', email: 'alice@example.test', expires_at: new Date(Date.now() + 3_600_000).toISOString() }
const response = () => new Response(JSON.stringify(user), { status: 200, headers: { 'Content-Type': 'application/json' } })

beforeEach(() => {
	clear()
	sessionStorage.clear()
	localStorage.clear()
	vi.spyOn(location, 'assign').mockImplementation(() => {})
})

afterEach(() => {
	vi.useRealTimers()
	vi.restoreAllMocks()
	document.body.innerHTML = ''
})

it('first login navigates to the Gateway and never accepts a browser token', async () => {
	await oidc.login({ returnTo: '/accounts?tab=1' })
	expect(location.assign).toHaveBeenCalledWith('/oauth2/start?return=%2Faccounts%3Ftab%3D1')
	expect(sessionStorage.length).toBe(0)
	expect(localStorage.length).toBe(0)
})

it('current user and refresh use only the HttpOnly-cookie endpoint', async () => {
	const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => response())
	await oidc.currentUser()
	await oidc.currentUser()
	expect(session.name).toBe('Alice')
	expect(session.authenticated).toBe(true)
	expect(fetch).toHaveBeenCalledTimes(2)
	for (const [path, options] of fetch.mock.calls) {
		expect(path).toBe('/oauth2/me')
		expect(options.credentials).toBe('same-origin')
		expect(options.headers.Authorization).toBeUndefined()
	}
	expect(JSON.stringify(session)).not.toContain('access_token')
	expect(sessionStorage.length).toBe(0)
})

it('client-side expiry clears identity and returns to login', () => {
	vi.useFakeTimers()
	const now = Date.now()
	vi.setSystemTime(now)
	saveGatewayUser({ ...user, expires_at: new Date(now + 1000).toISOString() })
	vi.advanceTimersByTime(1001)
	expect(session.authenticated).toBe(false)
	expect(location.assign).toHaveBeenCalledWith('/login?expired=1')
})

it('logout is a same-origin POST navigation and clears frontend identity', () => {
	saveGatewayUser(user)
	const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {})
	oidc.logout()
	expect(session.authenticated).toBe(false)
	expect(submit).toHaveBeenCalledOnce()
	const form = document.querySelector('form')
	expect(form.method.toLowerCase()).toBe('post')
	expect(form.getAttribute('action')).toBe('/oauth2/logout')
	expect(sessionStorage.length).toBe(0)
})
