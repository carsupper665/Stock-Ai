package test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestTicket22LimitOrderKeepsCreatingRunWhenFilledOrRejectedLater(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-22-limit", 100000)
	status, raw := backend.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.5, "price": 50000, "leverage": 10,
		"source": agentSource("s_agent", 7),
	})
	if status != http.StatusCreated {
		t.Fatalf("place limit status=%d body=%s", status, raw)
	}
	order := decodeJSON[orderView](t, raw)
	entries := backend.ledger(t, token, "")
	if order.Status != "open" || len(entries) != 1 || entries[0].Event != "order_placed" || entries[0].OrderID != order.ID ||
		entries[0].Quantity != 0.5 || entries[0].Price != 50000 || entries[0].BalanceDelta != 0 || entries[0].Source == nil || entries[0].Source.RunID != 7 {
		t.Fatalf("open limit order=%+v ledger=%+v", order, entries)
	}

	backend.setPrice(t, 49000)
	if result := backend.engine.Tick(context.Background()); result.Filled != 1 || result.Errors != 0 {
		t.Fatalf("matching tick = %+v", result)
	}
	status, raw = backend.request(t, http.MethodGet, "/v1/orders/"+order.ID, token, nil)
	filled := decodeJSON[orderView](t, raw)
	if status != http.StatusOK || filled.Status != "filled" || filled.AvgFillPrice != 50000 || filled.Fee != 5 ||
		filled.Source == nil || filled.Source.SessionID != "s_agent" || filled.Source.RunID != 7 {
		t.Fatalf("filled limit order = %s", raw)
	}
	entries = backend.ledger(t, token, "")
	if len(entries) != 2 || entries[0].Event != "fill" || entries[0].OrderID != order.ID || entries[0].Price != 50000 ||
		entries[0].Fee != 5 || entries[0].BalanceDelta != -5 || entries[0].BalanceAfter != 99995 ||
		entries[0].Source == nil || entries[0].Source.RunID != 7 || entries[1].Event != "order_placed" {
		t.Fatalf("ledger after delayed fill = %+v", entries)
	}

	// A second limit order that can no longer be afforded is rejected once by the engine and traced to its Run.
	status, raw = backend.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 100, "price": 48000, "leverage": 10,
		"source": agentSource("s_agent", 8),
	})
	if status != http.StatusCreated {
		t.Fatalf("place unaffordable limit status=%d body=%s", status, raw)
	}
	rejected := decodeJSON[orderView](t, raw)
	backend.setPrice(t, 47000)
	if result := backend.engine.Tick(context.Background()); result.Rejected != 1 || result.Errors != 0 {
		t.Fatalf("rejecting tick = %+v", result)
	}
	entries = backend.ledger(t, token, "?run_id=8")
	if len(entries) != 2 || entries[0].Event != "order_rejected" || entries[0].OrderID != rejected.ID || entries[0].BalanceDelta != 0 ||
		entries[0].Source == nil || entries[0].Source.RunID != 8 || entries[1].Event != "order_placed" {
		t.Fatalf("ledger for run 8 = %+v", entries)
	}
}

