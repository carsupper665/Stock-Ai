package service

import (
    "context"
    "encoding/json"
    "fmt"
    "math"
    "sync"
    "time"

    "server/domain"
    "server/model/repo"
    "server/model/store"
    "server/utils"

    "gorm.io/gorm"
)

type EventBus struct {
    db    *gorm.DB
    clock domain.Clock

    mu     sync.RWMutex
    nextID int
    subs   map[int]subscription
}

type subscription struct {
    ch     chan domain.DomainEvent
    filter func(domain.DomainEvent) bool
}

func NewEventBus(db *gorm.DB, clock domain.Clock) *EventBus {
    return &EventBus{db: db, clock: clock, subs: map[int]subscription{}}
}

func (b *EventBus) Publish(ctx context.Context, event domain.DomainEvent) error {
    event.CreatedAt = b.clock.Now()
    payload, err := json.Marshal(event.Payload)
    if err != nil {
        return err
    }
    log := store.EventLog{
        ID:            newID("evt"),
        EventType:     event.Topic,
        AggregateType: aggregateTypeFromTopic(event.Topic),
        AggregateID:   event.AggregateID,
        PayloadJSON:   string(payload),
        CreatedAt:     event.CreatedAt,
    }
    if err := b.db.WithContext(ctx).Create(&log).Error; err != nil {
        return err
    }

    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, sub := range b.subs {
        if sub.filter != nil && !sub.filter(event) {
            continue
        }
        select {
        case sub.ch <- event:
        default:
        }
    }
    return nil
}

func (b *EventBus) Subscribe(buffer int, fn func(domain.DomainEvent) bool) (<-chan domain.DomainEvent, func()) {
    b.mu.Lock()
    defer b.mu.Unlock()
    id := b.nextID
    b.nextID++
    ch := make(chan domain.DomainEvent, buffer)
    b.subs[id] = subscription{ch: ch, filter: fn}
    return ch, func() {
        b.mu.Lock()
        defer b.mu.Unlock()
        if sub, ok := b.subs[id]; ok {
            delete(b.subs, id)
            close(sub.ch)
        }
    }
}

type ReplayMarketProvider struct {
    repo  *repo.Repository
    clock domain.Clock
}

func NewReplayMarketProvider(repo *repo.Repository, clock domain.Clock) *ReplayMarketProvider {
    return &ReplayMarketProvider{repo: repo, clock: clock}
}

func (p *ReplayMarketProvider) GetTicker(ctx context.Context, sandboxID, symbol string) (domain.MarketTicker, error) {
    price, at, err := p.GetLastPrice(ctx, sandboxID, symbol)
    if err != nil {
        return domain.MarketTicker{}, err
    }
    return domain.MarketTicker{Symbol: symbol, Price: price, At: at}, nil
}

func (p *ReplayMarketProvider) GetLastPrice(ctx context.Context, sandboxID, symbol string) (float64, time.Time, error) {
    sandbox, err := p.repo.FindSandbox(ctx, sandboxID)
    if err != nil {
        return 0, time.Time{}, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
    }
    currentTime := currentReplayTime(*sandbox, p.clock.Now())
    kline, err := p.repo.FindReplayPrice(ctx, sandbox.DatasetID, symbol, currentTime)
    if err != nil {
        return 0, time.Time{}, domain.NotFoundError("MARKET_DATA_UNAVAILABLE", "market data unavailable")
    }
    return kline.Close, kline.Ts, nil
}

func (p *ReplayMarketProvider) GetKlines(ctx context.Context, sandboxID, symbol, interval string, from, to time.Time) ([]domain.Kline, error) {
    sandbox, err := p.repo.FindSandbox(ctx, sandboxID)
    if err != nil {
        return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
    }
    var klines []store.ReplayKline
    err = p.repo.WithContext(ctx).
        Where("dataset_id = ? AND symbol = ? AND ts BETWEEN ? AND ?", sandbox.DatasetID, symbol, from, to).
        Order("ts asc").
        Find(&klines).Error
    if err != nil {
        return nil, err
    }
    out := make([]domain.Kline, 0, len(klines))
    for _, k := range klines {
        out = append(out, domain.Kline{Symbol: k.Symbol, At: k.Ts, Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume})
    }
    return out, nil
}

type SimpleFillPolicy struct{}

func (SimpleFillPolicy) LimitShouldFill(side string, marketPrice, limitPrice float64) bool {
    switch side {
    case store.OrderSideBuy:
        return marketPrice <= limitPrice
    case store.OrderSideSell:
        return marketPrice >= limitPrice
    default:
        return false
    }
}

func (SimpleFillPolicy) StopShouldTrigger(positionSide string, marketPrice, stopPrice float64) bool {
    switch positionSide {
    case store.PositionSideLong:
        return marketPrice <= stopPrice
    case store.PositionSideShort:
        return marketPrice >= stopPrice
    default:
        return false
    }
}

type SimpleRiskEngine struct{}

