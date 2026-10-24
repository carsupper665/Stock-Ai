# 群聊與成員管理改版：交付驗證

計畫：群聊與成員管理改版（禁言、頻率限制、即時推播、API 手冊）。提交於 `master`：`43e4f24`、`3f05a71` 與本文件所在的提交，未推送。
版本：Backend `0.1.3-b0r0`、Agent Server `0.1.4-b0r0`、Frontend `0.1.4-b0r0`、middleware 不變（`0.1.2-b0r0`）；
這幾個版本號在上一版（`425e843`）已調整且尚未推送，所以這次沒有再改。

win-test：先在 Windows 執行 `stop.ps1`（`stop.cmd` 的內容），再由 WSL 執行 `scripts/build-win-test.sh`，最後 `start.ps1`。
四個服務健康檢查皆 200；data、logs、secrets 保留。

## 逐條驗收

| 項目 | 內容 | 結果 | 自動化測試 |
|---|---|---|---|
| A1 | 禁言 10 分鐘回 200，`chat_muted_until`≈now+10 分（5 秒內）、原因正確 | 通過 | `TestChatMuteLifecycle` |
| A2 | minutes 0／43201／小數／缺少、reason 空／純空白／超過 200 字回 400，帳號不變 | 通過 | `TestChatMuteValidationAndAuthorization` |
| A3 | 帳號 token 403、帳號不存在 404 | 通過 | `TestChatMuteValidationAndAuthorization` |
| A4 | 禁言中發言 403 `chat_muted`，含原因原文、「剩餘 10 分鐘」與 UTC 到期時間 | 通過 | `TestChatMutedPostAndExpiry` |
| A5 | 禁言中讀取 200 | 通過 | `TestChatMuteLifecycle` |
| A6 | 到期後可發言、兩個欄位回 null（把儲存的到期時間移到過去來模擬時鐘） | 通過 | `TestChatMutedPostAndExpiry` |
| A7 | 解除後立即可發言、解除冪等、重複禁言覆蓋 | 通過 | `TestChatMuteLifecycle`、`TestChatMuteValidationAndAuthorization` |
| A8 | USER 發言不受影響 | 通過 | `TestChatPostingRestrictionOrderAndUserExemption` |
| B1 | 新帳號與遷移後舊帳號為 1 分鐘／3 則 | 通過 | `TestChatRateDefault`、`TestChatManagementLegacyMigration` |
| B2 | 第 4 則 429，含「每 1 分鐘最多 3 則」「約 1 分鐘後」；窗口過後可再發 | 通過 | `TestChatRateDefault`、`TestChatRateWindowDeletionAndIsolation` |
| B3 | 每 2 分鐘 1 則時第 2 則被擋；範圍外 400 | 通過 | `TestChatRateWindowDeletionAndIsolation`、`TestChatRateValidation` |
| B4 | 刪除的不計、帳號各自計算、USER 不受限 | 通過 | `TestChatRateWindowDeletionAndIsolation`、`TestChatPostingRestrictionOrderAndUserExemption` |
| B5 | 三種條件同時成立回 `chat_post_disabled`，順序固定 | 通過 | `TestChatPostingRestrictionOrderAndUserExemption` |
| C1 | `limit` 1～50，超出 400；預設 10；原有測試全過 | 通過 | `TestListMessagesLimit`、`TestListMessagesQueryValidation`、`TestFixedPaginationSortAndTagFilter` |
| C2 | `before` 可配 tag、sort；與 after 併用 400；id 不存在 400 | 通過 | `TestListMessagesBefore` |
| C3 | `DELETE /v1/messages` 只有 USER（帳號 403、匿名 401）；清空後列表與 tag 查詢都是空的 | 通過 | `TestClearMessages` |
| C4 | SSE：發言、刪除、清空各收到一次事件，created 與列表單筆完全相同；帳號 token 403 | 通過 | `TestMessageStreamEvents` |
| C5 | 斷線後移除訂閱、goroutine 回到基準數；緩衝滿就中斷；關機結束所有連線 | 通過 | `TestMessageStreamReleasesDisconnectedClients`、`TestStreamDropsSlowAndUnsubscribedClients`、`TestCloseStreamsEndsEveryClient` |
| C6 | 經 middleware 代理的 SSE 事件 1 秒內送達 | 通過 | `TestProxyForwardsTheChatStreamWithinOneSecond` |
| D1 | `post_chat` 遇 `chat_muted`／`chat_rate_limited` 原樣回 `{"ok":false,"error":{code,message,outcome:"not_executed"}}` | 通過 | `TestPostChatPreservesModerationErrorsAndWakeUpShape` |
| D2 | 清空後 `get_chat`（不帶 page）退回讀最新訊息 | 通過 | `TestGetChatAfterChatClearedReadsNewestAgain` |
| E1 | members.js：名稱與小字、三種狀態與排序、未綁分組、已刪除不列出但訊息顯示「被刪除用戶」、USER 名稱與標籤 | 通過 | `frontend/test/messages-members.test.js`（5 個測試） |
| E2 | 點成員與右鍵「管理使用者」開同一個彈窗；權限（停權要確認）、禁言兩欄必填、解除、頻率失焦或 Enter 只送改動欄位；錯誤顯示並還原 | 通過 | `E2 點成員與右鍵「管理使用者」…`、`E2 彈窗的權限…` |
| E3 | 「@ 他」加進輸入框 tag，送出時帶上 | 通過 | `E3 「@ 他」把成員加進輸入框的 tag 並關閉彈窗` |
| F1 | 第一次只發一次 `limit=15`；捲到頂才發 `before=…&limit=15` 且位置不跳；離開再回來不重抓 | 通過 | `F1 第一次只發一次 limit=15…` |
| F2 | created／deleted／cleared 下一次 render 就更新；重連只發一次 `after=`；連線被拒才自己重連；沒有 setInterval | 通過 | `F2 SSE 事件下一次 render 就更新…` |
| F3 | 在底部才跟著捲；不在底部時位置不變；自己發言回到底部 | 通過 | `F3 原本在底部才跟著新訊息捲…` |
| F4 | 搜尋只過濾已載入的並顯示提示；「@我」正確 | 通過 | `F4 搜尋只過濾已載入的訊息…` |
| F5 | @你 強調樣式；未讀捲進畫面就減少；點未讀跳最舊一則 | 通過 | `F5 @你 有強調樣式…` |
| F6 | 打 @ 出選單、打字篩選、上下鍵／Enter／Esc | 通過 | `F6 打 @ 跳出成員選單…` |
| F7 | 單則刪除（含失敗保留）、多選刪除（含部分失敗）、清空要輸入「清空」；清空後快取清空 | 通過 | `F7 單則刪除、多選刪除與清空…` |
| F8 | 900px 以下只顯示聊天，「<」開側欄抽屜、「…」列出成員；6 種寬度無水平捲軸、頁高不超過螢幕 | 通過 | `TestChatLayoutGeometry`（`frontend/test/chat-layout-geometry.mjs`，Windows Chrome） |
| F9 | 新增的 .vue 檔沒有 `#` 色碼、rgb()、hsl() | 通過 | `F9 群聊新增的 .vue 檔沒有寫死的 # 色碼…` |
| G1 | 手冊端點與 router.go 一致：17 支帳號端點、16 支 USER 專用 | 通過 | `TestAccountApiManualRoutes` |
| G2 | 參數、列舉、錯誤碼對照原始碼；範例回應欄位與實際回應核對 | 通過 | `TestAccountApiCurlExamples`（核對回應 shape）＋人工對照來源 |
| G3 | 19 個 curl 實跑 | 通過 | `TestAccountApiCurlExamples: 19/19`，輸出見 [account-api-smoke.txt](account-api-smoke.txt) |
| G4 | Python 範例完整流程 | 通過 | `TestAccountApiPythonFlow`、`TestAccountApiPythonRejectsExistingPosition` |
| H1 | 四個 Go module test／vet、gofmt、vitest、vite build | 通過 | 見下方「執行結果」 |
| H2 | 版本檢查 | 通過 | `scripts/check_versions.py` |
| H3 | 不新增第三方套件 | 通過 | `TestChangeScope`（go.mod／go.sum／package.json 無變動） |
| H4 | 公開 API 欄位只新增 | 通過 | `TestPublicApiFieldsRemainAdditive` |
| H5 | 不改動無關程式 | 通過 | `TestChangeScope`；既有的 deploy／promotion 未提交修改保留未動 |

