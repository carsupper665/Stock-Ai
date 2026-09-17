# Frontend Spec v0.2

USER（人）用的控制台。後端行為以 `backend/SPEC.md` 為準，本文只講前端需要哪些 API、代碼怎麼組。
頁面長什麼樣不在本文範圍。

## 0. 原則

1. 沿用 `AGENT.md` 全部 21 條；單檔 300 行。
2. 不用 TypeScript。欄位形狀直接看後端 `backend/internal/api/*.go` 的 view struct，§10 每個回應都標了定義位置。
3. 長相集中在一個 `theme.css`（§7）：設計代幣、原生元素、少數共用 class。頁面不寫顏色。
   設計依據是 `design_handoff_agent_console/README.md`（高保真交接稿）。
4. 新增頁面 = 新增一個資料夾。`core/` 與其他頁面零修改。
5. 預設所有路由都要登入。公開頁自己標 `meta.public`，目前只有登入頁。
6. 登入方式是可換的 provider：現在貼 USER Token，之後接自家 IDP（OIDC）。切換點只有一行。
7. 三個服務（交易後端、Agent Server、LLM Provider Server）都沒有 CORS，Agent／LLM 的管理憑證也不能進瀏覽器：
   同源代理去前綴並注入 Bearer（§8）。瀏覽器只持有 USER Token。
8. 規格沒定或 API 尚未提供的資料：畫面明說「尚未提供」、掛「待定 N」標籤，不假裝有值（交接稿「狀態不足時的顯示規則」）。

## 1. 選型

| 需要 | 用 | 不用 | 為什麼 |
|---|---|---|---|
| 框架 | Vue 3，`<script setup>` | Options API | 少寫一半 |
| 建置 | Vite 8 | | `import.meta.glob` 就是頁面載入器 |
| 路由 | vue-router 5 | | |
| 全域狀態 | 一個 `reactive()` | Pinia | 全站只有一份狀態：登入身分 |
| HTTP | `fetch` | axios | 25 行函式夠用 |
| 樣式 | 一個 `theme.css`：CSS 變數 + 原生元素 | UI 框架、Tailwind、SCSS、CSS-in-JS | 長相之後改一個檔（§7） |
| 型別 | 無 | TS | 查後端 |
| 測試 | Vitest 5 + happy-dom | | 只測 `core/` 與頁面包契約 |

依賴：`vue`、`vue-router`。開發依賴：`vite`、`@vitejs/plugin-vue`、`vitest`、`happy-dom`。就這六個。

## 2. 目錄與骨架

```text
frontend/
  SPEC.md
  REQUIREMENTS.md            # 使用者需求 v0.1（A-／T-／L-／X- 編號出處）
  design_handoff_agent_console/   # 設計交接稿：README（代幣、版面、互動）與 HTML 原型
  package.json
  vite.config.js            # dev proxy：/v1 → 交易後端；/agent/* → Agent Server；/llm/* → LLM Server（注入憑證）
  index.html                # 載 Google Fonts（IBM Plex Sans／Mono、Noto Sans TC）
  .env.example
  src/
    main.js                 # import theme.css；createApp(App).use(router).mount('#app')
    App.vue                 # 殼：側邊欄（服務狀態燈、選單、使用者）+ 頁首（標題、更新於、重新整理、主題）+ <RouterView/>
    theme.css               # 全站唯一的樣式檔：代幣 + 原生元素 + 共用 class（§7）
    core/                   # 只做接線，不含業務
      pages.js              # 頁面包載入器：routes 與 nav（3 行）
      router.js             # 路由 + 唯一的 auth 守衛
      session.js            # 登入狀態：token、name、save、logout
      auth.js               # login(input)；provider 切換點
      providers/token.js    # 現在的登入方式：貼 USER Token
      api.js                # fetch 包裝：帶 token、拆三種錯誤信封
      load.js               # useLoad：頁面載資料的唯一寫法；sync：殼的「更新於」與全域重新整理
      theme.js              # setTheme：套用並記住 light／dark／跟系統
    shared/                 # 跨頁共用（AGENT.md 9）
      format.js             # 數字／金額／時間／狀態色 → 純函式
      run.js                # Run 物件的顯示推導：activity、錯誤說明、progress → 時間軸
      confirm.js Confirm.vue# 破壞性操作確認框（X-08），promise 介面
      toast.js              # say()／copy()：非破壞性回饋
      Modal.vue             # 表單 modal（原生 <dialog>）
    pages/                  # 一個資料夾一個頁面（§3）
      login/
      sessions/             # Session 總覽：Cards（大膽）、Table（穩健）、NewSession（建立／複製）
      session-detail/       # /sessions/:id：當前 Run 帶狀區 + 概覽／Run／Memory／監控
      accounts/             # 帳號列表 + 右側明細（token、代查四數字、部位／訂單／成交）
      market/
      messages/
      providers/            # Provider 列表 + 明細 + API Token + 模型目錄
  test/                     # vitest（§9）
```