func (SimpleRiskEngine) ValidateNewOrder(ctx context.Context, req domain.RiskOrderRequest) error {
    if req.SandboxStatus != store.SandboxStatusRunning && req.SandboxStatus != store.SandboxStatusPaused {
        return domain.ConflictError("SANDBOX_NOT_RUNNING", "sandbox is not running")
    }
    if !req.Tradable {
        return domain.ValidationError("SYMBOL_NOT_TRADABLE", "symbol is not tradable")
    }
    if req.Quantity <= 0 {
        return domain.ValidationError("INVALID_QUANTITY", "quantity must be positive")
    }
    if req.Leverage <= 0 {
        return domain.ValidationError("INVALID_LEVERAGE", "leverage must be positive")
    }
    if req.Leverage > 10 {
        return domain.ValidationError("LEVERAGE_TOO_HIGH", "leverage exceeds limit")
    }
    if req.OrderType == store.OrderTypeLimit && req.LimitPrice <= 0 {
        return domain.ValidationError("INVALID_PRICE", "limit price must be positive")
    }
    if req.OrderType == store.OrderTypeStop && req.StopPrice <= 0 {
        return domain.ValidationError("INVALID_STOP_PRICE", "stop price must be positive")
    }
    isOpening := (req.PositionSide == store.PositionSideLong && req.Side == store.OrderSideBuy) || (req.PositionSide == store.PositionSideShort && req.Side == store.OrderSideSell)
    isClosing := (req.PositionSide == store.PositionSideLong && req.Side == store.OrderSideSell) || (req.PositionSide == store.PositionSideShort && req.Side == store.OrderSideBuy)
    if !isOpening && !isClosing {
        return domain.ValidationError("INVALID_ORDER_DIRECTION", "side and position_side do not match a supported action")
    }
    if isClosing {
        if req.CurrentQuantity <= 0 {
            return domain.ValidationError("POSITION_NOT_FOUND", "position not found")
        }
        if req.Quantity > req.CurrentQuantity {
            return domain.ValidationError("POSITION_QTY_EXCEEDED", "order quantity exceeds current position size")
        }
        if req.OrderType == store.OrderTypeStop && req.PositionSide == store.PositionSideLong && req.StopPrice >= req.MarketPrice {
            return domain.ValidationError("INVALID_STOP_PRICE", "long stop price must be below market price")
        }
        if req.OrderType == store.OrderTypeStop && req.PositionSide == store.PositionSideShort && req.StopPrice <= req.MarketPrice {
            return domain.ValidationError("INVALID_STOP_PRICE", "short stop price must be above market price")
        }
        return nil
    }
    requiredMargin := req.Quantity * req.MarketPrice / req.Leverage
    if requiredMargin > req.AvailableBalance+1e-9 {
        return domain.NewError(400, "INSUFFICIENT_BALANCE", "available balance is not enough", nil)
    }
    return nil
}

type SimpleLedger struct{}

func (SimpleLedger) ReserveMargin(wallet, locked *float64, amount float64) error {
    available := *wallet - *locked
    if amount > available+1e-9 {
        return domain.NewError(400, "INSUFFICIENT_BALANCE", "available balance is not enough", nil)
    }
    *locked += amount
    return nil
}

func (SimpleLedger) ReleaseMargin(wallet, locked *float64, amount float64) {
    *locked = math.Max(0, *locked-amount)
}

func (SimpleLedger) ApplyRealizedPnL(wallet, realized *float64, pnl float64) {
    *wallet += pnl
    *realized += pnl
}

func (SimpleLedger) Reprice(unrealized, equity, available *float64, walletBalance, lockedMargin, totalUnrealized float64) {
    *unrealized = totalUnrealized
    *equity = walletBalance + totalUnrealized
    *available = *equity - lockedMargin
}

func newID(prefix string) string {
    return fmt.Sprintf("%s_%s", prefix, utils.GetTimeString()+utils.GetRandomString(6))
}

func aggregateTypeFromTopic(topic string) string {
    switch {
    case len(topic) >= 7 && topic[:7] == "sandbox":
        return "sandbox"
    case len(topic) >= 7 && topic[:7] == "account":
        return "account"
    case len(topic) >= 5 && topic[:5] == "order":
        return "order"
    case len(topic) >= 5 && topic[:5] == "trade":
        return "trade"
    case len(topic) >= 8 && topic[:8] == "position":
        return "position"
    default:
        return "system"
    }
}

func currentReplayTime(sandbox store.Sandbox, now time.Time) time.Time {
    current := sandbox.ReplayCurrentTime.UTC()
    if current.IsZero() {
        current = sandbox.StartDatetime.UTC()
    }
    if sandbox.Status != store.SandboxStatusRunning || sandbox.RuntimeAnchorAt == nil {
        return current
    }
    elapsed := now.Sub(sandbox.RuntimeAnchorAt.UTC())
    if elapsed < 0 {
        elapsed = 0
    }
    return current.Add(time.Duration(float64(elapsed) * sandbox.ReplaySpeed))
}
