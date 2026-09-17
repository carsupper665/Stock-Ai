package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/market"
	"backend/internal/market/crypto"
)

func TestTicket20MarketInfoHTTPContractPreservesSourceFactsAndMissingFields(t *testing.T) {
	observed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	source := marketReadSource{info: func(_ context.Context, symbol string) (market.MarketInfo, error) {
		if symbol != "BTCUSDT" {
			t.Errorf("symbol = %q", symbol)
		}
		return market.MarketInfo{
			Symbol: symbol, Status: "BREAK", BaseAsset: "BTC", QuoteAsset: "USDT",
			OrderTypes: []string{"LIMIT", "MARKET"}, SpotTradingAllowed: true,
			PriceFilter:    &market.PriceFilter{MinPrice: "0.01", MaxPrice: "1000000", TickSize: "0.01"},
			QuantityFilter: &market.QuantityFilter{MinQuantity: "0.00001", MaxQuantity: "9000", StepSize: "0.00001"},
			MinNotional:    nil,
		}, nil
	}}
	handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: source}, observed)
	status, raw := marketRequest(t, handler, "/v1/market/info?market=CRYPTO&symbol=btcusdt")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, raw)
	}
	for _, fragment := range [][]byte{
		[]byte(`"market":"crypto"`), []byte(`"symbol":"BTCUSDT"`), []byte(`"source":"fixture-source"`),
		[]byte(`"status":"BREAK"`), []byte(`"min_notional":null`), []byte(`"mode":"virtual"`),
		[]byte(`"source_rules_enforced":false`),
	} {
		if !bytes.Contains(raw, fragment) {
			t.Fatalf("response %s missing %s", raw, fragment)
		}
	}
}

func TestTicket20MarketInfoHTTPUnknownUnsupportedAndTimeout(t *testing.T) {
	var sourceErr error
	var calls atomic.Int32
	source := marketReadSource{info: func(context.Context, string) (market.MarketInfo, error) {
		calls.Add(1)
		return market.MarketInfo{}, sourceErr
	}}
	handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: source, market.Stock: streamOnlySource{}}, time.Now().UTC())
	for _, test := range []struct {
		name   string
		path   string
		err    error
		status int
		code   string
	}{
		{"missing symbol", "/v1/market/info", nil, http.StatusBadRequest, "invalid_request"},
		{"unsupported market", "/v1/market/info?market=forex&symbol=EURUSD", nil, http.StatusBadRequest, "invalid_request"},
		{"source has no info", "/v1/market/info?market=stock&symbol=AAPL", nil, http.StatusNotImplemented, "not_implemented"},
		{"unknown symbol", "/v1/market/info?symbol=UNKNOWN", market.ErrUnknownSymbol, http.StatusNotFound, "symbol_not_found"},
		{"timeout", "/v1/market/info?symbol=BTCUSDT", context.DeadlineExceeded, http.StatusGatewayTimeout, "market_timeout"},
		{"source failure", "/v1/market/info?symbol=BTCUSDT", errors.New("source down"), http.StatusBadGateway, "market_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourceErr = test.err
			status, raw := marketRequest(t, handler, test.path)
			if status != test.status || !bytes.Contains(raw, []byte(`"error":"`+test.code+`"`)) {
				t.Fatalf("status=%d body=%s", status, raw)
			}
		})
	}
	if calls.Load() != 3 {
		t.Fatalf("source calls=%d, want only source-backed cases", calls.Load())
	}
}

func TestTicket20MarketInfoUsesBinanceExchangeInfoWithoutInventingMissingRule(t *testing.T) {
	observed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v3/exchangeInfo" || request.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Errorf("Binance request=%s", request.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","orderTypes":["LIMIT","MARKET"],"isSpotTradingAllowed":true,"isMarginTradingAllowed":false,"filters":[{"filterType":"PRICE_FILTER","minPrice":"0.01","maxPrice":"1000000","tickSize":"0.01"},{"filterType":"LOT_SIZE","minQty":"0.00001","maxQty":"9000","stepSize":"0.00001"}]}]}`))
	}))
	t.Cleanup(upstream.Close)
	binance := crypto.NewBinance()
	binance.BaseURL = upstream.URL
	binance.Client = upstream.Client()
	handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: binance}, observed)
	status, raw := marketRequest(t, handler, "/v1/market/info?symbol=BTCUSDT")
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"source":"binance"`)) ||
		!bytes.Contains(raw, []byte(`"tick_size":"0.01"`)) || !bytes.Contains(raw, []byte(`"min_notional":null`)) ||
		!bytes.Contains(raw, []byte(`"updated_at":"2026-09-13T12:00:00Z"`)) {
		t.Fatalf("status=%d body=%s", status, raw)
	}
}

