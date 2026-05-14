package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"server/domain"
)

type SymbolState string

const (
	SymbolStateActive   SymbolState = "active"
	SymbolStateStale    SymbolState = "stale"
	SymbolStateDegraded SymbolState = "degraded"
)

type LivePriceSnapshot struct {
	Symbol          string      `json:"symbol"`
	State           SymbolState `json:"state"`
	Price           float64     `json:"price"`
	LastPrice       float64     `json:"last_price"`
	MarkPrice       *float64    `json:"mark_price,omitempty"`
	BidPrice        *float64    `json:"bid_price,omitempty"`
	BidQty          *float64    `json:"bid_qty,omitempty"`
	AskPrice        *float64    `json:"ask_price,omitempty"`
	AskQty          *float64    `json:"ask_qty,omitempty"`
	Spread          *float64    `json:"spread,omitempty"`
	LastUpdated     time.Time   `json:"last_updated,omitempty"`
	ReceivedAt      time.Time   `json:"received_at,omitempty"`
	FreshnessMs     int64       `json:"freshness_ms"`
	LastAccessed    time.Time   `json:"last_accessed,omitempty"`
	SubscriberCount int         `json:"subscriber_count"`
	Provider        string      `json:"provider"`
	Error           string      `json:"error,omitempty"`
	ErrorCode       string      `json:"error_code,omitempty"`
}

type SubscriptionState struct {
	Symbol          string    `json:"symbol"`
	SubscriberCount int       `json:"subscriber_count"`
	LastAccessed    time.Time `json:"last_accessed,omitempty"`
}

type LiveSymbolsSummary struct {
	ActiveSymbols   int `json:"active_symbols"`
	StaleSymbols    int `json:"stale_symbols"`
	DegradedSymbols int `json:"degraded_symbols"`
	GCRemovals      int `json:"gc_removals"`
}

type LivePriceFetcher interface {
	Name() string
	Fetch(ctx context.Context, symbol string) (domain.MarketTicker, error)
}

type LiveMarketProvider struct {
	fetcher    LivePriceFetcher
	clock      domain.Clock
	events     domain.EventPublisher
	idleTTL    time.Duration
	staleAfter time.Duration

	mu         sync.RWMutex
	entries    map[string]*liveSymbolEntry
	gcRemovals int
}

type liveSymbolEntry struct {
	Symbol          string
	Price           float64
	MarkPrice       *float64
	BidPrice        *float64
	BidQty          *float64
	AskPrice        *float64
	AskQty          *float64
	Spread          *float64
	LastUpdated     time.Time
	ReceivedAt      time.Time
	LastAccessed    time.Time
	SubscriberCount int
	Provider        string
	Error           string
	ErrorCode       string
	State           SymbolState
}

type StaticLivePriceFetcher struct {
	prices map[string]float64
}

func NewLiveMarketProvider(fetcher LivePriceFetcher, clock domain.Clock) *LiveMarketProvider {
	if fetcher == nil {
		fetcher = NewStaticLivePriceFetcher()
	}
	return &LiveMarketProvider{
		fetcher:    fetcher,
		clock:      clock,
		idleTTL:    15 * time.Minute,
		staleAfter: 30 * time.Second,
		entries:    map[string]*liveSymbolEntry{},
	}
}

func (p *LiveMarketProvider) SetPublisher(events domain.EventPublisher) {
	p.events = events
}

func NewStaticLivePriceFetcher() *StaticLivePriceFetcher {
	return &StaticLivePriceFetcher{prices: map[string]float64{
		"BTCUSDT": 100,
		"ETHUSDT": 50,
		"SOLUSDT": 20,
	}}
}

func (f *StaticLivePriceFetcher) Name() string {
	return "static"
}

func (f *StaticLivePriceFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketTicker, error) {
	price, ok := f.prices[strings.ToUpper(strings.TrimSpace(symbol))]
	if !ok {
		return domain.MarketTicker{}, domain.NotFoundError("LIVE_PRICE_NOT_FOUND", "live price not found")
	}
	return domain.MarketTicker{Symbol: strings.ToUpper(symbol), Price: price, At: time.Now().UTC()}, nil
}

func (p *LiveMarketProvider) TouchSymbol(symbol string) {
	normalized := normalizeSymbol(symbol)
	if normalized == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[normalized]
	if !ok {
		p.entries[normalized] = &liveSymbolEntry{
			Symbol:          normalized,
			LastAccessed:    p.clock.Now(),
			SubscriberCount: 1,
			Provider:        p.fetcher.Name(),
			State:           SymbolStateStale,
		}
		return
	}
	entry.LastAccessed = p.clock.Now()
	if entry.SubscriberCount == 0 {
		entry.SubscriberCount = 1
	}
}