`core/` 八個檔加起來約 110 行，之後不該再長。業務全在 `pages/`。

依賴方向單向、無循環：

```text
pages/*  →  core/api.js  →  core/session.js
         →  core/load.js
         →  shared/*     →  core/load.js（Confirm／toast 不碰 core）
core/router.js  →  core/pages.js（讀 pages/*/index.js）、core/session.js
App.vue  →  core/*、shared/*
```

頁面之間不互相 import（`session-detail` 重用 `sessions/NewSession.vue` 是唯一例外：同一個表單，複製設定用）。

## 3. 頁面包契約

```text
src/pages/<name>/
  index.js      必要。export default 一個 vue-router 的 route record
  Page.vue      頁面本體
  api.js        這頁打的端點，一個端點一個函式，直接回後端 JSON，不轉型
  *.vue         這頁自己的元件；Page.vue 超過 300 行就往這裡拆
```

```js
// src/pages/accounts/index.js
export default {
  path: '/accounts',
  name: 'accounts',
  component: () => import('./Page.vue'),   // lazy：每頁自動一個 chunk
  meta: { title: '帳號', nav: 10 },
}
```

| meta | 意思 |
|---|---|
| `title` | 選單與頁籤文字 |
| `nav` | 選單排序。沒有就不進選單（明細頁、登入頁） |
| `public: true` | 免登入。預設 false，不用寫 |

- 帶參數的頁面（`/accounts/:id`）加 `props: true`，Page.vue 直接收 `id` prop。
- 子路由用 vue-router 的 `children`，只在子頁要畫在父頁版面內時；否則就是另一個資料夾。
  `account-detail` 是獨立資料夾，不是 `accounts` 的 child。
- 載入器：

```js
// src/core/pages.js
const modules = import.meta.glob('../pages/*/index.js', { eager: true })
export const routes = Object.values(modules).map((m) => m.default)
export const nav = routes.filter((r) => r.meta?.nav).sort((a, b) => a.meta.nav - b.meta.nav)
```

`nav` 給殼畫選單，也決定首頁（選單第一項）。

新增頁面的全部動作：建資料夾、寫 `index.js`、寫 `Page.vue`。重新整理就出現在選單。

## 4. Auth：統一把關與登入街口

兩層把關，都在 `core/`：

1. 路由守衛（全站唯一一個）：不是 `public` 又沒有 `session.token` → 轉去 `/login?redirect=原路徑`。
2. API 層：每個請求自動帶 `Authorization: Bearer`；用自己的 token 收到 401 → `logout()`。

```js
// src/core/router.js
import { createRouter, createWebHistory } from 'vue-router'
import { nav, routes } from './pages.js'
import { session } from './session.js'

// 首頁是選單第一項；還沒有任何頁面時退到登入頁
const home = { name: nav[0]?.name ?? 'login' }

export const router = createRouter({
  history: createWebHistory(),
  routes: [...routes, { path: '/', redirect: home }, { path: '/:pathMatch(.*)*', redirect: '/' }],
})

router.beforeEach((to) => {
  if (to.meta.public || session.token) return true
  return { name: 'login', query: { redirect: to.fullPath } }
})
```

```js
// src/core/session.js
import { reactive } from 'vue'

export const session = reactive({
  token: sessionStorage.getItem('token'),
  name: sessionStorage.getItem('name'),
})

export function save({ token, name }) {
  Object.assign(session, { token, name })
  sessionStorage.setItem('token', token)
  sessionStorage.setItem('name', name)
}

export function logout() {
  sessionStorage.clear()
  location.assign('/login')
}
```

`sessionStorage`：重新整理不掉、關分頁即登出。`logout()` 用整頁跳轉，所以 `api.js` 不需要認得 router。

### 登入街口

```js
// src/core/auth.js
import { save } from './session.js'
import * as token from './providers/token.js'

const providers = { token }                                   // 接 IDP 時加一行：oidc
const providerName = import.meta.env.VITE_AUTH_PROVIDER ?? 'token'

export async function login(input) {
  save(await providers[providerName].login(input))
}
```

provider 契約只有一個函式：

```text
login(input) → Promise<{ token, name }>     token：能放進 Authorization: Bearer 的字串
```

**token provider（現在）**

```js
// src/core/providers/token.js
import { api } from '../api.js'

export async function login({ token }) {
  await api('GET', '/v1/accounts', undefined, token)   // 200 才是 USER Token；401/403 由 api() 丟出
  return { token, name: 'USER' }
}
```

登入頁：一個輸入框（開發時由 `VITE_DEV_TOKEN` 預填）→ `login({ token })` → 成功後 `router.push(redirect ?? '/')`，失敗顯示後端的 `message`。已登入時改顯示身分與登出鈕。

