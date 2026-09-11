"""Step 5 驗證：撮合引擎在真實行情下觸發限價單與停損。

真實價格無法操控，所以用兩個確定會發生的情境：
  1. 限價買單掛在現價「上方」→ 下一輪掃描就成交，而且成交價是限價不是市價。
  2. 限價單上帶的 stop_loss 是對「限價」驗證的，可以設在現價上方；
     成交後現價已低於停損 → 再下一輪就觸發平倉。
停利是停損的鏡像，由 Go 單元測試覆蓋四個象限；這裡驗設定與移除。

執行方式：
    python backend/test/05_matching.py
"""

import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _harness import USER_TOKEN, Checks, Server  # noqa: E402

MAKER_FEE = 0.0005
TAKER_FEE = 0.001
QTY = 0.01
LEVERAGE = 10
INTERVAL = 0.5


def close_to(a, b, tol=1e-6):
    return abs(a - b) <= tol


def wait_until(fn, timeout=15, every=0.2):
    """輪詢直到 fn 回傳非 None 值。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        result = fn()
        if result is not None:
            return result
        time.sleep(every)
    return None


def main():
    check = Checks()
    env = {
        "FEE_RATE_MAKER": str(MAKER_FEE),
        "FEE_RATE_TAKER": str(TAKER_FEE),
        "MATCHING_INTERVAL": f"{INTERVAL}s",
    }

    with Server(port=7895, env_extra=env) as server:
        status, account, _ = server.request("POST", "/v1/accounts", USER_TOKEN,
                                            {"user_name": "BTC-Agent-01", "initial_balance": 1_000_000})
        if status != 201:
            check("建立交易帳號", False, f"{status} {account}")
            return check.report()
        token = account["token"]

        status, quote, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT", token)
        if status != 200:
            check("取得 BTCUSDT 現價", False, f"{status} {quote}；需要能連到 api.binance.com")
            return check.report()
        market_price = quote["price"]
        check("取得 BTCUSDT 現價", True)

        def order_status(order_id):
            _, body, _ = server.request("GET", f"/v1/orders/{order_id}", token)
            return body

        def positions():
            _, body, _ = server.request("GET", "/v1/positions", token)
            return body.get("positions", [])

        def order_reached(order_id, want):
            body = order_status(order_id)
            return body if body.get("status") == want else None

        def no_positions():
            return True if not positions() else None

        check.section("掛在現價上方的限價買單：下一輪就成交，成交價是限價")
        limit_price = round(market_price * 1.01, 2)
        stop_loss = round(market_price * 1.005, 2)
        status, order, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit",
            "quantity": QTY, "price": limit_price, "leverage": LEVERAGE, "stop_loss": stop_loss,
        })
        check("限價單建立回 201", status == 201, f"{status} {order}")
        check("建立時狀態為 open", order.get("status") == "open", str(order))

        filled = wait_until(lambda: order_reached(order["id"], "filled"))
        check("撮合引擎在時限內成交了限價單", filled is not None, "等了 15 秒仍未成交")
        if filled:
            check("成交價等於限價（不是市價）", close_to(filled["avg_fill_price"], limit_price),
                  f"{filled['avg_fill_price']} vs 限價 {limit_price}")
            check("掛單成交收 maker 費率",
                  close_to(filled["fee"], limit_price * QTY * MAKER_FEE, 1e-6),
                  f"{filled['fee']} vs {limit_price * QTY * MAKER_FEE}")

        check.section("停損已低於現價：再下一輪觸發平倉")
        gone = wait_until(no_positions)
        check("部位在時限內被平掉", gone is not None, f"部位仍在: {positions()}")

        _, body, _ = server.request("GET", "/v1/orders?status=filled", token)
        closing = [o for o in body.get("orders", []) if o.get("reduce_only")]
        check("有一筆 reduce_only 的平倉單", len(closing) == 1, str(body))
        if closing:
            c = closing[0]
            check("平倉單方向為賣出", c["side"] == "sell", str(c))
            check("平倉價低於停損價", c["avg_fill_price"] <= stop_loss,
                  f"{c['avg_fill_price']} 應 <= {stop_loss}")
            expected_pnl = (c["avg_fill_price"] - limit_price) * QTY
            check("已實現損益 = (平倉價 − 進場價) × 數量",
                  close_to(c["realized_pnl"], expected_pnl, 1e-4), f"{c['realized_pnl']} vs {expected_pnl}")
            check("停損觸發的平倉收 taker 費率",
                  close_to(c["fee"], c["avg_fill_price"] * QTY * TAKER_FEE, 1e-6), str(c))

        _, trades, _ = server.request("GET", "/v1/trades", token)
        roles = sorted(t["role"] for t in trades.get("trades", []))
        check("兩筆成交：一 maker 一 taker", roles == ["maker", "taker"], str(roles))

        check.section("空單鏡像：掛在現價下方的限價賣單 + 停損在現價上方")
        sell_limit = round(market_price * 0.99, 2)
        short_stop = round(market_price * 0.995, 2)
        status, short, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "futures", "side": "sell", "type": "limit",
            "quantity": QTY, "price": sell_limit, "leverage": LEVERAGE, "stop_loss": short_stop,
        })
        check("限價賣單建立", status == 201, f"{status} {short}")
        filled = wait_until(lambda: order_reached(short["id"], "filled"))
        check("限價賣單成交", filled is not None, "未成交")
        gone = wait_until(no_positions)
        check("空單被停損平掉", gone is not None, f"部位仍在: {positions()}")

        _, body, _ = server.request("GET", "/v1/orders?status=filled", token)
        short_close = [o for o in body.get("orders", []) if o.get("reduce_only") and o["side"] == "buy"]
        check("空單平倉單為買回", len(short_close) == 1, str(body))

        check.section("不會成交的限價單維持 open，可取消")
        status, parked, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit",
            "quantity": QTY, "price": round(market_price * 0.5, 2), "leverage": LEVERAGE,
        })
        check("遠離市價的限價單建立", status == 201, f"{status} {parked}")
        time.sleep(INTERVAL * 3)
        check("幾輪之後仍是 open", order_status(parked["id"]).get("status") == "open")
        status, canceled, _ = server.request("POST", f"/v1/orders/{parked['id']}/cancel", token)
        check("取消成功", status == 200 and canceled.get("status") == "canceled", f"{status} {canceled}")

        check.section("觸發時餘額不足：標記 rejected 並記原因，不重試")
        status, huge, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit",
            "quantity": 1000, "price": limit_price, "leverage": 1,
        })
        check("超額限價單可以掛上", status == 201, f"{status} {huge}")
        rejected = wait_until(lambda: order_reached(huge["id"], "rejected"))
        check("觸發後被標記為 rejected", rejected is not None, str(order_status(huge["id"])))
        if rejected:
            check("記下拒絕原因", bool(rejected.get("reject_reason")), str(rejected))
        _, summary, _ = server.request("GET", "/v1/account", token)
        check("被拒絕的單沒有動到保證金", close_to(summary["locked_margin"], 0), str(summary))

        check.section("停損停利的設定、更新、移除")
        status, opened, _ = server.request("POST", "/v1/orders", token, {
            "symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market",
            "quantity": QTY, "leverage": LEVERAGE,
        })
        check("市價開一個多單", status == 201, f"{status} {opened}")
        pos = positions()[0]
        entry = opened["avg_fill_price"]
        path = f"/v1/positions/{pos['id']}"

        status, updated, _ = server.request("PATCH", path, token, {
            "stop_loss": round(entry * 0.9, 2), "take_profit": round(entry * 1.1, 2),
        })
        check("設定停損停利回 200", status == 200, f"{status} {updated}")
        check("回應帶回設定值",
              close_to(updated.get("stop_loss", 0), round(entry * 0.9, 2)) and
              close_to(updated.get("take_profit", 0), round(entry * 1.1, 2)), str(updated))

        status, updated, _ = server.request("PATCH", path, token, {"take_profit": round(entry * 1.2, 2)})
        check("只更新停利", status == 200 and close_to(updated.get("take_profit", 0), round(entry * 1.2, 2)),
              str(updated))
        check("停損不受影響", close_to(updated.get("stop_loss", 0), round(entry * 0.9, 2)), str(updated))

        status, updated, _ = server.request("PATCH", path, token, {"stop_loss": 0})
        check("傳 0 移除停損", status == 200 and "stop_loss" not in updated, str(updated))

        status, err, _ = server.request("PATCH", path, token, {"stop_loss": round(entry * 1.5, 2)})
        check("多單停損高於現價回 400", status == 400, f"{status} {err}")
        status, err, _ = server.request("PATCH", path, token, {})
        check("空的修改回 400", status == 400, f"{status} {err}")

        time.sleep(INTERVAL * 3)
        check("遠離現價的停損停利不會誤觸發", len(positions()) == 1, str(positions()))

        status, closed, _ = server.request("POST", f"{path}/close", token)
        check("手動平倉仍然可用", status == 200, f"{status} {closed}")
        check("手動平倉單也是 reduce_only", closed.get("reduce_only") is True, str(closed))

        check.section("日誌")
        matching_log = server.read_log("matching")
        check("撮合事件記在自己的 log", "成交" in matching_log and "觸發" in matching_log,
              "matching log 缺少成交或觸發紀錄")
        check("拒絕也有記錄", "拒絕" in matching_log)

    return check.report()


if __name__ == "__main__":
    sys.exit(main())
