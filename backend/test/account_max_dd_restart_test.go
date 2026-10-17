package test

import (
	"context"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// restartWith 關閉目前的 store 並以同一個 DB 檔案重新開（模擬服務重啟），
// 用傳入的構造函式保留原 fixture 的費率模式。
func restartWith(t *testing.T, a *tradingAPI, newAPI func(*testing.T, string) *tradingAPI) *tradingAPI {
	t.Helper()
	if err := a.store.Close(); err != nil {
		t.Fatalf("close store（模擬重啟）: %v", err)
	}
	return newAPI(t, a.dbPath)
}

// faultExec 以第二個連線執行原始 SQL（直接改資料／裝 trigger），供重啟與
// 資料完整性驗收使用。
func faultExec(t *testing.T, a *tradingAPI, sql string, args ...any) {
	t.Helper()
	fault, err := gorm.Open(sqlite.Open(a.dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open fault db: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := fault.DB()
		_ = sqlDB.Close()
	})
	if err := fault.Exec(sql, args...).Error; err != nil {
		t.Fatalf("fault exec: %v", err)
	}
}

// TestAccountMaxDDRestartAfterPartialLiquidation 是 E5 崩潰點 1／2：marker 提交後、
// 首筆平倉提交後關閉並重開 store——仍維持停止、只平剩餘部位、不重複扣已完成的
// fee、不依賴舊 memory queue。
func TestAccountMaxDDRestartAfterPartialLiquidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	a := newTradingAPI(t, path)
	accID, token := createMaxDDAccount(t, a, "restart", 10000, 10) // floor 9000

	a.setPrice(t, 1000)
	placeOrderRaw(t, a, token, "BTCUSDT", database.ProductSpot, "buy", 5, 1) // fee 2 → 9998
	a.setPrice(t, 900)
	y := placeOrderRaw(t, a, token, "ETHUSDT", database.ProductFutures, "sell", 15, 10) // fee 5.4 → 9992.6
	if y.PositionID == "" {
		t.Fatalf("open response missing position_id: %+v", y)
	}

	drop := injectSecondMaxDDCloseFault(t, a)
	a.setPrice(t, 950) // equity = 9992.6 - 250 - 750 = 8992.6 ≤ 9000
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 1 {
		t.Fatalf("trigger tick: %+v, want Closed=1 Errors=1", r)
	}
	if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt == nil {
		t.Fatal("應已落盤 marker")
	}
	if got := len(a.allPositions(t, accID)); got != 1 {
		t.Fatalf("應剩一個部位，got %d", got)
	}

	// 崩潰：marker 與第一筆平倉都已提交，重開後狀態必須完全來自 DB。
	b := restartWith(t, a, newTradingAPI)
	acc := getAccount(t, b, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("重啟後 marker 必須保留（不能依賴 memory queue）")
	}
	if status, raw := b.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	}); status != http.StatusConflict {
		t.Fatalf("重啟後仍停止、下單應 409：status=%d body=%s", status, raw)
	}

	// 只平剩餘部位，不重複扣第一筆 fee。
	drop()
	b.setPrice(t, 950)
	if r := b.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("restart tick: %+v, want Closed=1 Errors=0", r)
	}
	acc = getAccount(t, b, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("重啟補平不得解除 marker")
	}
	if got := len(b.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0", got)
	}
	want := 9992.6 - 250 - 5*950*0.0004 - 750 - 15*950*0.0004 // 8985.0
	if math.Abs(acc.Balance-want) > 1e-8 {
		t.Fatalf("balance = %v, want %v（重啟不重複扣第一筆 fee）", acc.Balance, want)
	}
	if n := countMaxDDFills(t, b, token, ""); n != 2 {
		t.Fatalf("max_dd fills = %d, want 2（不重複平倉）", n)
	}
	if n := countLedgerEvent(t, b, token, "max_dd_triggered"); n != 1 {
		t.Fatalf("max_dd_triggered = %d, want 1", n)
	}
}

// TestAccountMaxDDRestartAfterFullLiquidation 是 E5 崩潰點 3＋D3：全部平完但未
// 解除時重啟仍停止；解除後重啟保留新 baseline 8991 與 floor 7991。
func TestAccountMaxDDRestartAfterFullLiquidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	a := newTradingAPIZeroFee(t, path)
	accID, token := stopAndLiquidate(t, a, "restart-flat")

	b := restartWith(t, a, newTradingAPIZeroFee)
	acc := getAccount(t, b, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("全平未解除重啟後仍須停止")
	}
	if status, raw := b.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	}); status != http.StatusConflict {
		t.Fatalf("重啟後下單應 409：status=%d body=%s", status, raw)
	}

	status, raw := resumeTrading(t, b, accID, tradingUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("resume after restart: status=%d body=%s", status, raw)
	}
	view := decodeView(t, raw)
	approx(t, "重啟後 resume 的 baseline", view["max_dd_baseline_equity"].(float64), 8991)
	approx(t, "重啟後 floor", view["max_dd_equity_floor"].(float64), 7991)
	if view["max_dd_stopped_at"] != nil {
		t.Fatalf("marker 應清除：%v", view["max_dd_stopped_at"])
	}
	placeMarketOrder(t, b, token, 1, 10) // 解除後交易恢復

	// 解除後再重啟：baseline/floor 保留、不回第一輪 9000 門檻。
	c := restartWith(t, b, newTradingAPIZeroFee)
	if status, raw = c.request(t, http.MethodGet, "/v1/accounts/"+accID, tradingUserToken, nil); status != http.StatusOK {
		t.Fatalf("get after second restart: status=%d body=%s", status, raw)
	}
	view = decodeView(t, raw)
	approx(t, "第二次重啟後 baseline 仍 8991", view["max_dd_baseline_equity"].(float64), 8991)
	approx(t, "第二次重啟後 floor 仍 7991", view["max_dd_equity_floor"].(float64), 7991)
	if view["max_dd_stopped_at"] != nil {
		t.Fatalf("第二次重啟後 marker 應仍為 null：%v", view["max_dd_stopped_at"])
	}
}

