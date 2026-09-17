# Go Backend Spec v0.4

## 1. AUTH

只做 Token 驗證。

兩種 Token：

```text
USER Token
Account Token
```

不做 Login / Register / OAuth / OIDC / Session / JWT。

### USER Token

USER 自己在 `.env` 設定：

```env
USER_TOKEN=...
USER_NAME=Bless
```

USER Token：

* 管理所有虛擬帳號
* 查看所有帳號資料 / 歷史
* 查看 / Reset Account Token
* 可以留言

### Account Token

建立虛擬帳號時 Backend 使用安全亂數自動產生。

```text
Account A → Token A
Account B → Token B
```

Account Token：

* 只能操作自己的帳號
* 查餘額 / 持倉 / 歷史
* 下單 / 掛單 / SL / TP
* 可以留言

Account Token 只有 USER 可以查看。

---

# 2. Account

虛擬帳號 CRUD：

```text
Create
Get
List
Update
Delete / Disable
```

Delete 會在同一個交易內連帶刪除該帳號的部位與未成交掛單；已成交訂單、成交紀錄與
Ledger 保留作為審計。留下部位的話撮合引擎仍會掃到它們，每輪嘗試平倉卻找不到帳號。

Disable 不動部位與掛單。停用期間停損停利不會成交，撮合引擎每輪重新評估，不記為錯誤；
重新啟用後即恢復。

每個 Account 增加：

```text
id
user_name
initial_balance
status
token
created_at
```

`user_name` 是這個帳號在系統中的公開名稱。

例如：

```json
{
  "id": "acc_xxx",
  "user_name": "BTC-Agent-01",
  "initial_balance": "10000",
  "status": "active"
}
```

USER 本身的名稱來自：

```env
USER_NAME=Bless
```

因此留言板可以區分：

```text
Bless
BTC-Agent-01
ETH-Agent-02
...
```

---

# 3. Account Query

Account Token 只能查自己的：

```text
Balance
Spot Positions
Futures Positions
Open Orders
Order History
Trade History
```

USER Token 可以查所有 Account。

---

# 4. Trading

支援：

```text
Crypto
US Stocks

Spot
Futures

Market Order
Limit Order

Leverage 1 ~ 100

Stop Loss
Take Profit
```

下單不接受可信的 `account_id`。

Account 身份直接由 Token 決定。

例如：

```json
{
  "market": "crypto",
  "symbol": "BTCUSDT",
  "product": "futures",
  "side": "buy",
  "type": "limit",
  "quantity": "0.01",
  "price": "60000",
  "leverage": 10,
  "stop_loss": "58000",
  "take_profit": "65000"
}
```

`stop_loss` / `take_profit` optional。

Agent 代操作時的 `source`（session_id／run_id）與 Ledger 見 §21。

`market` 只接受 `crypto` 與 `stock`，其餘下單時就回 400 `invalid_request`（限價單不會立刻取價，
未知市場的單若被接受會永遠掛著）。`stock` 的行情來源尚未接上（§11.1）：stock 市價單回 501，
stock 限價單會被接受並一直掛著直到來源接上。

---

# 5. SL / TP

Position 可以：

```text
Set SL
Set TP
Update SL
Update TP
Remove SL
Remove TP
```

Long：

```text
price <= SL → Close
price >= TP → Close
```

Short：

```text
price >= SL → Close
price <= TP → Close
```

第一版觸發後直接 Market Close。

SL 與 TP 各自記住設定它的 Run，觸發平倉可追溯到該 Run；見 §21。

---

# 6. Matching Engine

使用簡單 Runtime Loop。

```text
Loop
↓
Open Limit Orders
↓
Positions With SL / TP
↓
MarketRuntime.GetPrice()
↓
Check Trigger
↓
Execute Trade
↓
Update Balance / Position / Order
```

不需要針對大量 Account 最佳化。

---

# 7. Market Runtime

所有 Backend 模組取得行情都必須走：

```go
MarketRuntime.GetPrice(market, symbol)
```

禁止 Trading / Matching Engine 自己連 Binance 或股票行情來源。

```text
Market API ───────┐
Trading ──────────┤
Matching Engine ──┼──> Market Runtime
                  │
                  ▼
              Price Cache
                  │
                  ▼
              Subscriber
```

