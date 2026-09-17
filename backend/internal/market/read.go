package market

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	DefaultOHLCVLimit = 100
	MaxOHLCVLimit     = 500
)

var (
	ErrReadUnsupported     = errors.New("行情來源不支援此查詢")
	ErrUnknownSymbol       = errors.New("未知的 symbol")
	ErrUnsupportedInterval = errors.New("不支援的 interval")
	ErrInvalidTimeRange    = errors.New("行情時間範圍無效")
	ErrInvalidLimit        = errors.New("行情 limit 無效")
	ErrInvalidSourceData   = errors.New("行情來源資料無效")
)

var supportedOHLCVIntervals = map[string]bool{
	"1m": true, "5m": true, "15m": true, "1h": true, "4h": true, "1d": true,
}

type Candle struct {
	OpenTime  time.Time `json:"open_time"`
	CloseTime time.Time `json:"close_time"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
}

type OHLCVSource interface {
	OHLCV(context.Context, string, string, time.Time, time.Time, int) ([]Candle, error)
}

type OHLCVResult struct {
	Market             string    `json:"market"`
	Symbol             string    `json:"symbol"`
	Interval           string    `json:"interval"`
	StartTime          time.Time `json:"start_time"`
	EndTime            time.Time `json:"end_time"`
	TimeZone           string    `json:"time_zone"`
	IncludesIncomplete bool      `json:"includes_incomplete"`
	Candles            []Candle  `json:"candles"`
}

type PriceFilter struct {
	MinPrice string `json:"min_price"`
	MaxPrice string `json:"max_price"`
	TickSize string `json:"tick_size"`
}

type QuantityFilter struct {
	MinQuantity string `json:"min_quantity"`
	MaxQuantity string `json:"max_quantity"`
	StepSize    string `json:"step_size"`
}

type MarketInfo struct {
	Symbol               string          `json:"-"`
	Status               string          `json:"status"`
	BaseAsset            string          `json:"base_asset"`
	QuoteAsset           string          `json:"quote_asset"`
	OrderTypes           []string        `json:"order_types"`
	SpotTradingAllowed   bool            `json:"spot_trading_allowed"`
	MarginTradingAllowed bool            `json:"margin_trading_allowed"`
	PriceFilter          *PriceFilter    `json:"price_filter"`
	QuantityFilter       *QuantityFilter `json:"quantity_filter"`
	MinNotional          *string         `json:"min_notional"`
}

type MarketInfoSource interface {
	MarketInfo(context.Context, string) (MarketInfo, error)
}

type MarketInfoResult struct {
	Market           string     `json:"market"`
	Symbol           string     `json:"symbol"`
	Source           string     `json:"source"`
	UpdatedAt        time.Time  `json:"updated_at"`
	SourceRules      MarketInfo `json:"source_rules"`
	BackendExecution struct {
		Mode                string `json:"mode"`
		SourceRulesEnforced bool   `json:"source_rules_enforced"`
	} `json:"backend_execution"`
}

func (r *Runtime) GetOHLCV(ctx context.Context, marketName, symbol, interval string, start, end time.Time, limit int) (OHLCVResult, error) {
	source, marketName, symbol, err := r.readSource(marketName, symbol)
	if err != nil {
		return OHLCVResult{}, err
	}
	if !supportedOHLCVIntervals[interval] {
		return OHLCVResult{}, fmt.Errorf("%w: %s", ErrUnsupportedInterval, interval)
	}
	start, end = start.UTC(), end.UTC()
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return OHLCVResult{}, ErrInvalidTimeRange
	}
	if limit < 1 || limit > MaxOHLCVLimit {
		return OHLCVResult{}, ErrInvalidLimit
	}
	reader, ok := source.(OHLCVSource)
	if !ok {
		return OHLCVResult{}, ErrReadUnsupported
	}
	candles, err := reader.OHLCV(ctx, symbol, interval, start, end, limit)
	if err != nil {
		return OHLCVResult{}, err
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	completed := make([]Candle, 0, min(len(candles), limit))
	now := r.now().UTC()
	for _, candle := range candles {
		candle.OpenTime, candle.CloseTime = candle.OpenTime.UTC(), candle.CloseTime.UTC()
		if candle.OpenTime.Before(start) || !candle.OpenTime.Before(end) || !candle.CloseTime.Before(now) {
			continue
		}
		if !validCandle(candle) {
			return OHLCVResult{}, ErrInvalidSourceData
		}
		completed = append(completed, candle)
		if len(completed) == limit {
			break
		}
	}
	return OHLCVResult{
		Market: marketName, Symbol: symbol, Interval: interval, StartTime: start, EndTime: end,
		TimeZone: "UTC", IncludesIncomplete: false, Candles: completed,
	}, nil
}

func (r *Runtime) GetMarketInfo(ctx context.Context, marketName, symbol string) (MarketInfoResult, error) {
	source, marketName, symbol, err := r.readSource(marketName, symbol)
	if err != nil {
		return MarketInfoResult{}, err
	}
	reader, ok := source.(MarketInfoSource)
	if !ok {
		return MarketInfoResult{}, ErrReadUnsupported
	}
	info, err := reader.MarketInfo(ctx, symbol)
	if err != nil {
		return MarketInfoResult{}, err
	}
	if info.Symbol != "" && info.Symbol != symbol {
		return MarketInfoResult{}, ErrInvalidSourceData
	}
	info.Symbol = ""
	result := MarketInfoResult{Market: marketName, Symbol: symbol, Source: source.Name(), UpdatedAt: r.now().UTC(), SourceRules: info}
	result.BackendExecution.Mode = "virtual"
	result.BackendExecution.SourceRulesEnforced = false
	return result, nil
}

func (r *Runtime) readSource(marketName, symbol string) (Source, string, string, error) {
	marketName, symbol, err := normalize(marketName, symbol)
	if err != nil {
		return nil, "", "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, "", "", ErrClosed
	}
	source, ok := r.sources[marketName]
	if !ok {
		return nil, "", "", fmt.Errorf("%w: %s", ErrUnknownMarket, marketName)
	}
	return source, marketName, symbol, nil
}

func validCandle(c Candle) bool {
	return !c.OpenTime.IsZero() && c.OpenTime.Before(c.CloseTime) && finite(c.Open) && finite(c.High) && finite(c.Low) && finite(c.Close) && finite(c.Volume) &&
		c.Open > 0 && c.High > 0 && c.Low > 0 &&
		c.Close > 0 && c.Volume >= 0 && c.High >= c.Open && c.High >= c.Close && c.Low <= c.Open && c.Low <= c.Close && c.Low <= c.High
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
