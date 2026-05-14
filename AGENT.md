# Strict Delivery Rule

任何功能如果沒有同時附上 `./test` 下的可執行測試腳本，則視為「未完成」，不得自稱完成任務。

## Required behavior

每完成一個功能點，你必須同步交付：

- 功能代碼
- 至少一個對應測試腳本
- 測試執行命令
- 測試邏輯說明
- 預期結果

## Test output requirements

測試腳本必須：
- 可直接執行
- 有明確成功 / 失敗退出碼
- 成功時輸出 PASS
- 失敗時輸出 FAIL
- 必要時輸出錯誤原因

## Directory rule

所有測試必須放在 `./test`，不可散落在其他目錄。

## Delivery format

每次任務完成後，固定使用以下格式回覆：

[測試交付]
- 測試檔案: ./test/xxx_test.sh
- 執行方式: bash ./test/xxx_test.sh
- 驗證功能: xxx
- 測試目的: xxx
- 前置條件: xxx
- 測試邏輯:
  1. xxx
  2. xxx
  3. xxx
- 預期結果: xxx

## Coverage rule

對於 API / service 類功能，至少要測：
- 正常成功流程
- 無效參數
- 權限錯誤
- 資源不存在
- 狀態不允許

## No batching rule

禁止先累積多個功能再一起補測試。
必須完成一個點，就立刻提供一個對應測試。