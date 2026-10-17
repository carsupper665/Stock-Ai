# Account max_dd 實作計畫與驗收規定

日期：2026-09-23。本輪只有計畫與測試規定，不實作功能、不新增測試程式。

## Current State

### 已確認需求

- 建立戶頭增加選填 `max_dd`，輸入百分比，UI 同步支援。
- 可虧損金額以**原始 initial_balance**計算，包含未實現損益，以 equity 判斷。
- 達到門檻後取消掛單、自動全平、停止該戶頭交易。
- 允許 USER 管理者手動解除；保留原 max_dd，解除後只計新的虧損。
- 因此百分比的金額基準和每輪虧損的起算點不同，兩者不可混用：

```text
loss_limit = initial_balance × (max_dd / 100)
equity = balance + Σ Unrealized(position, mark_price)
equity_floor = max_dd_baseline_equity − loss_limit
觸發：equity <= equity_floor

第一次 baseline_equity = initial_balance
管理者解除後 baseline_equity = 全平後的 equity（無持倉時等於 balance）
```

例：原始額度 10,000、max_dd=10，每輪 loss_limit 都是 1,000。第一次門檻 9,000；全平扣費後 balance=8,990，管理者解除後 baseline=8,990，新門檻 7,990。**不能改寫 initial_balance、不能把下一輪額度改成 899、不能改成歷史最高 equity 的回撤。**

### 實際架構與流程

- `frontend/src/pages/accounts/Page.vue` 的新增 Modal 目前只有 user_name、initial_balance；`create()` 經 accounts/api.js → core/api.js 送 POST `/v1/accounts`。
- `backend/internal/api/account.go` 的 `createAccount` 解析 DTO，呼叫 `account.Service.Create`；service 驗證帳號資料、產生 Token，透過 `database.Store.CreateAccount` 保存。
- `database.Account` 已有 InitialBalance、Balance、Status；`Store.Migrate` 使用 AutoMigrate。Account CRUD 和交易 mutation 以 `LockAccount` 序列化。Postgres 使用 row lock；SQLite 單連線。
- `trading.Summary` 的 equity 已是 Balance + Unrealized；損益公式在 `trading.Unrealized`。所有價格來自 `market.Runtime.GetPrice`。
- `trading.Engine.Tick` 目前先處理限價單，再處理 SL／TP，只載入 `OpenLimitOrders` 與 `PositionsWithStops`。沒有 stops 的持倉不會被這條掃描路徑檢查。
- `trading.Service.settle` 共用於市價成交、限價成交、手動／SL／TP 平倉，處理 margin、fee、Position、Balance、Trade、Ledger；order 本身由 caller 在同一 transaction 建立／保存。
- `closeAt` 在 transaction 內重新讀持倉 ID，避免重複平倉或平到重開的新部位。目前非空 trigger 一律視為 SL／TP，需明確擴充 max_dd 分支。
- `Account.Status=disabled` 會影響 Account Token 認證，且 settle 會拒絕停用戶頭成交。不能直接拿它當風控停止狀態，否則查詢與系統全平都可能被擋住。
- Console 對 `/v1/*` 已有通用代理，不需為新 action 修改代理。Agent 透過既有交易 API 呼叫，交易權限必須由 backend 最終把關。

## Proposed Approach

沿用 Account CRUD、Trading Service、Matching Engine、Store 的現有責任，新增一個交易模組內的 `drawdown.go`，集中風控計算與全平協調；不建立另一個 risk server、背景 goroutine、scheduler 或行情訂閱系統。

Account 增加三個 nullable 欄位：

| 欄位 | 意義 |
|---|---|
| `max_dd` / `MaxDD *float64` | 百分比；null 表示未啟用 |
| `max_dd_baseline_equity` / `MaxDDBaselineEquity *float64` | 本輪起算 equity；啟用時必有值 |
| `max_dd_stopped_at` / `MaxDDStoppedAt *time.Time` | 已觸發停止的持久化標記；非 null 時不能普通下單 |

`max_dd` 接受有限數值 `0 < value <= 100`，可帶小數；省略或 null 為未設定，0 不是「停用」。首次建立時設定 baseline=initial_balance。本次不增加一般 PATCH 修改 max_dd，以保持新增戶頭與解除後沿用原設定的範圍。

停止流程分成兩個可恢复的階段：