func validTicket20Symbol() map[string]any {
	return map[string]any{
		"symbol": "BTCUSDT", "status": "TRADING", "baseAsset": "BTC", "quoteAsset": "USDT",
		"orderTypes": []any{"LIMIT", "MARKET"}, "isSpotTradingAllowed": false, "isMarginTradingAllowed": false,
		"filters": []any{},
	}
}

func ticket20Payload(t *testing.T, symbols any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"symbols": symbols})
	if err != nil {
		t.Fatalf("marshal source payload: %v", err)
	}
	return string(raw)
}

func TestTicket20MarketInfoRequiredFieldPresenceAndExplicitFalse(t *testing.T) {
	observed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	valid := validTicket20Symbol()
	valid["filters"] = []any{
		map[string]any{"filterType": "PRICE_FILTER", "minPrice": "0.00000000", "maxPrice": "1000000.00000000", "tickSize": "0.01000000"},
		map[string]any{"filterType": "LOT_SIZE", "minQty": "0.00001000", "maxQty": "9000.00000000", "stepSize": "0.00001000"},
		map[string]any{"filterType": "MIN_NOTIONAL", "minNotional": "0.00000000"},
	}
	handler, calls := newBinanceReadAPI(t, observed, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ticket20Payload(t, []any{valid})))
	})
	status, raw := marketRequest(t, handler, "/v1/market/info?symbol=BTCUSDT")
	for _, fragment := range [][]byte{
		[]byte(`"spot_trading_allowed":false`), []byte(`"margin_trading_allowed":false`),
		[]byte(`"min_price":"0.00000000"`), []byte(`"tick_size":"0.01000000"`),
		[]byte(`"step_size":"0.00001000"`), []byte(`"min_notional":"0.00000000"`),
	} {
		if status != http.StatusOK || !bytes.Contains(raw, fragment) {
			t.Fatalf("status=%d calls=%d body=%s missing=%s", status, calls.Load(), raw, fragment)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("source calls=%d", calls.Load())
	}

	for _, field := range []string{"symbol", "status", "baseAsset", "quoteAsset", "orderTypes", "isSpotTradingAllowed", "isMarginTradingAllowed"} {
		for _, null := range []bool{false, true} {
			name := "missing " + field
			if null {
				name = "null " + field
			}
			t.Run(name, func(t *testing.T) {
				symbol := validTicket20Symbol()
				if null {
					symbol[field] = nil
				} else {
					delete(symbol, field)
				}
				handler, calls := newBinanceReadAPI(t, observed, func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(ticket20Payload(t, []any{symbol})))
				})
				status, raw := marketRequest(t, handler, "/v1/market/info?symbol=BTCUSDT")
				if status != http.StatusBadGateway || calls.Load() != 1 || !bytes.Contains(raw, []byte(`"error":"market_response_invalid"`)) {
					t.Fatalf("status=%d calls=%d body=%s", status, calls.Load(), raw)
				}
			})
		}
	}
}

func TestTicket20MarketInfoAbsentOptionalRulesRemainNull(t *testing.T) {
	observed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	symbol := validTicket20Symbol()
	delete(symbol, "filters")
	handler, calls := newBinanceReadAPI(t, observed, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ticket20Payload(t, []any{symbol})))
	})
	status, raw := marketRequest(t, handler, "/v1/market/info?symbol=BTCUSDT")
	for _, fragment := range [][]byte{
		[]byte(`"price_filter":null`), []byte(`"quantity_filter":null`), []byte(`"min_notional":null`),
	} {
		if status != http.StatusOK || !bytes.Contains(raw, fragment) {
			t.Fatalf("status=%d calls=%d body=%s missing=%s", status, calls.Load(), raw, fragment)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("source calls=%d", calls.Load())
	}
}

