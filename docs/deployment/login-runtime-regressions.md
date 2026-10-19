# 登入與 Codex 部署問題：修正與本機驗證

工作分支：`fix/deployment-auth-codex`。基底：`4dbfbbfcbba0dc13b8d4cb3aab06e6ebeb99ca2d`。
這份紀錄描述程式與部署設定的修正，不代表現場環境已部署或驗收完成。

| 問題 | 處理結果 | 驗證與部署條件 |
|---|---|---|
| 1. `/callback` 與 `/oauth2/callback` 不一致 | 基底已有固定的 `/oauth2/callback` 與 middleware preflight。 | middleware 測試通過；現場 IdP 須登記 preflight 印出的完整 URI，包含 host、port 與路徑。 |
| 2. `client_secret_basic` 與 `client_secret_post` 不相容 | 基底已支援 `OIDC_TOKEN_AUTH_METHOD=client_secret_post`，並比對 discovery 支援的方法。 | middleware 的 POST 交換測試通過；現場須選擇 IdP client 實際要求的方法。 |
| 3. 登入後 Access Token 401 | 基底已有 middleware 發 Cookie 前的 Access Token 驗證（現在只有 middleware 驗 OIDC，Backend 不再驗）。 | middleware 的不相容 Token 拒絕案例通過；實際 IdP 簽發內容仍須現場驗收。 |
| 4. 未登入健康檢查造成重載循環 | 基底已限制登入後才呼叫服務健康端點。 | 前端相關 20 項測試通過，涵蓋未登入時不送健康請求。 |
| 5. 容器缺少中繼及 HTTPS 檢查 CA | Release overlay 補上 Agent、LLM 的額外 CA 掛載，並將 Codex 額外 CA 傳入隔離程序。 | 設定、程序環境與空 CA 回歸測試通過。安裝器只對非空的額外 CA 設定 `CODEX_CA_CERTIFICATE`。實際 IdP／Kaspersky PEM 需由現場提供並驗證。 |
| 6. Agent USER 憑證被 Release Backend 拒絕 | Backend 新增獨立的 Agent 帳號查詢認證。沿用既有共享私有檔案，避免重新產生不一致的金鑰。 | 實際 Backend handler 測試重現原 401，修正後可取得帳號與 runtime Token；同一憑證無法列舉、修改帳號或下單，JWT 形狀的值也不被接受。Backend 與 Compose 必須一併更新。 |
| 7. LLM 容器缺少 Codex 及登入檔 | LLM 映像打包 Codex 0.162.1；新增 `compose.codex.yaml` 與安裝器 `--codex-auth-file`。 | 官方套件雜湊與原生程式啟動已驗證；登入檔保存於 runtime，唯讀掛入容器。Provider 須設定 `auth_mode=local_login`。 |
| 8. 缺少 `codex-code-mode-host` | Docker 與 Windows 發行流程保留官方完整目錄；模型呼叫前檢查工具宿主及套件 metadata，支援官方 launcher symlink。 | 已重現缺件時仍允許呼叫的行為；修正後回傳 `HARNESS_PACKAGE_INCOMPLETE`。映像建置驗證另實際啟動主程式及工具宿主。Windows 腳本測試案例已更新，尚未在 Windows 執行。 |
| 9. 工具故障卻標為 completed | Codex 非重試錯誤、失敗回合及沒有工具呼叫的中斷會回傳錯誤；Finalization 可明確回報 `status=failed`，Run 以 `TASK_FAILED` 結束。 | 工具宿主錯誤、空錯誤訊息、中斷及模型明確失敗的回歸測試通過。正常無動作決策仍可完成；部署驗收另外要求成功的實際工具紀錄。 |
| 10. 僅驗證健康及初始化 | 新增 `scripts/release/verify_authenticated.py`，檢查登入後 API、Agent／Backend 帳號查詢、持久化的成功 `get_account` 結果及登入保持。 | 401、服務認證失敗、零工具呼叫與失敗工具結果都會使驗收失敗。安裝後的外部 IdP 驗收證據先標為 `pending`，需使用真實登入 Cookie 執行此流程。 |

