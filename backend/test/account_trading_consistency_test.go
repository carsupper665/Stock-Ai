package test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
	"backend/internal/trading"
)

type sqlBarrier struct {
	mu      sync.Mutex
	match   string
	reached chan struct{}
	release chan struct{}
}

func (b *sqlBarrier) arm(match string) (<-chan struct{}, chan<- struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.match = match
	b.reached = make(chan struct{})
	b.release = make(chan struct{})
	return b.reached, b.release
}

func (b *sqlBarrier) Write(p []byte) (int, error) {
	b.mu.Lock()
	if b.match == "" || !bytes.Contains(p, []byte(b.match)) {
		b.mu.Unlock()
		return len(p), nil
	}
	reached, release := b.reached, b.release
	b.match = ""
	close(reached)
	b.mu.Unlock()
	<-release
	return len(p), nil
}

func waitSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func newBarrierTradingAPI(t *testing.T) (*tradingAPI, *sqlBarrier) {
	t.Helper()
	barrier := &sqlBarrier{}
	dbLogger, err := logging.New("review-db", t.TempDir(), 10000, true)
	if err != nil {
		t.Fatalf("open database logger: %v", err)
	}
	dbLogger.SetConsole(barrier)
	t.Cleanup(func() { _ = dbLogger.Close() })
	dbPath := filepath.Join(t.TempDir(), "review.db")
	store, err := database.Open(&config.Config{DBDriver: "sqlite", DBDSN: dbPath, Debug: true}, dbLogger)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newTradingAPIWithStore(t, store, dbPath), barrier
}

type adminAccountView struct {
	ID       string  `json:"id"`
	UserName string  `json:"user_name"`
	Token    string  `json:"token"`
	Balance  float64 `json:"balance"`
	Status   string  `json:"status"`
}

func getAdminAccount(t *testing.T, backend *tradingAPI, accountID string) (int, adminAccountView) {
	t.Helper()
	status, raw := backend.request(t, http.MethodGet, "/v1/accounts/"+accountID, tradingUserToken, nil)
	if status == http.StatusNotFound {
		return status, adminAccountView{}
	}
	if status != http.StatusOK {
		t.Fatalf("get Account status=%d body=%s", status, raw)
	}
	return status, decodeJSON[adminAccountView](t, raw)
}

func TestAccountMutationUpdateCannotOverwriteANewerTradingBalance(t *testing.T) {
	backend, barrier := newBarrierTradingAPI(t)
	accountID, token := backend.createAccount(t, "balance-owner", 100000)

	reached, release := barrier.arm("FROM `accounts` WHERE id =")
	updateDone := make(chan struct{})
	go func() {
		defer close(updateDone)
		status, raw := backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{"user_name": "balance-owner-updated"})
		if status != http.StatusOK {
			t.Errorf("update Account status=%d body=%s", status, raw)
		}
	}()
	waitSignal(t, reached, "Account update did not reach its account read")

	orderDone := make(chan struct{})
	go func() {
		defer close(orderDone)
		status, raw := backend.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
			"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10,
		})
		if status != http.StatusCreated {
			t.Errorf("place market order status=%d body=%s", status, raw)
		}
	}()
	time.Sleep(25 * time.Millisecond)
	close(release)
	waitSignal(t, updateDone, "Account update did not finish")
	waitSignal(t, orderDone, "market order did not finish")

	_, account := getAdminAccount(t, backend, accountID)
	if account.UserName != "balance-owner-updated" || account.Balance != 99997.6 {
		t.Fatalf("Account update overwrote newer fields: %+v", account)
	}
}

func TestAccountMutationResetTokenCannotOverwriteANewerUserName(t *testing.T) {
	backend, barrier := newBarrierTradingAPI(t)
	accountID, oldToken := backend.createAccount(t, "token-owner", 100000)

	reached, release := barrier.arm("FROM `accounts` WHERE id =")
	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		status, raw := backend.request(t, http.MethodPost, "/v1/accounts/"+accountID+"/token/reset", tradingUserToken, nil)
		if status != http.StatusOK {
			t.Errorf("reset token status=%d body=%s", status, raw)
		}
	}()
	waitSignal(t, reached, "token reset did not reach its account read")

	updateDone := make(chan struct{})
	go func() {
		defer close(updateDone)
		status, raw := backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{"user_name": "latest-owner"})
		if status != http.StatusOK {
			t.Errorf("update Account status=%d body=%s", status, raw)
		}
	}()
	time.Sleep(25 * time.Millisecond)
	close(release)
	waitSignal(t, resetDone, "token reset did not finish")
	waitSignal(t, updateDone, "Account update did not finish")

	_, account := getAdminAccount(t, backend, accountID)
	if account.UserName != "latest-owner" || account.Token == oldToken {
		t.Fatalf("token reset overwrote a newer Account field: %+v", account)
	}
}

