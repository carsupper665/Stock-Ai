package trading

import (
	"context"
	"errors"
	"testing"
)

func TestSetStops(t *testing.T) {
	f := newFixture(t, 10000, 100)
	f.place(t, futuresBuy(1, 10))
	pos := f.onlyPosition(t)
	ctx := context.Background()
	ptr := func(v float64) *float64 { return &v }

	updated, err := f.svc.SetStops(ctx, f.accountID, pos.ID, StopInput{StopLoss: ptr(90), TakeProfit: ptr(120)})
	if err != nil {
		t.Fatalf("設定停損停利: %v", err)
	}
	if !near(updated.StopLoss, 90) || !near(updated.TakeProfit, 120) {
		t.Fatalf("設定結果不符: %+v", updated)
	}

	updated, err = f.svc.SetStops(ctx, f.accountID, pos.ID, StopInput{TakeProfit: ptr(130)})
	if err != nil {
		t.Fatalf("只更新停利: %v", err)
	}
	if !near(updated.StopLoss, 90) || !near(updated.TakeProfit, 130) {
		t.Fatalf("只給 take_profit 不該動到 stop_loss: %+v", updated)
	}

	updated, err = f.svc.SetStops(ctx, f.accountID, pos.ID, StopInput{StopLoss: ptr(0)})
	if err != nil {
		t.Fatalf("移除停損: %v", err)
	}
	if updated.StopLoss != 0 || !near(updated.TakeProfit, 130) {
		t.Fatalf("傳 0 應只移除停損: %+v", updated)
	}

	if _, err := f.svc.SetStops(ctx, f.accountID, pos.ID, StopInput{StopLoss: ptr(110)}); !errors.Is(err, ErrInvalidStopLoss) {
		t.Fatalf("多單停損高於現價應被拒絕，得到 %v", err)
	}
	if _, err := f.svc.SetStops(ctx, f.accountID, pos.ID, StopInput{}); !errors.Is(err, ErrNoStopChange) {
		t.Fatalf("什麼都沒給應回 ErrNoStopChange，得到 %v", err)
	}
	if _, err := f.svc.SetStops(ctx, f.accountID, "pos_missing", StopInput{StopLoss: ptr(90)}); !errors.Is(err, ErrPositionClosed) {
		t.Fatalf("不存在的部位應回 ErrPositionClosed，得到 %v", err)
	}
}
