# Stock-AI 行為規格（從 v1 萃取）

> 這份文件是 **重寫用的驗收標準**，不是舊碼的說明書。
> 內容全部來自 v1 的 56 個測試、agent policy 與 domain 型別，是踩過坑換來的規則。
> 舊碼刪除後，要翻舊實作用 `git show v1-legacy:<path>`。
>
> 讀法：每條規則都是「重寫後必須仍然成立」的行為。標 ⚠️ 的是 v1 的設計問題，重寫時要重新決定。

---

## 1. 詞彙

| 詞 | 意思 |
|---|---|
| **Sandbox（沙盤）** | 一個回放環境，有自己的重播時鐘 `replay_current_time` 與速度 `replay_speed` |
| **Replay Dataset（資料集）** | 匯入的歷史 K 線，沙盤的行情來源 |
| **Virtual Account（虛擬帳戶）** | 綁在某個 sandbox 內的帳戶，只能用回放行情交易 |
| **Live Account（實盤帳戶）** | 不綁 sandbox，用即時行情。依 environment 分 paper / testnet / mainnet |
| **Agent** | 拿 account-scoped token 的 LLM，只能讀行情與操作自己的帳戶 |
| **Admin** | 拿 session cookie 的 root 使用者，控制沙盤、資料集、帳戶 |
| **Tick** | 沙盤時鐘前進一步，會重新定價部位並撮合掛單 |

---

## 2. 帳戶模式矩陣

帳戶有兩軸：`type`（virtual / live）與 `environment`（paper / testnet / mainnet / production）。

| type | environment | 行情來源 | 下單行為 |
|---|---|---|---|
| virtual | — | sandbox 回放 | 內部撮合 |
| virtual | 無 sandbox 綁定 | — | ❌ `ACCOUNT_SANDBOX_REQUIRED` |
| live | 未設定 | — | ❌ `INVALID_TRADING_MODE` |
| live | paper | 即時行情 | 內部撮合（紙上），立即成交 |
| live | testnet | 即時行情 | 送交易所（需通過全部安全閘門） |
| live | mainnet | 即時行情 | ❌ `MAINNET_EXECUTION_DISABLED` |
| live | production | 即時行情 | ⚠️ 當作 **paper** 處理 |

**⚠️ `production` 被當成 paper 是 v1 的命名地雷。** 有人把環境設成 `production` 以為在打實盤，結果全部是紙上單。重寫時要嘛移除這個值，要嘛讓它明確報錯，不要沉默降級。

---

## 3. 實盤執行安全閘門

實盤下單前依序檢查，任一不過就擋。**預設值一律是「不能打實盤」**：

1. 伺服器設定 `AllowLiveExecution` 為 false → `LIVE_EXECUTION_DISABLED`
2. 沒有掛交易所 adapter → `LIVE_EXCHANGE_EXECUTION_UNSUPPORTED`
3. `environment == mainnet` → `MAINNET_EXECUTION_DISABLED`
4. 帳戶 `live_trading_enabled` 為 false → `ACCOUNT_LIVE_TRADING_DISABLED`

**新建的 live 帳戶，`live_trading_enabled` 必須預設 false。** 要開實盤必須是一個獨立、明確的動作。

### 行情品質閘門

實盤／紙上下單前，行情必須新鮮且健康：

- 快照過舊（v1 測試用 1 分鐘前）→ `MARKET_DATA_STALE`
- 行情來源抓取失敗（狀態 degraded）→ `LIVE_PROVIDER_ERROR`

行情狀態有三態：`active` / `stale` / `degraded`。degraded 與 recovered 要發事件（見 §9）。

---

## 4. 下單與撮合

### 訂單正規化

依帳戶 risk profile 的 symbol 規則處理，**全部無條件捨去，不四捨五入**：

- symbol 轉大寫並去空白：`" btcusdt "` → `"BTCUSDT"`
- 數量對齊 `step_size`：`1.239` @ step 0.01 → `1.23`
- 價格對齊 `tick_size`：`100.129` @ tick 0.01 → `100.12`
- 訂單類型不在 `allowed_order_types` → `ORDER_TYPE_NOT_ALLOWED`
- 槓桿超過 `max_leverage` → `LEVERAGE_TOO_HIGH`
- 低於 `min_qty` → `MIN_QTY_NOT_MET`；低於 `min_notional` → `MIN_NOTIONAL_NOT_MET`

### 撮合規則

| 訂單類型 | 下單當下 | 之後 |
|---|---|---|
| market | 立即成交，成交價 = 當前行情價 | — |
| limit | 狀態 `new`，掛著 | 行情穿價時於 **tick 成交** |
| stop | 狀態 `new`，掛著 | 觸價時成交 |

**⚠️ v1 的限價單成交價是「當時的市價」而非限價。** 買單掛 99、行情跌到 95 時，v1 以 **95** 成交，不是 99。這對下單方永遠有利，會讓回測績效系統性高估。重寫時要明確決定：照實際交易所行為用限價成交，還是保留這個樂觀撮合並在報告上標註。

### 冪等性

