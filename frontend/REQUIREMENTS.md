# 前端使用者需求 v0.1

USER（人）用的控制台要滿足什麼：**看到什麼、能做什麼、有什麼規則**。
不講長相、不講程式怎麼組（那是 `frontend/SPEC.md`）。`SPEC.md` v0.1 第一階段只做交易後端（T 組）；
Agent Server（A 組）與 LLM Provider Server（L 組）的頁面之後照 `SPEC.md` §3 的頁面包契約加。

來源縮寫：

| 縮寫 | 文件 |
|---|---|
| B | `backend/SPEC.md`（交易後端 v0.4） |
| F | `frontend/SPEC.md` §10（對著 `backend/internal/api` 整理的實際端點、欄位、錯誤） |
| A | `agent_server/spec.md`（Agent Server v1） |
| L | `llm_provider_server/spec.md`（LLM Provider Server V1） |

每條需求標來源章節。另外兩種標記：

- **建議**：規格沒寫，但控制台照常理需要；可砍。
- **待定**：規格沒定、會影響前端怎麼設計，要先決定。集中在 §5。

## 0. 使用者與情境

### 0.1 誰用

只有一個人：USER，名字來自後端 `.env` 的 `USER_NAME`（B §1）。
Agent（LLM 虛擬帳號）不用前端，拿 Account Token 直接打 API（B §1；A §21–§23）。
所以不需要多人、角色、權限；需要的是**一個人看得懂、管得住所有 Agent**。

### 0.2 管什麼

一個控制台，管三個服務：

| 服務 | 管的東西 | API 前綴 | 來源 |
|---|---|---|---|
| 交易後端 | 虛擬帳號、部位／訂單／成交、行情、留言板 | `/v1/*` | B、F |
| Agent Server | Session、Run、Memory、Event、用量／Ledger／Tool 統計 | `/api/v1/*` | A §41 |
| LLM Provider Server | Provider、API Token | `/v1/providers/*`、`/healthz` | L §10、§12、§23 |

三個服務各自獨立、各自會掛，控制台要分得出誰活著（X-02）。

### 0.3 主要情境

1. **初次設定**：登入 → 建 Provider、加 API Token → 建虛擬帳號（拿到 Account Token）→ 建 Session（name、model、prompt、上限）→ start。
   順序有依賴：沒有 Provider 就沒有可用的 model；沒有帳號 Agent 就不能交易（待定 2）。空狀態要指向下一步（建議）。
2. **日常監控**：Session 總覽掃一眼 → 點進去 → 現在這個 Run 在幹嘛 → 上一個 Run 做了什麼 → 部位／PnL → Ledger 每筆錢是哪個 Run 動的。
3. **介入**：stop Session；刪 Event；刪 Memory；在留言板留話給 Agent；緊急平倉（用該帳號的 token 代打）。
4. **事後分析**：翻 Run 歷史、Memory、Tool 統計、Token 用量；跨 Session 比較（deleted Session 資料保留就是為了這個，A §3.1）。
5. **整理**：停用／刪帳號、重設 token、停用 Provider／Token、刪 Session。

## 1. 共通需求（X）

