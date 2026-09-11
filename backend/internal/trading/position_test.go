package trading

import (
	"math"
	"testing"

	"backend/internal/database"
)

func pos(side string, qty, entry, leverage float64) *database.Position {
	return &database.Position{
		ID: "pos_1", AccountID: "acc_1", Market: "crypto", Symbol: "BTCUSDT",
		Product: database.ProductFutures, Side: side,
		Quantity: qty, EntryPrice: entry, Leverage: leverage,
		Margin: qty * entry / leverage,
	}
}

func near(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func TestApplyFillOpensPosition(t *testing.T) {
	updated, realized := applyFill(nil, database.SideBuy, 1, 100, 10)

	if realized != 0 {
		t.Fatalf("開倉不該有已實現損益，得到 %v", realized)
	}
	if updated.Side != database.Long || !near(updated.Quantity, 1) || !near(updated.EntryPrice, 100) {
		t.Fatalf("開倉結果不符: %+v", updated)
	}
	if !near(updated.Margin, 10) {
		t.Fatalf("保證金應為 1×100÷10=10，得到 %v", updated.Margin)
	}
}

func TestApplyFillOpensShort(t *testing.T) {
	updated, realized := applyFill(nil, database.SideSell, 2, 100, 5)

	if realized != 0 {
		t.Fatalf("開倉不該有已實現損益，得到 %v", realized)
	}
	if updated.Side != database.Short || !near(updated.Quantity, 2) {
		t.Fatalf("空單開倉結果不符: %+v", updated)
	}
	if !near(updated.Margin, 40) {
		t.Fatalf("保證金應為 2×100÷5=40，得到 %v", updated.Margin)
	}
}

func TestApplyFillAveragesEntryWhenAdding(t *testing.T) {
	updated, realized := applyFill(pos(database.Long, 1, 100, 10), database.SideBuy, 1, 120, 10)

	if realized != 0 {
		t.Fatalf("加倉不該有已實現損益，得到 %v", realized)
	}
	if !near(updated.Quantity, 2) || !near(updated.EntryPrice, 110) {
		t.Fatalf("加權平均進場價應為 110，得到 %+v", updated)
	}
	if !near(updated.Margin, 22) {
		t.Fatalf("保證金應為 2×110÷10=22，得到 %v", updated.Margin)
	}
}

func TestApplyFillPartialCloseKeepsEntryPrice(t *testing.T) {
	updated, realized := applyFill(pos(database.Long, 2, 110, 10), database.SideSell, 1, 130, 10)

	if !near(realized, 20) {
		t.Fatalf("已實現損益應為 (130-110)×1=20，得到 %v", realized)
	}
	if !near(updated.Quantity, 1) || !near(updated.EntryPrice, 110) {
		t.Fatalf("部分平倉不該改變進場價: %+v", updated)
	}
	if !near(updated.Margin, 11) {
		t.Fatalf("保證金應為 1×110÷10=11，得到 %v", updated.Margin)
	}
}

func TestApplyFillFullCloseReturnsNil(t *testing.T) {
	updated, realized := applyFill(pos(database.Long, 1, 110, 10), database.SideSell, 1, 130, 10)

	if updated != nil {
		t.Fatalf("全平之後不該留下部位: %+v", updated)
	}
	if !near(realized, 20) {
		t.Fatalf("已實現損益應為 20，得到 %v", realized)
	}
}

func TestApplyFillShortProfitAndLoss(t *testing.T) {
	_, profit := applyFill(pos(database.Short, 1, 100, 5), database.SideBuy, 1, 90, 5)
	if !near(profit, 10) {
		t.Fatalf("空單在 90 平倉應賺 (100-90)×1=10，得到 %v", profit)
	}

	_, loss := applyFill(pos(database.Short, 1, 100, 5), database.SideBuy, 1, 115, 5)
	if !near(loss, -15) {
		t.Fatalf("空單在 115 平倉應虧 -15，得到 %v", loss)
	}
}

func TestApplyFillFlipsDirection(t *testing.T) {
	updated, realized := applyFill(pos(database.Long, 1, 100, 10), database.SideSell, 3, 110, 10)

	if !near(realized, 10) {
		t.Fatalf("反手時只結算原本的 1 顆: 應為 10，得到 %v", realized)
	}
	if updated == nil || updated.Side != database.Short {
		t.Fatalf("反手後應變成空單: %+v", updated)
	}
	if !near(updated.Quantity, 2) || !near(updated.EntryPrice, 110) {
		t.Fatalf("反手後剩餘 2 顆、以成交價 110 為新成本: %+v", updated)
	}
}

func TestApplyFillClearsStopLevelsOnFlip(t *testing.T) {
	existing := pos(database.Long, 1, 100, 10)
	existing.StopLoss, existing.TakeProfit = 90, 120

	kept, _ := applyFill(existing, database.SideBuy, 1, 100, 10)
	if kept.StopLoss != 90 || kept.TakeProfit != 120 {
		t.Fatalf("同向加倉應保留停損停利: %+v", kept)
	}

	flipped, _ := applyFill(existing, database.SideSell, 3, 110, 10)
	if flipped.StopLoss != 0 || flipped.TakeProfit != 0 {
		t.Fatalf("反手後的停損停利方向已失效，必須清掉: %+v", flipped)
	}
}

func TestApplyFillTreatsTinyRemainderAsClosed(t *testing.T) {
	// 0.1+0.2 這類尾數不該讓部位卡著一個近乎 0 的數量。
	existing := pos(database.Long, 0.1+0.2, 100, 1)
	updated, _ := applyFill(existing, database.SideSell, 0.3, 100, 1)

	if updated != nil {
		t.Fatalf("殘量小於門檻時應視為平倉，得到 %+v", updated)
	}
}

func TestUnrealized(t *testing.T) {
	long := pos(database.Long, 2, 100, 10)
	if got := Unrealized(long, 110); !near(got, 20) {
		t.Fatalf("多單浮盈應為 (110-100)×2=20，得到 %v", got)
	}
	if got := Unrealized(long, 95); !near(got, -10) {
		t.Fatalf("多單浮虧應為 -10，得到 %v", got)
	}

	short := pos(database.Short, 2, 100, 10)
	if got := Unrealized(short, 90); !near(got, 20) {
		t.Fatalf("空單浮盈應為 (100-90)×2=20，得到 %v", got)
	}
	if got := Unrealized(short, 105); !near(got, -10) {
		t.Fatalf("空單浮虧應為 -10，得到 %v", got)
	}
}
