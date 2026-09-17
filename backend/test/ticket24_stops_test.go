package test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"backend/internal/trading"
)

type positionView struct {
	ID               string      `json:"id"`
	Side             string      `json:"side"`
	Quantity         float64     `json:"quantity"`
	StopLoss         float64     `json:"stop_loss"`
	TakeProfit       float64     `json:"take_profit"`
	StopLossSource   *sourceView `json:"stop_loss_source"`
	TakeProfitSource *sourceView `json:"take_profit_source"`
}

func setStops(t *testing.T, backend *tradingAPI, token, positionID string, body map[string]any) (int, []byte) {
	t.Helper()
	return backend.request(t, http.MethodPatch, "/v1/positions/"+positionID, token, body)
}

func sourceRun(source *sourceView) int64 {
	if source == nil {
		return 0
	}
	return source.RunID
}

func TestTicket24StopsKeepSeparateSourcesAndTriggerTracesToSetterAcrossRestart(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-long", 100000)
	_, otherToken := backend.createAccount(t, "ticket-24-other", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 58000,
		"source": agentSource("s_1", 1),
	})
	position := listPositions(t, backend, token).Positions[0]
	if position.StopLoss != 58000 || sourceRun(position.StopLossSource) != 1 || position.TakeProfitSource != nil {
		t.Fatalf("position opened with stop_loss = %+v", position)
	}
	positionID := position.ID

	for name, body := range map[string]map[string]any{
		"no change":       {"source": agentSource("s_1", 2)},
		"negative":        {"stop_loss": -1, "source": agentSource("s_1", 2)},
		"wrong side":      {"stop_loss": 61000, "source": agentSource("s_1", 2)},
		"invalid source":  {"take_profit": 65000, "source": map[string]any{"session_id": "s_1"}},
		"take profit low": {"take_profit": 59000, "source": agentSource("s_1", 2)},
	} {
		if status, raw := setStops(t, backend, token, positionID, body); status != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", name, status, raw)
		}
	}
	if status, raw := setStops(t, backend, token, "pos_missing", map[string]any{"take_profit": 65000}); status != http.StatusNotFound {
		t.Fatalf("unknown position status=%d body=%s", status, raw)
	}
	if status, raw := setStops(t, backend, otherToken, positionID, map[string]any{"take_profit": 65000}); status != http.StatusNotFound {
		t.Fatalf("cross-Account stops status=%d body=%s", status, raw)
	}
	if entries := backend.ledger(t, token, ""); len(entries) != 1 {
		t.Fatalf("rejected stop changes wrote ledger entries: %+v", entries)
	}

	status, raw := setStops(t, backend, token, positionID, map[string]any{"take_profit": 65000, "source": agentSource("s_1", 2)})
	updated := decodeJSON[positionView](t, raw)
	if status != http.StatusOK || updated.StopLoss != 58000 || updated.TakeProfit != 65000 || sourceRun(updated.StopLossSource) != 1 || sourceRun(updated.TakeProfitSource) != 2 {
		t.Fatalf("set take_profit = %d %s", status, raw)
	}
	status, raw = setStops(t, backend, token, positionID, map[string]any{"stop_loss": 57000, "source": agentSource("s_1", 3)})
	updated = decodeJSON[positionView](t, raw)
	if status != http.StatusOK || updated.StopLoss != 57000 || sourceRun(updated.StopLossSource) != 3 || sourceRun(updated.TakeProfitSource) != 2 {
		t.Fatalf("update stop_loss = %d %s", status, raw)
	}
	status, raw = setStops(t, backend, token, positionID, map[string]any{"stop_loss": 0, "source": agentSource("s_1", 4)})
	updated = decodeJSON[positionView](t, raw)
	if status != http.StatusOK || updated.StopLoss != 0 || updated.StopLossSource != nil || updated.TakeProfit != 65000 || sourceRun(updated.TakeProfitSource) != 2 ||
		strings.Contains(string(raw), `"stop_loss":`) {
		t.Fatalf("remove stop_loss = %d %s", status, raw)
	}
	entries := backend.ledger(t, token, "")
	if len(entries) != 4 || entries[0].Event != "stops_updated" || entries[0].StopLoss != 0 || entries[0].TakeProfit != 65000 || entries[0].Source.RunID != 4 ||
		entries[0].PositionID != positionID || entries[1].StopLoss != 57000 || entries[1].Source.RunID != 3 ||
		entries[2].StopLoss != 58000 || entries[2].TakeProfit != 65000 || entries[2].Source.RunID != 2 || entries[3].Event != "fill" {
		t.Fatalf("stops ledger = %+v", entries)
	}

	// The Backend restarts; the persisted take-profit still traces to Run 2 when it fires.
	restarted := newTradingAPI(t, backend.dbPath)
	restarted.setPrice(t, 65000)
	if result := restarted.engine.Tick(context.Background()); result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("take-profit tick = %+v", result)
	}
	if positions := listPositions(t, restarted, token); len(positions.Positions) != 0 {
		t.Fatalf("position survived take-profit: %+v", positions)
	}
	status, raw = restarted.request(t, http.MethodGet, "/v1/orders?limit=1", token, nil)
	orders := decodeJSON[struct {
		Orders []orderView `json:"orders"`
	}](t, raw)
	if status != http.StatusOK || len(orders.Orders) != 1 || orders.Orders[0].Trigger != "take_profit" || orders.Orders[0].RealizedPnL != 500 ||
		orders.Orders[0].Fee != 2.6 || orders.Orders[0].Source == nil || orders.Orders[0].Source.RunID != 2 {
		t.Fatalf("take-profit close order = %s", raw)
	}
	entries = restarted.ledger(t, token, "")
	if len(entries) != 5 || entries[0].Event != "fill" || entries[0].Trigger != "take_profit" || entries[0].PositionID != positionID ||
		entries[0].RealizedPnL != 500 || entries[0].BalanceDelta != 497.4 || entries[0].Source.RunID != 2 || entries[0].Source.SessionID != "s_1" {
		t.Fatalf("take-profit ledger = %+v", entries[0])
	}
}

