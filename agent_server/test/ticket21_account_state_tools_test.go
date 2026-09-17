package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func accountStateBackend(w http.ResponseWriter, request *http.Request, accountID string, _ []byte) {
	if request.Method != http.MethodGet {
		writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
		return
	}
	balance := map[string]float64{"acc_a": 1000.5, "acc_b": 20}[accountID]
	switch {
	case request.URL.Path == "/v1/account":
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"id": accountID, "user_name": "agent-" + accountID, "initial_balance": 1000, "balance": balance,
			"status": "active", "created_at": "2026-09-01T00:00:00Z", "locked_margin": 10, "available": balance - 10,
			"unrealized_pnl": -1.5, "equity": balance - 1.5,
		})
	case request.URL.Path == "/v1/positions":
		positions := []any{}
		if accountID == "acc_a" {
			positions = append(positions, map[string]any{"id": "pos_a1", "symbol": "BTCUSDT", "product": "futures", "side": "long", "quantity": 0.5, "stop_loss": 58000})
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"positions": positions})
	case request.URL.Path == "/v1/orders":
		orders := []any{}
		if accountID == "acc_a" {
			orders = append(orders, map[string]any{"id": "ord_a1", "status": "open", "type": "limit", "price": 59000})
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"orders": orders})
	case request.URL.Path == "/v1/orders/ord_a1" && accountID == "acc_a":
		writeFixtureJSON(w, http.StatusOK, map[string]any{"id": "ord_a1", "status": "open", "type": "limit", "price": 59000, "filled_quantity": 0})
	case strings.HasPrefix(request.URL.Path, "/v1/orders/"):
		writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "找不到指定的資料"})
	case request.URL.Path == "/v1/trades":
		trades := []any{}
		if accountID == "acc_a" {
			trades = append(trades, map[string]any{"id": "trd_a1", "order_id": "ord_a0", "price": 60000, "fee": 0.24})
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"trades": trades})
	default:
		writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
	}
}