| # | 需求 | 來源 |
|---|---|---|
| X-01 | 貼 USER Token 登入；重新整理不掉、關分頁登出。Agent Server 與 LLM Server 的管理 API 用什麼認證，規格沒定（待定 1） | B §1；F §4 |
| X-02 | 殼上看得到三個服務各自活不活。交易後端 `GET /v1/health` → `ok`／`degraded`；LLM Server `GET /healthz` → `ok`，但它不代表各 Provider 通；Agent Server 沒定義（待定 5） | F §10.2 A；L §23 |
| X-03 | 時間全是 UTC／RFC3339。顯示本地時間；列表可用相對時間（「3 分鐘前」）、明細要完整時間（建議） | B §15；F §10.1 |
| X-04 | 數字只格式化，不做金額運算 | F §10.1 |
| X-05 | 錯誤：三個服務三種信封（見 §7），使用者只要看到給人看的訊息；可重試的（`market_unavailable`、`market_timeout`、`RATE_LIMITED`、`PROVIDER_TIMEOUT`）要能重試。自己的 USER Token 收到 401 → 登出 | F §5、§10.5；A §54；L §22 |
| X-06 | 沒有 WebSocket／SSE，全部輪詢或手動重載。running 中的 Session 輪詢 `runs/current`；價格輪詢；其他頁面要有「重新整理」並顯示最後更新時間（建議） | A §46；F §6 |
| X-07 | 追溯：重要資料都能回到 `session_id + run_id`。Ledger 條目 → Run；Memory → Run；Event → 建立它的 Run；Run → 完整歷史。任何地方出現 `run_id` 都可以點過去 | A §1、§53、§59 |
| X-08 | 破壞性操作要確認，並把後果講清楚（下表） | — |
| X-09 | 一鍵複製：Account Token、`session_id`、`run_id`、Provider id、Memory id（建議） | — |
| X-10 | 分頁方式依資料不同（下表），前端照做，不自己發明 | — |
| X-11 | 狀態一眼可辨（資訊需求，不是美術）：Session、Run、帳號、訂單、Provider／Token 啟停、PnL 正負。`interrupted` 不能長得像完成 | A §8 |
| X-12 | 不可編輯的東西不給編輯入口：Session 設定、Memory（不可建不可改）、Event（不可建）、留言作者、Token 明文 | A §3、§32、§16；B §14；L §13 |

X-08 破壞性操作：

| 操作 | 後果 | 來源 |
|---|---|---|
| 刪 Session | 不可復原；資料保留可查 | A §11 |
| stop Session | hard stop：當前 Run 變 `interrupted`、active Event 全清、進行中的模型／Tool 請求取消 | A §10 |
| 刪帳號 | 留言板作者變 `[deleted]` | B §2；F §10.4 |
| 重設 Account Token | 舊 token 立即失效，正在用它的 Agent 會斷 | B §1 |
| 刪 Memory | 標 `is_expired`，Agent 不再看到；不物理刪 | A §30 |
| 刪 Event | Agent 不會被通知、不會自動重建；要等它下次醒來自己判斷 | A §16；A §10 不自動 restore 的精神 |
| 刪／停用 Provider | 用它的 Session 下次叫模型會失敗（`PROVIDER_NOT_FOUND`／`PROVIDER_DISABLED`） | L §22 |
| 刪／停用 Token、換 secret | 舊 secret 立即失效 | L §12 |

X-10 分頁：

| 資料 | 方式 | 來源 |
|---|---|---|
| Session | `page`，每頁固定 100；沒有 server 篩選／排序，前端自己 cache、排序、篩選 | A §43 |
| Run、Memory、Ledger | `page` | A §46、§48、§50 |
| 留言 | 游標 `after`／`before`，每次最多 30，`asc`／`desc` | B §15 |
| 訂單、成交 | 只有 `limit`（預設 50、最多 200、最新在前），沒有游標 | F §10.1 |

## 2. 交易後端（T）

### 2.1 虛擬帳號

| # | 需求 | 來源 |
|---|---|---|
| T-01 | 列表：`id` `user_name` `initial_balance` `balance` `status` `created_at` | B §2；F §10.3 |
| T-02 | 建立：`user_name`、`initial_balance`。成功後拿到 token，要當場看到、複製。名字重複 → `name_taken` | B §1、§2；F §10.5 |
| T-03 | 改名、停用、啟用。停用的帳號不能交易（`account_disabled`），資料照看 | B §2；F §10.2 B、§10.5 |
| T-04 | 刪除 | B §2；F §10.2 B |
| T-05 | 每個帳號「被哪個 Session 用」 | 待定 2 |

### 2.2 帳號明細

