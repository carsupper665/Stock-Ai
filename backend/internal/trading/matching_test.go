package trading

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/internal/database"
)

func limitOrder(side string, qty, price, leverage float64) PlaceOrderInput {
	return PlaceOrderInput{
		Symbol: "BTCUSDT", Product: database.ProductFutures,
		Side: side, Type: database.OrderLimit,
		Quantity: qty, Price: price, Leverage: leverage,
	}
}

func (f *fixture) tick(t *testing.T) TickResult {
	t.Helper()
	return NewEngine(f.svc, time.Hour, nil).Tick(context.Background())
}

func (f *fixture) order(t *testing.T, id string) *database.Order {
	t.Helper()
	order, err := f.svc.Order(context.Background(), f.accountID, id)
	if err != nil {
		t.Fatalf("查詢訂單 %s: %v", id, err)
	}
	return order
}

func (f *fixture) onlyPosition(t *testing.T) *database.Position {
	t.Helper()
	positions, err := f.svc.Positions(context.Background(), f.accountID, "")
	if err != nil || len(positions) != 1 {
		t.Fatalf("應恰好有一個部位: %v %v", positions, err)
	}
	return &positions[0].Position
}

func TestLimitBuyFillsAtLimitPriceWhenPriceDrops(t *testing.T) {
	f := newFixture(t, 10000, 100)
	order := f.place(t, limitOrder(database.SideBuy, 1, 90, 10))

	if r := f.tick(t); r.Filled != 0 {
		t.Fatalf("價格 100 未跌到限價 90，不該成交: %+v", r)
	}

	f.setPrice(t, 85)
	r := f.tick(t)
	if r.Filled != 1 {
		t.Fatalf("價格跌破限價應成交一筆: %+v", r)
	}

	filled := f.order(t, order.ID)
	if filled.Status != database.OrderFilled {
		t.Fatalf("狀態應為 filled，得到 %s", filled.Status)
	}
	if !near(filled.AvgFillPrice, 90) {
		t.Fatalf("限價單應以限價 90 成交而非市價 85，得到 %v", filled.AvgFillPrice)
	}
	if !near(filled.Fee, 90*makerFee) {
		t.Fatalf("掛單成交應收 maker 費率: 預期 %v，得到 %v", 90*makerFee, filled.Fee)
	}

	pos := f.onlyPosition(t)
	if pos.Side != database.Long || !near(pos.EntryPrice, 90) || !near(pos.Margin, 9) {
		t.Fatalf("部位應為多單、成本 90、保證金 9: %+v", pos)
	}

	trades, _ := f.svc.Trades(context.Background(), f.accountID, 10)
	if len(trades) != 1 || trades[0].Role != database.RoleMaker {
		t.Fatalf("應有一筆 maker 成交: %+v", trades)
	}
}

func TestLimitSellFillsWhenPriceRises(t *testing.T) {
	f := newFixture(t, 10000, 100)
	order := f.place(t, limitOrder(database.SideSell, 1, 110, 10))

	f.setPrice(t, 105)
	if r := f.tick(t); r.Filled != 0 {
		t.Fatalf("價格 105 未漲到限價 110，不該成交: %+v", r)
	}

	f.setPrice(t, 112)
	if r := f.tick(t); r.Filled != 1 {
		t.Fatalf("價格漲破限價應成交: %+v", r)
	}
	if filled := f.order(t, order.ID); !near(filled.AvgFillPrice, 110) {
		t.Fatalf("賣出限價單應以 110 成交，得到 %v", filled.AvgFillPrice)
	}
	if pos := f.onlyPosition(t); pos.Side != database.Short {
		t.Fatalf("賣出開倉應為空單: %+v", pos)
	}
}

func TestFilledOrderIsNotFilledAgain(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, limitOrder(database.SideBuy, 1, 90, 10))

	f.setPrice(t, 80)
	f.tick(t)
	if r := f.tick(t); r.Filled != 0 || r.Rejected != 0 {
		t.Fatalf("已成交的單不該再被處理: %+v", r)
	}
	if pos := f.onlyPosition(t); !near(pos.Quantity, 1) {
		t.Fatalf("重複撮合讓部位變成 %v", pos.Quantity)
	}
}

func TestCanceledOrderIsIgnored(t *testing.T) {
	f := newFixture(t, 10000, 100)
	order := f.place(t, limitOrder(database.SideBuy, 1, 90, 10))
	if _, err := f.svc.CancelOrder(context.Background(), f.accountID, order.ID, database.Source{}); err != nil {
		t.Fatalf("取消: %v", err)
	}

	f.setPrice(t, 80)
	if r := f.tick(t); r.Filled != 0 {
		t.Fatalf("已取消的單不該成交: %+v", r)
	}
	if positions, _ := f.svc.Positions(context.Background(), f.accountID, ""); len(positions) != 0 {
		t.Fatalf("已取消的單開出了部位: %+v", positions)
	}
}