1. **同一 transaction**：鎖戶頭、以最新資料確認門檻、寫停止標記、取消所有 open orders、寫風控事件與撤單 Ledger，commit。
2. 標記已落盤後，逐筆經原結算路徑 reduce-only 全平。每筆 Order／Position／Balance／Trade／Ledger 原子提交。某筆失敗不解除停止、不撤銷其他已成功平倉；後續 Tick 重試剩餘部位。

不新增 persisted「全平完成」旗標：停止標記加上目前剩餘持倉／open orders 就能判定；避免兩份狀態不一致。

管理者解除用明確 action：`POST /v1/accounts/:id/trading/resume`，只接受 USER Token。必須已停止、無持倉、無 open orders；在 row lock 下重設 baseline=Balance、清除停止標記，並將行政 Status 設為 active。max_dd、initial_balance、balance、Token 不變。

## Implementation Steps

### 1. 保存設定與停止狀態

1. **File/module**：`backend/internal/database/models.go`、`account.go`；需要無分頁 open-order 查詢時擴充 `trading.go`。
2. **Existing behavior**：Account model 自動遷移，Store 已支援帳號交易與欄位限定更新。
3. **Exact change**：新增上述三欄；新增 `ListMaxDDAccounts(ctx)`，選出已設定 max_dd 或已停止的戶頭，包含行政 disabled。提供欄位限定的 `MarkAccountMaxDDStopped(ctx,id,at)` 與 `ResumeAccountTrading(ctx,id,baseline)`；只做持久化，不計算門檻。提供 `OpenOrdersForAccount(ctx,id)` 無分頁查詢，不能用 UI 的 limit 代替全部 open orders。
4. **Interfaces/types/functions affected**：Account、Store 上述方法；新增 Ledger event 常數 `max_dd_triggered`、`max_dd_resumed`。沿用 CreateLedgerEntry、SaveOrder、ListPositions、LockAccount、Tx。
5. **Data/control flow**：service 驗證後寫設定；trading 在已持有 row lock 的 transaction 內寫停止標記；account admin service 原子解除。
6. **Error/state handling**：新欄 nullable、舊戶頭維持 null；不得遷移成 0 或啟用限制。重複 Migrate 不改既有值。mark／resume 只更新所屬欄位，不能用 stale Account 覆寫 Balance／Token。資料庫故障回 rollback；標記與撤單不可部分成功。
7. **Why here**：schema、查詢與原子寫入屬於 repository；百分比判斷與強平政策不下沉至 Store。既有 AutoMigrate 已可處理新增 nullable 欄，預設不改 db.go、不新增 migration framework。

### 2. 建立戶頭與管理者解除

1. **File/module**：`backend/internal/account/account.go`。
2. **Existing behavior**：CreateInput／Create 負責設定驗證，Update、ResetToken 已用 LockAccount 和欄位限定寫入。
3. **Exact change**：CreateInput 加 MaxDD；Create 驗證數值有限且在 (0,100]，有設定才初始化 baseline。新增 `ResumeTrading(ctx,id)`；在 Tx 第一個步驟 LockAccount，重讀狀態，確認已觸發、MaxDD/baseline 完整且無持倉／掛單，更新 baseline／marker／Status 並寫 `max_dd_resumed` Ledger。
4. **Interfaces/types/functions affected**：CreateInput、Create、ResumeTrading；新增 ErrInvalidMaxDD、ErrTradingNotStopped、ErrLiquidationPending。一般 UpdateInput 不增加 max_dd；rename、status、ResetToken 不可清理風控狀態。
5. **Data/control flow**：建立設定→儲存；解除 action→service 驗證→Store transaction→回傳新的 Account。因為只有全平才可解除，baseline 可直接取 Balance，不需要 account service 呼叫行情或 trading.Summary。
6. **Error/state handling**：未停止／重複解除為 409；尚有部位或 open orders 為 409；找不到戶頭為 404。解除失敗全部 rollback。兩個解除並行只有一個成功，第二個不能再次移動 baseline。一般 PATCH active 只改行政狀態，不解除 max_dd；disabled→resume 是使用者明確要求重新啟用的 action。
7. **Why here**：管理者建立與重設戶頭政策是 Account administration；不應由 UI 直接改停止旗標或由 trading process 代管 admin API。

### 3. 擴充 API 契約

