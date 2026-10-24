# 群聊管理與交易帳號 API：交付驗證

交付位置：`C:\stock-ai-win-test`。已先在 Windows 執行 `stop.cmd`，再由 WSL 執行 `scripts/build-win-test.sh`。
版本：Backend `0.1.3-b0r0`、Agent Server `0.1.4-b0r0`、Frontend `0.1.4-b0r0`。資料、logs、secrets 保留。
四個 Windows 執行檔已重新建置，web 與最終 Vite 產物逐檔 SHA-256 相同。

## 逐條驗收

| 項目 | 驗證內容 | 結果 | 自動化測試／檢查名稱 |
|---|---|---|---|
| A1 | 10 分鐘禁言、時間誤差與原因 | 通過 | `TestChatMuteLifecycle` |
| A2 | 非法分鐘／空白或超長 Unicode 原因，400 且不改資料 | 通過 | `TestChatMuteValidationAndAuthorization` |
| A3 | 帳號 token 403、帳號不存在 404 | 通過 | `TestChatMuteValidationAndAuthorization` |
| A4 | 403 chat_muted、原因原文、進位分鐘與 UTC 到期時間 | 通過 | `TestChatMutedPostAndExpiry` |
| A5 | 禁言中仍可讀取 | 通過 | `TestChatMuteLifecycle` |
| A6 | 禁言到期可發言、兩個禁言欄位 null | 通過 | `TestChatMutedPostAndExpiry` |
| A7 | 解除後可立即發言、重複解除成功 | 通過 | `TestChatMuteLifecycle` |
| A8 | 重複禁言覆蓋期限與原因 | 通過 | `TestChatMuteValidationAndAuthorization` |
| A9 | USER 不受禁言限制 | 通過 | `TestChatPostingRestrictionOrderAndUserExemption` |
| B1 | 新帳號及舊資料遷移後為 1 分鐘／3 則 | 通過 | `TestChatRateDefault；TestChatManagementLegacyMigration` |
| B2 | 第 1～3 則 201、第 4 則 429，訊息含額度及等待時間 | 通過 | `TestChatRateDefault` |
| B3 | 窗口外舊訊息不擋新發言 | 通過 | `TestChatRateWindowDeletionAndIsolation` |
| B4 | 改為 2 分鐘／1 則即採新規則、範圍驗證 | 通過 | `TestChatRateWindowDeletionAndIsolation；TestChatRateValidation` |
| B5 | 已刪訊息不計數 | 通過 | `TestChatRateWindowDeletionAndIsolation` |
| B6 | 帳號各自計數、USER 不受限 | 通過 | `TestChatRateWindowDeletionAndIsolation；TestChatPostingRestrictionOrderAndUserExemption` |
| B7 | 檢查順序：永久發言權限 → 禁言 → 頻率 | 通過 | `TestChatPostingRestrictionOrderAndUserExemption` |
| C1 | post_chat 保留原始 error/message/outcome | 通過 | `TestPostChatPreservesModerationErrorsAndWakeUpShape` |
| C2 | 喚醒資料不新增禁言／頻率欄位 | 通過 | `TestPostChatPreservesModerationErrorsAndWakeUpShape` |
| D1 | 列出全部帳號、名稱、唯讀狀態、權限與頻率 | 通過 | `成員管理列出全部帳號、狀態、權限與頻率` |
| D2 | 兩欄必填、整數範圍、POST body、立即顯示禁言 | 通過 | `禁言對話框兩欄必填、整數與範圍驗證，POST 後立即顯示並可 DELETE 解除` |
| D3 | 解除送 DELETE 並恢復按鈕 | 通過 | `禁言對話框兩欄必填、整數與範圍驗證，POST 後立即顯示並可 DELETE 解除` |
| D4 | 失焦或 Enter 只 PATCH 一次，失敗顯示錯誤並復原 | 通過 | `頻率失焦或 Enter 才 PATCH 一次，只含改動欄位；後端失敗復原` |
| D5 | 30 秒倒數更新、到期恢復、5 秒更新帳號 | 通過 | `每 30 秒更新剩餘分鐘，到期自動顯示禁言按鈕；每 5 秒更新帳號清單` |
| D6 | 桌面側欄、900px 以下置頂／可收合、長名稱及訊息不溢出 | 通過 | `TestChatManagementResponsiveGeometry` |
| D7 | 既有發言、tag、刪除、選取、輪詢全部通過 | 通過 | `frontend/test/messages.test.js；前端完整 vitest` |
| E1 | 17 支帳號端點與 14 支 USER 管理端點逐項對應 router.go | 通過 | `TestAccountApiManualRoutes` |
| E2 | 參數與錯誤對照來源、JSON 範例欄位實際核對 | 通過 | `TestAccountApiCurlExamples（逐一執行請求並核對回應 shape）；來源審查` |
| E3 | 全部 18 個 curl 實跑且保存輸出 | 通過 | `TestAccountApiCurlExamples` |
| E4 | Python 查價／下單／停損停利／查持倉／平倉／發言流程 | 通過 | `TestAccountApiPythonFlow；TestAccountApiPythonRejectsExistingPosition` |
| F1 | 四個 Go module test/vet、vitest、vite build、gofmt | 通過 | `go test ./... ×4；go vet ./... ×4；vitest run；npm run build；gofmt -l` |
| F2 | 版本與依賴行相容 | 通過 | `scripts/check_versions.py` |
| F3 | 無新第三方套件、未改 middleware 程式／nginx／部署設定 | 通過 | `TestChangeScope` |
| F4 | 只修改需求相關程式、公開欄位僅新增 | 通過 | `TestChangeScope；TestPublicApiFieldsRemainAdditive` |