func TestTicket22SourceValidationLegacyNullAndForgedAccountIgnored(t *testing.T) {
	backend := newTradingAPI(t, "")
	accountID, token := backend.createAccount(t, "ticket-22-source", 100000)
	otherID, _ := backend.createAccount(t, "ticket-22-other", 100000)
	base := map[string]any{"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10}
	for name, source := range map[string]any{
		"missing run_id":    map[string]any{"session_id": "s_1"},
		"zero run_id":       map[string]any{"session_id": "s_1", "run_id": 0},
		"negative run_id":   map[string]any{"session_id": "s_1", "run_id": -2},
		"blank session":     map[string]any{"session_id": "   ", "run_id": 1},
		"string run_id":     map[string]any{"session_id": "s_1", "run_id": "1"},
		"session too long":  map[string]any{"session_id": strings.Repeat("s", 65), "run_id": 1},
		"source not object": "s_1/1",
	} {
		body := map[string]any{"source": source}
		for key, value := range base {
			body[key] = value
		}
		status, raw := backend.request(t, http.MethodPost, "/v1/orders", token, body)
		if status != http.StatusBadRequest || !strings.Contains(string(raw), `"error":"invalid_request"`) {
			t.Fatalf("%s: status=%d body=%s", name, status, raw)
		}
	}
	if entries := backend.ledger(t, token, ""); len(entries) != 0 {
		t.Fatalf("rejected sources wrote ledger entries: %+v", entries)
	}
	status, raw := backend.request(t, http.MethodGet, "/v1/orders", token, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"orders":[]`) {
		t.Fatalf("rejected sources persisted orders: %s", raw)
	}

	body := map[string]any{"account_id": otherID}
	for key, value := range base {
		body[key] = value
	}
	status, raw = backend.request(t, http.MethodPost, "/v1/orders", token, body)
	legacy := decodeJSON[orderView](t, raw)
	if status != http.StatusCreated || legacy.Source != nil || legacy.Status != "filled" {
		t.Fatalf("order without source = %d %s", status, raw)
	}
	entries := backend.ledger(t, token, "")
	if len(entries) != 1 || entries[0].Source != nil || entries[0].Event != "fill" {
		t.Fatalf("ledger without source = %+v", entries)
	}
	if status, raw = backend.request(t, http.MethodGet, "/v1/accounts/"+otherID+"/ledger", tradingUserToken, nil); status != http.StatusOK || !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("forged account_id changed another Account: %d %s", status, raw)
	}
	if status, raw = backend.request(t, http.MethodGet, "/v1/accounts/"+accountID+"/trades", tradingUserToken, nil); status != http.StatusOK || !strings.Contains(string(raw), `"source":null`) {
		t.Fatalf("legacy trade source = %d %s", status, raw)
	}
}

// A limit order on an unknown market would never be priced and would rest forever; it is
// refused at placement like every other invalid order.
func TestTicket22LimitOrderOnUnknownMarketIsRejectedBeforeResting(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-22-market", 100000)
	for _, orderType := range []string{"limit", "market"} {
		status, raw := backend.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
			"market": "forex", "symbol": "EURUSD", "product": "futures", "side": "buy", "type": orderType, "quantity": 1, "price": 1.1, "leverage": 1,
			"source": agentSource("s_agent", 1),
		})
		if status != http.StatusBadRequest || !strings.Contains(string(raw), `"error":"invalid_request"`) {
			t.Fatalf("%s order on unknown market: status=%d body=%s", orderType, status, raw)
		}
	}
	if status, raw := backend.request(t, http.MethodGet, "/v1/orders", token, nil); status != http.StatusOK || !strings.Contains(string(raw), `"orders":[]`) {
		t.Fatalf("unknown-market order was persisted: %d %s", status, raw)
	}
	if entries := backend.ledger(t, token, ""); len(entries) != 0 {
		t.Fatalf("unknown-market order wrote ledger entries: %+v", entries)
	}
	if result := backend.engine.Tick(context.Background()); result.Filled != 0 || result.Rejected != 0 || result.Errors != 0 {
		t.Fatalf("tick found resting orders: %+v", result)
	}
}

func TestTicket22LedgerPaginationAndFilters(t *testing.T) {
	backend := newTradingAPI(t, "")
	_, token := backend.createAccount(t, "ticket-22-pages", 100000)
	for run := int64(1); run <= 5; run++ {
		status, raw := backend.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
			"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.01, "leverage": 10,
			"source": agentSource("s_"+strconv.FormatInt(run%2, 10), run),
		})
		if status != http.StatusCreated {
			t.Fatalf("order %d status=%d body=%s", run, status, raw)
		}
	}
	all := backend.ledger(t, token, "")
	if len(all) != 5 || all[0].Seq != 5 || all[4].Seq != 1 {
		t.Fatalf("ledger order = %+v", all)
	}
	first := backend.ledger(t, token, "?limit=2")
	second := backend.ledger(t, token, "?limit=2&before_seq="+strconv.FormatInt(first[len(first)-1].Seq, 10))
	third := backend.ledger(t, token, "?limit=2&before_seq="+strconv.FormatInt(second[len(second)-1].Seq, 10))
	if len(first) != 2 || first[0].Seq != 5 || first[1].Seq != 4 || len(second) != 2 || second[0].Seq != 3 || second[1].Seq != 2 ||
		len(third) != 1 || third[0].Seq != 1 {
		t.Fatalf("pages = %+v %+v %+v", first, second, third)
	}
	if beyond := backend.ledger(t, token, "?before_seq=1"); len(beyond) != 0 {
		t.Fatalf("page beyond first entry = %+v", beyond)
	}
	bySession := backend.ledger(t, token, "?session_id=s_1")
	if len(bySession) != 3 || bySession[0].Source.RunID != 5 || bySession[2].Source.RunID != 1 {
		t.Fatalf("session filter = %+v", bySession)
	}
	byRun := backend.ledger(t, token, "?session_id=s_0&run_id=4")
	if len(byRun) != 1 || byRun[0].Source.RunID != 4 || byRun[0].Source.SessionID != "s_0" {
		t.Fatalf("run filter = %+v", byRun)
	}
	if mismatch := backend.ledger(t, token, "?session_id=s_1&run_id=4"); len(mismatch) != 0 {
		t.Fatalf("mismatched session/run filter = %+v", mismatch)
	}
	for _, query := range []string{"?run_id=0", "?run_id=x", "?before_seq=-1"} {
		if status, raw := backend.request(t, http.MethodGet, "/v1/ledger"+query, token, nil); status != http.StatusBadRequest {
			t.Fatalf("query %s status=%d body=%s", query, status, raw)
		}
	}
}