- 帶 `client_order_id` 重送**完全相同**的下單 → 回傳同一張單，不重複建立
- 相同 `client_order_id` 但內容不同 → `CLIENT_ORDER_ID_CONFLICT`
- 交易所訂單對帳（reconcile）重複執行 → 不得重複產生成交紀錄

### 訂單狀態

`new` → `partially_filled` → `filled` / `canceled`

不可取消的狀態再取消 → `ORDER_NOT_CANCELABLE`

---

## 5. 資金、部位、損益

這幾條公式 v1 測試釘得很死，重寫必須逐條對得上。

```
保證金        locked_margin  = 成交價 × 數量 ÷ 槓桿
權益          equity         = wallet_balance + unrealized_pnl
可用餘額      available      = wallet_balance − locked_margin + unrealized_pnl
未實現損益    unrealized     = (mark_price − entry_price) × qty      （多單）
                             = (entry_price − mark_price) × qty      （空單）
報酬率        return_pct     = (equity − initial_balance) ÷ initial_balance × 100
```

**加倉時進場價取加權平均**：先 1 顆 @100，再 1 顆 @95 → 部位 2 顆、進場價 97.5。

### 驗算範例（重寫後應原封不動通過）

初始 1000，買 1 顆 @100，槓桿 2，行情跌到 95：

```
locked_margin = 100 × 1 ÷ 2         = 50
unrealized    = (95 − 100) × 1      = −5
equity        = 1000 + (−5)         = 995
available     = 1000 − 50 + (−5)    = 945
return_pct    = (995−1000)/1000×100 = −0.5
```

加掛一張限價 1 顆 @99、tick 到 95 成交後：

```
部位           qty 2, entry (100+95)/2 = 97.5, mark 95
locked_margin = 50 + (95×1÷2)      = 97.5
unrealized    = (95 − 97.5) × 2    = −5
equity        = 1000 + (−5)        = 995
available     = 1000 − 97.5 + (−5) = 897.5
```

空單：賣 1 顆 @100 槓桿 2，跌到 95 → 未實現 **+5**；@95 平倉 → 已實現 +5、wallet 1005。

停損：多單 1 顆 @100、停損 96，行情到 95 觸發平倉 → 已實現 **−5**、wallet 995。

**⚠️ `available` 把未實現獲利算進可用保證金**（等同全倉模式）。重寫時確認這是否符合目標交易所的行為。

---

## 6. 多帳戶隔離

同一個 sandbox 下的多個帳戶必須完全隔離：

- 帳戶只看得到自己的 orders / trades / positions，沒交易過就是空陣列
- A 帳戶不能取消 B 帳戶的訂單
- 每個帳戶獨立計算 wallet / margin / equity / 績效
- 一次 tick 要同時正確更新同 sandbox 下所有帳戶

---

## 7. 沙盤回放控制

### 狀態機

```
created ──start──> running ──pause──> paused ──resume──> running
                      │                  │
                      └─────stop─────────┴──> stopped （終態）
```

- `stopped` 是終態：不能 pause、resume、start
- pause 與 stop 都**冪等**，重複呼叫不報錯
- 非 running 的沙盤 tick → `SANDBOX_RUNTIME_NOT_RUNNING`

### 時鐘規則（v1 踩最多坑的地方）

- **只在 tick 推進。** 不得依 wall clock 差值自行往前跳——設定一個過去的 anchor 不該讓重播時間自動前進。
- **auto tick 用實際經過時間。** handler 執行很慢時，下一 tick 要補上經過的時間，不能吃掉。
- **pause / stop 必須等 in-flight tick 跑完**，並保留最後一次 tick 的時間，不能中途截斷。

### Seek（跳轉）

- 只能在 `paused` 狀態 → 否則 `SANDBOX_SEEK_REQUIRES_PAUSE`
- **只能往前，不能往回** → 否則 `REPLAY_CURSOR_MONOTONIC`
- seek 後要重新定價，並撮合這段期間內該成交的掛單

### 不得看見未來

- 重播游標在第一根 K 線之前 → `MARKET_DATA_UNAVAILABLE`，**不得往後找最近的價格頂替**
- K 線查詢的 `to` 超過沙盤當前時間 → 只回傳到當前時間為止

---

## 8. 技術指標契約

指標必須包含這 17 個 key，缺一即違約：

```
sma_20
macd_line, macd_signal, macd_hist
rsi_14
bb_upper_20_2, bb_middle_20, bb_lower_20_2
obv
kd_k, kd_d, kd_j
dmi_plus_di, dmi_minus_di, dmi_adx
ad
bias_20
```

硬性要求：

1. **JSON 不得出現 `NaN`、`Infinity`、`+Inf`、`-Inf`。** 序列太短算不出來就不要輸出那個點。
2. **不得包含晚於重播游標的資料點。** 指標要跟著沙盤時鐘截斷。
3. 每個 key 至少要有資料點（資料足夠時）。

---

## 9. 領域事件

| Topic | 何時發 |
|---|---|
| `live.order.submitted` | 實盤單送出交易所 |
| `live.order.filled` | 實盤單成交 |
| `live.order.reconciled` | 對帳完成 |
| `live.connection.degraded` | 行情來源抓取失敗 |
| `live.connection.recovered` | 行情來源恢復 |

