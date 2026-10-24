# 安裝手冊（正式發布）

正式環境的架構：

```
瀏覽器 → 主機 Nginx（TLS、前端靜態檔、安全標頭、限流）→ middleware（127.0.0.1:4180）→ Backend／Agent／LLM
```

只用一層 Nginx，裝在跑 Docker 的同一台主機上。DNS 直接指到這台主機。Docker 內的 Nginx
（`compose.release.yaml`、`compose.test-edge.yaml`）只留給測試，不要在主機 Nginx 後面再疊一層。

## 前置

- Linux Docker engine（amd64），Python 3.12 以上、`cryptography`、OpenSSL、curl。
- 主機上已安裝 Nginx，並有網址的 TLS 憑證（例如 Let's Encrypt）。
- 外部 IdP 的 client 已建立；client secret 另存成檔案，不要寫進 env 檔。

## 安裝

1. 下載並校驗發布包，步驟同 `remote-windows-release.md` 的「安裝」第 1、2 步，但先不要執行 `bootstrap.py`。
2. 複製 `deploy/compose/release.env.example` 並填寫，各欄位的說明見 `remote-windows-release.md` 的「外部 IdP 部署」。
   `RELEASE_APP_HOST` 填使用者實際開啟的網址；主機 Nginx 用 443 時不要帶 port（例如 `stock-ai.example.com`）。
   `RELEASE_BIND` 保持 `127.0.0.1:8443`：安裝時會先用 Docker Nginx 驗證登入流程，之後才切到主機 Nginx。
   IdP 要登記 `https://<RELEASE_APP_HOST>/oauth2/callback` 與 `https://<RELEASE_APP_HOST>/`。
3. 安裝。會先做 OIDC preflight，再啟動並驗證整套服務：

   ```bash
   python3 bootstrap.py --runtime "$HOME/stock-ai-runtime" \
     --idp-env "$HOME/stock-ai-idp.env" --oidc-client-secret-file "$HOME/stock-ai-oidc-client-secret"
   ```

4. 改用 `compose.host-nginx.yaml` 啟動。`--remove-orphans` 會移除 Docker Nginx 容器（映像與設定保留，測試仍可使用）：

   ```bash
   docker compose --env-file "$HOME/stock-ai-runtime/compose.env" \
     -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.yaml" \
     -f "$HOME/stock-ai-runtime/deployment/deploy/compose/compose.host-nginx.yaml" \
     up -d --no-build --pull never --wait --remove-orphans
   ```

   之後重啟、停止、preflight 都用這組 `-f`。middleware 只綁 `127.0.0.1`，外部無法直接連到。
   若 4180 已被佔用，在 `compose.env` 設 `MIDDLEWARE_HOST_PORT`，並同步修改下方 `upstream`。
5. 從同一版的 Nginx 映像取出前端靜態檔（每次更新版本都要重做，前端與服務版本才會一致）：

   ```bash
   image=$(grep '^NGINX_IMAGE=' "$HOME/stock-ai-runtime/compose.env" | cut -d= -f2-)
   container=$(docker create "$image")
   sudo rm -rf /var/www/stock-ai && sudo docker cp "$container":/usr/share/nginx/html /var/www/stock-ai
   docker rm "$container"
   ```

