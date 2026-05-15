package service

import (
	"context"
	"sync"

	"server/domain"
	"server/model/store"
)

type ExchangeOrderRequest struct {
	AccountID     string
	OrderID       string
	Symbol        string
	Side          string
	PositionSide  string
	OrderType     string
	Quantity      float64
	Price         float64
	StopPrice     float64
	Leverage      float64
	ReduceOnly    bool
	ClientOrderID string
	Mode          TradingMode
}

type ExchangeOrderResult struct {
	ExchangeOrderID string
	Status          string
	FilledQty       float64
	AvgFillPrice    float64
	RejectionCode   string
}

type ExchangeAdapter interface {
	PlaceOrder(ctx context.Context, req ExchangeOrderRequest) (*ExchangeOrderResult, error)
	GetOrder(ctx context.Context, account store.Account, order store.Order) (*ExchangeOrderResult, error)
}

type FakeExchangeAdapter struct {
	mu      sync.Mutex
	results map[string]ExchangeOrderResult
	err     error
}

func NewFakeExchangeAdapter() *FakeExchangeAdapter {
	return &FakeExchangeAdapter{results: map[string]ExchangeOrderResult{}}
}

func (a *FakeExchangeAdapter) SetError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *FakeExchangeAdapter) SetOrderResult(clientOrderID string, result ExchangeOrderResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.results == nil {
		a.results = map[string]ExchangeOrderResult{}
	}
	a.results[clientOrderID] = result
}

func (a *FakeExchangeAdapter) PlaceOrder(ctx context.Context, req ExchangeOrderRequest) (*ExchangeOrderResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	if result, ok := a.results[req.ClientOrderID]; ok {
		return cloneExchangeResult(result), nil
	}
	result := ExchangeOrderResult{
		ExchangeOrderID: "ex_" + req.OrderID,
		Status:          store.OrderStatusNew,
	}
	if req.OrderType == store.OrderTypeMarket {
		result.Status = store.OrderStatusFilled
		result.FilledQty = req.Quantity
		result.AvgFillPrice = req.Price
		if result.AvgFillPrice == 0 {
			result.AvgFillPrice = req.StopPrice
		}
	}
	a.results[req.ClientOrderID] = result
	return cloneExchangeResult(result), nil
}

func (a *FakeExchangeAdapter) GetOrder(ctx context.Context, account store.Account, order store.Order) (*ExchangeOrderResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	result, ok := a.results[order.ClientOrderID]
	if !ok {
		if order.ExchangeOrderID == "" {
			return nil, domain.NotFoundError("EXCHANGE_ORDER_NOT_FOUND", "exchange order not found")
		}
		result = ExchangeOrderResult{ExchangeOrderID: order.ExchangeOrderID, Status: order.ExchangeStatus, FilledQty: order.FilledQty, AvgFillPrice: order.AvgFillPrice}
	}
	return cloneExchangeResult(result), nil
}

func cloneExchangeResult(result ExchangeOrderResult) *ExchangeOrderResult {
	copy := result
	return &copy
}
