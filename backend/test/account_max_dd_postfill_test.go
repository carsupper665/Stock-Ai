package test

import (
	"context"
	"net/http"
	"testing"

	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// blockMarkerWrite 以第二個連線建立 trigger，阻擋 max_dd_stopped_at 由 null
// 改寫為非 null 的 UPDATE（AC3：postcheck 的 marker 寫入失敗注入）。
func blockMarkerWrite(t *testing.T, a *tradingAPI) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(a.dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := conn.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	sql := "CREATE TRIGGER fail_postfill_mark BEFORE UPDATE ON accounts WHEN OLD.max_dd_stopped_at IS NULL AND NEW.max_dd_stopped_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'injected postfill mark failure'); END"
	if err := conn.WithContext(context.Background()).Exec(sql).Error; err != nil {
		t.Fatalf("create fault trigger: %v", err)
	}
}

// unblockMarkerWrite 移除故障 trigger，讓後續合法停止可以正常提交。
func unblockMarkerWrite(t *testing.T, a *tradingAPI) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(a.dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	defer func() {
		if sqlDB, dbErr := conn.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	}()
	if err := conn.WithContext(context.Background()).Exec("DROP TRIGGER fail_postfill_mark").Error; err != nil {
		t.Fatalf("drop fault trigger: %v", err)
	}
}

// TestAccountMaxDDPostFillKeepsFillAndStopsAccount 是 AC1＋AC4（C4）：合法
// 成交扣費後越線，成交結果保留、marker 同 transaction 提交；之後新單 409，
// Engine 下一輪完成清倉。
func TestAccountMaxDDPostFillKeepsFillAndStopsAccount(t *testing.T) {
	a := newTradingAPIFee(t, "", 0.0002, 0.001)
	accID, token := createMaxDDAccount(t, a, "post-fill", 10000, 10)
	a.setPrice(t, 1000)
	setBalance(t, a, accID, 9000.5) // floor = 9000：9000.5 仍在門檻上方

	// 買 1 @1000、taker fee = 1x1000x0.001 = 1 → equity = 8999.5 <= 9000。
	ov := placeMarketOrder(t, a, token, 1, 1)
	if ov.Status != database.OrderFilled {
		t.Fatalf("真實成交必須保留（不能回暗示未成交的拒絕）：status=%s", ov.Status)
	}
	approx(t, "fill price", ov.AvgFillPrice, 1000)
	approx(t, "taker fee", ov.Fee, 1)

	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatalf("停止標記必須與成交同 transaction 提交")
	}
	approx(t, "post-fill balance", acc.Balance, 8999.5)

	fills, triggered := 0, 0
	var triggerEquity float64
	for _, e := range a.ledger(t, token, "?limit=200") {
		switch e.Event {
		case "fill":
			fills++
			if e.OrderID != ov.ID {
				t.Fatalf("fill ledger 應屬本次成交單：%s", e.OrderID)
			}
		case "max_dd_triggered":
			triggered++
			triggerEquity = e.BalanceAfter
		}
	}
	if fills != 1 || triggered != 1 {
		t.Fatalf("fill=%d triggered=%d，want 1/1", fills, triggered)
	}
	approx(t, "trigger 記錄的 equity", triggerEquity, 8999.5)

	if n := len(a.openOrders(t, accID)); n != 0 {
		t.Fatalf("不應殘留 open order：%d 張", n)
	}
	positions, err := a.store.ListPositions(context.Background(), accID, "")
	if err != nil || len(positions) != 1 {
		t.Fatalf("成交部位必須保留：len=%d err=%v", len(positions), err)
	}

	// 停止後不得再下單。
	status, code := tryPlaceOrder(t, a, token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 1,
	})
	if status != http.StatusConflict || code != "max_dd_stopped" {
		t.Fatalf("停止後新單 = %d/%s，want 409/max_dd_stopped", status, code)
	}

	// Engine 完成清倉。
	res := a.engine.Tick(context.Background())
	if res.Closed != 1 || res.Filled != 0 || res.Errors != 0 {
		t.Fatalf("清倉 tick： %+v", res)
	}
	positions, err = a.store.ListPositions(context.Background(), accID, "")
	if err != nil || len(positions) != 0 {
		t.Fatalf("清倉後不應有部位：len=%d err=%v", len(positions), err)
	}
	// 平倉 fee = 1x1000x0.001 = 1 → 8999.5 - 1。
	approx(t, "最終 balance", getAccount(t, a, accID).Balance, 8998.5)
}

