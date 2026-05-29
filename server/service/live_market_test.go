package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func (f fakeLiveFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketSnapshot, error) {
	if f.err != nil {
		return domain.MarketSnapshot{}, f.err
	}
	price, ok := f.prices[symbol]
	if !ok {
		return domain.MarketSnapshot{}, domain.NotFoundError("LIVE_PRICE_NOT_FOUND", "live price not found")
	}
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	return domain.MarketSnapshot{Symbol: symbol, Price: price, LastPrice: price, At: at, ReceivedAt: at, Provider: f.Name(), State: domain.MarketStateActive}, nil
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
	snapshot domain.MarketSnapshot
	err      error
}

func (f *sequenceLiveFetcher) Name() string {
	return "sequence"
}

func (f *sequenceLiveFetcher) Fetch(ctx context.Context, symbol string) (domain.MarketSnapshot, error) {
	if f.index >= len(f.results) {
		return domain.MarketSnapshot{}, errors.New("no more results")
	}
	result := f.results[f.index]
	f.index++
	if result.err != nil {
		return domain.MarketSnapshot{}, result.err
	}
	return result.snapshot, nil
}

func TestLiveMarketProviderPublishesConnectionEvents(t *testing.T) {
	app := newTestApp(t)
	fetcher := &sequenceLiveFetcher{results: []fetchResult{
		{err: errors.New("upstream down")},
		{snapshot: domain.MarketSnapshot{Symbol: "BTCUSDT", Price: 101, LastPrice: 101, At: time.Date(2025, 1, 1, 0, 0, 1, 0, time.UTC), ReceivedAt: time.Date(2025, 1, 1, 0, 0, 1, 0, time.UTC), Provider: "sequence", State: domain.MarketStateActive}},
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

func TestBinanceRESTFetcherMapsBookTickerSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/ticker/bookTicker" || r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100.00","bidQty":"2.5","askPrice":"101.00","askQty":"3.5"}`))
	}))
	defer server.Close()

	snapshot, err := NewBinanceRESTFetcher(server.URL).Fetch(context.Background(), " btcusdt ")
	if err != nil {
		t.Fatalf("fetch book ticker: %v", err)
	}
	if snapshot.Symbol != "BTCUSDT" || snapshot.LastPrice != 100.5 || snapshot.Provider != "binance" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.BidPrice == nil || *snapshot.BidPrice != 100 || snapshot.AskPrice == nil || *snapshot.AskPrice != 101 || snapshot.Spread == nil || *snapshot.Spread != 1 {
		t.Fatalf("expected bid/ask/spread, got %+v", snapshot)
	}
}

func TestLiveMarketProviderKlineContract(t *testing.T) {
	app := newTestApp(t)
	provider := NewLiveMarketProvider(NewStaticLivePriceFetcher(), app.Clock)

	klines, err := provider.GetKlines(context.Background(), "", "btcusdt", "1m", app.Clock.Now().Add(-time.Minute), app.Clock.Now())
	if err != nil {
		t.Fatalf("get live klines: %v", err)
	}
	if len(klines) < 2 || klines[0].Symbol != "BTCUSDT" || klines[0].Close != 100 {
		t.Fatalf("unexpected klines: %+v", klines)
	}
	if _, err := provider.GetKlines(context.Background(), "", "BTCUSDT", "2m", app.Clock.Now().Add(-time.Minute), app.Clock.Now()); codeOf(err) != "INVALID_INTERVAL" {
		t.Fatalf("expected invalid interval, got %v", err)
	}
}

func TestBinanceRESTFetcherMapsKlines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/klines" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		if r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("unexpected symbol %s", r.URL.Query().Get("symbol"))
		}
		if r.URL.Query().Get("interval") != "1h" {
			t.Fatalf("unexpected interval %s", r.URL.Query().Get("interval"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			[1710000000000,"100.0","105.0","99.0","104.0","10.5",1710003599999,"0",1,"0","0","0"],
			[1710003600000,"104.0","106.0","103.0","105.5","8.0",1710007199999,"0",1,"0","0","0"]
		]`))
	}))
	defer server.Close()

	fetcher := NewBinanceRESTFetcher(server.URL)
	from := time.UnixMilli(1710000000000).UTC()
	to := time.UnixMilli(1710007199999).UTC()
	klines, err := fetcher.FetchKlines(context.Background(), "btcusdt", "1h", from, to)
	if err != nil {
		t.Fatalf("fetch klines: %v", err)
	}
	if len(klines) != 2 {
		t.Fatalf("expected 2 klines, got %d", len(klines))
	}
	if klines[0].Symbol != "BTCUSDT" || klines[0].At != from || klines[0].Open != 100 || klines[0].High != 105 || klines[0].Low != 99 || klines[0].Close != 104 || klines[0].Volume != 10.5 {
		t.Fatalf("unexpected first kline: %+v", klines[0])
	}
}
