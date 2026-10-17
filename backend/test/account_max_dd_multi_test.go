package test

import (
	"context"
	"math"
	"net/http"
	"testing"

	"backend/internal/database"
	"backend/internal/trading"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// placeOrderRaw 下任意 symbol/product 的市價單，供多標的 max_dd 驗收使用。
func placeOrderRaw(t *testing.T, a *tradingAPI, token, symbol, product, side string, quantity, leverage float64) *orderView {
	t.Helper()
	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": symbol, "product": product,
		"side": side, "type": "market", "quantity": quantity, "leverage": leverage,
	})
	if status != http.StatusCreated {
		t.Fatalf("place %s %s %s: status=%d body=%s", product, side, symbol, status, raw)
	}
	return decodeJSON[*orderView](t, raw)
}

// setStopLoss 以 API 為部位設定停損。
func setStopLoss(t *testing.T, a *tradingAPI, token, positionID string, level float64) {
	t.Helper()
	status, raw := a.request(t, http.MethodPatch, "/v1/positions/"+positionID, token, map[string]any{"stop_loss": level})
	if status != http.StatusOK {
		t.Fatalf("set stop_loss: status=%d body=%s", status, raw)
	}
}

// countMaxDDFills 數 max_dd 觸發的 fill ledger（orderID 為空時數全部）。
func countMaxDDFills(t *testing.T, a *tradingAPI, token, orderID string) int {
	t.Helper()
	n := 0
	for _, e := range a.ledger(t, token, "?limit=200") {
		if e.Event == "fill" && e.Trigger == trading.TriggerMaxDD && (orderID == "" || e.OrderID == orderID) {
			n++
		}
	}
	return n
}

const secondMaxDDCloseFaultSQL = "CREATE TRIGGER fail_second_maxdd_close BEFORE INSERT ON trades FOR EACH ROW WHEN (SELECT count(*) FROM ledger_entries WHERE event = 'fill' AND \"trigger\" = 'max_dd' AND account_id = NEW.account_id) >= 1 BEGIN SELECT RAISE(ABORT, 'injected second max_dd close failure'); END"

// injectSecondMaxDDCloseFault 以第二個連線裝上 SQLite trigger：同一帳號第二筆 max_dd
// 平倉的 Trade 寫入失敗，整筆平倉（Order/Trade/Ledger/Position/Balance）全部 rollback。
// 回傳移除故障的函式；供 E4 驗收（部分清倉失敗隔離與重試）使用。
func injectSecondMaxDDCloseFault(t *testing.T, a *tradingAPI) func() {
	t.Helper()
	fault, err := gorm.Open(sqlite.Open(a.dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open fault db: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := fault.DB()
		_ = sqlDB.Close()
	})
	if err := fault.Exec(secondMaxDDCloseFaultSQL).Error; err != nil {
		t.Fatalf("create fault trigger: %v", err)
	}
	return func() {
		t.Helper()
		if err := fault.Exec("DROP TRIGGER fail_second_maxdd_close").Error; err != nil {
			t.Fatalf("drop fault trigger: %v", err)
		}
	}
}