func TestTriggeredOrderWithoutBalanceIsRejectedOnce(t *testing.T) {
	f := newFixture(t, 10000, 100)
	order := f.place(t, limitOrder(database.SideBuy, 1000, 90, 1))

	f.setPrice(t, 80)
	if r := f.tick(t); r.Rejected != 1 || r.Filled != 0 {
		t.Fatalf("餘額不足的掛單觸發時應被拒絕: %+v", r)
	}

	rejected := f.order(t, order.ID)
	if rejected.Status != database.OrderRejected {
		t.Fatalf("狀態應為 rejected，得到 %s", rejected.Status)
	}
	if rejected.RejectReason == "" {
		t.Fatal("被拒絕的單應記下原因")
	}

	if r := f.tick(t); r.Rejected != 0 {
		t.Fatalf("被拒絕的單不該每一輪重試: %+v", r)
	}
	if s := f.summary(t); !near(s.Balance, 10000) {
		t.Fatalf("被拒絕的單不該動到餘額: %v", s.Balance)
	}
}

func TestStopLossClosesLong(t *testing.T) {
	f := newFixture(t, 10000, 100)
	in := futuresBuy(1, 10)
	in.StopLoss, in.TakeProfit = 90, 120
	f.place(t, in)

	f.setPrice(t, 95)
	if r := f.tick(t); r.Closed != 0 {
		t.Fatalf("價格 95 未觸及停損 90: %+v", r)
	}

	f.setPrice(t, 89)
	if r := f.tick(t); r.Closed != 1 {
		t.Fatalf("跌破停損應平倉: %+v", r)
	}
	if positions, _ := f.svc.Positions(context.Background(), f.accountID, ""); len(positions) != 0 {
		t.Fatalf("停損後部位應消失: %+v", positions)
	}

	orders, _ := f.svc.Orders(context.Background(), f.accountID, database.OrderFilled, 10)
	if len(orders) != 2 {
		t.Fatalf("應有開倉與停損平倉兩筆: %d", len(orders))
	}
	closing := orders[0]
	if !closing.ReduceOnly || closing.Side != database.SideSell || !near(closing.AvgFillPrice, 89) {
		t.Fatalf("停損平倉單應為 reduce-only 賣單、以觸發價 89 成交: %+v", closing)
	}
	if !near(closing.RealizedPnL, -11) {
		t.Fatalf("已實現損益應為 (89-100)×1=-11，得到 %v", closing.RealizedPnL)
	}
}

func TestTakeProfitClosesLong(t *testing.T) {
	f := newFixture(t, 10000, 100)
	in := futuresBuy(1, 10)
	in.TakeProfit = 120
	f.place(t, in)

	f.setPrice(t, 125)
	if r := f.tick(t); r.Closed != 1 {
		t.Fatalf("漲破停利應平倉: %+v", r)
	}
	orders, _ := f.svc.Orders(context.Background(), f.accountID, database.OrderFilled, 10)
	if !near(orders[0].RealizedPnL, 25) {
		t.Fatalf("已實現損益應為 (125-100)×1=25，得到 %v", orders[0].RealizedPnL)
	}
}

func TestStopsOnShortAreMirrored(t *testing.T) {
	open := func(t *testing.T) *fixture {
		f := newFixture(t, 10000, 100)
		in := futuresBuy(1, 10)
		in.Side = database.SideSell
		in.StopLoss, in.TakeProfit = 110, 80
		f.place(t, in)
		return f
	}

	t.Run("停損：價格漲破", func(t *testing.T) {
		f := open(t)
		f.setPrice(t, 111)
		if r := f.tick(t); r.Closed != 1 {
			t.Fatalf("空單漲破停損應平倉: %+v", r)
		}
		orders, _ := f.svc.Orders(context.Background(), f.accountID, database.OrderFilled, 10)
		if orders[0].Side != database.SideBuy || !near(orders[0].RealizedPnL, -11) {
			t.Fatalf("空單停損應買回、損益 (100-111)×1=-11: %+v", orders[0])
		}
	})

	t.Run("停利：價格跌破", func(t *testing.T) {
		f := open(t)
		f.setPrice(t, 79)
		if r := f.tick(t); r.Closed != 1 {
			t.Fatalf("空單跌破停利應平倉: %+v", r)
		}
		orders, _ := f.svc.Orders(context.Background(), f.accountID, database.OrderFilled, 10)
		if !near(orders[0].RealizedPnL, 21) {
			t.Fatalf("損益應為 (100-79)×1=21: %+v", orders[0])
		}
	})

	t.Run("區間內不動", func(t *testing.T) {
		f := open(t)
		f.setPrice(t, 95)
		if r := f.tick(t); r.Closed != 0 {
			t.Fatalf("價格在區間內不該平倉: %+v", r)
		}
	})
}

