# Go Autonomous Agent Server v1 Specification

## 1. 目標

Agent Server 使用 **Go** 實作，負責執行與管理長生命週期 Autonomous Agent。

系統主要目標：

- Agent 自主規劃與 Tool Call
- Agent 自主建立未來喚醒 Event
- Session lifecycle 管理
- Run / History 管理
- Memory 管理
- Token / Tool / Trading 等監控
- Crash Recovery
- 提供 Control Panel 所需 API
- 所有 Agent 行為可依 `session_id + run_id` 追溯

核心原則：

> User 設定 Agent 的使命與模型設定；Agent 自己決定下一步做什麼，以及什麼條件下需要再次被喚醒。

---

# 2. 核心架構

```text
Go Agent Server
│
├─ Session Manager
│
├─ Agent Loop
│  ├─ LLM Runtime
│  ├─ Planning / Decision
│  ├─ ToolRegistry
│  ├─ Memory Access
│  └─ Run Finalization
│
├─ Event Loop
│  ├─ Active Events
│  ├─ Trigger Matching
│  └─ Agent Wakeup
│
├─ Memory
│
├─ Run History
│
├─ Monitoring / Usage
│
├─ Storage
│  ├─ DB
│  └─ Run JSON Files
│
└─ HTTP API
```

Agent Loop 與 Event Loop 分離：

```text
Agent Loop
= 現在要做什麼

Event Loop
= 什麼時候再次叫醒 Agent
```

## 2.1 EventLoop 任務生命週期

`EventLoop` 是 Server 內排程任務與後續 Agent Event 檢查共用的公開生命週期邊界：

- `RegisterEvent(name, task, interval, count)` 註冊任務。`count=1` 是一次性、正整數是精確執行次數、`count=-1` 是常駐任務；`count=0`、小於 `-1`、非正 interval、空白名稱及 nil callback 均拒絕。
- 同名 active 任務回傳 `ErrAlreadyExist`；loop 正在 stopping 時註冊回傳 `ErrLoopStopping`。任務自然完成或刪除後可重新使用名稱。
- `TaskFunc` 接收每次執行獨立的 `context.Context`；callback 返回（包含 error 或 panic）後該 context 會被取消。`DelEvent` 與 `Stop` 也會取消已派發工作；callback 必須合作觀察 context，Go runtime 不保證強殺任意 goroutine。
- 同一任務不重疊執行。callback 尚未結束時錯過的週期不排隊、不補跑；結束後最多依當下時間派發下一次。
- `DelEvent` 阻止未來派發。已由 scheduler 擷取的 callback 仍可能開始，但會收到已取消的 context；`Wait` 在該 callback 離開後回傳 `ErrEventDeleted`。
- `Wait(ctx, name)` 等待任務終止。自然完成時回傳最後一次 callback error；panic 轉為含任務名稱的 error。刪除與 loop 停止分別回傳 `ErrEventDeleted`、`ErrLoopStopped`，呼叫端等待逾時則原樣回傳 `ctx.Err()`。終止結果獨立保留，不依賴 active 排程項目仍存在。
- `Start` 重複呼叫回傳 `ErrLoopRunning`。`Stop(ctx)` 立即停止新派發、取消工作並等待 callback；不合作工作超過期限時回傳 `ctx.Err()`，此時 lifecycle 保持 stopping，第二個 loop 不會啟動。待舊工作離開並完成 Stop 後才可再次 Start。
- 任一 callback error 或 panic 只終止該次 callback，不終止 scheduler 或其他任務。

正式環境使用 Go timer；測試可透過 `NewEventLoopWithClock` 注入實作 `Clock`／`Timer` 的可控制時鐘。Timer 以絕對 deadline 建立及重設，重設時若 deadline 已到必須立即觸發，避免時鐘在計算與重設間前進而遺失喚醒。時鐘只決定排程，不改變 callback cancellation 與 lifecycle 同步語義。

## 2.2 Server 後台任務與關機

Server 直接組裝 `EventLoop`，不另設後台任務管理層：

- `NewServer(loop, tasks, resources, logger, shutdownTimeout)` 把每個 `BackgroundTask` 以 `count=-1` 註冊為 Server 常駐任務。這些任務不屬於 Session，也不計入 `max_active_events`。
- `loop=nil` 回傳 `ErrEventLoopRequired`；`shutdownTimeout<=0` 回傳 `ErrShutdownTimeoutInvalid`。任一任務註冊失敗時，回傳包住 EventLoop 原始錯誤的錯誤，並刪除本次已註冊的任務，不接管或關閉傳入資源。
- `Start(listener)` 在 HTTP 開始服務前啟動 EventLoop；nil listener、重複啟動及已進入關機的 Server 分別回傳 `ErrListenerRequired`、`ErrServerRunning`、`ErrServerStopped`。HTTP listener 非預期失敗會自動開始相同關機流程，`Wait(ctx)` 回傳該錯誤。
- `Shutdown()` 是可直接由測試或平台 hook 呼叫的入口。第一次呼叫先關閉 listener，停止接收新請求，再停止 EventLoop 派發並取消工作；HTTP 中既有請求與 EventLoop 工作完成後，才依 `resources` 傳入順序關閉資源。listener 已由此流程關閉時，HTTP shutdown 回傳的 `net.ErrClosed` 是預期結果，不作為關機錯誤。callback error／panic 只影響該次工作，不停止 Server。
- 關機是冪等的；重複或並行呼叫不會重複停止或關閉資源。若 `shutdownTimeout` 到期，`Shutdown` 回傳包住 `context.DeadlineExceeded` 的錯誤；關機仍在背景等待未完成工作，且不會提早關閉其資源。之後可用 `Wait(ctx)` 等待最終結果。資源關閉錯誤以 `errors.Join` 回傳，並在 logger 尚可用時記錄。
- 正式入口為 `go run ./cmd/agent-server`。監聽位址取自 `AGENT_SERVER_ADDR`，缺值時為 `127.0.0.1:8080`。程序先註冊終止通知，之後才開啟 listener、啟動 Server 並輸出 `ready: listening on <address>`；收到通知後呼叫上述關機入口，預設收尾期限 10 秒。Unix 使用 `SIGTERM`／interrupt；Windows console 使用 Ctrl+C 或 Ctrl+Break，Go 會將後者交付為 `os.Interrupt`。

### 程序關機驗收

精確命令（從 `agent_server/` 執行）：

```text
go test ./test -run TestAgentServerProcessShutsDownOnPlatformSignal -count=1 -v
```

只需要 Go toolchain，不需要資料庫、固定 port 或外部服務。測試把 binary 建至 `t.TempDir()`，以 `AGENT_SERVER_ADDR=127.0.0.1:0` 啟動，等待 readiness log，再於 Unix 對獨立 process group 發送 `SIGTERM`，或於 Windows 對以 `CREATE_NEW_PROCESS_GROUP` 建立的程序發送 `CTRL_BREAK_EVENT`，並驗證 10 秒內以成功狀態退出。測試結束由 `t.TempDir` 移除 binary；失敗路徑會強制結束並等待子程序，避免殘留程序或 listener。

部分無 console 的 Windows test host 不支援 `GenerateConsoleCtrlEvent`；測試會明確以 unsupported 原因 skip，並強制清理子程序，不把 kill 當成 graceful shutdown 通過。這是該宿主的明確未覆蓋項，不宣稱 Windows signal 驗收成功。

### Health HTTP 合約

```http
GET /health
```

成功回應為 `200 application/json`，無 request body、參數或可為空欄位：

```json
{"status":"ok"}
```

其他 method 回應 `405` 並帶 `Allow: GET`：

```json
{"status_code":405,"error":"METHOD_NOT_ALLOWED","msg":"method not allowed"}
```

未知 route 回應 `404`：

```json
{"status_code":404,"error":"NOT_FOUND","msg":"route not found"}
```

---

# 3. Session

Session 是對外唯一主要 Agent 抽象。

一個 Session 代表：

> 一個長生命週期 Autonomous Agent Instance。

Session 建立後設定不可修改。

## 3.1 Session Status

```text
stopped
running
deleted
```

### stopped

Agent 不執行，不接受 Event 喚醒。

### running

Agent 可執行 Run，Event Loop 可喚醒 Agent。

### deleted

Soft Deleted。

資料保留供：

- Audit
- Experiment comparison
- History inspection

但：

```text
deleted Session 不可 restore
deleted Session 不可重新 start
```

---

# 4. Create Session

## Endpoint

```http
POST /api/v1/sessions
```

## Required

```json
{
  "name": "BTC Agent",
  "account_id": "acc_xxx",
  "model_name": "model-name"
}
```

只有：

```text
name
account_id
model_name
```

為 required。`account_id` 綁定已存在且啟用的 Backend Account；Agent Server 不建立
Account，也不保存 Account Token。同一 Account 在審計紀錄生命週期內只能綁定一個
Session，Session soft delete 後也不釋放綁定。

## Optional

```json
{
  "model_level": "high",
  "prompt": "...",
  "max_loop": 20,
  "max_tool_call": 40
}
```

### model_level

模型支援時使用，例如：

```text
low
high
max
```

實際合法值由 Model / Provider capability 決定。

### prompt

Session 自訂 System Prompt / Mission Prompt。

如果：

```text
omitted
或
""
```

使用 Server Default Prompt。

### max_loop

限制：

> 一個 Run 內 Agent 最多進行多少次 LLM decision iteration。

Server 有 Default。

### max_tool_call

限制：

> 一個 Run 內最多 Tool Call 次數。

Server 有 Default。

其他所有設定使用 Server Default。

---

# 5. Run

Run 代表：

> Session 被喚醒後，到再次進入等待狀態之前的一次完整工作週期。

例如：

```text
Event Trigger
↓
Run #15
↓
LLM
↓
Tool
↓
LLM
↓
Tool
↓
Decision
↓
Create Event
↓
Run End
```

## 5.1 run_id

每個 Session 自己維護 monotonically increasing count。

例如：

```text
Session abc123

Run #1
Run #2
Run #3
...
Run #15
```

新 Run：

```text
run_id = previous_run_id + 1
```

`run_id` 具有天然時間順序。

---

# 6. Current Run ID vs Historical Run ID

必須明確區分。

## current_run_id

由 Server 管理。

目前 Run 產生的所有資料自動標記：

```text
session_id
run_id
```

例如：

```text
Tool Call
Ledger
Order
Usage
Memory
Event
History
```

LLM 不負責填 current run_id。

## target run_id

Agent 要查以前歷史時，由 LLM 自己指定。

例如：

```text
get_run_history(run_id=7)
```

實際 Server 查詢：

```sql
WHERE session_id = current_session
AND run_id = 7
```

LLM 永遠不能指定 `session_id`。

---

# 7. Run Internal Loop

Run 內可以進行多次 Agent Loop iteration。

```text
Run #15
│
├─ Loop 1
├─ Loop 2
├─ Loop 3
└─ ...
```

受到：

```text
max_loop
max_tool_call
```

限制。

若超限：

```text
Run status = failed
```

並記錄對應 error reason。

不使用全域 `max_run_duration` 作為 v1 核心限制。

單一 Tool 卡住時由：

```text
Tool-level Timeout
```

處理。

---

# 8. Run Status

```text
running
completed
interrupted
failed
```

### interrupted

例如：

- User hard stop
- Server crash
- Runtime interruption

不得把 interrupted Run 當成正常完成。

---

# 9. Session Start

```http
POST /api/v1/sessions/{session_id}/start
```

Session 建立時：

```text
status = stopped
```

不會自動啟動。

第一次 start：

```text
stopped
↓
running
↓
Run #1
↓
trigger = session_start
```

Agent 立即開始工作。

---

# 10. Session Stop

```http
POST /api/v1/sessions/{session_id}/stop
```

採 Hard Stop。

執行：

```text
Cancel current Agent Run
Cancel cancellable LLM request
Cancel cancellable Tool execution
Clear active Events
Save restart context
status = stopped
```

目前 Run：

```text
status = interrupted
```

## Restart Context

Stop 時至少保留：

- 上一次 Run 已完成進度
- 中斷位置
- 被清掉的 active Events
- 必要最近 State / Decision Summary
- 尚未處理的重要 Trigger

下一次 start：

```text
trigger = session_restart
```

Agent 會知道：

- 上次在哪裡中斷
- 哪些 Events 被刪掉
- 上次做了什麼

但：

> 不自動 Restore 舊 Events。

Agent 必須重新判斷是否建立。

---

# 11. Delete Session

```http
DELETE /api/v1/sessions/{session_id}
```

採 Soft Delete。

如果 Session 正在 running：

```text
先 Hard Stop
↓
Soft Delete
```

保留：

- Runs
- Memories
- Usage
- History
- Logs
- Ledger references
- Experiment records

不支援：

```text
restore
```

---

# 12. Single Session Concurrency

同一 Session：

> 同時間最多只能有一個 Run。

禁止 Concurrent Runs。

避免：

- Trading race condition
- Memory race
- Event race
- Duplicate actions

---

# 13. Pending Triggers

Run 執行期間可能有 Event 被觸發。

不使用：

- Redis Queue
- Message Broker
- 專用 Queue System

第一版只維護：

```text
pending_triggers[]
```

它是一個：

> Ordered List。

例如：

```text
Run #15 running

A arrives
B arrives
C arrives

pending_triggers = [A, B, C]
```

Run #15 結束後：

```text
check pending_triggers
```

如果存在：

```text
立即建立 Run #16
```

並把多個 Trigger 一起交給 Agent。

例如：

```json
[
  {
    "type": "price_above"
  },
  {
    "type": "order_filled"
  }
]
```

Agent 自己判斷如何處理。

---

# 14. Trigger Limits

```text
max_active_events = 10
max_triggers_per_run = 10
```

如果 pending triggers：

```text
> 10
```

