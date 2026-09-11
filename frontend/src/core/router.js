import { createRouter, createWebHistory } from 'vue-router'

import { nav, routes } from './pages.js'
import { session } from './session.js'

// 首頁是選單第一項；還沒有任何頁面時退到登入頁
const home = { name: nav[0]?.name ?? 'login' }

export const router = createRouter({
  history: createWebHistory(),
  routes: [...routes, { path: '/', redirect: home }, { path: '/:pathMatch(.*)*', redirect: '/' }],
})

router.beforeEach((to) => {
  if (to.meta.public || session.token) return true
  return { name: 'login', query: { redirect: to.fullPath } }
})
