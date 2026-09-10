"""Step 1 驗證：服務能啟動、能連 DB、api 與 db 兩組日誌各自分流。

執行方式：
    python backend/test/01_health.py

只用標準函式庫，不需要 pip install。
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

BACKEND_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PORT = "7891"
BASE_URL = f"http://127.0.0.1:{PORT}"

# [api] INFO  2026/09/11-01:30:00| ...（等級欄位靠右補空格，故容許多個空白）
LOG_LINE = re.compile(r"^\[(api|db)\] (DEBUG|INFO|WARN|ERROR)\s+\d{4}/\d{2}/\d{2}-\d{2}:\d{2}:\d{2}\| ")

# Windows console 預設不是 UTF-8，不設定的話中文輸出會變亂碼。
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")

failures = []


def check(label, condition, detail=""):
    if condition:
        print(f"  [OK]   {label}")
    else:
        print(f"  [FAIL] {label}" + (f" -> {detail}" if detail else ""))
        failures.append(label)


def build(workdir):
    binary = os.path.join(workdir, "backend.exe" if sys.platform == "win32" else "backend")
    result = subprocess.run(
        ["go", "build", "-o", binary, "."],
        cwd=BACKEND_DIR, capture_output=True, text=True,
    )
    if result.returncode != 0:
        print("編譯失敗:\n" + result.stderr)
        sys.exit(1)
    return binary


def write_env(workdir, log_dir):
    env_path = os.path.join(workdir, ".env")
    with open(env_path, "w", encoding="utf-8") as f:
        f.write(
            "USER_TOKEN=health-check-token\n"
            "USER_NAME=Bless\n"
            f"PORT={PORT}\n"
            "DB_DRIVER=sqlite\n"
            f"DB_DSN={os.path.join(workdir, 'health.db')}\n"
            f"LOG_DIR={log_dir}\n"
            "DEBUG=true\n"
        )
    return env_path


def wait_until_up(process, timeout=20):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if process.poll() is not None:
            out, _ = process.communicate()
            print("服務啟動後隨即結束:\n" + out)
            return False
        try:
            with urllib.request.urlopen(BASE_URL + "/v1/health", timeout=1):
                return True
        except (urllib.error.URLError, ConnectionError, OSError):
            time.sleep(0.2)
    return False


def read_log(log_dir, name):
    """讀取指定 logger 的所有日誌檔內容。"""
    content = ""
    for entry in sorted(os.listdir(log_dir)):
        if entry.startswith(name + "-") and entry.endswith(".log"):
            with open(os.path.join(log_dir, entry), encoding="utf-8") as f:
                content += f.read()
    return content


def main():
    workdir = tempfile.mkdtemp(prefix="stockai-health-")
    log_dir = os.path.join(workdir, "logs")
    process = None
    try:
        binary = build(workdir)
        write_env(workdir, log_dir)

        process = subprocess.Popen(
            [binary], cwd=workdir,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        )

        print("啟動服務並等待回應")
        if not wait_until_up(process):
            print("\nFAIL: 服務未在時限內回應")
            return 1

        print("\n檢查 /v1/health")
        request = urllib.request.Request(BASE_URL + "/v1/health")
        with urllib.request.urlopen(request, timeout=5) as response:
            status = response.status
            body = json.loads(response.read())
            request_id = response.headers.get("X-Request-Id", "")

        check("狀態碼為 200", status == 200, f"實際 {status}")
        check("status 欄位為 ok", body.get("status") == "ok", str(body))
        check("回報使用中的資料庫", body.get("database") == "sqlite", str(body))
        check("回應帶有 X-Request-Id", bool(request_id), "header 不存在")

        print("\n檢查未知路徑回 404")
        try:
            urllib.request.urlopen(BASE_URL + "/v1/not-a-real-path", timeout=5)
            check("未知路徑回 404", False, "竟然成功了")
        except urllib.error.HTTPError as err:
            check("未知路徑回 404", err.code == 404, f"實際 {err.code}")

        # 日誌是非同步寫入 console 與檔案，給一點時間落地。
        time.sleep(0.3)

        print("\n檢查 api 與 db 日誌分流")
        api_log = read_log(log_dir, "api")
        db_log = read_log(log_dir, "db")

        check("api 日誌有內容", bool(api_log.strip()), "檔案是空的")
        check("db 日誌有內容", bool(db_log.strip()), "檔案是空的")
        check("api 日誌記錄了 health 請求", "/v1/health" in api_log)
        check("api 日誌記錄了本次的 request id", request_id in api_log, f"找不到 {request_id}")
        check("api 日誌記錄了 404", "404" in api_log)
        check("db 日誌記錄了資料庫連線", "sqlite" in db_log)
        check("兩組日誌沒有混在一起", "/v1/health" not in db_log, "db 日誌裡出現了 API 請求")

        api_lines = [line for line in api_log.splitlines() if line.strip()]
        bad = [line for line in api_lines if not LOG_LINE.match(line)]
        check("api 日誌格式正確", not bad, f"格式不符的行: {bad[:2]}")
        check(
            "日誌檔不含顏色控制碼",
            "\x1b[" not in api_log,
            "檔案裡殘留 ANSI escape code",
        )

    finally:
        if process and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
        shutil.rmtree(workdir, ignore_errors=True)

    print()
    if failures:
        print(f"FAIL: {len(failures)} 項未通過 -> {', '.join(failures)}")
        return 1
    print("PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
