package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func placeOrder(t *testing.T, backend *tradingAPI, token string, body map[string]any) orderView {
	t.Helper()
	status, raw := backend.request(t, http.MethodPost, "/v1/orders", token, body)
	if status != http.StatusCreated {
		t.Fatalf("place order status=%d body=%s", status, raw)
	}
	return decodeJSON[orderView](t, raw)
}

type positionListView struct {
	Positions []struct {
		ID               string      `json:"id"`
		Side             string      `json:"side"`
		Quantity         float64     `json:"quantity"`
		EntryPrice       float64     `json:"entry_price"`
		StopLoss         float64     `json:"stop_loss"`
		TakeProfit       float64     `json:"take_profit"`
		StopLossSource   *sourceView `json:"stop_loss_source"`
		TakeProfitSource *sourceView `json:"take_profit_source"`
	} `json:"positions"`
}

func listPositions(t *testing.T, backend *tradingAPI, token string) positionListView {
	t.Helper()
	status, raw := backend.request(t, http.MethodGet, "/v1/positions", token, nil)
	if status != http.StatusOK {
		t.Fatalf("positions status=%d body=%s", status, raw)
	}
	return decodeJSON[positionListView](t, raw)
}

func TestTicket23CancelKeepsCreatingRunAndRecordsCancelingRun(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-23-cancel", 100000)
	_, otherToken := backend.createAccount(t, "ticket-23-other", 100000)
	order := placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.2, "price": 50000, "leverage": 10,
		"source": agentSource("s_1", 1),
	})

	if status, raw := backend.request(t, http.MethodPost, "/v1/orders/"+order.ID+"/cancel", otherToken, map[string]any{"source": agentSource("s_9", 1)}); status != http.StatusNotFound {
		t.Fatalf("cross-Account cancel status=%d body=%s", status, raw)
	}
	if status, raw := backend.request(t, http.MethodPost, "/v1/orders/ord_missing/cancel", token, nil); status != http.StatusNotFound {
		t.Fatalf("unknown order cancel status=%d body=%s", status, raw)
	}
	if status, raw := backend.request(t, http.MethodPost, "/v1/orders/"+order.ID+"/cancel", token, map[string]any{"source": agentSource("", 2)}); status != http.StatusBadRequest {
		t.Fatalf("invalid cancel source status=%d body=%s", status, raw)
	}

	status, raw := backend.request(t, http.MethodPost, "/v1/orders/"+order.ID+"/cancel", token, map[string]any{"source": agentSource("s_1", 2)})
	canceled := decodeJSON[orderView](t, raw)
	if status != http.StatusOK || canceled.Status != "canceled" || canceled.Source == nil || canceled.Source.RunID != 1 {
		t.Fatalf("cancel status=%d body=%s", status, raw)
	}
	entries := backend.ledger(t, token, "")
	if len(entries) != 2 || entries[0].Event != "order_canceled" || entries[0].OrderID != order.ID || entries[0].Quantity != 0.2 ||
		entries[0].BalanceDelta != 0 || entries[0].Source == nil || entries[0].Source.RunID != 2 ||
		entries[1].Event != "order_placed" || entries[1].Source.RunID != 1 {
		t.Fatalf("ledger after cancel = %+v", entries)
	}

	status, raw = backend.request(t, http.MethodPost, "/v1/orders/"+order.ID+"/cancel", token, map[string]any{"source": agentSource("s_1", 3)})
	if status != http.StatusConflict || !strings.Contains(string(raw), `"error":"order_not_open"`) || !strings.Contains(string(raw), "canceled") {
		t.Fatalf("repeated cancel status=%d body=%s", status, raw)
	}
	filled := placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "source": agentSource("s_1", 3),
	})
	status, raw = backend.request(t, http.MethodPost, "/v1/orders/"+filled.ID+"/cancel", token, nil)
	if status != http.StatusConflict || !strings.Contains(string(raw), "filled") {
		t.Fatalf("cancel filled order status=%d body=%s", status, raw)
	}
	if entries := backend.ledger(t, token, ""); len(entries) != 3 {
		t.Fatalf("failed cancels changed the ledger: %+v", entries)
	}
}

