"""依序執行所有端到端測試，任何一支失敗即以非零退出。

執行方式：
    python backend/test/run_all.py
"""

import os
import subprocess
import sys
import time

TEST_DIR = os.path.dirname(os.path.abspath(__file__))

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
SCRIPTS = [
    "01_health.py",
    "02_account.py",
    "03_market.py",
    "04_trading.py",
    "05_matching.py",
    "06_message.py",
]


def main():
    failed = []
    started = time.time()
    for script in SCRIPTS:
        print(f"\n{'=' * 60}\n{script}\n{'=' * 60}")
        result = subprocess.run([sys.executable, os.path.join(TEST_DIR, script)])
        if result.returncode != 0:
            failed.append(script)

    elapsed = time.time() - started
    print(f"\n{'=' * 60}")
    if failed:
        print(f"FAIL: {len(failed)}/{len(SCRIPTS)} 支未通過 -> {', '.join(failed)}（{elapsed:.0f}s）")
        return 1
    print(f"PASS: {len(SCRIPTS)} 支全部通過（{elapsed:.0f}s）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
