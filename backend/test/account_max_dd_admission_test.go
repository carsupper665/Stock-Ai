package test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// tryPlaceOrder 送一張單但不 fatal，回傳 HTTP status 與錯誤碼，供 409／504
// 等拒絕情境斷言。
func tryPlaceOrder(t *testing.T, a *tradingAPI, token string, body map[string]any) (int, string) {
	t.Helper()
	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, body)
	var errBody struct {
		Error string `json:"error"`
	}
	if status == http.StatusCreated {
		return status, ""
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode order error: %v %s", err, raw)
	}
	return status, errBody.Error
}

// injectMaxDDMarker 以第二個連線直接寫入停止標記（測試專用）：模擬「掛單
// snapshot 已載入、marker 後來寫入」的競態結果狀態，且刻意不撤單，讓
// fillOpenOrder 必須靠鎖內重讀阻止成交。
func injectMaxDDMarker(t *testing.T, a *tradingAPI, accountID string) {
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
	now := time.Now().UTC()
	res := conn.WithContext(context.Background()).
		Model(&database.Account{}).
		Where("id = ?", accountID).
		Updates(map[string]any{"max_dd_stopped_at": now})
	if res.Error != nil || res.RowsAffected != 1 {
		t.Fatalf("inject marker: rows=%d err=%v", res.RowsAffected, res.Error)
	}
}

// TestAccountMaxDDOrderBeforeTickStopsAccount 是 AC1＋AC2（C3）：價格已達門檻
// 但尚未 Tick 時，limit 請求本身必須在 transaction 內寫停止標記、撤單並
// commit（回 409 不得 rollback 停止），新單無 fill／無 open order；market
// 請求接著也 409；下一 Tick 完成清倉。
func TestAccountMaxDDOrderBeforeTickStopsAccount(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "pre-tick", 10000, 10)
	a.setPrice(t, 1000)                                            // 行情設為 1000
	placeOrderRaw(t, a, token, "BTCUSDT", "futures", "buy", 10, 1) // 1000 開倉
	a.setPrice(t, 900)                                             // equity = 10000 - 1000 = 9000 = floor（達門檻、未 Tick）

	// limit 請求必須在真正 mutation 前被攔住：409，無 open order、無 order_placed。
	status, code := tryPlaceOrder(t, a, token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "limit", "quantity": 1, "price": 950,
	})
	if status != http.StatusConflict || code != "max_dd_stopped" {
		t.Fatalf("limit 請求 = %d/%s，want 409/max_dd_stopped", status, code)
	}
	if n := len(a.openOrders(t, accID)); n != 0 {
		t.Fatalf("新單應無 open order，得到 %d 張", n)
	}
	if n := countLedgerEvent(t, a, token, "order_placed"); n != 0 {
		t.Fatalf("新單不應寫 order_placed，得到 %d 筆", n)
	}
	// AC2：停止標記已 commit——回 409 沒有把停止 rollback。
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("limit 請求後停止標記應已 commit")
	}
	if n := countLedgerEvent(t, a, token, "max_dd_triggered"); n != 1 {
		t.Fatalf("max_dd_triggered=%d want 1", n)
	}

	// market 請求同樣 409，無 fill。
	status, code = tryPlaceOrder(t, a, token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 1,
	})
	if status != http.StatusConflict || code != "max_dd_stopped" {
		t.Fatalf("停止後 market 請求 = %d/%s，want 409/max_dd_stopped", status, code)
	}
	if n := len(a.allTrades(t, accID)); n != 1 {
		t.Fatalf("trades=%d want 1（只有開倉）", n)
	}

	// 下一 Tick 完成清倉。
	result := a.engine.Tick(context.Background())
	if result.Filled != 0 || result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("停止後 Tick = %+v，want Filled=0 Closed=1 Errors=0", result)
	}
	if n := len(a.allPositions(t, accID)); n != 0 {
		t.Fatalf("positions=%d want 0", n)
	}
	if acc := getAccount(t, a, accID); acc.Balance != 9000 {
		t.Fatalf("balance=%v want 9000", acc.Balance)
	}
}

// TestAccountMaxDDLimitFillAfterMarkerWrittenIsCanceled 是 AC3：掛單 snapshot
// 已載入（DB 中仍 open）而 marker 後來寫入時，fillOpenOrder 的鎖內重讀必須
// 阻止成交並以 canceled 回傳；Engine 不得把 canceled 算成 Filled。
func TestAccountMaxDDLimitFillAfterMarkerWrittenIsCanceled(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "fill-after-marker", 10000, 10)
	a.setPrice(t, 1000)                                            // 行情設為 1000
	placeOrderRaw(t, a, token, "BTCUSDT", "futures", "buy", 10, 1) // 1000 開倉
	orderID := placeLimitOrder(t, a, token, "buy", 950, nil)       // open（equity 10000 未達門檻）
	a.setPrice(t, 900)                                             // 門檻已達
	injectMaxDDMarker(t, a, accID)                                 // snapshot 後寫入的 marker（訂單未被撤）

	result := a.engine.Tick(context.Background())
	if result.Filled != 0 {
		t.Fatalf("被撤的掛單被算成成交: %+v", result)
	}
	if result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("Tick = %+v，want Closed=1 Errors=0", result)
	}

	orders := a.allOrders(t, accID)
	var limit *database.Order
	for i := range orders {
		if orders[i].ID == orderID {
			limit = &orders[i]
		}
	}
	if limit == nil || limit.Status != database.OrderCanceled {
		t.Fatalf("limit 訂單應為 canceled，得到 %+v", limit)
	}
	if n := countLedgerEvent(t, a, token, "order_canceled"); n != 1 {
		t.Fatalf("order_canceled=%d want 1", n)
	}
	for _, e := range a.ledger(t, token, "?limit=200") {
		if e.Event == "fill" && e.OrderID == orderID {
			t.Fatalf("被撤的掛單不得有 fill: %+v", e)
		}
	}
	// 標記已在盤：部位仍被清倉完成。
	if n := len(a.allPositions(t, accID)); n != 0 {
		t.Fatalf("positions=%d want 0", n)
	}
	if acc := getAccount(t, a, accID); acc.Balance != 9000 {
		t.Fatalf("balance=%v want 9000", acc.Balance)
	}
}

