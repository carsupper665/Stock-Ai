package common

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	tradingListDefaultLimit = 50
	tradingListMaxLimit     = 200
)

func tradingToolDefinitions() []map[string]any {
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
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"limit": limit}, "additionalProperties": false,
			},
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
			"name": "cancel_order", "description": "Cancel one of the bound Account's open limit orders. Filled or already canceled orders are rejected by the Backend.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"order_id": map[string]any{"type": "string"}},
				"required": []string{"order_id"}, "additionalProperties": false,
			},
		},
		{
			"name": "close_position", "description": "Close part or all of one of the bound Account's open positions at the current Backend price. " +
				"Omit quantity to close the whole position.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"position_id": map[string]any{"type": "string"},
					"quantity":    map[string]any{"type": "number", "exclusiveMinimum": 0},
				}, "required": []string{"position_id"}, "additionalProperties": false,
			},
		},
		{
			"name": "set_position_stops", "description": "Set, change or remove the stop-loss and/or take-profit of one of the bound Account's positions. " +
				"Give at least one level: omitted keeps the current level, 0 removes it, a positive price sets it. " +
				"When it later fires, the resulting close is traced to this Run.",
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

// executeTradingTool maps the trading tools onto the Backend Account API. Mutations carry
// the Server's Session/Run as body "source"; the model never supplies identity fields.
func (r *AgentRuntime) executeTradingTool(ctx context.Context, session Session, runID int64, call gatewayTool) (json.RawMessage, bool) {
	var (
		method = http.MethodGet
		path   string
		query  url.Values
		body   []byte
		err    error
	)
	switch call.Name {
	case "get_account":
		path = "/v1/account"
		if !decodeToolArguments(call.Arguments, &struct{}{}) {
			err = errors.New("get_account accepts no arguments")
		}
	case "get_positions":
		path = "/v1/positions"
		query, err = positionsToolQuery(call.Arguments)
	case "get_orders":
		path = "/v1/orders"
		query, err = ordersToolQuery(call.Arguments)
	case "get_order":
		var orderID string
		orderID, err = singleIDToolArgument(call.Arguments, "order_id")
		path = "/v1/orders/" + url.PathEscape(orderID)
	case "get_trades":
		path = "/v1/trades"
		query, err = tradesToolQuery(call.Arguments)
	case "get_ledger":
		path = "/v1/ledger"
		query, err = ledgerToolQuery(call.Arguments, session.ID)
	case "place_order":
		method, path = http.MethodPost, "/v1/orders"
		body, err = placeOrderToolBody(call.Arguments, session.ID, runID)
	case "cancel_order":
		var orderID string
		orderID, err = singleIDToolArgument(call.Arguments, "order_id")
		method, path = http.MethodPost, "/v1/orders/"+url.PathEscape(orderID)+"/cancel"
		body, _ = json.Marshal(map[string]any{"source": runSource(session.ID, runID)})
	case "close_position":
		var positionID string
		positionID, body, err = closePositionToolBody(call.Arguments, session.ID, runID)
		method, path = http.MethodPost, "/v1/positions/"+url.PathEscape(positionID)+"/close"
	case "set_position_stops":
		var positionID string
		positionID, body, err = setStopsToolBody(call.Arguments, session.ID, runID)
		method, path = http.MethodPatch, "/v1/positions/"+url.PathEscape(positionID)
	default:
		return nil, false
	}
	if err != nil {
		return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
	}
	return r.backendToolRequest(ctx, session, method, path, query, body), true
}

func positionsToolQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Product json.RawMessage `json:"product"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("only product is accepted")
	}
	query := url.Values{}
	if len(rawArguments.Product) != 0 {
		product, err := enumToolString(rawArguments.Product, "product", "spot", "futures")
		if err != nil {
			return nil, err
		}
		query.Set("product", product)
	}
	return query, nil
}

func ordersToolQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Status json.RawMessage `json:"status"`
		Limit  json.RawMessage `json:"limit"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("only status and limit are accepted")
	}
	limit, err := listLimitToolArgument(rawArguments.Limit)
	if err != nil {
		return nil, err
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if len(rawArguments.Status) != 0 {
		status, err := enumToolString(rawArguments.Status, "status", "open", "filled", "canceled", "rejected")
		if err != nil {
			return nil, err
		}
		query.Set("status", status)
	}
	return query, nil
}

func tradesToolQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Limit json.RawMessage `json:"limit"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("only limit is accepted")
	}
	limit, err := listLimitToolArgument(rawArguments.Limit)
	if err != nil {
		return nil, err
	}
	return url.Values{"limit": {strconv.Itoa(limit)}}, nil
}

// ledgerToolQuery scopes a run_id filter to this Session so the model cannot read other
// Sessions' events by guessing run numbers.
func ledgerToolQuery(arguments json.RawMessage, sessionID string) (url.Values, error) {
	var rawArguments struct {
		Limit     json.RawMessage `json:"limit"`
		BeforeSeq json.RawMessage `json:"before_seq"`
		RunID     json.RawMessage `json:"run_id"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("only limit, before_seq, and run_id are accepted")
	}
	limit, err := listLimitToolArgument(rawArguments.Limit)
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

func placeOrderToolBody(arguments json.RawMessage, sessionID string, runID int64) ([]byte, error) {
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
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("only market, symbol, product, side, type, quantity, price, leverage, stop_loss, and take_profit are accepted")
	}
	market, symbol, err := parseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	if market = strings.ToLower(market); market != "crypto" && market != "stock" {
		return nil, errors.New("market must be one of crypto, stock")
	}
	side, err := enumToolString(rawArguments.Side, "side", "buy", "sell")
	if err != nil {
		return nil, err
	}
	product, err := optionalEnumToolString(rawArguments.Product, "product", "futures", "spot", "futures")
	if err != nil {
		return nil, err
	}
	orderType, err := optionalEnumToolString(rawArguments.Type, "type", "market", "market", "limit")
	if err != nil {
		return nil, err
	}
	quantity, err := toolNumber(rawArguments.Quantity, "quantity", true)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"market": market, "symbol": symbol, "product": product, "side": side, "type": orderType,
		"quantity": quantity, "source": runSource(sessionID, runID),
	}
	for name, raw := range map[string]json.RawMessage{
		"price": rawArguments.Price, "leverage": rawArguments.Leverage,
		"stop_loss": rawArguments.StopLoss, "take_profit": rawArguments.TakeProfit,
	} {
		if len(raw) == 0 {
			continue
		}
		value, err := toolNumber(raw, name, name == "price" || name == "leverage")
		if err != nil {
			return nil, err
		}
		body[name] = value
	}
	return json.Marshal(body)
}

func closePositionToolBody(arguments json.RawMessage, sessionID string, runID int64) (string, []byte, error) {
	var rawArguments struct {
		PositionID json.RawMessage `json:"position_id"`
		Quantity   json.RawMessage `json:"quantity"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return "", nil, errors.New("only position_id and quantity are accepted")
	}
	positionID, err := requiredToolString(rawArguments.PositionID, "position_id")
	if err != nil {
		return "", nil, err
	}
	body := map[string]any{"source": runSource(sessionID, runID)}
	if len(rawArguments.Quantity) != 0 {
		quantity, err := toolNumber(rawArguments.Quantity, "quantity", true)
		if err != nil {
			return "", nil, err
		}
		body["quantity"] = quantity
	}
	encoded, err := json.Marshal(body)
	return positionID, encoded, err
}

func setStopsToolBody(arguments json.RawMessage, sessionID string, runID int64) (string, []byte, error) {
	var rawArguments struct {
		PositionID json.RawMessage `json:"position_id"`
		StopLoss   json.RawMessage `json:"stop_loss"`
		TakeProfit json.RawMessage `json:"take_profit"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return "", nil, errors.New("only position_id, stop_loss, and take_profit are accepted")
	}
	positionID, err := requiredToolString(rawArguments.PositionID, "position_id")
	if err != nil {
		return "", nil, err
	}
	if len(rawArguments.StopLoss) == 0 && len(rawArguments.TakeProfit) == 0 {
		return "", nil, errors.New("at least one of stop_loss or take_profit is required (0 removes a level)")
	}
	body := map[string]any{"source": runSource(sessionID, runID)}
	for name, raw := range map[string]json.RawMessage{"stop_loss": rawArguments.StopLoss, "take_profit": rawArguments.TakeProfit} {
		if len(raw) == 0 {
			continue
		}
		level, err := toolNumber(raw, name, false)
		if err != nil {
			return "", nil, err
		}
		body[name] = level
	}
	encoded, err := json.Marshal(body)
	return positionID, encoded, err
}

// runSource is the only identity a mutation carries: the Server's Session/Run, never model input.
func runSource(sessionID string, runID int64) map[string]any {
	return map[string]any{"session_id": sessionID, "run_id": runID}
}

func singleIDToolArgument(arguments json.RawMessage, name string) (string, error) {
	var rawArguments map[string]json.RawMessage
	if !decodeToolArguments(arguments, &rawArguments) {
		return "", fmt.Errorf("%s is required and no other field is accepted", name)
	}
	for key := range rawArguments {
		if key != name {
			return "", fmt.Errorf("%s is required and no other field is accepted", name)
		}
	}
	return requiredToolString(rawArguments[name], name)
}

