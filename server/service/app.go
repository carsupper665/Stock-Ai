package service

import (
    "fmt"
    "time"

    "server/domain"
    "server/model/repo"

    "gorm.io/gorm"
)

type AppConfig struct {
    DB            *gorm.DB
    SessionSecret string
    FrontendURL   string
    Now           func() time.Time
}

type App struct {
    DB        *gorm.DB
    Repo      *repo.Repository
    Clock     domain.Clock
    Events    *EventBus
    Market    domain.MarketDataProvider
    Tokens    *TokenService
    Sandboxes *SandboxService
    Accounts  *AccountService
    Trading   *TradingService
    Auth      *AuthService
    Monitor   *MonitorService
    Config    AppConfig
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
    ledger := SimpleLedger{}
    fillPolicy := SimpleFillPolicy{}
    risk := SimpleRiskEngine{}

    app := &App{
        DB:     cfg.DB,
        Repo:   repository,
        Clock:  clock,
        Events: events,
        Market: market,
        Config: cfg,
    }

    app.Accounts = NewAccountService(repository)
    app.Sandboxes = NewSandboxService(repository, clock, events)
    app.Tokens = NewTokenService(repository, clock, events)
    app.Trading = NewTradingService(repository, clock, market, events, risk, fillPolicy, ledger)
    app.Auth = NewAuthService(repository, app.Tokens, clock)
    app.Monitor = NewMonitorService(repository, app.Sandboxes)

    return app, nil
}

type funcClock struct {
    now func() time.Time
}

func (c funcClock) Now() time.Time {
    return c.now().UTC()
}