1. **File/module**：`backend/internal/api/account.go`、`router.go`、`trading.go`。
2. **Existing behavior**：USER group 提供 CRUD／Token reset；accountView 同時供 USER 查詢及 Account 自查使用；各 handler 做 error mapping。
3. **Exact change**：createAccountRequest 加 `MaxDD *float64`。accountView 回傳三個 nullable 欄位，另回傳 nullable `max_dd_loss_limit`／`max_dd_equity_floor` 供 UI 清楚展示；計算呼叫 Step 4 的交易政策函式，handler 不自行定義公式。註冊 USER-only resume action；handler 不收 initial_balance、balance、baseline 或解除用的新百分比。
4. **Interfaces/types/functions affected**：createAccountRequest、accountView、viewAccount、createAccount、resumeAccountTrading、新 route；writeAccountError／writeTradingError 映射新錯誤。
5. **Data/control flow**：DTO→Account service 或 Trading service→回傳 DTO。`ErrMaxDDStopped` 統一 HTTP 409 / `max_dd_stopped`；缺足夠報價不能評估風險時回既有 market timeout，或 503 / `risk_price_unavailable`；本次不修改 native Account authentication。
6. **Error/state handling**：數字 10 表示 10%，字串 `"10"`、bool、物件、陣列、0、負數、>100 都回 400。JSON 的無效數字由 decoder 拒絕，直接 service 呼叫的 NaN／Inf 也需拒絕。未設定輸出 null，不能顯示成 0%。Token 的 includeToken 邊界不變。
7. **Why here**：HTTP 只負責型別、身分、status/code 和表示；不查價格判斷強平、不自行修改交易資料。

### 4. 集中 equity 判斷、停止與重試

1. **File/module**：新增 `backend/internal/trading/drawdown.go`。
2. **Existing behavior**：Summary／Unrealized 已有正確帳務語意，但沒有帳戶級風控。Summary 自己讀 Store／取價，不能在持有 tx 時直接呼叫，否則可能讀到不一致資料或在 SQLite 單連線死鎖。
3. **Exact change**：新增純函式 `DrawdownLimits(initialBalance,baseline,maxDD)`，只回傳 loss_limit、equity_floor，供交易判斷與 view 使用。新增私有 risk price preparation／check／enforcement 函式：先在 transaction 外經 GetPrice 收集價格，再在 LockAccount 後重讀 Account 和完整 ListPositions，使用既有 Unrealized 累加 equity。
4. **Interfaces/types/functions affected**：公開只需要 DrawdownLimits；例如私有 `checkMaxDD(ctx,tx,accountID,marks) (stopped bool,err error)` 與 `enforceMaxDD(ctx,accountID,marks) (closed int,err error)`。價格 map 沿用 Matching 的 `key(market,symbol)` 和 `map[string]float64`，不另建 price cache。
5. **Data/control flow**：先檢查已持久化標記，再檢查配置與完整 mark set；達門檻即寫標記、取消全戶 open orders、寫 Ledger。enforce 在該 transaction commit 後，逐筆以 max_dd trigger 呼叫 closeAt。已 stopped 的戶頭不再要求重新證明 equity 低於門檻；即使行情回升也必須完成清倉。
6. **Error/state handling**：首次評估缺任一持倉報價，不用 0、不使用部分 equity 判斷；停止新增交易並待可用報價。row lock 後出現尚未收價的新標的，回 risk_price_unavailable，不拿舊持倉 snapshot 放行。已 stopped 時可先平有報價的部位，缺價的留下重試。停止事件每輪只寫一次；重複 Tick、API 呼叫不再寫相同事件。金融金額比較沿用 float64，但固定比較容差為絕對 `1e-8` 金額單位，明確定義為 `equity <= floor + 1e-8`，不可拿 quantity 的 epsilon 當百分比容差。
7. **Why here**：這是 Trading 的帳戶風控政策與平倉協調，不屬於行情服務、transport、Provider adapter 或 shared utility。共用 Unrealized，避免另一套損益公式。

### 5. 阻擋新交易並沿用結算

