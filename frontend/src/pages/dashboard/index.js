export default {
  path: '/dashboard',
  name: 'dashboard',
  component: () => import('./Page.vue'),
  meta: { title: '交易監控', nav: 5 },
}
