# 交易沙箱後端 Server 設計文檔

## 1. 文件目的

本文件定義一套可供 **AI / LLM 交易 Agent** 與 **管理後台 UI** 使用的交易沙箱後端系統設計。系統需支援：

- 虛擬帳戶管理
- 沙箱環境管理
- 基本撮合 / 成交邏輯
- 下單功能（一般下單、做多、做空、槓桿、止損）
- 依照沙箱時間的行情查詢
- Token 驗證，供 LLM Agent 安全存取
- 即時監控沙箱狀態、帳戶資產、訂單與成交
- 未來可平滑擴展到 live 模擬交易 / 實盤接軌

本系統最重要的非功能要求是：

> **核心交易能力必須高度模組化、低耦合、可重用，使未來 live trading 模式可直接復用沙箱期所建立的核心 domain/service。**

---

## 2. 專案目標

### 2.1 功能目標

1. 提供多個可獨立運行的 sandbox。
2. 每個 sandbox 可配置：
   - Sandbox ID
   - 開始運行日期 / replay 起始時間
   - 可用帳號數量
   - 帳號初始資金（統一以 USD 計價）
3. 提供每個帳號的 access token，供 LLM Agent 使用。
4. 提供下單與成交模擬能力。
5. 提供 replay 模式與 live 價格模式。
6. 提供 admin 後台可 CRUD sandbox 與帳戶。
7. 提供 real-time 監控與推送。

### 2.2 架構目標

1. **交易核心與資料來源解耦**。
2. **下單/風控/撮合 與 HTTP API 解耦**。
3. **Sandbox mode 與 live mode 共用同一套 trading domain**。
4. **價格來源可替換**（Replay DB / Exchange WS / Third-party API）。
5. **推播層可替換**（WebSocket / SSE / Queue）。
6. **認證機制與帳戶域分離**。

---

## 3. 系統範圍

### 3.1 In Scope

- Sandbox CRUD
- Live account CRUD
- Virtual trading accounts
- Access token generation / rotation
- Order placement / cancellation / query
- Position tracking
- PnL / equity calculation
- Replay market data serving
- Live price cache / queue update system
- Real-time admin monitoring
- Audit log / order event history

### 3.2 Out of Scope（第一版可不做）

- 複雜撮合深度模擬（完整 order book matching）
- 多幣別保證金結算
- 複雜清算引擎
- 多交易所聚合 routing
- 高頻策略優化
- 完整風控規則引擎 DSL

---

## 4. 使用者角色

### 4.1 Admin

用途：
- 建立 / 修改 / 刪除 sandbox
- 建立 / 修改 / 刪除虛擬交易帳戶
- 查看帳戶 token
- 監控 sandbox 執行狀態
- 監控所有帳戶資產與績效
- 管理 live 版帳號

### 4.2 LLM Trading Agent

用途：
- 使用 token 登入或直接以 token 呼叫交易 API
- 查詢行情
- 查詢帳戶
- 建立 / 查詢 / 取消訂單
- 查詢持倉與損益

### 4.3 UI Operator

用途：
- 登入後台
- 監控沙箱即時狀態
- 觀察帳戶收益變化與成交紀錄

---

## 5. 高階架構

```text
[ Admin UI / Monitor UI ]
           |
           v
      [ API Gateway / HTTP Layer ]
           |
   +-------+--------+--------------------+
   |                |                    |
   v                v                    v
[ Auth ]       [ Sandbox App ]      [ Live App ]
                   |                    |
                   +---------+----------+
                             |
                             v
                   [ Trading Core Domain ]
         +-------------------+-------------------+
         |                   |                   |
         v                   v                   v
   [ Order Service ]   [ Position Service ] [ Risk Service ]
         |                   |                   |
         +-------------------+-------------------+
                             |
                             v
                      [ Execution Engine ]
                             |
         +-------------------+-------------------+
         |                                       |
         v                                       v
 [ Replay Price Provider ]              [ Live Price Provider ]
         |                                       |
         v                                       v
 [ Replay Market DB ]                  [ Cache + Update Queue + WS ]


[ Event Bus / PubSub ] --> [ Realtime Push Gateway ] --> UI / Monitor
```

### 5.1 核心原則