**oidc provider（預留，接自家 IDP 時實作）**

- 流程：Authorization Code + PKCE（SPA 沒有 client secret）。
- `login({})`：把 `redirect` 目標存進 `sessionStorage`，產生 PKCE、導去 IDP 的 authorize endpoint，`redirect_uri` 是 `<origin>/login`。
- `login({ code, state })`：IDP 導回 `/login?code=…` 時由登入頁呼叫；驗 state、用 code 換 token；回 `{ token: access_token, name: id_token.name }`。
- 同一個 `login(input)`，用 `input.code` 有沒有值區分去程與回程，不另開函式。
- 需要新增：`core/providers/oidc.js`、`.env` 的 `VITE_OIDC_ISSUER`、`VITE_OIDC_CLIENT_ID`、`auth.js` 的一行、登入頁把輸入框換成按鈕（把 `providerName` export 出去判斷）。
- 後端要能認 IDP 的 JWT，見 §11。
- 預留的是契約與切換點，**不寫空殼檔案**（AGENT.md 5、11）。

## 5. API 層

```js
// src/core/api.js
import { logout, session } from './session.js'

const base = import.meta.env.VITE_API_BASE ?? ''

// 三個服務三種錯誤信封：交易後端 {error, message}、Agent Server {status_code, error, msg}、LLM Server {error: {code, message}}
const codeOf = (d) => (typeof d.error === 'string' ? d.error : d.error?.code)
const messageOf = (d) => d.message ?? d.msg ?? d.error?.message

// token：預設帶 USER Token；傳 null 就不帶（Agent／LLM Server 的管理憑證由代理注入）
export async function api(method, path, body, token = session.token) {
  const headers = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  if (res.status === 204) return null
  const data = await res.json().catch(() => ({}))     // 後端掛了時代理回空的 502，不能讓解析錯誤蓋掉狀態碼
  if (res.ok) return data

  if (res.status === 401 && token && token === session.token) logout()
  throw Object.assign(new Error(messageOf(data) ?? `${res.status} ${res.statusText}`), { status: res.status, code: codeOf(data) })
}
```

規則：

- 成功直接回後端 JSON；204 回 `null`。不轉型、不改 key、不快取、不重試。
- 失敗丟 `Error`，帶 `status`、`code`、`message`；三種信封在這裡拆成同一形狀，頁面直接顯示 `message`。
  後端沒開時代理回的是空的 502，`message` 會是 `502 Bad Gateway`，`code` 是 `undefined`。
- 第四個參數 `token`：
  - 省略 → USER Token（交易後端）。
  - 帳號的 token → 代打（§10 D 組：帳號明細與 Session 概覽用它代查 `GET /v1/account`）。代打的 401 只丟錯，不登出 USER。
  - `null` → 不帶 Authorization：`/agent/*`、`/llm/*` 由代理注入管理憑證；這種 401 表示代理設定錯，也只丟錯。
- `VITE_API_BASE` 預設空字串＝同源：開發走 vite proxy，正式走反向代理。

頁面的 `api.js` 一行一個端點；打 Agent／LLM Server 的頁面在檔頭包一個加前綴、傳 `null` 的小函式：

```js
// src/pages/sessions/api.js
const agent = (method, path, body) => api(method, `/agent/api/v1${path}`, body, null)
export const listSessions = (page = 1) => agent('GET', `/sessions?page=${page}`)
export const listAccounts = () => api('GET', '/v1/accounts')
```

## 6. 資料載入

每一頁都要「載入中／錯誤／資料／最後更新時間／重載」，這些狀態只寫一次：

```js
// src/core/load.js
import { ref } from 'vue'

export function useLoad(fn) {
  const data = ref(null)
  const error = ref(null)
  const loading = ref(false)
  const at = ref(null)

  async function reload() {
    loading.value = true
    error.value = null
    try {
      data.value = await fn()
      at.value = new Date()
    } catch (e) {
      error.value = e
    } finally {
      loading.value = false
    }
  }

  reload()
  return { data, error, loading, at, reload }
}
```

```vue
<!-- src/pages/accounts/Page.vue -->
<script setup>
import { useLoad } from '../../core/load.js'
import { listAccounts, createAccount } from './api.js'

const { data, error, loading, at, reload } = useLoad(listAccounts)

async function create(form) {
  await createAccount(form)
  reload()
}
</script>
```

寫入（建立、修改、刪除）不經過 `useLoad`：頁面自己 `try/catch`、成功就 `reload()`。這是 `core/` 唯一的 composable，不再加第二個。

`load.js` 另外 export 一個 `sync = reactive({ at, key })`：每次載入成功寫 `sync.at`（殼的「更新於 N 秒前」，輪詢也算）；
殼的「重新整理」把 `sync.key++`，它是 `<RouterView>` key 的一部分，整頁重建＝所有 `useLoad` 重跑。頁面不用認得殼。

