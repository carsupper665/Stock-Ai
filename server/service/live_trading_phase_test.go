package service

import (
	"context"
	"encoding/json"
	"testing"

	"server/domain"
	"server/model/store"
)

func TestPaperTradingFlowUsesLivePrices(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	account := createPaperLiveAccount(t, app)

	order, err := app.Trading.PlaceOrder(ctx, account.ID, PlaceOrderInput{
		Symbol:       "btcusdt",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeMarket,
		Quantity:     1,
		Leverage:     1,
	})
	if err != nil {
		t.Fatalf("place paper order: %v", err)
	}
	if order.Status != store.OrderStatusFilled || order.AvgFillPrice != 100 || order.SandboxID != "" {
		t.Fatalf("unexpected paper fill: %+v", order)
	}
	positions, err := app.Trading.ListPositions(ctx, account.ID)
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	if len(positions) != 1 || positions[0].Symbol != "BTCUSDT" || positions[0].MarkPrice != 100 {
		t.Fatalf("expected live-priced position, got %+v", positions)
	}
}

func TestOrderNormalizationUsesSymbolRules(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	account := createPaperLiveAccount(t, app)
	riskProfile, err := json.Marshal(accountRiskProfile{
		AllowedOrderTypes: []string{store.OrderTypeMarket, store.OrderTypeLimit},
		SymbolRules: []domain.SymbolRule{{
			Symbol:      "BTCUSDT",
			TickSize:    0.01,
			StepSize:    0.01,
			MinQty:      0.01,
			MinNotional: 10,
			MaxLeverage: 3,
		}},
	})
	if err != nil {
		t.Fatalf("marshal risk profile: %v", err)
	}
	account.RiskProfileJSON = string(riskProfile)
	if err := app.Repo.Save(ctx, account); err != nil {
		t.Fatalf("save account: %v", err)
	}

	order, err := app.Trading.PlaceOrder(ctx, account.ID, PlaceOrderInput{
		Symbol:       "btcusdt",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeLimit,
		Quantity:     1.239,
		Price:        100.129,
		Leverage:     2,
	})
	if err != nil {
		t.Fatalf("place normalized order: %v", err)
	}
	if order.Symbol != "BTCUSDT" || order.Quantity != 1.23 || order.Price != 100.12 {
		t.Fatalf("expected normalized payload, got %+v", order)
	}
	if _, err := app.Trading.PlaceOrder(ctx, account.ID, PlaceOrderInput{Symbol: "BTCUSDT", Side: store.OrderSideBuy, PositionSide: store.PositionSideLong, OrderType: store.OrderTypeStop, Quantity: 1, StopPrice: 90}); codeOf(err) != "ORDER_TYPE_NOT_ALLOWED" {
		t.Fatalf("expected order type rejection, got %v", err)
	}
	if _, err := app.Trading.PlaceOrder(ctx, account.ID, PlaceOrderInput{Symbol: "BTCUSDT", Side: store.OrderSideBuy, PositionSide: store.PositionSideLong, OrderType: store.OrderTypeMarket, Quantity: 1, Leverage: 4}); codeOf(err) != "LEVERAGE_TOO_HIGH" {
		t.Fatalf("expected leverage rejection, got %v", err)
	}
}

func TestExchangeAdapterContractAndReconciliation(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	adapter := NewFakeExchangeAdapter()
	app.Exchange = adapter
	app.Trading.exchange = adapter
	app.Trading.config.AllowLiveExecution = true
	account := createLiveExecutionAccount(t, app, "testnet", true)
	adapter.SetOrderResult("partial-1", ExchangeOrderResult{ExchangeOrderID: "ex_partial", Status: store.OrderStatusPartiallyFilled, FilledQty: 0.4, AvgFillPrice: 100})

	order, err := app.Trading.PlaceOrder(ctx, account.ID, PlaceOrderInput{
		Symbol:        "BTCUSDT",
		Side:          store.OrderSideBuy,
		PositionSide:  store.PositionSideLong,
		OrderType:     store.OrderTypeMarket,
		Quantity:      1,
		Leverage:      1,
		ClientOrderID: "partial-1",
	})
	if err != nil {
		t.Fatalf("submit exchange order: %v", err)
	}
	if order.Status != store.OrderStatusPartiallyFilled || order.FilledQty != 0.4 || order.ExchangeOrderID != "ex_partial" {
		t.Fatalf("unexpected partial order: %+v", order)
	}
	adapter.SetOrderResult("partial-1", ExchangeOrderResult{ExchangeOrderID: "ex_partial", Status: store.OrderStatusFilled, FilledQty: 1, AvgFillPrice: 100})
	if err := app.Trading.ReconcileExchangeOrder(ctx, account.ID, order.ID); err != nil {
		t.Fatalf("reconcile filled order: %v", err)
	}
	filled, err := app.Trading.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("get filled order: %v", err)
	}
	if filled.Status != store.OrderStatusFilled || filled.FilledQty != 1 || filled.ExchangeStatus != store.OrderStatusFilled {
		t.Fatalf("expected filled reconciliation, got %+v", filled)
	}
	if err := app.Trading.ReconcileExchangeOrder(ctx, account.ID, order.ID); err != nil {
		t.Fatalf("repeat reconciliation: %v", err)
	}
	trades, err := app.Trading.ListTrades(ctx, account.ID)
	if err != nil {
		t.Fatalf("list trades: %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected idempotent reconciliation trade count 1, got %+v", trades)
	}
}

