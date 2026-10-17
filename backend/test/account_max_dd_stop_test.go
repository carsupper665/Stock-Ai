package test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/trading"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// createMaxDDAccount 透過 API 建立設定 max_dd 的帳號，回傳 ID 與 token。
func createMaxDDAccount(t *testing.T, a *tradingAPI, name string, balance, maxDD float64) (string, string) {
	t.Helper()
	status, raw := a.request(t, http.MethodPost, "/v1/accounts", tradingUserToken, map[string]any{
		"user_name": name, "initial_balance": balance, "max_dd": maxDD,
	})
	if status != http.StatusCreated {
		t.Fatalf("create max_dd account: status=%d body=%s", status, raw)
	}
	var acc struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &acc); err != nil || acc.ID == "" || acc.Token == "" {
		t.Fatalf("decode account: %v %s", err, raw)
	}
	return acc.ID, acc.Token
}

// setBalance 直接以 store 調整餘額（fixture 準備，不走交易路徑）。
func setBalance(t *testing.T, a *tradingAPI, accountID string, balance float64) {
	t.Helper()
	acc, err := a.store.AccountByID(context.Background(), accountID)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	acc.Balance = balance
	if err := a.store.SaveAccount(context.Background(), acc); err != nil {
		t.Fatalf("set balance: %v", err)
	}
}

func getAccount(t *testing.T, a *tradingAPI, accountID string) *database.Account {
	t.Helper()
	acc, err := a.store.AccountByID(context.Background(), accountID)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	return acc
}

func countLedgerEvent(t *testing.T, a *tradingAPI, token, event string) int {
	t.Helper()
	n := 0
	for _, e := range a.ledger(t, token, "?limit=200") {
		if e.Event == event {
			n++
		}
	}
	return n
}

func placeLimitOrder(t *testing.T, a *tradingAPI, token, side string, price float64, source map[string]any) string {
	t.Helper()
	body := map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": side, "type": "limit", "quantity": 1, "price": price,
	}
	if source != nil {
		body["source"] = source
	}
	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, body)
	if status != http.StatusCreated {
		t.Fatalf("place limit order: status=%d body=%s", status, raw)
	}
	var order struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &order); err != nil || order.ID == "" {
		t.Fatalf("decode order: %v %s", err, raw)
	}
	return order.ID
}

func placeMarketOrder(t *testing.T, a *tradingAPI, token string, quantity, leverage float64) *orderView {
	t.Helper()
	status, raw := a.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": quantity, "leverage": leverage,
	})
	if status != http.StatusCreated {
		t.Fatalf("place market order: status=%d body=%s", status, raw)
	}
	return decodeJSON[*orderView](t, raw)
}

func (a *tradingAPI) openOrders(t *testing.T, accountID string) []database.Order {
	t.Helper()
	orders, err := a.store.OpenOrdersForAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("list open orders: %v", err)
	}
	return orders
}

func (a *tradingAPI) allPositions(t *testing.T, accountID string) []database.Position {
	t.Helper()
	positions, err := a.store.ListPositions(context.Background(), accountID, "")
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	return positions
}

func (a *tradingAPI) allOrders(t *testing.T, accountID string) []database.Order {
	t.Helper()
	orders, err := a.store.ListOrders(context.Background(), accountID, "", 200)
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	return orders
}

func (a *tradingAPI) allTrades(t *testing.T, accountID string) []database.Trade {
	t.Helper()
	trades, err := a.store.ListTrades(context.Background(), accountID, 200)
	if err != nil {
		t.Fatalf("list trades: %v", err)
	}
	return trades
}

