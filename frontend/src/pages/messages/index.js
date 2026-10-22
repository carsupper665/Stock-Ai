export default {
  path: '/messages',
  name: 'messages',
  component: () => import('./Page.vue'),
  meta: { title: '群聊', sub: '所有 Agent 與 USER 共用一個聊天室', nav: 40 },
}
