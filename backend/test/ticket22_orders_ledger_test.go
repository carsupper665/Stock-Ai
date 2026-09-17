package test

import (
	"net/http"
	"testing"
)

type orderView struct {
	ID             string      `json:"id"`
	Status         string      `json:"status"`
	Type           string      `json:"type"`
	FilledQuantity float64     `json:"filled_quantity"`
	AvgFillPrice   float64     `json:"avg_fill_price"`
	Fee            float64     `json:"fee"`
	RealizedPnL    float64     `json:"realized_pnl"`
	Trigger        string      `json:"trigger"`
	Source         *sourceView `json:"source"`
}

type tradeView struct {
	ID      string      `json:"id"`
	OrderID string      `json:"order_id"`
	Price   float64     `json:"price"`
	Fee     float64     `json:"fee"`
	Source  *sourceView `json:"source"`
}

func TestTicket22MarketOrderRecordsSourceOnOrderTradeAndLedgerFill(t *testing.T) {
	backend := newTradingAPI(t, "")
	accountID, token := backend.createAccount(t, "ticket-22-market", 100000)

	status, raw := backend.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10,
		"account_id": "acc_forged", "source": agentSource("s_agent", 3),
	})
	if status != http.StatusCreated {
		t.Fatalf("place order status=%d body=%s", status, raw)
	}
	order := decodeJSON[orderView](t, raw)
	if order.Status != "filled" || order.Fee != 2.4 || order.Trigger != "" || order.Source == nil ||
		order.Source.SessionID != "s_agent" || order.Source.RunID != 3 {
		t.Fatalf("filled order = %s", raw)
	}

	status, raw = backend.request(t, http.MethodGet, "/v1/trades", token, nil)
	trades := decodeJSON[struct {
		Trades []tradeView `json:"trades"`
	}](t, raw)
	if status != http.StatusOK || len(trades.Trades) != 1 || trades.Trades[0].OrderID != order.ID ||
		trades.Trades[0].Source == nil || trades.Trades[0].Source.SessionID != "s_agent" || trades.Trades[0].Source.RunID != 3 {
		t.Fatalf("trades = %s", raw)
	}

	status, raw = backend.request(t, http.MethodGet, "/v1/positions", token, nil)
	positions := decodeJSON[struct {
		Positions []struct {
			ID string `json:"id"`
		} `json:"positions"`
	}](t, raw)
	if status != http.StatusOK || len(positions.Positions) != 1 {
		t.Fatalf("positions = %s", raw)
	}

	entries := backend.ledger(t, token, "")
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %+v", entries)
	}
	fill := entries[0]
	if fill.Seq != 1 || fill.Event != "fill" || fill.OrderID != order.ID || fill.TradeID != trades.Trades[0].ID ||
		fill.PositionID != positions.Positions[0].ID || fill.Trigger != "" || fill.Quantity != 0.1 || fill.Price != 60000 ||
		fill.Fee != 2.4 || fill.RealizedPnL != 0 || fill.BalanceDelta != -2.4 || fill.BalanceAfter != 99997.6 ||
		fill.Source == nil || fill.Source.SessionID != "s_agent" || fill.Source.RunID != 3 || fill.CreatedAt.IsZero() {
		t.Fatalf("ledger fill = %+v", fill)
	}

	status, raw = backend.request(t, http.MethodGet, "/v1/accounts/"+accountID+"/ledger", tradingUserToken, nil)
	userView := decodeJSON[struct {
		Entries []ledgerEntryView `json:"entries"`
	}](t, raw)
	if status != http.StatusOK || len(userView.Entries) != 1 || userView.Entries[0].Seq != fill.Seq ||
		userView.Entries[0].TradeID != fill.TradeID || *userView.Entries[0].Source != *fill.Source {
		t.Fatalf("USER ledger status=%d body=%s", status, raw)
	}
	if status, raw = backend.request(t, http.MethodGet, "/v1/accounts/"+accountID+"/ledger", token, nil); status != http.StatusForbidden {
		t.Fatalf("Account Token on USER ledger route status=%d body=%s", status, raw)
	}
	if status, raw = backend.request(t, http.MethodGet, "/v1/ledger", tradingUserToken, nil); status != http.StatusForbidden {
		t.Fatalf("USER Token on Account ledger route status=%d body=%s", status, raw)
	}
}