// TestAccountMaxDDStopFlatAccount 是 B7＋E3：零部位、零掛單、balance 正好落在
// floor 的帳號，Tick 不取任何行情就寫一次 max_dd_triggered；重複 Tick 不重寫；
// 行政 Status 不被風控改掉。
func TestAccountMaxDDStopFlatAccount(t *testing.T) {
	a := newTradingAPI(t, "")
	accountID, token := createMaxDDAccount(t, a, "flat", 10000, 10)
	setBalance(t, a, accountID, 9000) // floor = 10000 - 10000*10% = 9000

	result := a.engine.Tick(context.Background())
	if result.Filled != 0 || result.Closed != 0 || result.Rejected != 0 || result.Errors != 0 {
		t.Fatalf("flat stop tick: %+v", result)
	}

	acc := getAccount(t, a, accountID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatalf("equity 9000 == floor 9000 應觸發風控停止")
	}
	if acc.Status != database.AccountActive {
		t.Fatalf("E3：風控不得改行政 Status，got %s", acc.Status)
	}
	if acc.Balance != 9000 {
		t.Fatalf("風控停止不成交，balance 不變：got %v want 9000", acc.Balance)
	}

	triggered := 0
	for _, e := range a.ledger(t, token, "?limit=200") {
		switch e.Event {
		case "max_dd_triggered":
			triggered++
			if e.BalanceAfter != 9000 {
				t.Fatalf("trigger ledger 應記錄觸發時 equity：got %v want 9000", e.BalanceAfter)
			}
		case "fill", "order_rejected":
			t.Fatalf("風控停止不得產生成交或拒絕：got %s", e.Event)
		}
	}
	if triggered != 1 {
		t.Fatalf("max_dd_triggered=%d want 1", triggered)
	}

	// E3：重複 Tick 不重寫事件
	for i := 0; i < 2; i++ {
		if r := a.engine.Tick(context.Background()); r.Errors != 0 {
			t.Fatalf("repeat tick %d: %+v", i, r)
		}
	}
	if n := countLedgerEvent(t, a, token, "max_dd_triggered"); n != 1 {
		t.Fatalf("重複 Tick 後 max_dd_triggered=%d want 1", n)
	}
	if n := len(a.openOrders(t, accountID)); n != 0 {
		t.Fatalf("flat 帳號不應有掛單：%d", n)
	}
	if n := len(a.allPositions(t, accountID)); n != 0 {
		t.Fatalf("flat 帳號不應有部位：%d", n)
	}
}

// TestAccountMaxDDStopWithoutMarketData 是 B7 的「不取行情」：流被卡住、行情
// 全部逾時時，零部位帳號的風控停止仍要提交，掛單照撤。
func TestAccountMaxDDStopWithoutMarketData(t *testing.T) {
	a := newTradingAPI(t, "")
	accountID, token := createMaxDDAccount(t, a, "noprices", 10000, 10)
	// 先下掛單（equity 高於 floor）再降 balance 到 floor：ticket 05 後，已在 floor 的帳號下單會自行觸發風控停止回 409。
	placeLimitOrder(t, a, token, "sell", 1e6, nil)
	placeLimitOrder(t, a, token, "sell", 1e6, nil)
	setBalance(t, a, accountID, 9000)

	// 先讓訂閱啟動，stall 才有 stream 可 park。
	a.setPrice(t, 60000)
	a.stallPrices(t)
	done := make(chan trading.TickResult, 1)
	go func() { done <- a.engine.Tick(context.Background()) }()
	var result trading.TickResult
	select {
	case result = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Tick 在行情卡住時未完成")
	}
	a.resumePrices()
	if result.Errors != 0 {
		t.Fatalf("stalled tick: %+v", result)
	}

	acc := getAccount(t, a, accountID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatalf("零部位風控停止不應等待行情")
	}
	if n := len(a.openOrders(t, accountID)); n != 0 {
		t.Fatalf("掛單應全部撤掉，剩 %d", n)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_triggered"); n != 1 {
		t.Fatalf("max_dd_triggered=%d want 1", n)
	}
	if n := countLedgerEvent(t, a, token, "order_canceled"); n != 2 {
		t.Fatalf("order_canceled=%d want 2", n)
	}
	if n := countLedgerEvent(t, a, token, "fill"); n != 0 {
		t.Fatalf("風控停止不成交：fill=%d", n)
	}
}