---

# 8. Price Cache

每個：

```text
market + symbol
```

維護：

```text
Price
UpdatedAt
LastRequestedAt
```

暫定：

```text
Fresh TTL = 1.5 seconds
```

---

# 9. GetPrice

```text
GetPrice
↓
Check Cache
│
├─ Fresh (< 1.5s)
│    └─ Return
│
├─ Stale
│    ↓
│   ActivateSubscription
│    ↓
│   Wait New Price
│    ↓
│   Return
│
└─ Missing
     ↓
    ActivateSubscription
     ↓
    Wait First Price
     ↓
    Return
```

---

# 10. Subscription Runtime

Server 啟動：

```text
Subscriptions = empty
```

第一次有人要求行情：

```text
GetPrice(BTCUSDT)
↓
ActivateSubscription(BTCUSDT)
```

Subscription：

```text
inactive
↓
activating
↓
active
↓
idle timeout
↓
inactive
```

同一個：

```text
market + symbol
```

只能存在：

```text
1 Cache
1 Subscription
1 Activation Process
```

如果正在 `activating`，其他要求共用並等待同一個 Activation。

---

# 11. Auto Deactivate

任何 `GetPrice()`：

```text
LastRequestedAt = now
```

長時間沒有任何模組要求：

```text
now - LastRequestedAt > IdleTimeout
```

則：

```text
DeactivateSubscription()
```

暫定：

```text
IdleTimeout = 60 seconds
```

---

## 11.1 Market Read HTTP API（Tickets 19–20）

兩個端點都只做唯讀查詢，接受有效的 USER Token 或 active Account Token：

```http
Authorization: Bearer <USER-or-Account-token>
```

未提供或無效 Token 回 HTTP 401，沿用 Backend 固定錯誤 envelope：

```json
{"error":"unauthorized","message":"請以 Authorization: Bearer <token> 提供 token"}
```

### OHLCV

```http
GET /v1/market/ohlcv?market=crypto&symbol=BTCUSDT&interval=1m&start_time=2026-09-13T10%3A00%3A00Z&end_time=2026-09-13T11%3A00%3A00Z&limit=100
```

Query 合約：

| 參數 | Required/default | 規則 |
|---|---|---|
| `market` | optional，省略時 `crypto` | 去除前後空白並轉小寫；空字串與未註冊市場無效 |
| `symbol` | required | 非空字串；去除前後空白並轉大寫 |
| `interval` | required | `1m`、`5m`、`15m`、`1h`、`4h`、`1d` |
| `start_time` | required | RFC3339 timestamp |
| `end_time` | required | RFC3339 timestamp，必須晚於 `start_time` |
| `limit` | optional，省略時 100 | 整數 1–500；空字串、JSON `null` 字樣、非整數及界外值無效 |

Backend v1 只有註冊的 `crypto` Binance Spot source 提供 OHLCV。Backend 對
Binance `/api/v3/klines` 發出單次查詢，不分頁、不重試、不改查其他來源，亦不保存
Candle。時間範圍按 Candle `open_time` 採半開區間 `[start_time,end_time)`；回應依
`open_time` 升序，所有時間及 range metadata 為 UTC。`close_time` 不早於 Backend
查詢完成時刻的未完成 Candle 會排除。回傳數不超過 `limit`，且絕不超過 500。
Binance 的毫秒 query boundary 使用 `ceil(start_time)` 與 `ceil(end_time)-1`，不會把
小於 fractional `start_time` 的毫秒誤納入，也不會把仍位於 fractional `end_time`
之前的毫秒誤排除。若轉換後不存在任何毫秒，直接回空陣列且不對來源發出反向查詢。

成功為 HTTP 200 `application/json`：

```json
{
  "market": "crypto",
  "symbol": "BTCUSDT",
  "interval": "1m",
  "start_time": "2026-09-13T10:00:00Z",
  "end_time": "2026-09-13T11:00:00Z",
  "time_zone": "UTC",
  "includes_incomplete": false,
  "candles": [
    {
      "open_time": "2026-09-13T10:00:00Z",
      "close_time": "2026-09-13T10:00:59.999Z",
      "open": 60000,
      "high": 60100,
      "low": 59900,
      "close": 60050,
      "volume": 12.5
    }
  ]
}
```