func TestTicket24ShortStopLossTriggersWithBothThresholdsAndTracesToSetter(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-short", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "sell", "type": "market", "quantity": 0.1, "leverage": 5, "source": agentSource("s_2", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID
	status, raw := setStops(t, backend, token, positionID, map[string]any{"stop_loss": 62000, "take_profit": 55000, "source": agentSource("s_2", 2)})
	updated := decodeJSON[positionView](t, raw)
	if status != http.StatusOK || updated.Side != "short" || sourceRun(updated.StopLossSource) != 2 || sourceRun(updated.TakeProfitSource) != 2 {
		t.Fatalf("short stops = %d %s", status, raw)
	}
	backend.setPrice(t, 61000)
	if result := backend.engine.Tick(context.Background()); result.Closed != 0 {
		t.Fatalf("price inside both thresholds must not close: %+v", result)
	}
	backend.setPrice(t, 62000)
	if result := backend.engine.Tick(context.Background()); result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("stop-loss tick = %+v", result)
	}
	if result := backend.engine.Tick(context.Background()); result.Closed != 0 || result.Errors != 0 {
		t.Fatalf("second tick settled again: %+v", result)
	}
	entries := backend.ledger(t, token, "")
	if len(entries) != 3 || entries[0].Event != "fill" || entries[0].Trigger != "stop_loss" || entries[0].RealizedPnL != -200 || entries[0].Fee != 2.48 ||
		entries[0].Source.RunID != 2 || entries[0].Source.SessionID != "s_2" || entries[1].Event != "stops_updated" || entries[2].Source.RunID != 1 {
		t.Fatalf("short stop-loss ledger = %+v", entries)
	}
}