// TestAccountMaxDDRestartAfterSecondRoundTrigger 是 D3：第二輪觸發後重啟，
// baseline 與 marker 都保留、不回第一輪 9,000 門檻。
func TestAccountMaxDDRestartAfterSecondRoundTrigger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	a := newTradingAPIZeroFee(t, path)
	accID, token := stopAndLiquidate(t, a, "restart-round2")
	if status, raw := resumeTrading(t, a, accID, tradingUserToken, nil); status != http.StatusOK {
		t.Fatalf("resume: status=%d body=%s", status, raw)
	}
	a.setPrice(t, 899.1)
	placeMarketOrder(t, a, token, 10, 10)
	a.setPrice(t, 799.1) // equity = 8991 - 1000 = 7991 <= floor 7991
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("second round tick: %+v", r)
	}
	if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt == nil {
		t.Fatal("第二輪應觸發停止")
	}

	b := restartWith(t, a, newTradingAPIZeroFee)
	acc := getAccount(t, b, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("第二輪觸發後重啟仍須停止")
	}
	if acc.MaxDDBaselineEquity == nil || math.Abs(*acc.MaxDDBaselineEquity-8991) > 1e-8 {
		t.Fatalf("baseline 應保留 8991（不回第一輪 10000）：%v", acc.MaxDDBaselineEquity)
	}
	approx(t, "第二輪全平後 balance 保留", acc.Balance, 7991)
	if status, raw := b.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	}); status != http.StatusConflict {
		t.Fatalf("第二輪停止中下單應 409：status=%d body=%s", status, raw)
	}
}

// TestAccountMaxDDResumeLedgerFailureRollsBack 是 D8：resume 的 Ledger 寫入失敗時
// baseline／marker／Status／Ledger 全部 rollback；再次合法解除只產生一筆事件。
func TestAccountMaxDDResumeLedgerFailureRollsBack(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := stopAndLiquidate(t, a, "resume-ledger")

	faultExec(t, a, "CREATE TRIGGER fail_resumed_ledger BEFORE INSERT ON ledger_entries FOR EACH ROW WHEN NEW.event = 'max_dd_resumed' BEGIN SELECT RAISE(ABORT, 'injected resumed ledger failure'); END")

	if status, raw := resumeTrading(t, a, accID, tradingUserToken, nil); status != http.StatusInternalServerError {
		t.Fatalf("ledger 失敗的 resume 應 500：status=%d body=%s", status, raw)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("resume 失敗不得清 marker（不能留半清標記）")
	}
	if acc.MaxDDBaselineEquity == nil || math.Abs(*acc.MaxDDBaselineEquity-10000) > 1e-8 {
		t.Fatalf("resume 失敗不得移動 baseline：%v", acc.MaxDDBaselineEquity)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 0 {
		t.Fatalf("失敗時不得有 max_dd_resumed：%d", n)
	}

	faultExec(t, a, "DROP TRIGGER fail_resumed_ledger")
	if status, raw := resumeTrading(t, a, accID, tradingUserToken, nil); status != http.StatusOK {
		t.Fatalf("故障移除後 resume 應 200：status=%d body=%s", status, raw)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 1 {
		t.Fatalf("max_dd_resumed = %d, want 1（只產生一筆事件）", n)
	}
}

// TestAccountMaxDDMissingBaselineIsDataError 是 E7：max_dd 非 null 但 baseline
// 缺失是資料錯誤——不可當未設定或自動補 initial，交易要報錯且無 fill；補回
// baseline 後交易恢復。
func TestAccountMaxDDMissingBaselineIsDataError(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "inconsistent", 10000, 10)
	faultExec(t, a, "UPDATE accounts SET max_dd_baseline_equity = NULL WHERE id = ?", accID)

	a.setPrice(t, 1000)
	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	})
	if status != http.StatusConflict || !strings.Contains(string(raw), "max_dd_inconsistent") {
		t.Fatalf("baseline 缺失下單應 409 max_dd_inconsistent：status=%d body=%s", status, raw)
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("不得有 fill/部位：%d", got)
	}
	acc := getAccount(t, a, accID)
	if acc.Balance != 10000 {
		t.Fatalf("balance 不應改變：%v", acc.Balance)
	}

	// 引擎也必須報錯而不是默認未設定或自動補 initial。
	if r := a.engine.Tick(context.Background()); r.Errors != 1 {
		t.Fatalf("tick 應報一次錯：%+v", r)
	}
	acc = getAccount(t, a, accID)
	if acc.MaxDDStoppedAt != nil {
		t.Fatal("資料錯誤不得建立停止標記")
	}
	if acc.MaxDDBaselineEquity != nil {
		t.Fatalf("不得自動補 baseline：%v", *acc.MaxDDBaselineEquity)
	}

	// 補回 baseline 後交易恢復（這是資料錯誤、不是帳號被停用）。
	faultExec(t, a, "UPDATE accounts SET max_dd_baseline_equity = 10000 WHERE id = ?", accID)
	placeMarketOrder(t, a, token, 1, 10)
}