func listLimitToolArgument(raw json.RawMessage) (int, error) {
	limit := tradingListDefaultLimit
	if len(raw) == 0 {
		return limit, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &limit) != nil || limit < 1 || limit > tradingListMaxLimit {
		return 0, fmt.Errorf("limit must be an integer from 1 through %d", tradingListMaxLimit)
	}
	return limit, nil
}

func enumToolString(raw json.RawMessage, name string, allowed ...string) (string, error) {
	value, err := requiredToolString(raw, name)
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

func optionalEnumToolString(raw json.RawMessage, name, fallback string, allowed ...string) (string, error) {
	if len(raw) == 0 {
		return fallback, nil
	}
	return enumToolString(raw, name, allowed...)
}

// toolNumber validates a JSON number and forwards its exact text so the Backend alone owns
// numeric conversion.
func toolNumber(raw json.RawMessage, name string, positive bool) (json.RawMessage, error) {
	var value float64
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || json.Unmarshal(trimmed, &value) != nil {
		return nil, fmt.Errorf("%s must be a number", name)
	}
	if positive && value <= 0 {
		return nil, fmt.Errorf("%s must be greater than 0", name)
	}
	if value < 0 {
		return nil, fmt.Errorf("%s must not be negative", name)
	}
	return json.RawMessage(trimmed), nil
}

// backendToolRequest performs one authenticated Backend request for a Tool attempt.
// A fully read Backend JSON error is a definite rejection (not_executed) except HTTP 500,
// whose transaction state is not proven; a non-JSON 5xx, a transport failure, or an
// unfinished body on a mutation likewise cannot prove the operation did not happen and
// is unknown. Nothing is retried.
func (r *AgentRuntime) backendToolRequest(ctx context.Context, session Session, method, path string, query url.Values, body []byte) json.RawMessage {
	toolContext, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
	defer cancel()
	token, err := r.fetchAccountToken(toolContext, session.AccountID)
	if err != nil {
		// No token means the trading request was never sent, so even a timeout here is provably not_executed.
		if toolContext.Err() != nil {
			return toolErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "not_executed")
		}
		return toolErrorResult(dependencyReasonCode(err), dependencyReason(err), "not_executed")
	}
	endpoint, err := url.Parse(strings.TrimRight(r.config.BackendURL, "/") + path)
	if err != nil {
		return toolErrorResult("TOOL_REQUEST_INVALID", "unable to create Backend request", "not_executed")
	}
	endpoint.RawQuery = query.Encode()
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(toolContext, method, endpoint.String(), payload)
	if err != nil {
		return toolErrorResult("TOOL_REQUEST_INVALID", "unable to create Backend request", "not_executed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.httpClient.Do(request)
	if err != nil {
		if toolContext.Err() != nil {
			return toolErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "unknown")
		}
		return toolErrorResult("BACKEND_UNAVAILABLE", "Backend request failed", "unknown")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		mutation := method != http.MethodGet
		var failure struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := decodeDependencyJSON(response.Body, &failure); err != nil || failure.Error == "" {
			if toolContext.Err() != nil {
				return toolErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "unknown")
			}
			outcome := "not_executed"
			if mutation && response.StatusCode >= 500 {
				outcome = "unknown"
			}
			return toolErrorResult("BACKEND_ERROR", fmt.Sprintf("Backend returned HTTP %d", response.StatusCode), outcome)
		}
		outcome := "not_executed"
		if mutation && response.StatusCode == http.StatusInternalServerError {
			outcome = "unknown"
		}
		if failure.Message == "" {
			failure.Message = fmt.Sprintf("Backend returned HTTP %d", response.StatusCode)
		}
		return toolErrorResult(failure.Error, failure.Message, outcome)
	}
	var result map[string]json.RawMessage
	if err := decodeDependencyJSON(response.Body, &result); err != nil {
		if toolContext.Err() != nil {
			return toolErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "unknown")
		}
		return toolErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response", "unknown")
	}
	if result == nil {
		return toolErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response", "unknown")
	}
	projected, err := projectBackendToolResult(method, path, result)
	if err != nil {
		return toolErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response", "unknown")
	}
	encoded, err := json.Marshal(map[string]any{"ok": true, "result": projected})
	if err != nil {
		return toolErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response", "unknown")
	}
	return encoded
}