1. **File/module**：`backend/internal/trading/trading.go`、`settle.go`、`stops.go`、`validate.go`。
2. **Existing behavior**：PlaceOrder 的 limit path 直接建單；market／fill／close 呼叫 settle；closeAt 的非空 trigger 只支援 SL／TP。settle 會檢查行政 active、reduce-only、margin、fee，並寫完整帳務。
3. **Exact change**：PlaceOrder（market／limit）及 fillOpenOrder 在已有 account lock 的 transaction 裡做 max_dd 檢查；限制已觸發就不新增／成交。已停止時拒絕普通 PlaceOrder、手動 ClosePosition、SetStops；保留 CancelOrder 和讀取。新增私有 `TriggerMaxDD="max_dd"`，closeAt 只在重新讀到 marker 非 null 且指定持倉 ID 仍存在時建全量 reduce-only order，Source 為空。
4. **Interfaces/types/functions affected**：PlaceOrder、execute、fillOpenOrder、closeAt、SetStops、settle、isBusinessRejection；新增 ErrMaxDDStopped。HTTP PlaceOrderInput 不新增 trigger、bypass 或 force 欄位。
5. **Data/control flow**：普通請求 transaction：lock→risk precheck→原 mutation→保存 order→risk postcheck→commit。postcheck 用最新 Balance／Positions，涵蓋手續費或成交造成的越線；order 必須先保存成 filled，不能被同 transaction 的全戶撤單誤撤。成交已完成才觸發風控時仍回真實 fill，戶頭同 transaction 進入 stopped，由 Engine 清倉。
6. **Error/state handling**：precheck 新寫入停止標記時，callback 必須回 nil 使停止與撤單 commit；用外部 `blocked` 結果在 Tx 成功後回 ErrMaxDDStopped，不能把業務拒絕直接返回 callback 使標記 rollback。max_dd 系統全平是唯一可繞過行政 disabled／risk stopped 的結算來源；仍驗證持倉與 reduce-only，不得反向開倉。只對這個內部全平分支放寬「平倉手續費餘額不足」檢查，照實扣費，允許最後 Balance 為負，不把數字 clamp 到 0。普通交易規則不變。
7. **Why here**：最後能阻止 stale／並行交易的是交易 mutation 內的 row-lock 檢查；API 提前檢查或 Engine snapshot 都不能取代它。強平重用 settle 的帳務，不直接刪 Position 或直接改 Balance。

取價必須在 transaction 外；保留 `max_dd=null` 的原有快速路徑，不為未設定的戶頭額外拉全戶行情。未停止的手動 reduce-only 平倉若自己的報價可用，但其他持倉報價缺失，仍允許沿用原平倉路徑降低風險；本次無法完整做 postcheck 時由下輪 Tick 評估。不可把普通的 quote 缺失變成略過**新增風險交易**檢查的理由；也不可吞掉實際 DB 失敗。

### 6. 擴充原 Matching Engine

1. **File/module**：`backend/internal/trading/matching.go`。
2. **Existing behavior**：每輪聚合標的價格一次，先限價單、後 stops；只掃有 stops 的部位。
3. **Exact change**：載入 max_dd candidates，利用既有 ListPositions 收集其全部持倉（含無 SL／TP），與 orders／stop positions 一起交給 collectPrices 去重。先對風控戶頭 enforce，再跑原限價／SL／TP；最後對 candidates 再做清理，處理本輪成交費用造成的新 stopped 狀態。
4. **Interfaces/types/functions affected**：Tick、collectPrices 的呼叫資料；沿用 TickResult.Closed 計入已成功的系統平倉，Errors 計入失敗，不新增另一套 scheduler。fillOpenOrder 因風控取消的 order 不可被 default 分支算成 Filled。
5. **Data/control flow**：完整持倉／掛單→本輪價格 map→risk sweep→既有 matching→risk cleanup。每個 account 的實際決策都在 service 重新鎖定讀取；Engine snapshot 只選候選者。
6. **Error/state handling**：一個戶頭缺價／失敗不擋其他戶頭。已 stopped、即使行政 disabled 仍掃描清倉。重啟後由 DB marker 和剩餘持倉繼續，不依賴 memory queue。Close／Stop 沿用原 lifecycle；不保證真實世界瞬間成交，只保證下一個有足夠行情的 Tick 嘗試，並留下失敗訊息。
7. **Why here**：背景檢查與重試是現有 Engine 的職責；不能要求 Agent 或 UI 開著才會觸發風控。

### 7. 新增表單與風控／解除 UI

