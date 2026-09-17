package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/api"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
	"backend/internal/market"
	"backend/internal/trading"
)

const tradingUserToken = "ticket-trading-user"

// settablePriceSource streams the currently configured price so tests can move the
// market between requests and matching ticks. stall parks the stream so that every
// GetPrice has to wait until resume, which gives tests a place to interleave a second
// request while the first one is between its read and its transaction.
type settablePriceSource struct {
	mu    sync.Mutex
	price float64
	gate  *priceGate
}

type priceGate struct {
	parked chan struct{}
	resume chan struct{}
}

func (*settablePriceSource) Name() string { return "settable" }

func (s *settablePriceSource) set(price float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.price = price
}

func (s *settablePriceSource) Stream(ctx context.Context, _ string, out chan<- float64) error {
	for {
		s.mu.Lock()
		price, gate := s.price, s.gate
		s.mu.Unlock()
		if gate != nil {
			select {
			case gate.parked <- struct{}{}:
			default:
			}
			select {
			case <-gate.resume:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case out <- price:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// stall returns once the stream is parked; no further sample is published until resume.
func (s *settablePriceSource) stall() {
	gate := &priceGate{parked: make(chan struct{}, 1), resume: make(chan struct{})}
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
	<-gate.parked
}

func (s *settablePriceSource) resume() {
	s.mu.Lock()
	gate := s.gate
	s.gate = nil
	s.mu.Unlock()
	close(gate.resume)
}

// countingClock is the market Runtime clock. GetPrice reads it at least twice per attempt,
// so a test can tell that a request has reached its price lookup (i.e. finished the reads
// that precede it) without a sleep.
type countingClock struct{ reads atomic.Int64 }

func (c *countingClock) now() time.Time {
	c.reads.Add(1)
	return time.Now()
}

type tradingAPI struct {
	handler http.Handler
	engine  *trading.Engine
	store   *database.Store
	trader  *trading.Service
	source  *settablePriceSource
	prices  *market.Runtime
	clock   *countingClock
	dbPath  string
}

func newTradingAPI(t *testing.T, dbPath string) *tradingAPI {
	t.Helper()
	if dbPath == "" {
		dbPath = filepath.Join(t.TempDir(), "backend.db")
	}
	store, err := database.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newTradingAPIWithStore(t, store, dbPath)
}

func newTradingAPIWithStore(t *testing.T, store *database.Store, dbPath string) *tradingAPI {
	t.Helper()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	logger, err := logging.New("ticket-trading", t.TempDir(), 1000, false)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	logger.SetConsole(io.Discard)
	t.Cleanup(func() { _ = logger.Close() })
	source := &settablePriceSource{price: 60000}
	clock := &countingClock{}
	prices := market.New(map[string]market.Source{market.Crypto: source}, logger, market.Options{
		FreshTTL: time.Millisecond, WaitTimeout: 2 * time.Second, Now: clock.now,
	})
	t.Cleanup(prices.Close)
	cfg := &config.Config{UserToken: tradingUserToken, UserName: "ticket", FeeRateMaker: 0.0002, FeeRateTaker: 0.0004}
	trader := trading.New(store, prices, cfg.FeeRateMaker, cfg.FeeRateTaker)
	return &tradingAPI{
		handler: api.New(cfg, store, prices, trader, logger),
		engine:  trading.NewEngine(trader, time.Hour, logger),
		store:   store,
		trader:  trader,
		source:  source, prices: prices, clock: clock, dbPath: dbPath,
	}
}

// setPrice moves the market and waits until the Backend price cache observed it.
func (a *tradingAPI) setPrice(t *testing.T, price float64) {
	t.Helper()
	a.source.set(price)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := a.prices.GetPrice(context.Background(), market.Crypto, "BTCUSDT")
		if err == nil && got.Price == price {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("price cache never observed %v", price)
}

// stallPrices parks the price stream and returns once the cache is stale, so the next
// GetPrice blocks until resumePrices. The returned mark is the clock reading to pass to
// waitForPriceLookup.
func (a *tradingAPI) stallPrices(t *testing.T) int64 {
	t.Helper()
	a.source.stall()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		probe, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, err := a.prices.GetPrice(probe, market.Crypto, "BTCUSDT")
		cancel()
		if err != nil {
			return a.clock.reads.Load()
		}
	}
	t.Fatal("price cache never went stale after the stream was parked")
	return 0
}

func (a *tradingAPI) resumePrices() { a.source.resume() }

// waitForLedgerEvent returns once the newest ledger entry is event by run. The ledger
// endpoint touches no price, so it observes a committed change while prices are stalled.
func (a *tradingAPI) waitForLedgerEvent(t *testing.T, token, event string, run int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entries := a.ledger(t, token, "?limit=1"); len(entries) == 1 && entries[0].Event == event && entries[0].Source != nil && entries[0].Source.RunID == run {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ledger never showed %s by run %d", event, run)
}

// waitForPriceLookup returns once a GetPrice started after mark is waiting for the parked
// stream: the Runtime reads its clock twice per lookup, and nothing else reads it more than
// once a second while no sample is published.
func (a *tradingAPI) waitForPriceLookup(t *testing.T, mark int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a.clock.reads.Load() >= mark+2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no price lookup started while the stream was parked")
}

func (a *tradingAPI) request(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	a.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func (a *tradingAPI) createAccount(t *testing.T, name string, balance float64) (string, string) {
	t.Helper()
	status, raw := a.request(t, http.MethodPost, "/v1/accounts", tradingUserToken, map[string]any{"user_name": name, "initial_balance": balance})
	if status != http.StatusCreated {
		t.Fatalf("create Account status=%d body=%s", status, raw)
	}
	var account struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &account); err != nil || account.ID == "" || account.Token == "" {
		t.Fatalf("decode Account: %v %s", err, raw)
	}
	return account.ID, account.Token
}

type sourceView struct {
	SessionID string `json:"session_id"`
	RunID     int64  `json:"run_id"`
}

type ledgerEntryView struct {
	Seq          int64       `json:"seq"`
	Event        string      `json:"event"`
	OrderID      string      `json:"order_id"`
	TradeID      string      `json:"trade_id"`
	PositionID   string      `json:"position_id"`
	Trigger      string      `json:"trigger"`
	Quantity     float64     `json:"quantity"`
	Price        float64     `json:"price"`
	Fee          float64     `json:"fee"`
	RealizedPnL  float64     `json:"realized_pnl"`
	BalanceDelta float64     `json:"balance_delta"`
	BalanceAfter float64     `json:"balance_after"`
	StopLoss     float64     `json:"stop_loss"`
	TakeProfit   float64     `json:"take_profit"`
	Source       *sourceView `json:"source"`
	CreatedAt    time.Time   `json:"created_at"`
}

func (a *tradingAPI) ledger(t *testing.T, token, query string) []ledgerEntryView {
	t.Helper()
	status, raw := a.request(t, http.MethodGet, "/v1/ledger"+query, token, nil)
	if status != http.StatusOK {
		t.Fatalf("ledger status=%d body=%s", status, raw)
	}
	var body struct {
		Entries []ledgerEntryView `json:"entries"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode ledger: %v %s", err, raw)
	}
	return body.Entries
}

func decodeJSON[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}

func agentSource(sessionID string, runID int64) map[string]any {
	return map[string]any{"session_id": sessionID, "run_id": runID}
}