func projectBackendToolResult(method, path string, result map[string]json.RawMessage) (any, error) {
	switch {
	case method == http.MethodGet && path == "/v1/account":
		return selectToolFields(result, "id", "balance", "locked_margin", "available", "unrealized_pnl", "equity"), nil
	case method == http.MethodGet && path == "/v1/positions":
		return projectToolList(result, "positions", "id", "market", "symbol", "product", "side", "quantity", "entry_price", "mark_price", "leverage", "unrealized_pnl", "stop_loss", "take_profit")
	case method == http.MethodGet && path == "/v1/orders":
		return projectToolList(result, "orders", orderToolFields...)
	case method == http.MethodGet && strings.HasPrefix(path, "/v1/orders/"):
		return projectRequiredToolFields(result, []string{"id", "status"}, orderToolFields...)
	case method == http.MethodGet && path == "/v1/trades":
		return projectToolList(result, "trades", "id", "order_id", "market", "symbol", "product", "side", "quantity", "price", "fee", "realized_pnl")
	case method == http.MethodGet && path == "/v1/ledger":
		return projectToolList(result, "entries", "seq", "event", "order_id", "trade_id", "position_id", "trigger", "quantity", "price", "fee", "realized_pnl", "balance_delta", "balance_after", "stop_loss", "take_profit", "created_at")
	case method == http.MethodGet && path == "/v1/market/price":
		return selectToolFields(result, "market", "symbol", "price", "updated_at"), nil
	case method == http.MethodGet && path == "/v1/market/ohlcv":
		return projectOHLCVToolResult(result)
	case method == http.MethodGet && path == "/v1/market/info":
		return projectMarketInfoToolResult(result)
	case method == http.MethodPost && path == "/v1/orders":
		return projectRequiredToolFields(result, []string{"id", "status"}, "id", "status", "filled_quantity", "avg_fill_price", "fee", "realized_pnl", "reject_reason")
	case method == http.MethodPost && strings.HasSuffix(path, "/cancel"):
		return projectRequiredToolFields(result, []string{"id", "status"}, "id", "status")
	case method == http.MethodPost && strings.HasSuffix(path, "/close"):
		return projectRequiredToolFields(result, []string{"id", "status"}, "id", "status", "filled_quantity", "avg_fill_price", "fee", "realized_pnl")
	case method == http.MethodPatch && strings.HasPrefix(path, "/v1/positions/"):
		return projectRequiredToolFields(result, []string{"id"}, "id", "stop_loss", "take_profit")
	default:
		return result, nil
	}
}

var orderToolFields = []string{
	"id", "market", "symbol", "product", "side", "type", "quantity", "price", "leverage",
	"stop_loss", "take_profit", "reduce_only", "status", "reject_reason", "filled_quantity",
	"avg_fill_price", "fee", "realized_pnl", "trigger", "created_at",
}

func selectToolFields(source map[string]json.RawMessage, fields ...string) map[string]json.RawMessage {
	selected := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		if value, ok := source[field]; ok {
			selected[field] = value
		}
	}
	return selected
}

func projectRequiredToolFields(source map[string]json.RawMessage, required []string, fields ...string) (any, error) {
	for _, field := range required {
		var value string
		if raw, ok := source[field]; !ok || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is missing or invalid", field)
		}
	}
	return selectToolFields(source, fields...), nil
}

func projectToolList(result map[string]json.RawMessage, name string, fields ...string) (any, error) {
	var items []map[string]json.RawMessage
	if raw, ok := result[name]; !ok || json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, fmt.Errorf("%s is missing or invalid", name)
	}
	projected := make([]map[string]json.RawMessage, 0, len(items))
	for _, item := range items {
		projected = append(projected, selectToolFields(item, fields...))
	}
	return map[string]any{name: projected}, nil
}

func projectOHLCVToolResult(result map[string]json.RawMessage) (any, error) {
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
	projected := selectToolFields(result, "market", "symbol", "interval", "time_zone")
	projected["columns"] = json.RawMessage(`["open_time_unix_ms","open","high","low","close","volume"]`)
	encodedRows, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	projected["rows"] = encodedRows
	return projected, nil
}

func projectMarketInfoToolResult(result map[string]json.RawMessage) (any, error) {
	var rules map[string]json.RawMessage
	if raw, ok := result["source_rules"]; !ok || json.Unmarshal(raw, &rules) != nil || rules == nil {
		return nil, errors.New("source_rules are missing or invalid")
	}
	projected := selectToolFields(result, "symbol", "source")
	for name, value := range selectToolFields(rules, "status", "base_asset", "quote_asset", "order_types", "price_filter", "quantity_filter", "min_notional") {
		projected[name] = value
	}
	var execution map[string]json.RawMessage
	if raw, ok := result["backend_execution"]; !ok || json.Unmarshal(raw, &execution) != nil || execution == nil {
		return nil, errors.New("backend_execution is missing or invalid")
	}
	if value, ok := execution["source_rules_enforced"]; ok {
		projected["source_rules_enforced"] = value
	}
	return projected, nil
}
