export default {
  path: '/health',
  name: 'health',
  component: () => import('./Page.vue'),
  meta: { title: '狀態', nav: 90 },
}
