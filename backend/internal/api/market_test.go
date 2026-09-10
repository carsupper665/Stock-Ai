package api

import (
	"net/http"
	"testing"
)

func TestMarketPriceRequiresToken(t *testing.T) {
	engine, _ := newTestServer(t)
	_, accountToken := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	// USER 與 Account 都讀得到行情。
	for label, token := range map[string]string{"USER": testUserToken, "Account": accountToken} {
		status, body := do(t, engine, http.MethodGet, "/v1/market/price?symbol=BTCUSDT", token, nil)
		if status != http.StatusOK {
			t.Fatalf("%s 取得行情應回 200，得到 %d: %v", label, status, body)
		}
		if body["price"] != testPrice {
			t.Fatalf("%s 取得的價格不符: %v", label, body)
		}
		if body["symbol"] != "BTCUSDT" || body["market"] != "crypto" {
			t.Fatalf("%s 回應缺少市場或標的: %v", label, body)
		}
	}

	if status, _ := do(t, engine, http.MethodGet, "/v1/market/price?symbol=BTCUSDT", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("未帶 token 應回 401，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/market/price?symbol=BTCUSDT", "at_bogus", nil); status != http.StatusUnauthorized {
		t.Fatalf("無效 token 應回 401，得到 %d", status)
	}
}

func TestMarketPriceNormalizesSymbol(t *testing.T) {
	engine, _ := newTestServer(t)

	status, body := do(t, engine, http.MethodGet, "/v1/market/price?symbol=btcusdt", testUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("小寫 symbol 應可查詢，得到 %d: %v", status, body)
	}
	if body["symbol"] != "BTCUSDT" {
		t.Fatalf("回應中的 symbol 應正規化為大寫: %v", body)
	}
}

func TestMarketPriceRejectsBadInput(t *testing.T) {
	engine, _ := newTestServer(t)

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"缺少 symbol", "/v1/market/price", http.StatusBadRequest},
		{"symbol 只有空白", "/v1/market/price?symbol=%20%20", http.StatusBadRequest},
		{"未支援的市場", "/v1/market/price?market=forex&symbol=EURUSD", http.StatusBadRequest},
		{"美股尚未接上", "/v1/market/price?market=stock&symbol=AAPL", http.StatusNotImplemented},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, engine, http.MethodGet, tc.query, testUserToken, nil)
			if status != tc.want {
				t.Fatalf("預期 %d，得到 %d: %v", tc.want, status, body)
			}
			if body["error"] == nil || body["message"] == nil {
				t.Fatalf("錯誤回應缺少 error 或 message: %v", body)
			}
		})
	}
}

func TestSubscriptionsAreUserOnlyAndStartEmpty(t *testing.T) {
	engine, _ := newTestServer(t)
	_, accountToken := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	// 規格 §10：啟動時沒有任何訂閱。
	status, body := do(t, engine, http.MethodGet, "/v1/market/subscriptions", testUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("查詢訂閱應回 200，得到 %d: %v", status, body)
	}
	subs, ok := body["subscriptions"].(map[string]any)
	if !ok || len(subs) != 0 {
		t.Fatalf("啟動時不該有訂閱: %v", body)
	}

	// 要過一次價之後才會出現訂閱。
	if status, _ := do(t, engine, http.MethodGet, "/v1/market/price?symbol=BTCUSDT", testUserToken, nil); status != http.StatusOK {
		t.Fatalf("取得行情失敗: %d", status)
	}
	_, body = do(t, engine, http.MethodGet, "/v1/market/subscriptions", testUserToken, nil)
	subs, _ = body["subscriptions"].(map[string]any)
	if subs["crypto:BTCUSDT"] != "active" {
		t.Fatalf("取價後應有一個 active 訂閱: %v", body)
	}

	// 這是營運資訊，Account Token 看不到。
	if status, _ := do(t, engine, http.MethodGet, "/v1/market/subscriptions", accountToken, nil); status != http.StatusForbidden {
		t.Fatalf("Account Token 查訂閱應回 403，得到 %d", status)
	}
}