// TestAccountMaxDDLiquidatesAllPositionsAcrossProducts 是 B5：同一帳號同時有
// spot 多單（浮盈）與 futures 空單（浮虧）、都無 stops，以全戶 equity 判斷：
// 浮盈 300 + 浮虧 1300、equity 9000 時觸發；兩個部位（含盈利的）都全平，
// 各自一筆 Order/Trade/fill Ledger，Trigger=max_dd。
func TestAccountMaxDDLiquidatesAllPositionsAcrossProducts(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-all", 10000, 10) // floor 9000

	a.setPrice(t, 1000)
	x := placeOrderRaw(t, a, token, "BTCUSDT", database.ProductSpot, "buy", 3, 1)
	a.setPrice(t, 900)
	y := placeOrderRaw(t, a, token, "ETHUSDT", database.ProductFutures, "sell", 6.5, 10)
	if x.PositionID == "" || y.PositionID == "" {
		t.Fatalf("open responses missing position_id: %+v %+v", x, y)
	}

	// 浮盈 300（spot long）+ 浮虧 1300（futures short）→ equity = 9000 == floor → 全戶觸發
	a.setPrice(t, 1100)
	r := a.engine.Tick(context.Background())
	if r.Closed != 2 || r.Filled != 0 || r.Errors != 0 {
		t.Fatalf("tick: %+v, want Closed=2 Errors=0", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("equity 9000 == floor 9000 應觸發 max_dd 停止")
	}
	if acc.Balance != 9000 {
		t.Fatalf("final balance = %v, want 9000（零費率）", acc.Balance)
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0（全部位全平）", got)
	}

	closes := 0
	for _, o := range a.allOrders(t, accID) {
		if o.Trigger != trading.TriggerMaxDD {
			continue
		}
		closes++
		if o.Status != database.OrderFilled || !o.ReduceOnly || o.Fee != 0 || o.AvgFillPrice != 1100 || o.Source != (database.Source{}) {
			t.Fatalf("close order = %+v", o)
		}
	}
	if closes != 2 {
		t.Fatalf("max_dd close orders = %d, want 2（每個部位各一筆）", closes)
	}
	if got := len(a.allTrades(t, accID)); got != 4 {
		t.Fatalf("trades = %d, want 4（2 開倉 + 2 平倉）", got)
	}

	seenX, seenY, triggered := false, false, 0
	for _, e := range a.ledger(t, token, "?limit=200") {
		switch e.Event {
		case "max_dd_triggered":
			triggered++
			if e.BalanceAfter != 9000 {
				t.Fatalf("trigger ledger balance_after = %v, want 9000（觸發時 equity）", e.BalanceAfter)
			}
		case "fill":
			if e.Trigger != trading.TriggerMaxDD {
				continue
			}
			if e.Source != nil {
				t.Fatalf("max_dd fill 不假借 stop source：%+v", e)
			}
			switch e.Quantity {
			case 3:
				seenX = e.PositionID == x.PositionID
				if e.RealizedPnL != 300 {
					t.Fatalf("盈利部位的 max_dd 平倉 realized = %v, want 300（盈利部位也全平）", e.RealizedPnL)
				}
			case 6.5:
				seenY = e.PositionID == y.PositionID
				if e.RealizedPnL != -1300 {
					t.Fatalf("浮虧部位的 max_dd 平倉 realized = %v, want -1300", e.RealizedPnL)
				}
			}
		}
	}
	if !seenX || !seenY {
		t.Fatalf("fills: spot long seen=%v futures short seen=%v，兩者都必須被平", seenX, seenY)
	}
	if triggered != 1 {
		t.Fatalf("max_dd_triggered = %d, want 1", triggered)
	}
}

// TestAccountMaxDDLiquidationRollsBackFailedPositionOnly 是 E4：停止與撤單先 commit
// 後，第二個部位的平倉 Trade 寫入失敗 → 只有那筆平倉 rollback（Order/Trade/Ledger/
// Position/Balance 全部恢復）；第一筆平倉保留。移除故障重試只平剩下的部位，
// 不重複第一筆的 fee。
func TestAccountMaxDDLiquidationRollsBackFailedPositionOnly(t *testing.T) {
	a := newTradingAPI(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-rollback", 10000, 10) // floor 9000

	a.setPrice(t, 1000)
	x := placeOrderRaw(t, a, token, "BTCUSDT", database.ProductSpot, "buy", 5, 1) // fee 2 → 9998
	a.setPrice(t, 900)
	y := placeOrderRaw(t, a, token, "ETHUSDT", database.ProductFutures, "sell", 15, 10) // fee 5.4 → 9992.6
	if x.PositionID == "" || y.PositionID == "" {
		t.Fatalf("open responses missing position_id: %+v %+v", x, y)
	}

	drop := injectSecondMaxDDCloseFault(t, a)

	// equity = 9992.6 - 250 - 750 = 8992.6 ≤ 9000
	a.setPrice(t, 950)
	r := a.engine.Tick(context.Background())
	if r.Closed != 1 || r.Errors != 1 {
		t.Fatalf("tick with second-close fault: %+v, want Closed=1 Errors=1", r)
	}

	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("停止必須先於清倉 commit（phase 1 已落盤）")
	}
	if got := len(a.allPositions(t, accID)); got != 1 {
		t.Fatalf("position count = %d, want 1（失敗的平倉要整筆 rollback）", got)
	}

	var preservedOrderID string
	for _, o := range a.allOrders(t, accID) {
		if o.Trigger == trading.TriggerMaxDD {
			preservedOrderID = o.ID
			if o.Status != database.OrderFilled || o.AvgFillPrice != 950 || math.Abs(o.Fee-5*950*0.0004) > 1e-12 {
				t.Fatalf("保留的平倉單 = %+v", o)
			}
		}
	}
	if preservedOrderID == "" {
		t.Fatal("應保留一筆成功的 max_dd 平倉單")
	}
	if got := len(a.allTrades(t, accID)); got != 3 {
		t.Fatalf("trades = %d, want 3（2 開倉 + 1 成功平倉；失敗的沒有 Trade）", got)
	}
	if n := countMaxDDFills(t, a, token, ""); n != 1 {
		t.Fatalf("max_dd fills = %d, want 1（失敗的平倉不留 Ledger）", n)
	}
	want := 9992.6 - 250 - 5*950*0.0004 // 9740.7
	if math.Abs(acc.Balance-want) > 1e-8 {
		t.Fatalf("balance = %v, want %v（只扣成功平倉的 pnl 與 fee）", acc.Balance, want)
	}
	positions := a.allPositions(t, accID)
	if positions[0].ID != y.PositionID || positions[0].Quantity != 15 || positions[0].Margin != 1350 {
		t.Fatalf("rollback 後的部位 = %+v，應完整保留", positions[0])
	}

	drop()
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("retry tick: %+v, want Closed=1 Errors=0", r)
	}
	acc = getAccount(t, a, accID)
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0（重試平掉剩下的部位）", got)
	}
	want = want - 750 - 15*950*0.0004 // 8985.0
	if math.Abs(acc.Balance-want) > 1e-8 {
		t.Fatalf("balance after retry = %v, want %v（不重複扣第一筆 fee）", acc.Balance, want)
	}
	if n := countMaxDDFills(t, a, token, ""); n != 2 {
		t.Fatalf("max_dd fills after retry = %d, want 2", n)
	}
	if n := countMaxDDFills(t, a, token, preservedOrderID); n != 1 {
		t.Fatalf("第一筆平倉的 fills = %d, want 1（重試不重複成交）", n)
	}
	if got := len(a.allTrades(t, accID)); got != 4 {
		t.Fatalf("trades after retry = %d, want 4", got)
	}
}