// TestAccountMaxDDStopCancelsAllOpenOrders 是 C1：超過 UI 預設分頁（50）的掛單
// 全部撤掉，各一筆 order_canceled，本輪已符合限價條件的單也被撤而非成交；
// 重複 Tick 不新增任何事件。
func TestAccountMaxDDStopCancelsAllOpenOrders(t *testing.T) {
	a := newTradingAPI(t, "")
	accountID, token := createMaxDDAccount(t, a, "many-orders", 10000, 10)

	const total = 55 // UI 預設分頁是 50 筆
	// 先下掛單（equity 高於 floor）再降 balance 到 floor：ticket 05 後，已在 floor 的帳號下單會自行觸發風控停止回 409。
	triggeredID := placeLimitOrder(t, a, token, "buy", 1, nil) // 現價 60000 已符合買進限價
	for i := 1; i < total; i++ {
		placeLimitOrder(t, a, token, "sell", 1e6, nil)
	}
	setBalance(t, a, accountID, 9000)

	result := a.engine.Tick(context.Background())
	if result.Filled != 0 || result.Closed != 0 || result.Rejected != 0 || result.Errors != 0 {
		t.Fatalf("stop tick: %+v", result)
	}

	if n := len(a.openOrders(t, accountID)); n != 0 {
		t.Fatalf("超過分頁的 %d 張掛單沒有全撤，剩 %d", total, n)
	}
	order, err := a.store.OrderByID(context.Background(), accountID, triggeredID)
	if err != nil {
		t.Fatalf("load triggered order: %v", err)
	}
	if order.Status != database.OrderCanceled {
		t.Fatalf("本輪已符合條件的單應被風控撤掉而非成交：status=%s", order.Status)
	}

	canceled, triggered, fills := 0, 0, 0
	for _, e := range a.ledger(t, token, "?limit=200") {
		switch e.Event {
		case "order_canceled":
			canceled++
			if e.Fee != 0 {
				t.Fatalf("撤單不計費：fee=%v", e.Fee)
			}
		case "max_dd_triggered":
			triggered++
		case "fill":
			fills++
		}
	}
	if canceled != total {
		t.Fatalf("order_canceled=%d want %d", canceled, total)
	}
	if triggered != 1 {
		t.Fatalf("max_dd_triggered=%d want 1", triggered)
	}
	if fills != 0 {
		t.Fatalf("風控停止不成交：fill=%d", fills)
	}
	if n := len(a.ledger(t, token, "?limit=200")); n != total*2+1 {
		t.Fatalf("ledger 應只有 placed+canceled+triggered：%d want %d", n, total*2+1)
	}

	if n := len(a.allTrades(t, accountID)); n != 0 {
		t.Fatalf("風控停止不得有成交：%d", n)
	}

	// C1/E3：重複 Tick 不新增事件
	if r := a.engine.Tick(context.Background()); r.Errors != 0 {
		t.Fatalf("repeat tick: %+v", r)
	}
	if n := len(a.ledger(t, token, "?limit=200")); n != total*2+1 {
		t.Fatalf("重複 Tick 後 ledger 增加了事件：%d want %d", n, total*2+1)
	}
}

