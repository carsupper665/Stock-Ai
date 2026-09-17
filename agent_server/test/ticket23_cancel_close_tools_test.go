package test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTicket23CancelAndCloseInjectRunSourceAndReturnBackendResults(t *testing.T) {
	backend := newTradingBackend(t, func(w http.ResponseWriter, request *http.Request, _ string, body []byte) {
		switch request.Method + " " + request.URL.Path {
		case "POST /v1/orders/ord_1/cancel":
			writeFixtureJSON(w, http.StatusOK, map[string]any{"id": "ord_1", "status": "canceled", "source": map[string]any{"session_id": "s_creator", "run_id": 1}})
		case "POST /v1/positions/pos_1/close":
			writeFixtureJSON(w, http.StatusOK, map[string]any{"id": "ord_close", "status": "filled", "reduce_only": true, "realized_pnl": 12.5, "echo": string(body)})
		default:
			t.Errorf("unexpected Backend request %s %s", request.Method, request.URL.RequestURI())
			writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
		}
	})
	backend.addAccount(testAccountID, "tok-23")
	gateway := newScriptedGateway()
	gateway.script(testAccountID,
		scriptedCall{ID: "cancel", Name: "cancel_order", Arguments: map[string]any{"order_id": "ord_1"}},
		scriptedCall{ID: "partial", Name: "close_position", Arguments: map[string]any{"position_id": "pos_1", "quantity": 0.25}},
		scriptedCall{ID: "full", Name: "close_position", Arguments: map[string]any{"position_id": "pos_1"}},
	)
	runtime := openTradingRuntime(t, backend, gateway, time.Second)
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 8, "max_tool_call": 3})
	startSessionRun(t, runtime, session)
	if run := waitForRunStatus(t, runtime, session.ID, 1, "completed"); run.ToolCalls != 3 {
		t.Fatalf("Run = %+v", run)
	}
	for _, name := range []string{"cancel_order", "close_position"} {
		properties := gateway.schema(t, name)["properties"].(map[string]any)
		for _, forbidden := range []string{"account_id", "session_id", "run_id", "source"} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("%s schema exposes %s", name, forbidden)
			}
		}
	}
	source := `"source":{"run_id":1,"session_id":"` + session.ID + `"}`
	requests := backend.authenticatedRequests()
	if len(requests) != 3 || requests[0].Method != http.MethodPost || requests[0].URI != "/v1/orders/ord_1/cancel" || requests[0].Body != `{`+source+`}` ||
		requests[1].URI != "/v1/positions/pos_1/close" || requests[1].Body != `{"quantity":0.25,`+source+`}` ||
		requests[2].URI != "/v1/positions/pos_1/close" || requests[2].Body != `{`+source+`}` {
		t.Fatalf("Backend requests = %+v", requests)
	}
	cancel := decodeToolResult(t, gateway.result(t, "cancel"))
	if !cancel.OK || !strings.Contains(string(cancel.Result), `"status":"canceled"`) || strings.Contains(string(cancel.Result), `"session_id"`) {
		t.Fatalf("cancel_order result = %s", gateway.result(t, "cancel"))
	}
	closed := decodeToolResult(t, gateway.result(t, "partial"))
	if !closed.OK || !strings.Contains(string(closed.Result), `"realized_pnl":12.5`) {
		t.Fatalf("close_position result = %s", gateway.result(t, "partial"))
	}
}

func TestTicket23CancelAndCloseErrorsAreFixedAndRetriesCostBudget(t *testing.T) {
	t.Run("cancel_order", func(t *testing.T) {
		valid := map[string]any{"order_id": "ord_1"}
		cases := []mutationErrorCase{
			{name: "identity in arguments", arguments: map[string]any{"order_id": "ord_1", "session_id": "s_x"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "account in arguments", arguments: map[string]any{"order_id": "ord_1", "account_id": "acc_b"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "blank id", arguments: map[string]any{"order_id": ""}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "missing id", arguments: map[string]any{}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "already filled or canceled", arguments: valid, wantCode: "order_not_open", wantMessage: "filled", wantOutcome: "not_executed", wantIO: 1,
				backend: func(w http.ResponseWriter, _ *http.Request) {
					writeFixtureJSON(w, http.StatusConflict, map[string]any{"error": "order_not_open", "message": "訂單已不是掛單，無法取消：目前狀態為 filled"})
				}},
			{name: "unknown or foreign order", arguments: map[string]any{"order_id": "ord_other"}, wantCode: "not_found", wantOutcome: "not_executed", wantIO: 1,
				backend: func(w http.ResponseWriter, _ *http.Request) {
					writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "找不到指定的資料"})
				}},
		}
		runMutationErrorCases(t, "cancel_order", append(cases, backendFailureCases(valid)...))
	})
	t.Run("close_position", func(t *testing.T) {
		valid := map[string]any{"position_id": "pos_1"}
		cases := []mutationErrorCase{
			{name: "identity in arguments", arguments: map[string]any{"position_id": "pos_1", "run_id": 3}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "zero quantity", arguments: map[string]any{"position_id": "pos_1", "quantity": 0}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "string quantity", arguments: map[string]any{"position_id": "pos_1", "quantity": "1"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "null quantity", arguments: map[string]any{"position_id": "pos_1", "quantity": nil}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "missing id", arguments: map[string]any{"quantity": 1}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
			{name: "quantity above position", arguments: map[string]any{"position_id": "pos_1", "quantity": 5}, wantCode: "invalid_request", wantMessage: "平倉數量超過持倉", wantOutcome: "not_executed", wantIO: 1,
				backend: func(w http.ResponseWriter, _ *http.Request) {
					writeFixtureJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "平倉數量超過持倉"})
				}},
			{name: "position already closed or foreign", arguments: valid, wantCode: "not_found", wantOutcome: "not_executed", wantIO: 1,
				backend: func(w http.ResponseWriter, _ *http.Request) {
					writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "部位已不存在"})
				}},
			{name: "market timeout is a definite Backend rejection", arguments: valid, wantCode: "market_timeout", wantOutcome: "not_executed", wantIO: 1,
				backend: func(w http.ResponseWriter, _ *http.Request) {
					writeFixtureJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "market_timeout", "message": "等不到行情，無法成交"})
				}},
		}
		runMutationErrorCases(t, "close_position", append(cases, backendFailureCases(valid)...))
	})
}