// TestAccountMaxDDLiquidationRetriesAfterPriceRestored 是 E2＋B9：停止 marker 落盤後
// 其中一個標的缺價 → 該輪不成交也不清 marker；行情恢復（價格回升）後只平剩餘部位，
// 不再次扣已完成的 fee；marker 不自動解除，普通下單仍被拒絕。
// 前提：「marker 已落盤 + 部位仍在 + 該標的缺價」這個狀態必須先由 E4 式的平倉失敗
// 製造（缺價本身依 E1 設計永遠不會讓停止 commit）。
func TestAccountMaxDDLiquidationRetriesAfterPriceRestored(t *testing.T) {
	a := newTradingAPI(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-retry", 10000, 10) // floor 9000

	a.setPrice(t, 1000)
	placeOrderRaw(t, a, token, "BTCUSDT", database.ProductSpot, "buy", 5, 1) // fee 2 → 9998
	a.setPrice(t, 900)
	y := placeOrderRaw(t, a, token, "ETHUSDT", database.ProductFutures, "sell", 15, 10) // fee 5.4 → 9492.6
	if y.PositionID == "" {
		t.Fatalf("open response missing position_id: %+v", y)
	}

	drop := injectSecondMaxDDCloseFault(t, a)
	a.setPrice(t, 950)
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 1 {
		t.Fatalf("trigger tick: %+v, want Closed=1 Errors=1", r)
	}
	if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt == nil {
		t.Fatal("應已建立 marker（停止先 commit）")
	}

	// 故障只模擬「第二筆平倉當輪失敗」；移除後行情恢復的那輪才能成功
	drop()

	// 已停止後 ETHUSDT 缺價：該輪什麼都不做，marker 保留
	a.dropSymbolPrice(t, "ETHUSDT")
	if r := a.engine.Tick(context.Background()); r.Closed != 0 || r.Errors != 0 {
		t.Fatalf("tick with missing price: %+v, want Closed=0 Errors=0", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("缺價不得清除 marker")
	}
	positions := a.allPositions(t, accID)
	if len(positions) != 1 || positions[0].ID != y.PositionID {
		t.Fatalf("缺價輪部位 = %+v, want 剩餘部位保留", positions)
	}
	balanceAfterStop := acc.Balance

	// 行情回升到 1000：只平剩餘部位，marker 不自動解除
	a.restoreSymbolPrice(t, "ETHUSDT", 1000)
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("recovery tick: %+v, want Closed=1 Errors=0", r)
	}
	acc = getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("B9：行情回升不得自動解除 marker")
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0（補價後只平剩餘部位）", got)
	}
	want := balanceAfterStop + (900-1000)*15 - 15*1000*0.0004 // 7734.7
	if math.Abs(acc.Balance-want) > 1e-8 {
		t.Fatalf("balance = %v, want %v（不重複扣已完成的 fee）", acc.Balance, want)
	}
	if n := countMaxDDFills(t, a, token, ""); n != 2 {
		t.Fatalf("max_dd fills = %d, want 2（不重複平倉）", n)
	}

	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	})
	if status != http.StatusConflict {
		t.Fatalf("已停止後下單: status=%d body=%s, want 409 max_dd_stopped", status, raw)
	}
}

