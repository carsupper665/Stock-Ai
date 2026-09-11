export default {
  path: '/login',
  name: 'login',
  component: () => import('./Page.vue'),
  meta: { title: '登入', public: true },
}