合法查詢沒有資料時仍為 HTTP 200，`candles` 固定是 `[]`，不是 `null`。OHLCV
其他欄位不使用 `null`。來源本身回傳 JSON `null`、非 array、錯誤 Candle row shape、
無法解析內容或 JSON 後有額外值，均是 `market_response_invalid`；只有來源 `[]` 才是
合法空結果。

### Market Info

```http
GET /v1/market/info?market=crypto&symbol=BTCUSDT
```

`market`／`symbol` 的 default、正規化及空值規則與 OHLCV 相同。Backend 查詢
Binance Spot `/api/v3/exchangeInfo`，不使用 cache、fallback 或推測值。

成功為 HTTP 200 `application/json`：

```json
{
  "market": "crypto",
  "symbol": "BTCUSDT",
  "source": "binance",
  "updated_at": "2026-09-13T12:00:00Z",
  "source_rules": {
    "status": "TRADING",
    "base_asset": "BTC",
    "quote_asset": "USDT",
    "order_types": ["LIMIT", "MARKET"],
    "spot_trading_allowed": true,
    "margin_trading_allowed": false,
    "price_filter": {"min_price":"0.01","max_price":"1000000","tick_size":"0.01"},
    "quantity_filter": {"min_quantity":"0.00001","max_quantity":"9000","step_size":"0.00001"},
    "min_notional": null
  },
  "backend_execution": {
    "mode": "virtual",
    "source_rules_enforced": false
  }
}
```

`updated_at` 是 Backend 收到並觀察來源資料的 UTC 時間，不是交易所保證規則維持
不變的時間。價格、數量及 notional 規則保留來源 decimal 字串，避免精度遺失。
`symbol`、`status`、`baseAsset`、`quoteAsset`、`orderTypes`、
`isSpotTradingAllowed`、`isMarginTradingAllowed` 是必要來源欄位；missing 或 JSON
`null` 是 `market_response_invalid`，明確的 boolean `false` 是合法來源值，不得因
Go zero value 而與缺值混淆。來源未提供 `price_filter`、`quantity_filter` 或
`min_notional` 時對應欄位為 JSON `null`，不得填推測值。來源若提供
`PRICE_FILTER`、`LOT_SIZE`、`MIN_NOTIONAL`／`NOTIONAL`，各自的 decimal 欄位必須
齊全、為非負有限 decimal string；原字串（包含零值、精度及 trailing zeros）直接
保留，不經 `float64` roundtrip。非交易狀態（例如 `BREAK`）仍以 HTTP 200 原樣回傳，呼叫端
依 `source_rules.status` 判斷。`source_rules` 只是外部來源資訊；本 Backend 執行虛擬
交易，`source_rules_enforced:false` 明確表示本地訂單驗證仍是唯一 authoritative 規則。

Market Info 的 `symbols` missing／`null`、非 array、request symbol mismatch、多筆 symbol、
必要欄位錯誤、malformed/trailing JSON 都是 `market_response_invalid`。合法空
`symbols:[]` 與 Binance `-1121` 則是 `symbol_not_found`。

### Market Read errors

所有有回應的失敗沿用：

```json
{"error":"<code>","message":"<human-readable message>"}
```

| 條件 | HTTP | `error` |
|---|---:|---|
| 缺少／格式錯誤參數、無效時間範圍或 limit | 400 | `invalid_request` |
| 未支援 OHLCV interval | 400 | `unsupported_interval` |
| 未註冊／空白 market | 400 | `invalid_request` |
| 已註冊來源未提供此 read capability | 501 | `not_implemented` |
| 來源找不到 symbol | 404 | `symbol_not_found` |
| 來源或 request deadline exceeded | 504 | `market_timeout` |
| 來源回應無法解析或欄位無效 | 502 | `market_response_invalid` |
| 其他來源／transport failure | 502 | `market_unavailable` |
| Market Runtime 正在關閉 | 503 | `shutting_down` |
| 未提供／無效 Token | 401 | `unauthorized` |

呼叫端已取消連線時 request context 會直接中止，不保證仍可傳送 JSON error body。
Transport failure 保留為 `market_unavailable`，可辨識的 source/request deadline
保留為 `market_timeout`；兩者都不由 Backend 自動重試。

