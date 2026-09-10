// Package crypto 提供加密貨幣的行情來源。
package crypto

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultBaseURL  = "https://api.binance.com"
	defaultInterval = time.Second
	requestTimeout  = 5 * time.Second
)

// Binance 以 REST 輪詢取得最新成交價。
//
// 這裡刻意不用 WebSocket：快取的新鮮度上限是 1.5 秒，每秒輪詢就足夠，
// 而且少了連線管理與重連邏輯。要換成 WebSocket 時只要換掉這個型別，
// Market Runtime 不受影響。
type Binance struct {
	BaseURL  string
	Interval time.Duration
	Client   *http.Client
}

func NewBinance() *Binance {
	return &Binance{
		BaseURL:  defaultBaseURL,
		Interval: defaultInterval,
		Client:   &http.Client{Timeout: requestTimeout},
	}
}

func (b *Binance) Name() string { return "binance" }

// Stream 立刻抓一次價格，之後每隔 Interval 抓一次，直到 ctx 結束。
// 任何一次抓取失敗就結束訂閱並回報錯誤，由 Runtime 決定是否重新啟動；
// 這樣比在這裡藏一層重試迴圈好追。
func (b *Binance) Stream(ctx context.Context, symbol string, out chan<- float64) error {
	interval := b.Interval
	if interval <= 0 {
		interval = defaultInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		price, err := b.fetch(ctx, symbol)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		select {
		case out <- price:
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type tickerPrice struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

func (b *Binance) fetch(ctx context.Context, symbol string) (float64, error) {
	base := b.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	endpoint := base + "/api/v3/ticker/price?symbol=" + url.QueryEscape(symbol)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}

	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("binance 連線失敗: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("binance 回應 %d（symbol=%s）", resp.StatusCode, symbol)
	}

	var payload tickerPrice
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("binance 回應無法解析: %w", err)
	}

	price, err := strconv.ParseFloat(payload.Price, 64)
	if err != nil {
		return 0, fmt.Errorf("binance 價格格式錯誤 %q: %w", payload.Price, err)
	}
	if price <= 0 {
		return 0, fmt.Errorf("binance 回報非正數價格 %v（symbol=%s）", price, symbol)
	}
	return price, nil
}
