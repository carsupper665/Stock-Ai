package test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"

	"backend/internal/database"
	"backend/internal/trading"
)

// placeMarketOrderSide 以指定方向下市價單（placeMarketOrder 的 short 變體）。
func placeMarketOrderSide(t *testing.T, a *tradingAPI, token, side string, quantity, leverage float64) *orderView {
	t.Helper()
	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": side, "type": "market", "quantity": quantity, "leverage": leverage,
	})
	if status != http.StatusCreated {
		t.Fatalf("place market order %s: status=%d body=%s", side, status, raw)
	}
	return decodeJSON[*orderView](t, raw)
}

// orderWithTrigger 回傳帳號全部訂單中第一筆指定 trigger 的訂單。
func (a *tradingAPI) orderWithTrigger(t *testing.T, accountID, trigger string) *database.Order {
	t.Helper()
	orders := a.allOrders(t, accountID)
	for i := range orders {
		if orders[i].Trigger == trigger {
			return &orders[i]
		}
	}
	return nil
}

// TestAccountMaxDDLiquidatesSinglePosition 是 B1/B2/B4：沒有 stops 的持倉，
// equity 9000.1（price 900.01）不觸發，900（正好 floor）觸發；零費用下最終
// balance 恰為 9000、無持倉無掛單；全平走原成交路徑：一張 Order、一筆
// Trade、一筆 fill Ledger，Trigger=max_dd、Source=null。
func TestAccountMaxDDLiquidatesSinglePosition(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-single", 10000, 10)

	a.setPrice(t, 1000)
	placeMarketOrder(t, a, token, 10, 10) // long 10@1000，零費用 → balance 維持 10000

	a.setPrice(t, 900.01)
	if r := a.engine.Tick(context.Background()); r.Filled != 0 || r.Closed != 0 || r.Errors != 0 {
		t.Fatalf("tick at 900.01 should do nothing, got %+v", r)
	}
	if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt != nil {
		t.Fatalf("equity 9000.1 > floor 9000, should not stop: %+v", acc)
	}
	if got := len(a.allPositions(t, accID)); got != 1 {
		t.Fatalf("position count = %d, want 1", got)
	}

	a.setPrice(t, 900)
	r := a.engine.Tick(context.Background())
	if r.Closed != 1 || r.Filled != 0 || r.Errors != 0 {
		t.Fatalf("tick at 900: %+v, want Closed=1", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("equity 9000 == floor 9000, expected max_dd stop")
	}
	if acc.Balance != 9000 {
		t.Fatalf("final balance = %v, want 9000 (zero fees)", acc.Balance)
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0 after liquidation", got)
	}
	if got := len(a.openOrders(t, accID)); got != 0 {
		t.Fatalf("open orders = %d, want 0", got)
	}

	closeOrder := a.orderWithTrigger(t, accID, trading.TriggerMaxDD)
	if closeOrder == nil {
		t.Fatalf("no max_dd close order among %d orders", len(a.allOrders(t, accID)))
	}
	if closeOrder.Status != database.OrderFilled || closeOrder.Side != database.SideSell ||
		!closeOrder.ReduceOnly || closeOrder.Fee != 0 || closeOrder.AvgFillPrice != 900 {
		t.Fatalf("close order = %+v", closeOrder)
	}

	// API 視圖：Source 必須是 null（系統強平沒有來源 Run）。
	status, raw := a.request(t, http.MethodGet, "/v1/orders", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list orders: status=%d body=%s", status, raw)
	}
	var list struct {
		Orders []orderView `json:"orders"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Orders) != 2 {
		t.Fatalf("orders list = %s", raw)
	}
	var apiClose *orderView
	for i := range list.Orders {
		if list.Orders[i].Trigger == trading.TriggerMaxDD {
			apiClose = &list.Orders[i]
		}
	}
	if apiClose == nil || apiClose.Source != nil {
		t.Fatalf("close order view = %+v, want trigger=max_dd source=null", apiClose)
	}

	trades := a.allTrades(t, accID)
	if len(trades) != 2 {
		t.Fatalf("trades = %d, want 2 (open + close)", len(trades))
	}
	var closeTrade *database.Trade
	for i := range trades {
		if trades[i].OrderID == closeOrder.ID {
			closeTrade = &trades[i]
		}
	}
	if closeTrade == nil || closeTrade.RealizedPnL != -1000 || closeTrade.Fee != 0 {
		t.Fatalf("close trade = %+v, want realized=-1000 fee=0", closeTrade)
	}

	fills := 0
	for _, e := range a.ledger(t, token, "?limit=200") {
		if e.Event != "fill" {
			continue
		}
		fills++
		if e.Trigger == trading.TriggerMaxDD {
			if e.Source != nil || e.OrderID != closeOrder.ID || e.Fee != 0 ||
				e.RealizedPnL != -1000 || e.BalanceAfter != 9000 {
				t.Fatalf("max_dd fill ledger = %+v", e)
			}
		}
	}
	if fills != 2 {
		t.Fatalf("fill ledgers = %d, want 2", fills)
	}
	if n := countLedgerEvent(t, a, token, database.LedgerMaxDDTriggered); n != 1 {
		t.Fatalf("max_dd_triggered ledgers = %d, want 1", n)
	}
}

// TestAccountMaxDDLiquidationBaselineDoesNotRatchet 是 B3：先漲到 1200 再回
// 1000，baseline 不跟峰值上移（不是歷史最高 equity 的回撤），equity 未低於
// floor 就不觸發。
func TestAccountMaxDDLiquidationBaselineDoesNotRatchet(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-ratchet", 10000, 10)

	a.setPrice(t, 1000)
	placeMarketOrder(t, a, token, 10, 10)

	a.setPrice(t, 1200)
	a.engine.Tick(context.Background())
	a.setPrice(t, 1000)
	a.engine.Tick(context.Background())

	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt != nil {
		t.Fatalf("equity never below floor 9000, should not stop: %+v", acc)
	}
	if acc.MaxDDBaselineEquity == nil || *acc.MaxDDBaselineEquity != 10000 {
		t.Fatalf("baseline = %v, must stay initial 10000 (no ratchet)", acc.MaxDDBaselineEquity)
	}
	if got := len(a.allPositions(t, accID)); got != 1 {
		t.Fatalf("position count = %d, want 1", got)
	}
	if n := countLedgerEvent(t, a, token, database.LedgerMaxDDTriggered); n != 0 {
		t.Fatalf("max_dd_triggered ledgers = %d, want 0", n)
	}
}

// TestAccountMaxDDLiquidationHandlesDisabledAccount 是 C5：行政 disabled 的
// 帳號仍會被風控停止並全平（系統強平繞過行政狀態）；風控不改行政 Status。
func TestAccountMaxDDLiquidationHandlesDisabledAccount(t *testing.T) {
	a := newTradingAPI(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-disabled", 10000, 10)

	a.setPrice(t, 1000)
	placeMarketOrder(t, a, token, 10, 10) // taker fee 4 → balance 9996

	acc := getAccount(t, a, accID)
	acc.Status = database.AccountDisabled
	if err := a.store.SaveAccount(context.Background(), acc); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	a.setPrice(t, 900)
	r := a.engine.Tick(context.Background())
	if r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("tick on disabled account: %+v, want Closed=1", r)
	}
	acc = getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("expected max_dd stop")
	}
	if acc.Status != database.AccountDisabled {
		t.Fatalf("risk control must not change admin status, got %s", acc.Status)
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0 (system liquidation must bypass admin disabled)", got)
	}
	want := 9996 - 1000 - 10*900*0.0004
	if math.Abs(acc.Balance-want) > 1e-8 {
		t.Fatalf("balance = %v, want %v", acc.Balance, want)
	}
	if closeOrder := a.orderWithTrigger(t, accID, trading.TriggerMaxDD); closeOrder == nil {
		t.Fatal("no max_dd close order")
	}
}

// TestAccountMaxDDLiquidationFeeShortfall 是 C6/C7：平倉費用超過帳號可用餘額
// 時，只有 max_dd 內部 reduce-only 分支放寬——費用照實扣、允許最終 Balance
// 為負；普通平倉路徑仍按原規則拒絕（insufficient_balance）。
func TestAccountMaxDDLiquidationFeeShortfall(t *testing.T) {
	a := newTradingAPI(t, "")
	accID, token := createMaxDDAccount(t, a, "liq-feeshort", 10000, 100) // loss_limit 10000 → floor 0
	plainID, plainToken := a.createAccount(t, "liq-feeplain", 10000)

	a.setPrice(t, 1000)
	placeMarketOrderSide(t, a, token, "sell", 10, 10) // short 10@1000, fee 4 → 9996
	plainOrder := placeMarketOrderSide(t, a, plainToken, "sell", 10, 10)

	a.setPrice(t, 3000000) // 平倉費 12000 > 可用 8996：普通路徑會被 insufficient 擋下
	r := a.engine.Tick(context.Background())
	if r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("tick with fee shortfall: %+v, want Closed=1", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("expected max_dd stop")
	}
	if got := len(a.allPositions(t, accID)); got != 0 {
		t.Fatalf("position count = %d, want 0 (fee shortfall must not block system liquidation)", got)
	}
	want := 9996 + (1000-3000000)*10 - 10*3000000*0.0004 // = -29992004
	if math.Abs(acc.Balance-want) > 1e-8 {
		t.Fatalf("balance = %v, want %v (fee deducted as-is, negative allowed)", acc.Balance, want)
	}
	closeOrder := a.orderWithTrigger(t, accID, trading.TriggerMaxDD)
	if closeOrder == nil || closeOrder.Fee != 10*3000000*0.0004 {
		t.Fatalf("close order = %+v, fee %v must be deducted as-is", closeOrder, 10*3000000*0.0004)
	}

	// 同樣條件下未設 max_dd 的帳號：手動平倉仍被原規則拒絕、部位保留。
	status, raw := a.request(t, http.MethodPost, "/v1/positions/"+plainOrder.PositionID+"/close", plainToken, map[string]any{})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("plain close: status=%d body=%s, want 422 insufficient_balance", status, raw)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil || errBody.Error != "insufficient_balance" {
		t.Fatalf("plain close error = %s", raw)
	}
	if got := len(a.allPositions(t, plainID)); got != 1 {
		t.Fatalf("plain position count = %d, want 1 (normal fill rules unchanged)", got)
	}
}
