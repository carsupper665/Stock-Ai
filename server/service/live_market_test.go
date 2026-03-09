package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"server/domain"
)

type fakeLiveFetcher struct {
	prices map[string]float64
	err    error
}

func (f fakeLiveFetcher) Name() string {
	return "fake"
}

func (f fakeLiveFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketTicker, error) {
	if f.err != nil {
		return domain.MarketTicker{}, f.err
	}
	price, ok := f.prices[symbol]
	if !ok {
		return domain.MarketTicker{}, domain.NotFoundError("LIVE_PRICE_NOT_FOUND", "live price not found")
	}
	return domain.MarketTicker{Symbol: symbol, Price: price, At: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func TestLiveMarketProviderDeduplicatesSubscriptions(t *testing.T) {
	provider := NewLiveMarketProvider(fakeLiveFetcher{prices: map[string]float64{"BTCUSDT": 100}}, funcClock{now: func() time.Time {
		return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	}})

	provider.TouchSymbol("BTCUSDT")
	provider.TouchSymbol("BTCUSDT")

	snapshot := provider.DebugSnapshot()
	if snapshot["BTCUSDT"].SubscriberCount != 1 {
		t.Fatalf("expected deduplicated symbol subscription, got %+v", snapshot)
	}
}

func TestLiveMarketProviderGCRemovesIdleSymbols(t *testing.T) {
	provider := NewLiveMarketProvider(fakeLiveFetcher{prices: map[string]float64{"BTCUSDT": 100}}, funcClock{now: func() time.Time {
		return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	}})

	provider.TouchSymbol("BTCUSDT")
	provider.ForceIdle("BTCUSDT", time.Hour)
	provider.RunGC(time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC))

	if _, ok := provider.DebugSnapshot()["BTCUSDT"]; ok {
		t.Fatal("expected idle symbol to be removed")
	}
}

func TestLiveMarketProviderMarksDegradedOnFetchError(t *testing.T) {
	provider := NewLiveMarketProvider(fakeLiveFetcher{err: errors.New("boom")}, funcClock{now: func() time.Time {
		return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	}})

	if _, err := provider.GetTicker(context.Background(), "", "BTCUSDT"); err == nil {
		t.Fatalf("expected ticker error")
	}

	snapshot := provider.DebugSnapshot()["BTCUSDT"]
	if snapshot.State != SymbolStateDegraded {
		t.Fatalf("expected degraded symbol state, got %+v", snapshot)
	}
}

type sequenceLiveFetcher struct {
	results []fetchResult
	index   int
}

type fetchResult struct {
	ticker domain.MarketTicker
	err    error
}

func (f *sequenceLiveFetcher) Name() string {
	return "sequence"
}

func (f *sequenceLiveFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketTicker, error) {
	if f.index >= len(f.results) {
		return domain.MarketTicker{}, errors.New("no more results")
	}
	result := f.results[f.index]
	f.index++
	if result.err != nil {
		return domain.MarketTicker{}, result.err
	}
	return result.ticker, nil
}

func TestLiveMarketProviderPublishesConnectionEvents(t *testing.T) {
	app := newTestApp(t)
	fetcher := &sequenceLiveFetcher{results: []fetchResult{
		{err: errors.New("upstream down")},
		{ticker: domain.MarketTicker{Symbol: "BTCUSDT", Price: 101, At: time.Date(2025, 1, 1, 0, 0, 1, 0, time.UTC)}},
	}}
	provider := NewLiveMarketProvider(fetcher, app.Clock)
	provider.SetPublisher(app.Events)

	ch, unsubscribe := app.Events.Subscribe(8, func(evt domain.DomainEvent) bool {
		return evt.Topic == "live.connection.degraded" || evt.Topic == "live.connection.recovered"
	})
	defer unsubscribe()

	if _, err := provider.GetTicker(context.Background(), "", "BTCUSDT"); err == nil {
		t.Fatalf("expected degraded fetch to fail")
	}
	if _, err := provider.GetTicker(context.Background(), "", "BTCUSDT"); err != nil {
		t.Fatalf("expected recovery fetch to succeed: %v", err)
	}

	topics := []string{}
	for len(topics) < 2 {
		select {
		case evt := <-ch:
			topics = append(topics, evt.Topic)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for live connection events, got %v", topics)
		}
	}

	if topics[0] != "live.connection.degraded" || topics[1] != "live.connection.recovered" {
		t.Fatalf("unexpected live connection events: %v", topics)
	}
}