func (p *LiveMarketProvider) GetTicker(ctx context.Context, sandboxID, symbol string) (domain.MarketTicker, error) {
	snapshot, err := p.GetSnapshot(ctx, symbol)
	if err != nil {
		return domain.MarketTicker{}, err
	}
	return domain.MarketTicker{Symbol: snapshot.Symbol, Price: snapshot.LastPrice, At: snapshot.At}, nil
}

func (p *LiveMarketProvider) GetSnapshot(ctx context.Context, symbol string) (domain.MarketSnapshot, error) {
	normalized := normalizeSymbol(symbol)
	if normalized == "" {
		return domain.MarketSnapshot{}, domain.ValidationError("INVALID_SYMBOL", "symbol is required")
	}
	p.TouchSymbol(normalized)
	if err := p.refreshSymbol(ctx, normalized); err != nil {
		p.mu.RLock()
		entry := p.entries[normalized]
		snapshot := marketSnapshotFromEntry(*entry, p.clock.Now(), p.staleAfter)
		p.mu.RUnlock()
		if snapshot.ErrorCode != "" {
			return snapshot, domain.ValidationError(snapshot.ErrorCode, "live market provider is degraded")
		}
		return snapshot, err
	}

	p.mu.RLock()
	entry := p.entries[normalized]
	snapshot := marketSnapshotFromEntry(*entry, p.clock.Now(), p.staleAfter)
	p.mu.RUnlock()
	if snapshot.State == domain.MarketStateStale {
		return snapshot, domain.ValidationError("MARKET_DATA_STALE", "market data is stale")
	}
	if snapshot.State == domain.MarketStateDegraded {
		return snapshot, domain.ValidationError("MARKET_DATA_DEGRADED", "market data provider is degraded")
	}
	return snapshot, nil
}

func (p *LiveMarketProvider) GetLastPrice(ctx context.Context, sandboxID, symbol string) (float64, time.Time, error) {
	ticker, err := p.GetTicker(ctx, sandboxID, symbol)
	if err != nil {
		return 0, time.Time{}, err
	}
	return ticker.Price, ticker.At, nil
}

func (p *LiveMarketProvider) GetKlines(ctx context.Context, sandboxID, symbol, interval string, from, to time.Time) ([]domain.Kline, error) {
	ticker, err := p.GetTicker(ctx, sandboxID, symbol)
	if err != nil {
		return nil, err
	}
	return []domain.Kline{{
		Symbol: ticker.Symbol,
		At:     ticker.At,
		Open:   ticker.Price,
		High:   ticker.Price,
		Low:    ticker.Price,
		Close:  ticker.Price,
		Volume: 0,
	}}, nil
}

func (p *LiveMarketProvider) ForceIdle(symbol string, duration time.Duration) {
	normalized := normalizeSymbol(symbol)
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.entries[normalized]; ok {
		entry.LastAccessed = p.clock.Now().Add(-duration)
	}
}

func (p *LiveMarketProvider) RunGC(at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for symbol, entry := range p.entries {
		if at.Sub(entry.LastAccessed) > p.idleTTL {
			delete(p.entries, symbol)
			p.gcRemovals++
		}
	}
}

func (p *LiveMarketProvider) DebugSnapshot() map[string]LivePriceSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[string]LivePriceSnapshot, len(p.entries))
	now := p.clock.Now()
	for symbol, entry := range p.entries {
		out[symbol] = snapshotFromEntry(*entry, now, p.staleAfter)
	}
	return out
}

func (p *LiveMarketProvider) SnapshotList() ([]LivePriceSnapshot, LiveSymbolsSummary) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := p.clock.Now()
	items := make([]LivePriceSnapshot, 0, len(p.entries))
	summary := LiveSymbolsSummary{GCRemovals: p.gcRemovals}
	for _, entry := range p.entries {
		item := snapshotFromEntry(*entry, now, p.staleAfter)
		items = append(items, item)
		switch item.State {
		case SymbolStateActive:
			summary.ActiveSymbols++
		case SymbolStateStale:
			summary.StaleSymbols++
		case SymbolStateDegraded:
			summary.DegradedSymbols++
		}
	}
	return items, summary
}

