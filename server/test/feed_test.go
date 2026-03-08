package test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"server/sandbox"
)

func TestReplayFeedLoadScenarioFile(t *testing.T) {
	feed := sandbox.NewReplayFeed()
	scenarioPath := filepath.Join("..", "testdata", "replay", "btcusdt_1m.json")

	if err := feed.LoadScenarioFile(scenarioPath); err != nil {
		t.Fatalf("LoadScenarioFile failed: %v", err)
	}

	if got := feed.Symbol(); got != "BTCUSDT" {
		t.Fatalf("expected symbol BTCUSDT, got %s", got)
	}
	if got := feed.Cursor(); got != 0 {
		t.Fatalf("expected cursor 0, got %d", got)
	}
	if got := feed.Len(); got != 3 {
		t.Fatalf("expected 3 bars, got %d", got)
	}

	state, ok := feed.Current()
	if !ok {
		t.Fatal("expected current bar after loading scenario")
	}

	if state.ScenarioID != "btc-usdt-1m-sample" {
		t.Fatalf("expected scenario id btc-usdt-1m-sample, got %s", state.ScenarioID)
	}
	if state.Interval != "1m" {
		t.Fatalf("expected interval 1m, got %s", state.Interval)
	}
	if state.TotalBars != 3 {
		t.Fatalf("expected total bars 3, got %d", state.TotalBars)
	}

	wantTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if !state.Bar.Time.Equal(wantTime) {
		t.Fatalf("expected first bar time %s, got %s", wantTime, state.Bar.Time)
	}
	if state.Bar.Close != 101.5 {
		t.Fatalf("expected first close 101.5, got %v", state.Bar.Close)
	}
}

func TestReplayFeedAdvanceAndReset(t *testing.T) {
	feed := sandbox.NewReplayFeed()
	scenarioPath := filepath.Join("..", "testdata", "replay", "btcusdt_1m.json")
	if err := feed.LoadScenarioFile(scenarioPath); err != nil {
		t.Fatalf("LoadScenarioFile failed: %v", err)
	}

	first, ok := feed.Current()
	if !ok {
		t.Fatal("expected current state")
	}

	second, err := feed.Advance()
	if err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if second.Cursor != 1 {
		t.Fatalf("expected cursor 1 after first advance, got %d", second.Cursor)
	}
	if second.Bar.Close != 102.25 {
		t.Fatalf("expected second close 102.25, got %v", second.Bar.Close)
	}

	third, err := feed.Advance()
	if err != nil {
		t.Fatalf("Advance to third bar failed: %v", err)
	}
	if third.Cursor != 2 {
		t.Fatalf("expected cursor 2 after second advance, got %d", third.Cursor)
	}
	if third.Bar.Close != 104.0 {
		t.Fatalf("expected third close 104.0, got %v", third.Bar.Close)
	}

	if _, err := feed.Advance(); !errors.Is(err, sandbox.ErrNoMoreBars) {
		t.Fatalf("expected ErrNoMoreBars, got %v", err)
	}

	if err := feed.Reset(); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	resetState, ok := feed.Current()
	if !ok {
		t.Fatal("expected current state after reset")
	}
	if resetState.Cursor != 0 {
		t.Fatalf("expected reset cursor 0, got %d", resetState.Cursor)
	}
	if !resetState.Bar.Time.Equal(first.Bar.Time) || resetState.Bar.Close != first.Bar.Close {
		t.Fatalf("expected reset to restore first bar, got %+v", resetState.Bar)
	}
}

type stubTickerClient struct {
	seenSymbol string
	book       sandbox.BookTicker
	err        error
}

func (c *stubTickerClient) GetBookTicker(_ context.Context, symbol string) (sandbox.BookTicker, error) {
	c.seenSymbol = symbol
	return c.book, c.err
}

func TestLiveBinanceFeedNormalizesSymbol(t *testing.T) {
	client := &stubTickerClient{
		book: sandbox.BookTicker{
			Symbol:   "BTCUSDT",
			BidPrice: "100.00",
			AskPrice: "100.10",
		},
	}
	feed := sandbox.NewLiveBinanceFeed(client)

	got, err := feed.GetBookTicker(context.Background(), " btc ")
	if err != nil {
		t.Fatalf("GetBookTicker failed: %v", err)
	}
	if client.seenSymbol != "BTCUSDT" {
		t.Fatalf("expected normalized symbol BTCUSDT, got %s", client.seenSymbol)
	}
	if got.Symbol != "BTCUSDT" {
		t.Fatalf("expected returned symbol BTCUSDT, got %s", got.Symbol)
	}
}

func TestNormalizeSymbolRejectsInvalidInput(t *testing.T) {
	if _, err := sandbox.NormalizeSymbol(" "); !errors.Is(err, sandbox.ErrInvalidSymbol) {
		t.Fatalf("expected ErrInvalidSymbol for empty symbol, got %v", err)
	}
	if _, err := sandbox.NormalizeSymbol("btc/usdt"); !errors.Is(err, sandbox.ErrInvalidSymbol) {
		t.Fatalf("expected ErrInvalidSymbol for slash symbol, got %v", err)
	}
	if got, err := sandbox.NormalizeLiveSymbol("eth"); err != nil || got != "ETHUSDT" {
		t.Fatalf("expected ETHUSDT, got %s, err=%v", got, err)
	}
}
