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
		Source:     in.Source,
	}

	if in.Type == database.OrderLimit {
		if err := checkStopLevels(in.Side, in.Price, in.StopLoss, in.TakeProfit); err != nil {
			return nil, err
		}
		err := s.store.Tx(ctx, func(tx *database.Store) error {
			if err := tx.LockAccount(ctx, accountID); err != nil {
				return err
			}
			account, err := tx.AccountByID(ctx, accountID)
			if err != nil {
				return err
			}
			if !account.Active() {
				return ErrAccountDisabled
			}
			if err := tx.CreateOrder(ctx, order); err != nil {
				return err
			}
			return tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
				AccountID: order.AccountID, Event: database.LedgerOrderPlaced, OrderID: order.ID,
				Quantity: order.Quantity, Price: order.Price, Source: order.Source,
			})
		})
		if err != nil {
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

// ClosePosition 以現價平倉。quantity 省略或為 0 時全平；source 是發起平倉的 Run。
// 這裡的檢查只是為了在取價前就擋掉明顯錯誤；真正的判定在 closeAt 的交易內重做。
func (s *Service) ClosePosition(ctx context.Context, accountID, positionID string, quantity float64, source database.Source) (*database.Order, error) {
	pos, err := s.store.PositionByID(ctx, accountID, positionID)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, ErrPositionClosed
		}
		return nil, err
	}
	if quantity > pos.Quantity+qtyEpsilon {
		return nil, ErrCloseTooMuch
	}

	price, err := s.prices.GetPrice(ctx, pos.Market, pos.Symbol)
	if err != nil {
		return nil, err
	}
	return s.closeAt(ctx, accountID, positionID, quantity, price.Price, source, "")
}

// closeAt 以指定價格平掉指定 id 的部位，供手動平倉與停損停利觸發共用；quantity 為 0 表示全平。
// 部位在交易內鎖定後重新讀取：id 對不上（已平倉或已重開成新部位）就失敗，不會平到別的部位。
// trigger 非空時是撮合引擎觸發：只有持久化的 stop 現在仍會在此價位觸發同一種 stop
// 才平倉，source 以持久化部位為準（設定該 stop 的 Run），引擎的快照不算數；
// 手動平倉的 source 是呼叫的 Run。
func (s *Service) closeAt(ctx context.Context, accountID, positionID string, quantity, price float64, source database.Source, trigger string) (*database.Order, error) {
	orderID, err := id.New("ord_")
	if err != nil {
		return nil, err
	}
	var order *database.Order
	err = s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
		pos, err := tx.PositionByID(ctx, accountID, positionID)
		if err != nil {
			if errors.Is(err, database.ErrNotFound) {
				return ErrPositionClosed
			}
			return err
		}
		if trigger != "" {
			reason, hit := stopTriggered(pos, price)
			if !hit || reason != trigger {
				return ErrStopInactive
			}
			source = pos.StopLossSource
			if trigger == TriggerTakeProfit {
				source = pos.TakeProfitSource
			}
		}
		if quantity <= 0 {
			quantity = pos.Quantity
		}
		if quantity > pos.Quantity+qtyEpsilon {
			return ErrCloseTooMuch
		}
		side := database.SideSell
		if pos.Side == database.Short {
			side = database.SideBuy
		}
		order = &database.Order{
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
			Source:     source,
			Trigger:    trigger,
		}
		if err := s.settle(ctx, tx, order, price, database.RoleTaker); err != nil {
			return err
		}
		return tx.CreateOrder(ctx, order)
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// CancelOrder 取消掛單。訂單保留建立它的來源；撤單者記在 Ledger。
func (s *Service) CancelOrder(ctx context.Context, accountID, orderID string, source database.Source) (*database.Order, error) {
	var order *database.Order
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
		var err error
		order, err = tx.OrderByID(ctx, accountID, orderID)
		if err != nil {
			return err
		}
		if order.Status != database.OrderOpen {
			return fmt.Errorf("%w：目前狀態為 %s", ErrOrderNotOpen, order.Status)
		}
		order.Status = database.OrderCanceled
		if err := tx.SaveOrder(ctx, order); err != nil {
			return err
		}
		return tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
			AccountID: order.AccountID, Event: database.LedgerOrderCanceled, OrderID: order.ID,
			Quantity: order.Quantity, Price: order.Price, Source: source,
		})
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// execute 建立並結算一張新的市價單。
func (s *Service) execute(ctx context.Context, order *database.Order, price float64, role string) (*database.Order, error) {
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, order.AccountID); err != nil {
			return err
		}
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
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
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
			if err := tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
				AccountID: order.AccountID, Event: database.LedgerOrderRejected, OrderID: order.ID,
				Quantity: order.Quantity, Price: order.Price, Source: order.Source,
			}); err != nil {
				return err
			}
		}
		result = order
		return tx.SaveOrder(ctx, order)
	})
	return result, err
}