輪詢（X-06，沒有 WebSocket）由頁面自己 `setInterval` + `onUnmounted` 清掉：running Session 的 `runs/current` 3 秒、行情 5 秒、殼的服務狀態燈 30 秒。非 running 不輪詢。

殼的 `<RouterView :key="route.fullPath" />` 讓路由參數或 query 一變就整頁重建，`useLoad` 自然重跑：
`/accounts/a` 換到 `/accounts/b`、`?status=open` 換到 `?status=filled`，頁面都不用自己 watch 路由。

## 7. 樣式與主題

全站長相只住在一個檔：`src/theme.css`，`main.js` import 一次。值來自交接稿的 Design Tokens，兩套主題同一組變數名：

```css
:root {
  color-scheme: light dark;                       /* 預設跟系統，被 data-theme 覆寫 */
  --bg: light-dark(#f1ece1, #0a0a0c);  --surf: …  --surf2: …  --surf3: …
  --line: …  --line2: …  --ink: …  --dim: …  --mute: …
  --acc: …  --accw: …  --pos: …  --neg: …  --warn: …  --shadow: …
  --font: 'IBM Plex Sans', 'Noto Sans TC', system-ui, sans-serif;
  --mono: 'IBM Plex Mono', ui-monospace, monospace;
}
:root[data-theme='light'] { color-scheme: light; }
:root[data-theme='dark']  { color-scheme: dark; }
```

`theme.css` 分三段：

1. **原生元素**：`body`、`a`、`button`、`input/select/textarea`、`table/th/td`、`dialog`、`label`、殼的 `aside/nav/header/main > article`。頁面直接寫原生元素。
2. **共用 class 白名單**（只在這裡定義，頁面不得自己發明 class 來寫外觀）：
   `card`（`flush`、`dim`、`data-status`、`data-mark`、`data-click`）、`inset`、`empty`、`hint`、`callout`、
   `eyebrow`、`note`、`mono`（`small`、`big`）、`ellipsis`、`pre`、`tag`（`pending`、`decided`、`outline`）、`dot`（`big`、`square`、`blip`）、
   `bar`、`chips`、`toggle`、`tabs`、`pills`、`kv`、`stat`／`stats`、`timeline`、`toast`、`rows`、`sep`、`avatar`，
   以及純排版的 `row`、`col`、`grow`、`cols`。
3. **語意色**：`data-tone="pos|neg|warn|acc|mute|dim"`。狀態 → 色的映射只在 `shared/format.js` 的 `tone()`：
   running／open → acc；completed／active／filled／long → pos；interrupted／activating → warn；failed／rejected／short → neg；stopped／deleted／disabled／canceled → mute。
   `interrupted` 永遠不能長得像 `completed`。

`setTheme` 與 `localStorage` 的機制不變（見 `core/theme.js`）；殼的主題鈕循環「跟系統 → 亮色 → 暗色」。

規則：

1. 顏色只能寫 `var(--…)`。`src/` 裡 `theme.css` 以外不准出現 `#hex`、`rgb(`、`hsl(`，`test/theme.test.js` 會掃。
2. 元件自己的 `<style scoped>` 只寫排版與字級：grid／flex／gap／寬度／`flex-basis`、`font-size`／`font-weight`／`text-align`／`white-space`。
   不寫顏色、邊框、背景、陰影、圓角——那些是「長相」，在 `theme.css`。
3. 動畫只有 `blip`（running 狀態點 1.8s、訂閱 activating 1.2s）。沒有轉場。
4. 不用 UI 框架、Tailwind、SCSS、CSS Modules、CSS-in-JS。
5. 字體從 Google Fonts 載（`index.html`），有 system fallback；離線只是換字體。

`light-dark()` 需要 2024 之後的瀏覽器（Chrome 123、Firefox 120、Safari 17.5）。控制台只有自己用，夠了。

## 8. 開發與部署

三個服務都要起來（`.claude/launch.json` 有四個設定，含開發用的 token）：

```text
cd backend             && go run .                          # :7794   USER_TOKEN…
cd agent_server        && go run ./cmd/agent-server         # :8080   AGENT_SERVER_ADMIN_TOKEN…
cd llm_provider_server && go run ./cmd/llm-provider-server  # :8090   LLM_SERVER_ADMIN_TOKEN／RUNTIME_TOKEN／MASTER_KEY…
cd frontend && npm install && npm run dev                   # :5173
```

代理（`vite.config.js`）：

| 瀏覽器打 | 轉到 | 注入 |
|---|---|---|
| `/v1/*` | 交易後端 `/v1/*` | 不注入，瀏覽器自己帶 USER Token |
| `/agent/*` | Agent Server `/*`（去掉 `/agent`） | `Authorization: Bearer $AGENT_SERVER_ADMIN_TOKEN` |
| `/llm/v1/models` | LLM Server `/v1/models` | `Bearer $LLM_SERVER_RUNTIME_TOKEN`（模型目錄只認 runtime 憑證） |
| `/llm/*` | LLM Server `/*`（去掉 `/llm`） | `Bearer $LLM_SERVER_ADMIN_TOKEN` |

