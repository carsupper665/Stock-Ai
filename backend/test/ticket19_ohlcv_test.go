package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/api"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
	"backend/internal/market"
	"backend/internal/market/crypto"
	"backend/internal/trading"
)

const ticketMarketToken = "ticket-market-user"

type marketReadSource struct {
	ohlcv func(context.Context, string, string, time.Time, time.Time, int) ([]market.Candle, error)
	info  func(context.Context, string) (market.MarketInfo, error)
}

func (marketReadSource) Name() string { return "fixture-source" }

func (marketReadSource) Stream(ctx context.Context, _ string, _ chan<- float64) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s marketReadSource) OHLCV(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]market.Candle, error) {
	return s.ohlcv(ctx, symbol, interval, start, end, limit)
}

func (s marketReadSource) MarketInfo(ctx context.Context, symbol string) (market.MarketInfo, error) {
	return s.info(ctx, symbol)
}

type streamOnlySource struct{}

func (streamOnlySource) Name() string { return "stream-only" }
func (streamOnlySource) Stream(ctx context.Context, _ string, _ chan<- float64) error {
	<-ctx.Done()
	return ctx.Err()
}

func newMarketReadAPI(t *testing.T, sources map[string]market.Source, now time.Time) http.Handler {
	t.Helper()
	store, err := database.OpenSQLite(filepath.Join(t.TempDir(), "backend.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	logger, err := logging.New("ticket-market", t.TempDir(), 1000, false)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	logger.SetConsole(io.Discard)
	t.Cleanup(func() { _ = logger.Close() })
	prices := market.New(sources, logger, market.Options{WaitTimeout: time.Second, Now: func() time.Time { return now }})
	t.Cleanup(prices.Close)
	cfg := &config.Config{UserToken: ticketMarketToken, UserName: "ticket", FeeRateMaker: 0.0002, FeeRateTaker: 0.0004}
	return api.New(cfg, store, prices, trading.New(store, prices, cfg.FeeRateMaker, cfg.FeeRateTaker), logger)
}

func marketRequest(t *testing.T, handler http.Handler, path string) (int, []byte) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+ticketMarketToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func newBinanceReadAPI(t *testing.T, now time.Time, upstream http.HandlerFunc) (http.Handler, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		upstream(w, request)
	}))
	t.Cleanup(server.Close)
	binance := crypto.NewBinance()
	binance.BaseURL = server.URL
	binance.Client = server.Client()
	return newMarketReadAPI(t, map[string]market.Source{market.Crypto: binance}, now), &calls
}

