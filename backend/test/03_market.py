"""Step 3 驗證：Market Runtime 的快取、訂閱與閒置回收。

這支會連到真實的 api.binance.com。沒有網路時第一項就會失敗，
並且會明確說明原因，不會誤判成程式壞掉。

閒置逾時在 .env 調成 3 秒（規格 §11 的 60 秒標示為「暫定」），
否則這支測試要等一分鐘。

執行方式：
    python backend/test/03_market.py
"""

import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _harness import USER_TOKEN, Checks, Server  # noqa: E402

IDLE_TIMEOUT = 3
SYMBOL_KEY = "crypto:BTCUSDT"


def subscriptions(server):
    _, body, _ = server.request("GET", "/v1/market/subscriptions", USER_TOKEN)
    return body.get("subscriptions", {})


def wait_for_state(server, key, want, timeout=15):
    """輪詢訂閱狀態直到符合預期。這裡不要求價格，避免把訂閱又叫醒。"""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = subscriptions(server).get(key)
        if last == want:
            return True, last
        time.sleep(0.3)
    return False, last


def main():
    check = Checks()
    env = {
        "MARKET_IDLE_TIMEOUT": f"{IDLE_TIMEOUT}s",
        "MARKET_FRESH_TTL": "1.5s",
        "MARKET_WAIT_TIMEOUT": "10s",
    }

    with Server(port=7893, env_extra=env) as server:
        check.section("啟動時沒有任何訂閱（規格 §10）")
        check("訂閱清單是空的", subscriptions(server) == {}, str(subscriptions(server)))

        check.section("第一次取價會建立訂閱")
        status, body, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT", USER_TOKEN)
        if status != 200:
            check("向 Binance 取得 BTCUSDT 報價", False,
                  f"HTTP {status} {body}；這支測試需要能連到 api.binance.com")
            return check.report()

        price = body.get("price")
        check("向 Binance 取得 BTCUSDT 報價", True)
        check("價格是正數", isinstance(price, (int, float)) and price > 0, str(body))
        check("價格落在合理範圍", 1000 < price < 10_000_000, f"BTC 報價 {price} 看起來不對")
        check("回應帶有 market 與 symbol",
              body.get("market") == "crypto" and body.get("symbol") == "BTCUSDT", str(body))
        check("回應帶有更新時間", bool(body.get("updated_at")), str(body))
        check("訂閱已啟動", subscriptions(server).get(SYMBOL_KEY) == "active",
              str(subscriptions(server)))

        check.section("新鮮期內直接吃快取（規格 §9）")
        start = time.time()
        _, cached, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT", USER_TOKEN)
        elapsed = time.time() - start
        check("立刻再查會很快回應", elapsed < 0.5, f"花了 {elapsed:.3f}s")
        check("回傳的是同一筆快取",
              cached.get("updated_at") == body.get("updated_at"),
              f"{cached.get('updated_at')} vs {body.get('updated_at')}")

        check.section("小寫與空白會正規化")
        _, body2, _ = server.request("GET", "/v1/market/price?symbol=%20btcusdt%20", USER_TOKEN)
        check("小寫 symbol 可查詢", body2.get("symbol") == "BTCUSDT", str(body2))
        check("沒有因為大小寫多開一個訂閱",
              list(subscriptions(server).keys()) == [SYMBOL_KEY], str(subscriptions(server)))

        check.section("價格會持續更新")
        time.sleep(2)  # 超過 1.5s 新鮮期，會等一筆新價
        _, fresh, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT", USER_TOKEN)
        check("過了新鮮期會拿到新的報價",
              fresh.get("updated_at") != body.get("updated_at"),
              f"更新時間沒有變: {fresh.get('updated_at')}")

        check.section(f"閒置 {IDLE_TIMEOUT}s 後停止訂閱（規格 §11）")
        ok, state = wait_for_state(server, SYMBOL_KEY, "inactive")
        check("訂閱被回收", ok, f"最後狀態是 {state}")
        check("快取項目仍然保留", SYMBOL_KEY in subscriptions(server),
              "規格只要求停訂閱，沒有要求清掉快取")

        check.section("停用後快取還新鮮：直接命中，不重新訂閱")
        # 訂閱是在最後一筆報價寫入後才被回收的，所以此時快取通常仍在新鮮期內。
        # 規格 §9 的第一步就是查快取，這種情況不該再開訂閱。
        status, cached_after, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT", USER_TOKEN)
        check("停用後仍取得報價", status == 200, f"{status} {cached_after}")
        check("命中快取時維持 inactive",
              subscriptions(server).get(SYMBOL_KEY) == "inactive",
              f"快取還新鮮卻重新訂閱了: {subscriptions(server)}")

        check.section("快取過期後再取價才重新啟動訂閱")
        time.sleep(2)  # 超過 1.5s 新鮮期
        status, body3, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT", USER_TOKEN)
        check("重新取價成功", status == 200, f"{status} {body3}")
        check("訂閱回到 active", subscriptions(server).get(SYMBOL_KEY) == "active",
              str(subscriptions(server)))
        check("拿到的是新報價", body3.get("updated_at") != cached_after.get("updated_at"),
              f"更新時間沒有變: {body3.get('updated_at')}")

        check.section("多個標的各自獨立")
        status, eth, _ = server.request("GET", "/v1/market/price?symbol=ETHUSDT", USER_TOKEN)
        check("取得 ETHUSDT 報價", status == 200, f"{status} {eth}")
        if status == 200:
            check("ETH 與 BTC 價格不同", eth.get("price") != body3.get("price"),
                  "兩個標的拿到同一個價格，快取可能共用了")
        subs = subscriptions(server)
        check("兩個標的各有一筆訂閱",
              "crypto:ETHUSDT" in subs and SYMBOL_KEY in subs, str(subs))

        check.section("錯誤處理")
        cases = [
            ("缺少 symbol 回 400", "/v1/market/price", 400),
            ("symbol 只有空白回 400", "/v1/market/price?symbol=%20", 400),
            ("未支援的市場回 400", "/v1/market/price?market=forex&symbol=EURUSD", 400),
            ("美股尚未接上回 501", "/v1/market/price?market=stock&symbol=AAPL", 501),
        ]
        for label, path, want in cases:
            status, err, _ = server.request("GET", path, USER_TOKEN)
            check(label, status == want, f"實際 {status} {err}")
            check(f"{label} 附帶錯誤說明", bool(err.get("error")) and bool(err.get("message")), str(err))

        check.section("權限")
        status, account, _ = server.request("POST", "/v1/accounts", USER_TOKEN,
                                            {"user_name": "BTC-Agent-01", "initial_balance": 1000})
        account_token = account.get("token")
        check("建立帳號", status == 201, f"{status} {account}")

        status, _, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT", account_token)
        check("Account Token 讀得到行情", status == 200, f"實際 {status}")
        status, _, _ = server.request("GET", "/v1/market/price?symbol=BTCUSDT")
        check("未帶 Token 回 401", status == 401, f"實際 {status}")
        status, _, _ = server.request("GET", "/v1/market/subscriptions", account_token)
        check("Account Token 查不了訂閱狀態", status == 403, f"實際 {status}")

        check.section("日誌")
        market_log = server.read_log("market")
        check("行情事件記在自己的 log", "閒置逾時" in market_log,
              "market log 沒有記錄閒置回收")
        check("行情 log 沒有混進 API 請求", "/v1/market/price" not in market_log,
              "market log 出現了 API 請求")

    return check.report()


if __name__ == "__main__":
    sys.exit(main())
