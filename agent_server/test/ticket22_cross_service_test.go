package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type crossOrderResult struct {
	ID           string         `json:"id"`
	Status       string         `json:"status"`
	AvgFillPrice float64        `json:"avg_fill_price"`
	Fee          float64        `json:"fee"`
	Trigger      string         `json:"trigger"`
	Source       *backendSource `json:"source"`
}

func restartSessionRun(t *testing.T, runtime *common.AgentRuntime, sessionID string) {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+sessionID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", status, raw)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+sessionID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("restart status=%d body=%s", status, raw)
	}
}

func TestTicket22CrossServiceOrdersAndLedgerTraceToRunEvenWhenFilledLater(t *testing.T) {
	source := newSettablePriceSource(t, "60000")
	backendURL := startTradingBackend(t, source.server.URL)
	account := createTradingAccount(t, backendURL, "ticket-22-cross", 100000)
	gateway := newScriptedGateway()
	gateway.script(account.ID,
		scriptedCall{ID: "market", Name: "place_order", Arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 0.1, "leverage": 10}},
		scriptedCall{ID: "limit", Name: "place_order", Arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "type": "limit", "quantity": 0.2, "price": 50000, "leverage": 10}},
		scriptedCall{ID: "ledger", Name: "get_ledger", Arguments: map[string]any{}},
	)
	fixture := newRuntimeFixture()
	fixture.generate = gateway.generate
	runtime := openCrossServiceRuntime(t, backendURL, crossBackendUserToken, fixture, 2*time.Second)
	session := createStoppedSession(t, runtime, map[string]any{"account_id": account.ID, "max_loop": 8, "max_tool_call": 3})
	startSessionRun(t, runtime, session)
	if run := waitForRunStatus(t, runtime, session.ID, 1, "completed"); run.ToolCalls != 3 {
		t.Fatalf("Run 1 = %+v", run)
	}

	marketResult := decodeToolResult(t, gateway.result(t, "market"))
	var market crossOrderResult
	if err := json.Unmarshal(marketResult.Result, &market); err != nil || !marketResult.OK || market.Status != "filled" ||
		market.AvgFillPrice != 60000 || market.Fee != 2.4 || market.Source != nil {
		t.Fatalf("market order via actual Backend = %s (%v)", gateway.result(t, "market"), err)
	}
	limitResult := decodeToolResult(t, gateway.result(t, "limit"))
	var limit crossOrderResult
	if err := json.Unmarshal(limitResult.Result, &limit); err != nil || !limitResult.OK || limit.Status != "open" || limit.Source != nil {
		t.Fatalf("limit order via actual Backend = %s (%v)", gateway.result(t, "limit"), err)
	}
	ledgerResult := decodeToolResult(t, gateway.result(t, "ledger"))
	var ledger struct {
		Entries []ledgerEntryResult `json:"entries"`
	}
	if err := json.Unmarshal(ledgerResult.Result, &ledger); err != nil || !ledgerResult.OK || len(ledger.Entries) != 2 ||
		ledger.Entries[0].Event != "order_placed" || ledger.Entries[0].OrderID != limit.ID || ledger.Entries[0].Source != nil ||
		ledger.Entries[1].Event != "fill" || ledger.Entries[1].OrderID != market.ID || ledger.Entries[1].BalanceDelta != -2.4 ||
		ledger.Entries[1].BalanceAfter != 99997.6 || ledger.Entries[1].Source != nil {
		t.Fatalf("ledger via actual Backend = %s (%v)", gateway.result(t, "ledger"), err)
	}

	// Run 1 is over; the engine fills the resting limit order from its persisted source.
	source.set("49000")
	raw := waitForBackend(t, backendURL+"/v1/orders/"+limit.ID, account.Token, "delayed limit fill", func(body []byte) bool {
		return strings.Contains(string(body), `"status":"filled"`)
	})
	var filled crossOrderResult
	if err := json.Unmarshal(raw, &filled); err != nil || filled.AvgFillPrice != 50000 || filled.Fee != 2 ||
		filled.Source == nil || filled.Source.SessionID != session.ID || filled.Source.RunID != 1 {
		t.Fatalf("delayed fill = %s (%v)", raw, err)
	}
	entries := backendLedger(t, backendURL, account.Token, "?session_id="+session.ID+"&run_id=1")
	if len(entries) != 3 || entries[0].Event != "fill" || entries[0].OrderID != limit.ID || entries[0].Price != 50000 ||
		entries[0].BalanceDelta != -2 || entries[0].BalanceAfter != 99995.6 || entries[0].Source.RunID != 1 {
		t.Fatalf("Backend ledger after delayed fill = %+v", entries)
	}

	// A later Run reads its predecessor's ledger and the resulting position through the tools.
	gateway.script(account.ID,
		scriptedCall{ID: "previous-run", Name: "get_ledger", Arguments: map[string]any{"run_id": 1}},
		scriptedCall{ID: "positions", Name: "get_positions", Arguments: map[string]any{}},
	)
	restartSessionRun(t, runtime, session.ID)
	if run := waitForRunStatus(t, runtime, session.ID, 2, "completed"); run.ToolCalls != 2 {
		t.Fatalf("Run 2 = %+v", run)
	}
	previous := decodeToolResult(t, gateway.result(t, "previous-run"))
	if err := json.Unmarshal(previous.Result, &ledger); err != nil || !previous.OK || len(ledger.Entries) != 3 {
		t.Fatalf("Run 2 ledger view = %s (%v)", gateway.result(t, "previous-run"), err)
	}
	for _, entry := range ledger.Entries {
		if entry.Source != nil {
			t.Fatalf("compact ledger leaked source metadata: %+v", entry)
		}
	}
	positions := decodeToolResult(t, gateway.result(t, "positions"))
	if !positions.OK || !strings.Contains(string(positions.Result), `"quantity":0.3`) || !strings.Contains(string(positions.Result), `"entry_price":53333.33333333`) {
		t.Fatalf("positions after both fills = %s", gateway.result(t, "positions"))
	}
}