| # | 需求 | 來源 |
|---|---|---|
| T-06 | 基本資料 + Account Token：看、複製、重設 | B §1；F `token/reset` |
| T-07 | 部位：`id` `market` `symbol` `product` `side` `quantity` `entry_price` `mark_price` `leverage` `margin` `unrealized_pnl` `stop_loss` `take_profit` `opened_at`；可依 `product` 篩 | F §10.2 B、§10.3 |
| T-08 | 訂單：`id` `market` `symbol` `product` `side` `type` `quantity` `price` `leverage` `stop_loss` `take_profit` `reduce_only` `status` `reject_reason` `filled_quantity` `avg_fill_price` `fee` `realized_pnl` `created_at`；可依 `status` 篩、設 `limit`；rejected 要看得到原因 | F §10.2 B、§10.3 |
| T-09 | 成交：`id` `order_id` `market` `symbol` `product` `side` `role` `quantity` `price` `fee` `realized_pnl` `created_at`；可設 `limit` | F §10.3 |
| T-10 | 帳務總覽 `locked_margin` `available` `unrealized_pnl` `equity` 目前只有 Account Token 自查拿得到（`GET /v1/account`）。USER 要在帳號明細看到這四個數字 | F §10.2 D；待定 6 |

### 2.3 行情

| # | 需求 | 來源 |
|---|---|---|
| T-11 | 查價：`market` + `symbol` → `price`、`updated_at`。使用者要知道：查價會讓後端啟動該 symbol 的訂閱；`stock` 目前 `not_implemented`；`market_unavailable`／`market_timeout` 可重試 | B §7、§10；F §10.2 C、§10.4、§10.5 |
| T-12 | 訂閱狀態：每個 `market:symbol` 目前 `inactive`／`activating`／`active`；60 秒沒人要就自動 `inactive`。這是營運資訊，不是交易資訊 | B §10、§11；F §10.2 B |

### 2.4 留言板

| # | 需求 | 來源 |
|---|---|---|
| T-13 | 看留言：不登入也能看。每則 `user_name` `content` `created_at`；自己發的顯示 `you`；作者帳號已刪顯示 `[deleted]` | B §12、§16、§18；F §10.4 |
| T-14 | 發留言：以 USER 身分，`content` ≤ 2000 字。作者由 token 決定，不能選 | B §14；F §10.2 C |
| T-15 | 翻更早：`before` 游標，每次 ≤ 30 | B §15 |
| T-16 | 分辨人與 Agent：只能靠名字（`USER_NAME` vs `Account.user_name`）；API 不回 `author_type` | B §2、§13、§18；待定 8 |

### 2.5 手動介入（用該帳號的 Account Token 代打）

`SPEC.md` §12 把這組列為第一階段之後；需求先列，API 都在（F §10.2 D）。

| # | 需求 | 來源 |
|---|---|---|
| T-17 | 緊急平倉：全平或部分（`POST /v1/positions/:id/close`） | F §10.2 D |
| T-18 | 取消掛單（`POST /v1/orders/:id/cancel`，只有 `open` 的限價單） | F §10.2 D |
| T-19 | 改停損停利（`PATCH /v1/positions/:id`；省略不動、`0` 移除、`> 0` 設定） | B §5；F §10.2 D |
| T-20 | 代 Agent 下單：`symbol` `product` `side` `type` `quantity` `leverage`（1–100，spot 只能 1）`price`（limit 必要）`stop_loss` `take_profit` | B §4；F §10.2 D |
| T-21 | 代打時畫面要明確顯示「目前以 <帳號名> 身分操作」；代打的 token 收到 401 只報錯，不登出 USER | F §5 |

## 3. Agent Server（A）

### 3.1 Session 總覽

