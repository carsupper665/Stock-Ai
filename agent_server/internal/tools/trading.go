package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	tradingListDefaultLimit = 50
	tradingListMaxLimit     = 200
)

func tradingDefinitions() []map[string]any {
	limit := map[string]any{"type": "integer", "minimum": 1, "maximum": tradingListMaxLimit, "default": tradingListDefaultLimit}
	return []map[string]any{
		{
			"name": "get_account", "description": "Get the bound Account's balance, locked margin, available balance, unrealized PnL and equity.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			"name": "get_positions", "description": "List the bound Account's open positions with mark price, unrealized PnL and stop levels.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"product": map[string]any{"type": "string", "enum": []string{"spot", "futures"}},
				}, "additionalProperties": false,
			},
		},
		{
			"name": "get_orders", "description": "List the bound Account's orders, newest first.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"status": map[string]any{"type": "string", "enum": []string{"open", "filled", "canceled", "rejected"}},
					"limit":  limit,
				}, "additionalProperties": false,
			},
		},
		{
			"name": "get_order", "description": "Get one of the bound Account's orders by id.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"order_id": map[string]any{"type": "string"}},
				"required": []string{"order_id"}, "additionalProperties": false,
			},
		},
		{
			"name": "get_trades", "description": "List the bound Account's fills, newest first.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"limit": limit}, "additionalProperties": false},
		},
		{
			"name": "get_ledger", "description": "List the bound Account's ledger events (fills, placed/rejected/canceled orders, stop changes) newest first. " +
				"run_id restricts to events this Session caused in that Run; before_seq pages further back.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"limit":      limit,
					"before_seq": map[string]any{"type": "integer", "minimum": 1},
					"run_id":     map[string]any{"type": "integer", "minimum": 1},
				}, "additionalProperties": false,
			},
		},
		{
			"name": "place_order", "description": "Place a market or limit order for the bound Account. Market orders fill at the current Backend price; " +
				"limit orders stay open until the matching engine fills them. The Backend validates prices, leverage and balance.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market":      map[string]any{"type": "string", "enum": []string{"crypto", "stock"}, "default": "crypto"},
					"symbol":      map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "Exchange symbol with no separator, uppercase, e.g. BTCUSDT or ETHUSDT. Never BTC/USDT or BTC-USDT."},
					"product":     map[string]any{"type": "string", "enum": []string{"spot", "futures"}, "default": "futures"},
					"side":        map[string]any{"type": "string", "enum": []string{"buy", "sell"}},
					"type":        map[string]any{"type": "string", "enum": []string{"market", "limit"}, "default": "market"},
					"quantity":    map[string]any{"type": "number", "exclusiveMinimum": 0},
					"price":       map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "Required for limit orders."},
					"leverage":    map[string]any{"type": "number", "minimum": 1, "maximum": 100, "default": 1},
					"stop_loss":   map[string]any{"type": "number", "minimum": 0},
					"take_profit": map[string]any{"type": "number", "minimum": 0},
				}, "required": []string{"symbol", "side", "quantity"}, "additionalProperties": false,
			},
		},
		{
			"name": "cancel_order", "description": "Cancel one of the bound Account's open orders by id.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"order_id": map[string]any{"type": "string"}},
				"required": []string{"order_id"}, "additionalProperties": false,
			},
		},
		{
			"name": "close_position", "description": "Close one of the bound Account's open positions by id, fully by default or partially with quantity.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"position_id": map[string]any{"type": "string"},
					"quantity":    map[string]any{"type": "number", "exclusiveMinimum": 0},
				}, "required": []string{"position_id"}, "additionalProperties": false,
			},
		},
		{
			"name": "set_position_stops", "description": "Set or remove the stop-loss and take-profit levels of one of the bound Account's open positions. " +
				"Value 0 removes that level.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"position_id": map[string]any{"type": "string"},
					"stop_loss":   map[string]any{"type": "number", "minimum": 0},
					"take_profit": map[string]any{"type": "number", "minimum": 0},
				}, "required": []string{"position_id"}, "additionalProperties": false,
			},
		},
	}
}

func PositionsQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Product json.RawMessage `json:"product"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("only product is accepted")
	}
	query := url.Values{}
	if len(rawArguments.Product) != 0 {
		product, err := enumString(rawArguments.Product, "product", "spot", "futures")
		if err != nil {
			return nil, err
		}
		query.Set("product", product)
	}
	return query, nil
}

func OrdersQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Status json.RawMessage `json:"status"`
		Limit  json.RawMessage `json:"limit"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("only status and limit are accepted")
	}
	limit, err := listLimitArgument(rawArguments.Limit)
	if err != nil {
		return nil, err
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if len(rawArguments.Status) != 0 {
		status, err := enumString(rawArguments.Status, "status", "open", "filled", "canceled", "rejected")
		if err != nil {
			return nil, err
		}
		query.Set("status", status)
	}
	return query, nil
}

func TradesQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Limit json.RawMessage `json:"limit"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("only limit is accepted")
	}
	limit, err := listLimitArgument(rawArguments.Limit)
	if err != nil {
		return nil, err
	}
	return url.Values{"limit": {strconv.Itoa(limit)}}, nil
}

// LedgerQuery scopes a run_id filter to this Session so the model cannot read other
// Sessions' events by guessing run numbers.
func LedgerQuery(arguments json.RawMessage, sessionID string) (url.Values, error) {
	var rawArguments struct {
		Limit     json.RawMessage `json:"limit"`
		BeforeSeq json.RawMessage `json:"before_seq"`
		RunID     json.RawMessage `json:"run_id"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("only limit, before_seq, and run_id are accepted")
	}
	limit, err := listLimitArgument(rawArguments.Limit)
	if err != nil {
		return nil, err
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	for name, raw := range map[string]json.RawMessage{"before_seq": rawArguments.BeforeSeq, "run_id": rawArguments.RunID} {
		if len(raw) == 0 {
			continue
		}
		var value int64
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || value < 1 {
			return nil, fmt.Errorf("%s must be a positive integer", name)
		}
		query.Set(name, strconv.FormatInt(value, 10))
	}
	if query.Has("run_id") {
		query.Set("session_id", sessionID)
	}
	return query, nil
}

func PlaceOrderBody(arguments json.RawMessage, sessionID string, runID int64) ([]byte, error) {
	var rawArguments struct {
		Market     json.RawMessage `json:"market"`
		Symbol     json.RawMessage `json:"symbol"`
		Product    json.RawMessage `json:"product"`
		Side       json.RawMessage `json:"side"`
		Type       json.RawMessage `json:"type"`
		Quantity   json.RawMessage `json:"quantity"`
		Price      json.RawMessage `json:"price"`
		Leverage   json.RawMessage `json:"leverage"`
		StopLoss   json.RawMessage `json:"stop_loss"`
		TakeProfit json.RawMessage `json:"take_profit"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return nil, errors.New("only market, symbol, product, side, type, quantity, price, leverage, stop_loss, and take_profit are accepted")
	}
	market, symbol, err := ParseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	if market = strings.ToLower(market); market != "crypto" && market != "stock" {
		return nil, errors.New("market must be one of crypto, stock")
	}
	side, err := enumString(rawArguments.Side, "side", "buy", "sell")
	if err != nil {
		return nil, err
	}
	product, err := optionalEnumString(rawArguments.Product, "product", "futures", "spot", "futures")
	if err != nil {
		return nil, err
	}
	orderType, err := optionalEnumString(rawArguments.Type, "type", "market", "market", "limit")
	if err != nil {
		return nil, err
	}
	quantity, err := number(rawArguments.Quantity, "quantity", true)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"market": market, "symbol": symbol, "product": product, "side": side, "type": orderType,
		"quantity": quantity, "source": RunSource(sessionID, runID),
	}
	for name, raw := range map[string]json.RawMessage{
		"price": rawArguments.Price, "leverage": rawArguments.Leverage,
		"stop_loss": rawArguments.StopLoss, "take_profit": rawArguments.TakeProfit,
	} {
		if len(raw) == 0 {
			continue
		}
		value, err := number(raw, name, name == "price" || name == "leverage")
		if err != nil {
			return nil, err
		}
		body[name] = value
	}
	return json.Marshal(body)
}

func ClosePositionBody(arguments json.RawMessage, sessionID string, runID int64) (string, []byte, error) {
	var rawArguments struct {
		PositionID json.RawMessage `json:"position_id"`
		Quantity   json.RawMessage `json:"quantity"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return "", nil, errors.New("only position_id and quantity are accepted")
	}
	positionID, err := RequiredString(rawArguments.PositionID, "position_id")
	if err != nil {
		return "", nil, err
	}
	body := map[string]any{"source": RunSource(sessionID, runID)}
	if len(rawArguments.Quantity) != 0 {
		quantity, err := number(rawArguments.Quantity, "quantity", true)
		if err != nil {
			return "", nil, err
		}
		body["quantity"] = quantity
	}
	encoded, err := json.Marshal(body)
	return positionID, encoded, err
}

