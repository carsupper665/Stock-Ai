# 遠端 Windows／WSL 部署候選版

這個候選版交付已通過本機驗收的 **Linux/amd64 容器及原始碼**。
遠端 Windows 必須已有可用的 WSL2 Linux 環境與 Linux Docker engine。
在 WSL 內執行以下命令；不是 Windows 原生 `.exe` 發行包。

## 範圍

- 包含五個產品映像、兩個 auxiliary 映像、原始碼、完整 image digest 清單與 SHA-256 校驗碼。
- 保留已驗收的 manifest digest，不經重新建置或重新推送產品映像。
- 映像包會在目標機建立只監聽 `127.0.0.1:5000` 的 registry，解決原設定指向來源機本地 registry 的問題。
- 目標機自行產生九個 secrets、TLS/JWKS 和空白資料庫；**不包含來源機的帳戶、交易資料、私鑰、環境檔或備份**。
- 登入、Provider、行情與服務憑證 adapter 仍是本地 fixture。這不是外部 IdP／真實行情／真實交易的正式上線驗證。
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

   安裝會檢查目標環境、還原獨立 registry、拉取七個精確 digest、產生私密設定、啟動並等待七個服務健康，
   最後執行嚴格 TLS 與登入驗證，建立獨立 recovery key 及第一份完整加密備份。它只支援**全新安裝**，遇到已有 runtime／同名容器／registry volume 會拒絕覆蓋。
   安裝中斷時保留狀態供檢查，不會自動刪除資料；勿對已有資料的目錄重新初始化。

3. 在遠端 Windows 的 hosts 檔加入 `127.0.0.1 app.localrelease`，並信任
   `$HOME/stock-ai-runtime/certs/fullchain.pem` 這張公開憑證，再於該 Windows 本機瀏覽器開啟
   `https://app.localrelease:8443`。不要複製或分享 `privkey.pem`。
   若從別台電腦操作，請用遠端桌面或受保護的 tunnel 存取；這個包沒有開放對外網路埠。

首次安裝沒有來源機的 Provider／Model／帳戶資料。畫面中的新建資料需要自行設定；本地 fake Provider 的網址是
`http://fake-provider.localrelease:18080/v1`，其憑證只存於新 runtime 的
`secrets/stock-ai-release_fixture_provider_secret`，請在目標機本地使用。
正式遠端 Provider、真實行情與外部 IdP 需要另一個經驗證的部署設定。

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