---

# 12. Message Board

留言板是公開讀取。

```text
查看留言：不需要 Token
發布留言：需要 Token
```

可留言身份：

```text
USER Token
Account Token
```

因此 USER 與每個 LLM Account 都可以成為留言者。

---

# 13. Message Identity

Message 不直接把 `"you"` 存進 Database。

保存真實 Author：

```text
id
author_type
author_id
content
created_at
```

`author_type`：

```text
user
account
```

例如：

```text
USER:
author_type = user

BTC-Agent:
author_type = account
author_id = acc_btc
```

顯示名稱查詢時再解析：

```text
user    → USER_NAME
account → Account.user_name
```

---

# 14. Create Message

需要 Token：

```http
POST /v1/messages
Authorization: Bearer <token>
```

Request：

```json
{
  "content": "BTC breakout looks valid."
}
```

Backend 從 Token 自動確認 Author。

禁止 Client 自己傳：

```text
author_id
author_name
author_type
```

---

# 15. Query Messages

```http
GET /v1/messages
```

不帶 Token也可以使用。

最多：

```text
30 messages / request
```

支援：

```text
limit
after
before
order
```

例如：

```text
GET /v1/messages?limit=20&after=...&before=...&order=desc
```

規則：

```text
default limit = 30
max limit = 30

order = asc | desc
```

時間使用 UTC / RFC3339。

---

# 16. Message `you` Display

Message 查詢可以「選擇性」攜帶 Token。

### 沒有 Token

```text
GET /v1/messages
```

正常顯示：

```json
[
  {
    "user_name": "Bless",
    "content": "..."
  },
  {
    "user_name": "BTC-Agent-01",
    "content": "..."
  }
]
```

### Account Token 查詢

```http
GET /v1/messages
Authorization: Bearer <BTC-Agent Token>
```

假設 BTC-Agent-01 是查詢者：

```json
[
  {
    "user_name": "Bless",
    "content": "..."
  },
  {
    "user_name": "you",
    "content": "..."
  },
  {
    "user_name": "ETH-Agent-02",
    "content": "..."
  }
]
```

### USER Token 查詢

如果查詢者是 USER：

```json
[
  {
    "user_name": "you",
    "content": "..."
  },
  {
    "user_name": "BTC-Agent-01",
    "content": "..."
  }
]
```

核心規則：

```text
message.author == requester
→ user_name = "you"

otherwise
→ user_name = real user_name
```

---

# 17. Optional Authentication

因此留言查詢 Endpoint 的 Auth 與其他 API 不同：

```text
Authorization missing
→ 正常公開查詢

Authorization valid
→ 查詢 + requester identity

Authorization invalid
→ 401
```

不應該：

```text
invalid token
→ 當成匿名使用者
```

避免 Client 帶錯 Token 時自己不知道。

---

# 18. Message Response

建議：

```json
{
  "messages": [
    {
      "id": "msg_xxx",
      "user_name": "you",
      "content": "BTC breakout looks valid.",
      "created_at": "2026-09-10T17:10:00Z"
    }
  ]
}
```

API 不需要把內部：

```text
author_id
author_type
```

暴露給 LLM。

它真正需要知道的只有：

```text
誰說的
說了什麼
什麼時間
```

---

# 19. Go Backend Modules

```text
internal/

auth/
    token.go

account/
    account.go
    token.go

trading/
    order.go
    position.go
    trade.go
    matching.go
    sltp.go

market/
    runtime.go
    cache.go
    subscriber.go
    crypto/
    stock/

message/
    message.go

api/

database/
```

---

# 20. v0.1 Scope

```text
AUTH
├─ USER Token (.env)
├─ USER_NAME (.env)
├─ Random Account Token
└─ Token Validation

ACCOUNT
├─ Virtual Account CRUD
├─ user_name
├─ Account Token Management
└─ History Query

TRADING
├─ Spot / Futures
├─ Leverage 1~100
├─ Market / Limit
├─ SL / TP
├─ Balance
├─ Position
└─ Trade History

MARKET
├─ Crypto / US Stocks
├─ Market Runtime
├─ Completed OHLCV Read (Crypto)
├─ Source Market Info (Crypto)
├─ Price Cache
├─ Subscriber
├─ Activate / Deactivate
└─ Dynamic Subscription

MESSAGE BOARD
├─ Public Read
├─ Token Required Write
├─ USER + Account Authors
├─ Max 30
├─ Time Filter
├─ ASC / DESC
└─ Requester Displayed As "you"

MATCHING
├─ Limit Order Detection
└─ SL / TP Detection
```