func TestTicket24StopRemovedBeforeTriggerIsNotClosedAndTriggerBeforeRemovalIs404(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-sequence", 100000)
	open := func(t *testing.T) string {
		t.Helper()
		placeOrder(t, backend, token, map[string]any{
			"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 59500,
			"source": agentSource("s_seq", 1),
		})
		return listPositions(t, backend, token).Positions[0].ID
	}

	// Removal committed before the engine looks: nothing closes, the position keeps living without a stop.
	positionID := open(t)
	backend.setPrice(t, 59000)
	if status, raw := setStops(t, backend, token, positionID, map[string]any{"stop_loss": 0, "source": agentSource("s_seq", 2)}); status != http.StatusOK {
		t.Fatalf("remove stop status=%d body=%s", status, raw)
	}
	if result := backend.engine.Tick(context.Background()); result.Closed != 0 || result.Errors != 0 {
		t.Fatalf("tick after removal = %+v", result)
	}
	positions := listPositions(t, backend, token)
	entries := backend.ledger(t, token, "")
	if len(positions.Positions) != 1 || positions.Positions[0].StopLoss != 0 || len(entries) != 2 || entries[0].Event != "stops_updated" || entries[0].StopLoss != 0 || entries[1].Event != "fill" {
		t.Fatalf("after removal: positions=%+v ledger=%+v", positions.Positions, entries)
	}
	if status, raw := backend.request(t, http.MethodPost, "/v1/positions/"+positionID+"/close", token, map[string]any{"source": agentSource("s_seq", 2)}); status != http.StatusOK {
		t.Fatalf("close status=%d body=%s", status, raw)
	}

	// Trigger committed before the removal: the removal finds no position and writes nothing.
	backend.setPrice(t, 60000)
	positionID = open(t)
	backend.setPrice(t, 59000)
	if result := backend.engine.Tick(context.Background()); result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("trigger tick = %+v", result)
	}
	if status, raw := setStops(t, backend, token, positionID, map[string]any{"stop_loss": 0, "source": agentSource("s_seq", 3)}); status != http.StatusNotFound {
		t.Fatalf("remove after trigger status=%d body=%s", status, raw)
	}
	entries = backend.ledger(t, token, "?limit=2")
	if len(entries) != 2 || entries[0].Event != "fill" || entries[0].Trigger != "stop_loss" || entries[0].PositionID != positionID ||
		sourceRun(entries[0].Source) != 1 || entries[1].Event != "fill" || entries[1].Trigger != "" {
		t.Fatalf("ledger after trigger = %+v", entries)
	}
}