注入用 `proxyReq.setHeader`，會蓋掉瀏覽器送的 Authorization。為什麼要前綴：交易後端與 LLM Server 都用 `/v1`，同源會撞（待定 11）。

`.env.example`（實際值放 `.env.local`，已在 `.gitignore`）：

```env
VITE_API_BASE=                 # 空＝同源
VITE_AUTH_PROVIDER=token
VITE_DEV_TOKEN=                # 開發時預填登入框
BACKEND_URL=http://localhost:7794            # 以下沒有 VITE_ 前綴，只有代理看得到
AGENT_SERVER_URL=http://127.0.0.1:8080
LLM_SERVER_URL=http://127.0.0.1:8090
AGENT_SERVER_ADMIN_TOKEN=
LLM_SERVER_ADMIN_TOKEN=
LLM_SERVER_RUNTIME_TOKEN=
```

正式：`npm run build` 產出 `dist/`。反向代理照上表做同樣的去前綴與注入，其他路徑給 `dist/`，找不到檔案時回 `index.html`（history 模式必要）。
反向代理還要把 `/agent/*`、`/llm/*` 擋在登入之後（現在 dev proxy 不擋：誰連得到 :5173 誰就能打管理 API）——這是接 OIDC 時一併做的事（§11）。

## 9. 測試

`frontend/test/*.test.js`，`npm test` = `vitest run`，環境 happy-dom（要有 `sessionStorage`、`location`）。
只測接線與契約，四支：

| 檔案 | 驗什麼 |
|---|---|
| `pages.test.js` | 每個 `pages/*/index.js` 都有 `path`（以 `/` 開頭）、`name`、`component`；`name` 不重複；只有 `login` 是 `public`；每個 `component()` 都載得起來；`nav` 只收有 `nav` 的頁面且已排序 |
| `router.test.js` | 用 `addRoute` 掛一個受保護的測試路由，不依賴真實頁面：沒 token 被送去 `login` 且 `query.redirect` 是原路徑；有 token 進得去；`/login` 免 token；不認得的路徑不 404 |
| `api.test.js` | 帶 `Authorization` 與 JSON body；204 回 `null`；三種錯誤信封都變成 `{status, code, message}`；自己的 token 401 會 `logout`、代打的 token 401 與 `token = null` 的 401 只丟錯；空的 502 也拿得到狀態碼（mock `fetch`） |
| `theme.test.js` | `src/**/*.{vue,css}` 除了 `theme.css` 沒有 `#hex`、`rgb(`、`hsl(`（`import.meta.glob` 讀原始碼掃） |

頁面不寫單元測試：對著真後端手動驗，跟 `backend/test/*.py` 一樣是真流程。頁面長相定下來後再考慮 e2e。

## 10. API 列表

### 10.1 共同約定

- 認證：`Authorization: Bearer <token>`。USER Token 來自後端 `.env`；Account Token 由後端產生，USER 查帳號時看得到。
- 時間：UTC、RFC3339、結尾 `Z`。顯示時轉瀏覽器本地時間。
- 數字：float64，後端已收斂到小數 8 位。前端只做顯示格式，不做金額運算。
- 列表都包在複數 key 裡：`accounts`、`orders`、`positions`、`trades`、`messages`、`subscriptions`。
- 分頁：`messages` 用 `after`／`before`，值是上一頁最後一則的 `created_at`；`orders`／`trades` 只有 `limit`（預設 50、最多 200、最新在前），沒有游標。
- 標 `?` 的欄位是後端 `omitempty`：零值時整個欄位不存在，讀取時當 `0`／`false`／`''`。
- 錯誤信封固定 `{ "error": "<code>", "message": "<給人看的>" }`，見 §10.5。

### 10.2 端點

**A. 不需要身分**

| 端點 | 用途 | 參數 | 回應 | 前端 |
|---|---|---|---|---|
| `GET /v1/health` | 服務與 DB 是否活著 | — | `{status: "ok" 或 "degraded", database}` | 殼的狀態燈（可選） |

**B. 只有 USER Token**（Account Token 打回 403）

