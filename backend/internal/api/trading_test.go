package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func buyOrder(qty, leverage float64) gin.H {
	return gin.H{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market",
		"quantity": qty, "leverage": leverage,
	}
}

func TestTradingEndpointsRequireAccountToken(t *testing.T) {
	engine, _ := newTestServer(t)
	createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)

	endpoints := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/orders", buyOrder(0.01, 10)},
		{http.MethodGet, "/v1/orders", nil},
		{http.MethodGet, "/v1/positions", nil},
		{http.MethodGet, "/v1/trades", nil},
		{http.MethodPost, "/v1/positions/pos_x/close", nil},
		{http.MethodPost, "/v1/orders/ord_x/cancel", nil},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			if status, _ := do(t, engine, ep.method, ep.path, "", ep.body); status != http.StatusUnauthorized {
				t.Fatalf("未帶 token 應回 401，得到 %d", status)
			}
			if status, _ := do(t, engine, ep.method, ep.path, "at_bogus", ep.body); status != http.StatusUnauthorized {
				t.Fatalf("無效 token 應回 401，得到 %d", status)
			}
			if status, _ := do(t, engine, ep.method, ep.path, testUserToken, ep.body); status != http.StatusForbidden {
				t.Fatalf("USER 沒有可交易的帳號，應回 403，得到 %d", status)
			}
		})
	}
}

func TestMarketOrderFillsAndOpensPosition(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)

	status, order := do(t, engine, http.MethodPost, "/v1/orders", token, buyOrder(0.01, 10))
	if status != http.StatusCreated {
		t.Fatalf("下單應回 201，得到 %d: %v", status, order)
	}
	if order["status"] != "filled" {
		t.Fatalf("市價單應立刻成交: %v", order)
	}
	if order["avg_fill_price"] != testPrice {
		t.Fatalf("成交價應為 %v: %v", testPrice, order)
	}
	if fee, _ := order["fee"].(float64); fee <= 0 {
		t.Fatalf("應收取手續費: %v", order)
	}

	status, body := do(t, engine, http.MethodGet, "/v1/positions", token, nil)
	positions, _ := body["positions"].([]any)
	if status != http.StatusOK || len(positions) != 1 {
		t.Fatalf("應有一個部位: %d %v", status, body)
	}
	position, _ := positions[0].(map[string]any)
	if position["side"] != "long" || position["quantity"] != 0.01 {
		t.Fatalf("部位內容不符: %v", position)
	}
	if position["mark_price"] != testPrice {
		t.Fatalf("部位應帶現價: %v", position)
	}

	status, body = do(t, engine, http.MethodGet, "/v1/trades", token, nil)
	trades, _ := body["trades"].([]any)
	if status != http.StatusOK || len(trades) != 1 {
		t.Fatalf("應有一筆成交: %d %v", status, body)
	}
}

func TestClosePositionEndpoint(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)

	do(t, engine, http.MethodPost, "/v1/orders", token, buyOrder(0.02, 10))

	_, body := do(t, engine, http.MethodGet, "/v1/positions", token, nil)
	positions, _ := body["positions"].([]any)
	position, _ := positions[0].(map[string]any)
	positionID, _ := position["id"].(string)

	status, order := do(t, engine, http.MethodPost, "/v1/positions/"+positionID+"/close", token, gin.H{"quantity": 0.01})
	if status != http.StatusOK {
		t.Fatalf("部分平倉應回 200，得到 %d: %v", status, order)
	}
	if order["side"] != "sell" || order["quantity"] != 0.01 {
		t.Fatalf("平倉單方向或數量不符: %v", order)
	}

	_, body = do(t, engine, http.MethodGet, "/v1/positions", token, nil)
	positions, _ = body["positions"].([]any)
	position, _ = positions[0].(map[string]any)
	if position["quantity"] != 0.01 {
		t.Fatalf("部分平倉後應剩 0.01: %v", position)
	}

	status, _ = do(t, engine, http.MethodPost, "/v1/positions/"+positionID+"/close", token, nil)
	if status != http.StatusOK {
		t.Fatalf("不帶 quantity 應全平，得到 %d", status)
	}
	_, body = do(t, engine, http.MethodGet, "/v1/positions", token, nil)
	if positions, _ = body["positions"].([]any); len(positions) != 0 {
		t.Fatalf("全平後不該留下部位: %v", body)
	}

	if status, _ := do(t, engine, http.MethodPost, "/v1/positions/"+positionID+"/close", token, nil); status != http.StatusNotFound {
		t.Fatalf("平已關閉的部位應回 404，得到 %d", status)
	}
}