func TestTicket19OHLCVHTTPContractFiltersSortsAndCapsCompletedCandles(t *testing.T) {
	start := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	now := start.Add(3*time.Minute + 30*time.Second)
	var calls atomic.Int32
	source := marketReadSource{ohlcv: func(_ context.Context, symbol, interval string, gotStart, gotEnd time.Time, limit int) ([]market.Candle, error) {
		calls.Add(1)
		if symbol != "BTCUSDT" || interval != "1m" || !gotStart.Equal(start) || !gotEnd.Equal(end) || limit != 2 {
			t.Errorf("source query = %s %s %s %s %d", symbol, interval, gotStart, gotEnd, limit)
		}
		return []market.Candle{
			{OpenTime: start.Add(2 * time.Minute), CloseTime: start.Add(3*time.Minute - time.Millisecond), Open: 102, High: 106, Low: 101, Close: 105, Volume: 12},
			{OpenTime: start.Add(-time.Minute), CloseTime: start.Add(-time.Millisecond), Open: 90, High: 91, Low: 89, Close: 90, Volume: 1},
			{OpenTime: start, CloseTime: start.Add(time.Minute - time.Millisecond), Open: 100, High: 103, Low: 99, Close: 102, Volume: 10},
			{OpenTime: start.Add(3 * time.Minute), CloseTime: start.Add(4*time.Minute - time.Millisecond), Open: 105, High: 110, Low: 104, Close: 108, Volume: 20},
		}, nil
	}}
	handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: source}, now)
	path := "/v1/market/ohlcv?market=CRYPTO&symbol=btcusdt&interval=1m&start_time=" +
		url.QueryEscape(start.Format(time.RFC3339)) + "&end_time=" + url.QueryEscape(end.Format(time.RFC3339)) + "&limit=2"
	status, raw := marketRequest(t, handler, path)
	if status != http.StatusOK {
		t.Fatalf("status = %d body = %s", status, raw)
	}
	var body struct {
		Market             string          `json:"market"`
		Symbol             string          `json:"symbol"`
		Interval           string          `json:"interval"`
		StartTime          time.Time       `json:"start_time"`
		EndTime            time.Time       `json:"end_time"`
		TimeZone           string          `json:"time_zone"`
		IncludesIncomplete bool            `json:"includes_incomplete"`
		Candles            []market.Candle `json:"candles"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if calls.Load() != 1 || body.Market != "crypto" || body.Symbol != "BTCUSDT" || body.Interval != "1m" ||
		!body.StartTime.Equal(start) || !body.EndTime.Equal(end) || body.TimeZone != "UTC" || body.IncludesIncomplete {
		t.Fatalf("metadata = %+v calls=%d", body, calls.Load())
	}
	if len(body.Candles) != 2 || !body.Candles[0].OpenTime.Equal(start) || !body.Candles[1].OpenTime.Equal(start.Add(2*time.Minute)) {
		t.Fatalf("candles = %+v", body.Candles)
	}
}

func TestTicket19OHLCVHTTPValidationEmptyAndSourceFailures(t *testing.T) {
	start := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	var sourceErr error
	var calls atomic.Int32
	source := marketReadSource{ohlcv: func(context.Context, string, string, time.Time, time.Time, int) ([]market.Candle, error) {
		calls.Add(1)
		return nil, sourceErr
	}}
	handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: source, market.Stock: streamOnlySource{}}, end.Add(time.Hour))
	valid := "?symbol=BTCUSDT&interval=1m&start_time=" + url.QueryEscape(start.Format(time.RFC3339)) + "&end_time=" + url.QueryEscape(end.Format(time.RFC3339))
	validWithoutInterval := "?symbol=BTCUSDT&start_time=" + url.QueryEscape(start.Format(time.RFC3339)) + "&end_time=" + url.QueryEscape(end.Format(time.RFC3339))

	status, raw := marketRequest(t, handler, "/v1/market/ohlcv"+valid)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"candles":[]`)) {
		t.Fatalf("empty response status=%d body=%s", status, raw)
	}

	for _, test := range []struct {
		name string
		path string
		code string
	}{
		{"missing symbol", "/v1/market/ohlcv?interval=1m&start_time=" + url.QueryEscape(start.Format(time.RFC3339)) + "&end_time=" + url.QueryEscape(end.Format(time.RFC3339)), "invalid_request"},
		{"unsupported interval", "/v1/market/ohlcv" + validWithoutInterval + "&interval=2m", "unsupported_interval"},
		{"invalid range", "/v1/market/ohlcv?symbol=BTCUSDT&interval=1m&start_time=" + url.QueryEscape(end.Format(time.RFC3339)) + "&end_time=" + url.QueryEscape(start.Format(time.RFC3339)), "invalid_request"},
		{"empty limit", "/v1/market/ohlcv" + valid + "&limit=", "invalid_request"},
		{"zero limit", "/v1/market/ohlcv" + valid + "&limit=0", "invalid_request"},
		{"too large limit", "/v1/market/ohlcv" + valid + "&limit=501", "invalid_request"},
		{"unknown market", "/v1/market/ohlcv" + valid + "&market=forex", "invalid_request"},
		{"source unsupported", "/v1/market/ohlcv" + valid + "&market=stock", "not_implemented"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, raw := marketRequest(t, handler, test.path)
			if status < 400 || !bytes.Contains(raw, []byte(`"error":"`+test.code+`"`)) {
				t.Fatalf("status=%d body=%s", status, raw)
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid requests reached source: calls=%d", calls.Load())
	}

	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{market.ErrUnknownSymbol, http.StatusNotFound, "symbol_not_found"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "market_timeout"},
		{errors.New("feed down"), http.StatusBadGateway, "market_unavailable"},
	} {
		sourceErr = test.err
		status, raw := marketRequest(t, handler, "/v1/market/ohlcv"+valid)
		if status != test.status || !bytes.Contains(raw, []byte(`"error":"`+test.code+`"`)) {
			t.Fatalf("error %v status=%d body=%s", test.err, status, raw)
		}
	}
}

