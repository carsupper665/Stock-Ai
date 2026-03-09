<template>
  <div class="monitor-shell">
    <header class="panel monitor-topbar">
      <div>
        <p class="eyebrow">Monitor Workspace</p>
        <h1>Realtime Operations Console</h1>
      </div>
      <nav class="monitor-nav">
        <RouterLink to="/monitor" class="nav-link" active-class="is-active">Overview</RouterLink>
        <RouterLink to="/monitor/performance" class="nav-link" active-class="is-active">Performance</RouterLink>
        <RouterLink to="/monitor/activity" class="nav-link" active-class="is-active">Activity</RouterLink>
        <RouterLink to="/monitor/alerts" class="nav-link" active-class="is-active">Alerts</RouterLink>
      </nav>
      <ConnectionStatusBanner :state="monitor.connectionState" :last-message-at="monitor.lastMessageAt" />
    </header>
    <RouterView />
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import ConnectionStatusBanner from '@/components/common/ConnectionStatusBanner.vue'
import { useMonitorStore } from '@/stores/monitor'

const monitor = useMonitorStore()

onMounted(() => {
  void monitor.connectGlobal()
})
</script>

<style scoped>
.monitor-shell {
  min-height: 100vh;
  padding: 1rem;
}
.monitor-topbar {
  display: grid;
  gap: 1rem;
  padding: 1rem;
  margin-bottom: 1rem;
}
.monitor-nav {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
}
.nav-link {
  padding: 0.72rem 0.95rem;
  border-radius: 999px;
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
</style>
