package test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"backend/internal/database"
)

// 票10 AC2：取價期間出現新 symbol 的部位時，鎖內（transaction 內）重讀 positions
// 若缺該 symbol 的 mark，必須 fail-closed 拒絕新增風險，不能用舊 snapshot 放行。
// SQLite 單連線下並行寫無法自然交錯，改用 trigger 在 order 交易的 fill ledger 寫入時
// 決定性地插入一個取價 snapshot 裡不存在的新 symbol 部位，模擬「取價後才出現的新部位」；
// 真正的多連線並行版本見 F6（ticket 11，需 BACKEND_TEST_POSTGRES_DSN）。
func TestAccountMaxDDStaleMarksRejectedInsideLock(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "stale-marks", 10000, 10)
	a.setPrice(t, 1000)
	setup := placeOrderRaw(t, a, token, "BTCUSDT", database.ProductFutures, "buy", 10, 10)

	faultExec(t, a, "CREATE TRIGGER ghost_new_symbol BEFORE INSERT ON ledger_entries FOR EACH ROW WHEN NEW.event = 'fill' BEGIN INSERT INTO positions (id, account_id, market, symbol, product, side, quantity, entry_price, leverage, margin, stop_loss, take_profit, created_at, updated_at) VALUES ('pos_ghost_stale', NEW.account_id, 'crypto', 'ETHUSDT', 'futures', 'long', 10, 900, 10, 900, 0, 0, NEW.created_at, NEW.created_at); END")

	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	})
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "risk_price_unavailable") {
		t.Fatalf("鎖內缺 mark 的新 symbol 不能以舊 snapshot 放行: status=%d body=%s", status, raw)
	}
	positions := a.allPositions(t, accID)
	if len(positions) != 1 || positions[0].Quantity != 10 {
		t.Fatalf("不得新增或變動部位: %+v", positions)
	}
	if acc := getAccount(t, a, accID); acc.Balance != 10000 {
		t.Fatalf("餘額不得變動: %v", acc.Balance)
	}
	orders := a.allOrders(t, accID)
	if len(orders) != 1 || orders[0].ID != setup.ID {
		t.Fatalf("失敗的訂單必須隨交易 rollback: %+v", orders)
	}

	faultExec(t, a, "DROP TRIGGER ghost_new_symbol")
	order := placeOrderRaw(t, a, token, "BTCUSDT", database.ProductFutures, "buy", 1, 10)
	if order.PositionID == "" {
		t.Fatal("狀態恢復後同一筆訂單必須能成交")
	}
}

// 票10 AC3：尚未 stopped 的手動 reduce-only 平倉，本標的有價即可降低風險（不要求
// 其他標的齊價，postcheck 留到下一輪 Tick）；實際 DB 失敗不能吞掉。
func TestAccountMaxDDManualReduceOnlyWithOtherQuoteMissing(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "reduce-missing", 10000, 10)
	a.setPrice(t, 1000)
	x := placeOrderRaw(t, a, token, "BTCUSDT", database.ProductFutures, "buy", 10, 10)
	placeOrderRaw(t, a, token, "ETHUSDT", database.ProductFutures, "buy", 10, 10)

	// DB 失敗不能吞掉：平倉的 ledger 寫入失敗 → 整筆 rollback 並回傳錯誤。
	faultExec(t, a, "CREATE TRIGGER fail_close_ledger BEFORE INSERT ON ledger_entries FOR EACH ROW WHEN NEW.event = 'fill' BEGIN SELECT RAISE(ABORT, 'injected close ledger failure'); END")
	status, raw := a.request(t, http.MethodPost, "/v1/positions/"+x.PositionID+"/close", token, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("DB 失敗的平倉不能吞掉: status=%d body=%s", status, raw)
	}
	if got := len(a.allPositions(t, accID)); got != 2 {
		t.Fatalf("平倉失敗後兩個部位都必須保留: %d", got)
	}
	if acc := getAccount(t, a, accID); acc.Balance != 10000 {
		t.Fatalf("平倉失敗後餘額不得變動: %v", acc.Balance)
	}
	faultExec(t, a, "DROP TRIGGER fail_close_ledger")

	// 其他標的缺價、本標的有價：reduce-only 平倉照常降低風險。
	a.dropSymbolPrice(t, "ETHUSDT")
	status, raw = a.request(t, http.MethodPost, "/v1/positions/"+x.PositionID+"/close", token, nil)
	if status != http.StatusOK {
		t.Fatalf("本標的有價的 reduce-only 平倉不得因其他標的缺價被擋: status=%d body=%s", status, raw)
	}
	if got := len(a.allPositions(t, accID)); got != 1 {
		t.Fatalf("只剩 ETH 部位: %d", got)
	}

	// 缺價標的恢復後由 Tick 補做延遲的風險評估：equity 達門檻 → 停止並平掉剩餘部位。
	a.restoreSymbolPrice(t, "ETHUSDT", 900)
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("延遲 Tick 評估: %+v, want Closed=1 Errors=0", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("Tick 必須完成評估並停止")
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("剩餘部位必須平掉: %d", got)
	}
	approx(t, "最終餘額", acc.Balance, 9000)
}

