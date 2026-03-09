package service

import (
	"context"
	"testing"
	"time"

	"server/model"
	"server/model/store"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestApp(t *testing.T) *App {
	t.Helper()

	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := model.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	app, err := NewApp(AppConfig{
		DB:            db,
		SessionSecret: "test-secret",
		FrontendURL:   "http://localhost:3000",
		Now: func() time.Time {
			return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return app
}

func seedReplaySandbox(t *testing.T, db *gorm.DB, sandboxID, accountID string) {
	t.Helper()

	dataset := store.ReplayDataset{
		ID:       "dataset-btc",
		Name:     "BTC Replay",
		Symbol:   "BTCUSDT",
		Interval: "1m",
		StartAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		EndAt:    time.Date(2025, 1, 1, 0, 2, 0, 0, time.UTC),
		Source:   "test",
	}
	if err := db.Create(&dataset).Error; err != nil {
		t.Fatalf("create dataset: %v", err)
	}

	klines := []store.ReplayKline{
		{DatasetID: dataset.ID, Symbol: dataset.Symbol, Ts: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Open: 100, High: 100, Low: 100, Close: 100, Volume: 1},
		{DatasetID: dataset.ID, Symbol: dataset.Symbol, Ts: time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC), Open: 95, High: 95, Low: 95, Close: 95, Volume: 1},
	}
	if err := db.Create(&klines).Error; err != nil {
		t.Fatalf("create klines: %v", err)
	}

	sandbox := store.Sandbox{
		ID:                sandboxID,
		Name:              "sandbox",
		Mode:              "replay",
		Status:            store.SandboxStatusPaused,
		StartDatetime:     klines[0].Ts,
		ReplayCurrentTime: klines[0].Ts,
		ReplaySpeed:       1,
		DatasetID:         dataset.ID,
	}
	if err := db.Create(&sandbox).Error; err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	account := store.Account{
		ID:               accountID,
		SandboxID:        &sandboxID,
		Name:             "agent",
		Type:             store.AccountTypeVirtual,
		BaseCurrency:     "USD",
		InitialBalance:   1000,
		WalletBalance:    1000,
		AvailableBalance: 1000,
		Equity:           1000,
		Status:           store.AccountStatusActive,
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}
}

func TestTokenServiceCreateVerifyAndRotate(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-token", "account-token")

	ctx := context.Background()
	issued, record, err := app.Tokens.Create(ctx, CreateTokenInput{
		AccountID: "account-token",
		Name:      "agent",
		Scopes:    []string{"trade:read", "market:read"},
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if issued == "" {
		t.Fatalf("expected plaintext token")
	}
	if record.TokenHash == "" {
		t.Fatalf("expected token hash to be stored")
	}

	verified, account, err := app.Tokens.Verify(ctx, issued, []string{"trade:read"})
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if verified.ID != record.ID {
		t.Fatalf("verify returned wrong token id: got %s want %s", verified.ID, record.ID)
	}
	if account.ID != "account-token" {
		t.Fatalf("verify returned wrong account: %s", account.ID)
	}

	if _, _, err := app.Tokens.Verify(ctx, issued, []string{"trade:write"}); err == nil {
		t.Fatalf("expected missing scope failure")
	}

	rotatedPlaintext, rotatedRecord, err := app.Tokens.Rotate(ctx, record.ID)
	if err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	if rotatedRecord.ID != record.ID {
		t.Fatalf("rotate should keep same token id")
	}
	if rotatedPlaintext == issued {
		t.Fatalf("rotate should issue a new secret")
	}

	if _, _, err := app.Tokens.Verify(ctx, issued, []string{"trade:read"}); err == nil {
		t.Fatalf("expected old token secret to be invalid after rotation")
	}
	if _, _, err := app.Tokens.Verify(ctx, rotatedPlaintext, []string{"trade:read"}); err != nil {
		t.Fatalf("verify rotated token: %v", err)
	}
}

func TestTradingServiceMarketOrderAndStopLoss(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-trade", "account-trade")

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-trade"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}

	marketOrder, err := app.Trading.PlaceOrder(ctx, "account-trade", PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeMarket,
		Quantity:     1,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("place market order: %v", err)
	}
	if marketOrder.Status != store.OrderStatusFilled {
		t.Fatalf("market order should be filled immediately, got %s", marketOrder.Status)
	}

	positions, err := app.Trading.ListPositions(ctx, "account-trade")
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}
	if positions[0].Quantity != 1 || positions[0].EntryPrice != 100 {
		t.Fatalf("unexpected position: %+v", positions[0])
	}

	stopOrder, err := app.Trading.PlaceOrder(ctx, "account-trade", PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideSell,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeStop,
		Quantity:     1,
		StopPrice:    96,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("place stop loss: %v", err)
	}
	if stopOrder.Status != store.OrderStatusNew {
		t.Fatalf("stop order should remain new until triggered, got %s", stopOrder.Status)
	}

	moved, err := app.Sandboxes.UpdateReplayTime(ctx, "sandbox-trade", time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("update replay time: %v", err)
	}
	if moved.ReplayCurrentTime.IsZero() {
		t.Fatalf("sandbox replay time should be updated")
	}

	if err := app.Trading.ProcessSandbox(ctx, "sandbox-trade"); err != nil {
		t.Fatalf("process sandbox: %v", err)
	}

	positions, err = app.Trading.ListPositions(ctx, "account-trade")
	if err != nil {
		t.Fatalf("list positions after stop: %v", err)
	}
	if len(positions) != 0 {
		t.Fatalf("expected stop loss to close position, got %+v", positions)
	}

	account, err := app.Accounts.Get(ctx, "account-trade")
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.RealizedPnL != -5 {
		t.Fatalf("unexpected realized pnl: got %.2f want -5", account.RealizedPnL)
	}
	if account.WalletBalance != 995 {
		t.Fatalf("unexpected wallet balance: got %.2f want 995", account.WalletBalance)
	}
}

func TestTradingServiceLimitOrderCancelAndFill(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-limit", "account-limit")

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-limit"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}

	pending, err := app.Trading.PlaceOrder(ctx, "account-limit", PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeLimit,
		Quantity:     1,
		Price:        99,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("place limit order: %v", err)
	}
	if pending.Status != store.OrderStatusNew {
		t.Fatalf("expected new limit order, got %s", pending.Status)
	}

	canceled, err := app.Trading.CancelOrder(ctx, "account-limit", pending.ID)
	if err != nil {
		t.Fatalf("cancel limit order: %v", err)
	}
	if canceled.Status != store.OrderStatusCanceled {
		t.Fatalf("expected canceled order, got %s", canceled.Status)
	}

	fillable, err := app.Trading.PlaceOrder(ctx, "account-limit", PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeLimit,
		Quantity:     1,
		Price:        99,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("place second limit order: %v", err)
	}
	if _, err := app.Sandboxes.UpdateReplayTime(ctx, "sandbox-limit", time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC)); err != nil {
		t.Fatalf("update replay time: %v", err)
	}
	if err := app.Trading.ProcessSandbox(ctx, "sandbox-limit"); err != nil {
		t.Fatalf("process sandbox: %v", err)
	}
	order, err := app.Trading.GetOrder(ctx, fillable.ID)
	if err != nil {
		t.Fatalf("get filled limit order: %v", err)
	}
	if order.Status != store.OrderStatusFilled || order.AvgFillPrice != 95 {
		t.Fatalf("unexpected filled limit order: %+v", order)
	}
}

func TestTradingServiceShortPositionLifecycle(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-short", "account-short")

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-short"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}

	opened, err := app.Trading.PlaceOrder(ctx, "account-short", PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideSell,
		PositionSide: store.PositionSideShort,
		OrderType:    store.OrderTypeMarket,
		Quantity:     1,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("open short: %v", err)
	}
	if opened.Status != store.OrderStatusFilled {
		t.Fatalf("expected short order to fill, got %s", opened.Status)
	}

	if _, err := app.Sandboxes.UpdateReplayTime(ctx, "sandbox-short", time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC)); err != nil {
		t.Fatalf("update replay time: %v", err)
	}
	if err := app.Trading.ProcessSandbox(ctx, "sandbox-short"); err != nil {
		t.Fatalf("process sandbox: %v", err)
	}
	account, err := app.Trading.GetAccountSummary(ctx, "account-short")
	if err != nil {
		t.Fatalf("get account summary: %v", err)
	}
	if account.UnrealizedPnL != 5 {
		t.Fatalf("expected unrealized pnl of 5, got %.2f", account.UnrealizedPnL)
	}

	closed, err := app.Trading.PlaceOrder(ctx, "account-short", PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideShort,
		OrderType:    store.OrderTypeMarket,
		Quantity:     1,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("close short: %v", err)
	}
	if closed.Status != store.OrderStatusFilled {
		t.Fatalf("expected close order to fill, got %s", closed.Status)
	}

	account, err = app.Accounts.Get(ctx, "account-short")
	if err != nil {
		t.Fatalf("get account after close: %v", err)
	}
	if account.RealizedPnL != 5 || account.WalletBalance != 1005 {
		t.Fatalf("unexpected short close account state: %+v", account)
	}
}

func TestSandboxReplayControlSeekRepricesMarketAndProcessesPendingOrders(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-replay", "account-replay")

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-replay"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	order, err := app.Trading.PlaceOrder(ctx, "account-replay", PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeLimit,
		Quantity:     1,
		Price:        99,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("place limit order: %v", err)
	}
	if order.Status != store.OrderStatusNew {
		t.Fatalf("expected pending order, got %+v", order)
	}

	status, err := app.Sandboxes.Seek(ctx, "sandbox-replay", time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("seek replay time: %v", err)
	}
	if !status.CurrentTime.Equal(time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC)) {
		t.Fatalf("unexpected replay status after seek: %+v", status)
	}

	order, err = app.Trading.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order after seek: %v", err)
	}
	if order.Status != store.OrderStatusFilled {
		t.Fatalf("expected seek to fill pending limit order, got %+v", order)
	}
}