| # | 需求 | 來源 |
|---|---|---|
| A-01 | 一頁看完所有 Session 的監控欄位：`id` `name` `status` `model_name` `model_level` `current_run_id` `created_at` `started_at` `last_run_at` `pending_event_count`（= active Event 數）`total_runs` `total_tool_calls` `total_tokens` `last_run_summary` `order_count` `position_count` `PnL` `account summary` `runtime health`。後三項具體欄位待定 9 | A §44 |
| A-02 | 預設不顯示 `deleted`，但要能切換顯示（保留是為了比較） | A §43、§3.1 |
| A-03 | 排序、篩選（status、model、name）都在前端做 | A §43 |
| A-04 | 從列表直接 start／stop（建議） | A §9、§10 |

### 3.2 建立 Session

| # | 需求 | 來源 |
|---|---|---|
| A-05 | 必填 `name`、`model_name`；選填 `model_level`、`prompt`、`max_loop`、`max_tool_call` | A §4 |
| A-06 | `model_name` 合法值從哪來 | A §55；待定 3 |
| A-07 | `model_level` 合法值由 model／provider 能力決定（例 `low` `high` `max`） | A §4；待定 3 |
| A-08 | `prompt` 留空 = Server Default。使用者要知道留空的後果，最好看得到預設是什麼 | A §4；待定 4 |
| A-09 | `max_loop`、`max_tool_call` 留空 = Server Default。超限的後果要讓使用者知道：那個 Run 直接 `failed` | A §4、§7；待定 4 |
| A-10 | 建好是 `stopped`，不自動啟動 | A §9 |
| A-11 | 建立後設定不可改，要改就建新的 → 建議提供「用這個 Session 的設定建新 Session」 | A §42 |
| A-12 | Session 綁哪個交易帳號 | A §23；待定 2 |

### 3.3 Session 明細

| # | 需求 | 來源 |
|---|---|---|
| A-13 | 一次看到：設定（`name` `model_name` `model_level` `prompt` `max_loop` `max_tool_call`）、時間（`created_at` `started_at` `stopped_at`）、`status`、當前 Run（A-22）、上一個 Run 摘要、active Events、Memory 摘要、用量摘要、PnL、部位、帳戶摘要、runtime health | A §45、§37 |
| A-14 | 大量資料（Run 列表、Ledger、Memory 全文、歷史）點了才載 | A §45 |
| A-15 | 重啟脈絡：stop 後下次 start 是 `session_restart`，Agent 會拿到「上次在哪中斷、哪些 Event 被清掉、上次做了什麼」。使用者也想看到同一份資訊 | A §10、§37；待定 7 |

### 3.4 控制

| # | 需求 | 來源 |
|---|---|---|
| A-16 | start：`stopped` → `running`，立刻開一個 Run（第一次 trigger `session_start`，之後 `session_restart`）。`deleted` 不能 start → 不給按鈕 | A §9、§10、§3.1 |
| A-17 | stop：hard stop，確認時列出後果（X-08） | A §10 |
| A-18 | delete：soft delete；`running` 的先 stop 再刪；不可 restore | A §11 |
| A-19 | 409 = 現在的狀態不允許這個操作 → 顯示訊息並重載 | A §54 |
| A-20 | 沒有「手動叫醒 Agent」：同一 Session 同時只有一個 Run，使用者不能建 Event。唯一的手動觸發是 stop 再 start（`session_restart`） | A §12、§16、§58 |

### 3.5 Run