// 票10 AC4 / 驗收 E2 / E6：已 stopped 時先平有價部位、缺價部位留待恢復；恢復時跳價
// 照實結算（最終 Balance 可以低於觸發線），報告必須區分 trigger equity 與最終 balance。
func TestAccountMaxDDStoppedClosesPricedPositionsFirst(t *testing.T) {
	a := newTradingAPI(t, "")
	accID, token := createMaxDDAccount(t, a, "stopped-first", 10000, 10)
	a.setPrice(t, 1000)
	placeOrderRaw(t, a, token, "BTCUSDT", database.ProductSpot, "buy", 5, 1)
	a.setPrice(t, 900)
	y := placeOrderRaw(t, a, token, "ETHUSDT", database.ProductFutures, "sell", 15, 10)
	if y.PositionID == "" {
		t.Fatal("ETH 部位未建立")
	}

	// 觸發輪：marker 落盤後兩筆清倉皆失敗 → marker + 兩個剩餘部位。
	faultExec(t, a, "CREATE TRIGGER fail_all_maxdd_close BEFORE INSERT ON ledger_entries FOR EACH ROW WHEN NEW.event = 'fill' AND NEW.trigger = 'max_dd' BEGIN SELECT RAISE(ABORT, 'injected max_dd close failure'); END")
	a.setPrice(t, 950)
	if r := a.engine.Tick(context.Background()); r.Closed != 0 || r.Errors != 1 {
		t.Fatalf("觸發輪（marker 落盤、兩筆清倉皆失敗）: %+v, want Closed=0 Errors=1", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("marker 必須先落盤")
	}
	if got := len(a.allPositions(t, accID)); got != 2 {
		t.Fatalf("兩個部位都應保留: %d", got)
	}
	approx(t, "marker 輪餘額", acc.Balance, 9992.6)
	faultExec(t, a, "DROP TRIGGER fail_all_maxdd_close")

	// 一個標的缺價：有價的先平，缺價的留待下輪。
	a.dropSymbolPrice(t, "ETHUSDT")
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("有價部位先平: %+v, want Closed=1 Errors=0", r)
	}
	acc = getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("marker 不得被清除")
	}
	positions := a.allPositions(t, accID)
	if len(positions) != 1 || positions[0].ID != y.PositionID {
		t.Fatalf("只剩缺價的 ETH 部位: %+v", positions)
	}
	approx(t, "平 BTC 後餘額", acc.Balance, 9740.7)

	// 仍有部位時 resume 必須被拒。
	if status, raw := resumeTrading(t, a, accID, tradingUserToken, nil); status != http.StatusConflict {
		t.Fatalf("有剩餘部位時 resume 必須 409: status=%d body=%s", status, raw)
	}

	// 恢復時跳價（950→1000）：照實結算，最終 Balance 低於觸發線。
	a.restoreSymbolPrice(t, "ETHUSDT", 1000)
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("恢復輪: %+v, want Closed=1 Errors=0", r)
	}
	acc = getAccount(t, a, accID)
	approx(t, "最終餘額（跳價結算、低於觸發線）", acc.Balance, 8234.7)
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("部位 = %d", got)
	}
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("marker 保留到 resume")
	}
	if n := countMaxDDFills(t, a, token, ""); n != 2 {
		t.Fatalf("max_dd fills = %d, want 2", n)
	}

	// 報告必須區分 trigger equity（8992.6）與最終 balance（8234.7）。
	var triggerBalance float64
	found := false
	for _, e := range a.ledger(t, token, "?limit=200") {
		if e.Event == "max_dd_triggered" {
			triggerBalance, found = e.BalanceAfter, true
		}
	}
	if !found {
		t.Fatal("缺 max_dd_triggered ledger")
	}
	approx(t, "trigger equity", triggerBalance, 8992.6)
	if triggerBalance == 8234.7 {
		t.Fatal("trigger equity 與最終 balance 必須可區分")
	}
}
