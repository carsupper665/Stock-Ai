package trading

import (
	"math"

	"backend/internal/database"
)

// qtyEpsilon 用來判定部位是否已歸零。浮點相減不見得剛好是 0，
// 這個門檻低於任何有意義的下單量。
const qtyEpsilon = 1e-9

// applyFill 把一筆成交套用到部位上，回傳更新後的部位與已實現損益。
// pos 為 nil 表示尚未持倉；部位歸零時回傳 nil。
func applyFill(pos *database.Position, side string, qty, price, leverage float64) (*database.Position, float64) {
	current := signedQuantity(pos)
	delta := qty
	if side == database.SideSell {
		delta = -qty
	}
	next := current + delta

	entry := price
	realized := 0.0

	switch {
	case current == 0:
		entry = price

	case sameDirection(current, delta):
		total := math.Abs(current) + qty
		entry = (math.Abs(current)*pos.EntryPrice + qty*price) / total

	default:
		closing := math.Min(math.Abs(current), qty)
		if current > 0 {
			realized = (price - pos.EntryPrice) * closing
		} else {
			realized = (pos.EntryPrice - price) * closing
		}
		if math.Abs(next) < qtyEpsilon {
			return nil, realized
		}
		if sameDirection(next, current) {
			entry = pos.EntryPrice // 部分平倉，剩下的成本不變
		}
	}

	updated := &database.Position{
		Quantity:   math.Abs(next),
		EntryPrice: entry,
		Leverage:   leverage,
		Side:       database.Long,
	}
	if next < 0 {
		updated.Side = database.Short
	}
	updated.Margin = updated.Quantity * updated.EntryPrice / leverage

	if pos != nil {
		updated.ID = pos.ID
		updated.AccountID = pos.AccountID
		updated.Market = pos.Market
		updated.Symbol = pos.Symbol
		updated.Product = pos.Product
		updated.CreatedAt = pos.CreatedAt
		// 反手之後舊的停損停利不再對應現在的方向，一律清掉。
		if sameDirection(next, current) {
			updated.StopLoss = pos.StopLoss
			updated.TakeProfit = pos.TakeProfit
			updated.Leverage = pos.Leverage
			updated.Margin = updated.Quantity * updated.EntryPrice / pos.Leverage
		}
	}
	return updated, realized
}

func signedQuantity(pos *database.Position) float64 {
	if pos == nil {
		return 0
	}
	if pos.Side == database.Short {
		return -pos.Quantity
	}
	return pos.Quantity
}

func sameDirection(a, b float64) bool {
	return a > 0 && b > 0 || a < 0 && b < 0
}

// Unrealized 依現價計算未實現損益。
func Unrealized(pos *database.Position, price float64) float64 {
	if pos.Side == database.Short {
		return (pos.EntryPrice - price) * pos.Quantity
	}
	return (price - pos.EntryPrice) * pos.Quantity
}