func TestLiveExecutionSafetyGates(t *testing.T) {
	ctx := context.Background()
	disabledApp := newTestApp(t)
	disabledAccount := createLiveExecutionAccount(t, disabledApp, "testnet", true)
	if _, err := disabledApp.Trading.PlaceOrder(ctx, disabledAccount.ID, liveContractOrder("BTCUSDT", "gate-disabled")); codeOf(err) != "LIVE_EXECUTION_DISABLED" {
		t.Fatalf("expected server live gate, got %v", err)
	}

	noAdapterApp := newTestApp(t)
	noAdapterApp.Trading.config.AllowLiveExecution = true
	noAdapterAccount := createLiveExecutionAccount(t, noAdapterApp, "testnet", true)
	if _, err := noAdapterApp.Trading.PlaceOrder(ctx, noAdapterAccount.ID, liveContractOrder("BTCUSDT", "gate-adapter")); codeOf(err) != "LIVE_EXCHANGE_EXECUTION_UNSUPPORTED" {
		t.Fatalf("expected missing adapter gate, got %v", err)
	}

	mainnetApp := newTestApp(t)
	mainnetApp.Trading.config.AllowLiveExecution = true
	mainnetApp.Trading.exchange = NewFakeExchangeAdapter()
	mainnetAccount := createLiveExecutionAccount(t, mainnetApp, "mainnet", true)
	if _, err := mainnetApp.Trading.PlaceOrder(ctx, mainnetAccount.ID, liveContractOrder("BTCUSDT", "gate-mainnet")); codeOf(err) != "MAINNET_EXECUTION_DISABLED" {
		t.Fatalf("expected mainnet gate, got %v", err)
	}

	accountGateApp := newTestApp(t)
	accountGateApp.Trading.config.AllowLiveExecution = true
	accountGateApp.Trading.exchange = NewFakeExchangeAdapter()
	accountGate := createLiveExecutionAccount(t, accountGateApp, "testnet", false)
	if _, err := accountGateApp.Trading.PlaceOrder(ctx, accountGate.ID, liveContractOrder("BTCUSDT", "gate-account")); codeOf(err) != "ACCOUNT_LIVE_TRADING_DISABLED" {
		t.Fatalf("expected account live trading gate, got %v", err)
	}
}

func TestProductionEnvironmentTreatedAsPaper(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	account, err := app.Accounts.Create(ctx, CreateAccountInput{
		Name:             "production-paper",
		InitialBalance:   1000,
		Type:             store.AccountTypeLive,
		Environment:      "production",
		PriceMode:        "live",
		SupportedSymbols: []string{"BTCUSDT"},
	})
	if err != nil {
		t.Fatalf("create production live account: %v", err)
	}

	order, err := app.Trading.PlaceOrder(ctx, account.ID, PlaceOrderInput{
		Symbol:       "BTCUSDT",
		Side:         store.OrderSideBuy,
		PositionSide: store.PositionSideLong,
		OrderType:    store.OrderTypeMarket,
		Quantity:     1,
		Leverage:     1,
	})
	if err != nil {
		t.Fatalf("place production live order: %v", err)
	}
	if order.Status != store.OrderStatusFilled {
		t.Fatalf("expected paper fill for production environment, got %+v", order)
	}
}

func TestLiveTradingAuditEvents(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	adapter := NewFakeExchangeAdapter()
	app.Trading.exchange = adapter
	app.Trading.config.AllowLiveExecution = true
	account := createLiveExecutionAccount(t, app, "testnet", true)
	ch, unsubscribe := app.Events.Subscribe(8, func(evt domain.DomainEvent) bool {
		return evt.Topic == "live.order.submitted" || evt.Topic == "live.order.filled" || evt.Topic == "live.order.reconciled"
	})
	defer unsubscribe()

	_, err := app.Trading.PlaceOrder(ctx, account.ID, liveContractOrder("BTCUSDT", "audit-1"))
	if err != nil {
		t.Fatalf("place audit order: %v", err)
	}
	topics := map[string]bool{}
	for len(topics) < 3 {
		select {
		case evt := <-ch:
			topics[evt.Topic] = true
		default:
			t.Fatalf("missing audit events, got %v", topics)
		}
	}
}

func createLiveExecutionAccount(t *testing.T, app *App, environment string, enabled bool) *store.Account {
	t.Helper()
	account, err := app.Accounts.Create(context.Background(), CreateAccountInput{
		Name:              environment,
		InitialBalance:    1000,
		Type:              store.AccountTypeLive,
		Environment:       environment,
		PriceMode:         "live",
		CredentialsStatus: "healthy",
		SupportedSymbols:  []string{"BTCUSDT"},
	})
	if err != nil {
		t.Fatalf("create live account: %v", err)
	}
	account.LiveTradingEnabled = enabled
	if err := app.Repo.Save(context.Background(), account); err != nil {
		t.Fatalf("save live account: %v", err)
	}
	return account
}