func TestTicket19OHLCVUsesBinanceKlineContract(t *testing.T) {
	start := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/api/v3/klines" || query.Get("symbol") != "BTCUSDT" || query.Get("interval") != "1m" ||
			query.Get("startTime") != "1789293600000" || query.Get("endTime") != "1789293659999" || query.Get("limit") != "1" {
			t.Errorf("Binance request = %s", request.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`[[1789293600000,"100.1","102.2","99.3","101.4","12.5",1789293659999,"0",4,"0","0","0"]]`))
	}))
	t.Cleanup(upstream.Close)
	binance := crypto.NewBinance()
	binance.BaseURL = upstream.URL
	binance.Client = upstream.Client()
	handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: binance}, end.Add(time.Minute))
	path := "/v1/market/ohlcv?symbol=BTCUSDT&interval=1m&start_time=" + url.QueryEscape(start.Format(time.RFC3339)) +
		"&end_time=" + url.QueryEscape(end.Format(time.RFC3339)) + "&limit=1"
	status, raw := marketRequest(t, handler, path)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"open":100.1`)) || !bytes.Contains(raw, []byte(`"volume":12.5`)) {
		t.Fatalf("status=%d body=%s", status, raw)
	}
}

func TestTicket19OHLCVBinanceFractionalMillisecondBoundsAndEmptyConvertedRange(t *testing.T) {
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	handler, calls := newBinanceReadAPI(t, base.Add(time.Minute), func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("startTime") != "1789293600001" || request.URL.Query().Get("endTime") != "1789293600001" ||
			request.URL.Query().Get("limit") != "500" {
			t.Errorf("fractional bounds request=%s", request.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`[]`))
	})
	path := "/v1/market/ohlcv?symbol=BTCUSDT&interval=1m&start_time=" + url.QueryEscape(base.Add(500*time.Microsecond).Format(time.RFC3339Nano)) +
		"&end_time=" + url.QueryEscape(base.Add(1200*time.Microsecond).Format(time.RFC3339Nano)) + "&limit=500"
	status, raw := marketRequest(t, handler, path)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"candles":[]`)) || calls.Load() != 1 {
		t.Fatalf("fractional range status=%d calls=%d body=%s", status, calls.Load(), raw)
	}

	emptyHandler, emptyCalls := newBinanceReadAPI(t, base.Add(time.Minute), func(http.ResponseWriter, *http.Request) {
		t.Error("sub-millisecond empty range must not perform source I/O")
	})
	emptyPath := "/v1/market/ohlcv?symbol=BTCUSDT&interval=1m&start_time=" + url.QueryEscape(base.Add(time.Nanosecond).Format(time.RFC3339Nano)) +
		"&end_time=" + url.QueryEscape(base.Add(999999*time.Nanosecond).Format(time.RFC3339Nano))
	status, raw = marketRequest(t, emptyHandler, emptyPath)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"candles":[]`)) || emptyCalls.Load() != 0 {
		t.Fatalf("empty converted range status=%d calls=%d body=%s", status, emptyCalls.Load(), raw)
	}
}

func TestTicket19OHLCVBinancePayloadClassification(t *testing.T) {
	start := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	path := "/v1/market/ohlcv?symbol=BTCUSDT&interval=1m&start_time=" + url.QueryEscape(start.Format(time.RFC3339)) +
		"&end_time=" + url.QueryEscape(start.Add(time.Minute).Format(time.RFC3339))
	for _, test := range []struct {
		name       string
		status     int
		payload    string
		wantStatus int
		wantCode   string
	}{
		{"malformed JSON", http.StatusOK, `[`, http.StatusBadGateway, "market_response_invalid"},
		{"wrong top-level shape", http.StatusOK, `{}`, http.StatusBadGateway, "market_response_invalid"},
		{"trailing object", http.StatusOK, `[] {}`, http.StatusBadGateway, "market_response_invalid"},
		{"null rows", http.StatusOK, `null`, http.StatusBadGateway, "market_response_invalid"},
		{"wrong row shape", http.StatusOK, `[[1789293600000,"1"]]`, http.StatusBadGateway, "market_response_invalid"},
		{"unknown symbol", http.StatusBadRequest, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusNotFound, "symbol_not_found"},
		{"empty rows", http.StatusOK, `[]`, http.StatusOK, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, calls := newBinanceReadAPI(t, start.Add(time.Hour), func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.payload))
			})
			status, raw := marketRequest(t, handler, path)
			if status != test.wantStatus || calls.Load() != 1 {
				t.Fatalf("status=%d calls=%d body=%s", status, calls.Load(), raw)
			}
			if test.wantCode == "" {
				if !bytes.Contains(raw, []byte(`"candles":[]`)) {
					t.Fatalf("empty rows body=%s", raw)
				}
			} else if !bytes.Contains(raw, []byte(`"error":"`+test.wantCode+`"`)) {
				t.Fatalf("body=%s", raw)
			}
		})
	}
}

type ticketRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ticketRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTicket19OHLCVBinanceTransportAndDeadlineRemainDistinctWithoutRetry(t *testing.T) {
	start := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	path := "/v1/market/ohlcv?symbol=BTCUSDT&interval=1m&start_time=" + url.QueryEscape(start.Format(time.RFC3339)) +
		"&end_time=" + url.QueryEscape(start.Add(time.Minute).Format(time.RFC3339))
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"transport", errors.New("connection reset"), http.StatusBadGateway, "market_unavailable"},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, "market_timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			binance := crypto.NewBinance()
			binance.BaseURL = "http://market.invalid"
			binance.Client = &http.Client{Transport: ticketRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, test.err
			})}
			handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: binance}, start.Add(time.Hour))
			status, raw := marketRequest(t, handler, path)
			if status != test.status || calls.Load() != 1 || !bytes.Contains(raw, []byte(`"error":"`+test.code+`"`)) {
				t.Fatalf("status=%d calls=%d body=%s", status, calls.Load(), raw)
			}
		})
	}
}
