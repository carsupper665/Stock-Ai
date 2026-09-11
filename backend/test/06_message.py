"""Step 6 驗證：留言板的公開讀取、token 發布、以及 "you" 依查詢者身分變化。

執行方式：
    python backend/test/06_message.py
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _harness import USER_NAME, USER_TOKEN, Checks, Server  # noqa: E402


def names(body):
    return [m.get("user_name") for m in body.get("messages", [])]


def main():
    check = Checks()

    with Server(port=7896) as server:
        accounts = {}
        for name, balance in [("BTC-Agent-01", 1000), ("ETH-Agent-02", 2000)]:
            status, acc, _ = server.request("POST", "/v1/accounts", USER_TOKEN,
                                            {"user_name": name, "initial_balance": balance})
            if status != 201:
                check(f"建立帳號 {name}", False, f"{status} {acc}")
                return check.report()
            accounts[name] = acc["token"]
        alpha, beta = accounts["BTC-Agent-01"], accounts["ETH-Agent-02"]
        check("建立兩個帳號", True)

        check.section("發布留言")
        status, posted, _ = server.request("POST", "/v1/messages", USER_TOKEN, {"content": "Market looks choppy today."})
        check("USER 發布回 201", status == 201, f"{status} {posted}")
        check("回應以 you 顯示自己", posted.get("user_name") == "you", str(posted))
        check("回應帶 id 與 created_at",
              str(posted.get("id", "")).startswith("msg_") and bool(posted.get("created_at")), str(posted))
        check("不外露 author 欄位", "author_id" not in posted and "author_type" not in posted, str(posted))

        status, _, _ = server.request("POST", "/v1/messages", alpha, {"content": "BTC breakout looks valid."})
        check("帳號 A 發布回 201", status == 201, f"實際 {status}")
        status, _, _ = server.request("POST", "/v1/messages", beta, {"content": "ETH is lagging."})
        check("帳號 B 發布回 201", status == 201, f"實際 {status}")

        status, err, _ = server.request("POST", "/v1/messages", None, {"content": "anon"})
        check("未帶 token 發布回 401", status == 401, f"{status} {err}")
        status, err, _ = server.request("POST", "/v1/messages", "at_bogus", {"content": "spoof"})
        check("無效 token 發布回 401", status == 401, f"{status} {err}")
        status, err, _ = server.request("POST", "/v1/messages", alpha, {"content": "hi", "author_name": "Bless"})
        check("自帶 author_name 回 400", status == 400, f"{status} {err}")
        status, err, _ = server.request("POST", "/v1/messages", alpha, {"content": "   "})
        check("空白內容回 400", status == 400, f"{status} {err}")

        check.section("同一份留言，不同身分看到的 you 位置不同（規格 §16）")
        expected = {
            "匿名": (None, [USER_NAME, "BTC-Agent-01", "ETH-Agent-02"]),
            "USER": (USER_TOKEN, ["you", "BTC-Agent-01", "ETH-Agent-02"]),
            "帳號 A": (alpha, [USER_NAME, "you", "ETH-Agent-02"]),
            "帳號 B": (beta, [USER_NAME, "BTC-Agent-01", "you"]),
        }
        contents = None
        for label, (token, want) in expected.items():
            status, body, _ = server.request("GET", "/v1/messages?order=asc", token)
            check(f"{label} 查詢回 200", status == 200, f"{status} {body}")
            check(f"{label} 看到的名稱為 {want}", names(body) == want, str(names(body)))
            got_contents = [m.get("content") for m in body.get("messages", [])]
            if contents is None:
                contents = got_contents
            check(f"{label} 看到相同的內容與順序", got_contents == contents, str(got_contents))

        check.section("無效 token 不可以被當成匿名（規格 §17）")
        status, err, _ = server.request("GET", "/v1/messages", "at_bogus")
        check("帶錯 token 查詢回 401", status == 401, f"{status} {err}")

        check.section("分頁與排序（規格 §15）")
        for i in range(35):
            server.request("POST", "/v1/messages", alpha, {"content": f"tick {i:02d}"})

        _, body, _ = server.request("GET", "/v1/messages")
        check("預設最多 30 則", len(body.get("messages", [])) == 30, f"實際 {len(body.get('messages', []))}")
        check("預設最新在前", body["messages"][0]["content"] == "tick 34", body["messages"][0]["content"])

        _, body, _ = server.request("GET", "/v1/messages?limit=100")
        check("limit 超過 30 被壓回 30", len(body.get("messages", [])) == 30, f"實際 {len(body.get('messages', []))}")

        _, body, _ = server.request("GET", "/v1/messages?order=asc&limit=2")
        check("asc 由舊到新", [m["content"] for m in body["messages"]] == ["Market looks choppy today.", "BTC breakout looks valid."],
              str([m["content"] for m in body["messages"]]))

        _, page1, _ = server.request("GET", "/v1/messages?order=asc&limit=5")
        cursor = page1["messages"][-1]["created_at"]
        _, page2, _ = server.request("GET", f"/v1/messages?order=asc&limit=5&after={cursor}")
        check("用 after 接續分頁不重複",
              page2["messages"] and page2["messages"][0]["content"] != page1["messages"][-1]["content"],
              f"page1 尾 {page1['messages'][-1]['content']} / page2 首 {page2['messages'][0]['content'] if page2['messages'] else None}")
        check("時間為 UTC RFC3339", cursor.endswith("Z"), cursor)

        _, older, _ = server.request("GET", f"/v1/messages?before={cursor}&order=asc")
        check("before 只回傳游標之前的", len(older["messages"]) == 4, f"實際 {len(older['messages'])}")

        for bad in ["limit=0", "limit=abc", "order=sideways", "after=yesterday"]:
            status, _, _ = server.request("GET", f"/v1/messages?{bad}")
            check(f"{bad} 回 400", status == 400, f"實際 {status}")

        check.section("作者帳號被刪除後留言仍在")
        _, acc_list, _ = server.request("GET", "/v1/accounts", USER_TOKEN)
        beta_id = next(a["id"] for a in acc_list["accounts"] if a["user_name"] == "ETH-Agent-02")
        status, _, _ = server.request("DELETE", f"/v1/accounts/{beta_id}", USER_TOKEN)
        check("刪除帳號 B", status == 204, f"實際 {status}")
        _, body, _ = server.request("GET", "/v1/messages?order=asc&limit=3")
        check("留言仍可查到、名稱改為 [deleted]", names(body) == [USER_NAME, "BTC-Agent-01", "[deleted]"], str(names(body)))

    return check.report()


if __name__ == "__main__":
    sys.exit(main())
