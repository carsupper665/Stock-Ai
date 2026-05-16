<template>
  <div class="shell">
    <button v-if="isMobileNavOpen" class="nav-overlay" type="button" @click="closeMobileNav" aria-label="關閉導覽" />

    <aside class="sidebar panel" :class="{ 'is-open': isMobileNavOpen }">
      <div class="brand-block">
        <p class="eyebrow">管理控制台</p>
        <h1>交易沙盒控制台</h1>
      </div>
      <nav>
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          active-class="is-active"
          @click="closeMobileNav"
        >
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>
    </aside>

    <div class="content">
      <header class="topbar panel">
        <div class="title-row">
          <button class="mobile-nav-btn" type="button" @click="toggleMobileNav" aria-label="開啟導覽">☰</button>
          <div>
            <p class="eyebrow">工作階段</p>
            <strong>{{ auth.user?.username ?? '管理員工作階段' }}</strong>
          </div>
        </div>
        <ConnectionStatusBanner :state="monitor.connectionState" :last-message-at="monitor.lastMessageAt" />
      </header>
      <RouterView />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import ConnectionStatusBanner from '@/components/common/ConnectionStatusBanner.vue'
import { useAuthStore } from '@/stores/auth'
import { useMonitorStore } from '@/stores/monitor'

const auth = useAuthStore()
const monitor = useMonitorStore()
const route = useRoute()
const isMobileNavOpen = ref(false)

const navItems = [
  { to: '/admin/sandboxes', label: '沙盒' },
  { to: '/admin/datasets', label: '回放資料集' },
  { to: '/admin/accounts', label: '帳戶與權杖' },
  { to: '/admin/live-accounts', label: '即時帳戶' },
  { to: '/admin/agent-boundary', label: 'Agent 邊界' },
  { to: '/admin/system-health', label: '系統健康' },
]

function toggleMobileNav() {
  isMobileNavOpen.value = !isMobileNavOpen.value
}

function closeMobileNav() {
  isMobileNavOpen.value = false
}

watch(
  () => route.fullPath,
  () => {
    isMobileNavOpen.value = false
  },
)

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
  position: relative;
}

.sidebar {
  padding: 1.25rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
  border-radius: var(--radius);
  background: linear-gradient(180deg, #ffffff 0%, #f8fbff 100%);
  z-index: 20;
}

.brand-block h1 {
  margin: 0;
  font-size: 1.1rem;
}

.topbar {
  display: grid;
  gap: 1rem;
  padding: 1rem;
  margin-bottom: 1rem;
  border-radius: var(--radius-sm);
  background: #ffffff;
}

.title-row {
  display: flex;
  gap: 0.75rem;
  align-items: center;
}

.content {
  min-width: 0;
}

.nav-link {
  display: flex;
  padding: 0.85rem 1rem;
  border-radius: 14px;
  color: var(--muted);
  font-weight: 600;
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

.nav-overlay {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.25);
  border: 0;
  z-index: 10;
}

@media (max-width: 1024px) {
  .shell {
    grid-template-columns: 1fr;
    padding: 0.75rem;
  }

  .mobile-nav-btn {
    display: inline-flex;
  }

  .sidebar {
    position: fixed;
    top: 0;
    left: 0;
    width: min(82vw, 320px);
    height: 100vh;
    transform: translateX(-105%);
    transition: transform 0.2s ease;
  }

  .sidebar.is-open {
    transform: translateX(0);
  }
}
</style>

