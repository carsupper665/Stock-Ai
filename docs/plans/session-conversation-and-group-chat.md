# 計畫：Session 對話延續 ＋ 群聊

用語見根目錄 `CONTEXT.md`。兩個功能互相獨立，可以分開做、分開上線；建議先做 A。

## A. Session 對話延續

### 目標
每個 Run 不再從零開始：Run 接在上一個 Run 結束（或中斷）的地方，帶著整段 Session 對話繼續；
帶進來的是**語意保留**後的版本；Run 內超出預算才 **壓縮**，壓縮結果成為之後的基準。

### 決策（已定）
- Session 對話＝模型看到的完整訊息序列：system prompt、各 Run 的喚醒訊息、assistant 回應、工具呼叫與結果。
- **語意保留**在每個 Run 結束時用規則做（不呼叫模型）：
  | 內容 | 保留形式 |
  |---|---|
  | 思考過程 | 不留 |
  | 模型文字回應 | 原文 |
  | 改變狀態的工具（下單、撤單、平倉、設停損停利、記憶寫入、Event 建立/刪除、發言） | 參數＋結果 |
  | 查詢工具（K 線、快照、帳戶、持倉、ledger、記憶讀取、群聊讀取等） | 「查了什麼 → 一行重點」，每個工具各一條摘要規則 |
  | 連續相同的失敗 | 只留一次並註明次數 |
- **壓縮**：Run 內（含 Run 開頭加上喚醒訊息後）估計用量 ≥ window 的 80% 時觸發，目標壓到 50%；
  最近一個 Run 盡量不動，先壓更早的部分；壓縮後版本取代原本的 Session 對話，不再還原。
- **中斷**：從中斷處接續。沒有結果的工具呼叫補一則系統產生的結果：
  「Run 中斷，這個呼叫可能已經執行，先用查詢工具確認狀態再決定」，再接下一則喚醒訊息。
- 拿掉 `recent_run_summary`（上一個 Run 的 summary 不再帶進 context）；記憶機制保留。
- 既有 Session 從下一個 Run 才開始累積，不回補。

### 實作
1. **儲存**（`agent_server/internal/agent/runtime.go` schema）：新增 `session_conversations`
   （session_id、已語意化的訊息 JSON、估計 tokens、最後納入的 run_id、updated_at）。一個 Session 一列，Run 結束時在同一個 transaction 更新。
2. **Run 開頭**（`agent_server/internal/agent/loop.go` 的 `runAgent`）：
   - 讀 Session 對話；沒有就用 system prompt 開新的。
   - system prompt 永遠是第一則（`session.Prompt` 建立後不變）。
   - 原本的 JSON context 改成**喚醒訊息**附在尾端：trigger／triggers、current_time、帳戶快照、記憶索引、bring_back 記憶、
     restart／recovery／retry 的說明、被 tag 的群聊訊息（B 完成前沿用現有 inbox）。
   - `memory.ProgressiveContext` 拿掉 recent run 那段（`agent_server/internal/memory/memory.go:377`）。
   - 開頭就超過門檻 → 先走壓縮。
3. **語意保留**：新增 `agent_server/internal/agent/semantic.go`：把本 Run 的 `[]gatewayMessage`
   轉成語意版本；查詢工具的一行摘要放在 `agent_server/internal/tools/` 各工具旁，沿用現有的 projection（例如 `ProjectOHLCV`）。
4. **壓縮**：改 `compactContext`／`compactRunHistory`（`model_context.go:276`、`:356`）讓它處理跨 Run 的對話，
   而不是只處理 `messages[2:]`；觸發 80%、目標 50%，規則先壓，不夠再用 `summarizeHistory`。
5. **Run 結束與中斷**：正常結束、失敗、max_loop 收尾都把語意版本寫回；
   `server_recovery` 從 `progress_json` 重建到中斷點，補上「結果未知」的工具結果（`recovery.go:129`）。
   `manual_retry` 就是接在失敗處的新 Run，`retry_context` 改成喚醒訊息的一段。
6. **Gateway**：`conversation_id` 目前是 `session/run`（`loop.go:412`），改成每個 Run 仍用新的 id 並送完整訊息，
   不依賴 Provider 端保留狀態；`continuationFor` 只在同一個 Run 內使用，不變。
7. **前端**：Run 明細的「初始 context」改名「喚醒訊息」；Session 頁加一個「Session 對話」檢視（目前用量／window、最後壓縮時間、可展開內容）。

### 驗證
- 單元：語意保留規則（思考移除、重複失敗合併、查詢摘要、狀態工具保留）；中斷補結果；壓縮觸發與目標比例。
- 整合（`agent_server/test/`，用 fake Gateway 抓送出的 messages）：Run 2 的請求包含 Run 1 的語意版本；
  中斷後 Run 開頭含「結果未知」；超出 80% 時壓縮且之後 Run 用壓縮版；舊 Session 第一個 Run 只有 system＋喚醒訊息。
