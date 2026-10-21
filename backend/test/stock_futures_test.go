package test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"backend/internal/market"
	"backend/internal/market/crypto"
)

func TestStockMarketReadsBinanceUSDMFutures(t *testing.T) {
	start := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/fapi/v1/ticker/price":
			_, _ = w.Write([]byte(`{"symbol":"TSLAUSDT","price":"380.23"}`))
		case "/fapi/v1/klines":
			_, _ = w.Write([]byte(`[[1789293600000,"380.1","381.2","379.3","380.4","12.5",1789293659999,"0",4,"0","0","0"]]`))
		case "/fapi/v1/exchangeInfo":
			// fapi ignores ?symbol= and lists every contract, without the spot-only flags.
			_, _ = w.Write([]byte(`{"symbols":[` +
				`{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","orderTypes":["LIMIT","MARKET"],"filters":[]},` +
				`{"symbol":"TSLAUSDT","status":"TRADING","baseAsset":"TSLA","quoteAsset":"USDT","orderTypes":["LIMIT","MARKET"],"filters":[` +
				`{"filterType":"PRICE_FILTER","minPrice":"0.01","maxPrice":"100000","tickSize":"0.01"},` +
				`{"filterType":"LOT_SIZE","minQty":"0.01","maxQty":"10000","stepSize":"0.01"},` +
				`{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`))
		default:
			t.Errorf("Binance request=%s", request.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	stock := crypto.NewBinanceFutures()
	stock.BaseURL = upstream.URL
	stock.Client = upstream.Client()
	handler := newMarketReadAPI(t, map[string]market.Source{market.Crypto: streamOnlySource{}, market.Stock: stock}, start.Add(2*time.Minute))
	ohlcv := "/v1/market/ohlcv?market=stock&symbol=TSLAUSDT&interval=1m&limit=1&start_time=" + url.QueryEscape(start.Format(time.RFC3339)) +
		"&end_time=" + url.QueryEscape(start.Add(time.Minute).Format(time.RFC3339))
	for _, test := range []struct {
		path      string
		status    int
		fragments []string
	}{
		{"/v1/market/price?market=stock&symbol=TSLAUSDT", http.StatusOK, []string{`"market":"stock"`, `"price":380.23`}},
		{ohlcv, http.StatusOK, []string{`"open":380.1`, `"volume":12.5`}},
		{"/v1/market/info?market=stock&symbol=TSLAUSDT", http.StatusOK, []string{
			`"base_asset":"TSLA"`, `"tick_size":"0.01"`, `"step_size":"0.01"`, `"min_notional":"5"`,
		}},
		{"/v1/market/info?market=stock&symbol=AAPLUSDT", http.StatusNotFound, []string{`"error":"symbol_not_found"`}},
	} {
		status, raw := marketRequest(t, handler, test.path)
		for _, fragment := range test.fragments {
			if status != test.status || !bytes.Contains(raw, []byte(fragment)) {
				t.Fatalf("%s status=%d body=%s missing=%s", test.path, status, raw, fragment)
			}
		}
	}
}
