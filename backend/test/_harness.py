"""端到端測試的共用工具：編譯、啟動服務、發請求、記錄檢查結果。

各測試腳本 import 這個模組，不要直接執行。
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

BACKEND_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
USER_TOKEN = "e2e-user-token"
USER_NAME = "Bless"

# Windows console 預設不是 UTF-8，不設定的話中文輸出會變亂碼。
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")


class Checks:
    """累積檢查結果，最後決定退出碼。"""

    def __init__(self):
        self.failures = []

    def __call__(self, label, condition, detail=""):
        if condition:
            print(f"  [OK]   {label}")
        else:
            print(f"  [FAIL] {label}" + (f" -> {detail}" if detail else ""))
            self.failures.append(label)
        return bool(condition)

    def section(self, title):
        print(f"\n{title}")

    def report(self):
        print()
        if self.failures:
            print(f"FAIL: {len(self.failures)} 項未通過 -> {', '.join(self.failures)}")
            return 1
        print("PASS")
        return 0


class Server:
    """在暫存目錄編譯並啟動 backend，離開 with 區塊時關閉並清理。"""

    def __init__(self, port, env_extra=None):
        self.port = str(port)
        self.url = f"http://127.0.0.1:{self.port}"
        self.workdir = tempfile.mkdtemp(prefix="stockai-e2e-")
        self.log_dir = os.path.join(self.workdir, "logs")
        self.env_extra = env_extra or {}
        self.process = None
        self.console_path = os.path.join(self.workdir, "console.out")
        self._console = None

    def __enter__(self):
        binary = self._build()
        self._write_env()
        # 一定要導到檔案，不能用 PIPE：伺服器的 console 日誌沒人讀的話，
        # OS 的 pipe buffer 一滿，寫日誌就會永遠阻塞，整個服務跟著卡死。
        self._console = open(self.console_path, "w", encoding="utf-8")
        self.process = subprocess.Popen(
            [binary], cwd=self.workdir,
            stdout=self._console, stderr=subprocess.STDOUT,
        )
        if not self._wait_until_up():
            message = "服務未在時限內回應\n" + self.console_output()
            self.__exit__(None, None, None)
            raise RuntimeError(message)
        return self

    def __exit__(self, *_):
        if self.process and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
        if self._console and not self._console.closed:
            self._console.close()
        shutil.rmtree(self.workdir, ignore_errors=True)
        return False

    def console_output(self):
        """讀取伺服器印到 console 的內容，用於診斷啟動失敗。"""
        try:
            with open(self.console_path, encoding="utf-8", errors="replace") as f:
                return f.read()
        except OSError:
            return ""

    def _build(self):
        binary = os.path.join(self.workdir, "backend.exe" if sys.platform == "win32" else "backend")
        result = subprocess.run(
            ["go", "build", "-o", binary, "."],
            cwd=BACKEND_DIR, capture_output=True, text=True,
        )
        if result.returncode != 0:
            raise RuntimeError("編譯失敗:\n" + result.stderr)
        return binary

    def _write_env(self):
        settings = {
            "USER_TOKEN": USER_TOKEN,
            "USER_NAME": USER_NAME,
            "PORT": self.port,
            "DB_DRIVER": "sqlite",
            "DB_DSN": os.path.join(self.workdir, "e2e.db"),
            "LOG_DIR": self.log_dir,
            "DEBUG": "true",
        }
        settings.update(self.env_extra)
        with open(os.path.join(self.workdir, ".env"), "w", encoding="utf-8") as f:
            for key, value in settings.items():
                f.write(f"{key}={value}\n")

    def _wait_until_up(self, timeout=30):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.process.poll() is not None:
                # stdout 導到檔案而非 PIPE，所以從檔案讀，不能用 communicate()。
                print("服務啟動後隨即結束:\n" + self.console_output())
                return False
            try:
                with urllib.request.urlopen(self.url + "/v1/health", timeout=1):
                    return True
            except (urllib.error.URLError, ConnectionError, OSError):
                time.sleep(0.2)
        return False

    def request(self, method, path, token=None, body=None):
        """發出請求，回傳 (狀態碼, 解析後的 body, headers)。連線層錯誤才會拋例外。"""
        data = None
        headers = {}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if token:
            headers["Authorization"] = "Bearer " + token

        req = urllib.request.Request(self.url + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, _parse(response.read()), dict(response.headers)
        except urllib.error.HTTPError as err:
            with err:
                return err.code, _parse(err.read()), dict(err.headers)

    def read_log(self, name):
        """讀取指定 logger 的所有日誌內容。"""
        content = ""
        if not os.path.isdir(self.log_dir):
            return content
        for entry in sorted(os.listdir(self.log_dir)):
            if entry.startswith(name + "-") and entry.endswith(".log"):
                with open(os.path.join(self.log_dir, entry), encoding="utf-8") as f:
                    content += f.read()
        return content


def _parse(raw):
    if not raw:
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return {"_raw": raw.decode("utf-8", "replace")}