| 端點 | 用途 | 參數 | 回應 | 前端 |
|---|---|---|---|---|
| `POST /v1/accounts` | 建立虛擬帳號 | body `user_name`、`initial_balance` | 201 account（含 `token`） | accounts |
| `GET /v1/accounts` | 全部帳號 | — | `{accounts: [account]}`（含 `token`） | accounts；login 驗證 token |
| `GET /v1/accounts/:id` | 單一帳號 | — | account（含 `token`） | account-detail |
| `PATCH /v1/accounts/:id` | 改名、停用、啟用 | body `user_name?`、`status?`，至少一個 | account | accounts |
| `DELETE /v1/accounts/:id` | 刪除帳號 | — | 204 | accounts |
| `POST /v1/accounts/:id/token/reset` | 重設 Account Token | — | account（新 `token`） | account-detail |
| `GET /v1/accounts/:id/positions` | 該帳號部位 | `product?` | `{positions: [position]}` | account-detail |
| `GET /v1/accounts/:id/orders` | 該帳號訂單 | `status?`、`limit?` | `{orders: [order]}` | account-detail |
| `GET /v1/accounts/:id/trades` | 該帳號成交 | `limit?` | `{trades: [trade]}` | account-detail |
| `GET /v1/accounts/:id/ledger` | 該帳號 Ledger（每筆帳戶事件與造成它的 Session／Run） | `limit?`、`before_seq?`（游標：只回更小的 seq）、`session_id?`、`run_id?` | `{entries: [ledger]}` | accounts；session-detail 監控 |
| `GET /v1/market/subscriptions` | 行情訂閱狀態 | — | `{subscriptions: {"crypto:BTCUSDT": state}}` | market |

**C. USER 或 Account Token**

| 端點 | 用途 | 參數 | 回應 | 前端 |
|---|---|---|---|---|
| `GET /v1/market/price` | 最新價 | `symbol`（必要）、`market?`（預設 `crypto`） | price | market；account-detail |
| `GET /v1/messages` | 留言列表。token 可帶可不帶：帶了自己的留言顯示 `you`，帶錯回 401 | `limit?`（1–30，超過壓到 30）、`order?`（`desc` 預設／`asc`）、`after?`、`before?` | `{messages: [message]}` | messages |
| `POST /v1/messages` | 發布留言 | body `content`（≤ 2000 字）。帶 `author_*` 任一欄位回 400 | 201 message（`user_name` 是 `you`） | messages |

**D. 只有 Account Token**（USER Token 打回 403）

| 端點 | 用途 | 參數 | 回應 |
|---|---|---|---|
| `GET /v1/account` | 該帳號帳務總覽 | — | account（無 `token`）+ `locked_margin`、`available`、`unrealized_pnl`、`equity` |
| `POST /v1/orders` | 下單 | body `symbol`、`product`、`side`、`type`、`quantity`、`leverage`（1–100，spot 只能 1）、`price`（limit 必要）、`market?`、`stop_loss?`、`take_profit?` | 201 order |
| `GET /v1/orders` | 訂單 | `status?`、`limit?` | `{orders}` |
| `GET /v1/orders/:id` | 單筆訂單 | — | order |
| `POST /v1/orders/:id/cancel` | 取消 open 的限價單 | — | order |
| `GET /v1/positions` | 部位 | `product?` | `{positions}` |
| `POST /v1/positions/:id/close` | 平倉 | body `quantity?`（省略＝全平） | order（`reduce_only: true`） |
| `PATCH /v1/positions/:id` | 停損停利 | body `stop_loss?`、`take_profit?`：省略不動、`0` 移除、`> 0` 設定 | position |
| `GET /v1/trades` | 成交 | `limit?` | `{trades}` |
| `GET /v1/ledger` | 該帳號 Ledger | 同 `/v1/accounts/:id/ledger` | `{entries}` |

下單、撤單、平倉、改 SL／TP 的 body 都可帶 `source: {session_id, run_id}`（Agent Server 注入；給了就要完整，否則 400）。帳號身分仍由 token 決定。

D 組是 AI agent 用的。USER 看得到每個帳號的 token，要手動介入（例如緊急平倉）時，
前端拿該帳號的 token 當 `api()` 第四個參數呼叫這組。第一階段不做這個頁面，API 層先留好參數。

### 10.3 回應形狀

定義位置都在 `backend/internal/api/`，欄位改了以 Go 為準。

| 名稱 | 欄位 | 定義 |
|---|---|---|
| account | `id` `user_name` `initial_balance` `balance` `status` `created_at` `token?` | `account.go` `accountView` |
| account（自查） | 上列去掉 `token`，加 `locked_margin` `available` `unrealized_pnl` `equity` | `account.go` `selfAccountView` |
| order | `id` `market` `symbol` `product` `side` `type` `quantity` `price?` `leverage` `stop_loss?` `take_profit?` `reduce_only?` `status` `reject_reason?` `filled_quantity` `avg_fill_price?` `fee` `realized_pnl` `trigger?` `source` `created_at` | `views.go` `orderView` |
| position | `id` `market` `symbol` `product` `side` `quantity` `entry_price` `mark_price` `leverage` `margin` `unrealized_pnl` `stop_loss?` `take_profit?` `stop_loss_source` `take_profit_source` `opened_at` | `views.go` `positionView` |
| trade | `id` `order_id` `market` `symbol` `product` `side` `role` `quantity` `price` `fee` `realized_pnl` `created_at` | `views.go` `tradeView` |
| ledger | `seq` `event` `order_id?` `trade_id?` `position_id?` `trigger?` `quantity` `price` `fee` `realized_pnl` `balance_delta` `balance_after` `stop_loss` `take_profit` `source` `created_at` | `views.go` `ledgerEntryView` |
| source | `{session_id, run_id}` 或 `null`（非 Agent 操作、舊資料） | `views.go` `sourceView` |
| price | `market` `symbol` `price` `updated_at` | `../market/market.go` `Price` |
| message | `id` `user_name` `content` `created_at` | `message.go` `messageView` |