- API Layer 不直接操作資料表，必須呼叫 application service。
- Application service 不直接依賴特定價格供應商，必須依賴 interface。
- Trading core 不知道自己跑在 sandbox 還是 live，只知道目前綁定的是哪個 MarketDataProvider / ExecutionPolicy。
- Domain model 與 persistence model 分離。

---

## 6. 分層設計

建議採用以下分層：

### 6.1 Interface Layer

負責：
- HTTP REST API
- WebSocket / SSE
- Admin API
- Agent API
- Auth middleware

不得負責：
- 直接業務邏輯
- 直接寫成交規則
- 直接查詢外部價格 API

### 6.2 Application Layer

負責：
- Use case orchestration
- Transaction boundary
- Permission check
- 呼叫 domain service
- 組裝 DTO

### 6.3 Domain Layer

負責：
- Order model
- Position model
- Account ledger
- Risk rules
- Execution decision
- PnL calculation
- Stop-loss trigger

### 6.4 Infrastructure Layer

負責：
- DB repository
- Redis / cache
- Exchange API / WS client
- Replay DB access
- Token persistence
- Event bus implementation

---

## 7. 核心模組拆分

建議 server 依模組拆分如下：

```text
server/
  cmd/
    api/
  internal/
    auth/
    sandbox/
    account/
    token/
    market/
      replay/
      live/
      cache/
    trading/
      domain/
      app/
      execution/
      risk/
      position/
      pnl/
    order/
    monitor/
    realtime/
    admin/
    shared/
  pkg/
    errors/
    clock/
    logger/
    money/
    uuid/
```

### 7.1 auth

- admin login
- token 驗證
- token scope / role

### 7.2 sandbox

- sandbox CRUD
- sandbox lifecycle
- replay clock
- sandbox state 管理

### 7.3 account

- virtual account CRUD
- live account CRUD
- balance/equity/pnl 查詢

### 7.4 token

- create token
- revoke token
- rotate token
- token hash storage

### 7.5 market

- MarketDataProvider interface
- ReplayPriceProvider
- LivePriceProvider
- PriceCache
- UpdateQueue
- GC scheduler

### 7.6 trading

- 核心交易 domain
- order validation
- leverage rule
- margin rule
- stop-loss handling
- execution flow

### 7.7 monitor / realtime

- sandbox snapshot
- account snapshot
- order stream
- trade stream
- realtime push

---

## 8. 關鍵抽象介面設計

為了達成低耦合，應先定義 interface。

### 8.1 MarketDataProvider

```go
type MarketDataProvider interface {
    GetTicker(ctx context.Context, symbol string, at time.Time) (Ticker, error)
    GetLastPrice(ctx context.Context, symbol string, at time.Time) (Decimal, error)
    GetKlines(ctx context.Context, symbol string, interval string, from, to time.Time) ([]Kline, error)
}
```

說明：
- sandbox mode：`at` 對應 sandbox clock
- live mode：`at` 可忽略或使用 current time

### 8.2 ExecutionEngine

```go
type ExecutionEngine interface {
    ExecuteOrder(ctx context.Context, req ExecuteOrderRequest) (*ExecutionResult, error)
    CancelOrder(ctx context.Context, orderID string) error
}
```

### 8.3 RiskEngine

```go
type RiskEngine interface {
    ValidateNewOrder(ctx context.Context, req OrderRequest, snapshot AccountSnapshot) error
    CheckStopTriggers(ctx context.Context, positions []Position, market MarketSnapshot) ([]TriggeredStop, error)
}
```

### 8.4 AccountLedger

```go
type AccountLedger interface {
    ReserveMargin(...)
    ReleaseMargin(...)
    ApplyTrade(...)
    ApplyFee(...)
    Snapshot(...)
}
```

### 8.5 EventPublisher

```go
type EventPublisher interface {
    Publish(ctx context.Context, event DomainEvent) error
}
```

用途：
- 推 UI 即時更新
- 記錄 audit
- 後續接 queue / Kafka / Redis PubSub

---

## 9. Sandbox 與 Live 的解耦策略

### 9.1 不可耦合的部分

以下邏輯不能寫死在 sandbox 裡：
- order validation
- position open/close
- leverage / margin 計算
- stop-loss trigger 邏輯
- pnl / equity 計算
- order state transition

### 9.2 可替換的部分

以下應透過策略 / provider 注入：
- 價格來源
- 時間來源（clock）
- 成交策略
- 手續費規則
- 風控規則

### 9.3 建議抽象