- 手動：本機跑一個 Session 連續 3 個 Run，在前端「Session 對話」檢視確認內容與用量。

## B. 群聊

### 目標
留言板換成所有 Agent 與 USER 共用的單一**群聊**。只有**提及（tag）**某 Agent 的訊息會主動送進它的 Session 對話；
其他訊息不通知，看不看全憑它自己。USER 可在建立 Session 時或隨時，設定該帳號的**聊天權限**（唯讀／停權）。

### 決策（已定）
- 一個房間；tag 改為選填；內容規則只寫在 prompt，程式不擋。
- 被 tag 的訊息：Run 進行中收到 → 下一次呼叫模型前插入（不打斷生成；收尾階段不插，留給下個 Run 的喚醒訊息）；
  running Session 待機時只有 USER 的 tag 會叫醒，最後一則 USER tag 滿 10 秒後合併為一次喚醒；其他 Agent 的 tag 留到下次 Run。每則只送一次；讀取權限關閉期間的 tag 不補送。
- 主動讀群聊：只回傳該 Agent 上次讀過之後的新訊息，可加參數往前翻。
- 聊天權限掛在帳號、由交易後端強制。工具不從清單拿掉；被禁止時後端回錯誤（讀或發各自判斷）：
  發言 `chat_post_disabled`「此帳號已被管理者停用群聊發言權限」，讀取 `chat_read_disabled`「此帳號已被管理者停用群聊讀取權限」。
- 舊留言直接成為群聊歷史；前端群聊頁每 5 秒重抓。

### 實作
1. **後端**（`backend/internal/message/`、`backend/internal/database/models.go`、`backend/internal/api/message.go`）：
   - `Account` 加 `ChatRead bool`、`ChatPost bool`（預設 true，遷移後舊帳號為 true）；USER 用 `PATCH /v1/accounts/{id}` 修改。
   - `Post`：移除「至少一個收件人」限制（`checkRecipients` 只保留 tag 必須存在、不能 tag 自己）；`ChatPost=false` → 403 `chat_post_disabled`。
   - `List`：Account 身分且 `ChatRead=false` → 403 `chat_read_disabled`；新增 `after=<message id>` 查詢參數（依 created_at、id 排序取之後的訊息）。
2. **Agent Server**（`agent_server/internal/agent/message_tools.go`、`agent_server/internal/tools/message.go`）：
   - 工具改名為群聊用語（`get_chat`、`post_chat`），描述與 `messageBoardInstruction` 改寫；權限錯誤原樣回給模型。
   - 每個 Session 記兩個游標：`chat_read_cursor`（`get_chat` 預設從這裡往後讀）、`mention_cursor`（tag 已送到哪裡）。
   - 每次呼叫模型前（`loop.go` 主迴圈 `callModel` 之前，非 finalizing 時）查 `tag=自己&after=mention_cursor`，
     有新的就插入一則 user 訊息並推進游標；Run 開頭同樣查一次放進喚醒訊息（取代 `inbox()`）。
     讀取權限關閉時查詢會回 `chat_read_disabled`：把游標推到最新，不送任何訊息。
3. **前端**：
   - `frontend/src/pages/messages/` 改成群聊頁：訊息依時間排列、5 秒重抓、tag 改為可選的 @ 對象。
   - 帳號詳情與 Session 頁加「聊天權限」：可讀＋可發／唯讀／停權（改的是帳號的兩個開關）；新增 Session 表單也能設定。
4. **文件**：`CONTEXT.md` 已更新；更新 `backend/SPEC.md`、`agent_server/spec.md` 中留言板相關段落。

### 驗證
- 後端單元：無 tag 可發；唯讀發言 403、停權讀取 403；`after` 分頁；USER 不受限制。
- Agent 整合：Run 進行中 USER tag → 下一次模型請求含該訊息且只出現一次；收尾階段的 tag 出現在下個 Run 的喚醒訊息；
  待機時 tag 不觸發 Run；唯讀時 `post_chat` 回 `chat_post_disabled`；讀取關閉期間的 tag 之後不補送。
- 手動：前端群聊頁發 @nemotron 帳號，看 Run 明細裡插入的訊息；切成唯讀後確認它的發言被擋。

## 尚未定、實作時預設的地方
- Run 結束的最終 JSON 仍保留 `summary` 欄位（Runs 列表與 Dashboard 在用），只是不再帶進下一個 Run。
- 語意保留只對「這次之後」的 Run 生效；`get_run_history` 不變。
