package test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type crossPositionResult struct {
	ID               string         `json:"id"`
	StopLoss         float64        `json:"stop_loss"`
	TakeProfit       float64        `json:"take_profit"`
	StopLossSource   *backendSource `json:"stop_loss_source"`
	TakeProfitSource *backendSource `json:"take_profit_source"`
}

func TestTicket24CrossServiceStopsTriggerAfterSessionStopAndAgentRestartTraceToSettingRun(t *testing.T) {
	source := newSettablePriceSource(t, "60000")
	backendURL := startTradingBackend(t, source.server.URL)
	account := createTradingAccount(t, backendURL, "ticket-24-cross", 100000)
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	gateway := newScriptedGateway()
	gateway.script(account.ID,
		scriptedCall{ID: "open", Name: "place_order", Arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 0.1, "leverage": 10, "stop_loss": 58000}},
		scriptedCall{ID: "positions", Name: "get_positions", Arguments: map[string]any{}},
	)
	runtime := openTradingCrossRuntime(t, backendURL, gateway, dbPath)
	session := createStoppedSession(t, runtime, map[string]any{"account_id": account.ID})
	startSessionRun(t, runtime, session)
	if run := waitForRunStatus(t, runtime, session.ID, 1, "completed"); run.ToolCalls != 2 {
		t.Fatalf("Run 1 = %+v", run)
	}
	var positions struct {
		Positions []crossPositionResult `json:"positions"`
	}
	if err := json.Unmarshal(decodeToolResult(t, gateway.result(t, "positions")).Result, &positions); err != nil || len(positions.Positions) != 1 ||
		positions.Positions[0].StopLoss != 58000 || positions.Positions[0].StopLossSource != nil || positions.Positions[0].TakeProfitSource != nil {
		t.Fatalf("position opened by Run 1 = %s (%v)", gateway.result(t, "positions"), err)
	}
	positionID := positions.Positions[0].ID

	// Run 2 moves the stop-loss; Run 3 adds the take-profit. Each level keeps its own setter.
	gateway.script(account.ID, scriptedCall{ID: "move-sl", Name: "set_position_stops", Arguments: map[string]any{"position_id": positionID, "stop_loss": 57000}})
	restartSessionRun(t, runtime, session.ID)
	if run := waitForRunStatus(t, runtime, session.ID, 2, "completed"); run.ToolCalls != 1 {
		t.Fatalf("Run 2 = %+v", run)
	}
	gateway.script(account.ID, scriptedCall{ID: "set-tp", Name: "set_position_stops", Arguments: map[string]any{"position_id": positionID, "take_profit": 65000}})
	restartSessionRun(t, runtime, session.ID)
	if run := waitForRunStatus(t, runtime, session.ID, 3, "completed"); run.ToolCalls != 1 {
		t.Fatalf("Run 3 = %+v", run)
	}
	var position crossPositionResult
	if err := json.Unmarshal(decodeToolResult(t, gateway.result(t, "set-tp")).Result, &position); err != nil || position.StopLoss != 57000 ||
		position.TakeProfit != 65000 || position.StopLossSource != nil || position.TakeProfitSource != nil {
		t.Fatalf("stops after Runs 2 and 3 = %s (%v)", gateway.result(t, "set-tp"), err)
	}

	// The Session is stopped and the Agent Server goes away; the Backend still fires the take-profit.
	if status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil); status != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", status, raw)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close Agent runtime: %v", err)
	}
	source.set("65000")
	raw := waitForBackend(t, backendURL+"/v1/orders?limit=1", account.Token, "take-profit close", func(body []byte) bool {
		return strings.Contains(string(body), `"trigger":"take_profit"`)
	})
	var orders struct {
		Orders []crossOrderResult `json:"orders"`
	}
	if err := json.Unmarshal(raw, &orders); err != nil || len(orders.Orders) != 1 || orders.Orders[0].Source == nil ||
		orders.Orders[0].Source.SessionID != session.ID || orders.Orders[0].Source.RunID != 3 || orders.Orders[0].AvgFillPrice != 65000 {
		t.Fatalf("take-profit close order = %s (%v)", raw, err)
	}
	if status, raw := crossHTTPRequest(t, http.MethodGet, backendURL+"/v1/positions", account.Token, nil); status != http.StatusOK || !strings.Contains(string(raw), `"positions":[]`) {
		t.Fatalf("position after take-profit = %d %s", status, raw)
	}
	run3 := backendLedger(t, backendURL, account.Token, "?session_id="+session.ID+"&run_id=3")
	if len(run3) != 2 || run3[0].Event != "fill" || run3[0].Trigger != "take_profit" || run3[0].PositionID != positionID ||
		run3[0].RealizedPnL != 500 || run3[0].Fee != 2.6 || run3[1].Event != "stops_updated" || run3[1].TakeProfit != 65000 {
		t.Fatalf("Run 3 Backend ledger = %+v", run3)
	}
	if run2 := backendLedger(t, backendURL, account.Token, "?session_id="+session.ID+"&run_id=2"); len(run2) != 1 || run2[0].Event != "stops_updated" || run2[0].StopLoss != 57000 {
		t.Fatalf("Run 2 Backend ledger = %+v", run2)
	}

	// After the Agent Server restarts, a new Run can look the trigger up by the Run that set it.
	gateway.script(account.ID,
		scriptedCall{ID: "run-3", Name: "get_ledger", Arguments: map[string]any{"run_id": 3}},
		scriptedCall{ID: "run-2", Name: "get_ledger", Arguments: map[string]any{"run_id": 2}},
	)
	restarted := openTradingCrossRuntime(t, backendURL, gateway, dbPath)
	if status, raw := runtimeRequest(t, restarted.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil); status != http.StatusAccepted {
		t.Fatalf("start after restart status=%d body=%s", status, raw)
	}
	if run := waitForRunStatus(t, restarted, session.ID, 4, "completed"); run.ToolCalls != 2 {
		t.Fatalf("Run 4 = %+v", run)
	}
	var ledger struct {
		Entries []ledgerEntryResult `json:"entries"`
	}
	if err := json.Unmarshal(decodeToolResult(t, gateway.result(t, "run-3")).Result, &ledger); err != nil || len(ledger.Entries) != 2 ||
		ledger.Entries[0].Trigger != "take_profit" || ledger.Entries[0].Source != nil {
		t.Fatalf("ledger of Run 3 via tool = %s (%v)", gateway.result(t, "run-3"), err)
	}
	if err := json.Unmarshal(decodeToolResult(t, gateway.result(t, "run-2")).Result, &ledger); err != nil || len(ledger.Entries) != 1 || ledger.Entries[0].Event != "stops_updated" {
		t.Fatalf("ledger of Run 2 via tool = %s (%v)", gateway.result(t, "run-2"), err)
	}
}