```go
type Clock interface {
    Now() time.Time
}
```

- `ReplayClock`：由 sandbox timeline 控制
- `SystemClock`：live 模式使用實際時間

```go
type FillPolicy interface {
    TryFill(order Order, market MarketSnapshot) FillDecision
}
```

- sandbox 可先做簡化版成交
- future live paper trading 可改成模擬接近交易所規則

---

## 10. 核心交易能力

### 10.1 訂單型別（第一版建議）

- Market Order
- Limit Order
- Stop Loss Order

第二版可擴充：
- Take Profit
- OCO
- Trailing Stop

### 10.2 方向與模式

- Spot Buy
- Spot Sell
- Long
- Short
- Leverage

### 10.3 基本欄位

- order_id
- account_id
- sandbox_id（live 可為 null）
- symbol
- side（buy/sell）
- position_side（long/short）
- order_type（market/limit/stop）
- qty
- price
- leverage
- stop_price
- status
- created_at
- updated_at
- filled_qty
- avg_fill_price

### 10.4 訂單狀態

- NEW
- PARTIALLY_FILLED
- FILLED
- CANCELED
- REJECTED
- EXPIRED
- TRIGGERED（for stop order）

---

## 11. 撮合 / 成交策略

第一版建議採用 **簡化成交引擎**，避免一開始就做完整 order book 模擬。

### 11.1 Market Order

- 以當前可取得的最新價格成交
- 可附加滑價模型（第二版）

### 11.2 Limit Order

- buy: 當市場價格 <= limit price 時成交
- sell: 當市場價格 >= limit price 時成交

### 11.3 Stop Loss

- long 持倉：當市場價格 <= stop price 觸發
- short 持倉：當市場價格 >= stop price 觸發
- 觸發後轉 market 或 limit（建議第一版先轉 market）

### 11.4 做多 / 做空

建議以統一 position model 實作：
- `position_side = long | short`
- `qty`
- `entry_price`
- `mark_price`
- `leverage`
- `margin_used`
- `unrealized_pnl`

---

## 12. 帳戶模型

### 12.1 帳戶類型

- Virtual Account（sandbox）
- Live Account（future paper/live integration）

### 12.2 帳戶核心欄位

- account_id
- account_name
- account_type
- sandbox_id
- base_currency = USD
- initial_balance
- available_balance
- margin_used
- realized_pnl
- unrealized_pnl
- equity
- status

### 12.3 資產計算

```text
equity = available_balance + margin_used + unrealized_pnl
```

或若採保證金帳戶語意：

```text
equity = wallet_balance + unrealized_pnl
available_balance = equity - locked_margin
```

需在實作前固定一種帳務語意，避免後期混亂。

---

## 13. Sandbox 模型

### 13.1 Sandbox 欄位

- sandbox_id
- name
- mode = replay
- status = draft/running/paused/stopped/completed
- start_datetime
- current_replay_time
- replay_speed
- dataset_id
- created_at
- updated_at

### 13.2 生命週期

- draft
- ready
- running
- paused
- stopped
- completed

### 13.3 Admin 能做的事

- 建立 sandbox
- 指定 sandbox ID
- 指定開始運行日期
- 指定可用帳號數量
- 指定初始資金
- 取得帳號 token
- 啟動 / 暫停 / 停止 sandbox

---

## 14. Token 設計

### 14.1 需求

每個帳戶需提供 token，方便 LLM Agent 呼叫 API。

### 14.2 建議

- 使用 `token_id + secret` 模式
- DB 僅存 hash，不存明文
- token 可綁定：
  - account_id
  - sandbox_id / live account
  - scope
  - expires_at
  - revoked_at

### 14.3 Token Scope

- `trade:read`
- `trade:write`
- `account:read`
- `market:read`
- `admin:*`（僅 admin）

### 14.4 登入頁設計

依需求可簡化為：
- login page
- 輸入 token
- 前端以 token 存 session/local storage
- 後端驗證 token 後決定角色與可見資源

---

## 15. 價格 API 策略

此部分是系統核心之一，必須兼顧：
- 避免打爆外部 API / WS
- replay 可重現
- live 價格可即時更新
- 能支援多 symbol 動態增減

---

## 16. Replay 模式價格策略

### 16.1 目標

將指定時間區間的歷史價格預先存入本地 DB，供 sandbox 使用。

