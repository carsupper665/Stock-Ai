package test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestToolBoundaryProjectsPositionAndOrderDecisionFields(t *testing.T) {
	backend := newTradingBackend(t, func(w http.ResponseWriter, request *http.Request, _ string, _ []byte) {
		switch request.Method + " " + request.URL.Path {
		case "GET /v1/positions":
			writeFixtureJSON(w, http.StatusOK, map[string]any{"positions": []any{map[string]any{
				"id": "pos_1", "market": "crypto", "symbol": "BTCUSDT", "product": "futures", "side": "long",
				"quantity": 0.25, "entry_price": 60000, "mark_price": 61000, "leverage": 10,
				"margin": 1500, "unrealized_pnl": 250, "stop_loss": 58000, "take_profit": 65000,
				"opened_at": "2026-09-16T00:00:00Z", "stop_loss_source": map[string]any{"session_id": "private"},
			}}})
		case "POST /v1/orders":
			writeFixtureJSON(w, http.StatusCreated, map[string]any{
				"id": "ord_1", "status": "filled", "filled_quantity": 0.25, "avg_fill_price": 61000,
				"fee": 6.1, "realized_pnl": 0, "symbol": "BTCUSDT", "quantity": 0.25,
				"source": map[string]any{"session_id": "private", "run_id": 9}, "internal_echo": "drop",
			})
		default:
			t.Errorf("unexpected Backend request %s %s", request.Method, request.URL.RequestURI())
			writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
		}
	})
	backend.addAccount(testAccountID, "projection-token")
	gateway := newScriptedGateway()
	gateway.script(testAccountID,
		scriptedCall{ID: "positions", Name: "get_positions", Arguments: map[string]any{}},
		scriptedCall{ID: "order", Name: "place_order", Arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 0.25}},
	)
	runtime := openTradingRuntime(t, backend, gateway, time.Second)
	session := createStoppedSession(t, runtime, nil)
	startSessionRun(t, runtime, session)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	positions := decodeToolResult(t, gateway.result(t, "positions"))
	var positionBody struct {
		Positions []map[string]json.RawMessage `json:"positions"`
	}
	if err := json.Unmarshal(positions.Result, &positionBody); err != nil || len(positionBody.Positions) != 1 {
		t.Fatalf("decode compact positions: %s (%v)", positions.Result, err)
	}
	wantPositionFields := map[string]bool{
		"id": true, "market": true, "symbol": true, "product": true, "side": true, "quantity": true,
		"entry_price": true, "mark_price": true, "leverage": true, "unrealized_pnl": true,
		"stop_loss": true, "take_profit": true,
	}
	if len(positionBody.Positions[0]) != len(wantPositionFields) {
		t.Fatalf("position projection fields = %v", positionBody.Positions[0])
	}
	for field := range positionBody.Positions[0] {
		if !wantPositionFields[field] {
			t.Fatalf("position projection leaked %q: %s", field, positions.Result)
		}
	}

	order := decodeToolResult(t, gateway.result(t, "order"))
	var orderBody map[string]json.RawMessage
	if err := json.Unmarshal(order.Result, &orderBody); err != nil {
		t.Fatalf("decode compact order: %s (%v)", order.Result, err)
	}
	for _, field := range []string{"id", "status", "filled_quantity", "avg_fill_price", "fee", "realized_pnl"} {
		if _, ok := orderBody[field]; !ok {
			t.Fatalf("compact order missing %q: %s", field, order.Result)
		}
	}
	if len(orderBody) != 6 {
		t.Fatalf("order projection leaked Backend echo: %s", order.Result)
	}
}
