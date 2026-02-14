package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type BinanceClient struct {
	BaseURL string
	HTTP    *http.Client
}

func NewBinanceClient(baseURL string) *BinanceClient {
	return &BinanceClient{
		BaseURL: baseURL,
		HTTP: &http.Client{
			Timeout: 6 * time.Second,
		},
	}
}

type BinanceAPIError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

var ErrRateLimited = errors.New("binance rate limited")

func (e *BinanceAPIError) Error() string {
	return fmt.Sprintf("binance api error code=%d msg=%s", e.Code, e.Msg)
}

func (c *BinanceClient) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return err
	}
	u.Path = path
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sandbox-bot/0.1")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// 429/418：會帶 Retry-After，需退避，否則可能被 ban :contentReference[oaicite:4]{index=4}
	if resp.StatusCode == 429 || resp.StatusCode == 418 {
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter != "" {
			return fmt.Errorf("%w: status=%d retry_after=%ss body=%s", ErrRateLimited, resp.StatusCode, retryAfter, string(body))
		}
		return fmt.Errorf("%w: status=%d body=%s", ErrRateLimited, resp.StatusCode, string(body))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 盡量解析 Binance error 格式
		var apiErr BinanceAPIError
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Msg != "" {
			return &apiErr
		}
		return fmt.Errorf("http status=%d body=%s", resp.StatusCode, string(body))
	}

	return json.Unmarshal(body, out)
}

type BookTicker struct {
	Symbol   string `json:"symbol"`
	BidPrice string `json:"bidPrice"`
	BidQty   string `json:"bidQty"`
	AskPrice string `json:"askPrice"`
	AskQty   string `json:"askQty"`
}
type LatestData struct {
	mu   sync.Mutex
	data *BookTicker
}

func (c *BinanceClient) GetBookTicker(ctx context.Context, symbol string) (BookTicker, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	var out BookTicker
	return out, c.getJSON(ctx, "/api/v3/ticker/bookTicker", q, &out)
}
