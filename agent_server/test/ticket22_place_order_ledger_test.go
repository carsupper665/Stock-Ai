package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type backendSource struct {
	SessionID string `json:"session_id"`
	RunID     int64  `json:"run_id"`
}

func TestTicket22PlaceOrderInjectsRunSourceAndLedgerMapsFilters(t *testing.T) {
	bodies := make(chan map[string]json.RawMessage, 4)
	backend := newTradingBackend(t, func(w http.ResponseWriter, request *http.Request, accountID string, body []byte) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/orders":
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Errorf("order body %q: %v", body, err)
			}
			bodies <- decoded
			writeFixtureJSON(w, http.StatusCreated, map[string]any{
				"id": "ord_new", "status": "filled", "avg_fill_price": 60000.5, "fee": 0.24, "source": json.RawMessage(decoded["source"]),
			})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/ledger":
			writeFixtureJSON(w, http.StatusOK, map[string]any{"entries": []any{map[string]any{"seq": 9, "event": "fill", "order_id": "ord_new"}}})
		default:
			t.Errorf("unexpected Backend request %s %s", request.Method, request.URL.RequestURI())
			writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
		}
	})
	backend.addAccount(testAccountID, "tok-22")
	gateway := newScriptedGateway()
	gateway.script(testAccountID,
		scriptedCall{ID: "market", Name: "place_order", Arguments: map[string]any{
			"symbol": " btcusdt ", "side": "BUY", "quantity": 0.01, "leverage": 10, "stop_loss": 58000, "take_profit": 65000.25,
		}},
		scriptedCall{ID: "limit", Name: "place_order", Arguments: map[string]any{
			"market": "crypto", "symbol": "ETHUSDT", "product": "spot", "side": "sell", "type": "limit", "quantity": 1.5, "price": 3000.125,
		}},
		scriptedCall{ID: "ledger", Name: "get_ledger", Arguments: map[string]any{}},
		scriptedCall{ID: "ledger-run", Name: "get_ledger", Arguments: map[string]any{"run_id": 1, "limit": 5, "before_seq": 40}},
	)
	runtime := openTradingRuntime(t, backend, gateway, time.Second)
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 8, "max_tool_call": 4})
	startSessionRun(t, runtime, session)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ToolCalls != 4 {
		t.Fatalf("Run = %+v", run)
	}

	schema := gateway.schema(t, "place_order")
	properties := schema["properties"].(map[string]any)
	for _, forbidden := range []string{"account_id", "session_id", "run_id", "source", "token"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("place_order schema exposes %s", forbidden)
		}
	}
	if required, _ := json.Marshal(schema["required"]); string(required) != `["symbol","side","quantity"]` {
		t.Errorf("place_order required = %s", required)
	}
	if markets, _ := json.Marshal(properties["market"].(map[string]any)["enum"]); string(markets) != `["crypto","stock"]` {
		t.Errorf("place_order market enum = %s", markets)
	}

	market := <-bodies
	var marketSource backendSource
	if err := json.Unmarshal(market["source"], &marketSource); err != nil || marketSource.SessionID != session.ID || marketSource.RunID != 1 {
		t.Fatalf("market order source = %s (%v)", market["source"], err)
	}
	if string(market["symbol"]) != `"btcusdt"` || string(market["side"]) != `"buy"` || string(market["quantity"]) != "0.01" ||
		string(market["leverage"]) != "10" || string(market["stop_loss"]) != "58000" || string(market["take_profit"]) != "65000.25" ||
		string(market["type"]) != `"market"` || string(market["product"]) != `"futures"` || string(market["market"]) != `"crypto"` {
		t.Fatalf("market order body = %v", market)
	}
	if _, exists := market["price"]; exists {
		t.Fatalf("market order forwarded an absent price: %v", market)
	}
	if _, exists := market["account_id"]; exists {
		t.Fatalf("Agent sent an account_id: %v", market)
	}
	limit := <-bodies
	if string(limit["type"]) != `"limit"` || string(limit["price"]) != "3000.125" || string(limit["quantity"]) != "1.5" ||
		string(limit["product"]) != `"spot"` || string(limit["side"]) != `"sell"` || string(limit["market"]) != `"crypto"` {
		t.Fatalf("limit order body = %v", limit)
	}
	for _, absent := range []string{"leverage", "stop_loss", "take_profit"} {
		if _, exists := limit[absent]; exists {
			t.Fatalf("limit order forwarded absent %s: %v", absent, limit)
		}
	}

	result := decodeToolResult(t, gateway.result(t, "market"))
	if !result.OK || !strings.Contains(string(result.Result), `"id":"ord_new"`) ||
		!strings.Contains(string(result.Result), `"status":"filled"`) || strings.Contains(string(result.Result), `"source"`) {
		t.Fatalf("place_order result = %s", gateway.result(t, "market"))
	}
	ledger := decodeToolResult(t, gateway.result(t, "ledger-run"))
	if !ledger.OK || !strings.Contains(string(ledger.Result), `"seq":9`) {
		t.Fatalf("get_ledger result = %s", gateway.result(t, "ledger-run"))
	}
	requests := backend.authenticatedRequests()
	if len(requests) != 4 || requests[0].Method != http.MethodPost || requests[0].URI != "/v1/orders" ||
		requests[2].URI != "/v1/ledger?limit=50" ||
		requests[3].URI != "/v1/ledger?before_seq=40&limit=5&run_id=1&session_id="+session.ID {
		t.Fatalf("Backend requests = %+v", requests)
	}
}

func TestTicket22PlaceOrderErrorsAreReturnedWithoutRetryAndRetriesCostBudget(t *testing.T) {
	valid := map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 1}
	cases := []mutationErrorCase{
		{name: "identity in arguments", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 1, "source": map[string]any{"session_id": "s_x", "run_id": 9}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "account in arguments", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 1, "account_id": "acc_b"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "string quantity", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": "1"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "zero quantity", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 0}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "negative stop", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 1, "stop_loss": -1}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "null price", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 1, "type": "limit", "price": nil}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "bad side", arguments: map[string]any{"symbol": "BTCUSDT", "side": "long", "quantity": 1}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "unknown market", arguments: map[string]any{"market": "forex", "symbol": "EURUSD", "side": "buy", "quantity": 1}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "missing symbol", arguments: map[string]any{"side": "buy", "quantity": 1}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "Backend rejection preserved", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 100}, wantCode: "insufficient_balance", wantMessage: "可用餘額不足", wantOutcome: "not_executed", wantIO: 1,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "insufficient_balance", "message": "可用餘額不足：需要 6000，可用 100"})
			}},
		{name: "Backend validation preserved", arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 1, "type": "limit"}, wantCode: "invalid_request", wantMessage: "price", wantOutcome: "not_executed", wantIO: 1,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "限價單必須指定大於 0 的 price"})
			}},
	}
	runMutationErrorCases(t, "place_order", append(cases, backendFailureCases(valid)...))
}