6. 把下面的設定存成 `/etc/nginx/conf.d/stock-ai.conf`，替換 `stock-ai.example.com` 與憑證路徑，
   執行 `sudo nginx -t && sudo systemctl reload nginx`。

   ```nginx
   # 放在 http 區塊內（conf.d 預設就在 http 內）
   limit_req_zone $binary_remote_addr zone=stock_ai_per_ip:10m rate=10r/s;
   limit_req_zone $binary_remote_addr zone=stock_ai_login_per_ip:10m rate=5r/m;
   limit_conn_zone $binary_remote_addr zone=stock_ai_connections_per_ip:10m;
   # 登入流程（/oauth2/）不轉 Authorization，其餘 API 原樣轉送。
   map $uri $stock_ai_authorization { ~^/oauth2/ ""; default $http_authorization; }

   upstream stock_ai_middleware { server 127.0.0.1:4180; keepalive 16; }

   server {
       listen 80;
       server_name stock-ai.example.com;
       return 301 https://stock-ai.example.com$request_uri;
   }

   server {
       listen 443 ssl;
       server_name stock-ai.example.com;
       if ($host != stock-ai.example.com) { return 421; }

       ssl_certificate     /etc/letsencrypt/live/stock-ai.example.com/fullchain.pem;
       ssl_certificate_key /etc/letsencrypt/live/stock-ai.example.com/privkey.pem;
       ssl_protocols TLSv1.2 TLSv1.3;
       ssl_session_tickets off;

       server_tokens off;
       client_max_body_size 1m;
       limit_req_status 429;
       limit_conn stock_ai_connections_per_ip 30;

       add_header Strict-Transport-Security "max-age=31536000" always;
       add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self' https://fonts.googleapis.com https://fonts.gstatic.com; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'" always;
       add_header X-Content-Type-Options "nosniff" always;
       add_header Referrer-Policy "same-origin" always;
       add_header X-Frame-Options "DENY" always;

       # 只轉白名單內的 header；瀏覽器偽造的轉送／身分 header 一律不送給 middleware。
       proxy_pass_request_headers off;
       proxy_http_version 1.1;
       proxy_set_header Connection "";
       proxy_set_header Host stock-ai.example.com;
       proxy_set_header Accept $http_accept;
       proxy_set_header Content-Type $content_type;
       proxy_set_header Content-Length $content_length;
       proxy_set_header Cookie $http_cookie;
       proxy_set_header Origin $http_origin;
       proxy_set_header User-Agent $http_user_agent;
       proxy_set_header X-Real-IP $remote_addr;
       proxy_set_header X-Forwarded-For $remote_addr;
       proxy_set_header X-Forwarded-Proto https;
       proxy_set_header X-Forwarded-Host stock-ai.example.com;
       proxy_set_header Authorization $stock_ai_authorization;

       location = /oauth2/start {
           limit_req zone=stock_ai_login_per_ip burst=5 nodelay;
           proxy_pass http://stock_ai_middleware;
       }
       location = /oauth2/callback {
           limit_req zone=stock_ai_login_per_ip burst=10 nodelay;
           proxy_pass http://stock_ai_middleware;
       }
       location ^~ /oauth2/ {
           limit_req zone=stock_ai_per_ip burst=30 nodelay;
           proxy_pass http://stock_ai_middleware;
       }

       location ~ ^/v1(?:/|$) {
           limit_req zone=stock_ai_per_ip burst=40 nodelay;
           proxy_pass http://stock_ai_middleware;
       }

       # Agent 執行串流與 LLM 測試呼叫可能跑好幾分鐘：不緩衝、拉長讀取逾時，避免中途被切成 502。
       location ^~ /agent/ {
           limit_req zone=stock_ai_per_ip burst=40 nodelay;
           proxy_buffering off;
           proxy_read_timeout 1h;
           proxy_send_timeout 1h;
           proxy_pass http://stock_ai_middleware;
       }
       location ^~ /llm/ {
           limit_req zone=stock_ai_per_ip burst=40 nodelay;
           proxy_buffering off;
           proxy_read_timeout 1h;
           proxy_send_timeout 1h;
           proxy_pass http://stock_ai_middleware;
       }

       # middleware 自己的健康狀態與版本，前端側欄「服務狀態」會顯示。
       location = /middleware/healthz {
           limit_except GET { deny all; }
           proxy_pass http://stock_ai_middleware/healthz;
       }

       location = /healthz {
           default_type text/plain;
           return 200 "ok\n";
       }

       location ^~ /assets/ {
           limit_req zone=stock_ai_per_ip burst=40 nodelay;
           limit_except GET HEAD { deny all; }
           root /var/www/stock-ai;
       }
       location / {
           limit_req zone=stock_ai_per_ip burst=40 nodelay;
           root /var/www/stock-ai;
           try_files $uri /index.html;
       }
   }
   ```

   `add_header` 和 `proxy_set_header` 只有在 location 內完全沒寫同名指令時才會繼承 server 層的設定；
   location 內只要寫了一行，server 層的就全部失效。新增 location 時不要在裡面寫這兩種指令，
   要不同的 header 就比照 `$stock_ai_authorization` 用 `map` 處理。
7. 檢查：`curl -s https://stock-ai.example.com/middleware/healthz` 回 `{"status":"ok","version":"…"}`，
   登入後側欄五列（四個服務與前端）都顯示版本。

## 版本

每個服務目錄下的 `VERSION` 檔第一行是自己的版本（格式 `X.Y.Z-bNrM`，例如 `5.0.1-b20r5`），
之後每行是它依賴的服務與版本，例如 `backend 0.1.1-b0r0`。建置 image 前會先跑
`scripts/check_versions.py` 比對，不相容就停止建置：

- 一般服務：依賴的 `X.Y.Z` 必須完全相同，`-bNrM` 不比對。
- 前端：向下相容，服務的 `X.Y.Z` 小於或等於前端宣告的版本即可。

執行中的系統不檢查相容性，只在側欄「服務狀態」顯示各服務回報的版本。

## Windows 測試包（win-test）

要在一台 Windows 上快速測試整套系統，不想設定 IdP、網域和憑證時使用。在 WSL 的 repo 執行：

```bash
scripts/build-win-test.sh
```

會在 `C:\stock-ai-win-test` 產生四個 Windows 執行檔、前端和啟動腳本，雙擊 `start.cmd` 啟動，瀏覽器開
`http://127.0.0.1:24180/`。用法見該資料夾的 `README.md`（來源 `scripts/win-test/README.md`）。

- 測試包的 middleware 以 `-tags wintest` 編譯：**沒有登入**，每個請求都是 USER，並直接提供前端。
  一般編譯（Docker image、正式發行包）不含這段程式，無法用設定打開。只能用於本機測試，不要對外開放。
- 重跑腳本會更新程式與 `.env`，`data\`、`logs\`、`secrets\` 保留不動；執行中的 `.exe` 無法覆蓋，先跑 `stop.cmd`。
- port 與正式發行包、開發環境分開：Backend 27794、LLM Provider 28090、Agent 28080、網頁 24180。