// TestAccountMaxDDStoppedBlocksNewTrading 是 C2：marker 設定後，市價／限價／Agent
// 來源下單、手動平倉、改 stops 一律 409 max_dd_stopped；查詢與撤單維持原授權
// 語意；沒有新交易資料。
func TestAccountMaxDDStoppedBlocksNewTrading(t *testing.T) {
	a := newTradingAPI(t, "")
	accountID, token := createMaxDDAccount(t, a, "blocked", 10000, 10)

	a.setPrice(t, 1000)
	opened := placeMarketOrder(t, a, token, 10, 10) // margin 1000、taker fee 4 → balance 9996
	if opened.PositionID == "" {
		t.Fatalf("開倉回應缺 position_id: %v", opened)
	}

	// 跌到 900：equity = 9996 + (900-1000)*10 = 8996 <= floor 9000
	a.setPrice(t, 900)
	if r := a.engine.Tick(context.Background()); r.Errors != 0 {
		t.Fatalf("stop tick: %+v", r)
	}
	acc := getAccount(t, a, accountID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatalf("equity 8996 低於 floor 9000，應停止")
	}
	// 03 票已實作清倉：同一 tick 全平（多一筆 Order/Trade/fill，Trigger=max_dd）
	if n := len(a.allPositions(t, accountID)); n != 0 {
		t.Fatalf("風控停止後部位應已被全平：%d", n)
	}
	// 記錄 409 之前的 ledger 長度：被擋掉的請求不得留下任何新事件
	ledgerBefore := len(a.ledger(t, token, "?limit=200"))

	assertStopped := func(method, path string, body any) {
		t.Helper()
		code, raw := a.request(t, method, path, token, body)
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &errBody)
		if code != http.StatusConflict || errBody.Error != "max_dd_stopped" {
			t.Fatalf("%s %s 應 409 max_dd_stopped：status=%d error=%s body=%s", method, path, code, errBody.Error, raw)
		}
	}
	assertStopped(http.MethodPost, "/v1/orders", map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	})
	limitBody := map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "limit", "quantity": 1, "price": 800,
	}
	assertStopped(http.MethodPost, "/v1/orders", limitBody)
	agentBody := map[string]any{ // Agent 來源與手動完全同等對待
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "limit", "quantity": 1, "price": 800,
		"source": agentSource("sess-1", 7),
	}
	assertStopped(http.MethodPost, "/v1/orders", agentBody)
	assertStopped(http.MethodPost, "/v1/positions/"+opened.PositionID+"/close", map[string]any{})
	assertStopped(http.MethodPatch, "/v1/positions/"+opened.PositionID, map[string]any{"stop_loss": 850})

	// 查詢維持原授權（Account 與 USER 兩條路徑）
	for _, q := range []struct {
		path  string
		token string
	}{
		{"/v1/positions", token}, {"/v1/orders", token}, {"/v1/ledger?limit=200", token},
		{"/v1/accounts/" + accountID + "/positions", tradingUserToken},
		{"/v1/accounts/" + accountID + "/orders", tradingUserToken},
	} {
		if status, raw := a.request(t, http.MethodGet, q.path, q.token, nil); status != http.StatusOK {
			t.Fatalf("GET %s 查詢應維持原授權：status=%d body=%s", q.path, status, raw)
		}
	}
	// 撤單不被風控擋：已成交的單回原本的 order_not_open，而不是 max_dd_stopped
	code, raw := a.request(t, http.MethodPost, "/v1/orders/"+opened.ID+"/cancel", token, nil)
	var cancelErr struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &cancelErr)
	if code != http.StatusConflict || cancelErr.Error != "order_not_open" {
		t.Fatalf("撤單應維持原語意 order_not_open：status=%d error=%s", code, cancelErr.Error)
	}

	// 無新交易資料：409 與撤單嘗試都不應留下任何 ledger 事件
	//（2 = 開倉＋系統強平兩筆；此後沒有新增）
	if n := len(a.allOrders(t, accountID)); n != 2 {
		t.Fatalf("訂單數應為 2（開倉＋max_dd 強平）：got %d", n)
	}
	if n := len(a.allTrades(t, accountID)); n != 2 {
		t.Fatalf("成交數應為 2（開倉＋max_dd 強平）：got %d", n)
	}
	if n := len(a.ledger(t, token, "?limit=200")); n != ledgerBefore {
		t.Fatalf("409 不應留下新 ledger 事件：%d want %d", n, ledgerBefore)
	}
	if n := countLedgerEvent(t, a, token, "fill"); n != 2 {
		t.Fatalf("fill 應為開倉＋強平兩筆：got %d", n)
	}
}