### 16.2 特性

- 高可重現性
- 不依賴當前外部 API
- 適合回測 / 沙箱演練
- 可根據 sandbox clock 查詢對應時間價格

### 16.3 建議資料表

- replay_datasets
- replay_ticks 或 replay_klines

### 16.4 讀取模式

```text
sandbox current time -> replay provider -> local DB -> return closest/target price
```

### 16.5 優點

- deterministic
- 低外部依賴
- 測試容易

---

## 17. Live 時價模式價格策略

### 17.1 需求

為避免打到 API provider rate limit，採用：

- 緩衝池（cache）
- 更新隊列（update queue）
- 去重
- GC

### 17.2 邏輯流程

```text
1. 使用者請求 symbol 價格
2. 若 cache 內已有新鮮資料 -> 直接返回
3. 若 cache 無資料 -> 將 symbol 放入訂閱/更新隊列
4. 背景更新程序負責從外部來源拉取 / 訂閱該 symbol
5. 更新結果寫入 cache
6. 長期未使用 symbol 自動從 queue / cache 清除
```

### 17.3 核心元件

#### A. Symbol Cache

保存：
- latest price
- last update time
- last access time
- subscriber count（可選）
- source meta

#### B. Update Queue / Subscription Registry

保存：
- 目前需要維護的 symbol 集合
- 去重後的待更新列表

#### C. Background Updater

負責：
- 拉取 REST
- 維護 WS 訂閱
- 寫回 cache

#### D. GC Cleaner

負責：
- 清除長期未被查詢 symbol
- 釋放連線與資源

### 17.4 去重策略

同一 symbol 不得重複加入更新隊列。

可使用：
- map + queue
- sync.Map + state flag
- symbol registry state machine

### 17.5 GC 策略

每個 symbol 紀錄 `last_used_at`。
若：

```text
now - last_used_at > expire_duration
```

則從：
- cache
- queue / subscription registry
- 外部 WS 訂閱

中清除。

### 17.6 建議狀態

每個 symbol 可有以下狀態：
- idle
- queued
- active
- stale
- removing

---

## 18. Realtime 監控需求

### 18.1 必須能監控的資料

每個 sandbox：
- sandbox 狀態
- current replay time
- 帳戶列表
- 每個帳戶餘額
- 持倉
- 未實現 / 已實現損益
- 訂單列表
- 成交列表

全系統：
- 所有帳戶資產變化
- 收益變化
- 最近成交
- 最近觸發止損

### 18.2 傳輸方式

建議：
- WebSocket 為主
- SSE 為備選

### 18.3 推播事件類型

- sandbox.updated
- account.balance.updated
- account.equity.updated
- order.created
- order.updated
- trade.executed
- position.updated
- stoploss.triggered

### 18.4 Realtime Gateway

設計為獨立模組，避免 trading core 直接依賴 websocket implementation。

```go
type EventSubscriber interface {
    Subscribe(topic string, handler EventHandler)
}
```

---

## 19. Admin 功能設計

### 19.1 Sandbox CRUD

- Create sandbox
- Get sandbox detail
- List sandbox
- Update sandbox config
- Delete sandbox
- Start / Pause / Stop sandbox

### 19.2 Sandbox 內帳號管理

- 建立 N 個虛擬帳戶
- 設定初始資金
- 顯示 / 重建 token
- 啟用 / 停用帳戶

### 19.3 Live Account CRUD

- Create live account record
- Update credentials metadata
- Enable / disable
- token 管理

### 19.4 監控頁

- sandbox 狀態總覽
- 帳戶績效排名
- order / trade stream
- 風險警告

---

## 20. UI/UX 需求整理

### 20.1 Login Page

- 以 token 登入
- 驗證成功後進入對應頁面
- admin 與 account token 顯示不同權限畫面

### 20.2 基本 CRUD 頁

- Sandbox 管理
- Virtual Account 管理
- Token 建立 / 重置
- Live Account 管理

### 20.3 Realtime 監控頁

- sandbox summary card
- account equity chart
- open orders table
- fill history table
- position table
- recent events feed

### 20.4 必要交互

- 即時刷新
- 篩選 sandbox / account / symbol
- 查看 token（僅 admin）
- 一鍵複製 token

---

## 21. 建議 API 邊界

以下為概念 API，非最終定稿。

### 21.1 Auth