// TestAccountMaxDDPostFillDoesNotCancelFilledOrder 是 AC2：先保存本單成
// filled，postcheck 的全戶撤單只撤其他 open order，不能誤撤本次成交單。
func TestAccountMaxDDPostFillDoesNotCancelFilledOrder(t *testing.T) {
	a := newTradingAPIFee(t, "", 0.0002, 0.001)
	accID, token := createMaxDDAccount(t, a, "post-fill-orders", 10000, 10)
	a.setPrice(t, 1000)
	setBalance(t, a, accID, 9000.5)

	limitID := placeLimitOrder(t, a, token, "sell", 990, nil)
	ov := placeMarketOrder(t, a, token, 1, 1) // fee 1 → equity 8999.5 越線

	limitOrder, err := a.store.OrderByID(context.Background(), accID, limitID)
	if err != nil {
		t.Fatalf("load limit order: %v", err)
	}
	if limitOrder.Status != database.OrderCanceled {
		t.Fatalf("其他 open order 應被撤銷：status=%s", limitOrder.Status)
	}
	if ov.Status != database.OrderFilled {
		t.Fatalf("本次成交單不能被誤撤：status=%s", ov.Status)
	}

	canceled, filled, triggered := 0, 0, 0
	var canceledID string
	for _, e := range a.ledger(t, token, "?limit=200") {
		switch e.Event {
		case "order_canceled":
			canceled++
			canceledID = e.OrderID
		case "fill":
			filled++
		case "max_dd_triggered":
			triggered++
		}
	}
	if canceled != 1 || canceledID != limitID {
		t.Fatalf("order_canceled=%d（%s），want 1 張且為舊掛單 %s", canceled, canceledID, limitID)
	}
	if filled != 1 || triggered != 1 {
		t.Fatalf("fill=%d triggered=%d，want 1/1", filled, triggered)
	}
	if getAccount(t, a, accID).MaxDDStoppedAt == nil {
		t.Fatalf("停止標記必須提交")
	}
}