func TestSelfAccountReportsEquity(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)

	do(t, engine, http.MethodPost, "/v1/orders", token, buyOrder(0.01, 10))

	status, body := do(t, engine, http.MethodGet, "/v1/account", token, nil)
	if status != http.StatusOK {
		t.Fatalf("查詢帳號應回 200，得到 %d: %v", status, body)
	}
	for _, field := range []string{"balance", "locked_margin", "available", "unrealized_pnl", "equity"} {
		if _, present := body[field]; !present {
			t.Fatalf("帳務總覽缺少 %s: %v", field, body)
		}
	}
	locked, _ := body["locked_margin"].(float64)
	if locked <= 0 {
		t.Fatalf("開倉後應有保證金被鎖住: %v", body)
	}
	if _, present := body["token"]; present {
		t.Fatalf("帳號自查不該回傳 token: %v", body)
	}
}

func TestOrderValidationErrors(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)

	cases := []struct {
		name string
		body gin.H
		want int
	}{
		{"缺少 symbol", gin.H{"side": "buy", "quantity": 1}, http.StatusBadRequest},
		{"數量為零", gin.H{"symbol": "BTCUSDT", "side": "buy", "quantity": 0}, http.StatusBadRequest},
		{"未知的 side", gin.H{"symbol": "BTCUSDT", "side": "hold", "quantity": 1}, http.StatusBadRequest},
		{"槓桿過高", gin.H{"symbol": "BTCUSDT", "side": "buy", "quantity": 1, "leverage": 500}, http.StatusBadRequest},
		{"spot 帶槓桿", gin.H{"symbol": "BTCUSDT", "product": "spot", "side": "buy", "quantity": 1, "leverage": 5}, http.StatusBadRequest},
		{"限價單缺價格", gin.H{"symbol": "BTCUSDT", "side": "buy", "type": "limit", "quantity": 1}, http.StatusBadRequest},
		{"餘額不足", gin.H{"symbol": "BTCUSDT", "side": "buy", "quantity": 10000, "leverage": 1}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, engine, http.MethodPost, "/v1/orders", token, tc.body)
			if status != tc.want {
				t.Fatalf("預期 %d，得到 %d: %v", tc.want, status, body)
			}
			if body["error"] == nil || body["message"] == nil {
				t.Fatalf("錯誤回應缺少 error 或 message: %v", body)
			}
		})
	}
}

func TestLimitOrderStaysOpenAndCanBeCanceled(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)

	status, order := do(t, engine, http.MethodPost, "/v1/orders", token, gin.H{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit",
		"quantity": 0.01, "price": testPrice - 1000, "leverage": 10,
	})
	if status != http.StatusCreated || order["status"] != "open" {
		t.Fatalf("限價單應建立且維持 open: %d %v", status, order)
	}
	orderID, _ := order["id"].(string)

	_, body := do(t, engine, http.MethodGet, "/v1/orders?status=open", token, nil)
	if orders, _ := body["orders"].([]any); len(orders) != 1 {
		t.Fatalf("應有一筆未成交訂單: %v", body)
	}

	status, canceled := do(t, engine, http.MethodPost, "/v1/orders/"+orderID+"/cancel", token, nil)
	if status != http.StatusOK || canceled["status"] != "canceled" {
		t.Fatalf("取消訂單失敗: %d %v", status, canceled)
	}
}