func TestSandboxReplayControlResumeAndSpeed(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-control", "account-control")

	ctx := context.Background()
	speed, err := app.Sandboxes.SetReplaySpeed(ctx, "sandbox-control", 4)
	if err != nil {
		t.Fatalf("set replay speed: %v", err)
	}
	if speed.Speed != 4 || speed.Status != store.SandboxStatusPaused {
		t.Fatalf("unexpected speed status: %+v", speed)
	}

	resumed, err := app.Sandboxes.Resume(ctx, "sandbox-control")
	if err != nil {
		t.Fatalf("resume sandbox: %v", err)
	}
	if resumed.Status != store.SandboxStatusRunning {
		t.Fatalf("unexpected resumed status: %+v", resumed)
	}

	sandbox, err := app.Sandboxes.Get(ctx, "sandbox-control")
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if sandbox.RuntimeAnchorAt == nil || sandbox.ReplaySpeed != 4 {
		t.Fatalf("unexpected sandbox after resume: %+v", sandbox)
	}
}

func TestLiveAccountCRUDAndTokenIssuance(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	live, err := app.Accounts.Create(ctx, CreateAccountInput{
		Name:              "binance-paper-1",
		InitialBalance:    1000,
		Type:              store.AccountTypeLive,
		Provider:          "binance",
		Environment:       "paper",
		PriceMode:         "live",
		CredentialsStatus: "healthy",
		SupportedSymbols:  []string{"BTCUSDT", "ETHUSDT"},
	})
	if err != nil {
		t.Fatalf("create live account: %v", err)
	}
	if live.Type != store.AccountTypeLive || live.SandboxID != nil || live.Provider != "binance" {
		t.Fatalf("unexpected live account: %+v", live)
	}

	token, _, err := app.Tokens.Create(ctx, CreateTokenInput{
		AccountID: live.ID,
		Name:      "live-token",
		Scopes:    []string{"account:read", "market:read"},
	})
	if err != nil || token == "" {
		t.Fatalf("expected token for live account, got token=%q err=%v", token, err)
	}

	if _, err := app.Trading.PlaceOrder(ctx, live.ID, PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeMarket,
		Quantity:     1,
		Leverage:     1,
	}); err == nil {
		t.Fatalf("expected live account order placement to be rejected")
	}
}
