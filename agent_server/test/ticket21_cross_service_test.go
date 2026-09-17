package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func createTradingAccount(t *testing.T, backendURL, name string, balance float64) crossAccount {
	t.Helper()
	status, raw := crossHTTPRequest(t, http.MethodPost, backendURL+"/v1/accounts", crossBackendUserToken, map[string]any{
		"user_name": name, "initial_balance": balance,
	})
	if status != http.StatusCreated {
		t.Fatalf("create Backend Account %s status=%d body=%s", name, status, raw)
	}
	var account crossAccount
	if err := json.Unmarshal(raw, &account); err != nil || account.Token == "" {
		t.Fatalf("decode Backend Account: %v %s", err, raw)
	}
	return account
}

func backendOrder(t *testing.T, backendURL, token string, body map[string]any) map[string]any {
	t.Helper()
	status, raw := crossHTTPRequest(t, http.MethodPost, backendURL+"/v1/orders", token, body)
	if status != http.StatusCreated {
		t.Fatalf("Backend order status=%d body=%s", status, raw)
	}
	var order map[string]any
	if err := json.Unmarshal(raw, &order); err != nil {
		t.Fatalf("decode Backend order: %v", err)
	}
	return order
}

func TestTicket21CrossServiceAccountStateReflectsActualBackend(t *testing.T) {
	source := newSettablePriceSource(t, "60000.00")
	backendURL := startCrossBackend(t, source.server.URL)
	accountA := createTradingAccount(t, backendURL, "ticket-21-a", 100000)
	accountB := createTradingAccount(t, backendURL, "ticket-21-b", 500)
	backendOrder(t, backendURL, accountA.Token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10,
	})
	limitOrder := backendOrder(t, backendURL, accountA.Token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.1, "price": 50000, "leverage": 10,
	})
	limitOrderID := limitOrder["id"].(string)

	gateway := newScriptedGateway()
	gateway.script(accountA.ID,
		scriptedCall{ID: "a-account", Name: "get_account", Arguments: map[string]any{}},
		scriptedCall{ID: "a-positions", Name: "get_positions", Arguments: map[string]any{}},
		scriptedCall{ID: "a-open-orders", Name: "get_orders", Arguments: map[string]any{"status": "open"}},
		scriptedCall{ID: "a-trades", Name: "get_trades", Arguments: map[string]any{}},
		scriptedCall{ID: "a-order", Name: "get_order", Arguments: map[string]any{"order_id": limitOrderID}},
	)
	gateway.script(accountB.ID,
		scriptedCall{ID: "b-account", Name: "get_account", Arguments: map[string]any{}},
		scriptedCall{ID: "b-positions", Name: "get_positions", Arguments: map[string]any{}},
		scriptedCall{ID: "b-orders", Name: "get_orders", Arguments: map[string]any{}},
		scriptedCall{ID: "b-trades", Name: "get_trades", Arguments: map[string]any{}},
		scriptedCall{ID: "b-foreign-order", Name: "get_order", Arguments: map[string]any{"order_id": limitOrderID}},
	)
	fixture := newRuntimeFixture()
	fixture.generate = gateway.generate
	runtime := openCrossServiceRuntime(t, backendURL, crossBackendUserToken, fixture, 2*time.Second)
	sessionA := createStoppedSession(t, runtime, map[string]any{"account_id": accountA.ID, "max_loop": 8, "max_tool_call": 5})
	sessionB := createStoppedSession(t, runtime, map[string]any{"account_id": accountB.ID, "max_loop": 8, "max_tool_call": 5})
	startSessionRun(t, runtime, sessionA)
	startSessionRun(t, runtime, sessionB)
	runA := waitForRunStatus(t, runtime, sessionA.ID, 1, "completed")
	runB := waitForRunStatus(t, runtime, sessionB.ID, 1, "completed")
	if runA.ToolCalls != 5 || runB.ToolCalls != 5 {
		t.Fatalf("Run counters A=%+v B=%+v", runA, runB)
	}

	var account struct {
		ID           string  `json:"id"`
		Balance      float64 `json:"balance"`
		LockedMargin float64 `json:"locked_margin"`
		Available    float64 `json:"available"`
		Equity       float64 `json:"equity"`
		Token        string  `json:"token"`
	}
	result := decodeToolResult(t, gateway.result(t, "a-account"))
	if err := json.Unmarshal(result.Result, &account); err != nil || !result.OK || account.ID != accountA.ID || account.Token != "" ||
		account.Balance != 99997.6 || account.LockedMargin != 600 || account.Available != 99397.6 || account.Equity != 99997.6 {
		t.Fatalf("actual Backend Account state = %s (%v)", gateway.result(t, "a-account"), err)
	}
	var positions struct {
		Positions []struct {
			Symbol     string  `json:"symbol"`
			Side       string  `json:"side"`
			Quantity   float64 `json:"quantity"`
			EntryPrice float64 `json:"entry_price"`
			MarkPrice  float64 `json:"mark_price"`
			Leverage   float64 `json:"leverage"`
		} `json:"positions"`
	}
	result = decodeToolResult(t, gateway.result(t, "a-positions"))
	if err := json.Unmarshal(result.Result, &positions); err != nil || !result.OK || len(positions.Positions) != 1 ||
		positions.Positions[0].Symbol != "BTCUSDT" || positions.Positions[0].Side != "long" || positions.Positions[0].Quantity != 0.1 ||
		positions.Positions[0].EntryPrice != 60000 || positions.Positions[0].MarkPrice != 60000 || positions.Positions[0].Leverage != 10 {
		t.Fatalf("actual Backend positions = %s (%v)", gateway.result(t, "a-positions"), err)
	}
	var orders struct {
		Orders []struct {
			ID     string  `json:"id"`
			Status string  `json:"status"`
			Price  float64 `json:"price"`
		} `json:"orders"`
	}
	result = decodeToolResult(t, gateway.result(t, "a-open-orders"))
	if err := json.Unmarshal(result.Result, &orders); err != nil || !result.OK || len(orders.Orders) != 1 ||
		orders.Orders[0].ID != limitOrderID || orders.Orders[0].Status != "open" || orders.Orders[0].Price != 50000 {
		t.Fatalf("actual Backend open orders = %s (%v)", gateway.result(t, "a-open-orders"), err)
	}
	var trades struct {
		Trades []struct {
			Price float64 `json:"price"`
			Fee   float64 `json:"fee"`
			Role  string  `json:"role"`
		} `json:"trades"`
	}
	result = decodeToolResult(t, gateway.result(t, "a-trades"))
	if err := json.Unmarshal(result.Result, &trades); err != nil || !result.OK || len(trades.Trades) != 1 ||
		trades.Trades[0].Price != 60000 || trades.Trades[0].Fee != 2.4 || trades.Trades[0].Role != "" {
		t.Fatalf("actual Backend trades = %s (%v)", gateway.result(t, "a-trades"), err)
	}
	result = decodeToolResult(t, gateway.result(t, "a-order"))
	if !result.OK || !strings.Contains(string(result.Result), `"id":"`+limitOrderID+`"`) {
		t.Fatalf("actual Backend order = %s", gateway.result(t, "a-order"))
	}

	result = decodeToolResult(t, gateway.result(t, "b-account"))
	if !result.OK || !strings.Contains(string(result.Result), `"balance":500`) || strings.Contains(string(result.Result), accountB.Token) {
		t.Fatalf("Account B state = %s", gateway.result(t, "b-account"))
	}
	for _, callID := range []string{"b-positions", "b-orders", "b-trades"} {
		result = decodeToolResult(t, gateway.result(t, callID))
		if !result.OK || !strings.Contains(string(result.Result), `":[]`) {
			t.Fatalf("Account B %s = %s", callID, gateway.result(t, callID))
		}
	}
	result = decodeToolResult(t, gateway.result(t, "b-foreign-order"))
	if result.OK || result.Error.Code != "not_found" || result.Error.Outcome != "not_executed" {
		t.Fatalf("Account B reading Account A order = %s", gateway.result(t, "b-foreign-order"))
	}
	for _, sessionID := range []string{sessionA.ID, sessionB.ID} {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+sessionID+"/runs/1", agentAdminToken, nil)
		if status != http.StatusOK || strings.Contains(string(raw), accountA.Token) || strings.Contains(string(raw), accountB.Token) {
			t.Fatalf("Run trace leaked an Account Token (status=%d)", status)
		}
	}
}