按照 arrival order 分批。

例如 23 個：

```text
Run #16 → trigger 1~10
Run #17 → trigger 11~20
Run #18 → trigger 21~23
```

Server：

- 不重新排序
- 不做語意摘要
- 不替 Agent 判斷 priority

---

# 15. Event System

Event 是：

> Agent 對未來的一次性等待條件。

所有 Event 都是：

```text
one-shot
```

Agent 建立：

```text
create_event
```

Event Loop 負責：

```text
Register
↓
Monitor
↓
Match
↓
Trigger
↓
Wake Agent
↓
Delete Event
```

---

# 16. Event Tools

Agent 可用：

```text
create_event
delete_event
list_events
```

只有 Agent 可以 Create Event。

Control Panel：

```text
可以查看
可以手動 Delete
不能 Create
```

---

# 17. Event Types v1

```text
timer

price_above
price_below

price_cross_above
price_cross_below

price_change_pct
```

未來可加入：

```text
order_filled
position_closed
SL_hit
TP_hit
volume_spike
funding
market signal
news signal
```

但不屬於 v1 必做。

---

# 18. Active Event Persistence

合法 Event 經 Server validation 後寫入 DB。

```text
events
├─ id
├─ owner_session_id
├─ created_run_id
├─ type
├─ params
├─ created_at
└─ expires_at
```

`events` table：

> 只保存目前 Active Events。

Event：

```text
Triggered
Expired
Deleted
```

之後直接：

```text
DELETE FROM events
```

Event History 可由 Run History / Event Log 保存。

---

# 19. Event Crash Recovery

Server 啟動：

```text
SELECT * FROM events
```

重新註冊所有尚未完成的 Active Events。

DB 是 Event Persistence Source of Truth。

---

# 20. Server Crash Recovery

如果 Server crash 時：

```text
Run #35 = running
```

Server restart 後：

```text
Run #35 → interrupted
```

禁止：

```text
Replay Run #35
Replay Tool Calls
```

尤其避免：

```text
重複下單
```

之後：

```text
建立 Run #36
trigger = server_recovery
```

Agent 收到：

- Previous interrupted Run
- Relevant context
- Current State
- Ledger

Agent 應重新查真實 State / Ledger，再自行決定下一步。

Active Events 按 DB 恢復。

---

# 21. ToolRegistry

Agent 只透過統一 ToolRegistry 看工具。

```text
ToolRegistry
├─ Market Tools
├─ Trading Tools
├─ Event Tools
└─ Memory / History Tools
```

底層 Tool 來源可以不同。

Agent 不需要知道 Tool 實際來源。

---

# 22. Market Tools v1

固定：

```text
get_market_snapshot
get_ohlcv
get_market_info
```

不提供：

```text
get_order_book
```

原因：

Raw Order Book 是高頻、連續時間序列資料。

LLM 零散 Snapshot 判讀價值有限。

未來若加入 Order Flow：

```text
Raw stream
↓
Deterministic Signal Engine
↓
Summary / Event
↓
Agent
```

---

# 23. Trading Tools

第一版不重新設計。

直接根據現有 Trading Backend API 映射。

例如：

```text
Account
Balance
Positions
Orders
Trade
SL / TP
History
Ledger
```

Agent Server ToolRegistry 只負責暴露。

帳戶變動 Tool Call 必須把內部：

```text
session_id
current_run_id
```

一起傳給 Trading Backend / Ledger Layer。

LLM 不填這些值。

---

# 24. Tool Timeout

每個 Tool 必須有 timeout。

例如：

```text
Tool call
↓
timeout exceeded
↓
return Tool Error
```

不得因單一 Tool 永久卡住 Run。

---

# 25. Memory Model

Memory 分成：

```text
Run Summary
Persistent Memory
State
Run History
```

它們用途不同。

## State

表示：

> 現在真實存在的狀態。

例如：

- Positions
- Account balance
- Orders
- Ledger
- Active Events

State Source of Truth 必須來自：

```text
Trading Backend
Tool Result
Deterministic System
```

不能以 LLM Summary 作為真實 State。

---

# 26. Run Summary

每一個 Run 都必須有非常短的：

```text
Run Summary
```

它屬於：

> Run Metadata。

用途：

- Control Panel 快速瀏覽
- Agent 快速回顧
- History Index
- Context reduction

不是 State Source of Truth。

---

# 27. Run Metadata

每個 Run 至少記錄：

```text
session_id
run_id

status

start_time
end_time
duration

trigger / triggers

summary

tool_call_count
model_call_count

order_count
position_count / position change

token usage
```

Server 可確定的資訊全部由 Server 自動產生。

例如：

```text
start/end
counts
usage
status
trigger
```

LLM 不填。

只有：

```text
summary
candidate memories
```

屬於 Agent 語意輸出。

---

# 28. Memory Finalization Flow v1

每個正常完成（`finish_reason:"stop"`）的 Run 使用以下 finalization 流程：

```text
Agent Loop 完成
↓
Agent 強制產生很短 Run Summary
↓
Server 使用純程式碼過濾 Tool Use / Tool Return
↓
Agent 提出 Candidate Memories
↓
Server 補齊 Metadata
↓
Save
↓
Run End
```

`failed`、`interrupted`、hard stop、dependency failure、budget exhaustion 與非正常
finish reason 不執行 Summary 或 Memory finalization；其 `summary` 必須為 `null`，並保存
明確的 terminal reason。取消後不得再發出 summary/model/tool request。Tickets 14–18
只實作正常完成 Run 的短 Summary；Candidate Memory、filter 與 Save 留待後續票實作。

第一版：

> 不使用額外 Memory Compactor Model。

未來 Memory 大量累積後才考慮：

```text
merge
deduplicate
compress
archive
```

---

# 29. Tool History Filtering

不需要永久保存所有巨大 Tool Return。

Server 以程式規則過濾。

例如：

```text
get_ohlcv → 大量 Candle
```

通常不保存完整結果。

但是以下應保留必要結果：

```text
Order success
Position change
Balance relevant to decision
Trade execution
SL / TP change
Critical failure
```

核心原則：

> 保存足以理解「為什麼做這個決定」的資訊，而不是把所有外部資料永久複製一份。

---

# 30. Persistent Memory

Candidate Memory 由 Agent 提出。

不是每個 Run 都一定有 Memory。

Memory Metadata v1：

```text
id
session_id
run_id

type
summary
content

importance

run_start_at
run_end_at

is_expired
```

其中：

### summary

非常重要。

作為 LLM 的快速 Memory Index。

LLM 先看 Summary 判斷：

> 值不值得讀全文？

### importance

簡單等級即可，例如：

```text
1
2
3
```

### is_expired

Soft Delete / Obsolete Flag。

Agent 可以標記舊 Memory 不再有效。

User 也可以刪除 Memory。

實際行為：

```text
is_expired = true
```

不立即物理刪除。

---

# 31. Memory Progressive Read

Agent 不應每次把所有 Memory 全讀入 Context。

流程：

```text
Memory Metadata
↓
Memory Content
↓
Run History
```

例如：

```text
memory_search("BTC breakout")
↓
Memory #12
summary = "Weak-volume breakout failed."
run_id = 18
↓
memory_read(12)
↓
需要更多細節
↓
get_run_history(run_id=18)
```

---

# 32. Memory Tools

Agent 可使用：

```text
memory_list
memory_search
memory_read
get_run_history
```

不提供：

```text
memory_write
```

Persistent Memory 由 Run Finalization 的 Candidate Memory 流程建立。

User：

```text
可 Delete Memory
不可 Create Memory
```

---

# 33. Run History Storage

完整 Run History 不全部塞 DB。

採：

```text
DB = Index / Metadata
Filesystem = History JSON
```

約每：

```text
20 Runs
```

存一個 JSON File。

例如：

```text
s_abc123_1-20.json
s_abc123_21-40.json
s_abc123_41-60.json
```

Run #27：

```text
→ s_abc123_21-40.json
```

建議固定 shard 計算：

```text
start = floor((run_id - 1) / 20) * 20 + 1
end   = start + 19
```

---

# 34. Run History File Index

DB 保存：

```text
run_history_files
├─ session_id
├─ start_run_id
├─ end_run_id
├─ file_path
├─ created_at
└─ updated_at
```

LLM / Frontend 都不直接指定 filesystem path。

Server 負責 Resolve。

---

# 35. Run History JSON

每個 File 包含約 20 個 Run：

```json
{
  "session_id": "abc123",
  "start_run_id": 21,
  "end_run_id": 40,
  "runs": []
}
```

單個 Run 可包含：

```text
run_id
status
start/end
triggers
summary

iterations

model calls
tool calls
filtered tool results

events created/deleted
memory references

usage
error
```

不要求保存模型 Hidden Chain-of-Thought。

只保存：

- Model 可見輸出
- Decision Summary
- Tool trace
- Structured result
- Run Summary

---

# 36. Database Core Tables

v1 建議：

```text
sessions
runs
memories
events
pending_triggers
run_history_files
```

Trading Ledger 仍以 Trading Backend 為 Source of Truth。

Agent Server 透過：

```text
session_id
run_id
```

做關聯。

---

# 37. sessions Table

概念欄位：

```text
id
name

model_name
model_level
prompt

max_loop
max_tool_call

status
current_run_id

restart_context

created_at
started_at
stopped_at
deleted_at
```

核心設定建立後 immutable。

---

# 38. runs Table

```text
session_id
run_id

status

summary

start_time
end_time
duration

trigger_metadata

tool_call_count
model_call_count

order_count
position_count

input_tokens
output_tokens
cached_tokens
total_tokens

error

history_file_index
```

主鍵可以：

```text
(session_id, run_id)
```

---

# 39. memories Table

```text
id
session_id
run_id

type
summary
content

importance

run_start_at
run_end_at

is_expired

created_at
updated_at
```

---

# 40. pending_triggers

這不是 Message Queue。

只是持久化 Ordered List。

```text
id
session_id
sequence
payload
created_at
```

用途：

- 保存 Run 執行期間到達的 Trigger
- 保持順序
- Crash 後不遺失尚未處理 Trigger

取出後刪除。

每 Run 最多 consume 10 筆。

---

# 41. HTTP API Base

```text
/api/v1
```

全部以 Session 為主要 namespace。

---

# 42. Session Routes

```http
POST   /api/v1/sessions
GET    /api/v1/sessions?page={page}

GET    /api/v1/sessions/{session_id}
DELETE /api/v1/sessions/{session_id}

POST   /api/v1/sessions/{session_id}/start
POST   /api/v1/sessions/{session_id}/stop
```

不提供：

```http
PATCH /sessions/{id}
```

Session 設定 immutable。

要改實驗條件：

> 建立新的 Session。

---

# 43. Session List Pagination

```http
GET /api/v1/sessions?page=1
```

固定：

```text
page_size = 100
```

不提供：

```text
limit
complex filter
server-side sort
```

前端：

```text
cache
sort
render
```

優先快速完成 v1。

預設不顯示 deleted Session。

---

# 44. Session List Monitoring

`GET /sessions` 應提供豐富監控資訊。

例如：

```text
id
name
status

model_name
model_level

current_run_id

created_at
started_at
last_run_at

pending_event_count

total_runs
total_tool_calls
total_tokens

last_run_summary

order_count
position_count

PnL
account summary

runtime health
```

監控資訊：

> 越完整越好。

---

# 45. Session Detail

```http
GET /api/v1/sessions/{session_id}
```

採 Aggregate Detail。

一次提供控制面板首頁需要的：

```text
Session Config
Status
Current Run
Last Run Summary
Pending Events
Memory Summary
Usage Summary
PnL
Positions
Account Summary
Runtime Health
```

大量資料另外 Lazy Load。

---

# 46. Run APIs

```http
GET /api/v1/sessions/{session_id}/runs?page={page}

GET /api/v1/sessions/{session_id}/runs/current

GET /api/v1/sessions/{session_id}/runs/{run_id}

GET /api/v1/sessions/{session_id}/runs/{run_id}/history
```

`runs/current` 用於 Control Panel Polling。

例如：

```text
Run #152
status
loop 4 / 20
tool calls 7 / 40
elapsed
current activity
tokens
```

---

# 47. History File API

```http
GET /api/v1/sessions/{session_id}/runs/{run_id}/history
```

Server：

```text
run_id
↓
DB History Index
↓
Resolve corresponding JSON shard
↓
Return file
```

例如 Run #27：

```text
s_abc123_21-40.json
```

Frontend 下載後自行 Cache / Render。

---

# 48. Memory APIs

```http
GET /api/v1/sessions/{session_id}/memories?page={page}

GET /api/v1/sessions/{session_id}/memories/{memory_id}

DELETE /api/v1/sessions/{session_id}/memories/{memory_id}
```

DELETE：

```text
is_expired = true
```

不提供 User：

```http
POST Memory
PATCH Memory
```

---

# 49. Event APIs

```http
GET /api/v1/sessions/{session_id}/events

DELETE /api/v1/sessions/{session_id}/events/{event_id}
```

不提供：

```http
POST /events
```

只能 Agent 建 Event。

---

# 50. Monitoring APIs

```http
GET /api/v1/sessions/{session_id}/usage

GET /api/v1/sessions/{session_id}/ledger?page={page}

GET /api/v1/sessions/{session_id}/positions

GET /api/v1/sessions/{session_id}/tools/stats
```

---

# 51. Usage Monitoring

至少提供：

```text
input_tokens
output_tokens
cached_tokens
total_tokens

model_calls

usage by model
usage by run
session total
```

---

# 52. Tool Monitoring

```text
tool_name

call_count
success_count
error_count

avg_latency
max_latency
last_latency
```

Tool Latency 是第一版重要 Monitoring 指標。

---

# 53. Trading Monitoring

控制面板需要：

```text
PnL

Positions

Session Ledger

Orders / Account Changes

run_id
```

