package service

import (
	"context"
	"fmt"
	"time"

	"server/domain"
	"server/model/repo"

	"gorm.io/gorm"
)

type AppConfig struct {
	DB                    *gorm.DB
	SessionSecret         string
	FrontendURL           string
	Now                   func() time.Time
	AllowLiveExecution    bool
	AllowMainnetExecution bool
}

type App struct {
	DB         *gorm.DB
	Repo       *repo.Repository
	Clock      domain.Clock
	Events     *EventBus
	Market     domain.MarketDataProvider
	LiveMarket *LiveMarketProvider
	Runtime    *SandboxRuntimeManager
	Tokens     *TokenService
	Datasets   *DatasetService
	Sandboxes  *SandboxService
	Accounts   *AccountService
	Trading    *TradingService
	Auth       *AuthService
	Monitor    *MonitorService
	Config     AppConfig
}

func NewApp(cfg AppConfig) (*App, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("db is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.SessionSecret == "" {
		cfg.SessionSecret = "dev-secret"
	}

	repository := repo.New(cfg.DB)
	clock := funcClock{now: cfg.Now}
	events := NewEventBus(cfg.DB, clock)
	market := NewReplayMarketProvider(repository, clock)
	liveMarket := NewLiveMarketProvider(NewStaticLivePriceFetcher(), clock)
	liveMarket.SetPublisher(events)
	runtime := NewSandboxRuntimeManager()
	ledger := SimpleLedger{}
	fillPolicy := SimpleFillPolicy{}
	risk := SimpleRiskEngine{}

	app := &App{
		DB:         cfg.DB,
		Repo:       repository,
		Clock:      clock,
		Events:     events,
		Market:     market,
		LiveMarket: liveMarket,
		Runtime:    runtime,
		Config:     cfg,
	}

	app.Accounts = NewAccountService(repository)
	app.Datasets = NewDatasetService(repository, clock, events)
	app.Sandboxes = NewSandboxService(repository, clock, events)
	app.Sandboxes.SetRuntime(runtime)
	app.Tokens = NewTokenService(repository, clock, events)
	app.Trading = NewTradingService(repository, clock, market, liveMarket, events, risk, fillPolicy, ledger, cfg)
	app.Sandboxes.SetProcessor(app.Trading.ProcessSandbox)
	app.Runtime.SetAutoTickPlanner(app.Sandboxes.PlanRuntimeTick)
	app.Runtime.SetTickHandler(app.Sandboxes.HandleRuntimeTick)
	app.Auth = NewAuthService(repository, app.Tokens, clock)
	app.Monitor = NewMonitorService(repository, app.Sandboxes, app.Datasets, app.LiveMarket, app.Clock)

	if err := app.Datasets.RecoverInterruptedImports(context.Background()); err != nil {
		return nil, err
	}

	return app, nil
}

type funcClock struct {
	now func() time.Time
}

func (c funcClock) Now() time.Time {
	return c.now().UTC()
}