// The engine decides from a snapshot, but the close must be re-evaluated on the persisted
// position: a stop removed while the engine waits for the price must not fire.
func TestTicket24StopRemovedWhileEngineSnapshotIsStaleDoesNotClose(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-stale", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 59500,
		"source": agentSource("s_stale", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID
	backend.setPrice(t, 59000)

	mark := backend.stallPrices(t)
	ticked := make(chan trading.TickResult, 1)
	go func() { ticked <- backend.engine.Tick(context.Background()) }()
	backend.waitForPriceLookup(t, mark) // the snapshot (stop_loss 59500) is taken; the engine is waiting for the price
	removed := make(chan int, 1)
	go func() {
		status, _ := setStops(t, backend, token, positionID, map[string]any{"stop_loss": 0, "source": agentSource("s_stale", 2)})
		removed <- status
	}()
	backend.waitForLedgerEvent(t, token, "stops_updated", 2) // the removal is committed; its response only waits for the mark price
	backend.resumePrices()
	if result := <-ticked; result.Closed != 0 || result.Errors != 0 {
		t.Fatalf("engine closed from a stale snapshot: %+v", result)
	}
	if status := <-removed; status != http.StatusOK {
		t.Fatalf("remove stop while engine waits status=%d", status)
	}

	positions := listPositions(t, backend, token)
	entries := backend.ledger(t, token, "")
	if len(positions.Positions) != 1 || positions.Positions[0].ID != positionID || positions.Positions[0].StopLoss != 0 || positions.Positions[0].StopLossSource != nil ||
		len(entries) != 2 || entries[0].Event != "stops_updated" || entries[0].StopLoss != 0 || sourceRun(entries[0].Source) != 2 || entries[1].Event != "fill" {
		t.Fatalf("positions=%+v ledger=%+v", positions.Positions, entries)
	}
	if result := backend.engine.Tick(context.Background()); result.Closed != 0 || result.Errors != 0 {
		t.Fatalf("second tick = %+v", result)
	}
}

func TestTicket24MovedStopLossTriggersAtNewLevelAndTracesToMover(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-moved", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 59500,
		"source": agentSource("s_move", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID
	if status, raw := setStops(t, backend, token, positionID, map[string]any{"stop_loss": 57000, "source": agentSource("s_move", 2)}); status != http.StatusOK {
		t.Fatalf("move stop status=%d body=%s", status, raw)
	}
	backend.setPrice(t, 59000)
	if result := backend.engine.Tick(context.Background()); result.Closed != 0 || result.Errors != 0 {
		t.Fatalf("old level must not fire: %+v", result)
	}
	backend.setPrice(t, 57000)
	if result := backend.engine.Tick(context.Background()); result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("new level must fire: %+v", result)
	}
	entries := backend.ledger(t, token, "?limit=1")
	if len(entries) != 1 || entries[0].Event != "fill" || entries[0].Trigger != "stop_loss" || entries[0].Price != 57000 || sourceRun(entries[0].Source) != 2 {
		t.Fatalf("triggered fill = %+v", entries)
	}
}

func TestTicket24ConcurrentStopRemovalAndTriggerNeverDoubleSettle(t *testing.T) {
	backend := newTradingAPI(t, "")
	backend.setPrice(t, 60000)
	closed, kept := 0, 0
	for round := 0; round < 12; round++ {
		_, token := backend.createAccount(t, "ticket-24-race-"+strconv.Itoa(round), 100000)
		placeOrder(t, backend, token, map[string]any{
			"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 59500,
			"source": agentSource("s_race", 1),
		})
		positionID := listPositions(t, backend, token).Positions[0].ID
		backend.setPrice(t, 59000)
		var wait sync.WaitGroup
		wait.Add(2)
		var patchStatus int
		go func() {
			defer wait.Done()
			backend.engine.Tick(context.Background())
		}()
		go func() {
			defer wait.Done()
			patchStatus, _ = setStops(t, backend, token, positionID, map[string]any{"stop_loss": 0, "source": agentSource("s_race", 2)})
		}()
		wait.Wait()
		backend.engine.Tick(context.Background())

		positions := listPositions(t, backend, token)
		entries := backend.ledger(t, token, "")
		fills, stopUpdates := 0, 0
		for _, entry := range entries {
			switch {
			case entry.Event == "fill" && entry.Trigger == "stop_loss":
				fills++
				if entry.Source == nil || entry.Source.RunID != 1 {
					t.Fatalf("round %d: triggered fill lost its setter: %+v", round, entry)
				}
			case entry.Event == "stops_updated":
				stopUpdates++
			}
		}
		switch {
		case len(positions.Positions) == 0 && fills == 1 && stopUpdates == 0 && patchStatus == http.StatusNotFound:
			closed++
		case len(positions.Positions) == 1 && fills == 0 && stopUpdates == 1 && patchStatus == http.StatusOK &&
			positions.Positions[0].StopLoss == 0 && positions.Positions[0].StopLossSource == nil:
			kept++
		default:
			t.Fatalf("round %d: patch=%d positions=%+v ledger=%+v", round, patchStatus, positions.Positions, entries)
		}
		backend.setPrice(t, 60000)
	}
	if closed+kept != 12 {
		t.Fatalf("closed=%d kept=%d", closed, kept)
	}
}

// A committed stop change is answered 2xx even when the market is unavailable afterwards:
// the Agent treats every non-500 error as "not executed" (SPEC §21.5).
func TestTicket24SetStopsSucceedsWhenMarketIsUnavailableAfterCommit(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-offline", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 58000,
		"source": agentSource("s_off", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID

	backend.stallPrices(t)
	status, raw := setStops(t, backend, token, positionID, map[string]any{"stop_loss": 0, "source": agentSource("s_off", 2)})
	removed := decodeJSON[struct {
		positionView
		MarkPrice  float64 `json:"mark_price"`
		Unrealized float64 `json:"unrealized_pnl"`
	}](t, raw)
	if status != http.StatusOK || strings.Contains(string(raw), `"stop_loss":`) || removed.StopLossSource != nil || removed.MarkPrice != 0 || removed.Unrealized != 0 {
		t.Fatalf("remove stop while market is unavailable = %d %s", status, raw)
	}
	entries := backend.ledger(t, token, "?limit=1")
	if len(entries) != 1 || entries[0].Event != "stops_updated" || entries[0].StopLoss != 0 || entries[0].PositionID != positionID || sourceRun(entries[0].Source) != 2 {
		t.Fatalf("ledger after offline removal = %+v", entries)
	}
	backend.resumePrices()
	backend.setPrice(t, 60000)
	if positions := listPositions(t, backend, token); len(positions.Positions) != 1 || positions.Positions[0].StopLoss != 0 || positions.Positions[0].StopLossSource != nil {
		t.Fatalf("removal not persisted: %+v", positions.Positions)
	}

	status, raw = setStops(t, backend, token, positionID, map[string]any{"take_profit": 65000, "source": agentSource("s_off", 3)})
	updated := decodeJSON[struct {
		positionView
		MarkPrice float64 `json:"mark_price"`
	}](t, raw)
	if status != http.StatusOK || updated.TakeProfit != 65000 || updated.MarkPrice != 60000 || sourceRun(updated.TakeProfitSource) != 3 {
		t.Fatalf("set take_profit with market available = %d %s", status, raw)
	}
}

// Two stop changes in flight at once must not overwrite each other: the write happens on the
// position re-read inside the locked transaction, not on the snapshot taken before the price check.
func TestTicket24ConcurrentStopLossAndTakeProfitChangesKeepBoth(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-both", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "take_profit": 65000,
		"source": agentSource("s_both", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID

	mark := backend.stallPrices(t)
	type response struct {
		status int
		raw    []byte
	}
	setStopLoss := make(chan response, 1)
	go func() {
		status, raw := setStops(t, backend, token, positionID, map[string]any{"stop_loss": 58000, "source": agentSource("s_both", 2)})
		setStopLoss <- response{status, raw}
	}()
	backend.waitForPriceLookup(t, mark) // the stop_loss request has read the position (take_profit 65000) and waits for the price
	removeTakeProfit := make(chan response, 1)
	go func() {
		status, raw := setStops(t, backend, token, positionID, map[string]any{"take_profit": 0, "source": agentSource("s_both", 3)})
		removeTakeProfit <- response{status, raw}
	}()
	backend.waitForLedgerEvent(t, token, "stops_updated", 3) // the removal is committed while the stop_loss request still waits
	backend.resumePrices()
	if removal := <-removeTakeProfit; removal.status != http.StatusOK {
		t.Fatalf("remove take_profit status=%d body=%s", removal.status, removal.raw)
	}
	result := <-setStopLoss
	written := decodeJSON[positionView](t, result.raw)
	if result.status != http.StatusOK || written.StopLoss != 58000 || sourceRun(written.StopLossSource) != 2 || written.TakeProfit != 0 || written.TakeProfitSource != nil {
		t.Fatalf("stop_loss response after concurrent take_profit removal = %d %s", result.status, result.raw)
	}

	positions := listPositions(t, backend, token)
	entries := backend.ledger(t, token, "?limit=2")
	if len(positions.Positions) != 1 || positions.Positions[0].StopLoss != 58000 || sourceRun(positions.Positions[0].StopLossSource) != 2 ||
		positions.Positions[0].TakeProfit != 0 || positions.Positions[0].TakeProfitSource != nil {
		t.Fatalf("final position = %+v", positions.Positions)
	}
	if len(entries) != 2 || entries[0].Event != "stops_updated" || entries[0].StopLoss != 58000 || entries[0].TakeProfit != 0 || sourceRun(entries[0].Source) != 2 ||
		entries[1].Event != "stops_updated" || entries[1].StopLoss != 0 || entries[1].TakeProfit != 0 || sourceRun(entries[1].Source) != 3 {
		t.Fatalf("stops ledger = %+v", entries)
	}
}

// A same-direction order that carries only one stop level replaces that level and its
// setter; the other level keeps its value and setter. A flip still clears both.
func TestTicket24OrderAttachedStopKeepsTheOtherLevelAndItsSetter(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-24-attach", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "source": agentSource("s_att", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID
	if status, raw := setStops(t, backend, token, positionID, map[string]any{"take_profit": 65000, "source": agentSource("s_att", 2)}); status != http.StatusOK {
		t.Fatalf("set take_profit status=%d body=%s", status, raw)
	}
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 58000,
		"source": agentSource("s_att", 3),
	})
	position := listPositions(t, backend, token).Positions[0]
	if position.ID != positionID || position.Quantity != 0.2 || position.StopLoss != 58000 || sourceRun(position.StopLossSource) != 3 ||
		position.TakeProfit != 65000 || sourceRun(position.TakeProfitSource) != 2 {
		t.Fatalf("position after adding with stop_loss only = %+v", position)
	}
	backend.setPrice(t, 65000)
	if result := backend.engine.Tick(context.Background()); result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("take-profit tick = %+v", result)
	}
	entries := backend.ledger(t, token, "?limit=1")
	if len(entries) != 1 || entries[0].Trigger != "take_profit" || entries[0].Quantity != 0.2 || sourceRun(entries[0].Source) != 2 {
		t.Fatalf("take-profit fill = %+v", entries)
	}

	backend.setPrice(t, 60000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "stop_loss": 58000, "source": agentSource("s_att", 4),
	})
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "sell", "type": "market", "quantity": 0.2, "leverage": 10, "take_profit": 55000, "source": agentSource("s_att", 5),
	})
	flipped := listPositions(t, backend, token).Positions[0]
	if flipped.Side != "short" || flipped.Quantity != 0.1 || flipped.StopLoss != 0 || flipped.StopLossSource != nil || flipped.TakeProfit != 55000 || sourceRun(flipped.TakeProfitSource) != 5 {
		t.Fatalf("flipped position = %+v", flipped)
	}
}
