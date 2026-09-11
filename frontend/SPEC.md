# Frontend Spec v0.1

USER（人）用的控制台。後端行為以 `backend/SPEC.md` 為準，本文只講前端需要哪些 API、代碼怎麼組。
頁面長什麼樣不在本文範圍。

## 0. 原則

1. 沿用 `AGENT.md` 全部 21 條；單檔 300 行。
2. 不用 TypeScript。欄位形狀直接看後端 `backend/internal/api/*.go` 的 view struct，§10 每個回應都標了定義位置。
3. 先不設計頁面長相：不引入 UI 框架、不建 class 系統。頁面用原生元素把資料流跑通；長相集中在一個 `theme.css`（§7），之後獨立一步只改那個檔。
4. 新增頁面 = 新增一個資料夾。`core/` 與其他頁面零修改。
5. 預設所有路由都要登入。公開頁自己標 `meta.public`，目前只有登入頁。
6. 登入方式是可換的 provider：現在貼 USER Token，之後接自家 IDP（OIDC）。切換點只有一行。

## 1. 選型

| 需要 | 用 | 不用 | 為什麼 |
|---|---|---|---|
| 框架 | Vue 3，`<script setup>` | Options API | 少寫一半 |
| 建置 | Vite | | `import.meta.glob` 就是頁面載入器 |
| 路由 | vue-router 4 | | |
| 全域狀態 | 一個 `reactive()` | Pinia | 全站只有一份狀態：登入身分 |
| HTTP | `fetch` | axios | 25 行函式夠用 |
| 樣式 | 一個 `theme.css`：CSS 變數 + 原生元素 | UI 框架、Tailwind、SCSS、CSS-in-JS | 長相之後改一個檔（§7） |
| 型別 | 無 | TS | 查後端 |
| 測試 | vitest + happy-dom | | 只測 `core/` 與頁面包契約 |

依賴：`vue`、`vue-router`。開發依賴：`vite`、`@vitejs/plugin-vue`、`vitest`、`happy-dom`。就這六個。

## 2. 目錄與骨架

```text
frontend/
  SPEC.md
  package.json
  vite.config.js            # dev proxy：/v1 → 後端，不用 CORS
  index.html
  .env.example
  src/
    main.js                 # import theme.css；createApp(App).use(router).mount('#app')
    App.vue                 # 殼：選單（由路由 meta 產生）+ 主題切換 + <RouterView/>
    theme.css               # 全站唯一的樣式檔：變數 + 原生元素長相（§7）
    core/                   # 只做接線，不含業務
      pages.js              # 頁面包載入器（2 行）
      router.js             # 路由 + 唯一的 auth 守衛
      session.js            # 登入狀態：token、name、save、logout
      auth.js               # login(input)；provider 切換點
      providers/token.js    # 現在的登入方式：貼 USER Token
      api.js                # fetch 包裝：帶 token、拆錯誤信封
      load.js               # useLoad：頁面載資料的唯一寫法
      theme.js              # setTheme：套用並記住 light／dark／跟系統
    pages/                  # 一個資料夾一個頁面（§3）
      login/
      accounts/
      account-detail/
      market/
      messages/
  test/                     # vitest（§9）
```

`core/` 八個檔加起來約 90 行，之後不該再長。業務全在 `pages/`。
跨頁共用的元件才放 `src/shared/`；現在沒有，需要時再建（AGENT.md 5、9）。

依賴方向單向、無循環：