func TestReduceOnlyPreventsFlipOnDoubleClose(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(1, 10))
	pos := f.onlyPosition(t)

	if _, err := f.svc.closeAt(context.Background(), f.accountID, pos.ID, 1, 100, database.Source{}, ""); err != nil {
		t.Fatalf("第一次平倉: %v", err)
	}
	// 拿著舊的部位 id 再平一次，模擬兩筆平倉同時進來。
	if _, err := f.svc.closeAt(context.Background(), f.accountID, pos.ID, 1, 100, database.Source{}, ""); !errors.Is(err, ErrPositionClosed) {
		t.Fatalf("部位已不在時 reduce-only 單應失敗而不是反向開倉，得到 %v", err)
	}
	if positions, _ := f.svc.Positions(context.Background(), f.accountID, ""); len(positions) != 0 {
		t.Fatalf("第二筆平倉反向開出了部位: %+v", positions)
	}
}

// 引擎的快照判定觸發後，closeAt 必須以持久化部位重新評估：stop 已被移除或移到別處就不平，
// 平倉時的來源是現在設定該 stop 的 Run，而不是快照裡的。
func TestTriggeredCloseReevaluatesPersistedStop(t *testing.T) {
	f := newFixture(t, 10000, 100)
	in := futuresBuy(1, 10)
	in.StopLoss = 95
	in.Source = database.Source{SessionID: "s_1", RunID: 1}
	f.place(t, in)
	pos := f.onlyPosition(t)
	ctx := context.Background()
	ptr := func(v float64) *float64 { return &v }

	// 快照說 95 觸發，但停損已被 Run 2 移到 90：價格 94 不能平。
	if _, err := f.svc.SetStops(ctx, f.accountID, pos.ID, StopInput{StopLoss: ptr(90), Source: database.Source{SessionID: "s_1", RunID: 2}}); err != nil {
		t.Fatalf("移動停損: %v", err)
	}
	if _, err := f.svc.closeAt(ctx, f.accountID, pos.ID, 0, 94, database.Source{}, TriggerStopLoss); !errors.Is(err, ErrStopInactive) {
		t.Fatalf("停損已移走時不該平倉，得到 %v", err)
	}
	// 停利觸發的快照也不能拿停損來平。
	if _, err := f.svc.closeAt(ctx, f.accountID, pos.ID, 0, 89, database.Source{}, TriggerTakeProfit); !errors.Is(err, ErrStopInactive) {
		t.Fatalf("觸發種類不符時不該平倉，得到 %v", err)
	}
	if positions, _ := f.svc.Positions(ctx, f.accountID, ""); len(positions) != 1 {
		t.Fatalf("部位不該被平掉: %+v", positions)
	}

	closed, err := f.svc.closeAt(ctx, f.accountID, pos.ID, 0, 89, database.Source{SessionID: "stale", RunID: 99}, TriggerStopLoss)
	if err != nil {
		t.Fatalf("現在的停損 90 在 89 應平倉: %v", err)
	}
	if closed.Trigger != TriggerStopLoss || closed.Source.RunID != 2 || closed.Source.SessionID != "s_1" || !near(closed.Quantity, 1) {
		t.Fatalf("觸發平倉應追溯到設定 90 的 Run 2 並平掉整個部位: %+v", closed)
	}
	if _, err := f.svc.closeAt(ctx, f.accountID, pos.ID, 0, 89, database.Source{}, TriggerStopLoss); !errors.Is(err, ErrPositionClosed) {
		t.Fatalf("部位已平掉後再觸發應回 ErrPositionClosed，得到 %v", err)
	}
}

func TestEngineStartAndStop(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, limitOrder(database.SideBuy, 1, 90, 10))

	engine := NewEngine(f.svc, 5*time.Millisecond, nil)
	engine.Start()
	f.setPrice(t, 80)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if positions, _ := f.svc.Positions(context.Background(), f.accountID, ""); len(positions) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	engine.Stop()
	engine.Stop()

	if pos := f.onlyPosition(t); !near(pos.EntryPrice, 90) {
		t.Fatalf("背景迴圈應自行撮合限價單: %+v", pos)
	}
}
