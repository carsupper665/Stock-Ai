package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"stock-ai/agent-server/internal/tools"
)

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
		if !tools.DecodeArguments(call.Arguments, &struct{}{}) {
			err = errors.New("get_account accepts no arguments")
		}
	case "get_positions":
		path = "/v1/positions"
		query, err = tools.PositionsQuery(call.Arguments)
	case "get_orders":
		path = "/v1/orders"
		query, err = tools.OrdersQuery(call.Arguments)
	case "get_order":
		var orderID string
		orderID, err = tools.SingleIDArgument(call.Arguments, "order_id")
		path = "/v1/orders/" + url.PathEscape(orderID)
	case "get_trades":
		path = "/v1/trades"
		query, err = tools.TradesQuery(call.Arguments)
	case "get_ledger":
		path = "/v1/ledger"
		query, err = tools.LedgerQuery(call.Arguments, session.ID)
	case "place_order":
		method, path = http.MethodPost, "/v1/orders"
		body, err = tools.PlaceOrderBody(call.Arguments, session.ID, runID)
	case "cancel_order":
		var orderID string
		orderID, err = tools.SingleIDArgument(call.Arguments, "order_id")
		method, path = http.MethodPost, "/v1/orders/"+url.PathEscape(orderID)+"/cancel"
		body, _ = json.Marshal(map[string]any{"source": tools.RunSource(session.ID, runID)})
	case "close_position":
		var positionID string
		positionID, body, err = tools.ClosePositionBody(call.Arguments, session.ID, runID)
		method, path = http.MethodPost, "/v1/positions/"+url.PathEscape(positionID)+"/close"
	case "set_position_stops":
		var positionID string
		positionID, body, err = tools.SetStopsBody(call.Arguments, session.ID, runID)
		method, path = http.MethodPatch, "/v1/positions/"+url.PathEscape(positionID)
	default:
		return nil, false
	}
	if err != nil {
		return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
	}
	return r.backendToolRequest(ctx, session, method, path, query, body), true
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
			return tools.ErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "not_executed")
		}
		return tools.ErrorResult(dependencyReasonCode(err), dependencyReason(err), "not_executed")
	}
	endpoint, err := url.Parse(strings.TrimRight(r.config.BackendURL, "/") + path)
	if err != nil {
		return tools.ErrorResult("TOOL_REQUEST_INVALID", "unable to create Backend request", "not_executed")
	}
	endpoint.RawQuery = query.Encode()
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(toolContext, method, endpoint.String(), payload)
	if err != nil {
		return tools.ErrorResult("TOOL_REQUEST_INVALID", "unable to create Backend request", "not_executed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.httpClient.Do(request)
	if err != nil {
		if toolContext.Err() != nil {
			return tools.ErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "unknown")
		}
		return tools.ErrorResult("BACKEND_UNAVAILABLE", "Backend request failed: "+err.Error(), "unknown")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		mutation := method != http.MethodGet
		var failure struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxRuntimeBody+1))
		if err := decodeDependencyJSON(bytes.NewReader(body), &failure); err != nil || failure.Error == "" {
			if toolContext.Err() != nil {
				return tools.ErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "unknown")
			}
			outcome := "not_executed"
			if mutation && response.StatusCode >= 500 {
				outcome = "unknown"
			}
			return tools.ErrorResult("BACKEND_ERROR", failureText(response.StatusCode, body), outcome)
		}
		outcome := "not_executed"
		if mutation && response.StatusCode == http.StatusInternalServerError {
			outcome = "unknown"
		}
		if failure.Message == "" {
			failure.Message = fmt.Sprintf("Backend returned HTTP %d", response.StatusCode)
		}
		return tools.ErrorResult(failure.Error, failure.Message, outcome)
	}
	var result map[string]json.RawMessage
	if err := decodeDependencyJSON(response.Body, &result); err != nil {
		if toolContext.Err() != nil {
			return tools.ErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "unknown")
		}
		return tools.ErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response: "+err.Error(), "unknown")
	}
	if result == nil {
		return tools.ErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response: null body", "unknown")
	}
	projected, err := tools.ProjectBackendResult(method, path, result)
	if err != nil {
		return tools.ErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response: "+err.Error(), "unknown")
	}
	encoded, err := json.Marshal(map[string]any{"ok": true, "result": projected})
	if err != nil {
		return tools.ErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response", "unknown")
	}
	return encoded
}