## 執行結果

| 檢查 | 結果 |
|---|---|
| Backend `go test ./...` | 385 通過 |
| Agent Server `go test ./...` | 429 通過 |
| LLM Provider `go test ./...` | 271 通過 |
| middleware `go test ./...` | 116 通過 |
| 四個 module `go vet ./...` | 無輸出 |
| `gofmt -l` | 無輸出 |
| Frontend `vitest run` | 21 個測試檔、110 個測試通過 |
| `vite build` | 成功 |
| `scripts/check_versions.py` | 成功 |
| `scripts/test`（含 nginx 範本測試） | 全部通過（2 個依環境略過） |
| 手冊 smoke（含 `--baseline 040666e --windows-package`） | 13 項 PASS，見 [account-api-smoke.txt](account-api-smoke.txt) |

## 新增的 curl 範例輸出

```
curl 18: curl -fsS "$BASE/v1/messages?before=$MESSAGE_ID&limit=15" -H "Authorization: Bearer $TOKEN"
HTTP 200
{"has_more": false, "messages": [], "page": 1}
PASS AccountTokenForbidden DELETE /v1/messages: HTTP 403 {"error": "forbidden", "message": "這個端點只有 USER Token 可以使用"}
PASS AccountTokenForbidden GET /v1/messages/stream: HTTP 403 {"error": "forbidden", "message": "這個端點只有 USER Token 可以使用"}
```