### 10.4 枚舉

| 欄位 | 值 | 定義 |
|---|---|---|
| account `status` | `active` `disabled` | `database/models.go` |
| `market` | `crypto` `stock`（stock 目前回 501） | `market/market.go` |
| `product` | `spot` `futures` | `database/models.go` |
| order `side` | `buy` `sell` | |
| order `type` | `market` `limit` | |
| order `status` | `open` `filled` `canceled` `rejected` | |
| position `side` | `long` `short` | |
| trade `role` | `maker`（限價成交）`taker`（市價、停損停利、手動平倉） | |
| ledger `event` | `order_placed`（限價掛單）`fill`（唯一會動餘額：`balance_delta = realized_pnl − fee`）`order_rejected` `order_canceled` `stops_updated` | `database/models.go` |
| order／ledger `trigger` | 空（手動或 Agent）、`stop_loss`、`take_profit`（撮合自動平倉） | `trading/matching.go` |
| subscription state | `inactive` `activating` `active` | `market/market.go` |
| message `user_name` | 真名、`you`（自己）、`[deleted]`（作者帳號已刪） | `message/message.go` |

### 10.5 錯誤

| HTTP | `error` | 什麼時候 | 前端 |
|---|---|---|---|
| 400 | `invalid_request` | JSON 壞掉、欄位不合法、參數格式錯 | 顯示 `message` |
| 401 | `unauthorized` | 沒帶、帶錯、帳號已停用 | 自己的 token → 登出；否則顯示 |
| 403 | `forbidden` | token 種類不對（USER 打 D 組、Account 打 B 組） | 顯示 |
| 403 | `account_disabled` | 停用帳號想交易 | 顯示 |
| 404 | `not_found` | id 不存在、部位已平 | 顯示，列表 `reload()` |
| 409 | `name_taken` | `user_name` 重複 | 顯示 |
| 409 | `order_not_open` | 撤單時訂單已不是 open | 顯示，列表 `reload()` |
| 422 | `insufficient_balance` | 可用餘額不夠 | 顯示 |
| 501 | `not_implemented` | `market=stock` | 顯示 |
| 502 | `market_unavailable` | 行情來源掛了 | 顯示，可重試 |
| 503 | `shutting_down` | 後端關機中 | 顯示 |
| 504 | `market_timeout` | 等不到報價 | 顯示，可重試 |
| 500 | `internal_error` | 後端錯誤 | 顯示 |

### 10.6 Agent Server（`/agent/api/v1/*`，實際端點以 `agent_server/runtime.go` `serveHTTP` 為準）

| 端點 | 用途 | 回應 | 前端 |
|---|---|---|---|
| `GET /health`（無前綴，打 `/agent/health`） | 活著；持久化壞掉回 503 | `{status: "ok"}` | 殼的狀態燈 |
| `GET /sessions?page=N` | 列表，每頁固定 100、oldest first、**不含 deleted**；除了 `page` 不准帶別的 query | `{sessions, page, page_size}` | sessions |
| `POST /sessions` | 建立；`name`、`account_id`、`model_name` 必填，`model_level`／`prompt`／`max_loop`／`max_tool_call` 選填；未知欄位 400 | 201 session | sessions／NewSession |
| `GET /sessions/:id` | 單一（deleted 也回） | session | session-detail |
| `DELETE /sessions/:id` | soft delete | `{session, cancellation_pending}` | session-detail |
| `POST /sessions/:id/start` | 開一個 Run（`session_start`／`session_restart`） | 202 run | sessions／session-detail |
| `POST /sessions/:id/stop` | hard stop | 200／202 `{session, cancellation_pending}` | sessions／session-detail |
| `GET /sessions/:id/runs?page=N` | run_id 遞增 | `{runs, page, page_size}` | sessions／session-detail |
| `GET /sessions/:id/runs/current` | 進行中的 Run；沒有就 404 `CURRENT_RUN_NOT_FOUND` | run | 輪詢 |
| `GET /sessions/:id/runs/:run_id` | 單一 Run | run | — |
| `GET /sessions/:id/events` | active Event（只有 Agent 能建；已觸發／過期／刪除的就不在了） | `{events: [event]}` | session-detail 概覽 |
| `DELETE /sessions/:id/events/:event_id` | 刪 Event（Agent 不會被通知、不重建） | event；404 `EVENT_NOT_FOUND` | session-detail 概覽 |

