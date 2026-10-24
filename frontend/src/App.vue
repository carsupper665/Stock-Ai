<script setup>
import { computed, onMounted, onUnmounted, provide, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from './core/api.js'
import { sync } from './core/load.js'
import { nav } from './core/pages.js'
import * as oidc from './core/providers/oidc.js'
import { isAuthenticated, session } from './core/session.js'
import { setTheme } from './core/theme.js'
import Confirm from './shared/Confirm.vue'
import { relTime } from './shared/format.js'
import { toast } from './shared/toast.js'
import versionFile from '../VERSION?raw'

const route = useRoute()
const active = computed(() => route.meta.parent ?? route.name)

const themes = [['', '跟系統'], ['light', '亮色'], ['dark', '暗色']]
const theme = ref(localStorage.getItem('theme') ?? '')
// 側欄可收起，讓內容區（例如 Run 明細）更寬；瀏覽器不能存設定時每次都從展開開始
const sidebarKey = 'sidebar'
const sidebarHidden = ref(false)
try { sidebarHidden.value = localStorage.getItem(sidebarKey) === 'hidden' } catch { /* 無法讀取就維持展開 */ }
function toggleSidebar() {
  sidebarHidden.value = !sidebarHidden.value
  try { localStorage.setItem(sidebarKey, sidebarHidden.value ? 'hidden' : 'shown') } catch { /* 只影響下次開啟 */ }
}

// 群聊頁（meta.immersive）在窄螢幕只顯示聊天，由頁面上的「<」把側欄當抽屜打開
const drawerOpen = ref(false)
provide('openSidebar', () => {
  sidebarHidden.value = false
  drawerOpen.value = true
})
watch(() => route.fullPath, () => { drawerOpen.value = false })

function cycleTheme() {
  const i = themes.findIndex(([v]) => v === theme.value)
  theme.value = themes[(i + 1) % themes.length][0]
  setTheme(theme.value)
}

// 各服務各自獨立、各自會掛（X-02）；健康端點都不需要憑證，並回報各自 VERSION 檔的版本。
// 版本只顯示：相容與否在建置 image 時由 scripts/check_versions.py 檢查。
const frontendVersion = versionFile.split('\n')[0].trim()
const services = reactive([
  { name: '交易後端', path: '/v1/health', state: '…', tone: 'mute', version: '' },
  { name: 'Agent Server', path: '/agent/health', state: '…', tone: 'mute', version: '' },
  { name: 'LLM Provider', path: '/llm/healthz', state: '…', tone: 'mute', version: '' },
  { name: 'Middleware', path: '/middleware/healthz', state: '…', tone: 'mute', version: '' },
])
async function probe(s) {
  try {
    const health = await api('GET', s.path, undefined, null)
    s.state = health.status
    s.version = health.version ?? '?'
    s.tone = s.state === 'ok' ? 'pos' : 'warn'
  } catch (e) {
    s.version = '?'
    // 503 是服務活著但 DB 不健康；其他（代理回 502、憑證錯 401）視為連不上
    s.state = e.status === 503 ? 'degraded' : e.status ? `down ${e.status}` : 'down'
    s.tone = e.status === 503 ? 'warn' : 'neg'
  }
}
// 未登入時 /agent、/llm 會回 401 並觸發登出重載，登入頁上查詢會變成無限重載
const probeAll = () => { if (isAuthenticated()) services.forEach(probe) }
function logout() { oidc.logout() }

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
watch(() => [sync.key, isAuthenticated()], probeAll)
</script>

<template>
  <div v-if="isAuthenticated()" id="shell" :class="{ immersive: route.meta.immersive, drawer: drawerOpen }">
    <div v-if="drawerOpen" class="drawer-backdrop" @click="drawerOpen = false"></div>
    <aside v-show="!sidebarHidden">
      <div class="row logo"><i class="dot square" data-tone="acc"></i><b>Agent 控制台</b></div>
      <div class="col services">
        <div class="eyebrow">服務狀態</div>
        <div v-for="s in services" :key="s.name" class="row service">
          <i class="dot big" :data-tone="s.tone"></i>
          <span class="grow ellipsis" data-tone="dim">{{ s.name }}</span>
          <span class="mono small" data-tone="mute">{{ s.version }}</span>
          <span class="mono small" :data-tone="s.tone">{{ s.state }}</span>
        </div>
        <div class="row service">
          <i class="dot big" data-tone="pos"></i>
          <span class="grow ellipsis" data-tone="dim">前端</span>
          <span class="mono small" data-tone="mute">{{ frontendVersion }}</span>
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
        <div class="mono small" data-tone="mute">安全工作階段 · token 不進入瀏覽器儲存空間</div>
      </div>
    </aside>

    <main>
      <header>
        <div class="row grow">
          <button class="text" :aria-expanded="!sidebarHidden" :aria-label="sidebarHidden ? '展開側欄' : '收起側欄'" :title="sidebarHidden ? '展開側欄' : '收起側欄'" @click="toggleSidebar">☰</button>
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
/* 群聊頁高度限制在螢幕內，訊息區自己捲動 */
#shell.immersive {
  height: 100vh;
  height: 100dvh;
  min-height: 0;
}
#shell.immersive main {
  min-height: 0;
}
#shell.immersive > aside {
  overflow-y: auto;
}
.drawer-backdrop {
  display: none;
}
@media (max-width: 900px) {
  #shell.immersive > aside,
  #shell.immersive header {
    display: none;
  }
  #shell.immersive.drawer > aside {
    display: flex;
    position: fixed;
    inset: 0 auto 0 0;
    z-index: 30;
    width: min(300px, 85vw);
    gap: 14px;
    padding: 14px 12px;
    border-right: 1px solid var(--line);
    border-bottom: 0;
  }
  #shell.immersive.drawer nav {
    flex-direction: column;
    flex-wrap: nowrap;
    overflow: visible;
    margin: 0;
    padding: 12px 0 0;
  }
  #shell.immersive.drawer nav a {
    border-left: 2px solid transparent;
    border-bottom: 0;
    border-radius: 0 5px 5px 0;
  }
  #shell.immersive.drawer nav a[aria-current='page'] {
    border-left-color: var(--acc);
  }
  .drawer-backdrop {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 29;
    background: var(--overlay);
  }
}
@media (max-width: 640px) {
  .services {
    flex-flow: row wrap;
    gap: 4px 14px;
  }
  .services > .eyebrow,
  .user > .mono {
    display: none;
  }
  .service > .grow {
    flex: none;
  }
}
</style>
