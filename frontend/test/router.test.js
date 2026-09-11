import { beforeAll, beforeEach, expect, it } from 'vitest'

import { router } from '../src/core/router.js'
import { session } from '../src/core/session.js'

beforeAll(() => {
  router.addRoute({ path: '/secret', name: 'secret', component: { template: '<div />' } })
})

beforeEach(() => {
  session.token = null
})

it('沒登入開受保護的頁面會被送去 login，並記住原路徑', async () => {
  await router.push('/secret?tab=1')
  expect(router.currentRoute.value.name).toBe('login')
  expect(router.currentRoute.value.query.redirect).toBe('/secret?tab=1')
})

it('登入後受保護的頁面進得去', async () => {
  session.token = 'ut_me'
  await router.push('/secret')
  expect(router.currentRoute.value.name).toBe('secret')
})

it('login 免登入', async () => {
  await router.push('/login')
  expect(router.currentRoute.value.name).toBe('login')
})

it('不認得的路徑不會 404', async () => {
  session.token = 'ut_me'
  await router.push('/no/such/page')
  expect(router.currentRoute.value.matched.length).toBeGreaterThan(0)
  expect(router.currentRoute.value.path).not.toBe('/no/such/page')
})