- `POST /auth/token/login`
- `POST /admin/login`
- `POST /tokens`
- `POST /tokens/:id/rotate`
- `DELETE /tokens/:id`

### 21.2 Sandbox Admin

- `POST /admin/sandboxes`
- `GET /admin/sandboxes`
- `GET /admin/sandboxes/:id`
- `PATCH /admin/sandboxes/:id`
- `DELETE /admin/sandboxes/:id`
- `POST /admin/sandboxes/:id/start`
- `POST /admin/sandboxes/:id/pause`
- `POST /admin/sandboxes/:id/stop`

### 21.3 Account Admin

- `POST /admin/sandboxes/:id/accounts`
- `GET /admin/sandboxes/:id/accounts`
- `GET /admin/accounts/:id`
- `PATCH /admin/accounts/:id`
- `DELETE /admin/accounts/:id`

### 21.4 Live Account Admin

- `POST /admin/live-accounts`
- `GET /admin/live-accounts`
- `PATCH /admin/live-accounts/:id`
- `DELETE /admin/live-accounts/:id`

### 21.5 Agent Market API

- `GET /market/ticker?symbol=BTCUSDT`
- `GET /market/price?symbol=BTCUSDT`
- `GET /market/klines?symbol=BTCUSDT&interval=1m`

### 21.6 Agent Trade API

- `POST /orders`
- `GET /orders`
- `GET /orders/:id`
- `POST /orders/:id/cancel`
- `GET /trades`
- `GET /positions`
- `GET /account`
- `GET /account/performance`

### 21.7 Monitor / Realtime

- `GET /admin/monitor/sandboxes/:id/snapshot`
- `WS /ws/admin/monitor`
- `WS /ws/account`

---

## 22. 資料表建議

### 22.1 sandboxes
- id
- name
- mode
- status
- start_datetime
- replay_current_time
- replay_speed
- dataset_id
- created_at
- updated_at

### 22.2 accounts
- id
- sandbox_id
- type
- name
- base_currency
- initial_balance
- available_balance
- margin_used
- realized_pnl
- status
- created_at
- updated_at

### 22.3 account_tokens
- id
- account_id
- token_name
- token_hash
- scope
- expires_at
- revoked_at
- created_at

### 22.4 orders
- id
- sandbox_id
- account_id
- symbol
- side
- position_side
- order_type
- qty
- price
- stop_price
- leverage
- status
- filled_qty
- avg_fill_price
- created_at
- updated_at

### 22.5 trades
- id
- order_id
- account_id
- sandbox_id
- symbol
- qty
- price
- fee
- side
- position_side
- executed_at

### 22.6 positions
- id
- account_id
- sandbox_id
- symbol
- position_side
- qty
- entry_price
- mark_price
- leverage
- margin_used
- unrealized_pnl
- liquidation_price（可選）
- updated_at

### 22.7 replay_datasets
- id
- name
- symbol
- interval
- start_at
- end_at
- source
- created_at

### 22.8 replay_prices / replay_klines
- id
- dataset_id
- symbol
- ts
- open
- high
- low
- close
- volume

### 22.9 event_logs
- id
- event_type
- aggregate_type
- aggregate_id
- payload_json
- created_at

---

## 23. 狀態一致性與帳務原則

此系統不能只改 balance 欄位，必須有明確帳務流程。

### 23.1 建議原則

- 下單時：檢查資金 / 保證金
- 成交時：由 execution service 統一更新
  - order status
  - trade record
  - position
  - account ledger
- 每次成交後發事件

### 23.2 建議

未來若系統變大，可加：
- ledger_entries
- double-entry accounting

第一版可先保留簡化帳務，但要把 ledger service 抽出介面。

---

## 24. 風控需求

第一版至少要做：

- 資金不足拒單
- 槓桿倍數上限
- 空單保證金檢查
- stop-loss 合法性檢查
- sandbox 是否運行中檢查
- symbol 是否可交易檢查

第二版可做：
- 最大倉位限制
- 單帳戶風險暴露限制
- 每日損失限制
- 自動強平

---

## 25. 可觀測性

### 25.1 Logging

需記錄：
- auth event
- sandbox lifecycle event
- order request
- order rejection reason
- trade execution
- stop trigger
- cache miss/hit
- symbol subscribe/unsubscribe

### 25.2 Metrics