| # | 需求 | 來源 |
|---|---|---|
| A-21 | Run 列表：`run_id` `status` `triggers` `summary` `start_time` `end_time` `duration` `tool_call_count` `model_call_count` `order_count` `position_count` tokens（`input` `output` `cached` `total`）`error`。`run_id` 遞增即時間順序 | A §27、§38、§46、§5.1 |
| A-22 | 當前 Run（輪詢 `runs/current`）：`run_id` `status`、loop n／`max_loop`、tool calls n／`max_tool_call`、elapsed、current activity（現在在叫哪個 tool／等模型）、tokens。使用者要一眼知道「Agent 現在在幹嘛、離上限多遠」 | A §46 |
| A-23 | Run 明細：A-21 全部欄位 + 每個 trigger 的內容（一個 Run 可能多個，最多 10 個）+ 失敗／中斷原因 | A §13、§14、§7、§8 |
| A-24 | Run 完整歷史：iterations、每次模型呼叫（模型可見輸出、decision summary）、每次 tool 呼叫（name、arguments、過濾後結果）、建立／刪除的 Event、引用的 Memory、usage、error。來源是約 20 個 Run 一份的 JSON shard，前端拿到整份自己快取，只顯示要看的那個 Run。不含 hidden chain-of-thought | A §33、§35、§47 |
| A-25 | tool 結果是過濾過的 → 標明「已過濾」，讓使用者知道缺的不是 bug（建議） | A §29 |
| A-26 | Run 狀態語意：`running`／`completed`／`interrupted`（hard stop、crash）／`failed`（超限、錯誤）。`interrupted` 不可顯示成完成 | A §8、§20 |
| A-27 | trigger 類型：`session_start` `session_restart` `server_recovery` `timer` `price_above` `price_below` `price_cross_above` `price_cross_below` `price_change_pct` | A §9、§10、§17、§20 |

### 3.6 Memory

| # | 需求 | 來源 |
|---|---|---|
| A-28 | 列表：`id` `run_id` `type` `summary` `importance`（1–3）`run_start_at` `run_end_at` `is_expired` `created_at`。`summary` 是索引，列表主要看它 | A §30、§39、§48 |
| A-29 | 點開看 `content` 全文 | A §48 |
| A-30 | 刪除 = `is_expired = true`，不物理刪。expired 的要看得出來、要能選擇顯示或隱藏（建議） | A §30、§48 |
| A-31 | 不可建立、不可修改 | A §32、§48 |
| A-32 | 從 Memory 跳到產生它的 Run 歷史 | A §31；X-07 |

### 3.7 Event

| # | 需求 | 來源 |
|---|---|---|
| A-33 | 列表：`id` `type` `params` `created_run_id` `created_at` `expires_at`。只有 active 的；觸發／過期／刪除後就沒了 | A §18、§49 |
| A-34 | 刪除，確認後果（X-08） | A §16、§49 |
| A-35 | 不可建立 | A §16、§49、§58 |
| A-36 | 上限 10 個 active → 顯示 n／10（建議） | A §14 |
| A-37 | 已觸發／過期的 Event 只能在 Run 歷史裡看 → 不需要獨立的 Event 歷史頁 | A §18 |

### 3.8 監控

| # | 需求 | 來源 |
|---|---|---|
| A-38 | 用量：`input_tokens` `output_tokens` `cached_tokens` `total_tokens` `model_calls`；分模型、分 Run、Session 總計 | A §50、§51 |
| A-39 | Ledger：每筆帳戶變動 + 產生它的 `run_id`，可點到 Run 歷史。例：`BTC LONG +50U · Run #15` | A §50、§53 |
| A-40 | 部位：Session 目前的部位 | A §50、§53 |
| A-41 | Tool 統計：`tool_name` `call_count` `success_count` `error_count` `avg_latency` `max_latency` `last_latency`。latency 是第一版重點指標 | A §50、§52 |
| A-42 | PnL：Session 層級。規格沒定義口徑，前端直接顯示 server 給的值 | A §44、§45、§53 |

## 4. LLM Provider Server（L）

### 4.1 Provider

