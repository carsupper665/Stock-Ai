# 遠端 Windows／WSL 部署候選版

這個候選版交付已通過本機驗收的 **Linux/amd64 容器及原始碼**。
遠端 Windows 必須已有可用的 WSL2 Linux 環境與 Linux Docker engine。
在 WSL 內執行以下命令；不是 Windows 原生 `.exe` 發行包。

## 範圍

- 包含五個產品映像（Backend、Agent、LLM Provider、middleware、nginx）、一個 auxiliary 映像（fixture IdP）、原始碼、完整 image digest 清單與 SHA-256 校驗碼。
- 保留已驗收的 manifest digest，不經重新建置或重新推送產品映像。
- 映像包會在目標機建立只監聽 `127.0.0.1:5000` 的 registry，解決原設定指向來源機本地 registry 的問題。
- registry 只供安裝時載入映像，六個映像驗證完成後便停止；應用啟動使用 `--pull never`。
- 目標機自行產生九個產品 secrets 與 fixture Provider 憑證、TLS/JWKS 和空白資料庫；**不包含來源機的帳戶、交易資料、私鑰、環境檔或備份**。
- 登入、Provider、行情仍是本地 fixture（Agent 直接連 Backend，不再經過 fixture adapter）。這不是外部 IdP／真實行情／真實交易的正式上線驗證。
- 本機既有 144 個 Python 測試及 T16 安全／還原驗收記錄保留於原始碼中；遠端 Windows 仍需現場驗收。

## 安裝

1. 在遠端 Windows 的 WSL 執行 `docker info`，確認 engine 為 Linux、架構 amd64/x86_64。
   準備 Python 3.12 以上、`cryptography`、OpenSSL 和 curl，例如 Ubuntu：

   ```bash
   sudo apt-get update
   sudo apt-get install -y python3 python3-cryptography openssl curl
   ```

2. 從 GitHub Release 下載 `Stock-Ai-release-wsl-20261015.tar.gz`、`release-manifest.json` 及 `SHA256SUMS`，
   放進 WSL 的 Linux 檔案系統，例如 `~/stock-ai-release`。不要在 `/mnt/c` 上存放 runtime secrets。

   ```bash
   sha256sum -c SHA256SUMS
   tar -xzf Stock-Ai-release-wsl-20261015.tar.gz
   cd Stock-Ai-release-wsl-20261015
   python3 bootstrap.py --verify-only
   python3 bootstrap.py --runtime "$HOME/stock-ai-runtime"
   ```

   安裝會檢查目標環境、還原獨立 registry、拉取六個精確 digest、產生私密設定、啟動並等待六個服務健康，
   最後執行嚴格 TLS 與登入驗證，建立獨立 recovery key 及第一份完整加密備份。它只支援**全新安裝**，遇到已有 runtime／同名容器／registry volume 會拒絕覆蓋。
   安裝中斷時保留狀態供檢查，不會自動刪除資料；勿對已有資料的目錄重新初始化。

3. 在遠端 Windows 的 hosts 檔加入 `127.0.0.1 app.localrelease`，並信任
   `$HOME/stock-ai-runtime/certs/fullchain.pem` 這張公開憑證，再於該 Windows 本機瀏覽器開啟
   `https://app.localrelease:8443`。不要複製或分享 `privkey.pem`。
   若從別台電腦操作，請用遠端桌面或受保護的 tunnel 存取；這個包沒有開放對外網路埠。

首次安裝沒有來源機的 Provider／Model／帳戶資料。畫面中的新建資料需要自行設定；本地 fake Provider 的網址是
`http://fake-provider.localrelease:18080/v1`，其憑證只存於新 runtime 的
`secrets/stock-ai-release_fixture_provider_secret`，請在目標機本地使用。
正式遠端 Provider、真實行情需要另一個經驗證的部署設定；外部 IdP 見下一節。

## 重啟、停止與驗收

設定與 operator 工具會複製到 `$HOME/stock-ai-runtime/deployment`，後續執行：

```bash
docker compose --env-file "$HOME/stock-ai-runtime/compose.env" \
  -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.yaml" \
  -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.local-release.yaml" ps
docker compose --env-file "$HOME/stock-ai-runtime/compose.env" \
  -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.yaml" \
  -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.local-release.yaml" up -d --no-build --pull never --wait
```

