"""Step 4 驗證：市價下單、部位、餘額、手續費與平倉。

用真實 Binance 報價，因此不驗絕對數字，驗的是關係：
保證金 = 數量×進場價÷槓桿、餘額變動 = 已實現損益 − 手續費、平倉後保證金歸零。

執行方式：
    python backend/test/04_trading.py
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _harness import USER_TOKEN, Checks, Server  # noqa: E402

MAKER_FEE = 0.0005
TAKER_FEE = 0.001
BALANCE = 1_000_000
QTY = 0.01
LEVERAGE = 10


def close_to(a, b, tol=1e-6):
    return abs(a - b) <= tol


def main():
    check = Checks()
    env = {"FEE_RATE_MAKER": str(MAKER_FEE), "FEE_RATE_TAKER": str(TAKER_FEE)}

    with Server(port=7894, env_extra=env) as server:
        status, account, _ = server.request("POST", "/v1/accounts", USER_TOKEN,
                                            {"user_name": "BTC-Agent-01", "initial_balance": BALANCE})
        if status != 201:
            check("建立交易帳號", False, f"{status} {account}")
            return check.report()
        token = account["token"]
        account_id = account["id"]
        check("建立交易帳號", True)

        check.section("市價開倉")
        status, order, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market",
            "quantity": QTY, "leverage": LEVERAGE,
        })
        if status != 201:
            check("市價買進 BTCUSDT", False, f"{status} {order}；這支測試需要能連到 api.binance.com")
            return check.report()

        entry = order["avg_fill_price"]
        open_fee = order["fee"]
        check("市價買進 BTCUSDT", True)
        check("訂單立刻成交", order["status"] == "filled", str(order))
        check("成交價是正數", entry > 0, str(order))
        check("手續費 = 名目金額 × taker 費率",
              close_to(open_fee, entry * QTY * TAKER_FEE), f"{open_fee} vs {entry * QTY * TAKER_FEE}")
        check("開倉沒有已實現損益", order["realized_pnl"] == 0, str(order))

        check.section("部位")
        status, body, _ = server.request("GET", "/v1/positions", token)
        positions = body.get("positions", [])
        check("有一個部位", status == 200 and len(positions) == 1, f"{status} {body}")
        position = positions[0]
        position_id = position["id"]
        expected_margin = QTY * position["entry_price"] / LEVERAGE
        check("方向為 long", position["side"] == "long", str(position))
        check("數量正確", close_to(position["quantity"], QTY), str(position))
        check("保證金 = 數量×進場價÷槓桿",
              close_to(position["margin"], expected_margin, 1e-4), f"{position['margin']} vs {expected_margin}")
        check("帶有現價", position["mark_price"] > 0, str(position))

        check.section("帳務總覽")
        status, summary, _ = server.request("GET", "/v1/account", token)
        check("查詢帳務回 200", status == 200, f"{status} {summary}")
        check("餘額 = 初始 − 手續費",
              close_to(summary["balance"], BALANCE - open_fee, 1e-4),
              f"{summary['balance']} vs {BALANCE - open_fee}")
        check("保證金被鎖住", close_to(summary["locked_margin"], position["margin"], 1e-4), str(summary))
        check("可用 = 餘額 − 保證金",
              close_to(summary["available"], summary["balance"] - summary["locked_margin"], 1e-4), str(summary))
        check("權益 = 餘額 + 浮動損益",
              close_to(summary["equity"], summary["balance"] + summary["unrealized_pnl"], 1e-4), str(summary))
        check("自查看不到 token", "token" not in summary, str(summary))

        check.section("部分平倉")
        status, half, _ = server.request("POST", f"/v1/positions/{position_id}/close", token,
                                         {"quantity": QTY / 2})
        check("部分平倉回 200", status == 200, f"{status} {half}")
        check("平倉單方向相反", half["side"] == "sell", str(half))
        check("平倉數量正確", close_to(half["quantity"], QTY / 2), str(half))

        _, body, _ = server.request("GET", "/v1/positions", token)
        remaining = body["positions"][0]
        check("剩餘數量減半", close_to(remaining["quantity"], QTY / 2), str(remaining))
        check("部分平倉不改變進場價",
              close_to(remaining["entry_price"], position["entry_price"]), str(remaining))

        check.section("全部平倉")
        balance_before = summary["balance"]
        status, rest, _ = server.request("POST", f"/v1/positions/{position_id}/close", token)
        check("不帶數量即全平", status == 200, f"{status} {rest}")

        _, body, _ = server.request("GET", "/v1/positions", token)
        check("部位已清空", body.get("positions") == [], str(body))

        _, final, _ = server.request("GET", "/v1/account", token)
        realized = half["realized_pnl"] + rest["realized_pnl"]
        fees = half["fee"] + rest["fee"]
        check("餘額變動 = 已實現損益 − 手續費",
              close_to(final["balance"], balance_before + realized - fees, 1e-4),
              f"{final['balance']} vs {balance_before + realized - fees}")
        check("保證金歸零", close_to(final["locked_margin"], 0), str(final))
        check("浮動損益歸零", close_to(final["unrealized_pnl"], 0), str(final))

        check.section("歷史紀錄")
        _, body, _ = server.request("GET", "/v1/trades", token)
        trades = body.get("trades", [])
        check("三筆成交（開倉、半平、全平）", len(trades) == 3, f"實際 {len(trades)}")
        check("市價成交都是 taker", all(t["role"] == "taker" for t in trades), str(trades))

        _, body, _ = server.request("GET", "/v1/orders", token)
        orders = body.get("orders", [])
        check("三筆訂單", len(orders) == 3, f"實際 {len(orders)}")
        check("全部已成交", all(o["status"] == "filled" for o in orders), str(orders))

        check.section("限價單先掛著")
        status, limit, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit",
            "quantity": QTY, "price": round(entry * 0.5, 2), "leverage": LEVERAGE,
        })
        check("限價單建立回 201", status == 201, f"{status} {limit}")
        check("狀態維持 open", limit.get("status") == "open", str(limit))

        _, body, _ = server.request("GET", "/v1/account", token)
        check("未成交的限價單不佔保證金", close_to(body["locked_margin"], 0), str(body))

        status, canceled, _ = server.request("POST", f"/v1/orders/{limit['id']}/cancel", token)
        check("取消限價單回 200", status == 200, f"{status} {canceled}")
        check("狀態變成 canceled", canceled.get("status") == "canceled", str(canceled))

        check.section("現貨")
        status, spot, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "spot", "side": "buy", "type": "market", "quantity": QTY,
        })
        check("現貨買進回 201", status == 201, f"{status} {spot}")
        check("現貨槓桿為 1", spot.get("leverage") == 1, str(spot))

        _, body, _ = server.request("GET", "/v1/positions?product=spot", token)
        spot_positions = body.get("positions", [])
        check("現貨部位獨立於合約", len(spot_positions) == 1, str(body))
        if spot_positions:
            check("現貨鎖住全額",
                  close_to(spot_positions[0]["margin"], QTY * spot_positions[0]["entry_price"], 1e-4),
                  str(spot_positions[0]))

        status, err, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "spot", "side": "sell", "type": "market", "quantity": QTY * 10,
        })
        check("現貨賣超過持倉回 400", status == 400, f"{status} {err}")
        status, err, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "spot", "side": "buy", "type": "market",
            "quantity": QTY, "leverage": 5,
        })
        check("現貨帶槓桿回 400", status == 400, f"{status} {err}")

        check.section("輸入驗證")
        cases = [
            ("缺少 symbol 回 400", {"side": "buy", "quantity": 1}, 400),
            ("數量為零回 400", {"symbol": "BTCUSDT", "side": "buy", "quantity": 0}, 400),
            ("未知 side 回 400", {"symbol": "BTCUSDT", "side": "hold", "quantity": 1}, 400),
            ("槓桿過高回 400", {"symbol": "BTCUSDT", "side": "buy", "quantity": 1, "leverage": 500}, 400),
            ("限價單缺價格回 400",
             {"symbol": "BTCUSDT", "side": "buy", "type": "limit", "quantity": 1}, 400),
            ("餘額不足回 422",
             {"symbol": "BTCUSDT", "side": "buy", "quantity": 10000, "leverage": 1}, 422),
            ("停損方向錯誤回 400",
             {"symbol": "BTCUSDT", "side": "buy", "quantity": QTY, "leverage": LEVERAGE,
              "stop_loss": entry * 2}, 400),
        ]
        for label, payload, want in cases:
            status, err, _ = server.request("POST", "/v1/orders", token, payload)
            check(label, status == want, f"實際 {status} {err}")

        check.section("權限")
        status, _, _ = server.request("POST", "/v1/orders", USER_TOKEN, {
            "symbol": "BTCUSDT", "side": "buy", "quantity": QTY, "leverage": LEVERAGE,
        })
        check("USER 不能代下單（回 403）", status == 403, f"實際 {status}")
        status, _, _ = server.request("GET", "/v1/positions")
        check("未帶 token 回 401", status == 401, f"實際 {status}")

        status, body, _ = server.request("GET", f"/v1/accounts/{account_id}/trades", USER_TOKEN)
        check("USER 查得到帳號的成交紀錄", status == 200 and len(body.get("trades", [])) >= 3,
              f"{status} {body}")
        status, _, _ = server.request("GET", f"/v1/accounts/{account_id}/trades", token)
        check("Account Token 走 USER 路徑回 403", status == 403, f"實際 {status}")

    return check.report()


if __name__ == "__main__":
    sys.exit(main())