// TestAccountMaxDDStopTransactionRollsBack 是 E3：在停止交易的 mark 寫入、
// 第二張撤單、或 max_dd_triggered ledger 寫入注入錯誤，整個 transaction 必須
// 回滾——不能出現 marker=null 卻只撤一半；帳號維持可交易，下輪可重試。
func TestAccountMaxDDStopTransactionRollsBack(t *testing.T) {
	cases := []struct {
		name   string
		create string
		drop   string
	}{
		{
			name: "mark 寫入失敗",
			create: `CREATE TRIGGER fail_mark BEFORE UPDATE ON accounts
FOR EACH ROW
WHEN NEW.max_dd_stopped_at IS NOT NULL AND OLD.max_dd_stopped_at IS NULL
BEGIN SELECT RAISE(ABORT, 'injected mark failure'); END`,
			drop: "DROP TRIGGER fail_mark",
		},
		{
			name: "第二張撤單失敗",
			create: `CREATE TRIGGER fail_second_cancel BEFORE UPDATE ON orders
FOR EACH ROW
WHEN (SELECT count(*) FROM ledger_entries WHERE event = 'order_canceled' AND account_id = NEW.account_id) >= 1
BEGIN SELECT RAISE(ABORT, 'injected second cancel failure'); END`,
			drop: "DROP TRIGGER fail_second_cancel",
		},
		{
			name: "triggered ledger 寫入失敗",
			create: `CREATE TRIGGER fail_ledger BEFORE INSERT ON ledger_entries
FOR EACH ROW
WHEN NEW.event = 'max_dd_triggered'
BEGIN SELECT RAISE(ABORT, 'injected ledger failure'); END`,
			drop: "DROP TRIGGER fail_ledger",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTradingAPI(t, "")
			accountID, token := createMaxDDAccount(t, a, "rollback", 10000, 10)
			// 先下掛單（equity 高於 floor）再降 balance 到 floor。
			placeLimitOrder(t, a, token, "sell", 1e6, nil)
			placeLimitOrder(t, a, token, "sell", 1e6, nil)
			placeLimitOrder(t, a, token, "sell", 1e6, nil)
			setBalance(t, a, accountID, 9000)

			fault, err := gorm.Open(sqlite.Open(a.dbPath), &gorm.Config{})
			if err != nil {
				t.Fatalf("open fault db: %v", err)
			}
			t.Cleanup(func() {
				sqlDB, _ := fault.DB()
				_ = sqlDB.Close()
			})
			if err := fault.Exec(tc.create).Error; err != nil {
				t.Fatalf("create fault trigger: %v", err)
			}

			result := a.engine.Tick(context.Background())
			if result.Errors != 1 {
				t.Fatalf("停止交易應恰好失敗一次：%+v", result)
			}

			acc := getAccount(t, a, accountID)
			if acc.MaxDDStoppedAt != nil {
				t.Fatalf("E3：失敗時停止標記不得提交")
			}
			if n := len(a.openOrders(t, accountID)); n != 3 {
				t.Fatalf("E3：撤單要與標記一起回滾，剩 %d 張 open（應 3）", n)
			}
			for _, e := range a.ledger(t, token, "?limit=200") {
				if e.Event == "max_dd_triggered" || e.Event == "order_canceled" {
					t.Fatalf("E3：失敗時不得留下停止事件：%s", e.Event)
				}
			}

			// 帳號狀態未變：balance 回到 floor 上方時 API 照常交易，沒有半停止狀態
			setBalance(t, a, accountID, 9500)
			placeMarketOrder(t, a, token, 1, 10)
			setBalance(t, a, accountID, 9000)

			// 下輪可重試：移除故障後 Tick 成功停止
			if err := fault.Exec(tc.drop).Error; err != nil {
				t.Fatalf("drop fault trigger: %v", err)
			}
			if r := a.engine.Tick(context.Background()); r.Errors != 0 {
				t.Fatalf("重試 tick: %+v", r)
			}
			if acc = getAccount(t, a, accountID); acc.MaxDDStoppedAt == nil {
				t.Fatalf("重試後應停止成功")
			}
			if n := len(a.openOrders(t, accountID)); n != 0 {
				t.Fatalf("重試後掛單應全撤，剩 %d", n)
			}
			if n := countLedgerEvent(t, a, token, "max_dd_triggered"); n != 1 {
				t.Fatalf("max_dd_triggered=%d want 1", n)
			}
			if n := countLedgerEvent(t, a, token, "order_canceled"); n != 3 {
				t.Fatalf("order_canceled=%d want 3", n)
			}
		})
	}
}