停止時使用相同參數加 `stop`。設定已存於持久 runtime，請保留該目錄及其 deployment 子目錄。
不要使用 `down -v` 刪除資料。備份、完整還原及更新方式見
`source/docs/deployment/local-release-signoff.md`；文件內來源機絕對路徑需替換為自己的 runtime／套件路徑。

本地 fixture 憑證有效期兩天。憑證到期、外部 IdP、公開 DNS/TLS 更新、Windows 開機自啟與長期運作，
均不能用來源機的本機驗收紀錄代替目標機驗收。完整 T16 runner 需要代表性資料、recovery key 等前置條件，
不要直接對空白安裝宣稱業務流程／備份演練已通過。

## 外部 IdP 部署

外部 IdP 模式啟動完整登入鏈路：Backend、Agent、LLM Provider、middleware 與入口 Nginx，
不啟動 fixture IdP。設定全部來自 ENV，Compose 檔為 `compose.yaml` 加 `compose.release.yaml`。

1. 複製 `source/deploy/compose/release.env.example` 到 WSL 的私有位置並填寫。數值以 IdP 的
   `/.well-known/openid-configuration` 為準。IdP client secret 另存一個檔案（至少 32 bytes），不要寫進 env 檔。
2. 在 IdP client 登記下列兩個 URL。callback 路徑固定為 `/oauth2/callback`，不是 `/callback`：
   - Redirect URI：`https://<RELEASE_APP_HOST>/oauth2/callback`
   - Post-logout URI：`https://<RELEASE_APP_HOST>/`
3. 若 IdP 只送出網站憑證（缺中繼憑證）或使用私有 CA，把經驗證的中繼／根憑證鏈存成 PEM，填入
   `OIDC_CA_BUNDLE`。不要停用 TLS 驗證。
4. 安裝：

   ```bash
   python3 bootstrap.py --runtime "$HOME/stock-ai-runtime" \
     --idp-env "$HOME/stock-ai-idp.env" --oidc-client-secret-file "$HOME/stock-ai-oidc-client-secret"
   ```

   安裝會在啟動前於 middleware 容器內執行 `middleware preflight`，使用與正式執行相同的信任庫，檢查：
   TLS 憑證鏈、discovery 的 issuer 與各端點是否與 ENV 完全一致、JWKS、`OIDC_TOKEN_AUTH_METHOD`
   是否在 `token_endpoint_auth_methods_supported` 內，以及 authorize 端點是否接受 Redirect URI。
   任一項失敗即停止，並印出原因與要登記的 URL。之後可隨時重跑：

   ```bash
   docker compose --env-file "$HOME/stock-ai-runtime/compose.env" \
     -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.yaml" \
     -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.release.yaml" run --rm --no-deps middleware preflight
   ```

5. 重啟、停止使用上一節的指令，但第二個 `-f` 換成 `compose.release.yaml`。

### Access token 設定

登入時 middleware 會比對 access token（Backend 不再驗 OIDC）：header `typ` 要等於 `OIDC_TOKEN_TYPE`，
`alg` 要是 RS256 且要有 `kid`，`iss` 要等於 `OIDC_ISSUER`，`aud` 要包含 `OIDC_AUDIENCE`，`sub` 要等於
`OIDC_USER_SUBJECT`。不符時登入頁顯示登入失敗，middleware log 記下是哪個欄位及實際值與設定值（不記 token）。
常見情況：Keycloak 的 access token `typ` 是 `JWT`；`aud` 不含 API audience 時，要在 client 加 audience mapper。

### 登入驗收清單

服務健康不代表登入可用，以下全部通過才算外部 IdP 驗收完成：

1. preflight 全部 `ok`，且 IdP 已登記它印出的 Redirect URI 與 Post-logout URI。
2. 瀏覽器登入成功，`docker compose ... logs middleware` 沒有 `OIDC login failed`。
3. 登入後側欄各服務狀態皆為 `ok`（`/v1/health`、`/agent/health`、`/llm/healthz` 均 200；主機 Nginx 部署另有 `/middleware/healthz`），並顯示各自的版本。
4. 實際操作：開啟 Provider 頁、建立 Session、查看執行明細，皆無 401 或被登出。
5. 登出後回到登入頁且沒有重新載入循環，再次登入成功。


### 容器信任鏈與 Agent 認證