func TestUserCanReadAnyAccountActivity(t *testing.T) {
	engine, _ := newTestServer(t)
	alphaID, alphaToken := createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)
	betaID, betaToken := createAccountFor(t, engine, "ETH-Agent-02", 1_000_000)

	do(t, engine, http.MethodPost, "/v1/orders", alphaToken, buyOrder(0.01, 10))

	status, body := do(t, engine, http.MethodGet, "/v1/accounts/"+alphaID+"/positions", testUserToken, nil)
	if positions, _ := body["positions"].([]any); status != http.StatusOK || len(positions) != 1 {
		t.Fatalf("USER 應看得到該帳號的部位: %d %v", status, body)
	}
	_, body = do(t, engine, http.MethodGet, "/v1/accounts/"+alphaID+"/trades", testUserToken, nil)
	if trades, _ := body["trades"].([]any); len(trades) != 1 {
		t.Fatalf("USER 應看得到成交紀錄: %v", body)
	}

	_, body = do(t, engine, http.MethodGet, "/v1/accounts/"+betaID+"/positions", testUserToken, nil)
	if positions, _ := body["positions"].([]any); len(positions) != 0 {
		t.Fatalf("沒交易過的帳號應是空的: %v", body)
	}

	// 帳號之間互相看不到，也不能用 USER 的路徑繞過。
	if status, _ := do(t, engine, http.MethodGet, "/v1/accounts/"+alphaID+"/positions", betaToken, nil); status != http.StatusForbidden {
		t.Fatalf("Account Token 走 USER 路徑應回 403，得到 %d", status)
	}
	_, body = do(t, engine, http.MethodGet, "/v1/positions", betaToken, nil)
	if positions, _ := body["positions"].([]any); len(positions) != 0 {
		t.Fatalf("另一個帳號不該看到別人的部位: %v", body)
	}
}

func TestSetStopsEndpoint(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 1_000_000)
	do(t, engine, http.MethodPost, "/v1/orders", token, buyOrder(0.01, 10))

	_, body := do(t, engine, http.MethodGet, "/v1/positions", token, nil)
	positions, _ := body["positions"].([]any)
	position, _ := positions[0].(map[string]any)
	positionID, _ := position["id"].(string)
	path := "/v1/positions/" + positionID

	status, updated := do(t, engine, http.MethodPatch, path, token, gin.H{
		"stop_loss": testPrice - 1000, "take_profit": testPrice + 1000,
	})
	if status != http.StatusOK {
		t.Fatalf("設定停損停利應回 200，得到 %d: %v", status, updated)
	}
	if updated["stop_loss"] != testPrice-1000 || updated["take_profit"] != testPrice+1000 {
		t.Fatalf("回應應帶回設定值: %v", updated)
	}

	status, updated = do(t, engine, http.MethodPatch, path, token, gin.H{"stop_loss": 0})
	if status != http.StatusOK {
		t.Fatalf("移除停損應回 200，得到 %d", status)
	}
	if _, present := updated["stop_loss"]; present {
		t.Fatalf("移除後不該再有 stop_loss: %v", updated)
	}
	if updated["take_profit"] != testPrice+1000 {
		t.Fatalf("只移除停損不該動到停利: %v", updated)
	}

	if status, _ := do(t, engine, http.MethodPatch, path, token, gin.H{"stop_loss": testPrice + 1}); status != http.StatusBadRequest {
		t.Fatalf("多單停損高於現價應回 400，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodPatch, path, token, gin.H{}); status != http.StatusBadRequest {
		t.Fatalf("空的修改應回 400，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodPatch, path, testUserToken, gin.H{"stop_loss": 1}); status != http.StatusForbidden {
		t.Fatalf("USER 不能改別人的停損停利，應回 403，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodPatch, "/v1/positions/pos_missing", token, gin.H{"stop_loss": 1}); status != http.StatusNotFound {
		t.Fatalf("不存在的部位應回 404，得到 %d", status)
	}
}