func TestTicket20MarketInfoValidatesPresentDecimalRules(t *testing.T) {
	observed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		filter map[string]any
	}{
		{"price missing", map[string]any{"filterType": "PRICE_FILTER", "maxPrice": "1", "tickSize": "0.1"}},
		{"price null", map[string]any{"filterType": "PRICE_FILTER", "minPrice": "0", "maxPrice": nil, "tickSize": "0.1"}},
		{"price negative", map[string]any{"filterType": "PRICE_FILTER", "minPrice": "-1", "maxPrice": "1", "tickSize": "0.1"}},
		{"price non-finite", map[string]any{"filterType": "PRICE_FILTER", "minPrice": "NaN", "maxPrice": "1", "tickSize": "0.1"}},
		{"price numeric", map[string]any{"filterType": "PRICE_FILTER", "minPrice": 0.01, "maxPrice": "1", "tickSize": "0.1"}},
		{"quantity missing", map[string]any{"filterType": "LOT_SIZE", "maxQty": "1", "stepSize": "0.1"}},
		{"quantity null", map[string]any{"filterType": "LOT_SIZE", "minQty": "0", "maxQty": nil, "stepSize": "0.1"}},
		{"quantity negative", map[string]any{"filterType": "LOT_SIZE", "minQty": "0", "maxQty": "1", "stepSize": "-0.1"}},
		{"notional missing", map[string]any{"filterType": "MIN_NOTIONAL"}},
		{"notional null", map[string]any{"filterType": "MIN_NOTIONAL", "minNotional": nil}},
		{"notional infinite", map[string]any{"filterType": "NOTIONAL", "minNotional": "Infinity"}},
		{"notional numeric", map[string]any{"filterType": "NOTIONAL", "minNotional": 10}},
	} {
		t.Run(test.name, func(t *testing.T) {
			symbol := validTicket20Symbol()
			symbol["filters"] = []any{test.filter}
			handler, calls := newBinanceReadAPI(t, observed, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(ticket20Payload(t, []any{symbol})))
			})
			status, raw := marketRequest(t, handler, "/v1/market/info?symbol=BTCUSDT")
			if status != http.StatusBadGateway || calls.Load() != 1 || !bytes.Contains(raw, []byte(`"error":"market_response_invalid"`)) {
				t.Fatalf("status=%d calls=%d body=%s", status, calls.Load(), raw)
			}
		})
	}
}

func TestTicket20MarketInfoBinancePayloadClassification(t *testing.T) {
	observed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	valid := validTicket20Symbol()
	other := validTicket20Symbol()
	other["symbol"] = "ETHUSDT"
	for _, test := range []struct {
		name       string
		status     int
		payload    string
		wantStatus int
		wantCode   string
	}{
		{"malformed JSON", http.StatusOK, `{`, http.StatusBadGateway, "market_response_invalid"},
		{"wrong top-level shape", http.StatusOK, `[]`, http.StatusBadGateway, "market_response_invalid"},
		{"trailing object", http.StatusOK, ticket20Payload(t, []any{valid}) + ` {}`, http.StatusBadGateway, "market_response_invalid"},
		{"symbols missing", http.StatusOK, `{}`, http.StatusBadGateway, "market_response_invalid"},
		{"symbols null", http.StatusOK, `{"symbols":null}`, http.StatusBadGateway, "market_response_invalid"},
		{"symbols empty", http.StatusOK, `{"symbols":[]}`, http.StatusNotFound, "symbol_not_found"},
		{"symbol mismatch", http.StatusOK, ticket20Payload(t, []any{other}), http.StatusBadGateway, "market_response_invalid"},
		{"multiple symbols", http.StatusOK, ticket20Payload(t, []any{valid, other}), http.StatusBadGateway, "market_response_invalid"},
		{"unknown symbol code", http.StatusBadRequest, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusNotFound, "symbol_not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, calls := newBinanceReadAPI(t, observed, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.payload))
			})
			status, raw := marketRequest(t, handler, "/v1/market/info?symbol=BTCUSDT")
			if status != test.wantStatus || calls.Load() != 1 || !bytes.Contains(raw, []byte(`"error":"`+test.wantCode+`"`)) {
				t.Fatalf("status=%d calls=%d body=%s", status, calls.Load(), raw)
			}
		})
	}
}

func TestTicket20MarketInfoTransportAndDeadlineRemainDistinctWithoutRetry(t *testing.T) {
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
			handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: binance}, time.Now().UTC())
			status, raw := marketRequest(t, handler, "/v1/market/info?symbol=BTCUSDT")
			if status != test.status || calls.Load() != 1 || !bytes.Contains(raw, []byte(`"error":"`+test.code+`"`)) {
				t.Fatalf("status=%d calls=%d body=%s", status, calls.Load(), raw)
			}
		})
	}
}
