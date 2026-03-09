package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"server/domain"
	"server/model/repo"
	"server/model/store"

	"gorm.io/gorm"
)

type TradingService struct {
	repo       *repo.Repository
	clock      domain.Clock
	market     domain.MarketDataProvider
	events     domain.EventPublisher
	risk       domain.RiskEngine
	fillPolicy domain.FillPolicy
	ledger     domain.AccountLedger
}

type PlaceOrderInput struct {
	Symbol       string  `json:"symbol"`
	Side         string  `json:"side"`
	PositionSide string  `json:"position_side"`
	OrderType    string  `json:"order_type"`
	Quantity     float64 `json:"qty"`
	Price        float64 `json:"price,omitempty"`
	StopPrice    float64 `json:"stop_price,omitempty"`
	Leverage     float64 `json:"leverage,omitempty"`
}

type AccountPerformance struct {
	AccountID      string  `json:"account_id"`
	Equity         float64 `json:"equity"`
	RealizedPnL    float64 `json:"realized_pnl"`
	UnrealizedPnL  float64 `json:"unrealized_pnl"`
	ReturnPct      float64 `json:"return_pct"`
	WalletBalance  float64 `json:"wallet_balance"`
	AvailableFunds float64 `json:"available_balance"`
}

func NewTradingService(repo *repo.Repository, clock domain.Clock, market domain.MarketDataProvider, events domain.EventPublisher, risk domain.RiskEngine, fillPolicy domain.FillPolicy, ledger domain.AccountLedger) *TradingService {
	return &TradingService{repo: repo, clock: clock, market: market, events: events, risk: risk, fillPolicy: fillPolicy, ledger: ledger}
}

func (s *TradingService) PlaceOrder(ctx context.Context, accountID string, input PlaceOrderInput) (*store.Order, error) {
	account, err := s.repo.FindAccount(ctx, accountID)
	if err != nil {
		return nil, domain.NotFoundError("ACCOUNT_NOT_FOUND", "account not found")
	}
	if account.Type == store.AccountTypeLive {
		return nil, domain.ConflictError("LIVE_ACCOUNT_TRADING_UNSUPPORTED", "live account trading is not enabled")
	}
	if account.SandboxID == nil || *account.SandboxID == "" {
		return nil, domain.ValidationError("ACCOUNT_SANDBOX_REQUIRED", "account is not bound to a sandbox")
	}
	sandbox, err := s.repo.FindSandbox(ctx, *account.SandboxID)
	if err != nil {
		return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
	}
	marketPrice, _, err := s.market.GetLastPrice(ctx, sandbox.ID, input.Symbol)
	tradable := err == nil
	if !tradable {
		marketPrice = input.Price
	}
	position, err := s.repo.FindPosition(ctx, accountID, sandbox.ID, input.Symbol, input.PositionSide)
	if err != nil {
		return nil, err
	}
	currentQty := 0.0
	if position != nil {
		currentQty = position.Quantity
	}
	leverage := input.Leverage
	if leverage <= 0 {
		leverage = 1
	}
	if err := s.risk.ValidateNewOrder(ctx, domain.RiskOrderRequest{
		WalletBalance:    account.WalletBalance,
		AvailableBalance: account.AvailableBalance,
		CurrentQuantity:  currentQty,
		MarketPrice:      marketPrice,
		OrderType:        input.OrderType,
		Side:             input.Side,
		PositionSide:     input.PositionSide,
		Quantity:         input.Quantity,
		LimitPrice:       input.Price,
		StopPrice:        input.StopPrice,
		Leverage:         leverage,
		SandboxStatus:    sandbox.Status,
		Tradable:         tradable,
	}); err != nil {
		return nil, err
	}

	order := &store.Order{
		ID:           newID("ord"),
		SandboxID:    sandbox.ID,
		AccountID:    accountID,
		Symbol:       input.Symbol,
		Side:         input.Side,
		PositionSide: input.PositionSide,
		OrderType:    input.OrderType,
		Quantity:     input.Quantity,
		Price:        input.Price,
		StopPrice:    input.StopPrice,
		Leverage:     leverage,
		Status:       store.OrderStatusNew,
	}
	if err := s.repo.Create(ctx, order); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "order.created", SandboxID: sandbox.ID, AccountID: accountID, AggregateID: order.ID, Payload: map[string]any{"symbol": order.Symbol, "status": order.Status}})

	switch order.OrderType {
	case store.OrderTypeMarket:
		if err := s.fillOrder(ctx, order.ID, marketPrice, false); err != nil {
			return nil, err
		}
	case store.OrderTypeLimit:
		if s.fillPolicy.LimitShouldFill(order.Side, marketPrice, order.Price) {
			if err := s.fillOrder(ctx, order.ID, marketPrice, false); err != nil {
				return nil, err
			}
		}
	case store.OrderTypeStop:
		if s.fillPolicy.StopShouldTrigger(order.PositionSide, marketPrice, order.StopPrice) {
			if err := s.fillOrder(ctx, order.ID, marketPrice, true); err != nil {
				return nil, err
			}
		}
	default:
		return nil, domain.ValidationError("INVALID_ORDER_TYPE", "unsupported order type")
	}
	return s.GetOrder(ctx, order.ID)
}