func TestTicket21AccountStateToolsMapToBackendAndIsolateAccounts(t *testing.T) {
	backend := newTradingBackend(t, accountStateBackend)
	backend.addAccount("acc_a", "tok-a-secret")
	backend.addAccount("acc_b", "tok-b-secret")
	gateway := newScriptedGateway()
	gateway.script("acc_a",
		scriptedCall{ID: "a-account", Name: "get_account", Arguments: map[string]any{}},
		scriptedCall{ID: "a-positions", Name: "get_positions", Arguments: map[string]any{"product": "futures"}},
		scriptedCall{ID: "a-orders", Name: "get_orders", Arguments: map[string]any{"status": "open", "limit": 200}},
		scriptedCall{ID: "a-order", Name: "get_order", Arguments: map[string]any{"order_id": "ord_a1"}},
		scriptedCall{ID: "a-trades", Name: "get_trades", Arguments: map[string]any{"limit": 1}},
	)
	gateway.script("acc_b",
		scriptedCall{ID: "b-account", Name: "get_account", Arguments: map[string]any{}},
		scriptedCall{ID: "b-positions", Name: "get_positions", Arguments: map[string]any{}},
		scriptedCall{ID: "b-orders", Name: "get_orders", Arguments: map[string]any{}},
		scriptedCall{ID: "b-trades", Name: "get_trades", Arguments: map[string]any{}},
		scriptedCall{ID: "b-foreign-order", Name: "get_order", Arguments: map[string]any{"order_id": "ord_a1"}},
	)
	runtime := openTradingRuntime(t, backend, gateway, time.Second)
	sessionA := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_a", "max_loop": 8, "max_tool_call": 5})
	sessionB := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_b", "max_loop": 8, "max_tool_call": 5})
	startSessionRun(t, runtime, sessionA)
	startSessionRun(t, runtime, sessionB)
	runA := waitForRunStatus(t, runtime, sessionA.ID, 1, "completed")
	runB := waitForRunStatus(t, runtime, sessionB.ID, 1, "completed")
	if runA.ModelCalls != 6 || runA.ToolCalls != 5 || runB.ModelCalls != 6 || runB.ToolCalls != 5 {
		t.Fatalf("Run counters A=%+v B=%+v", runA, runB)
	}

	for _, name := range []string{"get_account", "get_positions", "get_orders", "get_order", "get_trades"} {
		schema := gateway.schema(t, name)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s schema = %v", name, schema)
		}
	}
	if properties := gateway.schema(t, "get_account")["properties"].(map[string]any); len(properties) != 0 {
		t.Errorf("get_account must not accept identity or filter arguments: %v", properties)
	}

	account := decodeToolResult(t, gateway.result(t, "a-account"))
	var accountResult struct {
		ID         string  `json:"id"`
		Balance    float64 `json:"balance"`
		Available  float64 `json:"available"`
		Equity     float64 `json:"equity"`
		Unrealized float64 `json:"unrealized_pnl"`
	}
	if err := json.Unmarshal(account.Result, &accountResult); err != nil || !account.OK || accountResult.ID != "acc_a" ||
		accountResult.Balance != 1000.5 || accountResult.Available != 990.5 || accountResult.Equity != 999 || accountResult.Unrealized != -1.5 {
		t.Fatalf("get_account result = %s (%v)", gateway.result(t, "a-account"), err)
	}
	positions := decodeToolResult(t, gateway.result(t, "a-positions"))
	if !positions.OK || !strings.Contains(string(positions.Result), `"id":"pos_a1"`) || !strings.Contains(string(positions.Result), `"stop_loss":58000`) {
		t.Fatalf("get_positions result = %s", gateway.result(t, "a-positions"))
	}
	orders := decodeToolResult(t, gateway.result(t, "a-orders"))
	if !orders.OK || !strings.Contains(string(orders.Result), `"id":"ord_a1"`) {
		t.Fatalf("get_orders result = %s", gateway.result(t, "a-orders"))
	}
	order := decodeToolResult(t, gateway.result(t, "a-order"))
	if !order.OK || !strings.Contains(string(order.Result), `"filled_quantity":0`) {
		t.Fatalf("get_order result = %s", gateway.result(t, "a-order"))
	}
	trades := decodeToolResult(t, gateway.result(t, "a-trades"))
	if !trades.OK || !strings.Contains(string(trades.Result), `"id":"trd_a1"`) {
		t.Fatalf("get_trades result = %s", gateway.result(t, "a-trades"))
	}

	accountB := decodeToolResult(t, gateway.result(t, "b-account"))
	if !accountB.OK || !strings.Contains(string(accountB.Result), `"id":"acc_b"`) || !strings.Contains(string(accountB.Result), `"balance":20`) {
		t.Fatalf("Account B state = %s", gateway.result(t, "b-account"))
	}
	for _, callID := range []string{"b-positions", "b-orders", "b-trades"} {
		result := decodeToolResult(t, gateway.result(t, callID))
		if !result.OK || !strings.Contains(string(result.Result), `":[]`) {
			t.Fatalf("empty list %s = %s", callID, gateway.result(t, callID))
		}
	}
	foreign := decodeToolResult(t, gateway.result(t, "b-foreign-order"))
	if foreign.OK || foreign.Error.Code != "not_found" || foreign.Error.Outcome != "not_executed" || foreign.Error.Message != "找不到指定的資料" {
		t.Fatalf("cross-Account order lookup = %s", gateway.result(t, "b-foreign-order"))
	}

	wantA := []string{"/v1/account", "/v1/positions?product=futures", "/v1/orders?limit=200&status=open", "/v1/orders/ord_a1", "/v1/trades?limit=1"}
	wantB := []string{"/v1/account", "/v1/positions", "/v1/orders?limit=50", "/v1/trades?limit=50", "/v1/orders/ord_a1"}
	gotA, gotB := []string{}, []string{}
	for _, request := range backend.authenticatedRequests() {
		if request.Method != http.MethodGet || request.Body != "" {
			t.Errorf("read tool issued %s with body %q", request.Method, request.Body)
		}
		if request.AccountID == "acc_a" {
			gotA = append(gotA, request.URI)
		} else {
			gotB = append(gotB, request.URI)
		}
	}
	if strings.Join(gotA, " ") != strings.Join(wantA, " ") || strings.Join(gotB, " ") != strings.Join(wantB, " ") {
		t.Fatalf("Backend requests A=%v B=%v", gotA, gotB)
	}

	for _, session := range []string{sessionA.ID, sessionB.ID} {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session+"/runs/1", agentAdminToken, nil)
		if status != http.StatusOK || strings.Contains(string(raw), "tok-a-secret") || strings.Contains(string(raw), "tok-b-secret") {
			t.Fatalf("Run trace status=%d leaked credential=%v", status, strings.Contains(string(raw), "secret"))
		}
	}
}