// TestAccountMaxDDLimitFillPostCheckStopsAndLiquidatesSameTick 是本輪末清倉
// 接線：限價單在 Engine 本輪成交、maker 費用使 equity 越線時，postcheck 在
// 同一交易提交停止，Engine 同一輪完成清倉（不是等到下一輪）。
func TestAccountMaxDDLimitFillPostCheckStopsAndLiquidatesSameTick(t *testing.T) {
	a := newTradingAPI(t, "")                                              // maker 0.0002
	accID, token := createMaxDDAccount(t, a, "post-fill-limit", 10000, 10) // floor 9000
	a.setPrice(t, 1000)
	setBalance(t, a, accID, 9000.2) // 距離 floor 只差 0.2

	limitID := placeLimitOrder(t, a, token, "buy", 1000, nil)
	a.setPrice(t, 950) // 現價 950 <= 1000：限價觸發，以 1000 成交

	// 成交 fee = 1x1000x0.0002 = 0.2 → balance 9000.0；浮虧 = 1x(950-1000) = -50
	// → equity 8950 <= 9000：postcheck 停止，同一輪清理清倉 @950
	//（realized -50、max_dd 強平用 taker fee = 950x0.0004 = 0.38）→ 最終 8949.62。
	res := a.engine.Tick(context.Background())
	if res.Filled != 1 || res.Closed != 1 || res.Errors != 0 {
		t.Fatalf("tick： %+v，want Filled=1 Closed=1 Errors=0", res)
	}
	if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt == nil {
		t.Fatalf("停止標記必須提交")
	}
	positions, err := a.store.ListPositions(context.Background(), accID, "")
	if err != nil || len(positions) != 0 {
		t.Fatalf("同輪清倉後不應有部位：len=%d err=%v", len(positions), err)
	}
	approx(t, "最終 balance", getAccount(t, a, accID).Balance, 8949.62)

	fills, triggered := 0, 0
	var triggerEquity, maxDDFillPrice float64
	for _, e := range a.ledger(t, token, "?limit=200") {
		switch e.Event {
		case "fill":
			fills++
			if e.Trigger == "max_dd" {
				maxDDFillPrice = e.Price
			}
		case "max_dd_triggered":
			triggered++
			triggerEquity = e.BalanceAfter
		}
	}
	if fills != 2 || triggered != 1 {
		t.Fatalf("fill=%d triggered=%d，want 2/1（限價成交＋max_dd 清倉）", fills, triggered)
	}
	approx(t, "trigger 記錄的 equity", triggerEquity, 8950)
	approx(t, "max_dd 清倉成交價", maxDDFillPrice, 950)
	if n := countLedgerEvent(t, a, token, "order_canceled"); n != 0 {
		t.Fatalf("不應有撤單：%d 筆", n)
	}
	if _, err := a.store.OrderByID(context.Background(), accID, limitID); err != nil {
		t.Fatalf("load limit order: %v", err)
	}
}

// TestAccountMaxDDPostFillDBFailureRollsBackFill 是 AC3：postcheck 的 marker
// 寫入失敗時整筆成交 rollback，不能回成功但缺交易帳務；移除故障後重試，
// 成交保留且只觸發一次停止。
func TestAccountMaxDDPostFillDBFailureRollsBackFill(t *testing.T) {
	a := newTradingAPIFee(t, "", 0.0002, 0.001)
	accID, token := createMaxDDAccount(t, a, "post-fill-fault", 10000, 10)
	a.setPrice(t, 1000)
	setBalance(t, a, accID, 9000.5)

	body := map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 1,
	}

	blockMarkerWrite(t, a)
	status, code := tryPlaceOrder(t, a, token, body)
	if status != http.StatusInternalServerError || code != "internal_error" {
		t.Fatalf("postcheck 失敗必須回 500 錯誤：%d/%s", status, code)
	}

	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt != nil {
		t.Fatalf("rollback 後不得殘留停止標記")
	}
	approx(t, "rollback 後 balance 不變", acc.Balance, 9000.5)
	orders, err := a.store.ListOrders(context.Background(), accID, "", 100)
	if err != nil || len(orders) != 0 {
		t.Fatalf("rollback 後不得保留成交單：len=%d err=%v", len(orders), err)
	}
	if n := countLedgerEvent(t, a, token, "fill"); n != 0 {
		t.Fatalf("不能回成功但缺交易帳務：fill=%d", n)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_triggered"); n != 0 {
		t.Fatalf("rollback 後不應有 max_dd_triggered：%d", n)
	}

	// 移除故障後重試：成交保留，停止只發生一次。
	unblockMarkerWrite(t, a)
	status, _ = tryPlaceOrder(t, a, token, body)
	if status != http.StatusCreated {
		t.Fatalf("移除故障後重試應成功：%d", status)
	}
	acc = getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatalf("重試成交必須與停止標記同 transaction 提交")
	}
	if n := countLedgerEvent(t, a, token, "fill"); n != 1 {
		t.Fatalf("fill=%d want 1", n)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_triggered"); n != 1 {
		t.Fatalf("max_dd_triggered=%d want 1", n)
	}
	approx(t, "重試後 balance", acc.Balance, 8999.5)
}
