<script setup>
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from './core/api.js'
import { sync } from './core/load.js'
import { nav } from './core/pages.js'
import { logout, session } from './core/session.js'
import { setTheme } from './core/theme.js'
import Confirm from './shared/Confirm.vue'
import { relTime } from './shared/format.js'
import { toast } from './shared/toast.js'

const route = useRoute()
const active = computed(() => route.meta.parent ?? route.name)

const themes = [['', '跟系統'], ['light', '亮色'], ['dark', '暗色']]
const theme = ref(localStorage.getItem('theme') ?? '')
function cycleTheme() {
  const i = themes.findIndex(([v]) => v === theme.value)
  theme.value = themes[(i + 1) % themes.length][0]
  setTheme(theme.value)
}

// 三個服務各自獨立、各自會掛（X-02）；健康端點都不需要憑證
const services = reactive([
  { name: '交易後端', path: '/v1/health', state: '…', tone: 'mute' },
  { name: 'Agent Server', path: '/agent/health', state: '…', tone: 'mute' },
  { name: 'LLM Provider', path: '/llm/healthz', state: '…', tone: 'mute' },
])
async function probe(s) {
  try {
    s.state = (await api('GET', s.path, undefined, null)).status
    s.tone = s.state === 'ok' ? 'pos' : 'warn'
  } catch (e) {
    // 503 是服務活著但 DB 不健康；其他（代理回 502、憑證錯 401）視為連不上
    s.state = e.status === 503 ? 'degraded' : e.status ? `down ${e.status}` : 'down'
    s.tone = e.status === 503 ? 'warn' : 'neg'
  }
}
const probeAll = () => services.forEach(probe)

const now = ref(Date.now())
let tick, poll
onMounted(() => {
  probeAll()
  tick = setInterval(() => (now.value = Date.now()), 1000)
  poll = setInterval(probeAll, 30000)
})
onUnmounted(() => {
  clearInterval(tick)
  clearInterval(poll)
})
watch(() => sync.key, probeAll)
</script>

<template>
  <div v-if="session.token" id="shell">
    <aside>
      <div class="row logo"><i class="dot square" data-tone="acc"></i><b>Agent 控制台</b></div>
      <div class="col services">
        <div class="eyebrow">服務狀態</div>
        <div v-for="s in services" :key="s.name" class="row service">
          <i class="dot big" :data-tone="s.tone"></i>
          <span class="grow ellipsis" data-tone="dim">{{ s.name }}</span>
          <span class="mono small" :data-tone="s.tone">{{ s.state }}</span>
        </div>
      </div>
      <nav>
        <RouterLink v-for="r in nav" :key="r.name" :to="{ name: r.name }" :aria-current="active === r.name ? 'page' : null">{{ r.meta.title }}</RouterLink>
      </nav>
      <div class="col user">
        <div class="row">
          <span class="avatar mono small" data-tone="acc">{{ session.name.slice(0, 2).toUpperCase() }}</span>
          <span>{{ session.name }} <span data-tone="mute">· USER</span></span>
          <span class="grow"></span>
          <button class="text" @click="logout">登出</button>
        </div>
        <div class="mono small" data-tone="mute">USER Token 登入 · 重新整理不掉<br />關閉分頁即登出</div>
      </div>
    </aside>

    <main>
      <header>
        <div class="row grow">
          <h2>{{ route.meta.title }}</h2>
          <span class="note ellipsis">{{ route.meta.sub }}</span>
        </div>
        <div class="row">
          <span class="mono" data-tone="mute">更新於 {{ sync.at ? relTime(sync.at, now) : '—' }}</span>
          <button @click="sync.key++">重新整理</button>
          <span id="header-tools" class="row"></span>
          <button @click="cycleTheme">{{ themes.find(([v]) => v === theme)[1] }}</button>
        </div>
      </header>
      <!-- 換了參數或 query 就整頁重建，頁面的 useLoad 不用自己 watch 路由；sync.key 一變＝全域重新整理 -->
      <RouterView :key="route.fullPath + '#' + sync.key" />
    </main>
  </div>
  <RouterView v-else :key="route.fullPath" />

  <Confirm />
  <div v-if="toast.text" class="toast" :data-tone="toast.tone">{{ toast.text }}</div>
</template>

<style scoped>
#shell {
  display: flex;
  min-height: 100vh;
}
main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.logo {
  gap: 8px;
}
.services {
  gap: 5px;
}
.service {
  gap: 7px;
  flex-wrap: nowrap;
}
.user {
  margin-top: auto;
  gap: 7px;
}
.avatar {
  width: 20px;
  height: 20px;
  display: grid;
  place-items: center;
}
@media (max-width: 900px) {
  #shell {
    flex-direction: column;
  }
}
</style>
