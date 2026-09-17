package test

import (
	"context"
	"net/http"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// countAccountRows 直接查表，因為帳號刪除後 USER 端點與 Account Token 都問不到它的資料了。
func countAccountRows(t *testing.T, backend *tradingAPI, table, accountID, extra string) int64 {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(backend.dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() {
		if raw, err := db.DB(); err == nil {
			_ = raw.Close()
		}
	}()
	query := "SELECT COUNT(*) FROM " + table + " WHERE account_id = ?"
	if extra != "" {
		query += " AND " + extra
	}
	var count int64
	if err := db.Raw(query, accountID).Scan(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// 刪帳號只刪 accounts 那一列時，部位會留下變孤兒：撮合引擎每輪仍會掃到它，價格穿過
// 停損停利就嘗試平倉，卻在 settle 取帳號時拿到「資料不存在」，於是每秒寫一次 warning
// 且沒有終止狀態。
func TestDeletingAnAccountRemovesItsPositionsAndOpenOrders(t *testing.T) {
	backend := newTradingAPI(t, "")
	accountID, token := backend.createAccount(t, "cascade-owner", 100000)
	_, otherToken := backend.createAccount(t, "cascade-bystander", 100000)
	backend.setPrice(t, 60000)

	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "sell", "type": "market",
		"quantity": 0.1, "leverage": 10, "take_profit": 50000, "source": agentSource("s_1", 1),
	})
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit",
		"quantity": 0.1, "price": 40000, "leverage": 10, "source": agentSource("s_1", 1),
	})
	placeOrder(t, backend, otherToken, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market",
		"quantity": 0.1, "leverage": 10, "source": agentSource("s_2", 1),
	})
	if positions := listPositions(t, backend, token).Positions; len(positions) != 1 {
		t.Fatalf("expected one position before deletion, got %+v", positions)
	}
	if countAccountRows(t, backend, "orders", accountID, "status = 'open'") != 1 {
		t.Fatal("expected one resting order before deletion")
	}
	tradesBefore := countAccountRows(t, backend, "trades", accountID, "")
	ledgerBefore := countAccountRows(t, backend, "ledger_entries", accountID, "")
	if tradesBefore == 0 || ledgerBefore == 0 {
		t.Fatalf("expected audit rows before deletion: trades=%d ledger=%d", tradesBefore, ledgerBefore)
	}

	if status, raw := backend.request(t, http.MethodDelete, "/v1/accounts/"+accountID, tradingUserToken, nil); status != http.StatusNoContent {
		t.Fatalf("delete Account status=%d body=%s", status, raw)
	}

	if got := countAccountRows(t, backend, "positions", accountID, ""); got != 0 {
		t.Fatalf("positions survived deletion: %d", got)
	}
	if got := countAccountRows(t, backend, "orders", accountID, "status = 'open'"); got != 0 {
		t.Fatalf("open orders survived deletion: %d", got)
	}
	if status, _ := backend.request(t, http.MethodGet, "/v1/accounts/"+accountID, tradingUserToken, nil); status != http.StatusNotFound {
		t.Fatalf("deleted Account still readable: status=%d", status)
	}

	// 審計紀錄保留：成交與 Ledger 不隨帳號刪除消失。
	if got := countAccountRows(t, backend, "trades", accountID, ""); got != tradesBefore {
		t.Fatalf("trades = %d, want %d kept for audit", got, tradesBefore)
	}
	if got := countAccountRows(t, backend, "ledger_entries", accountID, ""); got != ledgerBefore {
		t.Fatalf("ledger entries = %d, want %d kept for audit", got, ledgerBefore)
	}

	// 別人的部位不能被波及。
	if positions := listPositions(t, backend, otherToken).Positions; len(positions) != 1 {
		t.Fatalf("bystander Account lost its position: %+v", positions)
	}

	// 引擎再也掃不到它，價格穿過原本的停利也不該出現錯誤。
	backend.setPrice(t, 45000)
	if result := backend.engine.Tick(context.Background()); result.Errors != 0 || result.Closed != 0 {
		t.Fatalf("tick after deletion = %+v", result)
	}
}

// 停用帳號的停損停利無法成交是帳號自己的狀態，不是引擎故障；記成錯誤的話撮合每輪
// 都會重撞一次並污染 tick 的錯誤計數。
func TestDisabledAccountStopTriggerIsNotAnEngineError(t *testing.T) {
	backend := newTradingAPI(t, "")
	accountID, token := backend.createAccount(t, "disabled-owner", 100000)
	backend.setPrice(t, 60000)
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "sell", "type": "market",
		"quantity": 0.1, "leverage": 10, "take_profit": 50000, "source": agentSource("s_1", 1),
	})
	if status, raw := backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{"status": "disabled"}); status != http.StatusOK {
		t.Fatalf("disable Account status=%d body=%s", status, raw)
	}

	backend.setPrice(t, 45000)
	for round := 0; round < 2; round++ {
		if result := backend.engine.Tick(context.Background()); result.Errors != 0 || result.Closed != 0 {
			t.Fatalf("tick %d on disabled Account = %+v", round, result)
		}
	}
	if got := countAccountRows(t, backend, "positions", accountID, ""); got != 1 {
		t.Fatalf("position count while disabled = %d, want 1", got)
	}

	// 重新啟用後就能正常觸發，代表停用期間只是跳過而不是把 stop 弄丟。
	if status, raw := backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{"status": "active"}); status != http.StatusOK {
		t.Fatalf("re-enable Account status=%d body=%s", status, raw)
	}
	if result := backend.engine.Tick(context.Background()); result.Closed != 1 || result.Errors != 0 {
		t.Fatalf("tick after re-enabling = %+v", result)
	}
	if positions := listPositions(t, backend, token).Positions; len(positions) != 0 {
		t.Fatalf("position survived the take_profit: %+v", positions)
	}
}