// TestAccountMaxDDFillSkippedWhenRiskUnevaluable 是規格 §5 的缺價規則：
// 缺任一持倉報價時不得略過新增風險交易檢查——舊掛單本輪不能成交（也不算
// 引擎錯誤），且缺價本身不觸發停止；行情恢復後門檻仍達時由風控 sweep 停止
// 並撤單、完成全平。
func TestAccountMaxDDFillSkippedWhenRiskUnevaluable(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "fill-unevaluable", 10000, 10)
	a.setPrice(t, 1000)                                             // 行情設為 1000
	placeOrderRaw(t, a, token, "BTCUSDT", "futures", "buy", 10, 10) // 1000 開倉
	placeOrderRaw(t, a, token, "ETHUSDT", "futures", "buy", 10, 10) // 1000 開倉
	orderID := placeLimitOrder(t, a, token, "buy", 950, nil)        // BTC 掛單（equity 10000 未達門檻）
	a.setPrice(t, 900)                                              // 雙標的都有價時 equity = 8000 < floor 9000
	a.dropSymbolPrice(t, "ETHUSDT")

	result := a.engine.Tick(context.Background())
	if result.Filled != 0 || result.Closed != 0 || result.Errors != 0 {
		t.Fatalf("缺價 Tick = %+v，want 全 0", result)
	}
	orders := a.openOrders(t, accID)
	if len(orders) != 1 || orders[0].ID != orderID {
		t.Fatalf("掛單應維持 open，得到 %+v", orders)
	}
	if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt != nil {
		t.Fatal("缺價本身不得觸發停止")
	}

	// 行情恢復後門檻仍達：風控 sweep 停止並撤單，全平完成。
	a.restoreSymbolPrice(t, "ETHUSDT", 900)
	result = a.engine.Tick(context.Background())
	if result.Filled != 0 || result.Closed != 2 || result.Errors != 0 {
		t.Fatalf("恢復後 Tick = %+v，want Filled=0 Closed=2 Errors=0", result)
	}
	if n := len(a.openOrders(t, accID)); n != 0 {
		t.Fatalf("掛單應被停止撤單，仍 open %d 張", n)
	}
	if acc := getAccount(t, a, accID); acc.Balance != 8000 {
		t.Fatalf("balance=%v want 8000", acc.Balance)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_triggered"); n != 1 {
		t.Fatalf("max_dd_triggered=%d want 1", n)
	}
}

// TestAccountMaxDDNullAccountKeepsFastPath 是 AC4：max_dd=null 維持原快速
// 路徑——持倉標的缺價時新單仍照常成交（完全不取持倉報價）；對照 max_dd
// 帳號持倉缺價時不能略過新增風險交易檢查（fail closed），行情恢復後可下單。
func TestAccountMaxDDNullAccountKeepsFastPath(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	a.setPrice(t, 1000) // 行情設為 1000

	// 對照組：max_dd 帳號持倉缺價 → 無法評估風險，下單 fail closed。
	acc2, token2 := createMaxDDAccount(t, a, "evaluable-maxdd", 10000, 10)
	placeOrderRaw(t, a, token2, "ETHUSDT", "futures", "buy", 10, 10) // 1000 開倉
	a.dropSymbolPrice(t, "ETHUSDT")
	status, code := tryPlaceOrder(t, a, token2, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 1,
	})
	if status != http.StatusGatewayTimeout || code != "market_timeout" {
		t.Fatalf("max_dd 帳號缺價下單 = %d/%s，want 504/market_timeout", status, code)
	}
	if n := len(a.allTrades(t, acc2)); n != 1 {
		t.Fatalf("被擋的下單不得產生交易：trades=%d want 1", n)
	}
	a.restoreSymbolPrice(t, "ETHUSDT", 1000)
	placeOrderRaw(t, a, token2, "BTCUSDT", "futures", "buy", 1, 1) // 恢復後 201
	if acc := getAccount(t, a, acc2); acc.MaxDDStoppedAt != nil {
		t.Fatal("未達門檻的帳號不得被停止")
	}

	// max_dd=null 帳號：持倉標的缺價仍照常成交（快速路徑不取持倉報價）。
	accID, token := a.createAccount(t, "no-maxdd", 10000)
	placeOrderRaw(t, a, token, "ETHUSDT", "futures", "buy", 10, 10) // 1000 開倉
	a.dropSymbolPrice(t, "ETHUSDT")
	order := placeOrderRaw(t, a, token, "BTCUSDT", "futures", "buy", 1, 1)
	if order.Status != database.OrderFilled {
		t.Fatalf("null max_dd 帳號應照常成交：status=%s", order.Status)
	}
	if acc := getAccount(t, a, accID); acc.MaxDD != nil {
		t.Fatalf("null max_dd 帳號不應有 max_dd 設定: %v", *acc.MaxDD)
	}
}
