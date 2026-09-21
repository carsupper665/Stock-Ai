package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"stock-ai/agent-server/internal/tools"
)

func (r *AgentRuntime) executeMessageTool(ctx context.Context, session Session, call gatewayTool) (json.RawMessage, bool) {
	switch call.Name {
	case "get_messages":
		query, err := tools.MessageQuery(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		return r.executeMessageRequest(ctx, session, http.MethodGet, query, nil), true
	case "post_message":
		content, tags, err := tools.MessagePostArguments(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		body, _ := json.Marshal(map[string]any{"content": content, "tags": tags})
		return r.executeMessageRequest(ctx, session, http.MethodPost, nil, body), true
	default:
		return nil, false
	}
}

func (r *AgentRuntime) executeMessageRequest(ctx context.Context, session Session, method string, query url.Values, body []byte) json.RawMessage {
	toolContext, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
	defer cancel()
	token, err := r.fetchAccountToken(toolContext, session.AccountID)
	if err != nil {
		if toolContext.Err() != nil {
			return tools.ErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "not_executed")
		}
		return tools.ErrorResult(dependencyReasonCode(err), dependencyReason(err), "not_executed")
	}
	endpoint, err := url.Parse(strings.TrimRight(r.config.BackendURL, "/") + "/v1/messages")
	if err != nil {
		return tools.ErrorResult("TOOL_REQUEST_INVALID", "unable to create Backend message request", "not_executed")
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(toolContext, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return tools.ErrorResult("TOOL_REQUEST_INVALID", "unable to create Backend message request", "not_executed")
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
		return tools.ErrorResult("BACKEND_UNAVAILABLE", "Backend message request failed", "unknown")
	}
	defer response.Body.Close()
	expectedStatus := http.StatusOK
	if method == http.MethodPost {
		expectedStatus = http.StatusCreated
	}
	if response.StatusCode != expectedStatus {
		return messageBackendError(toolContext, response, method)
	}
	result, err := tools.DecodeMessageSuccess(response.Body, method)
	if err != nil {
		if toolContext.Err() != nil {
			return tools.ErrorResult("TOOL_TIMEOUT", toolContext.Err().Error(), "unknown")
		}
		return tools.ErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid message response", "unknown")
	}
	encoded, err := json.Marshal(map[string]any{"ok": true, "result": result})
	if err != nil {
		return tools.ErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid message response", "unknown")
	}
	return encoded
}

func messageBackendError(ctx context.Context, response *http.Response, method string) json.RawMessage {
	outcome := "not_executed"
	if method == http.MethodPost && response.StatusCode >= 500 {
		outcome = "unknown"
	}
	var failure struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := decodeDependencyJSON(response.Body, &failure); err != nil {
		if ctx.Err() != nil {
			return tools.ErrorResult("TOOL_TIMEOUT", ctx.Err().Error(), "unknown")
		}
		return tools.ErrorResult("BACKEND_ERROR", fmt.Sprintf("Backend returned HTTP %d", response.StatusCode), outcome)
	}
	if failure.Error == "" {
		failure.Error = "BACKEND_ERROR"
	}
	if failure.Message == "" {
		failure.Message = fmt.Sprintf("Backend returned HTTP %d", response.StatusCode)
	}
	return tools.ErrorResult(failure.Error, failure.Message, outcome)
}
