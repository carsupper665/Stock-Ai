"""Step 1 驗證：服務能啟動、能連 DB、api 與 db 兩組日誌各自分流。

執行方式：
    python backend/test/01_health.py
"""

import os
import re
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _harness import Checks, Server  # noqa: E402

# [api] INFO  2026/09/11-01:30:00| ...（等級欄位靠右補空格，故容許多個空白）
LOG_LINE = re.compile(r"^\[(api|db)\] (DEBUG|INFO|WARN|ERROR)\s+\d{4}/\d{2}/\d{2}-\d{2}:\d{2}:\d{2}\| ")


def main():
    check = Checks()

    with Server(port=7891) as server:
        check.section("檢查 /v1/health")
        status, body, headers = server.request("GET", "/v1/health")
        request_id = headers.get("X-Request-Id", "")

        check("狀態碼為 200", status == 200, f"實際 {status}")
        check("status 欄位為 ok", body.get("status") == "ok", str(body))
        check("回報使用中的資料庫", body.get("database") == "sqlite", str(body))
        check("回應帶有 X-Request-Id", bool(request_id), "header 不存在")

        check.section("檢查未知路徑回 404")
        status, _, _ = server.request("GET", "/v1/not-a-real-path")
        check("未知路徑回 404", status == 404, f"實際 {status}")

        # 日誌寫入與請求處理不同步，給一點時間落地。
        time.sleep(0.3)

        check.section("檢查 api 與 db 日誌分流")
        api_log = server.read_log("api")
        db_log = server.read_log("db")

        check("api 日誌有內容", bool(api_log.strip()), "檔案是空的")
        check("db 日誌有內容", bool(db_log.strip()), "檔案是空的")
        check("api 日誌記錄了 health 請求", "/v1/health" in api_log)
        check("api 日誌記錄了本次的 request id", request_id in api_log, f"找不到 {request_id}")
        check("api 日誌記錄了 404", "404" in api_log)
        check("db 日誌記錄了資料庫連線", "sqlite" in db_log)
        check("兩組日誌沒有混在一起", "/v1/health" not in db_log, "db 日誌裡出現了 API 請求")

        bad = [line for line in api_log.splitlines() if line.strip() and not LOG_LINE.match(line)]
        check("api 日誌格式正確", not bad, f"格式不符的行: {bad[:2]}")
        check("日誌檔不含顏色控制碼", "\x1b[" not in api_log, "檔案裡殘留 ANSI escape code")

    return check.report()


if __name__ == "__main__":
    sys.exit(main())
