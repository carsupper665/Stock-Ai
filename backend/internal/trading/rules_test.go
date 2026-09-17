package trading

import (
	"context"
	"errors"
	"testing"

	"backend/internal/database"
)

func TestCloseRejectsTooMuchAndMissingPosition(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(1, 10))
	positions, _ := f.svc.Positions(context.Background(), f.accountID, "")

	if _, err := f.svc.ClosePosition(context.Background(), f.accountID, positions[0].ID, 5, database.Source{}); !errors.Is(err, ErrCloseTooMuch) {
		t.Fatalf("平倉數量超過持倉應被拒絕，得到 %v", err)
	}
	if _, err := f.svc.ClosePosition(context.Background(), f.accountID, "pos_missing", 0, database.Source{}); !errors.Is(err, ErrPositionClosed) {
		t.Fatalf("平不存在的部位應回 ErrPositionClosed，得到 %v", err)
	}
}

func TestInsufficientBalanceIsRejected(t *testing.T) {
	f := newFixture(t, 100, 100)

	// 需要保證金 1000×100÷10=10000，遠超過餘額。
	_, err := f.svc.PlaceOrder(context.Background(), f.accountID, futuresBuy(1000, 10))
	if !errors.Is(err, ErrInsufficient) {
		t.Fatalf("餘額不足應被拒絕，得到 %v", err)
	}

	s := f.summary(t)
	if !near(s.Balance, 100) {
		t.Fatalf("下單失敗不該動到餘額，得到 %v", s.Balance)
	}
	orders, _ := f.svc.Orders(context.Background(), f.accountID, "", 10)
	if len(orders) != 0 {
		t.Fatalf("被拒絕的單不該留下紀錄: %+v", orders)
	}
}

func TestSpotRules(t *testing.T) {
	f := newFixture(t, 10000, 100)

	spotBuy := PlaceOrderInput{
		Symbol: "BTCUSDT", Product: database.ProductSpot,
		Side: database.SideBuy, Type: database.OrderMarket, Quantity: 1,
	}
	order := f.place(t, spotBuy)
	if !near(order.Leverage, 1) {
		t.Fatalf("spot 槓桿應為 1，得到 %v", order.Leverage)
	}

	s := f.summary(t)
	if !near(s.LockedMargin, 100) {
		t.Fatalf("spot 應鎖住全額 100，得到 %v", s.LockedMargin)
	}

	leveraged := spotBuy
	leveraged.Leverage = 5
	if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, leveraged); !errors.Is(err, ErrSpotLeverage) {
		t.Fatalf("spot 不該接受槓桿，得到 %v", err)
	}

	oversell := spotBuy
	oversell.Side = database.SideSell
	oversell.Quantity = 2
	if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, oversell); !errors.Is(err, ErrSpotShort) {
		t.Fatalf("spot 賣超過持倉應被拒絕，得到 %v", err)
	}
}

func TestLeverageCannotChangeWhileHoldingPosition(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(1, 10))

	if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, futuresBuy(1, 20)); !errors.Is(err, ErrLeverageLocked) {
		t.Fatalf("加倉時改槓桿應被拒絕，得到 %v", err)
	}
	if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, futuresBuy(1, 10)); err != nil {
		t.Fatalf("維持相同槓桿應可加倉: %v", err)
	}
}

func TestStopLevelsMustBeOnTheCorrectSide(t *testing.T) {
	f := newFixture(t, 10000, 100)

	bad := futuresBuy(1, 10)
	bad.StopLoss = 110 // 多單的停損高於現價，掛上去就會立刻觸發
	if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, bad); !errors.Is(err, ErrInvalidStopLoss) {
		t.Fatalf("方向錯誤的停損應被拒絕，得到 %v", err)
	}

	bad = futuresBuy(1, 10)
	bad.TakeProfit = 90
	if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, bad); !errors.Is(err, ErrInvalidStopLoss) {
		t.Fatalf("方向錯誤的停利應被拒絕，得到 %v", err)
	}

	good := futuresBuy(1, 10)
	good.StopLoss, good.TakeProfit = 90, 120
	f.place(t, good)

	positions, _ := f.svc.Positions(context.Background(), f.accountID, "")
	if !near(positions[0].StopLoss, 90) || !near(positions[0].TakeProfit, 120) {
		t.Fatalf("停損停利應寫進部位: %+v", positions[0])
	}
}

func TestDisabledAccountCannotTrade(t *testing.T) {
	f := newFixture(t, 10000, 100)

	account, _ := f.store.AccountByID(context.Background(), f.accountID)
	account.Status = database.AccountDisabled
	if err := f.store.SaveAccount(context.Background(), account); err != nil {
		t.Fatalf("停用帳號: %v", err)
	}

	if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, futuresBuy(1, 10)); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("停用的帳號不該能下單，得到 %v", err)
	}
}

func TestValidationRejectsBadInput(t *testing.T) {
	f := newFixture(t, 10000, 100)

	cases := []struct {
		name    string
		input   PlaceOrderInput
		wantErr error
	}{
		{"數量為零", PlaceOrderInput{Symbol: "BTCUSDT", Side: database.SideBuy, Quantity: 0}, ErrInvalidQuantity},
		{"數量為負", PlaceOrderInput{Symbol: "BTCUSDT", Side: database.SideBuy, Quantity: -1}, ErrInvalidQuantity},
		{"未知的 side", PlaceOrderInput{Symbol: "BTCUSDT", Side: "hold", Quantity: 1}, ErrInvalidSide},
		{"未知的 product", PlaceOrderInput{Symbol: "BTCUSDT", Product: "option", Side: database.SideBuy, Quantity: 1}, ErrInvalidProduct},
		{"未知的 type", PlaceOrderInput{Symbol: "BTCUSDT", Side: database.SideBuy, Type: "stop", Quantity: 1}, ErrInvalidType},
		{"槓桿過高", PlaceOrderInput{Symbol: "BTCUSDT", Side: database.SideBuy, Quantity: 1, Leverage: 101}, ErrInvalidLeverage},
		{"限價單缺價格", PlaceOrderInput{Symbol: "BTCUSDT", Side: database.SideBuy, Type: database.OrderLimit, Quantity: 1}, ErrInvalidPrice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.PlaceOrder(context.Background(), f.accountID, tc.input); !errors.Is(err, tc.wantErr) {
				t.Fatalf("預期 %v，得到 %v", tc.wantErr, err)
			}
		})
	}
}
