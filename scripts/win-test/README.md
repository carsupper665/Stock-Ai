# Stock AI Windows 測試包（win-test）

在一台 Windows 上跑完整套服務的測試包：Backend、LLM Provider、Agent Server、middleware 和前端。
**不需要登入**，打開網頁就是 USER。只用來測試，不要對外開放。

## 啟動與停止

- 啟動：雙擊 `start.cmd`（或在 PowerShell 跑 `.\start.ps1`）。四個服務都健康後會自動開瀏覽器到
  `http://127.0.0.1:24180/`。
- 停止：雙擊 `stop.cmd`（或 `.\stop.ps1`）。

不需要安裝 Go、Node、Docker 或 nginx。

## 內容

| 項目 | 說明 |
|---|---|
| `backend.exe`、`llm-provider-server.exe`、`agent-server.exe` | 內部服務，只聽 127.0.0.1（27794、28090、28080） |
| `middleware.exe` | 以 `wintest` 編譯：沒有登入，替前端帶上各服務的憑證，並提供 `web\` 的前端（24180） |
| `web\` | 前端 |
| `.env` | 非機密設定，每次重建都會覆蓋 |
| `secrets\` | 服務之間的 token 與 LLM Provider 的加密金鑰；第一次建置時產生，之後不會變 |
| `data\` | 三個資料庫、Agent History、`pids.json` |
| `logs\` | 每個服務的 `.out.log`、`.err.log` |

## 常見操作

- **從頭來過**：先停止，再刪掉 `data\`。不要只刪 `secrets\`，否則 LLM Provider 的資料庫會解不開已存的 Provider Token。
- **設定模型**：到「Provider」頁新增 Provider 與模型目錄，再建立交易帳號和 Session。
- **更新到新版程式**：先停止，在 WSL 的 repo 跑 `scripts/build-win-test.sh`。資料、log 和 secrets 會保留。
- **啟動失敗**：看 `logs\<服務>.err.log`；port 被佔用時先跑 `stop.cmd` 或關掉佔用的程式。