func SetStopsBody(arguments json.RawMessage, sessionID string, runID int64) (string, []byte, error) {
	var rawArguments struct {
		PositionID json.RawMessage `json:"position_id"`
		StopLoss   json.RawMessage `json:"stop_loss"`
		TakeProfit json.RawMessage `json:"take_profit"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return "", nil, errors.New("only position_id, stop_loss, and take_profit are accepted")
	}
	positionID, err := RequiredString(rawArguments.PositionID, "position_id")
	if err != nil {
		return "", nil, err
	}
	if len(rawArguments.StopLoss) == 0 && len(rawArguments.TakeProfit) == 0 {
		return "", nil, errors.New("at least one of stop_loss or take_profit is required (0 removes a level)")
	}
	body := map[string]any{"source": RunSource(sessionID, runID)}
	for name, raw := range map[string]json.RawMessage{"stop_loss": rawArguments.StopLoss, "take_profit": rawArguments.TakeProfit} {
		if len(raw) == 0 {
			continue
		}
		level, err := number(raw, name, false)
		if err != nil {
			return "", nil, err
		}
		body[name] = level
	}
	encoded, err := json.Marshal(body)
	return positionID, encoded, err
}

// RunSource is the only identity a mutation carries: the Server's Session/Run, never model input.
func RunSource(sessionID string, runID int64) map[string]any {
	return map[string]any{"session_id": sessionID, "run_id": runID}
}

func SingleIDArgument(arguments json.RawMessage, name string) (string, error) {
	var rawArguments map[string]json.RawMessage
	if !DecodeArguments(arguments, &rawArguments) {
		return "", fmt.Errorf("%s is required and no other field is accepted", name)
	}
	for key := range rawArguments {
		if key != name {
			return "", fmt.Errorf("%s is required and no other field is accepted", name)
		}
	}
	return RequiredString(rawArguments[name], name)
}

func listLimitArgument(raw json.RawMessage) (int, error) {
	limit := tradingListDefaultLimit
	if len(raw) == 0 {
		return limit, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &limit) != nil || limit < 1 || limit > tradingListMaxLimit {
		return 0, fmt.Errorf("limit must be an integer from 1 through %d", tradingListMaxLimit)
	}
	return limit, nil
}

func enumString(raw json.RawMessage, name string, allowed ...string) (string, error) {
	value, err := RequiredString(raw, name)
	if err != nil {
		return "", err
	}
	value = strings.ToLower(value)
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s", name, strings.Join(allowed, ", "))
}

func optionalEnumString(raw json.RawMessage, name, fallback string, allowed ...string) (string, error) {
	if len(raw) == 0 {
		return fallback, nil
	}
	return enumString(raw, name, allowed...)
}

// Number validates a JSON number and forwards its exact text so the Backend alone owns
// numeric conversion.
func number(raw json.RawMessage, name string, positive bool) (json.RawMessage, error) {
	var value float64
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || json.Unmarshal(trimmed, &value) != nil {
		return nil, fmt.Errorf("%s must be a number", name)
	}
	if positive && value <= 0 {
		return nil, fmt.Errorf("%s must be greater than 0", name)
	}
	return json.RawMessage(trimmed), nil
}

func ProjectBackendResult(method, path string, result map[string]json.RawMessage) (any, error) {
	switch {
	case method == http.MethodGet && path == "/v1/account":
		return SelectFields(result, "id", "balance", "locked_margin", "available", "unrealized_pnl", "equity"), nil
	case method == http.MethodGet && path == "/v1/positions":
		return projectList(result, "positions", "id", "market", "symbol", "product", "side", "quantity", "entry_price", "mark_price", "leverage", "unrealized_pnl", "stop_loss", "take_profit")
	case method == http.MethodGet && path == "/v1/orders":
		return projectList(result, "orders", orderToolFields...)
	case method == http.MethodGet && path == "/v1/trades":
		return projectList(result, "trades", "id", "order_id", "symbol", "side", "quantity", "price", "fee", "created_at")
	case method == http.MethodGet && path == "/v1/ledger":
		return projectList(result, "events", "seq", "type", "symbol", "side", "quantity", "price", "fee", "run_id", "created_at")
	case strings.HasPrefix(path, "/v1/orders/") && (method == http.MethodGet || method == http.MethodPost):
		return projectRequiredFields(result, []string{"id", "status"}, orderToolFields...)
	case strings.HasPrefix(path, "/v1/positions/") && (method == http.MethodPost || method == http.MethodDelete):
		return projectRequiredFields(result, []string{"id", "status"}, "id", "symbol", "side", "quantity", "avg_close_price", "realized_pnl", "fee")
	default:
		return nil, fmt.Errorf("no projection for %s %s", method, path)
	}
}

var orderToolFields = []string{
	"id", "symbol", "side", "type", "quantity", "filled_quantity", "price", "avg_fill_price", "fee",
	"status", "reject_reason", "created_at", "updated_at",
}

func projectRequiredFields(source map[string]json.RawMessage, required []string, fields ...string) (any, error) {
	for _, field := range required {
		if len(source[field]) == 0 {
			return nil, fmt.Errorf("%s is missing or invalid", field)
		}
	}
	return SelectFields(source, fields...), nil
}

func projectList(result map[string]json.RawMessage, name string, fields ...string) (any, error) {
	var items []map[string]json.RawMessage
	if raw, ok := result[name]; !ok || json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, fmt.Errorf("%s are missing or invalid", name)
	}
	projected := make([]map[string]json.RawMessage, 0, len(items))
	for _, item := range items {
		projected = append(projected, SelectFields(item, fields...))
	}
	return projected, nil
}
