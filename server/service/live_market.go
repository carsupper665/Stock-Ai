package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
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
	Fetch(ctx context.Context, symbol string) (domain.MarketSnapshot, error)
}

type LiveKlineFetcher interface {
	FetchKlines(ctx context.Context, symbol, interval string, from, to time.Time) ([]domain.Kline, error)
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

type BinanceRESTFetcher struct {
	baseURL string
	client  *http.Client
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

func (f *StaticLivePriceFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketSnapshot, error) {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	price, ok := f.prices[normalized]
	if !ok {
		return domain.MarketSnapshot{}, domain.NotFoundError("LIVE_PRICE_NOT_FOUND", "live price not found")
	}
	at := time.Now().UTC()
	return domain.MarketSnapshot{Symbol: normalized, Price: price, LastPrice: price, At: at, ReceivedAt: at, Provider: f.Name(), State: domain.MarketStateActive}, nil
}

func (f *StaticLivePriceFetcher) FetchKlines(ctx context.Context, symbol, interval string, from, to time.Time) ([]domain.Kline, error) {
	if err := validateLiveInterval(interval); err != nil {
		return nil, err
	}
	snapshot, err := f.Fetch(ctx, symbol)
	if err != nil {
		return nil, err
	}
	if to.Before(from) {
		to = from
	}
	return []domain.Kline{
		{Symbol: snapshot.Symbol, At: from.UTC(), Open: snapshot.LastPrice, High: snapshot.LastPrice, Low: snapshot.LastPrice, Close: snapshot.LastPrice, Volume: 0},
		{Symbol: snapshot.Symbol, At: to.UTC(), Open: snapshot.LastPrice, High: snapshot.LastPrice, Low: snapshot.LastPrice, Close: snapshot.LastPrice, Volume: 0},
	}, nil
}

func NewBinanceRESTFetcher(baseURL string) *BinanceRESTFetcher {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.binance.com"
	}
	return &BinanceRESTFetcher{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

func (f *BinanceRESTFetcher) Name() string {
	return "binance"
}

func (f *BinanceRESTFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketSnapshot, error) {
	normalized := normalizeSymbol(symbol)
	if normalized == "" {
		return domain.MarketSnapshot{}, domain.ValidationError("INVALID_SYMBOL", "symbol is required")
	}
	endpoint := f.baseURL + "/api/v3/ticker/bookTicker?symbol=" + url.QueryEscape(normalized)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return domain.MarketSnapshot{}, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return domain.MarketSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return domain.MarketSnapshot{}, domain.NotFoundError("LIVE_PRICE_NOT_FOUND", "live price not found")
	}
	if resp.StatusCode >= 400 {
		return domain.MarketSnapshot{}, domain.NewError(http.StatusBadGateway, "LIVE_PROVIDER_ERROR", "live market provider returned an error", map[string]any{"status": resp.StatusCode})
	}
	var payload struct {
		Symbol   string `json:"symbol"`
		BidPrice string `json:"bidPrice"`
		BidQty   string `json:"bidQty"`
		AskPrice string `json:"askPrice"`
		AskQty   string `json:"askQty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return domain.MarketSnapshot{}, err
	}
	bid, err := strconv.ParseFloat(payload.BidPrice, 64)
	if err != nil {
		return domain.MarketSnapshot{}, domain.NewError(http.StatusBadGateway, "LIVE_PROVIDER_ERROR", "invalid bid price from provider", nil)
	}
	bidQty, err := strconv.ParseFloat(payload.BidQty, 64)
	if err != nil {
		return domain.MarketSnapshot{}, domain.NewError(http.StatusBadGateway, "LIVE_PROVIDER_ERROR", "invalid bid quantity from provider", nil)
	}
	ask, err := strconv.ParseFloat(payload.AskPrice, 64)
	if err != nil {
		return domain.MarketSnapshot{}, domain.NewError(http.StatusBadGateway, "LIVE_PROVIDER_ERROR", "invalid ask price from provider", nil)
	}
	askQty, err := strconv.ParseFloat(payload.AskQty, 64)
	if err != nil {
		return domain.MarketSnapshot{}, domain.NewError(http.StatusBadGateway, "LIVE_PROVIDER_ERROR", "invalid ask quantity from provider", nil)
	}
	last := (bid + ask) / 2
	spread := ask - bid
	now := time.Now().UTC()
	return domain.MarketSnapshot{
		Symbol: normalized, Price: last, LastPrice: last,
		BidPrice: &bid, BidQty: &bidQty, AskPrice: &ask, AskQty: &askQty, Spread: &spread,
		At: now, ReceivedAt: now, FreshnessMs: 0, Provider: f.Name(), State: domain.MarketStateActive,
	}, nil
}

func (f *BinanceRESTFetcher) FetchKlines(ctx context.Context, symbol, interval string, from, to time.Time) ([]domain.Kline, error) {
	if err := validateLiveInterval(interval); err != nil {
		return nil, err
	}
	normalized := normalizeSymbol(symbol)
	if normalized == "" {
		return nil, domain.ValidationError("INVALID_SYMBOL", "symbol is required")
	}
	params := url.Values{}
	params.Set("symbol", normalized)
	params.Set("interval", interval)
	params.Set("startTime", strconv.FormatInt(from.UnixMilli(), 10))
	params.Set("endTime", strconv.FormatInt(to.UnixMilli(), 10))
	params.Set("limit", "500")
	endpoint := f.baseURL + "/api/v3/klines?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return nil, domain.NotFoundError("LIVE_KLINES_NOT_FOUND", "klines not found for symbol")
	}
	if resp.StatusCode >= 400 {
		return nil, domain.NewError(http.StatusBadGateway, "LIVE_PROVIDER_ERROR", "live market provider returned an error", map[string]any{"status": resp.StatusCode})
	}
	// Binance klines response: [[openTime, open, high, low, close, volume, closeTime, ...], ...]
	// element[0] = openTime(ms int64), [1]–[5] = string floats
	var raw [][]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	klines := make([]domain.Kline, 0, len(raw))
	for _, row := range raw {
		if len(row) < 6 {
			continue
		}
		var openTimeMs int64
		if err := json.Unmarshal(row[0], &openTimeMs); err != nil {
			continue
		}
		parseStr := func(r json.RawMessage) (float64, error) {
			var s string
			if err := json.Unmarshal(r, &s); err != nil {
				return 0, err
			}
			return strconv.ParseFloat(s, 64)
		}
		open, err := parseStr(row[1])
		if err != nil {
			continue
		}
		high, err := parseStr(row[2])
		if err != nil {
			continue
		}
		low, err := parseStr(row[3])
		if err != nil {
			continue
		}
		close_, err := parseStr(row[4])
		if err != nil {
			continue
		}
		volume, err := parseStr(row[5])
		if err != nil {
			continue
		}
		klines = append(klines, domain.Kline{
			Symbol: normalized,
			At:     time.UnixMilli(openTimeMs).UTC(),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close_,
			Volume: volume,
		})
	}
	return klines, nil
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
	if err := validateLiveInterval(interval); err != nil {
		return nil, err
	}
	normalized := normalizeSymbol(symbol)
	if normalized == "" {
		return nil, domain.ValidationError("INVALID_SYMBOL", "symbol is required")
	}
	now := p.clock.Now()
	if to.IsZero() || to.After(now) {
		to = now
	}
	if from.IsZero() || from.After(to) {
		from = to.Add(-time.Hour)
	}
	if klineFetcher, ok := p.fetcher.(LiveKlineFetcher); ok {
		return klineFetcher.FetchKlines(ctx, normalized, interval, from, to)
	}
	snapshot, err := p.GetSnapshot(ctx, normalized)
	if err != nil {
		return nil, err
	}
	return []domain.Kline{{
		Symbol: snapshot.Symbol,
		At:     snapshot.At,
		Open:   snapshot.LastPrice,
		High:   snapshot.LastPrice,
		Low:    snapshot.LastPrice,
		Close:  snapshot.LastPrice,
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
	snapshot, err := p.fetcher.Fetch(ctx, symbol)
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

	entry.Price = snapshot.LastPrice
	if entry.Price == 0 {
		entry.Price = snapshot.Price
	}
	entry.MarkPrice = snapshot.MarkPrice
	entry.BidPrice = snapshot.BidPrice
	entry.BidQty = snapshot.BidQty
	entry.AskPrice = snapshot.AskPrice
	entry.AskQty = snapshot.AskQty
	entry.Spread = snapshot.Spread
	entry.LastUpdated = snapshot.At.UTC()
	if entry.LastUpdated.IsZero() {
		entry.LastUpdated = now
	}
	entry.ReceivedAt = snapshot.ReceivedAt.UTC()
	if entry.ReceivedAt.IsZero() {
		entry.ReceivedAt = now
	}
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

func validateLiveInterval(interval string) error {
	switch strings.TrimSpace(interval) {
	case "1m", "3m", "5m", "15m", "30m", "1h", "4h", "1d":
		return nil
	default:
		return domain.ValidationError("INVALID_INTERVAL", "invalid kline interval")
	}
}