func (s *TradingService) fillOrder(ctx context.Context, orderID string, fillPrice float64, triggered bool) error {
	var accountID string
	var sandboxID string
	err := s.repo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order store.Order
		if err := tx.First(&order, "id = ?", orderID).Error; err != nil {
			return err
		}
		if order.Status == store.OrderStatusFilled || order.Status == store.OrderStatusCanceled {
			return nil
		}
		var account store.Account
		if err := tx.First(&account, "id = ?", order.AccountID).Error; err != nil {
			return err
		}
		accountID = account.ID
		sandboxID = order.SandboxID

		var position store.Position
		positionErr := tx.Where("account_id = ? AND sandbox_id = ? AND symbol = ? AND position_side = ?", order.AccountID, order.SandboxID, order.Symbol, order.PositionSide).First(&position).Error
		if errors.Is(positionErr, gorm.ErrRecordNotFound) {
			positionErr = nil
		}
		if positionErr != nil {
			return positionErr
		}

		if triggered {
			now := s.clock.Now()
			order.Status = store.OrderStatusTriggered
			order.TriggeredAt = &now
			if err := tx.Save(&order).Error; err != nil {
				return err
			}
		}

		opening := isOpeningAction(order.Side, order.PositionSide)
		quantity := order.Quantity
		if opening {
			requiredMargin := quantity * fillPrice / order.Leverage
			if err := s.ledger.ReserveMargin(&account.WalletBalance, &account.LockedMargin, requiredMargin); err != nil {
				order.Status = store.OrderStatusRejected
				order.Rejection = err.Error()
				_ = tx.Save(&order).Error
				return err
			}
			if position.ID == "" {
				position = store.Position{
					ID:            newID("pos"),
					AccountID:     order.AccountID,
					SandboxID:     order.SandboxID,
					Symbol:        order.Symbol,
					PositionSide:  order.PositionSide,
					Quantity:      quantity,
					EntryPrice:    fillPrice,
					MarkPrice:     fillPrice,
					Leverage:      order.Leverage,
					MarginUsed:    requiredMargin,
					UnrealizedPnL: 0,
					UpdatedAt:     s.clock.Now(),
				}
				if err := tx.Create(&position).Error; err != nil {
					return err
				}
			} else {
				totalQty := position.Quantity + quantity
				if totalQty <= 0 {
					return domain.ValidationError("INVALID_POSITION_QTY", "position quantity must stay positive")
				}
				position.EntryPrice = ((position.EntryPrice * position.Quantity) + (fillPrice * quantity)) / totalQty
				position.Quantity = totalQty
				position.MarkPrice = fillPrice
				position.Leverage = order.Leverage
				position.MarginUsed += requiredMargin
				position.UpdatedAt = s.clock.Now()
				if err := tx.Save(&position).Error; err != nil {
					return err
				}
			}
		} else {
			if position.ID == "" || position.Quantity < quantity {
				return domain.ValidationError("POSITION_QTY_EXCEEDED", "order quantity exceeds current position size")
			}
			release := position.MarginUsed * (quantity / position.Quantity)
			pnl := realizedPnL(position.PositionSide, position.EntryPrice, fillPrice, quantity)
			s.ledger.ReleaseMargin(&account.WalletBalance, &account.LockedMargin, release)
			s.ledger.ApplyRealizedPnL(&account.WalletBalance, &account.RealizedPnL, pnl)
			position.Quantity -= quantity
			position.MarginUsed -= release
			position.MarkPrice = fillPrice
			position.UpdatedAt = s.clock.Now()
			if position.Quantity <= 0 {
				if err := tx.Delete(&position).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Save(&position).Error; err != nil {
					return err
				}
			}
		}

		trade := store.Trade{
			ID:           newID("trd"),
			OrderID:      order.ID,
			AccountID:    order.AccountID,
			SandboxID:    order.SandboxID,
			Symbol:       order.Symbol,
			Quantity:     quantity,
			Price:        fillPrice,
			Fee:          0,
			Side:         order.Side,
			PositionSide: order.PositionSide,
			ExecutedAt:   s.clock.Now(),
		}
		if err := tx.Create(&trade).Error; err != nil {
			return err
		}

		order.Status = store.OrderStatusFilled
		order.FilledQty = quantity
		order.AvgFillPrice = fillPrice
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if accountID != "" {
		if err := s.RefreshAccount(ctx, accountID); err != nil {
			return err
		}
	}
	if triggered {
		_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "stoploss.triggered", SandboxID: sandboxID, AccountID: accountID, AggregateID: orderID, Payload: map[string]any{"order_id": orderID}})
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "order.updated", SandboxID: sandboxID, AccountID: accountID, AggregateID: orderID, Payload: map[string]any{"status": store.OrderStatusFilled}})
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "trade.executed", SandboxID: sandboxID, AccountID: accountID, AggregateID: orderID, Payload: map[string]any{"order_id": orderID}})
	return nil
}