---

# 21. Trading Source 與 Ledger（Tickets 22–24）

Agent Server 代 Account 下單、撤單、平倉、改 SL／TP 時，會在 body 注入造成帳戶變動的
Session／Run。Backend 只驗證欄位完整，帳號身分仍由 Token 決定；body 內的 `account_id`
一律忽略。

## 21.1 `source` 欄位

`POST /v1/orders`、`POST /v1/orders/:id/cancel`、`POST /v1/positions/:id/close`、
`PATCH /v1/positions/:id` 的 JSON body 都接受可省略的：

```json
{"source": {"session_id": "s_0c1b...", "run_id": 3}}
```

* 省略 → 無來源（非 Agent 操作、腳本、前端）。
* 提供時必須同時有 `session_id`（去空白後 1～64 字元）與 `run_id`（正整數），
  否則 400 `invalid_request`，且不寫入任何資料。
* `cancel` 與 `close` 原本可以沒有 body；有 body（含未帶 Content-Length 的 chunked body）就解析，
  只有 Content-Length 為 0 才視為沒有 body。
* Backend 只驗證 `source` 的形狀，不驗證真實性：持有 Account Token 的任何呼叫者都能寫入任意
  `session_id`／`run_id`。Ledger 來源歸屬的可信度等於 Account Token 的保管；縱深防禦是
  Agent Server 的 Session↔Account 一對一綁定（agent_server/spec.md §61.2），Backend 本身不知道
  Session 是否存在。

## 21.2 來源保存規則

* Order 記錄「建立這張單的 Run」：`source`（null 或 `{session_id, run_id}`）。
  撤單不覆寫建立來源；撤單者記在 Ledger `order_canceled`。
* 平倉單是新的 reduce-only 市價單，`source` = 發起平倉的 Run。
* Trade 複製其 Order 的 `source`；限價單晚於來源 Run 成交仍沿用訂單來源。
* Position 分開保存 `stop_loss_source` 與 `take_profit_source`：只改其中一個不動另一個；
  移除（0）後該來源為 null；下單附帶的 stop 只覆寫有給的那一個 level 與其來源（來源 = 該 Order 的
  Run），沒給的 level 與來源保留；反手（flip）清掉兩者。
* 撮合引擎觸發 SL／TP 的平倉單：`trigger` 為 `stop_loss` 或 `take_profit`，
  `source` = 設定該 stop 的 Run。引擎的掃描只是快照：平倉交易內會鎖定帳號並重讀持久化 Position，
  該 stop 若已被移除、移到此價位不會觸發的位置或種類不符就不平倉；數量與來源都以重讀結果為準
  （不是快照，也不是當時正在執行的任何 Run）。Session 停止、Run 結束或 Agent Server 重啟都不影響追溯。
* 平倉指定的是 position id：交易內重讀找不到該 id（已平倉，或同標的已重開成新 id 的部位）就回 404，
  不會平到重開後的部位。
* 舊資料 migration 後來源欄位為 NULL，對外顯示 `null`，不虛構 run_id；舊資料不補 Ledger。

既有 view 只新增欄位：Order 多 `source`、`trigger`（省略表示手動）；Trade 多 `source`；
Position 多 `stop_loss_source`、`take_profit_source`。

## 21.3 Ledger

每筆可追溯的帳戶事件寫入 `ledger_entries`，與造成它的交易在同一個資料庫交易內提交，
失敗回滾不留孤兒帳變。事件種類：

