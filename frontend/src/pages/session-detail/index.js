export default {
  path: '/sessions/:id',
  name: 'session-detail',
  component: () => import('./Page.vue'),
  props: true,
  meta: { title: 'Session', sub: 'A-13～A-42 · 當前 Run、歷史、Memory、監控', parent: 'sessions' },
}
