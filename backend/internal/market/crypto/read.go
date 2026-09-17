package crypto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"backend/internal/market"
)

const maxMarketReadBody = 4 << 20

func (b *Binance) OHLCV(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]market.Candle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	startMillis := ceilUnixMilliseconds(start)
	endMillis := ceilUnixMilliseconds(end) - 1
	if startMillis > endMillis {
		return []market.Candle{}, nil
	}
	query := url.Values{
		"symbol": {symbol}, "interval": {interval}, "startTime": {strconv.FormatInt(startMillis, 10)},
		"endTime": {strconv.FormatInt(endMillis, 10)}, "limit": {strconv.Itoa(limit)},
	}
	var rows [][]json.RawMessage
	if err := b.getJSON(ctx, "/api/v3/klines?"+query.Encode(), &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, market.ErrInvalidSourceData
	}
	candles := make([]market.Candle, 0, len(rows))
	for _, row := range rows {
		if len(row) != 12 {
			return nil, market.ErrInvalidSourceData
		}
		openTime, err := rawInt64(row[0])
		if err != nil {
			return nil, market.ErrInvalidSourceData
		}
		closeTime, err := rawInt64(row[6])
		if err != nil {
			return nil, market.ErrInvalidSourceData
		}
		values := [5]float64{}
		for i, index := range []int{1, 2, 3, 4, 5} {
			values[i], err = rawFloat(row[index])
			if err != nil {
				return nil, market.ErrInvalidSourceData
			}
		}
		candles = append(candles, market.Candle{
			OpenTime: time.UnixMilli(openTime).UTC(), CloseTime: time.UnixMilli(closeTime).UTC(),
			Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4],
		})
	}
	return candles, nil
}

