package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"server/domain"
	"server/model/store"
)

type singleSnapshotFetcher struct {
	snapshot domain.MarketSnapshot
	err      error
}

func (f singleSnapshotFetcher) Name() string {
	return "contract"
}

func (f singleSnapshotFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketSnapshot, error) {
	if f.err != nil {
		return domain.MarketSnapshot{}, f.err
	}
	return f.snapshot, nil
}

func TestLiveContractModeMatrixAndSafetyDefaults(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	virtual := store.Account{
		ID:               "virtual-no-sandbox",
		Name:             "bad virtual",
		Type:             store.AccountTypeVirtual,
		BaseCurrency:     "USD",
		InitialBalance:   1000,
		WalletBalance:    1000,
		AvailableBalance: 1000,
		Equity:           1000,
		Status:           store.AccountStatusActive,
	}
	if err := app.Repo.Create(ctx, &virtual); err != nil {
		t.Fatalf("create virtual account: %v", err)
	}
	if _, err := app.Trading.PlaceOrder(ctx, virtual.ID, liveContractOrder("BTCUSDT", "")); codeOf(err) != "ACCOUNT_SANDBOX_REQUIRED" {
		t.Fatalf("expected virtual sandbox requirement, got %v", err)
	}

	liveMissingEnv, err := app.Accounts.Create(ctx, CreateAccountInput{
		Name:             "missing-env",
		InitialBalance:   1000,
		Type:             store.AccountTypeLive,
		PriceMode:        "live",
		SupportedSymbols: []string{"BTCUSDT"},
	})
	if err != nil {
		t.Fatalf("create live account: %v", err)
	}
	if _, err := app.Trading.PlaceOrder(ctx, liveMissingEnv.ID, liveContractOrder("BTCUSDT", "")); codeOf(err) != "INVALID_TRADING_MODE" {
		t.Fatalf("expected invalid live mode, got %v", err)
	}

	testnet, err := app.Accounts.Create(ctx, CreateAccountInput{
		Name:              "testnet",
		InitialBalance:    1000,
		Type:              store.AccountTypeLive,
		Environment:       "testnet",
		PriceMode:         "live",
		CredentialsStatus: "healthy",
		SupportedSymbols:  []string{"BTCUSDT"},
	})
	if err != nil {
		t.Fatalf("create testnet account: %v", err)
	}
	if testnet.LiveTradingEnabled {
		t.Fatalf("live trading must default disabled")
	}
	if _, err := app.Trading.PlaceOrder(ctx, testnet.ID, liveContractOrder("BTCUSDT", "")); codeOf(err) != "LIVE_EXECUTION_DISABLED" {
		t.Fatalf("expected server live execution gate, got %v", err)
	}
}

func TestLiveContractMarketSnapshotAndPaperOrderIdempotency(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	now := app.Clock.Now()
	liveMarket := NewLiveMarketProvider(singleSnapshotFetcher{snapshot: domain.MarketSnapshot{Symbol: "BTCUSDT", Price: 100, LastPrice: 100, At: now, ReceivedAt: now, Provider: "contract", State: domain.MarketStateActive}}, app.Clock)
	app.LiveMarket = liveMarket
	app.Trading.liveMarket = liveMarket

	snapshot, err := app.LiveMarket.GetSnapshot(ctx, " BTCUSDT ")
	if err != nil {
		t.Fatalf("get live snapshot: %v", err)
	}
	if snapshot.Symbol != "BTCUSDT" || snapshot.LastPrice != 100 || snapshot.State != domain.MarketStateActive {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.BidPrice != nil || snapshot.AskPrice != nil {
		t.Fatalf("missing bid/ask must remain nil, got %+v", snapshot)
	}

	account, err := app.Accounts.Create(ctx, CreateAccountInput{
		Name:             "paper",
		InitialBalance:   1000,
		Type:             store.AccountTypeLive,
		Environment:      "paper",
		PriceMode:        "live",
		SupportedSymbols: []string{"BTCUSDT"},
	})
	if err != nil {
		t.Fatalf("create paper account: %v", err)
	}
	req := liveContractOrder("btcusdt", "strategy-1")
	order, err := app.Trading.PlaceOrder(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("place paper order: %v", err)
	}
	if order.Status != store.OrderStatusFilled || order.Symbol != "BTCUSDT" {
		t.Fatalf("unexpected paper order: %+v", order)
	}
	repeated, err := app.Trading.PlaceOrder(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("repeat idempotent order: %v", err)
	}
	if repeated.ID != order.ID {
		t.Fatalf("expected idempotent order id %s, got %s", order.ID, repeated.ID)
	}
	conflict := req
	conflict.Quantity = 2
	if _, err := app.Trading.PlaceOrder(ctx, account.ID, conflict); codeOf(err) != "CLIENT_ORDER_ID_CONFLICT" {
		t.Fatalf("expected client order conflict, got %v", err)
	}
}

func TestLiveContractRejectsStaleAndDegradedMarketData(t *testing.T) {
	ctx := context.Background()

	staleApp := newTestApp(t)
	staleMarket := NewLiveMarketProvider(singleSnapshotFetcher{snapshot: domain.MarketSnapshot{
		Symbol:     "BTCUSDT",
		Price:      100,
		LastPrice:  100,
		At:         staleApp.Clock.Now().Add(-time.Minute),
		ReceivedAt: staleApp.Clock.Now().Add(-time.Minute),
		Provider:   "contract",
		State:      domain.MarketStateActive,
	}}, staleApp.Clock)
	staleApp.LiveMarket = staleMarket
	staleApp.Trading.liveMarket = staleMarket
	staleAccount := createPaperLiveAccount(t, staleApp)
	if _, err := staleApp.Trading.PlaceOrder(ctx, staleAccount.ID, liveContractOrder("BTCUSDT", "")); codeOf(err) != "MARKET_DATA_STALE" {
		t.Fatalf("expected stale market rejection, got %v", err)
	}

	degradedApp := newTestApp(t)
	degradedMarket := NewLiveMarketProvider(singleSnapshotFetcher{err: errors.New("upstream down")}, degradedApp.Clock)
	degradedApp.LiveMarket = degradedMarket
	degradedApp.Trading.liveMarket = degradedMarket
	degradedAccount := createPaperLiveAccount(t, degradedApp)
	if _, err := degradedApp.Trading.PlaceOrder(ctx, degradedAccount.ID, liveContractOrder("BTCUSDT", "")); codeOf(err) != "LIVE_PROVIDER_ERROR" {
		t.Fatalf("expected degraded provider rejection, got %v", err)
	}
}

func liveContractOrder(symbol, clientOrderID string) PlaceOrderInput {
	return PlaceOrderInput{
		Symbol:        symbol,
		Side:          store.OrderSideBuy,
		PositionSide:  store.PositionSideLong,
		OrderType:     store.OrderTypeMarket,
		Quantity:      1,
		Leverage:      1,
		ClientOrderID: clientOrderID,
	}
}

func createPaperLiveAccount(t *testing.T, app *App) *store.Account {
	t.Helper()
	account, err := app.Accounts.Create(context.Background(), CreateAccountInput{
		Name:             "paper",
		InitialBalance:   1000,
		Type:             store.AccountTypeLive,
		Environment:      "paper",
		PriceMode:        "live",
		SupportedSymbols: []string{"BTCUSDT"},
	})
	if err != nil {
		t.Fatalf("create paper account: %v", err)
	}
	return account
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	if appErr, ok := err.(*domain.AppError); ok {
		return appErr.Code
	}
	return err.Error()
}