func TestTicket21AccountStateToolsUseCommonErrorFlowAndConsumeBudget(t *testing.T) {
	for _, test := range []struct {
		name        string
		call        scriptedCall
		backend     func(http.ResponseWriter, *http.Request)
		accountOff  bool
		toolTimeout time.Duration
		wantCode    string
		wantOutcome string
		wantIO      bool
	}{
		{name: "identity override rejected", call: scriptedCall{Name: "get_positions", Arguments: map[string]any{"account_id": "acc_b"}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "session override rejected", call: scriptedCall{Name: "get_orders", Arguments: map[string]any{"session_id": "s_other", "run_id": 1}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "token argument rejected", call: scriptedCall{Name: "get_account", Arguments: map[string]any{"token": "tok"}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "null status", call: scriptedCall{Name: "get_orders", Arguments: map[string]any{"status": nil}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "unknown status", call: scriptedCall{Name: "get_orders", Arguments: map[string]any{"status": "partial"}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "limit above cap", call: scriptedCall{Name: "get_trades", Arguments: map[string]any{"limit": 201}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "limit not integer", call: scriptedCall{Name: "get_trades", Arguments: map[string]any{"limit": "50"}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "invalid product", call: scriptedCall{Name: "get_positions", Arguments: map[string]any{"product": "options"}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "blank order id", call: scriptedCall{Name: "get_order", Arguments: map[string]any{"order_id": "  "}}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "disabled Account", call: scriptedCall{Name: "get_account", Arguments: map[string]any{}}, accountOff: true, wantCode: "ACCOUNT_DISABLED", wantOutcome: "not_executed"},
		{name: "Backend error preserved", call: scriptedCall{Name: "get_positions", Arguments: map[string]any{}}, wantCode: "market_timeout", wantOutcome: "not_executed", wantIO: true,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "market_timeout", "message": "等不到行情"})
			}},
		{name: "Backend unavailable", call: scriptedCall{Name: "get_account", Arguments: map[string]any{}}, wantCode: "internal_error", wantOutcome: "not_executed", wantIO: true,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error", "message": "帳號操作失敗"})
			}},
		{name: "timeout", call: scriptedCall{Name: "get_trades", Arguments: map[string]any{}}, wantCode: "TOOL_TIMEOUT", wantOutcome: "unknown", wantIO: true, toolTimeout: 20 * time.Millisecond,
			backend: func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }},
		{name: "malformed Backend body", call: scriptedCall{Name: "get_orders", Arguments: map[string]any{}}, wantCode: "BACKEND_RESPONSE_INVALID", wantOutcome: "unknown", wantIO: true,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"orders":[`))
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newTradingBackend(t, func(w http.ResponseWriter, request *http.Request, _ string, _ []byte) {
				if test.backend == nil {
					t.Errorf("unexpected Backend request %s %s", request.Method, request.URL.RequestURI())
					writeFixtureJSON(w, http.StatusTeapot, map[string]any{"error": "unexpected", "message": "unexpected"})
					return
				}
				test.backend(w, request)
			})
			backend.addAccount(testAccountID, "tok-fixture")
			gateway := newScriptedGateway()
			test.call.ID = "attempt"
			gateway.script(testAccountID, test.call)
			runtime := openTradingRuntime(t, backend, gateway, durationOr(test.toolTimeout, time.Second))
			session := createStoppedSession(t, runtime, nil)
			if test.accountOff {
				backend.setStatus(testAccountID, "disabled")
			}
			startSessionRun(t, runtime, session)
			run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
			result := decodeToolResult(t, gateway.result(t, "attempt"))
			if run.ToolCalls != 1 || run.ModelCalls != 2 || result.OK || result.Error.Code != test.wantCode || result.Error.Outcome != test.wantOutcome {
				t.Fatalf("Run=%+v Tool Result=%s", run, gateway.result(t, "attempt"))
			}
			if got := len(backend.authenticatedRequests()); (got == 1) != test.wantIO || got > 1 {
				t.Fatalf("authenticated Backend requests = %d, want I/O=%v", got, test.wantIO)
			}
		})
	}
}

func TestTicket21ReadToolsUseCurrentAccountTokenAfterReset(t *testing.T) {
	var backend *tradingBackend
	backend = newTradingBackend(t, func(w http.ResponseWriter, request *http.Request, accountID string, _ []byte) {
		// The USER resets the Token between the two attempts; the second must use the new one.
		backend.setToken(accountID, "tok-after-reset")
		writeFixtureJSON(w, http.StatusOK, map[string]any{"id": accountID, "balance": 1, "request": request.URL.RequestURI()})
	})
	backend.addAccount(testAccountID, "tok-before-reset")
	gateway := newScriptedGateway()
	gateway.script(testAccountID,
		scriptedCall{ID: "first", Name: "get_account", Arguments: map[string]any{}},
		scriptedCall{ID: "second", Name: "get_account", Arguments: map[string]any{}},
	)
	runtime := openTradingRuntime(t, backend, gateway, time.Second)
	session := createStoppedSession(t, runtime, nil)
	startSessionRun(t, runtime, session)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	first, second := decodeToolResult(t, gateway.result(t, "first")), decodeToolResult(t, gateway.result(t, "second"))
	if run.ToolCalls != 2 || !first.OK || !second.OK || len(backend.authenticatedRequests()) != 2 {
		t.Fatalf("Run=%+v first=%s second=%s requests=%v", run, gateway.result(t, "first"), gateway.result(t, "second"), backend.authenticatedRequests())
	}
}