Ledger 中每筆重要帳戶變動都應能追溯：

```text
session_id
run_id
```

例如：

```text
BTC LONG +50U
created by Run #15
```

User / Agent 可以進一步查看 Run #15 History。

---

# 54. Error Response

統一保持簡單。

```json
{
  "status_code": 400,
  "error": "INVALID_REQUEST",
  "msg": "invalid model_name"
}
```

固定：

```text
status_code
error
msg
```

不建立過度複雜 Error Schema。

常見 HTTP Status：

```text
400 Invalid Request
404 Not Found
409 Invalid Session State
500 Internal Error
```

---

# 55. Model / Runtime

Agent Server 使用統一 Runtime Interface。

Model 由：

```text
model_name
```

解析到實際 Provider / Runtime。

各 Runtime 使用自己的 Native Structured Tool Channel。

例如：

```text
Codex Runtime
→ native tool channel

Claude Runtime
→ structured tool-use

Native LLM Provider
→ structured tool calling
```

Agent Loop 不直接處理各 Provider 私有 Tool Schema。

全部 Normalize 成 Agent Server 內部格式。

---

# 56. Autonomous Agent Context

每次 Run 的主要 Context：

```text
System / Session Prompt

Current Session State

Trigger(s)

Recent Run Summary

Relevant Memory Metadata / Content

Restart / Recovery Context（若有）

Available Tools
```

不預設塞入：

```text
完整所有歷史
所有 Raw Tool Result
所有舊 Messages
所有 OHLCV
```

需要時 Agent 自己查。

---

# 57. Agent 自主原則

User 決定：

```text
Prompt / Mission
Model
Model Level
Limits
```

Agent 自己決定：

```text
查什麼
做什麼
先做什麼
後做什麼

是否交易

讀哪段 Memory
讀哪個 Run

建立什麼 Event

什麼條件需要再次醒來
```

因此不使用固定：

```text
Step 1
Step 2
Step 3
```

作為 Agent 核心。

避免把 Autonomous Agent 做成 Workflow Engine。

---

# 58. v1 明確不做

第一版不做：

```text
Multi-Agent
DAG Workflow
MCP-based core architecture

Vector DB requirement
Complex Memory Compactor

Redis Queue
Kafka
Celery-style task system

Concurrent Runs in same Session

User-created Events

User-created Memories

Session PATCH

Session Restore

Raw Order Book Tool

复杂 Server-side Session filters/sorts
```

核心優先：

```text
簡單
精簡
快速
邊界清楚
可監控
可恢復
可追溯
```

---

# 59. v1 核心資料關係

```text
Session abc123
│
├─ Run #14
│  ├─ Summary
│  ├─ Tool Trace
│  ├─ Usage
│  └─ Memories
│
├─ Run #15
│  ├─ Trigger
│  ├─ Summary
│  ├─ Order
│  ├─ Ledger Entry
│  ├─ Event Creation
│  └─ Memories
│
└─ Run #16
```

任何重要資料都盡量可以回到：

```text
session_id
+
run_id
```

形成完整因果追蹤。

---

# 60. 核心一句話

整個 v1 Agent Server 可以濃縮成：

```text
Session 管 Agent
Run 管一次工作
Agent Loop 管現在做什麼
Event Loop 管什麼時候再醒
Memory 管值得記住什麼
History 管當時到底發生什麼
Monitoring 管整個 Agent 到底怎麼跑
```

---

# 61. Implemented Tickets 14–18 Runtime Contract

## 61.1 Startup and authorization

The Agent Runtime is mounted into the existing Server; `/health` remains public.
An unrecoverable persistence fault latches Runtime unavailability: `/health` and
new mutation/admission requests return HTTP `503 PERSISTENCE_UNAVAILABLE`, active
work is canceled, and audit reads remain available when SQLite can still serve them.
`context.Canceled`, `context.DeadlineExceeded`, and `sql.ErrTxDone` caused by the
same request context before commit are local request failures: the transaction is
rolled back, HTTP `408 REQUEST_CANCELED` is returned when possible, and Runtime
health plus unrelated work remain unchanged. A genuine SQLite error is still a
persistence fault even if the request context is also canceled.
Every `/api/v1/*` endpoint requires the exact, case-sensitive header:

```http
Authorization: Bearer <AGENT_SERVER_ADMIN_TOKEN>
```

Missing, malformed, or wrong credentials return HTTP `401`:

```json
{"status_code":401,"error":"UNAUTHORIZED","msg":"valid Agent Server admin credentials are required"}
```

Runtime configuration is read at process startup:

| Environment variable | Required/default | Meaning |
|---|---|---|
| `AGENT_SERVER_DB_PATH` | `agent-server.db` | SQLite Session/Run database |
| `AGENT_SERVER_ADMIN_TOKEN` | required | Agent control-plane credential |
| `AGENT_SERVER_BACKEND_URL` | required | Backend HTTP base URL |
| `AGENT_SERVER_BACKEND_USER_TOKEN` | required | Backend USER credential used only server-side for Account lookup |
| `AGENT_SERVER_GATEWAY_URL` | required | Gateway HTTP base URL |
| `AGENT_SERVER_GATEWAY_RUNTIME_TOKEN` | required | Gateway Runtime credential |
| `AGENT_SERVER_DEFAULT_PROMPT` | `You are an autonomous trading agent.` | Prompt used for omitted or empty Session prompt |
| `AGENT_SERVER_DEFAULT_MAX_LOOP` | `20` | Positive default LLM-decision limit |
| `AGENT_SERVER_DEFAULT_MAX_TOOL_CALL` | `40` | Positive default Tool-attempt limit |
| `AGENT_SERVER_REQUEST_TIMEOUT` | `30s` | Positive timeout for each Gateway/catalog and control-plane dependency request |
| `AGENT_SERVER_TOOL_TIMEOUT` | `10s` | Positive timeout for each Tool attempt |
| `AGENT_SERVER_STOP_TIMEOUT` | `5s` | Positive wait for canceled Run work to leave |
| `AGENT_SERVER_EVENT_SCAN_INTERVAL` | `1s` | Positive interval shared by the local and price Event resident tasks (§63.4) |
| `AGENT_SERVER_HISTORY_DIR` | `agent-history` | Directory containing atomically replaced Run History JSON shards (§66.2) |

URLs must be HTTP(S), include a host, and contain no credentials, query, or
fragment. Invalid/missing required values fail startup before listening. Agent,
Backend USER, Backend Account, and Gateway credentials are never returned in an
Agent response or placed in model messages, Run progress, or Session storage.

## 61.2 Account binding and token reset decision

Creation validates `account_id` with the actual Backend contract:

```http
GET /v1/accounts/{account_id}
Authorization: Bearer <AGENT_SERVER_BACKEND_USER_TOKEN>
```

The response must identify the requested Account, have `status:"active"`, and
contain its Account Token. The token is discarded after validation. Before every
Backend Tool execution, the Runtime performs the same USER-authorized lookup and
uses the current returned Account Token for that one Tool request. Therefore a
Backend token reset takes effect on the next Tool attempt without persisting a
secret or automatically retrying an already attempted request. Disabled/deleted
Accounts fail lookup; errors are returned to the model as Tool Errors when they
occur during a Run.

One Backend Account may be bound to exactly one Session for the lifetime of the
audit record, including after soft delete. This avoids concurrent trading through
two Sessions and avoids falsely treating Account-wide PnL as isolated Session PnL.
Creating a second binding returns `409 ACCOUNT_ALREADY_BOUND`. There is no Account
locking framework and the Runtime never creates a Backend Account.

## 61.3 Create, list, and detail Sessions

```http
POST /api/v1/sessions
Content-Type: application/json
```

```json
{
  "name": "BTC Agent",
  "account_id": "acc_xxx",
  "model_name": "fixture-reasoning",
  "model_level": "high",
  "prompt": "Trade carefully.",
  "max_loop": 20,
  "max_tool_call": 40
}
```

`name`, `account_id`, and `model_name` are required nonblank strings. Unknown
fields, trailing JSON, non-JSON content types, and bodies over 1 MiB are invalid.
`prompt` omitted or `""` uses the Server default; null/non-string is invalid.
Limits omitted use Server defaults and otherwise must be positive integers; null
is invalid. `model_level` omitted means no level; null/empty/unsupported values are invalid. Creation calls Gateway
`GET /v1/models` with its Runtime token. `model_name` must be present, enabled,
available, and list the requested level. No Generate call occurs during creation.

Success is HTTP `201` with a stopped immutable Session:

```json
{
  "id": "s_0123456789abcdef0123456789abcdef",
  "name": "BTC Agent",
  "account_id": "acc_xxx",
  "model_name": "fixture-reasoning",
  "model_level": "high",
  "prompt": "Trade carefully.",
  "max_loop": 20,
  "max_tool_call": 40,
  "status": "stopped",
  "current_run_id": 0,
  "restart_context": null,
  "created_at": "2026-09-13T00:00:00Z",
  "started_at": null,
  "stopped_at": null,
  "deleted_at": null
}
```

Account Tokens and management credentials are never Session fields. `PATCH` is
not supported and returns `405 METHOD_NOT_ALLOWED`; changing configuration requires
a new Session.

```http
GET /api/v1/sessions?page=1
GET /api/v1/sessions/{session_id}
```

List defaults to page 1, accepts only a positive integer `page`, uses fixed
`page_size:100`, and excludes deleted Sessions. Success is
`{"sessions":[...],"page":1,"page_size":100}`. Detail returns the Session directly,
including deleted Sessions for audit. Unknown Session is `404 SESSION_NOT_FOUND`.
Records and bindings persist across process restart.

Creation dependency errors are:

| Condition | HTTP | Code |
|---|---:|---|
| Missing/invalid fields, limits, level | 400 | `INVALID_REQUEST` |
| Unknown Gateway model | 400 | `MODEL_NOT_FOUND` |
| Disabled/unavailable Gateway model | 409 | `MODEL_UNAVAILABLE` |
| Unknown Backend Account | 404 | `ACCOUNT_NOT_FOUND` |
| Disabled Backend Account | 409 | `ACCOUNT_DISABLED` |
| Existing Account binding | 409 | `ACCOUNT_ALREADY_BOUND` |
| Gateway credential rejected | 502 | `GATEWAY_AUTH_FAILED` |
| Invalid Gateway catalog | 502 | `GATEWAY_RESPONSE_INVALID` |
| Backend USER credential rejected | 502 | `BACKEND_AUTH_FAILED` |
| Dependency unavailable | 502 | `GATEWAY_UNAVAILABLE` / `BACKEND_UNAVAILABLE` |
| Request canceled before mutation commit | 408 | `REQUEST_CANCELED` |
| Persistence unavailable | 503 | `PERSISTENCE_UNAVAILABLE` |

All Agent errors use §54's `status_code`, `error`, `msg` envelope.
Create, start, stop, and delete perform an authoritative `closed`/persistence-fault
check immediately after acquiring the Runtime lock. This prevents a request that
passed the early HTTP check before a concurrent fault or shutdown from writing.
The lock remains held through commit and, for start, active-Run registration;
dependency HTTP requests are never made while holding it.

## 61.4 Start and Run queries

```http
POST /api/v1/sessions/{session_id}/start
```

A successful start atomically changes a stopped Session to running, increments its
persistent Session-local `current_run_id`, and inserts the running Run. First start
uses `session_start`; later starts use `session_restart`. Concurrent starts of one
Session yield one HTTP `202`; the others return `409 SESSION_ALREADY_RUNNING`.
If prior cancellation is still pending, restart returns `409 SESSION_RUN_ACTIVE`
until that Run goroutine exits. Deleted Sessions return `409 SESSION_DELETED`. The
`202` body is the Run directly; the asynchronous Run is independent of the start
request context. After commit, response serialization does not query SQLite with
the caller's context, so caller cancellation cannot fault the Runtime or cancel the
admitted Run.

Each decision first durably reserves its decision budget, resolves the current
configured `model_name` from Gateway models without holding the Runtime lock, then
calls normalized Gateway `POST /v1/generate` with the mapped Provider/model,
optional `options.model_level`, Session prompt, factual trigger context, and Tool
schemas. Provider-private formats are not handled here. Normal final content is
stored as output and its trimmed first 500 Unicode characters become the short
summary without an extra model call. Gateway/dependency/invalid-final-response
failures set the Run to failed with a reason and `summary:null`; no repair or
summary call is made. Only `finish_reason:"stop"` is normal completion;
`length`/`content_filter` fail without a summary. A failed Run ID is never reused.

```http
GET /api/v1/sessions/{session_id}/runs?page=1
GET /api/v1/sessions/{session_id}/runs/current
GET /api/v1/sessions/{session_id}/runs/{run_id}
```

Run list uses the same fixed page size and returns
`{"runs":[...],"page":1,"page_size":100}`. Detail returns one Run. `runs/current`
returns only a currently running Run; a Session with no active Run returns
`404 CURRENT_RUN_NOT_FOUND`. Unknown positive or malformed Run IDs return
`404 RUN_NOT_FOUND`. Deleted Session Runs remain readable for audit.

```json
{
  "session_id": "s_xxx",
  "run_id": 1,
  "status": "completed",
  "trigger": "session_start",
  "summary": "Checked BTC and will wait.",
  "output": "Checked BTC and will wait.",
  "error": null,
  "progress": [{"type":"model_call","iteration":1}],
  "model_call_count": 1,
  "tool_call_count": 0,
  "start_time": "2026-09-13T00:00:00Z",
  "end_time": "2026-09-13T00:00:01Z"
}
```

## 61.5 Tool loop, ordering, errors, and budgets

Tickets 14–18 register only `get_market_snapshot`:

```json
{
  "name": "get_market_snapshot",
  "description": "Get the latest market price snapshot.",
  "input_schema": {
    "type": "object",
    "properties": {
      "market": {"type":"string"},
      "symbol": {"type":"string"}
    },
    "required": ["symbol"],
    "additionalProperties": false
  }
}
```

`market` omitted/empty-string defaults to `crypto`; explicit null and non-string
values are invalid. `symbol` is required, nonblank, and string-typed; null and
non-string values are invalid. Invalid arguments consume one accepted Tool attempt,
return a correlated `INVALID_TOOL_ARGUMENTS` Tool Error, and perform no Backend I/O.
Execution refreshes the Account Token as §61.2 describes, then maps directly to:

```http
GET /v1/market/price?market={market}&symbol={symbol}
Authorization: Bearer <current-account-token>
```

Each Run's first user context includes `max_loop`, `max_tool_call`, and a compact
Account snapshot (`id`, `status`, `initial_balance`, and `balance`) fetched through
the existing Backend Account lookup; no Account Token is exposed. Every autonomous
LLM decision consumes one `max_loop` unit before Gateway I/O. Transient Generate
retries and the terminal finalization allowance are counted in `model_call_count`
but do not create additional autonomous decisions. Every Tool
call accepted in Gateway response order consumes one `max_tool_call` unit before
name/schema validation or execution. This includes unknown tools, invalid
arguments, Backend errors, and timeouts. Tool calls are strictly sequential. If a
response has more calls than remaining units, only the prefix fitting the budget
is attempted; every remaining call receives a correlated, structured
`MAX_TOOL_CALL_REACHED` result without dispatch. Reaching either limit forces one
tool-disabled terminal finalization flow instead of failing the Run for the limit.
That flow has at most four actual Generate attempts (the initial attempt plus three
retries). Counters never come from the model and count every actual Generate and
accepted Tool attempt, including errors.

Generate transport failures, timeouts, rate limits, provider-unavailable responses,
and malformed successful Gateway responses are retried in place at most three times.
Cancellation, authentication failures, invalid requests, invalid Provider
configuration, and missing Harness configuration are not retried. Each attempt has
its own persisted model trace and contributes to `model_call_count`; known usage is
attached only to the attempt that returned it. Tool, Backend mutation, completion
transaction, and trade requests are never automatically retried. Tool
errors are returned as the content of the matching `tool_call_id` and retained in
Run progress:

```json
{
  "ok": false,
  "error": {
    "code": "TOOL_TIMEOUT",
    "message": "context deadline exceeded",
    "outcome": "unknown"
  }
}
```

`outcome` is `not_executed` for validation/known pre-execution failures and
`unknown` when cancellation, transport failure, or an invalid response cannot
prove the external operation did not occur. Backend error code/message are
preserved where provided. The Server does not decide whether to retry; the next
decision receives the Tool Error. An in-flight trace is first stored with unknown
outcome so a hard stop cannot lose the accepted attempt.

Final-envelope repair is provider-independent at the Agent Runtime boundary. Claude
and Codex Harness output can only be repaired here after the Gateway has returned a
Generate response (or a classified transient invalid response); process launch,
authentication, and Provider configuration failures remain Gateway dependencies and
are deliberately not repairable by the Runtime.

## 61.6 Hard stop, restart, and soft delete

```http
POST   /api/v1/sessions/{session_id}/stop
DELETE /api/v1/sessions/{session_id}
```

Stop atomically sets the Session to stopped and any running Run to interrupted,
with `HARD_STOP: interrupted by user`, `summary:null`, end time, counters, and
already persisted progress. It then cancels the Run context, which is shared by
Gateway and Tool requests. No new model/Tool/summary dispatch occurs. A late
response cannot replace the terminal interrupted state. Stopping an already
stopped Session is idempotent; stopping a deleted Session returns
`409 SESSION_DELETED`.
Request cancellation before the stop/delete transaction commits rolls back only
that mutation and does not cancel the target or unrelated active Runs. After commit,
the response is built from the committed in-memory state rather than performing a
caller-context SQLite read.

Success after cooperative cancellation is HTTP `200`:

```json
{"session": {/* Session */}, "cancellation_pending": false}
```

If work has not left by `AGENT_SERVER_STOP_TIMEOUT`, state is already safely
interrupted and HTTP `202` returns the same envelope with
`"cancellation_pending":true`. The Runtime continues waiting in the background.

`restart_context` is factual JSON copied from the previous Run: previous Run ID,
terminal status/reason, existing summary/output when any, progress counts, and only
the five most recent trace entries. Recent entries retain type/iteration, Tool name,
finish reason, and status only; arguments, Tool results, model content, and usage are
never copied. `progress_omitted` reports how many older entries were removed. Full
trace evidence remains available through the Run and Run History endpoints.
A later start creates a new monotonically increasing Run with
`trigger:"session_restart"` and passes this context directly. It does not replay
old Tool calls, restore Events, or create an interrupted/failed summary. Partial
or unknown outcomes remain explicitly partial/unknown.

Delete performs the same hard stop first when work is running, then sets
`status:"deleted"` and `deleted_at`. It returns the same Session/cancellation
envelope. Session and Run records, restart context, and the permanent Account
binding remain stored and readable through detail/Run APIs. Deleted Sessions are
excluded from list, cannot start or stop, and have no restore endpoint. `PATCH`
remains rejected. Delete/restart races serialize on Session state, so deletion is
terminal and late work cannot restore running/completed state.

## 61.7 Reproducible verification

From `agent_server/`, fixtures require only Go. They use local HTTP servers,
`t.TempDir()` SQLite files, explicit channels/context cancellation, and no external
credentials or fixed ports. The Ticket 16 integration test additionally builds and
starts the real local Gateway binary against a compatible-provider fixture:

```powershell
go test ./test -run '^TestTicket14' -count=20
go test ./test -run '^TestTicket15' -count=20
go test ./test -run '^TestTicket16' -count=20
go test ./test -run '^TestTicket17' -count=20
go test ./test -run '^TestTicket18' -count=20
go test ./... -run '^$' -count=1
go vet ./...
```

`t.TempDir()` removes Agent and process-Gateway test databases;
`httptest.Server` cleanup closes Backend/provider fixtures. No production Backend
or Gateway database is accessed by Agent tests.

---

# 62. Implemented Tickets 19–20 Market Read Contract

Tickets 19–20 add two read-only tools to the same ordered Tool loop described in
§61.5. They do not add a second registry or client. Every accepted attempt uses
the existing Tool budget and trace, refreshes the Account Token immediately before
Backend I/O, shares the Run cancellation context and `AGENT_SERVER_TOOL_TIMEOUT`,
and is never retried. Invalid arguments are `INVALID_TOOL_ARGUMENTS` with
`outcome:"not_executed"`; transport cancellation/timeout is `TOOL_TIMEOUT` with
`outcome:"unknown"`. Backend `error` and `message` are preserved in the correlated
Tool Error. Successful Backend JSON is returned to the model as:

```json
{"ok":true,"result":{/* exact Backend response fields */}}
```

The Tool timeout covers response-body reading as well as connection setup and
headers. If the timeout or Run cancellation occurs after Backend sent headers but
before either a success or error body is fully read, the body-read failure is
classified `TOOL_TIMEOUT` with `outcome:"unknown"`; an HTTP status alone does not prove the
operation outcome. A fully read Backend error keeps its supplied `error` and
`message`. A fully delivered malformed HTTP 200 body is
`BACKEND_RESPONSE_INVALID`, not a timeout. Neither case is retried automatically.
A concurrent hard stop retains the already persisted unknown in-flight trace,
dispatches no next model request, and cannot be overwritten by a late body result.

The Backend remains the only market-source boundary. Agent Server does not connect
to Binance, cache Candles or market rules, or add a fallback source. Backend market
read endpoints accept either its USER or an active Account credential; Agent tools
always use the Session-bound Account credential.

## 62.1 `get_ohlcv`

Gateway tool declaration:

```json
{
  "name": "get_ohlcv",
  "description": "Get completed OHLCV candles in an ascending UTC time range.",
  "input_schema": {
    "type": "object",
    "properties": {
      "market": {"type":"string","default":"crypto"},
      "symbol": {"type":"string"},
      "interval": {"type":"string","enum":["1m","5m","15m","1h","4h","1d"]},
      "start_time": {"type":"string","format":"date-time"},
      "end_time": {"type":"string","format":"date-time"},
      "limit": {"type":"integer","minimum":1,"maximum":500,"default":100}
    },
    "required": ["symbol","interval","start_time","end_time"],
    "additionalProperties": false
  }
}
```

`market` omitted or empty defaults to `crypto`; explicit null/non-string is invalid.
`symbol` and `interval` are trimmed nonblank strings. `start_time` and `end_time`
must be RFC3339 timestamps with `start_time < end_time`; after validation Agent
forwards their exact strings, including fractional precision and numeric UTC
offset, so Backend alone owns range conversion. `limit` omitted is 100; null,
non-integer, values below 1 and values above 500 are invalid. The direct mapping is:

```http
GET /v1/market/ohlcv?market=crypto&symbol=BTCUSDT&interval=1m&start_time=2026-09-13T10%3A00%3A00Z&end_time=2026-09-13T11%3A00%3A00Z&limit=100
Authorization: Bearer <current-account-token>
```

Backend v1 supports these intervals only for the registered `crypto` Binance Spot
source. It calls Binance `/api/v3/klines` at most once with the requested range and cap.
There is no pagination or fallback. Candle membership uses opening time in the
half-open range `[start_time,end_time)`. Results are ascending, timestamps and
range metadata are UTC, and only Candles whose `close_time` is strictly before the
Backend observation time are returned. Therefore an in-progress current Candle is
excluded. Because Binance query bounds are integer milliseconds, Backend converts the exact
range to `startTime=ceil(start_time in Unix milliseconds)` and
`endTime=ceil(end_time in Unix milliseconds)-1`. This preserves exact half-open
membership for fractional-millisecond inputs instead of widening or narrowing the
requested range. If that conversion contains no integer millisecond, Backend
returns an empty Candle array without source I/O. Every source fact required to
construct a returned Candle must be present and valid; a missing, null, malformed,
non-finite, or internally inconsistent supplied fact is HTTP 502
`market_response_invalid`.

At most `limit` and never more than 500 Candles are returned. A valid range with no
source rows is HTTP 200 with an empty non-null array:

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

Backend failures use its existing `{"error":"...","message":"..."}` envelope:

| Condition | HTTP | Backend code |
|---|---:|---|
| Missing/malformed field, invalid range or limit | 400 | `invalid_request` |
| Unsupported interval | 400 | `unsupported_interval` |
| Unknown market | 400 | `invalid_request` |
| Registered source does not implement OHLCV | 501 | `not_implemented` |
| Unknown symbol | 404 | `symbol_not_found` |
| Source timeout | 504 | `market_timeout` |
| Invalid source payload | 502 | `market_response_invalid` |
| Other source failure | 502 | `market_unavailable` |

## 62.2 `get_market_info`

Gateway tool declaration:

```json
{
  "name": "get_market_info",
  "description": "Get source market rules and their relationship to virtual Backend execution.",
  "input_schema": {
    "type": "object",
    "properties": {
      "market": {"type":"string","default":"crypto"},
      "symbol": {"type":"string"}
    },
    "required": ["symbol"],
    "additionalProperties": false
  }
}
```

Argument null/type/default semantics match `get_market_snapshot`. The direct map is:

```http
GET /v1/market/info?market=crypto&symbol=BTCUSDT
Authorization: Bearer <current-account-token>
```

Backend queries Binance Spot `/api/v3/exchangeInfo` for the requested symbol and
returns only source facts. `updated_at` is when Backend observed the source response,
not an exchange promise that rules remain unchanged. Decimal rule values remain
strings so tick/step precision is not lost. `price_filter`, `quantity_filter`, and
`min_notional` are JSON null when the source did not provide that rule; no value is
inferred. `status` is returned as reported, including non-trading values such as
`BREAK`. An empty source symbol set is `symbol_not_found`.

The selected source symbol must explicitly contain the required facts `symbol`,
`status`, `baseAsset`, `quoteAsset`, `orderTypes`, `isSpotTradingAllowed`, and
`isMarginTradingAllowed`; required booleans must be present even when false.
Missing, null, wrong-typed, or otherwise invalid required facts are HTTP 502
`market_response_invalid`. Optional `PRICE_FILTER`, `LOT_SIZE`, and
`MIN_NOTIONAL`/`NOTIONAL` rules become JSON null only when absent. If an optional
rule is supplied, all of its required decimal facts must be present and valid;
Backend does not silently turn a malformed supplied rule into null and instead
returns HTTP 502 `market_response_invalid`.

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

`source_rules` describe the external source only. `backend_execution` explicitly
states that this service executes virtual trades and does not claim to enforce the
same source rules; Backend order validation remains authoritative. Market Info uses
the same unknown-market, unsupported-source, unknown-symbol, timeout, invalid-source
payload and source-unavailable HTTP/code mapping listed in §62.1.

## 62.3 Reproducible verification

From each module directory, tests use only local `httptest.Server` source,
Backend and Gateway fixtures plus temporary SQLite/log paths. They need no external
credentials, fixed ports or persistent files:

```powershell
# backend/
go test ./test -run '^TestTicket19' -count=1
go test ./test -run '^TestTicket20' -count=1
go test ./... -run '^$' -count=1
go vet ./...

# agent_server/
go test ./test -run '^TestTicket19' -count=1
go test ./test -run '^TestTicket20' -count=1
go test ./... -run '^$' -count=1
go vet ./...
```

`t.TempDir()` removes databases/logs and `httptest.Server` cleanup closes every
fixture. No production Backend, Gateway or Binance endpoint is contacted.

# 63. Implemented Tickets 27–31 Event Contract