// TestAccountMaxDDLiquidationBeatsStopLossRace 是 B8：同一輪既達戶頭 max_dd 門檻又
// 觸發停損時，戶頭風控先處理；同一個 position 只被平一次，Trigger=max_dd、
// 不假借 stop source；SL/TP 側不會重複平或計錯。
func TestAccountMaxDDLiquidationBeatsStopLossRace(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-race", 10000, 10) // floor 9000

	a.setPrice(t, 1000)
	x := placeOrderRaw(t, a, token, "BTCUSDT", database.ProductFutures, "buy", 10, 10)
	if x.PositionID == "" {
		t.Fatalf("open response missing position_id: %+v", x)
	}
	setStopLoss(t, a, token, x.PositionID, 950)

	// 價格 900：equity = 10000 - 1000 = 9000 達 max_dd 門檻，停損 950 同一輪也命中
	a.setPrice(t, 900)
	r := a.engine.Tick(context.Background())
	if r.Closed != 1 || r.Filled != 0 || r.Errors != 0 {
		t.Fatalf("tick: %+v, want Closed=1 Errors=0（不重複平）", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("應觸發 max_dd 停止")
	}
	if acc.Balance != 9000 {
		t.Fatalf("balance = %v, want 9000（零費率）", acc.Balance)
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0", got)
	}

	maxddCloses, stopCloses := 0, 0
	for _, o := range a.allOrders(t, accID) {
		switch o.Trigger {
		case trading.TriggerMaxDD:
			maxddCloses++
			if o.Source != (database.Source{}) {
				t.Fatalf("max_dd 平倉不假借 stop source：%+v", o)
			}
		case trading.TriggerStopLoss:
			stopCloses++
		}
	}
	if maxddCloses != 1 || stopCloses != 0 {
		t.Fatalf("close orders: max_dd=%d stop_loss=%d, want 1/0（戶頭風控先處理）", maxddCloses, stopCloses)
	}
}