func TestAccountMutationUpdateCannotResurrectADeletedAccount(t *testing.T) {
	backend, barrier := newBarrierTradingAPI(t)
	accountID, _ := backend.createAccount(t, "delete-owner", 100000)

	reached, release := barrier.arm("FROM `accounts` WHERE id =")
	updateDone := make(chan struct{})
	go func() {
		defer close(updateDone)
		backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{"user_name": "must-not-return"})
	}()
	waitSignal(t, reached, "Account update did not reach its account read")

	deleteDone := make(chan struct{})
	go func() {
		defer close(deleteDone)
		status, raw := backend.request(t, http.MethodDelete, "/v1/accounts/"+accountID, tradingUserToken, nil)
		if status != http.StatusNoContent {
			t.Errorf("delete Account status=%d body=%s", status, raw)
		}
	}()
	time.Sleep(25 * time.Millisecond)
	close(release)
	waitSignal(t, updateDone, "Account update did not finish")
	waitSignal(t, deleteDone, "Account deletion did not finish")

	if status, account := getAdminAccount(t, backend, accountID); status != http.StatusNotFound {
		t.Fatalf("stale update resurrected deleted Account: %+v", account)
	}
}

func TestAccountMutationResetTokenCannotOverwriteANewerTradingBalance(t *testing.T) {
	backend, barrier := newBarrierTradingAPI(t)
	accountID, token := backend.createAccount(t, "reset-balance-owner", 100000)

	reached, release := barrier.arm("FROM `accounts` WHERE id =")
	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		status, raw := backend.request(t, http.MethodPost, "/v1/accounts/"+accountID+"/token/reset", tradingUserToken, nil)
		if status != http.StatusOK {
			t.Errorf("reset token status=%d body=%s", status, raw)
		}
	}()
	waitSignal(t, reached, "token reset did not reach its account read")

	orderDone := make(chan error, 1)
	go func() {
		order := validLimitOrder()
		order.Type = database.OrderMarket
		order.Price = 0
		_, err := backend.trader.PlaceOrder(context.Background(), accountID, order)
		orderDone <- err
	}()
	time.Sleep(25 * time.Millisecond)
	close(release)
	waitSignal(t, resetDone, "token reset did not finish")
	var orderErr error
	select {
	case orderErr = <-orderDone:
	case <-time.After(2 * time.Second):
		t.Fatal("market order did not finish")
	}
	if orderErr != nil {
		t.Fatalf("market order failed: %v", orderErr)
	}

	_, account := getAdminAccount(t, backend, accountID)
	if account.Token == token || account.Balance != 99997.6 {
		t.Fatalf("token reset overwrote newer trading state: %+v", account)
	}
}

func TestAccountMutationDisableSerializesWithFill(t *testing.T) {
	backend, barrier := newBarrierTradingAPI(t)
	accountID, _ := backend.createAccount(t, "disable-owner", 100000)

	reached, release := barrier.arm("FROM `accounts` WHERE id =")
	disableDone := make(chan struct{})
	go func() {
		defer close(disableDone)
		status, raw := backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{"status": database.AccountDisabled})
		if status != http.StatusOK {
			t.Errorf("disable Account status=%d body=%s", status, raw)
		}
	}()
	waitSignal(t, reached, "disable did not reach its account read")

	orderDone := make(chan error, 1)
	go func() {
		order := validLimitOrder()
		order.Type = database.OrderMarket
		order.Price = 0
		_, err := backend.trader.PlaceOrder(context.Background(), accountID, order)
		orderDone <- err
	}()
	time.Sleep(25 * time.Millisecond)
	close(release)
	waitSignal(t, disableDone, "disable did not finish")
	var orderErr error
	select {
	case orderErr = <-orderDone:
	case <-time.After(2 * time.Second):
		t.Fatal("market order did not finish")
	}
	if !errors.Is(orderErr, trading.ErrAccountDisabled) {
		t.Fatalf("fill racing disable error=%v, want ErrAccountDisabled", orderErr)
	}
	_, account := getAdminAccount(t, backend, accountID)
	if account.Balance != 100000 {
		t.Fatalf("rejected fill changed balance: %+v", account)
	}
	entries, err := backend.store.ListLedger(context.Background(), accountID, database.LedgerFilter{Limit: 10})
	if err != nil || len(entries) != 0 {
		t.Fatalf("fill racing disable left ledger effects: entries=%+v err=%v", entries, err)
	}
}

