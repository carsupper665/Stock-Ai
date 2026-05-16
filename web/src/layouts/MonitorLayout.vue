<template>
  <div class="monitor-shell">
    <header class="panel monitor-topbar">
      <div class="top-row">
        <button class="mobile-nav-btn" type="button" @click="toggleMobileNav" aria-label="開啟監控導覽">☰</button>
        <div>
          <p class="eyebrow">監控工作區</p>
          <h1>即時營運控制台</h1>
        </div>
      </div>
      <nav class="monitor-nav" :class="{ 'is-open': isMobileNavOpen }">
        <RouterLink to="/monitor" class="nav-link" active-class="is-active" @click="closeMobileNav">總覽</RouterLink>
        <RouterLink to="/monitor/performance" class="nav-link" active-class="is-active" @click="closeMobileNav">績效</RouterLink>
        <RouterLink to="/monitor/activity" class="nav-link" active-class="is-active" @click="closeMobileNav">活動</RouterLink>
        <RouterLink to="/monitor/alerts" class="nav-link" active-class="is-active" @click="closeMobileNav">告警</RouterLink>
      </nav>
      <ConnectionStatusBanner :state="monitor.connectionState" :last-message-at="monitor.lastMessageAt" />
    </header>
    <RouterView />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import ConnectionStatusBanner from '@/components/common/ConnectionStatusBanner.vue'
import { useMonitorStore } from '@/stores/monitor'

const monitor = useMonitorStore()
const isMobileNavOpen = ref(false)

function toggleMobileNav() {
  isMobileNavOpen.value = !isMobileNavOpen.value
}

function closeMobileNav() {
  isMobileNavOpen.value = false
}

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
  border-radius: var(--radius-sm);
  background: #ffffff;
}

.top-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
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
  border: 1px solid transparent;
}

.nav-link.is-active,
.nav-link:hover {
  color: var(--accent);
  background: var(--accent-soft);
}

.eyebrow {
  margin: 0 0 0.35rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--muted);
  font-size: 0.75rem;
}

.mobile-nav-btn {
  display: none;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: #ffffff;
  color: var(--text);
  padding: 0.35rem 0.6rem;
  font-size: 1rem;
}

@media (max-width: 720px) {
  .mobile-nav-btn {
    display: inline-flex;
  }

  .monitor-nav {
    display: none;
    flex-direction: column;
    align-items: stretch;
  }

  .monitor-nav.is-open {
    display: flex;
  }
}
</style>

