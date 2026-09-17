package test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func waitForMarkPrice(t *testing.T, backendURL, token, price string) {
	t.Helper()
	waitForBackend(t, backendURL+"/v1/positions", token, "mark price "+price, func(body []byte) bool {
		return strings.Contains(string(body), `"mark_price":`+price)
	})
}

func TestTicket23CrossServiceCancelAndCloseKeepCreationAndOperationRuns(t *testing.T) {
	source := newSettablePriceSource(t, "60000")
	backendURL := startTradingBackend(t, source.server.URL)
	account := createTradingAccount(t, backendURL, "ticket-23-cross", 100000)
	gateway := newScriptedGateway()
	gateway.script(account.ID,
		scriptedCall{ID: "limit", Name: "place_order", Arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "type": "limit", "quantity": 0.2, "price": 50000, "leverage": 10}},
		scriptedCall{ID: "market", Name: "place_order", Arguments: map[string]any{"symbol": "BTCUSDT", "side": "buy", "quantity": 0.3, "leverage": 10}},
		scriptedCall{ID: "positions", Name: "get_positions", Arguments: map[string]any{}},
	)
	fixture := newRuntimeFixture()
	fixture.generate = gateway.generate
	runtime := openCrossServiceRuntime(t, backendURL, crossBackendUserToken, fixture, 2*time.Second)
	session := createStoppedSession(t, runtime, map[string]any{"account_id": account.ID, "max_loop": 10, "max_tool_call": 6})
	startSessionRun(t, runtime, session)
	if run := waitForRunStatus(t, runtime, session.ID, 1, "completed"); run.ToolCalls != 3 {
		t.Fatalf("Run 1 = %+v", run)
	}
	var limit crossOrderResult
	if err := json.Unmarshal(decodeToolResult(t, gateway.result(t, "limit")).Result, &limit); err != nil || limit.Status != "open" {
		t.Fatalf("limit order = %s (%v)", gateway.result(t, "limit"), err)
	}
	var positions struct {
		Positions []struct {
			ID string `json:"id"`
		} `json:"positions"`
	}
	if err := json.Unmarshal(decodeToolResult(t, gateway.result(t, "positions")).Result, &positions); err != nil || len(positions.Positions) != 1 {
		t.Fatalf("positions = %s (%v)", gateway.result(t, "positions"), err)
	}
	positionID := positions.Positions[0].ID

	source.set("61000")
	waitForMarkPrice(t, backendURL, account.Token, "61000")
	gateway.script(account.ID,
		scriptedCall{ID: "cancel", Name: "cancel_order", Arguments: map[string]any{"order_id": limit.ID}},
		scriptedCall{ID: "cancel-again", Name: "cancel_order", Arguments: map[string]any{"order_id": limit.ID}},
		scriptedCall{ID: "close-too-much", Name: "close_position", Arguments: map[string]any{"position_id": positionID, "quantity": 5}},
		scriptedCall{ID: "close", Name: "close_position", Arguments: map[string]any{"position_id": positionID, "quantity": 0.1}},
		scriptedCall{ID: "close-foreign", Name: "close_position", Arguments: map[string]any{"position_id": "pos_missing"}},
		scriptedCall{ID: "ledger", Name: "get_ledger", Arguments: map[string]any{}},
	)
	restartSessionRun(t, runtime, session.ID)
	if run := waitForRunStatus(t, runtime, session.ID, 2, "completed"); run.ToolCalls != 6 {
		t.Fatalf("Run 2 = %+v", run)
	}

	var canceled crossOrderResult
	cancel := decodeToolResult(t, gateway.result(t, "cancel"))
	if err := json.Unmarshal(cancel.Result, &canceled); err != nil || !cancel.OK || canceled.Status != "canceled" || canceled.Source != nil {
		t.Fatalf("cancel via actual Backend = %s (%v)", gateway.result(t, "cancel"), err)
	}
	again := decodeToolResult(t, gateway.result(t, "cancel-again"))
	if again.OK || again.Error.Code != "order_not_open" || again.Error.Outcome != "not_executed" || !strings.Contains(again.Error.Message, "canceled") {
		t.Fatalf("repeated cancel = %s", gateway.result(t, "cancel-again"))
	}
	tooMuch := decodeToolResult(t, gateway.result(t, "close-too-much"))
	if tooMuch.OK || tooMuch.Error.Code != "invalid_request" || tooMuch.Error.Outcome != "not_executed" {
		t.Fatalf("close too much = %s", gateway.result(t, "close-too-much"))
	}
	var closed crossOrderResult
	closeResult := decodeToolResult(t, gateway.result(t, "close"))
	if err := json.Unmarshal(closeResult.Result, &closed); err != nil || !closeResult.OK || closed.Status != "filled" || closed.AvgFillPrice != 61000 ||
		closed.Fee != 2.44 || closed.Source != nil || closed.Trigger != "" {
		t.Fatalf("partial close via actual Backend = %s (%v)", gateway.result(t, "close"), err)
	}
	foreign := decodeToolResult(t, gateway.result(t, "close-foreign"))
	if foreign.OK || foreign.Error.Code != "not_found" || foreign.Error.Outcome != "not_executed" {
		t.Fatalf("close unknown position = %s", gateway.result(t, "close-foreign"))
	}
	var ledger struct {
		Entries []ledgerEntryResult `json:"entries"`
	}
	if err := json.Unmarshal(decodeToolResult(t, gateway.result(t, "ledger")).Result, &ledger); err != nil || len(ledger.Entries) != 4 {
		t.Fatalf("ledger via tool = %s (%v)", gateway.result(t, "ledger"), err)
	}
	entries := ledger.Entries
	if entries[0].Event != "fill" || entries[0].OrderID != closed.ID || entries[0].PositionID != positionID || entries[0].RealizedPnL != 100 ||
		entries[0].BalanceDelta != 97.56 || entries[0].Source != nil ||
		entries[1].Event != "order_canceled" || entries[1].OrderID != limit.ID || entries[1].Source != nil ||
		entries[2].Event != "fill" || entries[2].Source != nil || entries[3].Event != "order_placed" || entries[3].OrderID != limit.ID || entries[3].Source != nil {
		t.Fatalf("ledger causality = %+v", entries)
	}
	if run2 := backendLedger(t, backendURL, account.Token, "?session_id="+session.ID+"&run_id=2"); len(run2) != 2 || run2[0].Event != "fill" || run2[1].Event != "order_canceled" {
		t.Fatalf("Run 2 Backend ledger = %+v", run2)
	}
}