func TestAccountMutationFailurePreservesAllPreviousFields(t *testing.T) {
	backend := newTradingAPI(t, "")
	accountID, token := backend.createAccount(t, "unchanged-owner", 100000)
	backend.createAccount(t, "taken-owner", 100000)

	status, raw := backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{
		"user_name": "taken-owner", "status": database.AccountDisabled,
	})
	if status != http.StatusConflict {
		t.Fatalf("duplicate update status=%d body=%s", status, raw)
	}
	_, account := getAdminAccount(t, backend, accountID)
	if account.UserName != "unchanged-owner" || account.Token != token || account.Balance != 100000 || account.Status != database.AccountActive {
		t.Fatalf("failed update changed Account fields: %+v", account)
	}
}

func validLimitOrder() trading.PlaceOrderInput {
	return trading.PlaceOrderInput{
		Market: "crypto", Symbol: "BTCUSDT", Product: database.ProductFutures,
		Side: database.SideBuy, Type: database.OrderLimit, Quantity: 0.1, Price: 50000, Leverage: 10,
	}
}

func TestLimitOrderRequiresAnExistingActiveAccount(t *testing.T) {
	backend := newTradingAPI(t, "")
	accountID, _ := backend.createAccount(t, "disabled-limit", 100000)
	if status, raw := backend.request(t, http.MethodPatch, "/v1/accounts/"+accountID, tradingUserToken, map[string]any{"status": database.AccountDisabled}); status != http.StatusOK {
		t.Fatalf("disable Account status=%d body=%s", status, raw)
	}

	if _, err := backend.trader.PlaceOrder(context.Background(), accountID, validLimitOrder()); !errors.Is(err, trading.ErrAccountDisabled) {
		t.Fatalf("disabled Account limit order error=%v, want ErrAccountDisabled", err)
	}
	if _, err := backend.trader.PlaceOrder(context.Background(), "acc_missing", validLimitOrder()); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("missing Account limit order error=%v, want ErrNotFound", err)
	}
	for _, id := range []string{accountID, "acc_missing"} {
		orders, err := backend.store.ListOrders(context.Background(), id, "", 10)
		if err != nil || len(orders) != 0 {
			t.Fatalf("rejected limit order left orders for %s: orders=%+v err=%v", id, orders, err)
		}
		entries, err := backend.store.ListLedger(context.Background(), id, database.LedgerFilter{Limit: 10})
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected limit order left ledger for %s: entries=%+v err=%v", id, entries, err)
		}
	}
}

func TestLimitOrderRacingDeletionCannotCreateOrphans(t *testing.T) {
	backend, barrier := newBarrierTradingAPI(t)
	accountID, _ := backend.createAccount(t, "deleted-limit", 100000)

	reached, release := barrier.arm("DELETE FROM `positions`")
	deleteDone := make(chan struct{})
	go func() {
		defer close(deleteDone)
		status, raw := backend.request(t, http.MethodDelete, "/v1/accounts/"+accountID, tradingUserToken, nil)
		if status != http.StatusNoContent {
			t.Errorf("delete Account status=%d body=%s", status, raw)
		}
	}()
	waitSignal(t, reached, "Account deletion did not enter its transaction")

	orderDone := make(chan error, 1)
	go func() {
		_, err := backend.trader.PlaceOrder(context.Background(), accountID, validLimitOrder())
		orderDone <- err
	}()
	time.Sleep(25 * time.Millisecond)
	close(release)
	waitSignal(t, deleteDone, "Account deletion did not finish")
	var orderErr error
	select {
	case orderErr = <-orderDone:
	case <-time.After(2 * time.Second):
		t.Fatal("limit order did not finish")
	}
	if !errors.Is(orderErr, database.ErrNotFound) {
		t.Fatalf("limit order after committed deletion error=%v, want ErrNotFound", orderErr)
	}
	orders, err := backend.store.ListOrders(context.Background(), accountID, "", 10)
	if err != nil || len(orders) != 0 {
		t.Fatalf("limit order racing deletion left orphan orders: orders=%+v err=%v", orders, err)
	}
	entries, err := backend.store.ListLedger(context.Background(), accountID, database.LedgerFilter{Limit: 10})
	if err != nil || len(entries) != 0 {
		t.Fatalf("limit order racing deletion left orphan ledger: entries=%+v err=%v", entries, err)
	}
}
