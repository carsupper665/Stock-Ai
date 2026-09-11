package trading

import (
	"context"
	"errors"
	"fmt"

	"backend/internal/database"
	"backend/internal/id"
	"backend/internal/market"
)

type Service struct {
	store  *database.Store
	prices *market.Runtime
	maker  float64
	taker  float64
}

func New(store *database.Store, prices *market.Runtime, makerFee, takerFee float64) *Service {
	return &Service{store: store, prices: prices, maker: makerFee, taker: takerFee}
}

// PlaceOrder 下單。市價單立刻以現價成交；限價單掛著等撮合引擎觸發。
func (s *Service) PlaceOrder(ctx context.Context, accountID string, in PlaceOrderInput) (*database.Order, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}

	orderID, err := id.New("ord_")
	if err != nil {
		return nil, err
	}
	order := &database.Order{
		ID:         orderID,
		AccountID:  accountID,
		Market:     in.Market,
		Symbol:     in.Symbol,
		Product:    in.Product,
		Side:       in.Side,
		Type:       in.Type,
		Quantity:   in.Quantity,
		Price:      in.Price,
		Leverage:   in.Leverage,
		StopLoss:   in.StopLoss,
		TakeProfit: in.TakeProfit,
		Status:     database.OrderOpen,
	}

	if in.Type == database.OrderLimit {
		if err := checkStopLevels(in.Side, in.Price, in.StopLoss, in.TakeProfit); err != nil {
			return nil, err
		}
		if err := s.store.CreateOrder(ctx, order); err != nil {
			return nil, err
		}
		return order, nil
	}

	price, err := s.prices.GetPrice(ctx, in.Market, in.Symbol)
	if err != nil {
		return nil, err
	}
	if err := checkStopLevels(in.Side, price.Price, in.StopLoss, in.TakeProfit); err != nil {
		return nil, err
	}
	return s.execute(ctx, order, price.Price, database.RoleTaker)
}

// ClosePosition 以現價平倉。quantity 省略或為 0 時全平。
func (s *Service) ClosePosition(ctx context.Context, accountID, positionID string, quantity float64) (*database.Order, error) {
	pos, err := s.store.PositionByID(ctx, accountID, positionID)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, ErrPositionClosed
		}
		return nil, err
	}
	if quantity <= 0 {
		quantity = pos.Quantity
	}
	if quantity > pos.Quantity+qtyEpsilon {
		return nil, ErrCloseTooMuch
	}

	price, err := s.prices.GetPrice(ctx, pos.Market, pos.Symbol)
	if err != nil {
		return nil, err
	}
	return s.closeAt(ctx, pos, quantity, price.Price)
}

// closeAt 以指定價格平倉，供手動平倉與停損停利觸發共用。
func (s *Service) closeAt(ctx context.Context, pos *database.Position, quantity, price float64) (*database.Order, error) {
	orderID, err := id.New("ord_")
	if err != nil {
		return nil, err
	}
	side := database.SideSell
	if pos.Side == database.Short {
		side = database.SideBuy
	}
	order := &database.Order{
		ID:         orderID,
		AccountID:  pos.AccountID,
		Market:     pos.Market,
		Symbol:     pos.Symbol,
		Product:    pos.Product,
		Side:       side,
		Type:       database.OrderMarket,
		Quantity:   quantity,
		Leverage:   pos.Leverage,
		ReduceOnly: true,
		Status:     database.OrderOpen,
	}
	return s.execute(ctx, order, price, database.RoleTaker)
}

func (s *Service) CancelOrder(ctx context.Context, accountID, orderID string) (*database.Order, error) {
	order, err := s.store.OrderByID(ctx, accountID, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != database.OrderOpen {
		return nil, fmt.Errorf("訂單狀態為 %s，無法取消", order.Status)
	}
	order.Status = database.OrderCanceled
	if err := s.store.SaveOrder(ctx, order); err != nil {
		return nil, err
	}
	return order, nil
}

// execute 建立並結算一張新單。
func (s *Service) execute(ctx context.Context, order *database.Order, price float64, role string) (*database.Order, error) {
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := s.settle(ctx, tx, order, price, role); err != nil {
			return err
		}
		return tx.CreateOrder(ctx, order)
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// fillOpenOrder 結算一張已存在的掛單。訂單在載入後若已不是 open（被取消或
// 上一輪已成交）就跳過。業務規則擋下時把訂單標成 rejected 並記原因，
// 避免每一輪都重試同一張注定失敗的單。
func (s *Service) fillOpenOrder(ctx context.Context, accountID, orderID string, price float64) (*database.Order, error) {
	var result *database.Order
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		order, err := tx.OrderByID(ctx, accountID, orderID)
		if err != nil {
			return err
		}
		if order.Status != database.OrderOpen {
			return nil
		}

		if err := s.settle(ctx, tx, order, price, database.RoleMaker); err != nil {
			if !isBusinessRejection(err) {
				return err
			}
			order.Status = database.OrderRejected
			order.RejectReason = err.Error()
		}
		result = order
		return tx.SaveOrder(ctx, order)
	})
	return result, err
}
