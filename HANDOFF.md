# HANDOFF — Stock-ai-new 重構與修復任務交接檔案

> 產生時間：本次 session（llm_provider_server 重構剛完成並提交 a0a65fc 之後）
> 用途：完整交接所有任務、goal、狀態、環境事實與下一步，供後續 session/agent 直接接手。

---

## 0. Goal（同一 session 的 completion goal）

- **goal_id**: `goal-09306b74-aee6-4394-be6a-d80477443561`（接手後先 get_goal 取回最新 revision 再 update_goal）
- **phase**: active，rounds 1/40
- **objective**（原文）：
  "Refactor llm_provider_server & agent_server into clean modular architecture per their spec.md (no functional change, all tests must pass); fix listed issues (OHLCV candles to model, get_ohlcv time API, place_order SL/TP + position_id, tool list prompt, summary retry on limit, session-monitoring/run-detail page layout, missing run download, terminal logging, codex provider consistency); finish with full ticket-vs-implementation consistency report for user review."

---

## 1. 原始需求（用戶，繁體中文）

1. 整個代碼庫除了 backend & frontend，剩下的 **llm_provider_server、agent_server 沒有完全按照各自 spec.md 實作**。
2. 功能先不做太多改動；當前目標 = **整理優化代碼結構**：模塊化、像積木一樣拼起但互不耦合。
3. **最終驗收：當前全部功能不受影響 + 檔案與程式碼架構模塊化 100%**。
4. 另有分級修復清單（見 §4 SEV/MED/HARD）。
5. 遵守根目錄 AGENT.md（21 條規則：最小層/檔案、無責任的 wrapper 不行、禁止循環依賴、依賴單向、刪除舊代碼、無 compat shim）。注意：AGENT.md 裡有一段用戶貼入的無關 "rtk" 內容 — **不要刪除也不要理會**。

---

## 2. 狀態總覽

| 工作流 | 狀態 | 驗收門 |
|---|---|---|
| agent_server 重構 | 完成（已提交） | **206 PASS / 0 FAIL** |
| llm_provider_server 重構 | 完成（已提交 a0a65fc） | **104 PASS / 0 FAIL** |
| SEV：OHLCV / get_ohlcv / place_order | 未開始（已定位代碼位置，見 §4.1） | backend+agent 測試 |
| 最簡單：tool list disclosure | 部分驗證（harness prompt path 未驗證） | — |
| MED：retry / frontend 佈局 / run-download / terminal logging | 未開始 | 前端手動 + backend 測試 |
| HARD：codex harness 一致性 vs ticket 12 | 未開始 | — |
| 最終：40-ticket 一致性報告 | 未開始 | 報告交用戶 review |

（圖例：✅=完成 ⬜=未完成；表中文字已省略符號。）

---

## 3. 已完成工作細則

### 3.1 agent_server（模塊化完成）

- 根 package 已拆分；結構性完成，gate 206 PASS / 0 FAIL 已提交。
- 關鍵約束（接手後不可破壞）：
  - completeFinalizedRun 留在 agent package。
  - eventRunLaunch / startPendingRunTx / launchRunLocked / dispatchPendingLocked / runTriggers 留在根層。
  - events package 的 API surface 不變。
- 基線 commit 98e7a2a = 重構前 agent 206 PASS 狀態；需要原始檔案時 git show 98e7a2a:<path>。

### 3.2 llm_provider_server（模塊化完成，commit a0a65fc）

最終結構（root 只剩 go.mod / go.sum / spec.md / model-management.md）：

- internal/providers/ — 純適配器 + wire 型別（無外部依賴）
  - wire.go：GenerateRequest/Response, Message, Usage, ToolCall, ProviderCall（URL/Headers/Body 已導出）, ProviderError（Status/Code/Message 已導出）, 校驗 helpers
  - compatible.go：CompatibleCall(providerType, baseURL, model string, ...) — 已與 gateway 的 Provider struct 解耦
  - anthropic.go：AnthropicCall / NormalizeAnthropicResponse
  - nativegemini.go：GeminiCall / NormalizeGeminiResponse
- internal/webharness/ — CLI harness 領域 + Claude 有界 session store
  - harness.go：CodexWebHarness / ClaudeCodeWebHarness, WebHarnessRequest/Response, Continuation, HarnessErrorCategory, HarnessDetail, HarnessFileAvailable, ReadHarnessLogin
  - process_windows.go / process_unix.go / process_other.go：平台 process 處理（//go:build tag 在檔首）
  - sessions.go：ClaudeSessions（NewClaudeSessions / GenerateClaudeSession / Stop，自帶 GC）
- internal/gateway/ — HTTP Server + token 加密 + model catalog + harness dispatch
  - gateway.go：Server（含 claudeSessions *webharness.ClaudeSessions；無 harness 執行檔欄位，改每請求 harnessPath()）, Open/Close, 路由, token 存取
  - harness_gateway.go：generateHarness / harnessStatus / testProvider
  - model_catalog.go：/v1/model-catalog 管理