額外回歸：`TestChatRateConcurrentPosts` 驗證 10 個同時發送請求只有 3 個成功；
`延遲的篩選重載不會覆蓋剛成功的禁言狀態` 驗證管理結果不被舊回應蓋掉；
`多欄位確認擴充保留既有單欄位輸入與一般確認的回傳值` 驗證原有 confirm 使用方式。
窗口的數量與最早時間由同一 SQL snapshot 查出，避免 PostgreSQL 刪除交錯導致假 404。

## 執行結果

| 檢查 | 結果 |
|---|---|
| Backend `go test ./...` | 378 通過 |
| Agent Server `go test ./...` | 428 通過 |
| LLM Provider `go test ./...` | 271 通過 |
| middleware `go test ./...` | 115 通過 |
| 四個 module `go vet ./...` | 全部成功、無輸出 |
| Frontend `vitest run` | 20 個測試檔、107 個測試通過 |
| `npm run build`（vite build） | 成功 |
| `gofmt -l backend agent_server llm_provider_server middleware` | 無輸出 |
| `scripts/check_versions.py` | 成功 |
| 手冊 curl | 18／18 通過 |
| 手冊 Python | 完整流程通過；已有 BTCUSDT 部位時會在下單前明確拒絕 |
| Windows Chrome 排版 | 桌面與窄畫面：位置、收合、長名稱／長訊息不溢出通過 |
| 環境清理 | Linux smoke 的 Backend／middleware／行情 fixture 全部停止，三個 port 已釋放 |

Backend 原有 PostgreSQL 測試需要 `BACKEND_TEST_POSTGRES_DSN`；本機未設定時會由原測試自行略過。
本次新增行為以 SQLite、實際 Linux HTTP 服務與 Windows Chrome 驗證；不將此結果宣稱為已執行 PostgreSQL 整合測試。

## 證據與重跑方式

- [API 手冊](account-api.md)：完整 17 個端點、參數、回應、錯誤與 Python 流程。
- [全部 curl／Python 執行輸出](account-api-smoke.txt)：每次請求的指令、HTTP 狀態與完整回應，含帳號 token 打 USER／Agent／LLM 的 403。
- [瀏覽器尺寸與排版結果](chat-management-geometry.json)。
- [Windows 測試包 SHA-256 與版本](windows-test-package.json)。

從 repo 根目錄：

```bash
python3 backend/test/account_api_smoke.py --baseline 040666e5410e8ae87cacd0f2d1188f127d28aabf --windows-package /mnt/c/stock-ai-win-test
```

瀏覽器驗證：先在 frontend 執行 `npm run build`，再執行 `node test/chat-management-geometry.mjs`。
使用已安裝的 Windows Chrome；其他位置可設定 `CHROME_PATH`，不需新增套件。

API 來源已逐一讀取核對：`backend/internal/api/{router,identity,middleware,account,market,trading,message,views}.go`、
`backend/internal/trading/{validate,trading,stops,summary}.go`、`backend/internal/market/{market,read}.go`、
`backend/internal/market/crypto/read.go`、`backend/internal/message/message.go`、`backend/internal/database/{models,message}.go`。
例如平倉 quantity <=0 依現有實作為全平，手冊照實記錄，未變更交易規則。

## 審查

Standards：未發現需要修正的規範問題。
Spec：初次發現的舊回應覆蓋狀態、窗口查詢刪除競態、Python 既有持倉前提與含訊息排版均已修正並補驗證。

本機提交於 master；未推送。既有部署／promotion 修改與其他未追蹤工作均保留，未加入本次提交。