服務認證配置沿用歷史命名的 `<namespace>_backend_user_token` 私有檔案：
Backend 使用 `AGENT_SERVICE_TOKEN_FILE`；Agent 使用既有的
`AGENT_SERVER_BACKEND_USER_TOKEN_FILE`。`ALLOW_STATIC_USER_TOKEN` 已不存在；Backend 的 USER 憑證是只有 middleware 持有的 `backend_middleware_token`，`AGENT_SERVICE_TOKEN` 不可與它相同。
此憑證僅授權 `GET /v1/accounts/:id`；領域工具繼續使用各 Account 自己的 Token。

## 可重現測試

以下為修正當時的本機結果（之後的 middleware 合併未重跑這些數字）；Go／HTTP 測試使用可控 fixture，沒有使用現場登入資料或付費模型。

| 工作目錄 | 指令 | 結果 |
|---|---|---|
| `backend` | `rtk go test ./...` | 336 通過 |
| `agent_server` | `rtk go test ./...` | 406 通過 |
| `llm_provider_server` | `rtk go test ./...` | 259 通過 |
| `oidc_gateway`（現為 `middleware`） | `rtk go test ./internal/gateway` | 67 通過 |
| `frontend` | `rtk npm test -- test/app-health.test.js test/oidc-browser.test.js test/api.test.js test/router.test.js` | 20 通過 |
| 根目錄 | `rtk python3 -m unittest discover -s scripts/test -p 'test_*.py'` | 119 通過 |
| 根目錄 | `rtk python3 -m unittest discover -s scripts/release -p 'test_*.py'` | 4 通過 |
| 根目錄 | `rtk python3 -m unittest discover -s scripts/local-release -p test_external_idp.py` | 4 通過 |

修正前已觀察到的失敗包括：

- `TestAgentAccountLookupWorksWithStaticUSERDisabled`：帳號查詢得到 401，預期 200。
- `TestCodexToolHostErrorsCannotBecomeSuccessfulResponses`：宿主錯誤及無工具的中斷仍回傳成功。
- `TestCodexMissingToolHostIsRejectedBeforeModelInvocation`：缺少宿主時未回傳錯誤。
- `TestModelReportedFailureDoesNotCompleteRun`：無法直接以 `TASK_FAILED` 結束。
- `TestCodexDoesNotReceiveAnEmptyCustomCASetting`：空 CA 設定仍傳入隔離程序。

上述回歸案例均已轉綠。另已下載官方 `@openai/codex@0.162.1-linux-x64`，核對 npm metadata 的
SHA-512，使用 `--strip-components=3` 解包，實際取得 `codex-cli 0.162.1` 並成功執行
`codex-code-mode-host --help`。

## 現場仍需完成的驗證

- 此 WSL 未提供 Docker engine，無法建置、啟動或驗證實際容器。執行完整
  `scripts/local-release` 測試時，`test_configuration.ReleaseConfigurationTest` 的
  Docker Compose 設定檢查因此無法執行，不能視為該測試套件全數通過。
- 此環境沒有 PowerShell／Windows runner，尚未執行 Windows 發行打包測試。
- 尚未使用現場 IdP、實際中繼／Kaspersky CA、登入 Cookie 或真實模型執行端到端工具驗收。
- Codex 登入檔目前採用每個隔離程序的快照；程序中的 Token 更新不會回寫主機檔案。
  登入檔過期後需重新登入、更新 runtime 檔案並重建容器。
- 既有映像 digest、promotion manifest 及舊驗收紀錄不能代表這次修改已部署。
  必須重新建置、核准映像並在目標機完成登入後工具驗收。

操作步驟見 `docs/deployment/remote-windows-release.md`。Windows 原生發行版若已有 `.env`，
需將 Codex 入口更新為 `tools/codex/bin/codex.exe`。
