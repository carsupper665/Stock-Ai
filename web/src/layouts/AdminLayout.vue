<template>
  <div class="shell">
    <aside class="sidebar panel">
      <div>
        <p class="eyebrow">管理控制台</p>
        <h1>交易沙盒控制台</h1>
      </div>
      <nav>
        <RouterLink v-for="item in navItems" :key="item.to" :to="item.to" class="nav-link" active-class="is-active">
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>
    </aside>
    <div class="content">
      <header class="topbar panel">
        <div>
          <p class="eyebrow">工作階段</p>
          <strong>{{ auth.user?.username ?? '管理員工作階段' }}</strong>
        </div>
        <ConnectionStatusBanner :state="monitor.connectionState" :last-message-at="monitor.lastMessageAt" />
      </header>
      <RouterView />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import ConnectionStatusBanner from '@/components/common/ConnectionStatusBanner.vue'
import { useAuthStore } from '@/stores/auth'
import { useMonitorStore } from '@/stores/monitor'

const auth = useAuthStore()
const monitor = useMonitorStore()

const navItems = [
  { to: '/admin/sandboxes', label: '沙盒' },
  { to: '/admin/datasets', label: '回放資料集' },
  { to: '/admin/accounts', label: '帳戶與權杖' },
  { to: '/admin/live-accounts', label: '即時帳戶' },
  { to: '/admin/agent-boundary', label: 'Agent 邊界' },
  { to: '/admin/system-health', label: '系統健康' },
]

onMounted(() => {
  void auth.probeSession()
  void monitor.connectGlobal()
})
</script>

<style scoped>
.shell {
  min-height: 100vh;
  display: grid;
  grid-template-columns: 280px minmax(0, 1fr);
  gap: 1rem;
  padding: 1rem;
}
.sidebar {
  padding: 1.25rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.topbar {
  display: grid;
  gap: 1rem;
  padding: 1rem;
  margin-bottom: 1rem;
}
.content {
  min-width: 0;
}
.nav-link {
  display: flex;
  padding: 0.85rem 1rem;
  border-radius: 14px;
  color: var(--muted);
}
.nav-link.is-active,
.nav-link:hover {
  color: var(--text);
  background: rgba(78, 161, 255, 0.12);
}
.eyebrow {
  margin: 0 0 0.35rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--muted);
  font-size: 0.75rem;
}
@media (max-width: 1024px) {
  .shell {
    grid-template-columns: 1fr;
  }
  .sidebar {
    position: sticky;
    top: 0;
    z-index: 10;
  }
}
</style>
