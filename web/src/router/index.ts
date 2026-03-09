import type { Pinia } from 'pinia'
import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes = [
  {
    path: '/login',
    component: () => import('@/layouts/AuthLayout.vue'),
    children: [{ path: '', name: 'login', component: () => import('@/views/LoginView.vue') }],
  },
  {
    path: '/admin',
    component: () => import('@/layouts/AdminLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      { path: '', redirect: '/admin/sandboxes' },
      { path: 'sandboxes', name: 'admin-sandboxes', component: () => import('@/views/admin/SandboxListView.vue') },
      { path: 'sandboxes/:id', name: 'admin-sandbox-detail', component: () => import('@/views/admin/SandboxDetailView.vue') },
      { path: 'datasets', name: 'admin-datasets', component: () => import('@/views/admin/DatasetsView.vue') },
      { path: 'accounts', name: 'admin-accounts', component: () => import('@/views/admin/AccountsView.vue') },
      { path: 'live-accounts', name: 'admin-live-accounts', component: () => import('@/views/admin/LiveAccountsView.vue') },
      { path: 'system-health', name: 'admin-system-health', component: () => import('@/views/admin/SystemHealthView.vue') },
    ],
  },
  {
    path: '/monitor',
    component: () => import('@/layouts/MonitorLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      { path: '', name: 'monitor-overview', component: () => import('@/views/monitor/GlobalOverviewView.vue') },
      { path: 'sandboxes/:id', name: 'monitor-sandbox', component: () => import('@/views/monitor/SandboxMonitorView.vue') },
      { path: 'performance', name: 'monitor-performance', component: () => import('@/views/monitor/PerformanceView.vue') },
      { path: 'activity', name: 'monitor-activity', component: () => import('@/views/monitor/ActivityView.vue') },
      { path: 'alerts', name: 'monitor-alerts', component: () => import('@/views/monitor/AlertsView.vue') },
    ],
  },
  { path: '/', redirect: '/admin/sandboxes' },
]

export function createAppRouter(pinia: Pinia) {
  const router = createRouter({
    history: createWebHistory(),
    routes,
  })

  router.beforeEach(async (to) => {
    const auth = useAuthStore(pinia)
    if (!to.meta.requiresAuth) {
      if (to.path === '/login' && auth.isAuthenticated) {
        return '/admin/sandboxes'
      }
      return true
    }

    const hasSession = await auth.probeSession()
    if (!hasSession) {
      return '/login'
    }

    return true
  })

  return router
}