- active sandboxes
- active symbols in cache
- cache hit ratio
- order execution latency
- websocket connected clients
- realtime push latency

### 25.3 Audit

Admin 操作需可追蹤：
- sandbox create/update/delete
- token create/revoke/rotate
- account enable/disable

---

## 26. 錯誤處理

需統一 error model：

- validation error
- auth error
- permission denied
- sandbox not running
- insufficient balance
- market data unavailable
- order not found
- token revoked

建議返回統一格式：

```json
{
  "code": "INSUFFICIENT_BALANCE",
  "message": "available balance is not enough",
  "details": {}
}
```

---

## 27. 安全性要求

- token 僅顯示一次明文
- DB 僅存 token hash
- admin API 與 agent API 分離權限
- rate limit
- audit log
- sensitive config 走 env
- 未來可加 IP allowlist

---

## 28. 建議技術落地方向

若以 Go 實作：

- HTTP: Gin / Chi
- DB: PostgreSQL 或 SQLite（開發）
- ORM: GORM 或 sqlc / ent
- Realtime: WebSocket
- Cache: in-memory first，未來可擴 Redis
- Market data live update: goroutine + WS client

重點不是框架，而是 **domain interface 要先穩住**。

---

## 29. 第一版交付優先級

### P0

- sandbox CRUD
- virtual account CRUD
- token create / verify
- replay price provider
- market price query
- order create / cancel / list
- market / limit order execution
- basic long / short / leverage model
- stop-loss
- account balance / position / pnl query
- admin monitor snapshot
- websocket realtime update

### P1

- live price cache + update queue
- symbol dedup
- GC for unused symbols
- live account CRUD
- replay control（pause/resume/speed）

### P2

- 更細緻風控
- 更真實滑價模型
- event sourcing / ledger 強化
- strategy evaluation report

---

## 30. 實作建議順序

### Phase 1: 核心 Domain

先做：
- order model
- position model
- account snapshot
- execution service
- risk service
- pnl service

### Phase 2: Sandbox + Replay

再做：
- sandbox model
- replay clock
- replay market provider
- sandbox account provisioning

### Phase 3: API + Token

再做：
- auth middleware
- account token
- order api
- market api
- account api

### Phase 4: Realtime Monitor

再做：
- event publisher
- websocket gateway
- admin monitor endpoint

### Phase 5: Live Price Layer

最後做：
- symbol cache
- update queue
- dedup
- gc
- ws updater

---

## 31. 風險與注意事項

### 31.1 最大風險

若一開始把 sandbox、HTTP、DB schema、價格抓取、撮合邏輯全部寫死在一起，未來 live trading 幾乎一定要重寫。

### 31.2 必須避免的反模式

- handler 直接改 DB
- order service 直接呼叫第三方價格 API
- sandbox 專用欄位滲透到所有交易邏輯
- websocket 推播直接綁在 domain service 裡
- token 明文存 DB

### 31.3 最重要原則

> 核心交易引擎要只依賴抽象，不依賴 transport、DB、價格來源、時間來源。

---

## 32. 建議 MVP 定義

若要快速做出可用版本，MVP 可定義為：

1. Admin 可建立 sandbox
2. Admin 可建立虛擬帳戶並取得 token
3. LLM Agent 可用 token 查價格、下單、查帳戶
4. 系統可在 replay 模式下依 sandbox 時間成交
5. Admin UI 可即時看到帳戶資產、訂單、成交

只要這 5 點成立，整個系統就已經能作為 AI 交易 Agent 的可用沙箱基座。

---

## 33. 結論

本專案應被視為一個 **可重用交易核心 + 多種執行模式包裝層** 的系統，而不是單純的 sandbox API。

最推薦的設計方向是：

- 先做穩固的 trading core domain
- 用 provider / policy 抽象價格、時間、成交規則
- sandbox 與 live 只作為不同 runtime / adapter
- 以 event 驅動 realtime 監控
- 以 token-based access 供 LLM Agent 使用

如此未來從 sandbox 擴展到 live paper trading，甚至半自動實盤整合時，才能最大程度復用現有代碼。

---

## 34. 建議下一步

1. 先根據此文檔再切一份 `模組分工 / package 結構設計`
2. 再切一份 `DB schema + migration 草案`
3. 再切一份 `REST API 規格`
4. 最後再做 `Go 專案骨架`

這樣就能直接進入實作階段。

