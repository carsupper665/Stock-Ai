package api

import (
	"math"
	"time"

	"backend/internal/database"
	"backend/internal/trading"
)

// round 在輸出邊界收斂浮點尾數，避免 0.1+0.2 那類雜訊外流。
func round(v float64) float64 {
	return math.Round(v*1e8) / 1e8
}

// sourceView 是造成帳戶變動的 Agent Session／Run；沒有來源時輸出 null。
type sourceView struct {
	SessionID string `json:"session_id"`
	RunID     int64  `json:"run_id"`
}

func viewSource(s database.Source) *sourceView {
	if !s.Known() {
		return nil
	}
	return &sourceView{SessionID: s.SessionID, RunID: s.RunID}
}

type orderView struct {
	ID             string      `json:"id"`
	Market         string      `json:"market"`
	Symbol         string      `json:"symbol"`
	Product        string      `json:"product"`
	Side           string      `json:"side"`
	Type           string      `json:"type"`
	Quantity       float64     `json:"quantity"`
	Price          float64     `json:"price,omitempty"`
	Leverage       float64     `json:"leverage"`
	StopLoss       float64     `json:"stop_loss,omitempty"`
	TakeProfit     float64     `json:"take_profit,omitempty"`
	ReduceOnly     bool        `json:"reduce_only,omitempty"`
	Status         string      `json:"status"`
	RejectReason   string      `json:"reject_reason,omitempty"`
	FilledQuantity float64     `json:"filled_quantity"`
	AvgFillPrice   float64     `json:"avg_fill_price,omitempty"`
	Fee            float64     `json:"fee"`
	RealizedPnL    float64     `json:"realized_pnl"`
	Trigger        string      `json:"trigger,omitempty"`
	Source         *sourceView `json:"source"`
	CreatedAt      time.Time   `json:"created_at"`
}

func viewOrder(o *database.Order) orderView {
	return orderView{
		ID: o.ID, Market: o.Market, Symbol: o.Symbol, Product: o.Product,
		Side: o.Side, Type: o.Type,
		Quantity: round(o.Quantity), Price: round(o.Price), Leverage: o.Leverage,
		StopLoss: round(o.StopLoss), TakeProfit: round(o.TakeProfit), ReduceOnly: o.ReduceOnly,
		Status: o.Status, RejectReason: o.RejectReason, FilledQuantity: round(o.FilledQuantity),
		AvgFillPrice: round(o.AvgFillPrice), Fee: round(o.Fee), RealizedPnL: round(o.RealizedPnL),
		Trigger: o.Trigger, Source: viewSource(o.Source),
		CreatedAt: o.CreatedAt.UTC(),
	}
}

type positionView struct {
	ID         string    `json:"id"`
	Market     string    `json:"market"`
	Symbol     string    `json:"symbol"`
	Product    string    `json:"product"`
	Side       string    `json:"side"`
	Quantity   float64   `json:"quantity"`
	EntryPrice float64   `json:"entry_price"`
	MarkPrice  float64   `json:"mark_price"`
	Leverage   float64   `json:"leverage"`
	Margin     float64   `json:"margin"`
	Unrealized float64   `json:"unrealized_pnl"`
	StopLoss   float64   `json:"stop_loss,omitempty"`
	TakeProfit float64   `json:"take_profit,omitempty"`
	OpenedAt   time.Time `json:"opened_at"`

	StopLossSource   *sourceView `json:"stop_loss_source"`
	TakeProfitSource *sourceView `json:"take_profit_source"`
}

func viewPosition(p trading.OpenPosition) positionView {
	return positionView{
		ID: p.ID, Market: p.Market, Symbol: p.Symbol, Product: p.Product, Side: p.Side,
		Quantity: round(p.Quantity), EntryPrice: round(p.EntryPrice), MarkPrice: round(p.MarkPrice),
		Leverage: p.Leverage, Margin: round(p.Margin), Unrealized: round(p.Unrealized),
		StopLoss: round(p.StopLoss), TakeProfit: round(p.TakeProfit), OpenedAt: p.CreatedAt.UTC(),
		StopLossSource: viewSource(p.StopLossSource), TakeProfitSource: viewSource(p.TakeProfitSource),
	}
}

type tradeView struct {
	ID          string      `json:"id"`
	OrderID     string      `json:"order_id"`
	Market      string      `json:"market"`
	Symbol      string      `json:"symbol"`
	Product     string      `json:"product"`
	Side        string      `json:"side"`
	Role        string      `json:"role"`
	Quantity    float64     `json:"quantity"`
	Price       float64     `json:"price"`
	Fee         float64     `json:"fee"`
	RealizedPnL float64     `json:"realized_pnl"`
	Source      *sourceView `json:"source"`
	CreatedAt   time.Time   `json:"created_at"`
}

func viewTrade(t *database.Trade) tradeView {
	return tradeView{
		ID: t.ID, OrderID: t.OrderID, Market: t.Market, Symbol: t.Symbol, Product: t.Product,
		Side: t.Side, Role: t.Role, Quantity: round(t.Quantity), Price: round(t.Price),
		Fee: round(t.Fee), RealizedPnL: round(t.RealizedPnL), Source: viewSource(t.Source),
		CreatedAt: t.CreatedAt.UTC(),
	}
}

type ledgerEntryView struct {
	Seq          int64       `json:"seq"`
	Event        string      `json:"event"`
	OrderID      string      `json:"order_id,omitempty"`
	TradeID      string      `json:"trade_id,omitempty"`
	PositionID   string      `json:"position_id,omitempty"`
	Trigger      string      `json:"trigger,omitempty"`
	Quantity     float64     `json:"quantity"`
	Price        float64     `json:"price"`
	Fee          float64     `json:"fee"`
	RealizedPnL  float64     `json:"realized_pnl"`
	BalanceDelta float64     `json:"balance_delta"`
	BalanceAfter float64     `json:"balance_after"`
	StopLoss     float64     `json:"stop_loss"`
	TakeProfit   float64     `json:"take_profit"`
	Source       *sourceView `json:"source"`
	CreatedAt    time.Time   `json:"created_at"`
}

func viewLedgerEntry(e *database.LedgerEntry) ledgerEntryView {
	return ledgerEntryView{
		Seq: e.Seq, Event: e.Event, OrderID: e.OrderID, TradeID: e.TradeID, PositionID: e.PositionID, Trigger: e.Trigger,
		Quantity: round(e.Quantity), Price: round(e.Price), Fee: round(e.Fee), RealizedPnL: round(e.RealizedPnL),
		BalanceDelta: round(e.BalanceDelta), BalanceAfter: round(e.BalanceAfter),
		StopLoss: round(e.StopLoss), TakeProfit: round(e.TakeProfit), Source: viewSource(e.Source),
		CreatedAt: e.CreatedAt.UTC(),
	}
}