1. **File/module**：`frontend/src/pages/accounts/Page.vue`、`Detail.vue`、`api.js`。
2. **Existing behavior**：Page 擁有新增表單與清單；Detail 擁有管理 action；api.js 是頁面端點集合。既有 Modal、confirm、say、useLoad 可直接復用。
3. **Exact change**：新增「最大虧損 max_dd（%，選填）」數字輸入；說明「每輪可虧損金額以原始額度計算，含浮動損益；達門檻自動全平並停止交易」。留空時省略欄位，不能 Number('')→0。清單顯示百分比／未設定、獨立風控停止標記。Detail 展示 loss_limit、baseline、floor、停止時間；不要把風控停止誤顯示為 Token 失效。
4. **Interfaces/types/functions affected**：form、create、template；新增 `resumeTrading(id)` 呼叫新 POST action。Detail 加確認 Modal／操作按鈕，確認文字明示「沿用 max_dd 和原始額度，從本次全平後權益重新起算」。不給使用者輸入 baseline 或改寫 balance。
5. **Data/control flow**：表單→既有 createAccount→reload；解除→USER API→成功後重新載入 account／summary。管理動作以 server 回傳欄位為準，UI 不自行宣布已解除或全平完成。
6. **Error/state handling**：百分比空值、非有限、<=0、>100 顯示錯誤；server 仍是最終驗證。尚未全平解除回 409 時顯示原因，不清 badge。pending 時禁用重複按鈕；response 丟失先重新查詢狀態，不盲目重送解除。舊資料缺／null 欄位顯示未設定，不顯示 NaN。行情失敗時仍顯示已儲存的風控設定／停止狀態。
7. **Why here**：帳戶頁擁有這個表單與管理動作；不新增全域 risk store，不把業務判斷放進 core/api、shared/format 或 Agent UI。

### 8. 同步規格

1. **File/module**：`backend/SPEC.md`、`frontend/SPEC.md`。
2. **Existing behavior**：規格列出 Account 欄位、行政 Disable、matching、Ledger 與戶頭 UI，尚無 max_dd。
3. **Exact change**：記錄新增 nullable 欄位、數字／null 語意、以原始額度算固定金額、每輪 baseline、含未實現損益、停止／部分失敗／解除契約、新錯誤碼與 Ledger events。
4. **Interfaces/types/functions affected**：文件中的 Create／Get／List／self account／resume API 範例。
5. **Data/control flow**：記錄來源→policy→持久化→matching→UI；舊 Disable 與新 risk stop 明確分開。
6. **Error/state handling**：記錄強平費用／跳價可能使最終損失超過門檻，這是觸發線而非保證成交價。未完成的行情／Postgres 併發驗收不得記為通過。
7. **Why here**：保留契約來源，後續實作者不需從 UI 文案猜測不同輪次的起算規則。

## Files Changed

**預定修改：**

- Backend database：`internal/database/models.go`、`account.go`、`trading.go`。
- Backend service：`internal/account/account.go`、`internal/trading/trading.go`、`settle.go`、`stops.go`、`validate.go`、`matching.go`。
- Backend API：`internal/api/account.go`、`router.go`、`trading.go`。
- Frontend：`src/pages/accounts/Page.vue`、`Detail.vue`、`api.js`。
- 規格：`backend/SPEC.md`、`frontend/SPEC.md`。

**必要新增：**只有 `backend/internal/trading/drawdown.go`，承載帳戶風控政策與執行。

**明確不改：**

- `database/db.go` 的遷移架構、auth／Token 驗證、行情 Runtime／sources／subscription、Console proxy。
- Agent Runtime／Session lifecycle、LLM providers、Claude／Codex adapter、交易工具 registry。
- Balance／fee／Unrealized 的計算公式、普通 SL／TP 的來源歸屬與撮合價語意。
- 不新增 max_dd 一般編輯／清除功能、不新增入金或重設 initial_balance 功能。
- 本輪不新增測試程式；下面是後續實作必须满足的完整驗收規定。

## Invariants

- null 的舊戶頭行為完全相容，不因 max_dd 掃描新增其行情依賴或交易限制。
- 每轮 loss_limit 永遠由原始 initial_balance 與 max_dd 決定；只有管理者解除可移動 baseline。
- baseline 不跟最高 equity 上移，mark-to-market 浮虧與已實現損益／已扣費一起計入。
- marker 非 null 時不能增加任何新交易，無論 HTTP、Agent、stale matching snapshot 或服務重啟。
- 必須先將停止與撤單 commit，再嘗試可部分失敗的逐筆全平；trigger 不會因價格回升自動解除。
- 所有 mutation 都先 LockAccount，再重讀持久化資料；取價不持有 DB transaction。
- 每筆平倉仍有一張 Order、一筆 Trade、一筆 fill Ledger；取消掛單不產生成交或手續費。
- 系統風控 Source=null，Trigger=max_dd；不得假冒原 Agent Run、SL／TP 的設定者。
- 普通行政啟用、改名、Token reset 不移動 baseline、不清 marker；只有 resume action 可以。
- 風控停止不改 Account Token 身分與讀取能力。行政 disabled 原有認證效果仍保留。
- 權益評估／強平只在 backend；UI／Agent 不擁有第二套風控決策。

## Risks / Edge Cases