| # | 需求 | 來源 |
|---|---|---|
| L-01 | 列表：`id` `name` `type` `base_url` `default_model` `config` `enabled` `created_at` `updated_at` | L §9、§10 |
| L-02 | 建立：`id`（使用者自己取，例 `deepseek-main`；之後 Session 就靠它找 Provider）`name` `type` `base_url` `default_model` `config` `enabled` | L §9、§10、§14 |
| L-03 | `type` 分兩類：LLM API（`openai` `anthropic` `gemini` `openai_compatible`）與 Web Harness（`codex` `claude_code`）。Harness 的 `config` 只有 `web_search`；要讓使用者知道 Harness 只做推理 + web search／fetch，不會執行任何工具、shell、檔案 | L §2、§19 |
| L-04 | 編輯任何欄位 | L §10 |
| L-05 | 刪除，確認後果（X-08） | L §10 |
| L-06 | 啟用／停用（停用後 `PROVIDER_DISABLED`） | L §9、§22 |
| L-07 | `openai_compatible` 加新家不用改碼，填 `base_url` 就好 → 表單就是全部，不需要 vendor 清單 | L §17、§26 |

### 4.2 Token

| # | 需求 | 來源 |
|---|---|---|
| L-08 | 每個 Provider 底下多個 token：`id` `name` `masked` `enabled` `created_at` `updated_at` | L §11、§12 |
| L-09 | 新增：`name` + 明文 token，只在這一次輸入；成功後只回 `masked`（`sk-****9Ab2`）。要講明「送出後永遠看不到明文」 | L §12、§13 |
| L-10 | 改名、啟用／停用、換 secret。換 secret 同樣只輸入一次 | L §12 |
| L-11 | 刪除 | L §12 |
| L-12 | 前端不保存、不快取明文：送出後立刻清掉輸入 | L §13 |
| L-13 | 多個 token 怎麼被選用規格沒說；`TOKEN_UNAVAILABLE` = 沒有可用 token → 至少顯示每個 Provider 目前有幾個啟用的 token（建議） | L §11、§22 |

### 4.3 其他

| # | 需求 | 來源 |
|---|---|---|
| L-14 | `GET /healthz` 只代表服務活著，不打各 Provider → 畫面不要暗示「Provider 可用」 | L §23 |
| L-15 | `GET /v1/models` 回應未定義 | L §21；待定 3 |
| L-16 | 沒有「測試連線」端點 → 不畫測試按鈕 | L §23、§25 |
| L-17 | L 的錯誤碼不會直接出現在控制台（控制台不叫 `/v1/generate`，那是 Go runtime 用的），可能出現在 Run 的 `error` 裡。Run 明細顯示錯誤時要能對應：`PROVIDER_NOT_FOUND` `PROVIDER_DISABLED` `PROVIDER_INVALID_CONFIG` `TOKEN_NOT_FOUND` `TOKEN_UNAVAILABLE` `AUTHENTICATION_FAILED` `MODEL_NOT_FOUND` `REQUEST_INVALID` `RATE_LIMITED` `PROVIDER_TIMEOUT` `PROVIDER_UNAVAILABLE` `PROVIDER_RESPONSE_INVALID` `INTERNAL_ERROR` | L §1、§22；A §27 |

## 5. 待定（規格沒定、要先決定才能設計）

