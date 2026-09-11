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

// StopInput 的欄位為 nil 表示不動，0 表示移除，大於 0 表示設定。
type StopInput struct {
	StopLoss   *float64
	TakeProfit *float64
}

// SetStops 設定、更新或移除部位的停損停利（規格 §5）。
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

	stopLoss, takeProfit := pos.StopLoss, pos.TakeProfit
	if in.StopLoss != nil {
		stopLoss = *in.StopLoss
	}
	if in.TakeProfit != nil {
		takeProfit = *in.TakeProfit
	}
	if stopLoss < 0 || takeProfit < 0 {
		return nil, ErrInvalidStopLoss
	}

	if stopLoss > 0 || takeProfit > 0 {
		price, err := s.prices.GetPrice(ctx, pos.Market, pos.Symbol)
		if err != nil {
			return nil, err
		}
		side := database.SideBuy
		if pos.Side == database.Short {
			side = database.SideSell
		}
		if err := checkStopLevels(side, price.Price, stopLoss, takeProfit); err != nil {
			return nil, err
		}
	}

	pos.StopLoss, pos.TakeProfit = stopLoss, takeProfit
	if err := s.store.SavePosition(ctx, pos); err != nil {
		return nil, err
	}
	return pos, nil
}