Events are the Agent's one-shot wake-up conditions (§15). Tickets 27–31 implement them on
the existing Runtime: the same `r.mu`/SQLite transaction discipline, Run admission, hard
stop, persistence-fault latch and cancellation rules as §61 apply unchanged. There is no
second scheduler, queue, broker, rule engine or per-Event goroutine. The existing EventLoop
runs two resident tasks (`count=-1`): `agent-events-local` handles timers and expiry, while
`agent-events-price` performs remote sampling and price matching. Session stop never stops
either task and only Server shutdown cancels them.

## 63.1 Persistence

Three tables are created next to `sessions`/`runs`; SQLite remains the Event source of truth.

| Table | Purpose |
|---|---|
| `events` | Active Events only. Fired, expired and deleted Events are `DELETE`d in the same transaction that records their outcome. Columns: `id`, `owner_session_id`, `created_run_id`, `type`, `params_json`, `market`, `symbol`, `due_at` (timer), `state_json` (cross baseline), `created_at`, `expires_at`. |
| `pending_triggers` | Ordered list (§13, §40). `id INTEGER PRIMARY KEY AUTOINCREMENT` is the Server-generated arrival sequence and is never reused; `session_id`, `payload_json`, `created_at`. |
| `event_log` | Terminal trace for History/Event Log: the Event snapshot plus `outcome` ∈ `fired`, `expired`, `deleted_by_agent`, `deleted_by_user`, `cleared_by_stop`, `cleared_by_delete`, `outcome_run_id` (agent deletes), `detail_json` (match facts) and `at`. Nothing in this series deletes log rows, Runs, Memories or Ledger data. |

`runs` gains `triggers_json`: an event Run stores the exact trigger array it delivered.
Startup checks `PRAGMA table_info(runs)` and adds the nullable column only when it is absent,
so databases created before Tickets 27–31 retain their existing Sessions and Runs. Reopening
an already migrated database is a no-op; deleting or recreating the database is not required.
Every Event-domain time is a fixed-width UTC string `2006-01-02T15:04:05.000000000Z07:00`
(RFC3339 with nine fractional digits) so SQLite string comparison orders correctly;
`created_at`/`expires_at`/`fired_at` in responses use that format.

## 63.2 Tools

`create_event`, `delete_event` and `list_events` are registered in the same ordered Tool loop
as §61.5 and §62: every accepted attempt consumes one Tool unit, invalid arguments are
`INVALID_TOOL_ARGUMENTS` with `outcome:"not_executed"` and no persistence write, and the
Server injects `session_id`/`created_run_id`; the model never supplies them.

```json
{"name":"create_event","input_schema":{"type":"object","properties":{
  "type":{"type":"string","enum":["timer","price_above","price_below","price_cross_above","price_cross_below","price_change_pct"]},
  "params":{"type":"object"},
  "expires_at":{"type":"string","format":"date-time"}},
  "required":["type","params"],"additionalProperties":false}}
{"name":"delete_event","input_schema":{"type":"object","properties":{"event_id":{"type":"string"}},"required":["event_id"],"additionalProperties":false}}
{"name":"list_events","input_schema":{"type":"object","properties":{},"additionalProperties":false}}
```

`params` by type (unknown fields, JSON null, wrong types, NaN/Inf and non-positive numbers are invalid):

| type | params | Default `expires_at` |
|---|---|---|
| `timer` | `at`: RFC3339 with offset, must be strictly later than the current time; stored and returned in UTC | `at + 1h` |
| `price_above`, `price_below`, `price_cross_above`, `price_cross_below` | `market` (optional, empty → `crypto`, lowercased), `symbol` (required, uppercased), `price` > 0 | `now + 24h` |
| `price_change_pct` | `market`, `symbol` as above; `base_price` > 0 supplied by the Agent (normally from `get_market_snapshot`) and persisted unchanged; `direction` ∈ `up`/`down`; `pct` > 0 | `now + 24h` |

`expires_at` (optional RFC3339, any offset) must be strictly later than the current time
and, for timers, strictly later than `at`. `max_active_events = 10` per Session: an eleventh
active Event returns `EVENT_LIMIT_EXCEEDED`. The count check and insert share one
transaction under the Runtime lock, and that transaction also requires the calling Run to
still be `running`; a Run interrupted by a concurrent hard stop receives `RUN_NOT_RUNNING`
and writes nothing. `delete_event` for an unknown id or another Session's Event returns
`EVENT_NOT_FOUND`. SQLite failure returns `PERSISTENCE_UNAVAILABLE` (`not_executed` before
commit, `unknown` on commit failure) and latches Runtime unavailability as §61.1.

Success results:

```json
{"ok":true,"result":{"id":"ev_1f2e3d4c5b6a7980","session_id":"s_…","created_run_id":3,"type":"price_cross_above",
 "params":{"market":"crypto","symbol":"BTCUSDT","price":60000},"state":null,
 "created_at":"2026-09-14T10:00:00.000000000Z","expires_at":"2026-09-15T10:00:00.000000000Z"}}
{"ok":true,"result":{"events":[/* Event objects ordered by created_at, id */]}}
```

`state` is `null` except for cross Events after their first valid sample:
`{"price":59000,"updated_at":"…"}`. `delete_event` returns the removed Event.

## 63.3 Control Panel HTTP

```http
GET    /api/v1/sessions/{session_id}/events            → 200 {"events":[Event…]}
DELETE /api/v1/sessions/{session_id}/events/{event_id} → 200 Event
POST   /api/v1/sessions/{session_id}/events            → 405 METHOD_NOT_ALLOWED, Allow: GET
```