func TestTicket23ClosePositionPartialAndFullTraceToClosingRun(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-23-close", 100000)
	_, otherToken := backend.createAccount(t, "ticket-23-other", 100000)
	opened := placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.3, "leverage": 10, "source": agentSource("s_1", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID
	backend.setPrice(t, 61000)

	if status, raw := backend.request(t, http.MethodPost, "/v1/positions/"+positionID+"/close", otherToken, map[string]any{"quantity": 0.1}); status != http.StatusNotFound {
		t.Fatalf("cross-Account close status=%d body=%s", status, raw)
	}
	if status, raw := backend.request(t, http.MethodPost, "/v1/positions/"+positionID+"/close", token, map[string]any{"quantity": 0.5}); status != http.StatusBadRequest {
		t.Fatalf("close too much status=%d body=%s", status, raw)
	}

	status, raw := backend.request(t, http.MethodPost, "/v1/positions/"+positionID+"/close", token, map[string]any{"quantity": 0.1, "source": agentSource("s_1", 2)})
	partial := decodeJSON[orderView](t, raw)
	if status != http.StatusOK || partial.Status != "filled" || partial.RealizedPnL != 100 || partial.Fee != 2.44 || partial.Trigger != "" ||
		partial.Source == nil || partial.Source.RunID != 2 || partial.ID == opened.ID {
		t.Fatalf("partial close = %d %s", status, raw)
	}
	positions := listPositions(t, backend, token)
	if len(positions.Positions) != 1 || positions.Positions[0].ID != positionID || positions.Positions[0].Quantity != 0.2 || positions.Positions[0].EntryPrice != 60000 {
		t.Fatalf("position after partial close = %+v", positions)
	}
	entries := backend.ledger(t, token, "")
	if len(entries) != 2 || entries[0].Event != "fill" || entries[0].OrderID != partial.ID || entries[0].PositionID != positionID ||
		entries[0].Quantity != 0.1 || entries[0].RealizedPnL != 100 || entries[0].BalanceDelta != 97.56 || entries[0].BalanceAfter != 100090.36 ||
		entries[0].Source.RunID != 2 || entries[1].OrderID != opened.ID || entries[1].Source.RunID != 1 {
		t.Fatalf("ledger after partial close = %+v", entries)
	}

	status, raw = backend.request(t, http.MethodPost, "/v1/positions/"+positionID+"/close", token, map[string]any{"source": agentSource("s_1", 3)})
	full := decodeJSON[orderView](t, raw)
	if status != http.StatusOK || full.Status != "filled" || full.RealizedPnL != 200 || full.Fee != 4.88 || full.Source.RunID != 3 {
		t.Fatalf("full close = %d %s", status, raw)
	}
	if positions := listPositions(t, backend, token); len(positions.Positions) != 0 {
		t.Fatalf("position remained after full close: %+v", positions)
	}
	entries = backend.ledger(t, token, "")
	if len(entries) != 3 || entries[0].PositionID != positionID || entries[0].Quantity != 0.2 || entries[0].BalanceDelta != 195.12 ||
		entries[0].BalanceAfter != 100285.48 || entries[0].Source.RunID != 3 {
		t.Fatalf("ledger after full close = %+v", entries)
	}
	status, raw = backend.request(t, http.MethodGet, "/v1/account", token, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"balance":100285.48`) || !strings.Contains(string(raw), `"locked_margin":0`) {
		t.Fatalf("account after closes = %d %s", status, raw)
	}
	if status, raw = backend.request(t, http.MethodPost, "/v1/positions/"+positionID+"/close", token, nil); status != http.StatusNotFound {
		t.Fatalf("closing a closed position status=%d body=%s", status, raw)
	}
}

func TestTicket23CancelBeforeFillAndFillBeforeCancelAreBothFinal(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-23-order", 100000)
	backend.setPrice(t, 49000)

	// Cancel first: the engine's later look finds nothing open and settles nothing.
	canceled := placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.01, "price": 50000, "leverage": 10, "source": agentSource("s_1", 1),
	})
	if status, raw := backend.request(t, http.MethodPost, "/v1/orders/"+canceled.ID+"/cancel", token, map[string]any{"source": agentSource("s_1", 2)}); status != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", status, raw)
	}
	if result := backend.engine.Tick(context.Background()); result.Filled != 0 || result.Errors != 0 {
		t.Fatalf("tick after cancel = %+v", result)
	}
	entries := backend.ledger(t, token, "")
	if len(entries) != 2 || entries[0].Event != "order_canceled" || entries[0].OrderID != canceled.ID || sourceRun(entries[0].Source) != 2 || entries[1].Event != "order_placed" {
		t.Fatalf("ledger after cancel-then-tick = %+v", entries)
	}

	// Fill first: the later cancel is refused and leaves no ledger trace.
	filled := placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.01, "price": 50000, "leverage": 10, "source": agentSource("s_1", 3),
	})
	if result := backend.engine.Tick(context.Background()); result.Filled != 1 || result.Errors != 0 {
		t.Fatalf("fill tick = %+v", result)
	}
	if status, raw := backend.request(t, http.MethodPost, "/v1/orders/"+filled.ID+"/cancel", token, map[string]any{"source": agentSource("s_1", 4)}); status != http.StatusConflict || !strings.Contains(string(raw), "filled") {
		t.Fatalf("cancel after fill status=%d body=%s", status, raw)
	}
	entries = backend.ledger(t, token, "?limit=2")
	if len(entries) != 2 || entries[0].Event != "fill" || entries[0].OrderID != filled.ID || sourceRun(entries[0].Source) != 3 || entries[1].Event != "order_placed" || entries[1].OrderID != filled.ID {
		t.Fatalf("ledger after tick-then-cancel = %+v", entries)
	}
	if positions := listPositions(t, backend, token); len(positions.Positions) != 1 || positions.Positions[0].Quantity != 0.01 {
		t.Fatalf("positions = %+v", positions.Positions)
	}
}

func TestTicket23CancelAndFillRaceLeavesOneTerminalStateAndOneLedgerEffect(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-23-race", 1000000)
	backend.setPrice(t, 49000)
	fills, cancels := 0, 0
	for round := 0; round < 20; round++ {
		order := placeOrder(t, backend, token, map[string]any{
			"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.01, "price": 50000, "leverage": 10,
			"source": agentSource("s_1", 1),
		})
		var wait sync.WaitGroup
		wait.Add(2)
		var cancelStatus int
		go func() {
			defer wait.Done()
			backend.engine.Tick(context.Background())
		}()
		go func() {
			defer wait.Done()
			cancelStatus, _ = backend.request(t, http.MethodPost, "/v1/orders/"+order.ID+"/cancel", token, map[string]any{"source": agentSource("s_1", 2)})
		}()
		wait.Wait()

		status, raw := backend.request(t, http.MethodGet, "/v1/orders/"+order.ID, token, nil)
		final := decodeJSON[orderView](t, raw)
		entries := backend.ledger(t, token, "?limit=3")
		events := map[string]int{}
		for _, entry := range entries {
			if entry.OrderID == order.ID {
				events[entry.Event]++
			}
		}
		switch {
		case status == http.StatusOK && final.Status == "filled" && cancelStatus == http.StatusConflict && events["fill"] == 1 && events["order_canceled"] == 0 && events["order_placed"] == 1:
			fills++
		case status == http.StatusOK && final.Status == "canceled" && cancelStatus == http.StatusOK && events["order_canceled"] == 1 && events["fill"] == 0 && events["order_placed"] == 1:
			cancels++
		default:
			t.Fatalf("round %d: order=%+v cancel_status=%d events=%v", round, final, cancelStatus, events)
		}
	}
	positions := listPositions(t, backend, token)
	quantity := 0.0
	if len(positions.Positions) == 1 {
		quantity = positions.Positions[0].Quantity
	}
	if fills+cancels != 20 || quantity != float64(fills)*0.01 {
		t.Fatalf("fills=%d cancels=%d position=%v", fills, cancels, quantity)
	}
}

// A close names a position id: once that position is gone, a position re-opened later on
// the same symbol (new id) must not be closed in its place.
func TestTicket23CloseOfReopenedPositionRequiresTheNewID(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-23-reopen", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "source": agentSource("s_1", 1),
	})
	oldID := listPositions(t, backend, token).Positions[0].ID
	if status, raw := backend.request(t, http.MethodPost, "/v1/positions/"+oldID+"/close", token, map[string]any{"source": agentSource("s_1", 2)}); status != http.StatusOK {
		t.Fatalf("close status=%d body=%s", status, raw)
	}
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.1, "price": 50000, "leverage": 10, "source": agentSource("s_1", 3),
	})
	backend.setPrice(t, 49000)
	if result := backend.engine.Tick(context.Background()); result.Filled != 1 {
		t.Fatalf("reopen tick = %+v", result)
	}
	reopened := listPositions(t, backend, token).Positions[0]
	if reopened.ID == oldID {
		t.Fatalf("re-opened position reused the old id %s", oldID)
	}
	if status, raw := backend.request(t, http.MethodPost, "/v1/positions/"+oldID+"/close", token, map[string]any{"source": agentSource("s_1", 4)}); status != http.StatusNotFound {
		t.Fatalf("close by old id status=%d body=%s", status, raw)
	}
	if positions := listPositions(t, backend, token); len(positions.Positions) != 1 || positions.Positions[0].ID != reopened.ID || positions.Positions[0].Quantity != 0.1 {
		t.Fatalf("re-opened position changed: %+v", positions.Positions)
	}
	if entries := backend.ledger(t, token, "?limit=1"); len(entries) != 1 || entries[0].Event != "fill" || entries[0].OrderID == "" || sourceRun(entries[0].Source) != 3 {
		t.Fatalf("ledger after refused close = %+v", entries)
	}
}

// chunkedRequest sends a JSON body without Content-Length (as a chunked client would).
func chunkedRequest(t *testing.T, backend *tradingAPI, method, path, token string, body map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	request := httptest.NewRequest(method, path, io.NopCloser(bytes.NewReader(raw)))
	if request.ContentLength != -1 {
		t.Fatalf("test request must have unknown length, got %d", request.ContentLength)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	backend.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func TestTicket23CancelAndCloseParseBodiesWithoutContentLength(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-23-chunked", 100000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.3, "leverage": 10, "source": agentSource("s_1", 1),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID
	status, raw := chunkedRequest(t, backend, http.MethodPost, "/v1/positions/"+positionID+"/close", token, map[string]any{"quantity": 0.1, "source": agentSource("s_1", 2)})
	partial := decodeJSON[orderView](t, raw)
	if status != http.StatusOK || partial.FilledQuantity != 0.1 || sourceRun(partial.Source) != 2 {
		t.Fatalf("chunked partial close = %d %s", status, raw)
	}
	if positions := listPositions(t, backend, token); len(positions.Positions) != 1 || positions.Positions[0].Quantity != 0.2 {
		t.Fatalf("chunked close ignored quantity: %+v", positions.Positions)
	}

	limit := placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.1, "price": 50000, "leverage": 10, "source": agentSource("s_1", 3),
	})
	status, raw = chunkedRequest(t, backend, http.MethodPost, "/v1/orders/"+limit.ID+"/cancel", token, map[string]any{"source": agentSource("s_1", 4)})
	if status != http.StatusOK {
		t.Fatalf("chunked cancel = %d %s", status, raw)
	}
	entries := backend.ledger(t, token, "?limit=1")
	if len(entries) != 1 || entries[0].Event != "order_canceled" || sourceRun(entries[0].Source) != 4 {
		t.Fatalf("chunked cancel lost its source: %+v", entries)
	}
	if status, raw := chunkedRequest(t, backend, http.MethodPost, "/v1/positions/"+positionID+"/close", token, map[string]any{"source": map[string]any{"session_id": "s_1"}}); status != http.StatusBadRequest {
		t.Fatalf("chunked invalid source status=%d body=%s", status, raw)
	}
}