- **報價不完整**：首次無法算全戶 equity 時拒絕新增風險，不以缺價=0；已 stopped 則先平可取得價的部位，其餘重試。
- **並行成交／解除**：價格 snapshot 不是權限；交易內以持久化 marker／baseline／positions 重判。某個舊 close task 不得平掉解除後新建的 position ID。
- **交易 callback rollback**：返回 ErrMaxDDStopped 不能把剛寫入的停止事件一起 rollback。
- **部分全平／崩潰**：成功部分保留、marker 保留；重啟後只處理剩餘 positions，已平部分不再次扣費。
- **手續費／跳價**：觸發當下 equity 和最終 balance 可不同；resume 取全平後真實 Balance，不取門檻或之前的估計。
- **數值边界**：直接 service 的 NaN／Inf、100%、小數百分比、threshold 附近浮點值都需明確驗證；不得改成 trailing drawdown。
- **資料狀態不完整**：MaxDD 非 null 但 baseline 缺失不能當成未設定放行；視為資料不一致，拒絕新交易並記錄錯誤，不自行重設起算點。
- **停止交易與停止 Agent Run 不同**：本次是後端拒絕交易並清倉；不跨服務直接終止 Agent 的推理 Run。既有 Agent 錯誤處理會收到 max_dd_stopped。

## Verification

以下規定不代表已執行，也不是只要測試程序沒有 crash 就算通過。後續測試集中 `backend/test/` 與 `frontend/test/`；不用真實交易帳號或真實行情。

### 共用驗收資料與判定

- 沿用 `backend/test/trading_fixture_test.go` 的 tradingAPI、settablePriceSource、可控時鐘、engine.Tick，以及 stall/resume barriers。多標的擴充現有 fixture 的 symbol→price 對照，不另建 server framework。
- 主案例：initial_balance=10,000，max_dd=10；先以零 maker/taker fee 驗證純公式。建立 futures long 10 units，entry=1,000，leverage=10，無 SL／TP；初始 margin=1,000，balance=10,000。
- 報價從 1,000→900 時 Unrealized=-1,000，equity=9,000，觸發；全平後 balance=9,000，positions=[]，open orders=[]，marker 非 null。每個場景用新 DB，不共用先前測試狀態。
- 金額驗證採絕對誤差 <=1e-8；ID、事件數、交易狀態與百分比使用精確 assertions。測試預期值使用以下已算好的數字，不能呼叫 production 計算函式生成 expected。

### A. 建立、查詢、遷移（API／Service／Store）

| ID | 操作 | 必須驗證 |
|---|---|---|
| A1 | POST 省略 max_dd，另測 null | 201；三個 raw risk 欄位與兩個 limit 欄位均 null；交易照原流程 |
| A2 | 建立 10、0.5、100 | 201；baseline=10,000；loss_limit=1,000／50／10,000；floor=9,000／9,950／0 |
| A3 | 輸入 0、-1、100.01、字串、bool、物件、陣列 | 400；沒有新 Account／Token／Ledger；direct service NaN、±Inf 同樣拒絕 |
| A4 | Create→List→Get→Account self→重啟再讀 | raw設定、baseline、marker、derived limits 一致；Account self 不含 Token |
| A5 | 用舊 schema 建戶頭後 Migrate 兩次 | 舊戶頭 null、balance/token 不變；第二次不重設任何新欄值 |
| A6 | 模擬 Create／設定保存失敗 | 不留下半建立帳戶；錯誤不洩漏 SQL 或 Token |

### B. 觸發公式與全戶範圍

| ID | 操作 | 必須驗證 |
|---|---|---|
| B1 | 主案例 price=900.01，呼叫 Tick | equity=9,000.1；未停止、仍持倉、無風控事件 |
| B2 | price=900，Tick 一次 | 剛好等於門檻也觸發；一筆全量 reduce-only fill；balance=9,000；風控 marker 與一次 max_dd_triggered |
| B3 | price=899.99 | equity=8,999.9；觸發；最終 balance=8,999.9，不能被改寫成9,000 |
| B4 | price先=1,200，再=1,000 | 從峰值少2,000但equity仍10,000；不能觸發，baseline不能跟峰值變動 |
| B5 | long+short、spot+futures、多標的，分別浮盈300／浮虧1,300 | 合計 equity=9,000 才觸發；全部部位均全平，不只虧損或有 stops 的部位 |
| B6 | 沒有任何 SL／TP、沒有掛單、UI和Agent均未開啟 | 單靠 Engine Tick 仍觸發；不能依賴收到新 API 請求 |
| B7 | 零部位，balance已達floor | 不取行情；寫停止事件並取消掛單；不產生假 Trade |
| B8 | 同時有 SL／TP 且達戶頭門檻 | 戶頭風控先處理；每個 position 最多一筆全平；Trigger=max_dd，不假借 stop source |
| B9 | marker已建立後價格回升 | 仍完成全平，marker不自動清除；ordinary order仍被拒絕 |