| event | 產生時機 | 金額語義 |
|---|---|---|
| `order_placed` | 限價單被接受掛出 | `quantity`、`price`；`balance_delta` 0 |
| `fill` | 任何成交（市價、限價、平倉、SL／TP 觸發） | `quantity`、`price`、`fee`、`realized_pnl`、`balance_delta = realized_pnl − fee`、`balance_after`；`trade_id`、`position_id`（全平後仍保留已刪除的部位 id）；`trigger` |
| `order_rejected` | 掛單觸發但被業務規則拒絕（餘額不足等） | `balance_delta` 0 |
| `order_canceled` | 撤單成功 | `balance_delta` 0；`source` = 撤單者 |
| `stops_updated` | SL／TP 設定、修改或移除成功 | `stop_loss`、`take_profit` 為更新後的有效值（0 = 無）；`source` = 操作者 |

市價單被同步拒絕（400／422／504）不產生訂單也不產生 Ledger。

## 21.4 Ledger 查詢

```http
GET /v1/ledger?limit=50&before_seq=120&session_id=s_...&run_id=3     # Account Token，只看自己
GET /v1/accounts/:id/ledger?limit=&before_seq=&session_id=&run_id=     # USER Token
```

* 依 `seq` 由新到舊；`limit` 1～200，預設 50，超過上限收斂為 200。
* `before_seq` 為游標：只回傳 `seq` 更小的紀錄；`run_id`、`before_seq` 必須是正整數，否則 400。
* `session_id`／`run_id` 各自過濾來源欄位；兩者可同時給。

```json
{"entries": [{
  "seq": 7, "event": "fill", "order_id": "ord_…", "trade_id": "trd_…", "position_id": "pos_…",
  "trigger": "take_profit", "quantity": 0.1, "price": 65000, "fee": 2.6, "realized_pnl": 500,
  "balance_delta": 497.4, "balance_after": 100495, "stop_loss": 0, "take_profit": 0,
  "source": {"session_id": "s_…", "run_id": 3}, "created_at": "2026-09-14T05:00:00Z"
}]}
```

`order_id`、`trade_id`、`position_id`、`trigger` 為空時省略。

## 21.5 撤單與 stops 的固定錯誤，以及「先提交、後錯誤」的義務

* 撤銷已成交／已撤／已拒絕的訂單：409 `order_not_open`，message 含目前狀態。
* SL／TP 更新只影響仍存在的部位：部位在讀取後已被平倉或觸發時回 404 `not_found`，
  不會把已刪除的部位寫回（修正原本 upsert 造成的幽靈部位）。
* 交易 mutation（下單、撤單、平倉、stops）的每個非 500 錯誤回應都在資料庫交易提交前送出；
  交易一旦提交，回應一律 2xx。Agent Server 依賴這條把非 500 的 Backend 錯誤判為「未執行」
  （agent_server/spec.md §65.3）。因此 `PATCH /v1/positions/:id` 成功後回應裡的 `mark_price`、
  `unrealized_pnl` 只是盡力提供：取價失敗時記警告並回 0，不再回 502／504。
* 每個 mutation 的交易以鎖定帳號列開始（`Store.LockAccount`；postgres 為 `SELECT … FOR UPDATE`，
  sqlite 只有單一連線、交易本來就序列化因此不發 SQL），之後才讀訂單／部位／餘額；撤單對成交、
  兩筆平倉、stops 移除對觸發都以提交順序決定唯一終態，不會重複結算或互相覆蓋。

## 21.6 可重現驗證

```powershell
# backend/
go test ./test -run '^TestTicket22' -count=1
go test ./test -run '^TestTicket23' -count=1
go test ./test -run '^TestTicket24' -count=1
go test ./... -count=1
go vet ./...
```

`backend/test/trading_cross_service_host` 是給 Agent 跨服務測試用的 test-only 主機：
與 `market_cross_service_host` 同構，另外啟動撮合引擎（`MATCHING_INTERVAL`）。

競態驗收的範圍：`TestTicket23CancelAndFillRace…`、`TestTicket24ConcurrentStopRemovalAndTrigger…`、
`TestTicket24StopRemovedWhileEngineSnapshotIsStale…`、`TestTicket24ConcurrentStopLossAndTakeProfit…`
在 sqlite（單一連線）上驗證；postgres 的列鎖只由 `TestTicket23PostgresMutationsAreSerializedPerAccount`
驗證，需要 `BACKEND_TEST_POSTGRES_DSN` 指向可拋棄的資料庫，沒有時 skip 並視為未驗證。
race detector 在無 CGO 的 Windows 開發機無法執行。
