package trading

import (
	"context"
	"testing"

	"backend/internal/database"
)

const (
	makerFee = 0.0005
	takerFee = 0.001
)

func TestMarketBuyOpensPositionAndChargesFee(t *testing.T) {
	f := newFixture(t, 10000, 100)

	order := f.place(t, futuresBuy(1, 10))

	if order.Status != database.OrderFilled {
		t.Fatalf("市價單應立刻成交，狀態為 %s", order.Status)
	}
	if !near(order.AvgFillPrice, 100) {
		t.Fatalf("成交價應為 100，得到 %v", order.AvgFillPrice)
	}
	if !near(order.Fee, 0.1) {
		t.Fatalf("手續費應為 100×0.001=0.1，得到 %v", order.Fee)
	}

	s := f.summary(t)
	if !near(s.Balance, 9999.9) {
		t.Fatalf("餘額應為 10000-0.1=9999.9，得到 %v", s.Balance)
	}
	if !near(s.LockedMargin, 10) {
		t.Fatalf("保證金應為 100×1÷10=10，得到 %v", s.LockedMargin)
	}
	if !near(s.Available, 9989.9) {
		t.Fatalf("可用應為 9999.9-10=9989.9，得到 %v", s.Available)
	}
	if !near(s.Unrealized, 0) || !near(s.Equity, 9999.9) {
		t.Fatalf("剛開倉時浮動損益應為 0: %+v", s)
	}
}

func TestUnrealizedFollowsPrice(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(1, 10))

	f.setPrice(t, 120)
	s := f.summary(t)

	if !near(s.Unrealized, 20) {
		t.Fatalf("浮盈應為 (120-100)×1=20，得到 %v", s.Unrealized)
	}
	if !near(s.Equity, 10019.9) {
		t.Fatalf("權益應為 9999.9+20=10019.9，得到 %v", s.Equity)
	}
	if !near(s.Available, 9989.9) {
		t.Fatalf("浮盈不計入可用餘額，應維持 9989.9，得到 %v", s.Available)
	}
}

func TestClosePositionRealizesProfit(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(1, 10))

	positions, err := f.svc.Positions(context.Background(), f.accountID, "")
	if err != nil || len(positions) != 1 {
		t.Fatalf("應有一個部位: %v %v", positions, err)
	}

	f.setPrice(t, 120)
	order, err := f.svc.ClosePosition(context.Background(), f.accountID, positions[0].ID, 0)
	if err != nil {
		t.Fatalf("平倉失敗: %v", err)
	}

	if !near(order.RealizedPnL, 20) {
		t.Fatalf("已實現損益應為 20，得到 %v", order.RealizedPnL)
	}
	if !near(order.Fee, 0.12) {
		t.Fatalf("平倉手續費應為 120×0.001=0.12，得到 %v", order.Fee)
	}

	s := f.summary(t)
	if !near(s.Balance, 10019.78) {
		t.Fatalf("餘額應為 9999.9+20-0.12=10019.78，得到 %v", s.Balance)
	}
	if !near(s.LockedMargin, 0) {
		t.Fatalf("平倉後保證金應歸零，得到 %v", s.LockedMargin)
	}

	left, _ := f.svc.Positions(context.Background(), f.accountID, "")
	if len(left) != 0 {
		t.Fatalf("平倉後不該留下部位: %+v", left)
	}
}

func TestClosePositionPartially(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(2, 10))

	positions, _ := f.svc.Positions(context.Background(), f.accountID, "")
	f.setPrice(t, 110)

	order, err := f.svc.ClosePosition(context.Background(), f.accountID, positions[0].ID, 1)
	if err != nil {
		t.Fatalf("部分平倉失敗: %v", err)
	}
	if !near(order.RealizedPnL, 10) {
		t.Fatalf("平掉 1 顆應實現 10，得到 %v", order.RealizedPnL)
	}

	left, _ := f.svc.Positions(context.Background(), f.accountID, "")
	if len(left) != 1 || !near(left[0].Quantity, 1) {
		t.Fatalf("應剩下 1 顆: %+v", left)
	}
	if !near(left[0].EntryPrice, 100) {
		t.Fatalf("部分平倉不該改變進場價，得到 %v", left[0].EntryPrice)
	}
}

func TestLimitOrderStaysOpen(t *testing.T) {
	f := newFixture(t, 10000, 100)

	order := f.place(t, PlaceOrderInput{
		Symbol: "BTCUSDT", Product: database.ProductFutures,
		Side: database.SideBuy, Type: database.OrderLimit,
		Quantity: 1, Price: 90, Leverage: 10,
	})

	// Step 5 的撮合引擎才會觸發限價單，這一步只負責掛上去。
	if order.Status != database.OrderOpen {
		t.Fatalf("限價單應維持 open，得到 %s", order.Status)
	}
	if s := f.summary(t); !near(s.Balance, 10000) || !near(s.LockedMargin, 0) {
		t.Fatalf("未成交的限價單不該動到餘額或保證金: %+v", s)
	}

	canceled, err := f.svc.CancelOrder(context.Background(), f.accountID, order.ID)
	if err != nil {
		t.Fatalf("取消限價單: %v", err)
	}
	if canceled.Status != database.OrderCanceled {
		t.Fatalf("取消後狀態應為 canceled，得到 %s", canceled.Status)
	}
	if _, err := f.svc.CancelOrder(context.Background(), f.accountID, order.ID); err == nil {
		t.Fatal("重複取消應該失敗")
	}
}

func TestTradesAreRecorded(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(1, 10))

	positions, _ := f.svc.Positions(context.Background(), f.accountID, "")
	f.setPrice(t, 120)
	if _, err := f.svc.ClosePosition(context.Background(), f.accountID, positions[0].ID, 0); err != nil {
		t.Fatalf("平倉: %v", err)
	}

	trades, err := f.svc.Trades(context.Background(), f.accountID, 10)
	if err != nil {
		t.Fatalf("查詢成交紀錄: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("開倉與平倉各一筆成交，得到 %d", len(trades))
	}
	for _, trade := range trades {
		if trade.Role != database.RoleTaker {
			t.Fatalf("市價成交應記為 taker，得到 %s", trade.Role)
		}
	}

	orders, _ := f.svc.Orders(context.Background(), f.accountID, database.OrderFilled, 10)
	if len(orders) != 2 {
		t.Fatalf("應有兩筆已成交訂單，得到 %d", len(orders))
	}
}