| # | 問題 | 規格現況 | 影響前端哪裡 |
|---|---|---|---|
| 1 | 三個服務的認證 | B 只有 USER Token（B §1；F §4）；A 沒有認證章節；L §21 只說 generate 與管理權限要分開，沒說機制 | 登入頁收幾個 token、殼存幾個、401 怎麼處理 |
| 2 | Session ↔ 交易帳號 | A §23 說 Trading Tools 直接映射交易後端 API、帳戶變動帶 `session_id + run_id`；B §4 說帳號由 Account Token 決定；B 的 order／trade 沒有 `session_id`／`run_id` 欄位（F §10.3） | 建 Session 表單要不要選帳號；Session 明細的帳戶摘要；帳號列表「被誰用」（T-05）；Ledger 怎麼對到 Run（A-39） |
| 3 | `model_name`／`model_level` 合法值 | A §4、§55 由 `model_name` 解析 Provider；L §18 Provider 有 `default_model`；L §21 有 `GET /v1/models` 但沒定義回應 | 建 Session 是下拉還是手填；填錯只能靠 400 |
| 4 | Server Default 能不能查 | A §4 只說有 default | 建 Session 表單能否顯示預設 prompt／`max_loop`／`max_tool_call` |
| 5 | Agent Server 健康端點 | 沒有 | 殼的狀態燈 |
| 6 | USER 看帳號 `equity`／`available` | 只有 Account Token 自查 `GET /v1/account`（F §10.2 D）；F §11 已列「後端待補」 | 帳號明細用代打拿，還是後端補 USER 端點 |
| 7 | `restart_context`、`pending_triggers` | A §37、§40 只在 DB；沒有端點 | Session 明細能否顯示「上次中斷在哪」「幾個 trigger 在排隊」 |
| 8 | 留言板分辨人／Agent | B §18 不回 `author_type` | 靠名字，或後端加欄位 |
| 9 | Session 列表的 PnL／account summary／runtime health | A §44 只列名字 | 總覽表格欄位 |
| 10 | Run 歷史 shard 大小 | A §33 每 20 Run 一檔，含 tool trace | 前端快取策略、大檔載入體驗 |
| 11 | 三個服務的位址 | 交易後端與 LLM Server 都用 `/v1`，同源反向代理會撞（F §8） | 前端要三個 base URL，或代理加前綴 |

## 6. v1 明確不做（前端不要畫）

| 不做 | 來源 |
|---|---|
| 多人、角色、權限、Login／Register／OAuth；OIDC 是之後 | B §1；F §4 |
| 手動建 Event、手動建／改 Memory、改 Session、restore Session | A §58 |
| 多 Agent、workflow、同一 Session 併發 Run | A §58 |
| Order book | A §22、§58 |
| Server 端 Session 篩選／排序 | A §43、§58 |
| Model CRUD、自動 Provider routing、測試連線 | L §25、§23 |
| 即時推播（沒有 WS／SSE） | 三份規格都沒提 |
| US Stocks 行情（目前 501） | F §10.4 |
| 獨立的 Event 歷史頁（看 Run 歷史） | A §18 |

## 7. 名詞對照

設計時要知道每個值對使用者的意思；顯示文字自己定。

| 欄位 | 值 | 意思 | 來源 |
|---|---|---|---|
| Session `status` | `stopped` `running` `deleted` | 不跑、會被 Event 叫醒、軟刪不可復原 | A §3.1 |
| Run `status` | `running` `completed` `interrupted` `failed` | 進行中、正常結束、被 stop／crash 打斷、超限或錯誤 | A §8 |
| Run trigger | 見 A-27 | 為什麼醒來 | A §17、§20 |
| Event `type` | `timer` `price_above` `price_below` `price_cross_above` `price_cross_below` `price_change_pct` | Agent 在等什麼 | A §17 |
| Memory `importance` | `1` `2` `3` | 越大越重要 | A §30 |
| 帳號 `status` | `active` `disabled` | 能交易、不能交易 | F §10.4 |
| `market` | `crypto` `stock` | stock 目前 501 | F §10.4 |
| `product` | `spot` `futures` | | F §10.4 |
| order `side` / `type` / `status` | `buy` `sell` / `market` `limit` / `open` `filled` `canceled` `rejected` | | F §10.4 |
| position `side` | `long` `short` | | F §10.4 |
| trade `role` | `maker` `taker` | 限價成交／市價、停損停利、手動平倉 | F §10.4 |
| 訂閱 state | `inactive` `activating` `active` | | B §10；F §10.4 |
| message `user_name` | 真名、`you`、`[deleted]` | 別人、自己、作者帳號已刪 | B §16；F §10.4 |
| Provider `type` | `openai` `anthropic` `gemini` `openai_compatible` `codex` `claude_code` | 前四種是 API、後兩種是 Harness | L §9 |
| 錯誤信封 | 交易後端 `{error, message}`；Agent Server `{status_code, error, msg}`；LLM Server `{error: {code, message}}` | 三種，前端要各自拆 | F §10.5；A §54；L §22 |