session：`id name account_id model_name model_level prompt max_loop max_tool_call status current_run_id restart_context created_at started_at stopped_at deleted_at`。
run：`session_id run_id status trigger triggers summary output error progress[] model_call_count tool_call_count start_time end_time`；`progress` 是 `model_call`／`tool_call` trace。
`trigger` 是 `session_start`／`session_restart`／`event`；event Run 的 `triggers` 是陣列（最多 10 個）：`{type, event_id, created_run_id, params, expires_at, fired_at, matched}`，其他 Run 是 `null`。
event：`id session_id created_run_id type params state created_at expires_at`；`type` 是 `timer`／`price_above`／`price_below`／`price_cross_above`／`price_cross_below`／`price_change_pct`；時間是固定 9 位小數的 UTC 字串。
session 的 `restart_context` 多了 `triggers`、`cleared_events`、`pending_triggers`（stop 時清掉的 Event 與排隊中的 trigger）。

要知道的怪脾氣：Run 結束後 Session **仍是 `running`**（`runs/current` 404），要再跑必須 stop 再 start；`start` 對 running 回 409。前端以 `runs/current` 判斷「有沒有在跑」，不看 `status`。
尚未提供（畫面標「尚未提供」）：Memory、usage、ledger、tool stats、history shard、§44 的列表監控欄位；token 用量根本沒記。

### 10.7 LLM Provider Server（`/llm/*`，以 `llm_provider_server/gateway.go` 為準）

| 端點 | 用途 | 回應 | 前端 |
|---|---|---|---|
| `GET /healthz` | 活著（不代表 Provider 可用） | `{status: "ok"}` | 殼 |
| `GET /v1/providers` | 全部 | `Provider[]` | providers |
| `POST /v1/providers` | 建立：`id name type base_url default_model? config({}) enabled?`；`type` 只認 `openai` `anthropic` `openai_compatible` | 201 | providers |
| `PATCH /v1/providers/:id` | 改 `name base_url default_model enabled`（啟停也走這裡） | provider | providers |
| `DELETE /v1/providers/:id` | 連 token 一起刪 | 204 | providers |
| `GET／POST /v1/providers/:id/tokens` | 列表／新增（`name`、明文 `token`） | `ProviderToken[]`／201 | providers |
| `PATCH／DELETE /v1/providers/:id/tokens/:tok` | 改名、啟停、換 secret（`token`）／刪 | token／204 | providers |
| `GET /v1/models` | 模型目錄（runtime 憑證） | `{models: [{model_name provider_id model levels enabled available}]}` | providers；NewSession 下拉 |

Provider：`id name type base_url default_model config enabled created_at updated_at`。Token：`id provider_id name masked enabled created_at updated_at`（明文永遠不回）。
錯誤信封 `{error: {code, message}}`。

## 11. 後端待補

接 IDP 時才需要，現在不做：

1. `auth.Resolve` 多認一種 token：IDP 簽發的 JWT。用 JWKS 驗簽、比對 `iss`／`aud`／`sub`
   （`.env` 加 `OIDC_ISSUER`、`OIDC_CLIENT_ID`、`OIDC_USER_SUB`），通過就是 USER 身分。
   其他端點與前端都不用改，因為前端只是換了 Bearer 裡的字串。
2. `GET /v1/me` → `{kind: "user" 或 "account", name}`（可選）。讓登入頁不用拿 `GET /v1/accounts` 探測 token 種類、
   也能顯示 `USER_NAME`。

## 12. 實作步驟

| Step | 做什麼 | 狀態 |
|---|---|---|
| 1 骨架 | `core/` 八個檔、登入頁、四支測試 | 完成 |
| 2 全頁面 | 依交接稿做出六個頁面與殼，接上三個服務現有的 API；沒有 API 的區塊明說尚未提供 | 完成（本版） |
| 3 補齊 | Event（27～31）與 Ledger／來源追溯（21～24）已接上；剩 Memory／usage／tool stats／history 端點上線後把 session-detail 的「尚未提供」換成真資料 | 部分完成，餘等後端（tickets 32～39） |
| 4 手動介入 | 帳號明細加 D 組代打：平倉、撤單、改 SL／TP、代下單（T-17～T-21） | 未做 |
| 5 OIDC | §4 + §11；反向代理把 `/agent`、`/llm` 擋在登入後 | 未做 |

之後（需要時才做，各自獨立）：頁面長相微調（只改 `theme.css`）；交接稿裡的「設計說明」頁不做（review 用，內容由本文 §0.8 與 REQUIREMENTS §5 取代）。