func (b *Binance) MarketInfo(ctx context.Context, symbol string) (market.MarketInfo, error) {
	var response struct {
		Symbols json.RawMessage `json:"symbols"`
	}
	if err := b.getJSON(ctx, "/api/v3/exchangeInfo?symbol="+url.QueryEscape(symbol), &response); err != nil {
		return market.MarketInfo{}, err
	}
	if len(response.Symbols) == 0 || bytes.Equal(bytes.TrimSpace(response.Symbols), []byte("null")) {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	var symbols []binanceSymbol
	if err := json.Unmarshal(response.Symbols, &symbols); err != nil || symbols == nil {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	if len(symbols) == 0 {
		return market.MarketInfo{}, market.ErrUnknownSymbol
	}
	if len(symbols) != 1 {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	value := symbols[0]
	sourceSymbol, ok := requiredSourceString(value.Symbol)
	if !ok || sourceSymbol != symbol {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	status, ok := requiredSourceString(value.Status)
	if !ok {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	baseAsset, ok := requiredSourceString(value.BaseAsset)
	if !ok {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	quoteAsset, ok := requiredSourceString(value.QuoteAsset)
	if !ok {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	orderTypes, ok := requiredSourceStrings(value.OrderTypes)
	if !ok {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	spotAllowed, ok := requiredSourceBool(value.SpotTradingAllowed)
	if !ok {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	marginAllowed, ok := requiredSourceBool(value.MarginTradingAllowed)
	if !ok {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	info := market.MarketInfo{
		Symbol: symbol, Status: status, BaseAsset: baseAsset, QuoteAsset: quoteAsset,
		OrderTypes: orderTypes, SpotTradingAllowed: spotAllowed, MarginTradingAllowed: marginAllowed,
	}
	if len(value.Filters) == 0 || bytes.Equal(bytes.TrimSpace(value.Filters), []byte("null")) {
		return info, nil
	}
	var filters []binanceFilter
	if err := json.Unmarshal(value.Filters, &filters); err != nil {
		return market.MarketInfo{}, market.ErrInvalidSourceData
	}
	priceSeen, quantitySeen, notionalSeen := false, false, false
	for _, filter := range filters {
		filterType, ok := requiredSourceString(filter.Type)
		if !ok {
			return market.MarketInfo{}, market.ErrInvalidSourceData
		}
		switch filterType {
		case "PRICE_FILTER":
			minPrice, minOK := requiredSourceDecimal(filter.MinPrice)
			maxPrice, maxOK := requiredSourceDecimal(filter.MaxPrice)
			tickSize, tickOK := requiredSourceDecimal(filter.TickSize)
			if priceSeen || !minOK || !maxOK || !tickOK {
				return market.MarketInfo{}, market.ErrInvalidSourceData
			}
			priceSeen = true
			info.PriceFilter = &market.PriceFilter{MinPrice: minPrice, MaxPrice: maxPrice, TickSize: tickSize}
		case "LOT_SIZE":
			minQuantity, minOK := requiredSourceDecimal(filter.MinQuantity)
			maxQuantity, maxOK := requiredSourceDecimal(filter.MaxQuantity)
			stepSize, stepOK := requiredSourceDecimal(filter.StepSize)
			if quantitySeen || !minOK || !maxOK || !stepOK {
				return market.MarketInfo{}, market.ErrInvalidSourceData
			}
			quantitySeen = true
			info.QuantityFilter = &market.QuantityFilter{MinQuantity: minQuantity, MaxQuantity: maxQuantity, StepSize: stepSize}
		case "MIN_NOTIONAL", "NOTIONAL":
			minimum, minimumOK := requiredSourceDecimal(filter.MinNotional)
			if notionalSeen || !minimumOK {
				return market.MarketInfo{}, market.ErrInvalidSourceData
			}
			notionalSeen = true
			info.MinNotional = &minimum
		}
	}
	return info, nil
}

type binanceSymbol struct {
	Symbol               json.RawMessage `json:"symbol"`
	Status               json.RawMessage `json:"status"`
	BaseAsset            json.RawMessage `json:"baseAsset"`
	QuoteAsset           json.RawMessage `json:"quoteAsset"`
	OrderTypes           json.RawMessage `json:"orderTypes"`
	SpotTradingAllowed   json.RawMessage `json:"isSpotTradingAllowed"`
	MarginTradingAllowed json.RawMessage `json:"isMarginTradingAllowed"`
	Filters              json.RawMessage `json:"filters"`
}

type binanceFilter struct {
	Type        json.RawMessage `json:"filterType"`
	MinPrice    json.RawMessage `json:"minPrice"`
	MaxPrice    json.RawMessage `json:"maxPrice"`
	TickSize    json.RawMessage `json:"tickSize"`
	MinQuantity json.RawMessage `json:"minQty"`
	MaxQuantity json.RawMessage `json:"maxQty"`
	StepSize    json.RawMessage `json:"stepSize"`
	MinNotional json.RawMessage `json:"minNotional"`
}

func (b *Binance) getJSON(ctx context.Context, path string, target any) error {
	base := b.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("binance 連線失敗: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMarketReadBody+1))
	if err != nil {
		return fmt.Errorf("binance 回應過大或無法讀取: %w", err)
	}
	if len(body) > maxMarketReadBody {
		return fmt.Errorf("%w: binance 回應超過 %d bytes", market.ErrInvalidSourceData, maxMarketReadBody)
	}
	if response.StatusCode != http.StatusOK {
		return binanceFailure(response.StatusCode, body, request.URL.Query().Get("symbol"))
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: binance 回應無法解析: %v", market.ErrInvalidSourceData, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: binance 回應包含多餘資料", market.ErrInvalidSourceData)
	}
	return nil
}

func rawInt64(raw json.RawMessage) (int64, error) {
	return strconv.ParseInt(string(raw), 10, 64)
}

func rawFloat(raw json.RawMessage) (float64, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(value, 64)
}

func ceilUnixMilliseconds(value time.Time) int64 {
	milliseconds := value.Unix()*1000 + int64(value.Nanosecond())/int64(time.Millisecond)
	if value.Nanosecond()%int(time.Millisecond) != 0 {
		milliseconds++
	}
	return milliseconds
}

func requiredSourceString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func requiredSourceStrings(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, false
		}
	}
	return values, true
}

func requiredSourceBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func requiredSourceDecimal(raw json.RawMessage) (string, bool) {
	value, ok := requiredSourceString(raw)
	if !ok {
		return "", false
	}
	dot := -1
	for index, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character == '.' && dot == -1 && index > 0 && index < len(value)-1:
			dot = index
		default:
			return "", false
		}
	}
	return value, true
}