func (s *TradingService) RefreshAccount(ctx context.Context, accountID string) error {
	account, err := s.repo.FindAccount(ctx, accountID)
	if err != nil {
		return err
	}
	positions, err := s.repo.ListPositionsByAccount(ctx, accountID)
	if err != nil {
		return err
	}
	totalUnrealized := 0.0
	for i := range positions {
		price, _, err := s.market.GetLastPrice(ctx, positions[i].SandboxID, positions[i].Symbol)
		if err != nil {
			price = positions[i].MarkPrice
		}
		positions[i].MarkPrice = price
		positions[i].UnrealizedPnL = realizedPnL(positions[i].PositionSide, positions[i].EntryPrice, price, positions[i].Quantity)
		totalUnrealized += positions[i].UnrealizedPnL
		if err := s.repo.Save(ctx, &positions[i]); err != nil {
			return err
		}
	}
	s.ledger.Reprice(&account.UnrealizedPnL, &account.Equity, &account.AvailableBalance, account.WalletBalance, account.LockedMargin, totalUnrealized)
	if err := s.repo.Save(ctx, account); err != nil {
		return err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "account.balance.updated", AccountID: accountID, AggregateID: accountID, Payload: map[string]any{"available_balance": account.AvailableBalance}})
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "account.equity.updated", AccountID: accountID, AggregateID: accountID, Payload: map[string]any{"equity": account.Equity}})
	return nil
}