一張實盤單走完流程要能收到 submitted / filled / reconciled 三個事件（審計軌跡）。

訂閱者要能用 topic 過濾，並且能取消訂閱。

---

## 10. 行情供應

- 相同 symbol 重複訂閱要**去重**，訂閱數維持 1
- 閒置的 symbol 要被 GC 回收
- 抓取失敗要標記該 symbol 為 `degraded` 並發事件
- 快照缺 bid/ask 時欄位保持 null，**不要填 0**
- symbol 一律正規化為大寫去空白

---

## 11. 資料集匯入

- CSV 格式：`timestamp,open,high,low,close,volume`，timestamp 為 RFC3339
- 格式錯誤 → 匯入 job 標記 `failed` 並回報錯誤，**不留半筆髒資料**
- **服務啟動時，把所有卡在 `running` 的 job 標記為 `failed`**，錯誤訊息 `interrupted by restart`
- 資料集摘要要帶 `row_count` 與最近一次匯入 job 的狀態

---

## 12. 權限邊界

### 兩個平面

| 平面 | 憑證 | 能做什麼 |
|---|---|---|
| Admin | session cookie，role 必須是 root | 資料集、沙盤、帳戶、回放控制、監控 |
| Agent | account-scoped bearer token | 只有下列端點 |

非 root 的 admin 請求 → **403**。

### Agent 唯一允許的端點

```
GET  /sandbox/time
GET  /market/price
GET  /market/ticker
GET  /market/klines      （to <= sandbox current_time）
GET  /account
GET  /account/performance
GET  /orders
GET  /orders/:id
GET  /positions
GET  /trades
POST /orders
POST /orders/:id/cancel
```

### Agent 明確禁止

- 任何 `/admin/*` → **401**
- 建立 token（`POST /tokens`）→ **401**
- 回放游標控制（seek）、回放速度控制
- 任何晚於 sandbox `current_time` 的行情查詢
- 行情價格竄改、直接改資料庫

### Agent 作業規則

> 要抓 K 線之前，必須先呼叫 `GET /sandbox/time`，用回傳的 `current_time` 當硬上限。
> 沙盤時鐘處沒有資料時，**停下來回報資料不足**，不得用 seek、改速度或查未來 K 線來繞過。

### Token

- 只存 hash，不存明文
- scope 不足 → 驗證失敗（scope：`market:read` / `trade:read` / `trade:write` / `account:read`）
- 輪替（rotate）保留同一個 token id，換新 secret，**舊 secret 立即失效**

---

## 13. 錯誤模型

統一錯誤結構，HTTP status 不進 body：

```json
{ "code": "MARKET_DATA_STALE", "message": "...", "details": {} }
```

錯誤碼是**契約的一部分**，前端與 agent 靠 code 分支，不靠 message。v1 共 80+ 個碼，按前綴分群：

- `INVALID_*` — 參數驗證（400）
- `ACCOUNT_*` / `ORDER_*` / `POSITION_*` / `SANDBOX_*` / `DATASET_*` — 資源不存在與狀態不允許
- `LIVE_*` / `MAINNET_*` / `MARKET_DATA_*` — 實盤與行情閘門
- `REPLAY_*` — 回放約束

完整清單見 `git show v1-legacy:server/domain/contracts.go` 與各 service。

---

## 14. v1 的設計問題（重寫時不要重蹈）

1. **`production` 環境沉默降級成 paper** — 命名與行為不一致，是實盤事故的溫床。
2. **限價單以市價成交** — 樂觀撮合，回測績效系統性偏高。
3. **前端沒有路由也沒有狀態管理** — 全塞在一個 505 行的 `App.tsx` 裡切畫面，單一元件最大 825 行。
4. **兩個 `go.mod`** — 根目錄那個是過期殘骸（宣告 go 1.20、連 gin 都沒列），真正在用的在 `server/`。
5. **測試交付規則產生垃圾** — 舊 `AGENT.md` 要求每個功能都要有 `./test/*.sh`，結果 14 支裡 13 支是 13 行的 `go test` wrapper，零資訊量。規格價值全在 Go 測試裡。
6. **前端有兩份** — `demo-web/`（假資料原型）與 `web/`（真實接線）長期並存，改動要同步兩邊。

---

## 15. 重寫的驗收方式

這份規格的每一節都應該對應到新實作的測試。最低標準：

- **§3 安全閘門**：4 條閘門各一個測試，且驗證預設值是「擋下」
- **§4 撮合**：市價、限價、停損、冪等、衝突各一個測試
- **§5 資金公式**：§5 的兩組驗算數字直接寫成測試
- **§6 隔離**：兩個帳戶同沙盤互不可見、互不可操作
- **§7 時鐘**：wall-clock 不漂移、慢 handler 不吃時間、in-flight tick 不被截斷
- **§7 seek**：只能 paused、只能往前
- **§8 指標**：17 個 key 齊全、無 NaN、不超過游標
- **§12 邊界**：agent 打 admin 端點回 401、非 root 打 admin 回 403