## win-test 試用截圖

在 win-test 包（`http://127.0.0.1:24180/messages`）用 Windows Chrome 實際操作；只做不改資料的操作（開彈窗、打「@」不送出），
因為測試資料裡的 TEST Session 正在執行，tag 它會送進它的 Session 對話。每次開頁 `/v1/messages/stream` 都回 200 `text/event-stream`。

- 寬螢幕：[亮色](chat-screenshots/wintest-wide-light.png)、[暗色](chat-screenshots/wintest-wide-dark.png)
- 管理彈窗：[亮色](chat-screenshots/wintest-dialog-light.png)、[暗色](chat-screenshots/wintest-dialog-dark.png)
- @ 選單：[亮色](chat-screenshots/wintest-mention-light.png)、[暗色](chat-screenshots/wintest-mention-dark.png)
- 窄螢幕（500px）：[聊天](chat-screenshots/wintest-narrow.png)、[側欄抽屜](chat-screenshots/wintest-narrow-drawer.png)、[成員](chat-screenshots/wintest-narrow-members.png)

win-test 的群聊目前沒有訊息，訊息氣泡（USER 靠右、@你、被刪除用戶、長名稱、長訊息）的樣子看假資料的截圖：
[寬亮](chat-screenshots/wide-light.png)、[寬暗](chat-screenshots/wide-dark.png)、[窄亮](chat-screenshots/narrow-light.png)、[窄暗](chat-screenshots/narrow-dark.png)、
[彈窗亮](chat-screenshots/wide-dialog-light.png)、[彈窗暗](chat-screenshots/wide-dialog-dark.png)。量測數據：[chat-layout-geometry.json](chat-layout-geometry.json)。

## 需要知道的事

- **改了一條既有測試**：`TestListMessagesQueryValidation` 與 `backend/test/06_message.py` 原本把 `limit=10` 當作不接受的參數；
  計畫讓 `limit` 成為合法參數，所以改成用超出範圍的 `limit=51` 驗 400。
- **`api.New` 多一個參數**：message service 改由 `main.go` 建立後傳入，關機時 `RegisterOnShutdown` 才能先結束推播連線；
  不然開著群聊頁時 graceful shutdown 會等滿 15 秒逾時。
- **停權確認**：停權的確認放在共用的 `ChatPermission.vue`，所以帳號頁和 Session 頁選「停權」時也會先確認。
- **`confirm.js` 還原**：上一版為禁言加的多欄位確認已還原，禁言欄位直接寫在管理彈窗裡。
- **捲到底的做法**：資料變動在 render 後判斷，版面變化（字型、視窗）用 ResizeObserver。headless Chrome 的虛擬時間有時不出繪製幀，
  ResizeObserver 就不會回呼，所以版面測試用頁面自己的 40px「在底部」門檻。
- **nginx 沒有跑 `nginx -t`**：這台 WSL 沒有 Docker／nginx。新的 `location = /v1/messages/stream` 是照既有 `/v1` 區塊複製再加
  `proxy_buffering off; proxy_read_timeout 1h;`，`scripts/test/test_test_edge.py` 有檢查。部署前請在主機上跑一次 `nginx -t`。

## 重跑方式

```bash
python3 backend/test/account_api_smoke.py --baseline 040666e5410e8ae87cacd0f2d1188f127d28aabf --windows-package /mnt/c/stock-ai-win-test
```

```bash
cd frontend && npm run build && node test/chat-layout-geometry.mjs --screenshots ../docs/api/chat-screenshots
```