`OIDC_CA_BUNDLE` 會以唯讀方式掛入 Backend、middleware、Agent 與 LLM 容器。
若 Windows 的 Kaspersky 會檢查 HTTPS，將經管理者確認的 HTTPS 檢查根憑證匯出為 PEM，
與 IdP 缺少的中繼憑證合併至此檔案。保留映像的公開根憑證；Codex 另外透過
`CODEX_CA_CERTIFICATE` 讀取這份額外 CA，隔離程序也會保留此設定。
安裝器只在額外 CA 非空時設定此變數；既有 runtime 若需額外 CA，請在其 `compose.env`
加入 `CODEX_CA_CERTIFICATE=/etc/ssl/certs/oidc-ca.pem`。不需要額外 CA 時留空，使用公開信任庫。
更換檔案後重新建立容器並重跑 preflight 與下列登入後驗收。

Release 的 Agent 帳號查詢使用獨立的服務認證權限。Backend 的 `AGENT_SERVICE_TOKEN_FILE`
與 Agent 既有的 `AGENT_SERVER_BACKEND_USER_TOKEN_FILE` 掛載同一份私有檔案；歷史檔名仍為
`stock-ai-release_backend_user_token`。這只授權單筆帳號讀取，且不可與 Backend 的 USER 憑證
（`USER_TOKEN_FILE`，即 `backend_middleware_token`，只有 Backend 與 middleware 持有）相同。
Backend 與 Compose 必須一併更新。

### Codex 完整套件與登入檔

新 LLM 映像包含鎖定的 Codex 0.162.1 官方 Linux/amd64 完整套件，建置時核對 SHA-512，
並檢查主程式及 `codex-code-mode-host` 可執行。不要只複製 `codex` 單一檔案。
Provider 的 `auth_mode` 選擇 `local_login` 時，首次安裝可另外提供：

```bash
python3 bootstrap.py --runtime "$HOME/stock-ai-runtime" \
  --idp-env "$HOME/stock-ai-idp.env" --oidc-client-secret-file "$HOME/stock-ai-oidc-client-secret" \
  --codex-auth-file "$HOME/.codex/auth.json"
```

安裝會將指定登入檔私下複製至 runtime，並加上 `compose.codex.yaml` 唯讀掛載。
既有部署請在私有 env 設定 `CODEX_AUTH_FILE` 的持久化路徑，所有 Compose 指令加上
`-f .../compose.codex.yaml`。LLM 使用 `/opt/codex/bin/codex` 與
`LLM_SERVER_CODEX_AUTH_FILE=/run/codex-auth/auth.json`；登入檔需可由容器的群組讀取。
登入來源不是工作樹或映像的一部分。過期時在主機重新登入、更新 runtime 中的檔案並重建容器；
目前每個隔離程序使用登入快照，不會將程序內的更新回寫主機檔案。

官方登入檔與額外 CA 的設定依據：[OpenAI Authentication](https://learn.chatgpt.com/docs/auth)。

### 登入後工具驗收

外部 IdP 安裝成功只代表服務啟動，`evidence/install/authenticated-acceptance.json`
會標為 `pending`。使用私下儲存的實際登入 Cookie jar（Netscape 格式）執行：

```bash
python3 source/scripts/release/verify_authenticated.py \
  --base-url https://stock-ai.corp.internal:8443 \
  --cookie-file "$HOME/stock-ai-login.cookies" \
  --ca-bundle "$HOME/stock-ai-runtime/certs/fullchain.pem" \
  --model-name <已啟用的模型名稱> \
  --evidence "$HOME/stock-ai-runtime/evidence/install/authenticated-acceptance.json"
```

此流程會實際呼叫模型，先驗證登入後的 Backend、Provider、Agent API，再建立暫時的
Account／Session，要求一次 `get_account`，核對持久化的成功工具結果，最後確認仍保持登入，
並停止、刪除驗收資源。`completed`、模型自述或 App Server 初始化都不能替代工具結果；
零工具呼叫、工具失敗、API 401 或服務認證失敗皆會讓驗收失敗。證據不包含 Cookie 或 Token。
瀏覽器登出後不重載循環、再次登入成功，仍須按前述清單實際確認。

這些修正需重新建置、驗證及核准映像後才會進入部署；既有 promotion manifest 與舊驗收紀錄
不代表新映像已通過驗收。

## 正式環境：主機 Nginx

正式發布改用主機上的單層 Nginx，步驟見 `install.md`。