```text
pages/*  →  core/api.js  →  core/session.js
         →  core/load.js
         →  core/auth.js  →  core/providers/*.js  →  core/api.js
core/router.js  →  core/pages.js（讀 pages/*/index.js）、core/session.js
App.vue  →  core/router.js、core/session.js、core/theme.js
```

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
```

新增頁面的全部動作：建資料夾、寫 `index.js`、寫 `Page.vue`。重新整理就出現在選單。

## 4. Auth：統一把關與登入街口

兩層把關，都在 `core/`：

1. 路由守衛（全站唯一一個）：不是 `public` 又沒有 `session.token` → 轉去 `/login?redirect=原路徑`。
2. API 層：每個請求自動帶 `Authorization: Bearer`；用自己的 token 收到 401 → `logout()`。

```js
// src/core/router.js
import { createRouter, createWebHistory } from 'vue-router'
import { routes } from './pages.js'
import { session } from './session.js'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    ...routes,
    { path: '/', redirect: { name: 'accounts' } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
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
export const providerName = import.meta.env.VITE_AUTH_PROVIDER ?? 'token'

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

登入頁：一個輸入框（開發時由 `VITE_DEV_TOKEN` 預填）→ `login({ token })` → 成功後 `router.push(redirect ?? '/')`，失敗顯示後端的 `message`。

**oidc provider（預留，接自家 IDP 時實作）**

- 流程：Authorization Code + PKCE（SPA 沒有 client secret）。
- `login({})`：把 `redirect` 目標存進 `sessionStorage`，產生 PKCE、導去 IDP 的 authorize endpoint，`redirect_uri` 是 `<origin>/login`。
- `login({ code, state })`：IDP 導回 `/login?code=…` 時由登入頁呼叫；驗 state、用 code 換 token；回 `{ token: access_token, name: id_token.name }`。
- 同一個 `login(input)`，用 `input.code` 有沒有值區分去程與回程，不另開函式。
- 需要新增：`core/providers/oidc.js`、`.env` 的 `VITE_OIDC_ISSUER`、`VITE_OIDC_CLIENT_ID`、`auth.js` 的一行、登入頁把輸入框換成按鈕（`providerName === 'oidc'`）。
- 後端要能認 IDP 的 JWT，見 §11。
- 預留的是契約與切換點，**不寫空殼檔案**（AGENT.md 5、11）。

## 5. API 層

```js
// src/core/api.js
import { session, logout } from './session.js'

const base = import.meta.env.VITE_API_BASE ?? ''

export async function api(method, path, body, token = session.token) {
  const headers = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  if (res.status === 204) return null
  const data = await res.json()
  if (res.ok) return data

  if (res.status === 401 && token && token === session.token) logout()
  throw Object.assign(new Error(data.message), { status: res.status, code: data.error })
}
```

規則：

- 成功直接回後端 JSON；204 回 `null`。不轉型、不改 key、不快取、不重試。
- 失敗丟 `Error`，帶 `status`、`code`（後端的 `error` 欄位）、`message`（後端給人看的文字）。頁面通常直接顯示 `message`。
- 第四個參數 `token` 可覆寫，給「用某個帳號的 token 代打」用（§10 D 組）。代打的 token 收到 401 只丟錯，不會把 USER 登出。
- `VITE_API_BASE` 預設空字串＝同源：開發走 vite proxy，正式走反向代理。

頁面的 `api.js` 長這樣，一行一個端點：

```js
// src/pages/accounts/api.js
import { api } from '../../core/api.js'

export const listAccounts = () => api('GET', '/v1/accounts')
export const createAccount = (body) => api('POST', '/v1/accounts', body)
export const updateAccount = (id, body) => api('PATCH', `/v1/accounts/${id}`, body)
export const deleteAccount = (id) => api('DELETE', `/v1/accounts/${id}`)
```

## 6. 資料載入

每一頁都要「載入中／錯誤／資料／重載」，這四個狀態只寫一次：

```js
// src/core/load.js
import { ref } from 'vue'

export function useLoad(fn) {
  const data = ref(null)
  const error = ref(null)
  const loading = ref(false)

  async function reload() {
    loading.value = true
    error.value = null
    try {
      data.value = await fn()
    } catch (e) {
      error.value = e
    } finally {
      loading.value = false
    }
  }

  reload()
  return { data, error, loading, reload }
}
```

```vue
<!-- src/pages/accounts/Page.vue -->
<script setup>
import { useLoad } from '../../core/load.js'
import { listAccounts, createAccount } from './api.js'

const { data, error, loading, reload } = useLoad(listAccounts)

async function create(form) {
  await createAccount(form)
  reload()
}
</script>
```

寫入（建立、修改、刪除）不經過 `useLoad`：頁面自己 `try/catch`、成功就 `reload()`。這是 `core/` 唯一的 composable，不再加第二個。

## 7. 樣式與主題

全站長相只住在一個檔：`src/theme.css`，`main.js` import 一次。頁面不寫顏色、不發明 class。

```css
/* src/theme.css —— 值都是佔位，「設計長相」那一步只改這裡 */
:root {
  color-scheme: light dark;                  /* 預設跟系統，被 data-theme 覆寫 */
  --bg:     light-dark(#ffffff, #111111);
  --fg:     light-dark(#111111, #eeeeee);
  --muted:  light-dark(#666666, #999999);
  --line:   light-dark(#dddddd, #333333);
  --accent: light-dark(#2563eb, #60a5fa);
  --ok:     light-dark(#16a34a, #4ade80);
  --danger: light-dark(#dc2626, #f87171);
  --space: 8px;
  --radius: 4px;
  --font: system-ui, sans-serif;
  --mono: ui-monospace, monospace;
}
:root[data-theme='light'] { color-scheme: light; }
:root[data-theme='dark']  { color-scheme: dark; }

/* 原生元素的長相在這裡定義一次，頁面直接寫 <button>、<table>、<input> */
body { margin: 0; background: var(--bg); color: var(--fg); font: 14px/1.5 var(--font); }
button, input, select, textarea {
  font: inherit; color: inherit; background: var(--bg);
  border: 1px solid var(--line); border-radius: var(--radius); padding: var(--space);
}
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: var(--space); border-bottom: 1px solid var(--line); }
a { color: var(--accent); }
code { font-family: var(--mono); }
[data-tone='ok'] { color: var(--ok); }
[data-tone='danger'] { color: var(--danger); }
```

每個變數只寫一次、同時帶亮暗兩個值，切主題就是改 `color-scheme`。原生控件與捲軸跟著 `color-scheme` 自動變色，不用另外處理。

```js
// src/core/theme.js
export function setTheme(name) {            // 'light' | 'dark' | ''（跟系統）
  localStorage.setItem('theme', name)
  document.documentElement.dataset.theme = name
}
setTheme(localStorage.getItem('theme') ?? '')
```

用 `localStorage` 不用 `sessionStorage`：主題偏好要跨分頁、跨登入留著。殼（`App.vue`）放一個三選一的 `<select>` 呼叫 `setTheme`，就是全部的切換 UI。

規則：

1. 顏色、間距、圓角、字體只能寫 `var(--…)`。`src/` 裡 `theme.css` 以外不准出現 `#hex`、`rgb(`、`hsl(`，`test/theme.test.js` 會掃。
2. 原生元素長相寫一次在 `theme.css`。頁面用 `<button>`、`<table>`、`<input>`，不建 class 命名系統；共用的狀態外觀（獲利／虧損／停用）用 `data-tone`，也只在 `theme.css` 定義。
3. 元件自己的排版寫在 `<style scoped>`：只有 grid／flex／gap／寬度，間距用 `var(--space)`。不寫外觀。
4. 不用 UI 框架、Tailwind、SCSS、CSS Modules、CSS-in-JS。一個 `.css`，Vite 直接 import。
5. 之後「設計長相」= 改 `theme.css` 的值與元素規則，頁面零修改。

`light-dark()` 需要 2024 之後的瀏覽器（Chrome 123、Firefox 120、Safari 17.5）。控制台只有自己用，夠了；
真要撐舊瀏覽器就改成兩組 `:root` 變數加 `prefers-color-scheme` 媒體查詢，機制不變。

## 8. 開發與部署

```text
cd backend  && go run .                     # :7794
cd frontend && npm install && npm run dev   # :5173，/v1 代理到 :7794
```

```js
// vite.config.js
export default defineConfig({
  plugins: [vue()],
  server: { proxy: { '/v1': 'http://localhost:7794' } },
})
```

`.env.example`（實際值放 `.env.local`，已在 `.gitignore`）：

```env
# 後端位址。空字串＝同源（開發走 vite proxy，正式走反向代理）
VITE_API_BASE=
# 登入方式：token（貼 USER Token）。接 IDP 後改 oidc
VITE_AUTH_PROVIDER=token
# 開發時預填登入框
VITE_DEV_TOKEN=
```

正式：`npm run build` 產出 `dist/`。反向代理把 `/v1/*` 轉給後端、其他路徑給 `dist/`，
找不到檔案時回 `index.html`（history 模式必要）。後端不用改、不用 CORS。

## 9. 測試

`frontend/test/*.test.js`，`npm test` = `vitest run`，環境 happy-dom（要有 `sessionStorage`、`location`）。
只測接線與契約，四支：

| 檔案 | 驗什麼 |
|---|---|
| `pages.test.js` | 每個 `pages/*/index.js` 都有 `path`（以 `/` 開頭）、`name`、`component`；`name` 不重複；只有 `login` 是 `public`；每個 `component()` 都載得起來 |
| `router.test.js` | 沒 token 開 `/accounts` 被送去 `login` 且 `query.redirect === '/accounts'`；有 token 進得去；`/login` 免 token |
| `api.test.js` | 帶 `Authorization` 與 JSON body；204 回 `null`；錯誤信封變成 `{status, code, message}`；自己的 token 401 會 `logout`、代打的 token 401 只丟錯（mock `fetch`） |
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

D 組是 AI agent 用的。USER 看得到每個帳號的 token，要手動介入（例如緊急平倉）時，
前端拿該帳號的 token 當 `api()` 第四個參數呼叫這組。第一階段不做這個頁面，API 層先留好參數。

### 10.3 回應形狀

定義位置都在 `backend/internal/api/`，欄位改了以 Go 為準。

| 名稱 | 欄位 | 定義 |
|---|---|---|
| account | `id` `user_name` `initial_balance` `balance` `status` `created_at` `token?` | `account.go` `accountView` |
| account（自查） | 上列去掉 `token`，加 `locked_margin` `available` `unrealized_pnl` `equity` | `account.go` `selfAccountView` |
| order | `id` `market` `symbol` `product` `side` `type` `quantity` `price?` `leverage` `stop_loss?` `take_profit?` `reduce_only?` `status` `reject_reason?` `filled_quantity` `avg_fill_price?` `fee` `realized_pnl` `created_at` | `views.go` `orderView` |
| position | `id` `market` `symbol` `product` `side` `quantity` `entry_price` `mark_price` `leverage` `margin` `unrealized_pnl` `stop_loss?` `take_profit?` `opened_at` | `views.go` `positionView` |
| trade | `id` `order_id` `market` `symbol` `product` `side` `role` `quantity` `price` `fee` `realized_pnl` `created_at` | `views.go` `tradeView` |
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
| 422 | `insufficient_balance` | 可用餘額不夠 | 顯示 |
| 501 | `not_implemented` | `market=stock` | 顯示 |
| 502 | `market_unavailable` | 行情來源掛了 | 顯示，可重試 |
| 503 | `shutting_down` | 後端關機中 | 顯示 |
| 504 | `market_timeout` | 等不到報價 | 顯示，可重試 |
| 500 | `internal_error` | 後端錯誤 | 顯示 |

## 11. 後端待補

接 IDP 時才需要，現在不做：

1. `auth.Resolve` 多認一種 token：IDP 簽發的 JWT。用 JWKS 驗簽、比對 `iss`／`aud`／`sub`
   （`.env` 加 `OIDC_ISSUER`、`OIDC_CLIENT_ID`、`OIDC_USER_SUB`），通過就是 USER 身分。
   其他端點與前端都不用改，因為前端只是換了 Bearer 裡的字串。
2. `GET /v1/me` → `{kind: "user" 或 "account", name}`（可選）。讓登入頁不用拿 `GET /v1/accounts` 探測 token 種類、
   也能顯示 `USER_NAME`。

## 12. 實作步驟

一步一個 commit，每步都能跑。

| Step | 做什麼 | 驗收 |
|---|---|---|
| 1 骨架 | `package.json`、`vite.config.js`、`index.html`、`main.js`、`App.vue`、`theme.css`、`core/` 八個檔、`pages/login/`、`test/` 四支 | `npm test` 過；未登入開任何路徑都被送到 `/login`；貼對 token 進得去、貼錯看到後端 `message`；重新整理不掉登入 |
| 2 帳號 | `pages/accounts/`（列表、建立、改名、停用／啟用、刪除）、`pages/account-detail/`（基本資料、token 顯示／複製／重設、部位、訂單含 `status` 篩選、成交） | 對著 `go run .` 走完 `backend/test/02_account.py`、`04_trading.py` 建出的資料 |
| 3 行情 | `pages/market/`（查價：market + symbol；訂閱狀態表） | 查 BTCUSDT 有價；訂閱表看得到 `active` → 閒置後 `inactive` |
| 4 留言板 | `pages/messages/`（列表、發布、`before` 載入更早、自己顯示 `you`） | 對照 `backend/test/06_message.py` 的四種身分 |

之後（需要時才做，各自獨立）：頁面長相（只改 `theme.css`，§7）；手動介入頁（§10 D 組代打）；OIDC（§4 + §11）。
