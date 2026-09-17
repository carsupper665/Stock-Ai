package trading

import (
	"context"
	"errors"
	"fmt"

	"backend/internal/database"
	"backend/internal/id"
)

// settle 是成交的核心：檢查規則、更新部位與餘額、寫入成交紀錄，
// 並把結果填回 order。不負責寫入 order 本身，由呼叫端決定是建立還是更新。
func (s *Service) settle(ctx context.Context, tx *database.Store, order *database.Order, price float64, role string) error {
	account, err := tx.AccountByID(ctx, order.AccountID)
	if err != nil {
		return err
	}
	if !account.Active() {
		return ErrAccountDisabled
	}

	pos, err := tx.PositionFor(ctx, order.AccountID, order.Market, order.Symbol, order.Product)
	if err != nil && !errors.Is(err, database.ErrNotFound) {
		return err
	}
	if errors.Is(err, database.ErrNotFound) {
		pos = nil
	}

	if err := checkReduceOnly(pos, order); err != nil {
		return err
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

	locked, err := tx.LockedMargin(ctx, order.AccountID)
	if err != nil {
		return err
	}
	available := account.Balance - locked
	if cost := (newMargin - oldMargin) + fee; cost > available+qtyEpsilon {
		return fmt.Errorf("%w：需要 %.8f，可用 %.8f", ErrInsufficient, cost, available)
	}

	if err := s.persistPosition(ctx, tx, order, pos, updated); err != nil {
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

	tradeID, err := id.New("trd_")
	if err != nil {
		return err
	}
	if err := tx.CreateTrade(ctx, &database.Trade{
		ID:          tradeID,
		AccountID:   order.AccountID,
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
		Source:      order.Source,
	}); err != nil {
		return err
	}
	positionID := ""
	if updated != nil {
		positionID = updated.ID
	} else if pos != nil {
		positionID = pos.ID
	}
	return tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
		AccountID: order.AccountID, Event: database.LedgerFill, OrderID: order.ID, TradeID: tradeID,
		PositionID: positionID, Trigger: order.Trigger, Quantity: order.Quantity, Price: price, Fee: fee,
		RealizedPnL: realized, BalanceDelta: realized - fee, BalanceAfter: account.Balance, Source: order.Source,
	})
}

func (s *Service) persistPosition(ctx context.Context, tx *database.Store, order *database.Order, previous, updated *database.Position) error {
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
	// 下單附帶的 stop 只覆寫有給的那一個 level 與其來源；沒給的保留既有設定（反手已由 applyFill 清空）。
	if updated.Side == wantSide {
		if order.StopLoss > 0 {
			updated.StopLoss, updated.StopLossSource = order.StopLoss, order.Source
		}
		if order.TakeProfit > 0 {
			updated.TakeProfit, updated.TakeProfitSource = order.TakeProfit, order.Source
		}
	}

	if previous != nil {
		return tx.SavePosition(ctx, updated)
	}

	positionID, err := id.New("pos_")
	if err != nil {
		return err
	}
	updated.ID = positionID
	updated.AccountID = order.AccountID
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

// checkReduceOnly 讓平倉單在部位已經不在或不夠時失敗，而不是反向開倉。
// 兩筆平倉同時進來、或停損觸發時使用者剛好手動平掉，都會走到這裡。
func checkReduceOnly(pos *database.Position, order *database.Order) error {
	if !order.ReduceOnly {
		return nil
	}
	if pos == nil {
		return ErrPositionClosed
	}
	if order.Quantity > pos.Quantity+qtyEpsilon {
		return ErrCloseTooMuch
	}
	return nil
}

// checkLeverage 擋下加倉時偷改槓桿，減倉與平倉不受限制。
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

func isBusinessRejection(err error) bool {
	for _, target := range []error{
		ErrInsufficient, ErrSpotShort, ErrLeverageLocked, ErrAccountDisabled,
		ErrPositionClosed, ErrCloseTooMuch,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