func (p *LiveMarketProvider) refreshSymbol(ctx context.Context, symbol string) error {
	ticker, err := p.fetcher.Fetch(ctx, symbol)
	now := p.clock.Now()
	var events []domain.DomainEvent

	p.mu.Lock()
	entry, ok := p.entries[symbol]
	if !ok {
		entry = &liveSymbolEntry{Symbol: symbol, SubscriberCount: 1}
		p.entries[symbol] = entry
	}
	previousState := entry.State
	entry.LastAccessed = now
	entry.Provider = p.fetcher.Name()
	if err != nil {
		entry.State = SymbolStateDegraded
		entry.Error = err.Error()
		entry.ErrorCode = errorCode(err, "LIVE_PROVIDER_ERROR")
		if previousState != SymbolStateDegraded {
			events = append(events,
				domain.DomainEvent{Topic: "live.symbol.state_changed", AggregateID: symbol, Payload: map[string]any{"symbol": symbol, "from": previousState, "to": entry.State, "provider": entry.Provider}},
				domain.DomainEvent{Topic: "live.connection.degraded", AggregateID: symbol, Payload: map[string]any{"symbol": symbol, "provider": entry.Provider, "error": entry.Error}},
			)
		}
		p.mu.Unlock()
		p.publishEvents(ctx, events)
		return err
	}

	entry.Price = ticker.Price
	entry.MarkPrice = nil
	entry.BidPrice = nil
	entry.BidQty = nil
	entry.AskPrice = nil
	entry.AskQty = nil
	entry.Spread = nil
	entry.LastUpdated = ticker.At.UTC()
	if entry.LastUpdated.IsZero() {
		entry.LastUpdated = now
	}
	entry.ReceivedAt = now
	entry.Error = ""
	entry.ErrorCode = ""
	entry.State = SymbolStateActive
	if previousState != SymbolStateActive {
		events = append(events, domain.DomainEvent{Topic: "live.symbol.state_changed", AggregateID: symbol, Payload: map[string]any{"symbol": symbol, "from": previousState, "to": entry.State, "provider": entry.Provider, "price": entry.Price}})
	}
	if previousState == SymbolStateDegraded {
		events = append(events, domain.DomainEvent{Topic: "live.connection.recovered", AggregateID: symbol, Payload: map[string]any{"symbol": symbol, "provider": entry.Provider, "price": entry.Price}})
	}
	p.mu.Unlock()
	p.publishEvents(ctx, events)
	return nil
}

func (p *LiveMarketProvider) publishEvents(ctx context.Context, events []domain.DomainEvent) {
	if p.events == nil {
		return
	}
	for _, evt := range events {
		_ = p.events.Publish(ctx, evt)
	}
}

func snapshotFromEntry(entry liveSymbolEntry, now time.Time, staleAfter time.Duration) LivePriceSnapshot {
	state := entry.State
	if state != SymbolStateDegraded {
		if entry.LastUpdated.IsZero() || now.Sub(entry.LastUpdated) > staleAfter {
			state = SymbolStateStale
		} else {
			state = SymbolStateActive
		}
	}
	return LivePriceSnapshot{
		Symbol:          entry.Symbol,
		State:           state,
		Price:           entry.Price,
		LastPrice:       entry.Price,
		MarkPrice:       entry.MarkPrice,
		BidPrice:        entry.BidPrice,
		BidQty:          entry.BidQty,
		AskPrice:        entry.AskPrice,
		AskQty:          entry.AskQty,
		Spread:          entry.Spread,
		LastUpdated:     entry.LastUpdated,
		ReceivedAt:      entry.ReceivedAt,
		FreshnessMs:     freshnessMs(now, entry.LastUpdated),
		LastAccessed:    entry.LastAccessed,
		SubscriberCount: entry.SubscriberCount,
		Provider:        entry.Provider,
		Error:           entry.Error,
		ErrorCode:       entry.ErrorCode,
	}
}

func normalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

func marketSnapshotFromEntry(entry liveSymbolEntry, now time.Time, staleAfter time.Duration) domain.MarketSnapshot {
	item := snapshotFromEntry(entry, now, staleAfter)
	return domain.MarketSnapshot{
		Symbol:      item.Symbol,
		Price:       item.Price,
		LastPrice:   item.LastPrice,
		MarkPrice:   item.MarkPrice,
		BidPrice:    item.BidPrice,
		BidQty:      item.BidQty,
		AskPrice:    item.AskPrice,
		AskQty:      item.AskQty,
		Spread:      item.Spread,
		At:          item.LastUpdated,
		ReceivedAt:  item.ReceivedAt,
		FreshnessMs: item.FreshnessMs,
		Provider:    item.Provider,
		State:       domain.MarketState(item.State),
		ErrorCode:   item.ErrorCode,
	}
}

func freshnessMs(now, at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return now.Sub(at).Milliseconds()
}

func errorCode(err error, fallback string) string {
	if appErr, ok := err.(*domain.AppError); ok && appErr.Code != "" {
		return appErr.Code
	}
	return fallback
}