### C. 金額、費用與交易阻擋

- **C1 掛單清理**：觸發前放至少三張未成交掛單，包含本輪已符合限價條件者。觸發後全部 canceled，各有一次 order_canceled Ledger，沒有 fill／fee；重複 Tick 不增加事件。超過 UI 預設分頁筆數的掛單也必須全撤。
- **C2 停止後 API**：market、limit、Agent來源 PlaceOrder、手動 ClosePosition、SetStops 均回 409/max_dd_stopped，且無新交易資料；查帳務、歷史、Ledger 與 CancelOrder 保持原授權語意。
- **C3 API先於Tick**：價格已達門檻但尚未Tick時下單，API本身必須停止並撤单、拒絕新單；之後GET可看見marker，證明沒有因return error rollback；下一Tick完成全平。
- **C4 費用造成越線**：準備balance=9,000.5、無部位，baseline=10,000。以1,000價格買1 unit、taker fee=0.001，成交fee=1後equity=8,999.5；這張真實fill保留，marker同transaction提交，後續不得再開倉。
- **C5 強平費用**：以預置部位隔離建倉費，主案例在price=900強平，taker fee=0.001；close fee=10×900×0.001=9，realized=-1,000，最後balance=8,991。Order／Trade／Ledger金額一致。
- **C6 嚴重虧損與費用不足**：以fixture準備可使最終balance為負的部位，觸發後仍可reduce-only全平；最終負數照實保存，不創新倉、不略過fee。普通非強平訂單仍按既有margin規則拒絕。
- **C7 行政disabled**：已觸發風控的戶頭再被管理者停用，系統強平仍完成；一般disabled戶頭原本的API認證和SL／TP暫停行為不變。

### D. 解除、重新起算與持久化

- **D1 解除成功**：沿C5結果，USER呼叫resume，200；max_dd仍10、initial_balance仍10,000、balance仍8,991、baseline=8,991、loss_limit仍1,000、新floor=7,991、marker=null、Status=active，Token不變；新增一次max_dd_resumed事件。
- **D2 下一輪**：從D1建立新部位，equity=7,991.1不觸發，=7,991觸發。以fixture安排價格／數量，不用直接改baseline。不能按8,991×10%得到錯誤額度899.1。
- **D3 重啟**：解除後、下一輪觸發後分别重啟；每輪baseline與marker都保留，沒有返回第一輪9,000門檻。
- **D4 未完成清倉**：仍有一個position或一張open order時，resume=409/liquidation_pending，baseline／marker不變；價格恢復、清倉完成後才可成功。
- **D5 未停止／重複解除**：409/trading_not_stopped；不能藉反覆點擊或HTTP重送逐次移動baseline。兩個USER同時解除只有一次成功、一次事件。
- **D6 權限**：Account Token呼叫任何戶頭resume均403；缺Token=401；USER對不存在id=404。POST不接受客户端傳入的balance／baseline／initial_balance進行重設。
- **D7 其他管理操作**：改名、Token reset、PATCH disabled/active都不清marker、不改baseline；先stopped再PATCH active仍不能下單。
- **D8 解除交易失敗**：在更新或Ledger寫入注入DB錯誤；baseline、marker、Status和Ledger全rollback；再次合法解除只能產生一筆事件。

### E. 行情、資料庫與崩潰恢復

- **E1 首次缺價**：兩個標的只提供一個價格，不能用部分equity觸發；新增風險訂單回明確quote錯誤、無fill。補齊後下一Tick能正常判斷。未停止的手動reduce-only平倉若本身價格可用，仍可降低風險。
- **E2 已停止後缺價**：先確定marker落盤，再讓其中一個標的缺價；另一個可正常平倉，marker保留，resume拒絕；恢復後只平剩餘部分。
- **E3 停止transaction故障**：在mark、撤第二張order或Ledger寫入注入錯誤；整個標記／撤單transaction rollback，不能出現marker=null卻只撤一半；API不執行新交易，下輪可重試。
- **E4 平倉transaction故障**：在第二個position的Trade或Ledger寫入注入錯誤；該position的Balance／Order／Trade／Ledger／Position全部rollback，第一個已成功的平倉保留。重試不重複第一筆fee。
- **E5 崩潰點**：分別在marker提交後、首筆平倉提交後、全部平完但未解除時關閉並重開store/runtime。每種情況都維持停止，重試剩餘持倉，絕不依賴舊memory queue。
- **E6 跳價**：首次觸發价與後續可用平倉价不同，使用真實選定的執行價寫fill；balance可以低於觸發線。報告必須區分trigger equity與最終balance。
- **E7 資料完整性**：fixture寫入max_dd非null／baseline為null的資料；交易不可默認未設定或自動補initial，需報錯且無fill。