- cmd/llm-provider-server/main.go — import gateway "stock-ai/llm-provider-server/internal/gateway"
- test/ — 9 個 .go 測試檔全部改 import（104 PASS）

依賴方向（單向、無環）：gateway -> {providers, webharness} -> providers。

Server 生命週期：Open 建立 claudeSessions = webharness.NewClaudeSessions(harnessPath("LLM_SERVER_CLAUDE_EXECUTABLE"), harnessSessionTTL)；Close 呼叫 s.claudeSessions.Stop()。原來的 collectHarnessSessions / removeAllHarnessSessions / harnessGC 等 8 個 *Server 方法已隨 session 狀態移入 webharness 後刪除。

test/ 更新：6 檔只改 import path；claude_harness_test.go + codex_harness_test.go 改為 webharness import 且所有 gateway.X 改 webharness.X；harness_http_test.go 同時保留 gateway import 並新增 webharness import（只用於 WebHarnessMessage/WebHarnessToolCall）。

重構過程的抽取腳本（可重跑、冪等）：.scratch/extract_llm.ps1
- 以 root 原始檔案為 source（現在 root 已刪；重跑腳本前需先 git show 7bd0d84:llm_provider_server/<file> 還原 9 個 root 檔）。
- 所有「手動編輯」已編譯進腳本 post-step（CompatibleCall 簽名、gateway 呼叫點、ProviderError/ProviderCall 欄位導出、Server/Open/Close、generateHarness/harnessStatus 的 harnessPath 化）— 重跑不會再被洗掉。
- 完整性檢查：expected-exclusion 清單 = web_harness.go 的 8 個 *Server session-method blocks + harness_gateway.go:19:23。
- 若日後需要重新生成：還原 root 檔 -> 跑腳本 -> gofmt -w internal -> go build ./...。

其他腳本：.scratch/dump_blocks.ps1（0-based block 邊界 dump）、.scratch/cutover_llm.ps1（一次性 cutover，已執行過）。

### 3.3 Git 狀態

- HEAD = a0a65fc（llm 重構）<- 7bd71a8（agent runtime 拆分）<- 2c3545f（agent 根 package 改名）<- ... <- 98e7a2a（重構前基線）。
- untracked：CLAUDE.md、本 HANDOFF.md。
- 每次結構變更後都要跑對應 gate（§5）。

---

## 4. 剩餘任務（依優先序）

### 4.1 SEV（阻塞功能）

SEV-1：OHLCV candles 到不了模型 + get_ohlcv 未來時間區間壞掉
- Root cause（已確認）：模型不知道當前時間 -> 模型請求未來時間區間的 candles -> backend 過濾未完成 candle（!candle.CloseTime.Before(now) filter）-> 回傳 candle_count: 0。
- 修復位置：backend/internal/market/read.go:96 的 GetOHLCV(ctx, marketName, symbol, interval, start, end, limit)（上游 Binance 實現在 backend/internal/market/crypto/read.go:20）。
- 修復規格：start/end 改為可選 -> 缺省取最新 N 根；end 若為未來則 clamp 到 now；回應包含 current_time（讓模型知道現在幾點）。
- 相關：agent_server 的 get_ohlcv tool、ticket 19 spec（.scratch/agent-platform-v1/issues/19-ohlcv-tool.md）、backend/test/ticket19_ohlcv_test.go。

SEV-2：place_order 必須帶 SL/TP 參數（預設空）並回傳 position_id
- agent tool：agent_server/internal/tools/trading.go。
- backend POST /v1/orders 的 projection — 先查 backend/internal/api/views.go 再改。
- 相關 tickets：22/23/24（backend/test/ticket22_*、ticket23_*、ticket24_stops_test.go）。

最簡單：tool list disclosure prompt
- 確保所有送模型的 prompt 路徑都揭露工具清單；harness prompt 路徑尚未驗證（webharness 的 request 組裝處）。

### 4.2 MED

1. finalization 失敗時 retry 3–5 次（forced summary 失敗重試）。
2. frontend session-monitor 佈局（frontend 端）。
3. run-detail 無界成長 + ticket 頁缺少 run-download（frontend）。
4. terminal logging：logOperational 在 logger 為 nil 時直接丟棄 — 檢查 main.go 的 wiring，確保 log 會印到終端。

### 4.3 HARD

- codex harness 一致性檢查 vs ticket 12：對照 .scratch/agent-platform-v1/issues/12-codex-web-harness.md 與 llm_provider_server/internal/webharness/（CodexWebHarness、process 檔、版本 pin、受限 invocation）+ llm_provider_server/test/codex_harness_test.go。

### 4.4 最終驗收

- 40-ticket vs 實作一致性報告：對照 .scratch/agent-platform-v1/issues/01..40（40 個 issue 檔）與實際實作（含 backend/frontend 相關項），產出完整報告交用戶 review -> 之後可 update_goal action complete。

---

## 5. 驗收門（每次結構變更後必跑）

