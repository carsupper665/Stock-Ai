package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"
)

func marketDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name": "get_market_snapshot", "description": "Get the latest market price snapshot.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market": map[string]any{"type": "string"}, "symbol": map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "Exchange symbol with no separator, uppercase, e.g. BTCUSDT or ETHUSDT. Never BTC/USDT or BTC-USDT."},
				}, "required": []string{"symbol"}, "additionalProperties": false,
			},
		},
		{
			"name": "get_ohlcv", "description": "Get completed OHLCV candles in an ascending UTC time range.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market":     map[string]any{"type": "string", "default": "crypto"},
					"symbol":     map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "Exchange symbol with no separator, uppercase, e.g. BTCUSDT or ETHUSDT. Never BTC/USDT or BTC-USDT."},
					"interval":   map[string]any{"type": "string", "enum": []string{"1m", "5m", "15m", "1h", "4h", "1d"}},
					"start_time": map[string]any{"type": "string", "format": "date-time"},
					"end_time":   map[string]any{"type": "string", "format": "date-time"},
					"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100},
				}, "required": []string{"symbol", "interval", "start_time", "end_time"}, "additionalProperties": false,
			},
		},
		{
			"name": "get_market_info", "description": "Get source market rules and their relationship to virtual Backend execution.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market": map[string]any{"type": "string", "default": "crypto"}, "symbol": map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "Exchange symbol with no separator, uppercase, e.g. BTCUSDT or ETHUSDT. Never BTC/USDT or BTC-USDT."},
				}, "required": []string{"symbol"}, "additionalProperties": false,
			},
		},
	}
}

type marketSymbolArguments struct {
	Market json.RawMessage `json:"market"`
	Symbol json.RawMessage `json:"symbol"`
}

func SnapshotQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Market json.RawMessage `json:"market"`
		Symbol json.RawMessage `json:"symbol"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("symbol is required and only market and symbol are accepted")
	}
	market, symbol, err := ParseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	return url.Values{"market": {market}, "symbol": {symbol}}, nil
}

func OHLCVQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Market    json.RawMessage `json:"market"`
		Symbol    json.RawMessage `json:"symbol"`
		Interval  json.RawMessage `json:"interval"`
		StartTime json.RawMessage `json:"start_time"`
		EndTime   json.RawMessage `json:"end_time"`
		Limit     json.RawMessage `json:"limit"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("only market, symbol, interval, start_time, end_time, and limit are accepted")
	}
	market, symbol, err := ParseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	interval, err := RequiredString(rawArguments.Interval, "interval")
	if err != nil || !validOHLCVInterval(interval) {
		return nil, errors.New("interval must be one of 1m, 5m, 15m, 1h, 4h, or 1d")
	}
	startValue, err := RequiredString(rawArguments.StartTime, "start_time")
	if err != nil {
		return nil, err
	}
	endValue, err := RequiredString(rawArguments.EndTime, "end_time")
	if err != nil {
		return nil, err
	}
	start, startErr := time.Parse(time.RFC3339, startValue)
	end, endErr := time.Parse(time.RFC3339, endValue)
	if startErr != nil || endErr != nil || !start.Before(end) {
		return nil, errors.New("start_time and end_time must be RFC3339 timestamps with start_time before end_time")
	}
	limit := 100
	if len(rawArguments.Limit) != 0 {
		if bytes.Equal(bytes.TrimSpace(rawArguments.Limit), []byte("null")) || json.Unmarshal(rawArguments.Limit, &limit) != nil || limit < 1 || limit > 500 {
			return nil, errors.New("limit must be an integer from 1 through 500")
		}
	}
	return url.Values{
		"market": {market}, "symbol": {symbol}, "interval": {interval},
		"start_time": {startValue}, "end_time": {endValue},
		"limit": {strconv.Itoa(limit)},
	}, nil
}

func MarketInfoQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments marketSymbolArguments
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("symbol is required and only market and symbol are accepted")
	}
	market, symbol, err := ParseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	return url.Values{"market": {market}, "symbol": {symbol}}, nil
}

func validOHLCVInterval(interval string) bool {
	switch interval {
	case "1m", "5m", "15m", "1h", "4h", "1d":
		return true
	default:
		return false
	}
}

func ProjectOHLCV(result map[string]json.RawMessage) (any, error) {
	for _, field := range []string{"market", "symbol", "interval", "start_time", "end_time"} {
		if len(result[field]) == 0 {
			return nil, fmt.Errorf("%s is missing or invalid", field)
		}
	}
	var candles []map[string]json.RawMessage
	if raw, ok := result["candles"]; !ok || json.Unmarshal(raw, &candles) != nil || candles == nil {
		return nil, errors.New("candles are missing or invalid")
	}
	rows := make([][]any, 0, len(candles))
	for _, candle := range candles {
		var openTime string
		if json.Unmarshal(candle["open_time"], &openTime) != nil {
			return nil, errors.New("candle open_time is invalid")
		}
		at, err := time.Parse(time.RFC3339Nano, openTime)
		if err != nil {
			return nil, errors.New("candle open_time is invalid")
		}
		row := []any{at.UnixMilli()}
		for _, field := range []string{"open", "high", "low", "close", "volume"} {
			value, ok := candle[field]
			var number float64
			if !ok || json.Unmarshal(value, &number) != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("candle %s is invalid", field)
			}
			row = append(row, json.RawMessage(value))
		}
		rows = append(rows, row)
	}
	projected := SelectFields(result, "market", "symbol", "interval", "time_zone")
	projected["columns"] = json.RawMessage(`["open_time_unix_ms","open","high","low","close","volume"]`)
	encodedRows, err := json.Marshal(rows)
	if err != nil {
		return nil, errors.New("candles are missing or invalid")
	}
	projected["rows"] = encodedRows
	return projected, nil
}

func ProjectMarketInfo(result map[string]json.RawMessage) (any, error) {
	var rules map[string]json.RawMessage
	if raw, ok := result["source_rules"]; !ok || json.Unmarshal(raw, &rules) != nil || rules == nil {
		return nil, errors.New("source_rules are missing or invalid")
	}
	projected := SelectFields(result, "symbol", "source")
	for name, value := range SelectFields(rules, "status", "base_asset", "quote_asset", "order_types", "price_filter", "quantity_filter", "min_notional") {
		projected[name] = value
	}
	var execution map[string]json.RawMessage
	if raw, ok := result["backend_execution"]; !ok || json.Unmarshal(raw, &execution) != nil || execution == nil {
		return nil, errors.New("backend_execution are missing or invalid")
	}
	if value, ok := execution["source_rules_enforced"]; ok {
		projected["source_rules_enforced"] = value
	}
	return projected, nil
}