### F. 真正並行驗收（不能只靠 SQLite）

使用 `BACKEND_TEST_POSTGRES_DSN` 指向可丟棄DB，沿用 `ticket23_postgres_race_test.go` 的多連線與barrier方式。每項至少重複20次，用row-lock取得順序建立可驗證的兩種結果；不用隨機sleep宣稱覆蓋race。

1. **門檻觸發 vs market／limit下單**：若停止先commit，普通order無fill／無新open order；若fill先commit，風控必須重讀新position／Balance再判斷並清倉。不能讓stale Active()/marker snapshot放行。
2. **全平 vs 手動close／SL／TP／另一個Tick**：同position最多一筆最終close、一次fee；position已不存在者走原已關閉／inactive路徑，不能反向開倉。
3. **trigger vs 行政Update／Token reset**：新Balance、marker、baseline、Token各自保留，不發生stale SaveAccount覆寫。
4. **resume vs 最後一筆全平**：全平先提交可解除；resume先取得鎖但尚有position則409。不能解除後留下舊輪風控close繼續運作。
5. **resume→新position vs 舊Tick候選**：舊Tick必須重讀新baseline／marker；舊position ID不可用來平掉新position，不能用前一輪equity重新停止。
6. **取價途中新增標的**：用priceSource barrier在transaction外取價時插入新symbol部位；鎖內完整positions缺mark時不能放行新增風險交易，下一輪有完整價格後再評估。

缺Postgres時記「並行驗收未完成」，不能拿SQLite單連線通過替代。

### G. UI 操作與網路契約

沿用 Vitest／happy-dom／Vue createApp、fetch mock 與真實頁面掛載模式；測行為與request body，不以搜尋Vue原始碼字串代替互動。

1. 新增Modal留空max_dd送出：body不含max_dd；輸入10→JSON number10；輸入0.5保留小數；0、負、>100不得送request並顯示錯誤。
2. 建立後reload／切換戶頭／重開頁面：percent、baseline、floor都來自該戶頭response，不殘留另一戶資料；null顯示未設定。
3. marker非null時顯示「風控停止」和停止時間；不得顯示「已全平」除非實際持倉資料證明已清空；行情summary失敗不隱藏風控設定。
4. 管理者按解除先顯示每輪固定金額及重新起算說明；取消不發POST；確認只發一次正確id的POST，pending期間不能重複送出。
5. 409 liquidation_pending：保留停止狀態和舊baseline，顯示原因；200後reload並顯示新floor。失敗／斷網不能提前清badge或改成active。
6. 原始10,000、10%、全平8,991的回傳應顯示下一輪loss_limit1,000、floor7,991，不能顯示899.1或9,000。
7. 窄螢幕檢查新增欄位、清單、Detail與確認Modal可操作，label說明含百分比、原始額度、浮虧與自動全平；不引入新的全局版面改造。

### H. 執行與通過門檻

實作完成後應提供命令（新增測試命名採 `TestAccountMaxDD...`；此刻尚未建立）：

```bash
go -C backend test ./test -run '^TestAccountMaxDD' -count=1
go -C backend test -race ./test -run '^TestAccountMaxDD' -count=20
go -C backend test ./...
go -C backend vet ./...
npm --prefix frontend test -- --run test/accounts-max-dd.test.js
npm --prefix frontend test
npm --prefix frontend run build
```

必須確認regex確實選到測試，`no tests to run`不算通過。Postgres併發另設定既有DSN環境；未配置、未跑、skip、無行情案例都不能算對應驗收完成。

最終交付需附：A–G每項結果、使用的fee／價格序列、觸發前後Account與Ledger斷言、故障注入位置、Postgres並行結果、UI互動結果。所有資料為測試帳戶；不做真實下單。

計畫已排除：獨立風控微服務、額外scheduler、通用risk framework、全局共享狀態、Agent端重複判斷、修改原始額度、普通帳戶無關重構，以及本輪測試程式實作。