- agent_server：go build ./... ；go vet ./... ；gofmt -l . ；go test ./test -count=1 -> 期望 206 PASS / 0 FAIL
- llm_provider_server：同上（在 llm_provider_server/ 目錄）-> 期望 104 PASS / 0 FAIL
- backend / frontend：build + 各自測試 -> 不回歸
- 數 PASS 的方式：go test ./test -count=1 -v 輸出數 "^--- PASS" / "^--- FAIL"。
- build 不會編譯 test — 一定要跑 go test。
- 每個 go 指令前先設 GOCACHE（見 §6）。

---

## 6. 環境事實（重要，踩過的坑）

- Windows；workspace = D:\GO PROJ\Stock-ai-new ；Go 1.24.4 windows/amd64；PowerShell 7.6.6。
- 每個 go 指令都要：$env:GOCACHE='D:\GO PROJ\Stock-ai-new\.scratch\gocache'
- go 輸出會混入 telemetry "upload token" 雜訊 — 過濾：Where-Object { $_ -notmatch 'upload token' }
- 此環境 PowerShell 7.6.6 無法解析 here-string（絕對不要用）。
- 不要用 inline pwsh -Command 帶 $vars 或 backtick escapes（backtick 會被吃掉）-> 寫 .ps1 檔再用 -File 執行。
- PS 的 -match 預設大小寫不敏感 — 需要大小寫敏感時用 [regex]::IsMatch（llm 重構就因為這題把 net/url 誤導入 model_catalog.go）。
- 字串內的 tab/newline 在 PS 雙引號字串用 backtick-t / backtick-n 轉義 — 寫進 .ps1 時最容易把 backtick-t 打成字面 \t，務必核對。
- DSH Web GUI：http://127.0.0.1:3080（本 session 的 GUI；不要另起 server）。
- File sandbox = workspace-write；approval policy = ask。
- run_code 內的 JS：PS backtick 會破壞 JS template literal -> 用 line-array join 或 JS single-quoted string（本檔就是教訓）。

---

## 7. 重構方法學（已驗證的教訓）

1. word-boundary 重命名會破壞：(a) 同名 local var/param、(b) struct 欄位名、(c) JSON string key（wire format）、(d) 欄位存取（aliases.wireByRuntime）。編譯器抓得到 (a)，抓不到 string 破壞 -> 必須 string-aware 拆字（Apply-Renames 只在 code token 上改，保護字串字面量）+ grep 複核。
2. OrderedDictionary 順序陷阱：set 已存在的 key 會原地更新（保留原位置）— 廣義 key 先插入、之後又被 loop 改值時，其位置仍在前面；targeted a.b key 必須在廣義 key 之前定義。
3. 整檔搬移要處理：檔首 //go:build tag（抽出來放到 header 前）、重複 package 行、import 區塊移除。
4. import 自動推斷（Build-Header）：以「body 內出現 pkg. 前綴」判斷，需支援 alias（sqlite "github.com/glebarez/go-sqlite"）且大小寫敏感。
5. 編譯器 + grep + gofmt + 完整測試是四道安全網；腳本保持冪等（source 固定、所有手動編輯編進腳本）才能放心重跑。

---

## 8. 關鍵檔案索引

- .scratch/extract_llm.ps1 — llm 抽取腳本（冪等；重跑前需還原 root 檔）
- .scratch/dump_blocks.ps1 — 0-based block 邊界 dump
- .scratch/cutover_llm.ps1 — 一次性 cutover（已執行）
- .scratch/agent-platform-v1/issues/NN-*.md — 40 張 ticket spec（最終報告的對照基準）
- .scratch/agent-platform-v1/HANDOFF-STATUS-2026-09-15.md、PROGRESS.md、各 WORKER-*-HANDOFF.md — 歷史 handoff/進度
- llm_provider_server/spec.md、agent_server/spec.md — 兩 server 的 spec（重構依據）
- backend/SPEC.md、frontend/SPEC.md、frontend/REQUIREMENTS.md — 其他組件 spec
- AGENT.md（根）— 21 條代碼結構規則
- backend/test/ticket19_ohlcv_test.go 等 — ticket 對應測試
- llm_provider_server/test/（9 檔）、agent_server/test/（206 個 test case）— 兩 server 測試

---

## 9. 下一步（接手後照順序做）

1. SEV-1 OHLCV：讀 backend/internal/market/read.go（GetOHLCV，:96）+ backend/internal/market/crypto/read.go + agent_server 的 get_ohlcv tool + issues/19-ohlcv-tool.md -> 實作：start/end 可選（缺省最新 N）、end clamp to now、回應加 current_time -> 跑 backend + agent 測試。
2. SEV-2 place_order：查 backend/internal/api/views.go 的 POST /v1/orders projection -> agent tool 帶 SL/TP（預設空）+ 回傳 position_id -> 跑 ticket22/23/24 相關測試。
3. 驗證 tool list disclosure 在所有路徑（含 harness prompt path）。
4. MED 批次（retry 3–5、frontend session-monitor/run-detail/run-download、terminal logging wiring）。
5. HARD：codex harness vs ticket 12 一致性。
6. 全量套件（agent 206 / llm 104 / backend / frontend）+ 產出 40-ticket 一致性報告 -> 交用戶 review -> goal complete。