func (s *TradingService) ProcessSandbox(ctx context.Context, sandboxID string) error {
	orders, err := s.repo.ListPendingOrdersBySandbox(ctx, sandboxID)
	if err != nil {
		return err
	}
	sort.SliceStable(orders, func(i, j int) bool {
		return orders[i].CreatedAt.Before(orders[j].CreatedAt)
	})
	for _, order := range orders {
		marketPrice, _, err := s.market.GetLastPrice(ctx, sandboxID, order.Symbol)
		if err != nil {
			continue
		}
		switch order.OrderType {
		case store.OrderTypeLimit:
			if order.Status == store.OrderStatusNew && s.fillPolicy.LimitShouldFill(order.Side, marketPrice, order.Price) {
				if err := s.fillOrder(ctx, order.ID, marketPrice, false); err != nil {
					return err
				}
			}
		case store.OrderTypeStop:
			if order.Status == store.OrderStatusNew && s.fillPolicy.StopShouldTrigger(order.PositionSide, marketPrice, order.StopPrice) {
				if err := s.fillOrder(ctx, order.ID, marketPrice, true); err != nil {
					return err
				}
			}
		}
	}
	accountRows, err := s.repo.ListAccountsBySandbox(ctx, sandboxID)
	if err != nil {
		return err
	}
	for _, account := range accountRows {
		if err := s.RefreshAccount(ctx, account.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *TradingService) CancelOrder(ctx context.Context, accountID, orderID string) (*store.Order, error) {
	order, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.AccountID != accountID {
		return nil, domain.ForbiddenError("order does not belong to account")
	}
	if order.Status != store.OrderStatusNew && order.Status != store.OrderStatusTriggered {
		return nil, domain.ConflictError("ORDER_NOT_CANCELABLE", "order cannot be canceled")
	}
	order.Status = store.OrderStatusCanceled
	if err := s.repo.Save(ctx, order); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "order.updated", SandboxID: order.SandboxID, AccountID: order.AccountID, AggregateID: order.ID, Payload: map[string]any{"status": order.Status}})
	return order, nil
}

func (s *TradingService) GetOrder(ctx context.Context, orderID string) (*store.Order, error) {
	var order store.Order
	if err := s.repo.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return nil, domain.NotFoundError("ORDER_NOT_FOUND", "order not found")
	}
	return &order, nil
}

func (s *TradingService) ListOrders(ctx context.Context, accountID string) ([]store.Order, error) {
	return s.repo.ListOrdersByAccount(ctx, accountID)
}

func (s *TradingService) ListTrades(ctx context.Context, accountID string) ([]store.Trade, error) {
	return s.repo.ListTradesByAccount(ctx, accountID)
}

func (s *TradingService) ListPositions(ctx context.Context, accountID string) ([]store.Position, error) {
	positions, err := s.repo.ListPositionsByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	filtered := positions[:0]
	for _, position := range positions {
		if position.Quantity > 0 {
			filtered = append(filtered, position)
		}
	}
	return filtered, nil
}

func (s *TradingService) GetAccountSummary(ctx context.Context, accountID string) (*store.Account, error) {
	if err := s.RefreshAccount(ctx, accountID); err != nil {
		return nil, err
	}
	return s.repo.FindAccount(ctx, accountID)
}

func (s *TradingService) GetPerformance(ctx context.Context, accountID string) (*AccountPerformance, error) {
	account, err := s.GetAccountSummary(ctx, accountID)
	if err != nil {
		return nil, err
	}
	perf := &AccountPerformance{
		AccountID:      account.ID,
		Equity:         account.Equity,
		RealizedPnL:    account.RealizedPnL,
		UnrealizedPnL:  account.UnrealizedPnL,
		WalletBalance:  account.WalletBalance,
		AvailableFunds: account.AvailableBalance,
	}
	if account.InitialBalance > 0 {
		perf.ReturnPct = (account.Equity - account.InitialBalance) / account.InitialBalance * 100
	}
	return perf, nil
}

func isOpeningAction(side, positionSide string) bool {
	return (positionSide == store.PositionSideLong && side == store.OrderSideBuy) ||
		(positionSide == store.PositionSideShort && side == store.OrderSideSell)
}

func realizedPnL(positionSide string, entryPrice, exitPrice, quantity float64) float64 {
	switch positionSide {
	case store.PositionSideLong:
		return (exitPrice - entryPrice) * quantity
	case store.PositionSideShort:
		return (entryPrice - exitPrice) * quantity
	default:
		return 0
	}
}

func mustTimePtr(t time.Time) *time.Time {
	value := t.UTC()
	return &value
}
