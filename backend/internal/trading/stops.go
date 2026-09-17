package trading

import (
	"context"
	"errors"

	"backend/internal/database"
)

const (
	TriggerStopLoss   = "stop_loss"
	TriggerTakeProfit = "take_profit"
)

// stopTriggered 依規格 §5 判斷停損停利是否觸發：
// 多單 price <= SL 或 price >= TP；空單相反。
func stopTriggered(pos *database.Position, price float64) (string, bool) {
	long := pos.Side == database.Long
	switch {
	case pos.StopLoss > 0 && (long && price <= pos.StopLoss || !long && price >= pos.StopLoss):
		return TriggerStopLoss, true
	case pos.TakeProfit > 0 && (long && price >= pos.TakeProfit || !long && price <= pos.TakeProfit):
		return TriggerTakeProfit, true
	}
	return "", false
}

// limitTriggered：買單在價格跌到限價以下成交，賣單在漲到限價以上成交。
func limitTriggered(order *database.Order, price float64) bool {
	if order.Side == database.SideBuy {
		return price <= order.Price
	}
	return price >= order.Price
}

// StopInput 的欄位為 nil 表示不動，0 表示移除，大於 0 表示設定。Source 是發起變更的 Run，
// 只寫到這次有動到的 stop 上。
type StopInput struct {
	StopLoss   *float64
	TakeProfit *float64
	Source     database.Source
}

// SetStops 設定、更新或移除部位的停損停利（規格 §5）。
// 取價與方向檢查在交易外做（不拿著交易等行情）；寫入時在交易內鎖定帳號後重讀部位，
// 只有仍存在的部位才更新，沒動到的 level 與其來源以重讀結果為準，不會被這次的舊快照蓋掉。
func (s *Service) SetStops(ctx context.Context, accountID, positionID string, in StopInput) (*database.Position, error) {
	if in.StopLoss == nil && in.TakeProfit == nil {
		return nil, ErrNoStopChange
	}

	pos, err := s.store.PositionByID(ctx, accountID, positionID)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, ErrPositionClosed
		}
		return nil, err
	}
	stopLoss, takeProfit := applyStopInput(pos, in)
	if stopLoss < 0 || takeProfit < 0 {
		return nil, ErrInvalidStopLoss
	}
	var price float64
	if stopLoss > 0 || takeProfit > 0 {
		current, err := s.prices.GetPrice(ctx, pos.Market, pos.Symbol)
		if err != nil {
			return nil, err
		}
		price = current.Price
		if err := checkStopLevels(entrySide(pos), price, stopLoss, takeProfit); err != nil {
			return nil, err
		}
	}

	err = s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
		pos, err = tx.PositionByID(ctx, accountID, positionID)
		if err != nil {
			if errors.Is(err, database.ErrNotFound) {
				return ErrPositionClosed
			}
			return err
		}
		pos.StopLoss, pos.TakeProfit = applyStopInput(pos, in)
		if in.StopLoss != nil {
			pos.StopLossSource = sourceIfSet(pos.StopLoss, in.Source)
		}
		if in.TakeProfit != nil {
			pos.TakeProfitSource = sourceIfSet(pos.TakeProfit, in.Source)
		}
		// 部位若在取價期間反手，方向檢查要以重讀後的方向重做，只檢查這次要設定的 level。
		if price > 0 {
			if err := checkStopLevels(entrySide(pos), price, levelOr(in.StopLoss, 0), levelOr(in.TakeProfit, 0)); err != nil {
				return err
			}
		}
		if err := tx.UpdatePositionStops(ctx, pos); err != nil {
			if errors.Is(err, database.ErrNotFound) {
				return ErrPositionClosed
			}
			return err
		}
		return tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
			AccountID: pos.AccountID, Event: database.LedgerStopsUpdated, PositionID: pos.ID,
			Quantity: pos.Quantity, StopLoss: pos.StopLoss, TakeProfit: pos.TakeProfit, Source: in.Source,
		})
	})
	if err != nil {
		return nil, err
	}
	return pos, nil
}

// applyStopInput 回傳套用輸入後的兩個 level：nil 不動，其餘採輸入值。
func applyStopInput(pos *database.Position, in StopInput) (stopLoss, takeProfit float64) {
	return levelOr(in.StopLoss, pos.StopLoss), levelOr(in.TakeProfit, pos.TakeProfit)
}

func levelOr(level *float64, fallback float64) float64 {
	if level == nil {
		return fallback
	}
	return *level
}

// entrySide 是開出這個部位方向所用的下單方向，供 checkStopLevels 判斷 stop 該在哪一側。
func entrySide(pos *database.Position) string {
	if pos.Side == database.Short {
		return database.SideSell
	}
	return database.SideBuy
}

// sourceIfSet 讓被移除的 stop 不再帶來源。
func sourceIfSet(level float64, source database.Source) database.Source {
	if level > 0 {
		return source
	}
	return database.Source{}
}
