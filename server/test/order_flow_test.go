package test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"server/model"
	"server/model/store"
	"server/sandbox"
	"server/service"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestOrderServiceRejectsInsufficientBalance(t *testing.T) {
	env := newOrderFlowEnv(t)

	_, err := env.orders.SubmitOrder(context.Background(), service.SubmitOrderInput{
		RunID:         env.runID,
		OwnerID:       env.ownerID,
		ClientOrderID: "order-1",
		Symbol:        env.symbol,
		Side:          sandbox.OrderSideBuy,
		Type:          sandbox.OrderTypeMarket,
		Quantity:      1,
	})
	if !errors.Is(err, service.ErrInsufficientBalance) {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

func TestOrderServiceAdvanceAndFillUpdatesWalletsAndLedger(t *testing.T) {
	env := newOrderFlowEnv(t)
	ctx := context.Background()

	if err := env.accounts.Deposit(ctx, env.runID, env.ownerID, "USDT", 200, "seed capital"); err != nil {
		t.Fatalf("Deposit failed: %v", err)
	}

	order, err := env.orders.SubmitOrder(ctx, service.SubmitOrderInput{
		RunID:         env.runID,
		OwnerID:       env.ownerID,
		ClientOrderID: "order-1",
		Symbol:        env.symbol,
		Side:          sandbox.OrderSideBuy,
		Type:          sandbox.OrderTypeMarket,
		Quantity:      1,
	})
	if err != nil {
		t.Fatalf("SubmitOrder failed: %v", err)
	}

	fills, state, err := env.orders.AdvanceAndFill(ctx, env.runID)
	if err != nil {
		t.Fatalf("AdvanceAndFill failed: %v", err)
	}
	if state.Cursor != 1 {
		t.Fatalf("expected cursor 1 after advance, got %d", state.Cursor)
	}
	if len(fills) != 1 {
		t.Fatalf("expected one fill, got %d", len(fills))
	}

	fill := fills[0]
	assertApprox(t, fill.Price, 101.55075)
	assertApprox(t, fill.FeeAmount, 0.10155075)

	storedOrder := findOrder(t, env.db, order.ID)
	if storedOrder.Status != sandbox.OrderStatusFilled {
		t.Fatalf("expected filled order, got %s", storedOrder.Status)
	}

	wallets, err := env.accounts.Balances(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Balances failed: %v", err)
	}
	walletByAsset := indexWallets(wallets)
	assertApprox(t, walletByAsset["BTC"].Balance, 1)
	assertApprox(t, walletByAsset["BTC"].Available, 1)
	assertApprox(t, walletByAsset["BTC"].Frozen, 0)
	assertApprox(t, walletByAsset["USDT"].Balance, 98.34769925)
	assertApprox(t, walletByAsset["USDT"].Available, 98.34769925)
	assertApprox(t, walletByAsset["USDT"].Frozen, 0)

	ledger, err := env.accounts.Ledger(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Ledger failed: %v", err)
	}
	if len(ledger) != 4 {
		t.Fatalf("expected 4 ledger entries, got %d", len(ledger))
	}
	if ledger[0].EntryType != "deposit" || ledger[1].EntryType != "trade_buy_quote_out" || ledger[2].EntryType != "trade_fee" || ledger[3].EntryType != "fill_base_in" {
		t.Fatalf("unexpected ledger entry sequence: %#v", ledger)
	}

	openOrders, err := env.orders.OpenOrders(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("OpenOrders failed: %v", err)
	}
	if len(openOrders) != 0 {
		t.Fatalf("expected no open orders, got %d", len(openOrders))
	}

	storedRun := findRunSession(t, env.db, env.runID)
	if storedRun.Cursor != 1 {
		t.Fatalf("expected persisted cursor 1, got %d", storedRun.Cursor)
	}
}

func TestOrderServiceCancelReleasesReservation(t *testing.T) {
	env := newOrderFlowEnv(t)
	ctx := context.Background()
	limitPrice := 90.0

	if err := env.accounts.Deposit(ctx, env.runID, env.ownerID, "USDT", 200, "seed capital"); err != nil {
		t.Fatalf("Deposit failed: %v", err)
	}

	order, err := env.orders.SubmitOrder(ctx, service.SubmitOrderInput{
		RunID:         env.runID,
		OwnerID:       env.ownerID,
		ClientOrderID: "order-2",
		Symbol:        env.symbol,
		Side:          sandbox.OrderSideBuy,
		Type:          sandbox.OrderTypeLimit,
		Quantity:      1,
		Price:         &limitPrice,
	})
	if err != nil {
		t.Fatalf("SubmitOrder failed: %v", err)
	}

	if err := env.orders.CancelOrder(ctx, env.runID, env.ownerID, order.ID); err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}

	storedOrder := findOrder(t, env.db, order.ID)
	if storedOrder.Status != sandbox.OrderStatusCanceled {
		t.Fatalf("expected canceled order, got %s", storedOrder.Status)
	}

	wallets, err := env.accounts.Balances(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Balances failed: %v", err)
	}
	wallet := indexWallets(wallets)["USDT"]
	assertApprox(t, wallet.Balance, 200)
	assertApprox(t, wallet.Available, 200)
	assertApprox(t, wallet.Frozen, 0)
}

func TestOrderServiceAdvanceRollbackRestoresClockAndLeavesNoPartialState(t *testing.T) {
	hookErr := errors.New("forced tx failure")
	env := newOrderFlowEnv(t, service.WithBeforeStepCommitHook(func(context.Context, *gorm.DB) error {
		return hookErr
	}))
	ctx := context.Background()

	if err := env.accounts.Deposit(ctx, env.runID, env.ownerID, "USDT", 200, "seed capital"); err != nil {
		t.Fatalf("Deposit failed: %v", err)
	}

	order, err := env.orders.SubmitOrder(ctx, service.SubmitOrderInput{
		RunID:         env.runID,
		OwnerID:       env.ownerID,
		ClientOrderID: "order-3",
		Symbol:        env.symbol,
		Side:          sandbox.OrderSideBuy,
		Type:          sandbox.OrderTypeMarket,
		Quantity:      1,
	})
	if err != nil {
		t.Fatalf("SubmitOrder failed: %v", err)
	}

	beforeWallets, err := env.accounts.Balances(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Balances failed: %v", err)
	}
	beforeLedger, err := env.accounts.Ledger(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Ledger failed: %v", err)
	}

	_, _, err = env.orders.AdvanceAndFill(ctx, env.runID)
	if !errors.Is(err, hookErr) {
		t.Fatalf("expected hook error, got %v", err)
	}

	engineState, err := env.engine.State()
	if err != nil {
		t.Fatalf("engine State failed: %v", err)
	}
	if engineState.Cursor != 0 {
		t.Fatalf("expected engine cursor restored to 0, got %d", engineState.Cursor)
	}

	storedRun := findRunSession(t, env.db, env.runID)
	if storedRun.Cursor != 0 {
		t.Fatalf("expected persisted cursor to stay 0, got %d", storedRun.Cursor)
	}

	storedOrder := findOrder(t, env.db, order.ID)
	if storedOrder.Status != sandbox.OrderStatusOpen {
		t.Fatalf("expected order to remain open, got %s", storedOrder.Status)
	}

	fills, err := env.orders.Fills(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Fills failed: %v", err)
	}
	if len(fills) != 0 {
		t.Fatalf("expected no fills after rollback, got %d", len(fills))
	}

	afterWallets, err := env.accounts.Balances(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Balances failed: %v", err)
	}
	afterLedger, err := env.accounts.Ledger(ctx, env.runID, env.ownerID)
	if err != nil {
		t.Fatalf("Ledger failed: %v", err)
	}

	if !reflectWalletsEqual(beforeWallets, afterWallets) {
		t.Fatalf("expected wallets to be unchanged, before=%#v after=%#v", beforeWallets, afterWallets)
	}
	if len(beforeLedger) != len(afterLedger) {
		t.Fatalf("expected ledger count to stay %d, got %d", len(beforeLedger), len(afterLedger))
	}
}

type orderFlowEnv struct {
	db       *gorm.DB
	engine   *sandbox.ReplayEngine
	accounts *service.AccountService
	orders   *service.OrderService
	runID    string
	ownerID  uint
	symbol   string
}

func newOrderFlowEnv(t *testing.T, opts ...service.OrderServiceOption) *orderFlowEnv {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "order-flow.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB failed: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	if err := model.Migrate(db); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	feed := sandbox.NewReplayFeed()
	scenarioPath := filepath.Join("..", "testdata", "replay", "btcusdt_1m.json")
	if err := feed.LoadScenarioFile(scenarioPath); err != nil {
		t.Fatalf("LoadScenarioFile failed: %v", err)
	}
	engine, err := sandbox.NewReplayEngine(feed)
	if err != nil {
		t.Fatalf("NewReplayEngine failed: %v", err)
	}

	if err := db.Create(&store.SymbolConfig{
		Symbol:       "BTCUSDT",
		BaseAsset:    "BTC",
		QuoteAsset:   "USDT",
		PriceTick:    0.01,
		QuantityStep: 1,
		MinQuantity:  1,
		MinNotional:  10,
		MakerFeeBps:  5,
		TakerFeeBps:  10,
		IsActive:     true,
	}).Error; err != nil {
		t.Fatalf("create symbol config failed: %v", err)
	}

	if err := db.Create(&store.RunSession{
		RunID:                 "run-1",
		ScenarioID:            "btc-usdt-1m-sample",
		DatasetHash:           "dataset-hash-v1",
		Stage:                 string(sandbox.StageExecution),
		Cursor:                0,
		TrainStart:            0,
		TrainEnd:              0,
		ValidationStart:       1,
		ValidationEnd:         1,
		OOSStart:              2,
		OOSEnd:                2,
		ExecutionModelVersion: "exec-v1",
		FeeModelVersion:       "fee-v1",
		SlippageModelVersion:  "slippage-v1",
	}).Error; err != nil {
		t.Fatalf("create run session failed: %v", err)
	}

	accounts := service.NewAccountService(db)
	orders := service.NewOrderService(db, accounts, engine, opts...)

	return &orderFlowEnv{
		db:       db,
		engine:   engine,
		accounts: accounts,
		orders:   orders,
		runID:    "run-1",
		ownerID:  7,
		symbol:   "BTCUSDT",
	}
}

func findOrder(t *testing.T, db *gorm.DB, orderID uint) store.Order {
	t.Helper()
	var order store.Order
	if err := db.Where("id = ?", orderID).First(&order).Error; err != nil {
		t.Fatalf("find order failed: %v", err)
	}
	return order
}

func findRunSession(t *testing.T, db *gorm.DB, runID string) store.RunSession {
	t.Helper()
	var run store.RunSession
	if err := db.Where("run_id = ?", runID).First(&run).Error; err != nil {
		t.Fatalf("find run session failed: %v", err)
	}
	return run
}

func indexWallets(wallets []store.Wallet) map[string]store.Wallet {
	indexed := make(map[string]store.Wallet, len(wallets))
	for _, wallet := range wallets {
		indexed[wallet.Asset] = wallet
	}
	return indexed
}

func assertApprox(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("expected %.8f, got %.8f", want, got)
	}
}

func reflectWalletsEqual(a, b []store.Wallet) bool {
	if len(a) != len(b) {
		return false
	}
	mapA := indexWallets(a)
	mapB := indexWallets(b)
	for asset, walletA := range mapA {
		walletB, ok := mapB[asset]
		if !ok {
			return false
		}
		if math.Abs(walletA.Balance-walletB.Balance) > 1e-9 || math.Abs(walletA.Available-walletB.Available) > 1e-9 || math.Abs(walletA.Frozen-walletB.Frozen) > 1e-9 {
			return false
		}
	}
	return true
}
