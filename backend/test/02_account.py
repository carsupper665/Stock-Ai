"""Step 2 驗證：Token 驗證與虛擬帳號管理的完整流程。

驗證重點是權限邊界：
  USER Token    -> 管理所有帳號、看得到 Account Token
  Account Token -> 只能查自己、看不到自己的 token、碰不到管理端點
  無效 Token    -> 一律 401，不會被當成匿名

執行方式：
    python backend/test/02_account.py
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _harness import USER_TOKEN, Checks, Server  # noqa: E402

BOGUS_TOKEN = "at_this_token_does_not_exist"


def main():
    check = Checks()

    with Server(port=7892) as server:
        check.section("USER 建立虛擬帳號")
        status, alpha, _ = server.request("POST", "/v1/accounts", USER_TOKEN, {
            "user_name": "BTC-Agent-01", "initial_balance": 10000,
        })
        check("建立帳號回 201", status == 201, f"{status} {alpha}")
        check("回傳帳號 id", str(alpha.get("id", "")).startswith("acc_"), str(alpha))
        check("回傳 Account Token", str(alpha.get("token", "")).startswith("at_"), str(alpha))
        check("餘額等於初始餘額", alpha.get("balance") == 10000, str(alpha))
        check("新帳號狀態為 active", alpha.get("status") == "active", str(alpha))

        alpha_id, alpha_token = alpha.get("id"), alpha.get("token")

        status, beta, _ = server.request("POST", "/v1/accounts", USER_TOKEN, {
            "user_name": "ETH-Agent-02", "initial_balance": 5000,
        })
        check("建立第二個帳號回 201", status == 201, f"{status} {beta}")
        beta_id, beta_token = beta.get("id"), beta.get("token")
        check("兩個帳號的 token 不同", alpha_token != beta_token)

        check.section("輸入驗證")
        cases = [
            ("名稱空白回 400", {"user_name": "   ", "initial_balance": 100}, 400),
            ("初始餘額為 0 回 400", {"user_name": "zero", "initial_balance": 0}, 400),
            ("初始餘額為負回 400", {"user_name": "neg", "initial_balance": -1}, 400),
            ("名稱重複回 409", {"user_name": "BTC-Agent-01", "initial_balance": 100}, 409),
        ]
        for label, payload, want in cases:
            status, body, _ = server.request("POST", "/v1/accounts", USER_TOKEN, payload)
            check(label, status == want, f"實際 {status} {body}")
            check(f"{label} 附帶錯誤說明", bool(body.get("error")) and bool(body.get("message")), str(body))

        check.section("USER 查詢所有帳號")
        status, body, _ = server.request("GET", "/v1/accounts", USER_TOKEN)
        accounts = body.get("accounts", [])
        check("列出帳號回 200", status == 200, f"{status} {body}")
        check("列出兩個帳號", len(accounts) == 2, f"實際 {len(accounts)}")
        check("每筆都帶 token（只有 USER 看得到）",
              all(str(a.get("token", "")).startswith("at_") for a in accounts), str(accounts))

        status, body, _ = server.request("GET", f"/v1/accounts/{alpha_id}", USER_TOKEN)
        check("USER 查單一帳號回 200", status == 200, f"{status} {body}")
        check("USER 看得到該帳號的 token", body.get("token") == alpha_token, str(body))

        check.section("Account Token 只能查自己")
        status, body, _ = server.request("GET", "/v1/account", alpha_token)
        check("帳號查自己回 200", status == 200, f"{status} {body}")
        check("查到的是自己", body.get("id") == alpha_id, str(body))
        check("自查看不到自己的 token", "token" not in body, str(body))

        status, _, _ = server.request("GET", "/v1/account", beta_token)
        check("另一個帳號查到的是它自己", status == 200)

        check.section("Account Token 碰不到管理端點")
        admin_calls = [
            ("POST", "/v1/accounts", {"user_name": "sneaky", "initial_balance": 1}),
            ("GET", "/v1/accounts", None),
            ("GET", f"/v1/accounts/{beta_id}", None),
            ("PATCH", f"/v1/accounts/{beta_id}", {"status": "disabled"}),
            ("DELETE", f"/v1/accounts/{beta_id}", None),
            ("POST", f"/v1/accounts/{beta_id}/token/reset", None),
        ]
        for method, path, payload in admin_calls:
            status, _, _ = server.request(method, path, alpha_token, payload)
            check(f"Account Token {method} {path} 回 403", status == 403, f"實際 {status}")

        check.section("無效與缺漏的 Token")
        for method, path, payload in admin_calls:
            status, _, _ = server.request(method, path, None, payload)
            check(f"無 Token {method} {path} 回 401", status == 401, f"實際 {status}")

        status, _, _ = server.request("GET", "/v1/accounts", BOGUS_TOKEN)
        check("偽造的 Token 回 401", status == 401, f"實際 {status}")
        status, _, _ = server.request("GET", "/v1/account", BOGUS_TOKEN)
        check("偽造的 Token 查自己也回 401", status == 401, f"實際 {status}")

        check.section("換發 Token")
        status, body, _ = server.request("POST", f"/v1/accounts/{alpha_id}/token/reset", USER_TOKEN)
        rotated = body.get("token")
        check("換發回 200", status == 200, f"{status} {body}")
        check("換發後 token 改變", rotated and rotated != alpha_token, str(body))
        check("帳號 id 不變", body.get("id") == alpha_id, str(body))

        status, _, _ = server.request("GET", "/v1/account", alpha_token)
        check("舊 token 立即失效", status == 401, f"實際 {status}")
        status, _, _ = server.request("GET", "/v1/account", rotated)
        check("新 token 可用", status == 200, f"實際 {status}")
        alpha_token = rotated

        check.section("停用帳號")
        status, body, _ = server.request("PATCH", f"/v1/accounts/{beta_id}", USER_TOKEN, {"status": "disabled"})
        check("停用回 200", status == 200, f"{status} {body}")
        check("狀態變成 disabled", body.get("status") == "disabled", str(body))

        status, _, _ = server.request("GET", "/v1/account", beta_token)
        check("停用後該帳號的 token 失效", status == 401, f"實際 {status}")

        status, _, _ = server.request("PATCH", f"/v1/accounts/{beta_id}", USER_TOKEN, {"status": "active"})
        check("重新啟用回 200", status == 200)
        status, _, _ = server.request("GET", "/v1/account", beta_token)
        check("重新啟用後 token 恢復可用", status == 200, f"實際 {status}")

        status, _, _ = server.request("PATCH", f"/v1/accounts/{beta_id}", USER_TOKEN, {"status": "paused"})
        check("未知狀態回 400", status == 400, f"實際 {status}")

        check.section("改名")
        status, body, _ = server.request("PATCH", f"/v1/accounts/{beta_id}", USER_TOKEN, {"user_name": "ETH-Agent-renamed"})
        check("改名回 200", status == 200, f"{status} {body}")
        check("名稱已更新", body.get("user_name") == "ETH-Agent-renamed", str(body))
        check("改名不影響 token", body.get("token") == beta_token, str(body))

        check.section("刪除帳號")
        status, _, _ = server.request("DELETE", f"/v1/accounts/{beta_id}", USER_TOKEN)
        check("刪除回 204", status == 204, f"實際 {status}")
        status, _, _ = server.request("GET", f"/v1/accounts/{beta_id}", USER_TOKEN)
        check("刪除後查詢回 404", status == 404, f"實際 {status}")
        status, _, _ = server.request("GET", "/v1/account", beta_token)
        check("刪除後該 token 失效", status == 401, f"實際 {status}")

        status, _, _ = server.request("DELETE", "/v1/accounts/acc_missing", USER_TOKEN)
        check("刪除不存在的帳號回 404", status == 404, f"實際 {status}")

        check.section("資料落地")
        status, body, _ = server.request("GET", "/v1/accounts", USER_TOKEN)
        check("刪除後只剩一個帳號", len(body.get("accounts", [])) == 1, str(body))

        db_log = server.read_log("db")
        check("db 日誌記錄了 SQL", "accounts" in db_log, "db 日誌沒有帳號相關查詢")

    return check.report()


if __name__ == "__main__":
    sys.exit(main())
