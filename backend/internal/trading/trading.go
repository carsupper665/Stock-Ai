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

// PlaceOrder 下單。市價單立刻以現價成交；限價單先掛著，等 Step 5 的撮合引擎觸發。
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
	return s.execute(ctx, accountID, order, price.Price, database.RoleTaker)
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

	side := database.SideSell
	if pos.Side == database.Short {
		side = database.SideBuy
	}

	price, err := s.prices.GetPrice(ctx, pos.Market, pos.Symbol)
	if err != nil {
		return nil, err
	}

	orderID, err := id.New("ord_")
	if err != nil {
		return nil, err
	}
	order := &database.Order{
		ID:        orderID,
		AccountID: accountID,
		Market:    pos.Market,
		Symbol:    pos.Symbol,
		Product:   pos.Product,
		Side:      side,
		Type:      database.OrderMarket,
		Quantity:  quantity,
		Leverage:  pos.Leverage,
		Status:    database.OrderOpen,
	}
	return s.execute(ctx, accountID, order, price.Price, database.RoleTaker)
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

// execute 在一個交易裡完成成交：更新部位、餘額，寫入訂單與成交紀錄。
func (s *Service) execute(ctx context.Context, accountID string, order *database.Order, price float64, role string) (*database.Order, error) {
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		account, err := tx.AccountByID(ctx, accountID)
		if err != nil {
			return err
		}
		if !account.Active() {
			return ErrAccountDisabled
		}

		pos, err := tx.PositionFor(ctx, accountID, order.Market, order.Symbol, order.Product)
		if err != nil && !errors.Is(err, database.ErrNotFound) {
			return err
		}
		if errors.Is(err, database.ErrNotFound) {
			pos = nil
		}

		if err := checkSpotShort(order.Product, order.Side, pos, order.Quantity); err != nil {
			return err
		}
		if err := checkLeverage(pos, order); err != nil {
			return err
		}

		fee := order.Quantity * price * s.rate(role)
		oldMargin := 0.0
		if pos != nil {
			oldMargin = pos.Margin
		}

		updated, realized := applyFill(pos, order.Side, order.Quantity, price, order.Leverage)
		newMargin := 0.0
		if updated != nil {
			newMargin = updated.Margin
		}

		locked, err := tx.LockedMargin(ctx, accountID)
		if err != nil {
			return err
		}
		available := account.Balance - locked
		if cost := (newMargin - oldMargin) + fee; cost > available+qtyEpsilon {
			return fmt.Errorf("%w：需要 %.8f，可用 %.8f", ErrInsufficient, cost, available)
		}

		if err := s.persistPosition(ctx, tx, accountID, order, pos, updated); err != nil {
			return err
		}

		account.Balance += realized - fee
		if err := tx.SaveAccount(ctx, account); err != nil {
			return err
		}

		order.Status = database.OrderFilled
		order.FilledQuantity = order.Quantity
		order.AvgFillPrice = price
		order.Fee = fee
		order.RealizedPnL = realized
		if err := tx.CreateOrder(ctx, order); err != nil {
			return err
		}

		tradeID, err := id.New("trd_")
		if err != nil {
			return err
		}
		return tx.CreateTrade(ctx, &database.Trade{
			ID:          tradeID,
			AccountID:   accountID,
			OrderID:     order.ID,
			Market:      order.Market,
			Symbol:      order.Symbol,
			Product:     order.Product,
			Side:        order.Side,
			Role:        role,
			Quantity:    order.Quantity,
			Price:       price,
			Fee:         fee,
			RealizedPnL: realized,
		})
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

func (s *Service) persistPosition(ctx context.Context, tx *database.Store, accountID string,
	order *database.Order, previous, updated *database.Position) error {
	if updated == nil {
		if previous != nil {
			return tx.DeletePosition(ctx, previous.ID)
		}
		return nil
	}

	wantSide := database.Long
	if order.Side == database.SideSell {
		wantSide = database.Short
	}
	if updated.Side == wantSide && (order.StopLoss > 0 || order.TakeProfit > 0) {
		updated.StopLoss = order.StopLoss
		updated.TakeProfit = order.TakeProfit
	}

	if previous != nil {
		return tx.SavePosition(ctx, updated)
	}

	positionID, err := id.New("pos_")
	if err != nil {
		return err
	}
	updated.ID = positionID
	updated.AccountID = accountID
	updated.Market = order.Market
	updated.Symbol = order.Symbol
	updated.Product = order.Product
	return tx.CreatePosition(ctx, updated)
}

func (s *Service) rate(role string) float64 {
	if role == database.RoleMaker {
		return s.maker
	}
	return s.taker
}

// checkLeverage 擋下加倉時偷改槓桿。沿用舊值會讓下單參數被靜默忽略，
// 減倉與平倉則不受限制。
func checkLeverage(pos *database.Position, order *database.Order) error {
	if pos == nil || pos.Leverage == order.Leverage {
		return nil
	}
	delta := order.Quantity
	if order.Side == database.SideSell {
		delta = -delta
	}
	if sameDirection(signedQuantity(pos), delta) {
		return ErrLeverageLocked
	}
	return nil
}