Only the Agent creates Events (§16, §49). Unknown Session is `404 SESSION_NOT_FOUND`;
an Event that does not belong to the Session (including another Session's id) is
`404 EVENT_NOT_FOUND`; repeating a delete is `404`. `DELETE` follows the §61.3 mutation rule:
request cancellation before commit is `408 REQUEST_CANCELED`, other SQLite failure is
`503 PERSISTENCE_UNAVAILABLE`. A user delete writes a `deleted_by_user` trace and the
Event never fires afterwards.

## 63.4 Resident round, claim and dispatch

`AGENT_SERVER_EVENT_SCAN_INTERVAL` (positive Go duration, default `1s`) is the interval of
both resident tasks:

1. `agent-events-local` reads its own current clock snapshot and immediately runs expiry,
   due-timer claim, pending append and idle-Session admission in one Runtime-locked SQLite
   transaction. It performs no remote I/O, so any number of slow price requests cannot delay
   timer or expiry progress.
2. `agent-events-price` reads its sampling snapshot, finds the distinct unexpired watched
   instruments, then performs one `GET /v1/market/price` per instrument outside the Runtime
   lock. Sampling is Server work using the Backend USER credential; it refreshes no Account
   Token and creates no Ledger entry or Run trace. Each request uses
   `AGENT_SERVER_TOOL_TIMEOUT`. After sampling, price expiry checks, matches, pending appends
   and idle-Session admission use the same Runtime lock and transaction claim path as the
   local task.

The two callbacks may run concurrently, but their SQLite claim/append/admission transactions
are serialized by the Runtime lock. Within either transaction, expiry is processed before a
timer or price match; commit precedes Run goroutine start. Any SQLite failure rolls that whole
transaction back, returns the error to the EventLoop and latches the §61.1 persistence fault;
a canceled task context is not a fault.

There is deliberately no global atomic "HTTP scan round" across local timers and remote price
responses. Trigger arrival order is the order in which claim transactions commit and append
their AUTOINCREMENT pending rows, not callback start order, EventLoop map order, or HTTP
response order. Local and price triggers observed near each other may therefore share one Run
or enter consecutive Runs depending on commit/admission order; each trigger remains exactly
once and each Run preserves pending-row order.

Expiry is inclusive and wins: an Event whose `expires_at` has arrived is expired even if it
is due or its price condition holds in the same claim transaction. A concurrent transaction
that arrives later observes the active row already removed. The one-shot claim is the
`DELETE FROM events` row count inside the transaction, so fire, expire, agent delete, user
delete and stop clearing have exactly one terminal outcome per Event.

Trigger payload (identical in `pending_triggers`, `runs.triggers_json` and the model context):

```json
{"type":"price_above","event_id":"ev_…","created_run_id":3,
 "params":{"market":"crypto","symbol":"BTCUSDT","price":60000},
 "expires_at":"…","fired_at":"…",
 "matched":{"price":60010,"updated_at":"…","condition":"price >= 60000"}}
```

`matched` carries the deciding facts: timers `{"at"}`; level Events price, source
`updated_at` and the condition; cross Events additionally `previous_price` and
`previous_updated_at`; percentage Events `base_price` and `change_pct`.

## 63.5 Pending Triggers and event Runs

Every trigger, including one arriving while the Session is idle, is first appended to the
Session's `pending_triggers` in arrival order. A Session with no active Run consumes the
first `max_triggers_per_run = 10` rows in `id` order into a new Run in the same
transaction that inserts the Run, increments `sessions.current_run_id` and deletes the
consumed rows. A crash therefore either leaves the rows unconsumed or leaves a Run whose
`triggers_json` records them; it never duplicates or drops one. 23 triggers become Runs of
10, 10 and 3; nothing is reordered, summarized or prioritized, and mixed types stay in one
batch. Triggers arriving while a Run is busy queue behind existing rows and the next batch
starts immediately when that Run leaves, regardless of whether it `completed` or `failed`;
the previous Run's terminal state is never modified. Stopped and deleted Sessions, a
closing Runtime, and a latched persistence fault dispatch nothing.

An event Run has `trigger:"event"` and `triggers:[…]` in the Run API and receives
`"trigger":"event","triggers":[…]` in its context; `session_start`/`session_restart` Runs
have `triggers:null`.

## 63.6 Timer semantics

`at` is an absolute instant; any RFC3339 offset is accepted and normalized to UTC. A timer
is due when `now >= at` (inclusive) and fires at the first round observing that, so
`fired_at` may be later than `at` after downtime; if `now >= expires_at` first, it expires
instead. Timers due in the same round fire in `created_at, id` order.

## 63.7 Price semantics

A sample is valid when Backend answered HTTP 200 with a finite `price > 0` and an RFC3339
`updated_at` no older than 30 seconds before the round clock (Backend itself serves prices
fresher than its 1.5 s TTL or waits for a new one, so this only rejects broken sources or
gross clock skew). Backend errors, timeouts, malformed bodies and stale samples make no
change: the Event stays active until a later valid sample, its expiry or a delete. A sample
whose `updated_at` is not strictly later than the previous valid sample of that instrument is
a duplicate or out-of-order sample and is ignored; only the previous round's sample time and
value are kept in memory, and the resident task holds no other market cache.

| type | fires when | notes |
|---|---|---|
| `price_above` | `price >= params.price` | level condition, evaluated on every new valid sample including the first |
| `price_below` | `price <= params.price` | same |
| `price_cross_above` | `previous_price < params.price <= price` | the first valid sample only initializes `state`; a baseline equal to the threshold is not "below" it |
| `price_cross_below` | `previous_price > params.price >= price` | symmetric |
| `price_change_pct` | `up`: `(price - base_price) / base_price * 100 >= pct`; `down`: `… <= -pct` | `base_price` is the Agent-supplied baseline as of the Event's `created_at`; no rolling window |

Cross baselines are persisted in `events.state_json` after every newer valid sample, so a
restart compares against the last persisted sample rather than forgetting it; a sample not
strictly newer than the persisted baseline is ignored. Polling cannot observe crossings
between two samples or during downtime: the comparison is always between the last valid
sample and the next one, and an excursion that reverses inside that gap is not a crossing.

## 63.8 Stop, delete, and shutdown

`POST /stop` and `DELETE /sessions/{id}` clear the Session's active Events and pending
triggers in the same transaction that interrupts the Run, writing `cleared_by_stop` /
`cleared_by_delete` traces. `restart_context` gains `triggers` (the interrupted Run's
triggers), `cleared_events` (the removed Event objects) and `pending_triggers` (the
unconsumed payloads in arrival order); the next `session_restart` Run receives them as
facts and must decide itself whether to create Events again. Nothing is restored
automatically and no summary is generated. A repeated `POST /stop` on an already
stopped Session keeps the saved `restart_context` unchanged (it clears nothing and
recomputes nothing), so those facts survive until the next `session_restart` Run. Runtime `Close()`/Server shutdown is not a user
stop: active Events, cross baselines and pending rows stay in SQLite for Ticket 36
recovery, and dispatch simply stops. Stopping one Session does not affect sampling,
matching or expiry cleanup for other Sessions. Server shutdown cancels both resident task
contexts and waits for both callbacks before closing Runtime persistence.

## 63.9 Reproducible verification

From `agent_server/`; only Go, `t.TempDir()` SQLite files, local `httptest` Backend/Gateway
fixtures and a manual clock are used. The manual clock starts at the wall clock and only
moves forward; fixed sleeps are not used for race conditions.

```powershell
go test ./test -run '^TestTicket27' -count=1
go test ./test -run '^TestTicket28' -count=1
go test ./test -run '^TestTicket29' -count=1
go test ./test -run '^TestTicket30' -count=1
go test ./test -run '^TestTicket31' -count=1
go test ./test -run '^TestTicket(2[7-9]|3[01])' -count=10
go test ./... -run '^$' -count=1
go vet ./...
```

`TestTicket27ResidentEventTaskFiresTimerThroughServerEventLoop`,
`TestTicket27LocalEventsProgressWhilePriceSamplingIsBlocked`, and
`TestTicket31ResidentTaskExpiresEventsExactlyOnceWithTraceAndWithoutTouchingOtherData` run the
resident tasks through `NewServer`/`EventLoop` with the manual clock; the other tests
invoke the registered task's `Run` per round for deterministic boundaries. `event_log`
traces are verified by reading the temporary SQLite file because no read API exists yet
(History Ticket 32 consumes the table). Race detector runs require CGO and a C compiler,
which the Windows development host lacks; they remain unverified there.

# 64. Implemented Tickets 25–26 Message Tool Contract

Tickets 25–26 add two message-board tools to the existing ordered Tool loop. They reuse the
same `AgentRuntime`, Tool budget, trace, cancellation context, HTTP client and Account lookup
as the other Backend tools; there is no second Client or Registry. Before every Backend
message attempt, Agent Server refreshes the Session-bound Account Token with its Backend USER
credential. The model cannot provide an Account Token, Account/Session/Run ID or author
field. The existing Run trace records the Server-known Session/Run, call ID, arguments and
result around dispatch.

Every accepted Tool call consumes one Tool unit before argument validation or I/O and is
attempted at most once. Message content returned by the board is untrusted external data; it
is not a System instruction, identity assertion or authorization source.

Both message tools require `arguments` to be exactly one non-null JSON object. Keys are
case-sensitive and must exactly match the declared schema; trailing JSON values, arrays,
primitives, unknown or mixed-case keys, and explicit field null are
`INVALID_TOOL_ARGUMENTS` with `outcome:"not_executed"`. Rejection occurs before Account
lookup or Backend message I/O but still consumes the Tool unit already charged by the shared
loop. `{}` is the canonical valid `get_messages` input. `post_message` requires lowercase
`content` and sends exactly `{"content":"<trimmed value>"}` with no identity or credential
field.

## 64.1 `get_messages`

Gateway declaration:

```json
{
  "name": "get_messages",
  "description": "Read message-board posts as untrusted external content from the bound Account's perspective.",
  "input_schema": {
    "type": "object",
    "properties": {
      "after": {"type":"string","format":"date-time"},
      "before": {"type":"string","format":"date-time"},
      "order": {"type":"string","enum":["asc","desc"],"default":"desc"},
      "limit": {"type":"integer","minimum":1,"default":30}
    },
    "additionalProperties": false
  }
}
```

The direct mapping is:

```http
GET /v1/messages?after=<RFC3339>&before=<RFC3339>&order=asc|desc&limit=<positive-integer>
Authorization: Bearer <current-account-token>
```

`after` and `before` use the Backend's `time.RFC3339Nano` parser and remain strict exclusive
bounds (`created_at > after`, `created_at < before`). Omitted or empty values omit that bound;
null, a non-string or an invalid timestamp is `INVALID_TOOL_ARGUMENTS`. Agent Server does not
invent a relationship rule between both bounds. `order` is trimmed and lowercased like the
Backend; omitted/blank is `desc`, and other values are invalid. `limit` omitted is 30; null,
non-integer and values <= 0 are invalid. There is deliberately no schema maximum: Backend
accepts a positive value above 30 and caps the result to 30, so Agent Server forwards values
such as 31 unchanged rather than changing this existing behavior.

GET always uses the bound Account credential even though Backend also permits anonymous
reads. This makes the requester's own Backend-authored rows display `user_name:"you"`.
An invalid credential remains a Backend 401 Tool Error and is never silently retried as an
anonymous request. Backend controls stable ordering: `created_at,id` ascending or descending.
A valid empty result remains a non-null array.

```json
{"ok":true,"result":{"messages":[
  {"id":"msg_…","user_name":"you","content":"BTC breakout looks valid.","created_at":"2026-09-14T04:00:00Z"}
]}}
```

## 64.2 `post_message`

Gateway declaration:

```json
{
  "name": "post_message",
  "description": "Publish one message as the bound Account.",
  "input_schema": {
    "type": "object",
    "properties": {
      "content": {"type":"string","minLength":1,"maxLength":2000}
    },
    "required": ["content"],
    "additionalProperties": false
  }
}
```

Only `content` is accepted. Agent Server applies the Backend's `strings.TrimSpace` rule,
rejects empty content and counts the trimmed value as Unicode code points with a maximum of
2000. The request body contains no other field:

```http
POST /v1/messages
Authorization: Bearer <current-account-token>
Content-Type: application/json

{"content":"BTC breakout looks valid."}
```

Backend derives the real author from that Token and stores neither `you` nor a client-supplied
author. A successful HTTP 201 is returned to the model as:

```json
{"ok":true,"result":{"id":"msg_…","user_name":"you","content":"BTC breakout looks valid.","created_at":"2026-09-14T04:00:00Z"}}
```

Both Tool results whitelist only `id`, `user_name`, `content` and `created_at`. Unknown
top-level or row fields are discarded, including `author_id`, `author_type` and any
credential. Missing/wrong-typed required fields, a null `messages` value, malformed/trailing
JSON or a non-RFC3339 `created_at` is `BACKEND_RESPONSE_INVALID`.

## 64.3 Errors, timeout and retry boundary

Invalid Tool arguments return:

```json
{"ok":false,"error":{"code":"INVALID_TOOL_ARGUMENTS","message":"…","outcome":"not_executed"}}
```

A fully read Backend error preserves its existing `error` and `message`, including
`invalid_request`, `unauthorized` and `internal_error`. GET errors and POST 4xx responses are
`outcome:"not_executed"`; POST 5xx is `outcome:"unknown"`. Account refresh failures preserve
the §61 dependency code and are `not_executed` unless the Tool deadline has elapsed.

The single Tool deadline covers Account refresh, connection, headers and complete response
body reading. Transport failure, cancellation/timeout during any response body, or an invalid
successful response has `outcome:"unknown"`. In particular, Backend can commit a POST before
the response is lost. Agent Server never automatically resends it and provides no idempotency
claim; the model may first call `get_messages`, or may explicitly issue another `post_message`
that consumes another Tool unit.

## 64.4 Reproducible verification

From `agent_server/`:

```powershell
go test ./test -run '^TestTicket25' -count=1
go test ./test -run '^TestTicket26' -count=1
go test ./test -run '^TestTicket2[56]' -count=10
go test ./... -run '^$' -count=1
go vet . ./test
```

Ticket 25–26 fixtures are local HTTP servers with temporary Agent SQLite files and explicit
synchronization for GET partial-success-body timeout, POST partial-error-body timeout and the
uncertain-write case. They verify strict root/key/null argument admission before Account I/O,
null/malformed success responses, exact Backend 5xx error preservation, no retries, and Run
progress ownership/input/result without credential leakage.

`TestTicket26ActualBackendPublishesWithBoundAccountAndReadsRequesterIdentity` additionally
builds and starts the actual Backend with a temporary SQLite database/log directory and port.
It creates 35 seed messages and captures their real IDs and `created_at` values. Through Agent
Tool calls it verifies default descending order with omitted limit returns the newest 30,
`limit:100` is Backend-capped to the oldest 30 in ascending order, and `after`/`before` are
exclusive for individual, combined and empty ranges. It creates the Agent Session, resets the
bound Account Token, proves the old Token is rejected, then verifies `post_message` refreshes
identity and succeeds with the new Token. It uses no fixed port or real credential and
kills/waits the test process during cleanup.

The super-review repair was reproduced before the strict decoder: root `null` and mixed-case
`LIMIT` reached Backend, while mixed-case `CONTENT` published successfully. After repair,
`go test ./test -run '^TestTicket2[56]' -count=10` passed, as did the compile and vet commands
above. This is focused verification; it is not the final full-suite or series-review approval.

---

# 66. Implemented Tickets 32–35 Run History and Memory Contract

Tickets 32–35 add durable, downloadable Run History and Session-owned Memory without a
second runtime, model, compactor, vector database or free-form write path. SQLite remains the
authority for Run and Memory state. History JSON is a derived durable projection whose index
is stored in SQLite and whose files are repaired from terminal Runs at startup.

## 66.1 Normal completion and finalization

A normal completion consumes only the existing counted Generate decision. It requires
`finish_reason:"stop"`, no Tool calls, and `content` containing exactly one JSON object with
all four fields and no unknown/trailing data:

```json
{
  "output": "human-readable final decision",
  "summary": "short durable summary",
  "candidate_memories": [
    {"type":"risk_rule","summary":"Require confirmation.","content":"Do not chase without volume.","importance":3}
  ],
  "expire_memories": [
    {"id":"m_0123456789abcdef0123456789abcdef","reason":"superseded by newer evidence"}
  ]
}
```

`output` and `summary` are trimmed nonempty strings; summary is at most 500 Unicode
characters. Each array has at most 20 entries. Candidate fields are all required and are the
only accepted fields: type is 1–64 Unicode characters, summary is 1–500, content is 1–65536
UTF-8 bytes, and importance is integer 1, 2 or 3. Expiration IDs must have the Server-issued
`m_` plus 32 lowercase hexadecimal shape, must be unique in the decision, and their trimmed
reasons are 1–500 Unicode characters. The Server, never the model, creates Memory IDs and
assigns Session/Run ownership and timestamps.

Plain final text, missing/null fields, unknown fields, malformed/trailing JSON, and invalid
candidates trigger a tool-disabled repair request within the same four-attempt terminal
allowance. Exhaustion fails the Run as `FINALIZATION_INVALID`. Malformed or
foreign/nonexistent expiration references fail without retrying the completion transaction. A finalization storage
failure follows the existing persistence-fault path and cannot expose a completed Run or a
partial candidate/expiration set. The completed Run update, candidate inserts, requested
expirations, History index update and staged History rewrite are one logical commit. The
`UNIQUE(session_id,run_id,candidate_index)` key and `status='running'` completion predicate
make retries non-duplicating.

Exhausted Gateway/model/dependency retries, explicit user stop, Runtime
interruption and every other failed/interrupted path skip this finalization shape entirely.
They retain already persisted progress and reason, keep `summary:null` and `output:null`, and
publish or expire no Memory.

## 66.2 Fixed History shards, index and repair

`AGENT_SERVER_HISTORY_DIR` defaults to `agent-history` in the process command. Embedded
Runtime callers that omit `RuntimeConfig.HistoryDirectory` derive `<DatabasePath>.history`;
otherwise a nonblank directory is required. The Server creates it with owner-only directory
permissions where supported.

Every terminal Run immediately rewrites its fixed 20-ID range:

| Run ID | shard range |
|---:|---|
| 1–20 | 1–20 |
| 21–40 | 21–40 |
| 41–60 | 41–60 |

The internal filename is `{server_session_id}_{start}-{end}.json`; clients never submit or
receive a filesystem path. `run_history_files(session_id,start_run_id,end_run_id,file_path,
created_at,updated_at)` is the index. A writer reads the prior shard, creates a
mode-0600 temporary file in the same directory, writes and syncs complete JSON, then atomically
renames it. All affected ranges in one SQLite transaction are deduplicated and staged under one
`historyMu` acquisition until commit. Rollback restores prior complete files in reverse order
(or removes newly created files) and joins restoration errors, so an older callback cannot
overwrite a newer committed Run or expose only half of a multi-range update.

Startup scans terminal Runs, rebuilds every represented fixed range from SQLite, recreates
missing indexes, replaces missing/corrupt projections, and removes abandoned History temp
files before serving. Reads never regenerate: a missing/corrupt/index-invalid shard returns a
fixed persistence error and latches Runtime persistence health. A transaction whose History
index write fails rolls back both terminal state and the staged file; it cannot report a fake
completion. Recovery of a pre-existing SQLite `running` Run remains Ticket 36's responsibility.

```http
GET /api/v1/sessions/{session_id}/runs/{run_id}/history
Authorization: Bearer <AGENT_SERVER_ADMIN_TOKEN>
```

The response is the entire containing shard, including all currently terminal Runs in that
range, not only the requested Run:

```json
{"session_id":"s_…","start_run_id":1,"end_run_id":20,"runs":[{"run_id":1,"status":"completed","trigger":"session_start","triggers":null,"summary":"…","output":"…","error":null,"iterations":[],"model_call_count":1,"tool_call_count":0,"start_time":"…","end_time":"…","memory_ids":[],"event_log":[]}]}
```

Unknown Session/Run is `404 RUN_NOT_FOUND`; a currently running Run is `409
HISTORY_NOT_READY`; unavailable durable History is `503 PERSISTENCE_UNAVAILABLE`. A request
context canceled or expired during either the Run lookup or History-index lookup returns local
`408 REQUEST_CANCELED`, does not latch persistence health, does not cancel unrelated active
Runs, and leaves later mutations admissible. Deleted Sessions remain readable for audit. Only
GET is allowed.

Both HTTP and Tool reads verify that the index and body use the canonical range computed from
the requested Run ID. A shard is invalid if it omits its identity/range/Run collection; if Runs
are unordered/out of range; or if a Run omits terminal status, trigger, nullable
summary/output/error/triggers, terminal timestamps, counters, iterations, Memory IDs or Event
log collections. Completed Runs require summary/output and null error; failed/interrupted Runs
require null summary/output and an error. Event rows likewise retain required identity,
creation/expiry/outcome timestamps and present nullable state/outcome-Run/detail fields.
Unknown additional fields and optional facts inside trace entries remain forward/migration
compatible. Invalid files are never served and startup rewrites them from SQLite.

## 66.3 History trace projection and filtering

`iterations` is the persisted ordered Run progress. Every model entry includes its visible
content, finish reason, Tool calls and nullable normalized Usage fields (`input_tokens`,
`output_tokens`, `cached_tokens`, `total_tokens`) when Gateway supplied Usage. No hidden model
reasoning is requested or stored. Failed/interrupted History contains its status, reason and
the progress already durable at interruption, with null summary/output.

Tool results are persisted as their structured success/error envelope. Trading, balance,
orders, position stop, message and critical failure results remain intact. Only successful
`get_ohlcv` results are reduced for History: the model receives all candles, while persisted
progress replaces `candles` with `candle_count`, first/last `candles_sample`, and
`candles_omitted`. Existing `runs.triggers_json` and matching `event_log` rows are projected as
Event trace, including Event `created_at` and `expires_at`; created/pending Event facts also
remain in ordinary Tool/trigger progress. Terminal Run staging also refreshes older creator
ranges referenced by an Event outcome/current event trigger. Session stop/delete stages its
interrupted current range plus only the deduplicated older ranges affected by Event clearance.
User Event deletion and resident expiry/fire require the Event-owned transaction integration
recorded in `HISTORY-MEMORY-HANDOFF.md`; until that ownership handoff is applied, those three
late paths can leave an already-written creator shard stale. This is an explicit pending series
gate, not a read-time repair or background queue.
Each Run lists the IDs of Memories created by that Run, including later-expired records.

## 66.4 Memory records and progressive Run context

A Memory detail contains:

```json
{"id":"m_…","session_id":"s_…","run_id":1,"type":"risk_rule","summary":"Require confirmation.","content":"Do not chase without volume.","importance":3,"run_start_at":"…","run_end_at":"…","is_expired":false,"expired_at":null,"expired_source":null,"expired_run_id":null,"expiration_reason":null,"created_at":"…","updated_at":"…"}
```

At the start of a Run, the model context adds only the latest earlier completed Run's
`recent_run_summary:{run_id,summary}` and at most 20 unexpired `memory_index` entries ordered
by importance descending, then Run ID descending, then ID. An index entry has only
`id,run_id,type,summary,importance,is_expired`; it has no full content. A completed prior Run
creates no restart context when stop cleared no Event facts; when clearance facts exist, that
restart context contains only prior Run identity/status plus those facts. It never repeats the
completed output or raw model progress, so candidate contents cannot leak around this
progressive boundary. Interrupted factual restart progress remains unchanged.

## 66.5 Agent Memory and History tools

The four tools use the current Run cancellation context, consume the ordinary Tool budget,
are traced through the existing loop, and never accept Session IDs or paths:

- `memory_list({"limit":N})`: `limit` defaults to 20 and is 1–100; returns
  `{"ok":true,"result":{"memories":[metadata…]}}`.
- `memory_search({"query":"text","limit":N})`: query is trimmed and nonempty; minimal SQL
  substring matching searches only type and summary, never content, with the same order/cap.
- `memory_read({"memory_id":"m_…"})`: returns one full unexpired Memory from the current
  Session.
- `get_run_history({"run_id":N})`: resolves the current Session's indexed shard internally,
  validates it, then returns only that selected terminal Run object.

Arguments are exact JSON objects with no unknown fields. Cross-Session, nonexistent and
expired Memory reads are indistinguishable `MEMORY_NOT_FOUND`; blank search and malformed
arguments are `INVALID_TOOL_ARGUMENTS`; an unknown/nonterminal prior Run is `RUN_NOT_FOUND`.
Missing/corrupt History is `HISTORY_UNAVAILABLE` and latches the persistence fault. No Tool
accepts a filesystem path or returns credentials. A user stop/Runtime close that cancels a
local Memory/History read yields `TOOL_INTERRUPTED` if a result is observed and does not turn
that expected cancellation into a persistence fault.

## 66.6 User Memory HTTP and expiration

```http
GET /api/v1/sessions/{session_id}/memories?page=1&include_expired=false
GET /api/v1/sessions/{session_id}/memories/{memory_id}
DELETE /api/v1/sessions/{session_id}/memories/{memory_id}
```

List pages are fixed at 100 and ordered by importance descending, Run ID descending, then ID.
They return lifecycle/source metadata but omit `content`; `page` is a positive integer and
`include_expired` is exactly `true` or `false` (default false). Detail returns content and is
an audit read, so a directly addressed expired Memory remains visible. Unknown/cross-Session
IDs return `404 MEMORY_NOT_FOUND`. User POST/PATCH is not provided. Memory audit endpoints
remain available for soft-deleted Sessions; DELETE only changes an unexpired record.

DELETE is an idempotent soft expiration. Its first successful transition sets
`is_expired:true`, `expired_at`, `expired_source:"user"`, `expired_run_id:null`, reason
`"deleted by user"`, and `updated_at`; repeated DELETE returns the existing record without
rewriting provenance or time. A normal Agent finalization can atomically expire current-
Session IDs with `expired_source:"agent"`, its Run ID and the supplied reason. Already-expired
IDs are successful no-ops, so first expiration provenance wins in Agent/User races and no
operation revives a record. Agent list/search/read and default HTTP lists always exclude
expired data; `include_expired=true` and detail preserve original content and Run references.
If any requested expiration is malformed, nonexistent or foreign, the entire finalization—
including otherwise valid expirations and new candidates—rolls back.
The DELETE path rechecks Runtime `closed` and latched persistence state after acquiring the
authoritative Runtime mutex and before beginning SQLite work. A Close/fault race therefore has
only two outcomes: a fully committed soft expiration, or `503 PERSISTENCE_UNAVAILABLE` with no
Memory change. Caller-context cancellation remains local `408 REQUEST_CANCELED`.

## 66.7 Reproducible focused verification

Tests use `t.TempDir()` SQLite/History directories, local HTTP fixtures, public HTTP/Tool
boundaries, synchronized race channels, corrupt/missing files and SQLite rejecting triggers.
From `agent_server/`:

```powershell
go test ./test -run '^TestTicket32' -count=1 -timeout=90s
go test ./test -run '^TestTicket33' -count=1 -timeout=60s
go test ./test -run '^TestTicket34' -count=1 -timeout=90s
go test ./test -run '^TestTicket35' -count=1 -timeout=60s
go test ./test -run '^TestTicket3[2-5]' -count=1 -timeout=180s
go test ./... -run '^$'
go vet ./...
```

This is focused series verification, not final full-suite execution or independent series
approval. Race-detector execution still requires CGO and a C compiler unavailable on the
documented Windows development host.

---

# 67. Implemented Tickets 37–38 Usage, Current Run and Tool Statistics Contract

Tickets 37–38 read the existing `runs.model_call_count`, `runs.tool_call_count` and persisted
`runs.progress_json` trace. They do not add a second counter, Tool registry, Gateway client,
History reader or monitoring service. Every accepted model/Tool attempt is written to that
trace before external work starts, so failed, timed-out, canceled and model-requested retries
remain attempts and survive restart. Monitoring reads SQLite in Run order and never reads
History shard files.

## 67.1 Model attempt identity, timing and Usage

Every new `model_call` trace entry has Server-generated `iteration`, configured `model_name`
and `started_at`. After the per-decision catalog lookup succeeds, the Server writes the actual
resolved `provider_id` and provider `model` before calling Generate. The model cannot submit or
override these identities. A completed attempt also has `ended_at`, monotonic elapsed
`duration_ms`, visible response fields and normalized nullable `usage`; a lookup/Generate
failure has end timing but no invented Usage. A canceled in-flight attempt can retain a start
without an end because hard stop forbids a late callback from rewriting terminal progress.

All timestamps are UTC RFC3339Nano strings. Durations are nonnegative integer milliseconds;
duration subtraction uses the monotonic component of the two in-process `time.Time` values.
Existing pre-37 trace without timing/source fields remains readable and contributes to counts
and Usage with unavailable timing/source represented as null or absent trace facts.

Gateway Usage has four independently nullable, nonnegative fields. `cached_tokens` is a subset
already included in `input_tokens`; it is never added to input or total. `total_tokens` is only
the Gateway/Provider's reported value and is never synthesized. A malformed negative count or
`cached_tokens > input_tokens` is an invalid Gateway response and fails that model attempt.
Aggregates sum only reported values. A token total is JSON null when no attempt reported that
field, and `unknown_attempts` gives the exact number of model attempts that did not report each
field. Thus a non-null value can be a known reported subset while the corresponding unknown
count makes incompleteness explicit. There is no monetary cost field: V1 has no authoritative,
time-versioned Provider pricing source, so the Agent Server does not invent pricing.

## 67.2 Session Usage API

```http
GET /api/v1/sessions/{session_id}/usage
Authorization: Bearer <AGENT_SERVER_ADMIN_TOKEN>
```

No query parameters are accepted and no request body is defined. The response includes Session total, every
Run in ascending `run_id`, and actual per-attempt model-source groups. A catalog failure is
grouped under the configured `model_name` with null `provider_id`/`model` because no actual
mapping was resolved.

```json
{
  "session_id": "s_...",
  "input_tokens": 15,
  "output_tokens": 3,
  "cached_tokens": 4,
  "total_tokens": 13,
  "model_calls": 3,
  "unknown_attempts": {
    "input_tokens": 1,
    "output_tokens": 2,
    "cached_tokens": 2,
    "total_tokens": 2
  },
  "by_model": [
    {
      "model_name": "research-model",
      "provider_id": "provider-a",
      "model": "actual-model-v2",
      "input_tokens": 15,
      "output_tokens": 3,
      "cached_tokens": 4,
      "total_tokens": 13,
      "model_calls": 2,
      "unknown_attempts": {"input_tokens":0,"output_tokens":1,"cached_tokens":1,"total_tokens":1}
    },
    {
      "model_name": "research-model",
      "provider_id": null,
      "model": null,
      "input_tokens": null,
      "output_tokens": null,
      "cached_tokens": null,
      "total_tokens": null,
      "model_calls": 1,
      "unknown_attempts": {"input_tokens":1,"output_tokens":1,"cached_tokens":1,"total_tokens":1}
    }
  ],
  "by_run": [
    {
      "run_id": 1,
      "status": "completed",
      "input_tokens": 15,
      "output_tokens": 3,
      "cached_tokens": 4,
      "total_tokens": 13,
      "model_calls": 2,
      "unknown_attempts": {"input_tokens":0,"output_tokens":1,"cached_tokens":1,"total_tokens":1}
    }
  ]
}
```

An existing Session with no Runs returns zero `model_calls`, null token totals, zero unknown
counts and empty `by_model`/`by_run` arrays. Soft-deleted Sessions remain available for audit.
Unknown Session is `404 SESSION_NOT_FOUND`; query parameters are `400 INVALID_REQUEST`; methods
other than GET are `405 METHOD_NOT_ALLOWED` with `Allow: GET`; persisted monitoring read or
shape/overflow failure is `500 INTERNAL_ERROR`. Authentication uses the common Agent admin
boundary and returns `401 UNAUTHORIZED` before route handling.

## 67.3 Current Run polling

`GET /api/v1/sessions/{session_id}/runs/current` retains the existing top-level Run fields and
adds:

```json
{
  "session_id": "s_...",
  "run_id": 7,
  "status": "running",
  "trigger": "event",
  "triggers": [],
  "summary": null,
  "output": null,
  "error": null,
  "progress": [],
  "model_call_count": 2,
  "decision_count": 1,
  "tool_call_count": 1,
  "start_time": "2026-09-15T01:00:00Z",
  "end_time": null,
  "model_name": "research-model",
  "provider_id": "provider-a",
  "model": "actual-model-v2",
  "max_loop": 20,
  "max_tool_call": 40,
  "loop_remaining": 19,
  "tool_call_remaining": 39,
  "elapsed_ms": 1250,
  "current_activity": "waiting_for_model",
  "last_activity_at": "2026-09-15T01:00:01Z",
  "input_tokens": 10,
  "output_tokens": null,
  "cached_tokens": 3,
  "total_tokens": null,
  "model_calls": 2,
  "unknown_attempts": {"input_tokens":1,"output_tokens":2,"cached_tokens":1,"total_tokens":2}
}
```

`elapsed_ms` is Server now minus `start_time`, clamped to zero. `model_call_count` remains the
total number of model attempts, including retries and terminal finalization. New traces mark
attempts as `decision` or `terminal`; `decision_count` and `loop_remaining` are derived from
those trace facts. Legacy unmarked traces conservatively treat each distinct iteration as a
decision, so monitoring never overstates the remaining budget. Remaining budgets are clamped
to zero. Activity values are
`starting`, `resolving_model`, `waiting_for_model`, `preparing_tool_call`,
`tool:<registered-or-requested-name>`, `between_iterations`, `finalizing` or `running` for an
unknown migrated trace type. `last_activity_at` is the latest trace start/end, or Run start when
empty. Provider/model are the latest successfully resolved Server identities and are null
before any resolution succeeds.

No running Run remains `404 CURRENT_RUN_NOT_FOUND`. Its message distinguishes a running Session
waiting for an Event, a stopped Session and a deleted Session. This preserves the polling
contract where Session `status:"running"` does not imply an active Run. Unknown Session remains
`404 SESSION_NOT_FOUND`.

## 67.4 Tool trace and statistics

Every budget-admitted Tool request writes `started_at` immediately before the single existing
`executeTool` dispatch, so invalid arguments and unknown names count exactly like external I/O.
When dispatch returns, the same entry receives `ended_at` and monotonic `duration_ms` together
with its existing structured result. The interval therefore covers all Server validation,
credential refresh and dependency I/O, but excludes model generation and the preceding DB
trace write. There is no automatic retry; an LLM retry is a later counted trace entry.

```http
GET /api/v1/sessions/{session_id}/tools/stats
Authorization: Bearer <AGENT_SERVER_ADMIN_TOKEN>
```

No query parameters are accepted and no request body is defined. The response lists every Tool from the exact
same definitions sent to Gateway, sorted by `tool_name`, plus `_unknown`. All model-invented
unregistered names are grouped as `_unknown`: their raw names remain in auditable Run trace,
but cannot inject unbounded public metric identities.

```json
{
  "session_id": "s_...",
  "tools": [
    {
      "tool_name": "get_market_snapshot",
      "call_count": 3,
      "success_count": 1,
      "error_count": 2,
      "in_flight_count": 0,
      "latency_sample_count": 2,
      "avg_latency_ms": 15,
      "max_latency_ms": 20,
      "last_latency_ms": 20
    },
    {
      "tool_name": "memory_list",
      "call_count": 0,
      "success_count": 0,
      "error_count": 0,
      "in_flight_count": 0,
      "latency_sample_count": 0,
      "avg_latency_ms": null,
      "max_latency_ms": null,
      "last_latency_ms": null
    }
  ]
}
```

A result envelope with `ok:true` is success; every returned Tool Error—including argument
validation, unknown Tool, timeout and dependency error—is error. A started new-format entry
without an end is in-flight while its Run is running, and an error after that Run becomes
failed/interrupted. Therefore `call_count = success_count + error_count + in_flight_count`.
Legacy completed trace without timing is classified from its result but contributes no latency
sample. Likewise a hard-stopped callback cannot write a late end, so the attempt is an error
without a sample. `latency_sample_count` makes this explicit.

Average and max use all nonnegative completed duration samples. `last_latency_ms` is the last
available sample in ascending Run ID and trace-attempt order; attempts without a duration do
not replace it. All three are null when there is no sample. Different Sessions are isolated by
the query predicate. Repeated reads and restart only re-project persisted trace and cannot add
or duplicate an attempt.

Unknown Session, invalid query, method, authorization and internal read/shape failures use the
same statuses/codes as §67.2.

## 67.5 Focused reproducible verification

Tests use local synchronized Gateway/Backend fixtures and temporary SQLite databases. Fixed
persisted traces provide exact nullable Usage and 10/20/30 ms Tool durations; live tests cover
model resolution identity, in-flight polling, invalid arguments, timeout, unknown Tool, hard
stop and restart without fixed sleeps.

From `agent_server/`:

```powershell
go test ./test -run '^TestTicket37' -count=1 -timeout=60s
go test ./test -run '^TestTicket38' -count=1 -timeout=60s
go test ./test -run '^TestTicket3[78]' -count=3 -timeout=90s
go test ./test -run '^TestTicket3[2-5]' -count=1 -timeout=180s
go test ./... -run '^$' -count=1
go vet ./...
```

This is implementation awaiting the consolidated Tickets 37–38 `super_reviewer` review; it is
not approval. Race-detector verification still requires CGO and a C compiler unavailable on
the documented Windows host.

After all Ticket 37–38 source and documentation changes, the Agent module's one end-of-series
full run `go test ./... -count=1 -timeout=300s` passed in 57.653s; final `go vet ./...` also
passed. This was not a full-project Backend/Gateway/frontend suite.


---

# 65. Implemented Tickets 21–24 Account and Trading Tool Contract

Tickets 21–24 add ten Account/trading tools to the same ordered Tool loop described in
§61.5. They live in `trading_tools.go` (`tradingToolDefinitions`, `executeTradingTool`,
`backendToolRequest`) and are wired through the existing `generate` tool list and the
`executeTool` default branch. There is no second registry, client, counter or lifecycle:
every accepted attempt consumes one `max_tool_call` unit before validation, is stored in
Run progress with an unknown in-flight result first, refreshes the Account Token immediately
before Backend I/O (§61.2), shares the Run cancellation context and `AGENT_SERVER_TOOL_TIMEOUT`,
and is never retried. Account Tokens appear only in the `Authorization` header, never in
arguments, results, progress or logs; Backend Account responses carry no token.

The Backend remains the only trading state. Agent Server does not read or write the Backend
database, does not cache balances/positions/orders, and does not decide business rules:
prices, leverage, balance, reduce-only, stop sides and order state are validated by the
Backend and its `error`/`message` are returned verbatim to the model.

## 65.1 Tool declarations

All schemas are `type:"object"` with `additionalProperties:false`; none accepts
`account_id`, `session_id`, `run_id`, `source` or `token`. Unknown fields, JSON `null`, and
wrong types are `INVALID_TOOL_ARGUMENTS` with `outcome:"not_executed"` and no Backend I/O.

| Tool | Arguments | Backend request (Bearer = current Account Token) |
|---|---|---|
| `get_account` | none | `GET /v1/account` |
| `get_positions` | `product?` enum `spot`/`futures` | `GET /v1/positions[?product=]` |
| `get_orders` | `status?` enum `open`/`filled`/`canceled`/`rejected`, `limit?` 1–200 (default 50) | `GET /v1/orders?limit=&status=` |
| `get_order` | `order_id` | `GET /v1/orders/{order_id}` |
| `get_trades` | `limit?` 1–200 (default 50) | `GET /v1/trades?limit=` |
| `get_ledger` | `limit?`, `before_seq?` ≥1, `run_id?` ≥1 | `GET /v1/ledger?limit=&before_seq=&run_id=&session_id=` |
| `place_order` | `symbol`, `side` enum `buy`/`sell`, `quantity` >0, `market?` (default `crypto`), `product?` (default `futures`), `type?` enum `market`/`limit` (default `market`), `price?` >0, `leverage?` >0, `stop_loss?` ≥0, `take_profit?` ≥0 | `POST /v1/orders` |
| `cancel_order` | `order_id` | `POST /v1/orders/{order_id}/cancel` |
| `close_position` | `position_id`, `quantity?` >0 (omitted = whole position) | `POST /v1/positions/{position_id}/close` |
| `set_position_stops` | `position_id`, `stop_loss?` ≥0, `take_profit?` ≥0 (at least one; omitted keeps, 0 removes, >0 sets) | `PATCH /v1/positions/{position_id}` |

String enums are trimmed and lower-cased before comparison; `symbol` is trimmed and forwarded
(Backend upper-cases it). Numbers must be JSON numbers; their exact JSON text is forwarded so
the Backend alone owns numeric conversion. Only fields the model supplied (plus the defaults
listed above for `place_order`) are sent; e.g. a market order without `price` sends no `price`.
`get_ledger` with `run_id` also sends `session_id=<this Session>`, so the model can only read
events its own Session caused; without `run_id` the whole Account ledger is returned.

## 65.2 Source injection and traceability

Every mutation body carries the Server's identity, never model input:

```json
{"symbol":"BTCUSDT","side":"buy","type":"market","product":"futures","market":"crypto",
 "quantity":0.01,"leverage":10,"source":{"session_id":"s_…","run_id":3}}
```

`cancel_order` sends `{"source":…}`; `close_position` sends `{"quantity":…,"source":…}`
(quantity omitted when not given); `set_position_stops` sends the given levels plus `source`.
The Backend (backend/SPEC.md §21) stores the Run on the Order, copies it to the Trade, keeps
separate stop-loss/take-profit setters on the Position, writes a Ledger entry in the same
database transaction as the change, and traces engine-triggered stop closes to the Run that
set the level. Consequently orders filled after the Run ended, stops fired after the Session
stopped or the Agent Server restarted, and cancels/closes of orders created by earlier Runs
all remain attributable through `GET /v1/ledger` (Account) and `GET /v1/accounts/:id/ledger`
(USER), in addition to the Run progress trace that records every attempt, its arguments and
its correlated result.

## 65.3 Results, errors and outcomes

Successful Backend JSON is returned unchanged as `{"ok":true,"result":{…}}`. Failures use
the §61.5 Tool Error shape with these outcomes:

| Situation | code | outcome |
|---|---|---|
| Argument validation failed | `INVALID_TOOL_ARGUMENTS` | `not_executed` |
| Account refresh failed (`ACCOUNT_DISABLED`, `ACCOUNT_NOT_FOUND`, `BACKEND_AUTH_FAILED`, `BACKEND_UNAVAILABLE`, …) | §61 dependency code | `not_executed` |
| Backend JSON error, any read, or a mutation with status other than 500 (e.g. 400 `invalid_request`, 404 `not_found`, 409 `order_not_open`, 422 `insufficient_balance`, 501 `not_implemented`, 504 `market_timeout`) | Backend `error` | `not_executed` |
| Mutation answered 500 `internal_error` | `internal_error` | `unknown` |
| Non-JSON 5xx on a mutation | `BACKEND_ERROR` | `unknown` |
| Transport failure after the request was sent | `BACKEND_UNAVAILABLE` | `unknown` |
| Tool deadline or Run cancellation before the Account Token was obtained (no trading request sent) | `TOOL_TIMEOUT` | `not_executed` |
| Tool deadline or Run cancellation after the request was sent, including during body reading | `TOOL_TIMEOUT` | `unknown` |
| Fully delivered malformed 2xx body | `BACKEND_RESPONSE_INVALID` | `unknown` |

`not_executed` means the Backend proved it rejected the request before mutating (its
rejections are answered before any transaction commits); `unknown` means the Server cannot
prove the operation did not happen. The Server never resends: a retry is a new Tool Call
chosen by the model, consumes another unit, and the model can first inspect state with
`get_order`, `get_positions` or `get_ledger`. Disabled Accounts fail at token refresh with no
Backend trading I/O; cross-Account ids return the Backend's `not_found` because the Backend
scopes every lookup by the authenticated Account.

## 65.4 Reproducible verification

From `agent_server/` (local Backend/Gateway fixtures, temporary SQLite files, no fixed ports):

```powershell
go test ./test -run '^TestTicket21' -count=1
go test ./test -run '^TestTicket22' -count=1
go test ./test -run '^TestTicket23' -count=1
go test ./test -run '^TestTicket24' -count=1
go test ./test -run '^TestTicket2[1-4]' -count=3
go test ./... -run '^$' -count=1
go vet ./...
```

`TestTicket21CrossService*` builds and starts `backend/test/market_cross_service_host`;
`TestTicket2[234]CrossService*` build and start `backend/test/trading_cross_service_host`
(the same host plus the matching engine at `MATCHING_INTERVAL=50ms`) against a local Binance
ticker fixture, create real Accounts with the configured USER credential, and drive
`place_order`/`cancel_order`/`close_position`/`set_position_stops`/`get_ledger` through the
Gateway fixture, including a delayed limit fill, cancel after the creating Run, a partial and
full close, and a take-profit fired after the Session stopped and the Agent runtime was closed
and reopened on the same database. Backend-side acceptance (source validation, legacy
migration without invented sources, ledger pagination/filters, cancel-versus-fill and
stop-removal-versus-trigger races) is in `backend/test/ticket2[234]_*_test.go`.
## Gateway conversation continuity and Claude diagnostics

Every Generate attempt in one Agent Run sends `conversation_id` as the exact string `<session.ID>/<runID>`. Retries and later decision iterations keep that value, while another Run receives a different value. The Gateway may use it for Claude CLI transport continuity; the Agent Runtime remains the source of truth for replayed messages and does not persist provider sessions.

Generate responses may carry nullable `num_turns` and `total_cost_usd`. Valid nonnegative values are retained on the public model-call trace alongside Usage; absent values remain absent rather than becoming zero. These diagnostics and input/cached token metrics are observational and do not guarantee cache or billing savings. If the Gateway restarts or expires its transport session, it safely replays the full request without changing Run semantics.

## 67. Bounded model context, audit evidence and continuation

The Runtime keeps audit evidence and model transport context as separate representations. Each
Run persists its normalized initial context in `runs.initial_context_json` and publishes it as
`initial_context` in the Run API and fixed History shard. It contains the trigger, bounded state
and Memory index, limits, and `available_tools`; credentials and Account Tokens are never added.
The mandatory exact tool-name sentence and this array are both derived from
`runtimeToolDefinitions`, so there is no second tool registry. Terminal repair instructions and
denied Tool results are separate progress entries. A denial does not increment
`tool_call_count`, but records the call id, name, arguments, error code and definite
`not_executed` outcome.

Before Generate, the Runtime deterministically projects `messages` to at most 768 KiB and
rejects any encoded Gateway request above 1 MiB without dispatch. It preserves the system and
initial user messages and every assistant/Tool group. When reduction is required, old read
results are compacted first; errors retain code/outcome, OHLCV retains metadata plus first/last
candle samples, and prior Run History retains its reference, summary/counters and bounded output
preview rather than recursively copying old iterations. Mutation results are only reduced as a
last resort and retain identifiers, status, errors and uncertainty. Each model-call trace records
original/projected message bytes, compacted-message count and actual request bytes. Healthy
prefixes are unchanged while below the bound; the Runtime does not call a summarizer model.

After a successful Generate request containing `N` full normalized messages, the next request
may additionally contain:

```json
{"continuation":{"after_messages":N,"messages":[...]}}
```

The Runtime proves that the next projected context has the acknowledged request as an exact
prefix followed by the assistant response returned by that Generate call. The continuation
omits that assistant response and contains every later Tool result and instruction. Bootstrap,
an unprovable/empty delta, a changed bounded projection, and every retry send full `messages`
without continuation. The full messages remain present in every request for Gateway validation,
stateless/native providers and bounded cold recovery. Generate responses may include validated
optional `session_mode`, `fallback_reason` and positive `invocation_count`; they remain absent
when omitted and are copied to the model-call trace.

Model catalog resolution is reserved in persistence before external I/O but is not a Generate
dispatch and therefore does not increment `model_call_count`. A resolution failure is an
auditable `model_resolution` entry. Login, pinned-version, missing Harness and Provider config
errors are nonretryable. Usage may remain `null`/unknown.

## 67.1 Terminal finalization and Memory validation

Terminal finalization has four total model attempts. Its attempt numbers are monotonic across
malformed envelopes, transient Generate failures, disabled Tool calls and Memory-reference
repairs. Transient failures from the preceding decision request do not consume this allowance.
A natural malformed final response becomes terminal attempt 1; the fourth valid response may
complete, while a fourth invalid response fails with `FINALIZATION_INVALID`. Hard stop still
prevents later progress or completion writes.

Candidate Memory shape errors and nonexistent/cross-Session expiration references are repairable.
All expiration references are checked in the completion transaction before candidate insertion;
on a reference error the transaction rolls back, a tool-disabled repair instruction is audited,
and no Memory is published. SQLite, History filesystem, commit and other persistence failures are
not model-repairable. Successful Run completion, candidate publication, expirations and History
staging remain one atomic completion path. No Tool or mutation is automatically replayed.
