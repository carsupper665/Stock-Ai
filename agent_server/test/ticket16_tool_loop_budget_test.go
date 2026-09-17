package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTicket16ToolLoopRefreshesAccountTokenAndCorrelatesMarketResult(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []struct {
				Role       string  `json:"role"`
				Content    *string `json:"content"`
				ToolCallID string  `json:"tool_call_id"`
			} `json:"messages"`
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Type                 string                    `json:"type"`
					Properties           map[string]map[string]any `json:"properties"`
					Required             []string                  `json:"required"`
					AdditionalProperties *bool                     `json:"additionalProperties"`
				} `json:"input_schema"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode generate request: %v", err)
		}
		if round.Add(1) == 1 {
			var snapshotSchema *struct {
				Type                 string                    `json:"type"`
				Properties           map[string]map[string]any `json:"properties"`
				Required             []string                  `json:"required"`
				AdditionalProperties *bool                     `json:"additionalProperties"`
			}
			for index := range body.Tools {
				if body.Tools[index].Name == "get_market_snapshot" {
					snapshotSchema = &body.Tools[index].InputSchema
					break
				}
			}
			if snapshotSchema == nil || snapshotSchema.Type != "object" || len(snapshotSchema.Required) != 1 ||
				snapshotSchema.Required[0] != "symbol" || snapshotSchema.Properties["market"]["type"] != "string" ||
				snapshotSchema.Properties["symbol"]["type"] != "string" || snapshotSchema.AdditionalProperties == nil ||
				*snapshotSchema.AdditionalProperties {
				t.Errorf("get_market_snapshot schema = %+v", snapshotSchema)
			}
			fixture.mu.Lock()
			fixture.accountToken = "account-token-after-reset"
			fixture.mu.Unlock()
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": nil, "finish_reason": "tool_call", "usage": nil,
				"tool_calls": []any{map[string]any{
					"id": "call-price", "name": "get_market_snapshot", "arguments": map[string]any{"market": "crypto", "symbol": "BTCUSDT"},
				}},
			})
			return
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role != "tool" || last.ToolCallID != "call-price" || last.Content == nil || !strings.Contains(*last.Content, `"price":60000`) {
			t.Errorf("correlated Tool result = %+v", last)
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("market checked", "market checked", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", status, raw)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ModelCalls != 2 || run.ToolCalls != 1 || run.Summary == nil || *run.Summary != "market checked" {
		t.Fatalf("completed Tool Run = %+v", run)
	}
	fixture.mu.Lock()
	requests := strings.Join(fixture.backendRequests, "\n")
	authorizations := append([]string(nil), fixture.backendAuth...)
	fixture.mu.Unlock()
	if strings.Count(requests, "GET /v1/accounts/"+testAccountID) != 3 || !strings.Contains(requests, "GET /v1/market/price?market=crypto&symbol=BTCUSDT") {
		t.Fatalf("Backend requests do not show create validation plus per-attempt refresh: %s", requests)
	}
	wantedAuthorizations := []string{"Bearer " + backendUserToken, "Bearer " + backendUserToken, "Bearer " + backendUserToken, "Bearer account-token-after-reset"}
	if len(authorizations) != len(wantedAuthorizations) {
		t.Fatalf("Backend authorizations = %v, want %v", authorizations, wantedAuthorizations)
	}
	for index := range wantedAuthorizations {
		if authorizations[index] != wantedAuthorizations[index] {
			t.Fatalf("Backend authorizations = %v, want %v", authorizations, wantedAuthorizations)
		}
	}
}

func TestTicket16UnknownInvalidAndTimedOutAttemptsConsumeBudgetAndReturnErrorsToLLM(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.toolTimeout = 20 * time.Millisecond
	marketStarted := make(chan struct{})
	fixture.market = func(_ http.ResponseWriter, request *http.Request) {
		close(marketStarted)
		<-request.Context().Done()
	}
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := round.Add(1)
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		if current > 1 {
			last := lastMessageWithRole(messages, "tool")
			content, _ := last["content"].(string)
			wanted := []string{"UNKNOWN_TOOL", "INVALID_TOOL_ARGUMENTS", "TOOL_TIMEOUT"}[current-2]
			if last["tool_call_id"] != "call-"+jsonNumber(int64(current-1)) || !strings.Contains(content, wanted) {
				t.Errorf("round %d Tool error = %v", current, last)
			}
		}
		switch current {
		case 1:
			writeToolCall(w, "call-1", "missing_tool", map[string]any{})
		case 2:
			writeToolCall(w, "call-2", "get_market_snapshot", map[string]any{"unexpected": true})
		case 3:
			writeToolCall(w, "call-3", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
		default:
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": finalizationContent("handled errors", "handled errors", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
			})
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4, "max_tool_call": 3})
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	select {
	case <-marketStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout fixture was not called")
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ToolCalls != 3 || run.ModelCalls != 4 {
		t.Fatalf("attempt counters = %+v", run)
	}
}

func TestTicket16NullAndWrongTypedArgumentsAreCorrelatedWithoutBackendIO(t *testing.T) {
	invalidArguments := []map[string]any{
		{"market": nil, "symbol": "BTCUSDT"},
		{"market": 7, "symbol": "BTCUSDT"},
		{"symbol": nil},
		{"symbol": 7},
	}
	fixture := newRuntimeFixture()
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := int(round.Add(1))
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode generate request: %v", err)
		}
		if current > 1 {
			messages := body["messages"].([]any)
			last := lastMessageWithRole(messages, "tool")
			content, _ := last["content"].(string)
			if last["tool_call_id"] != "invalid-"+jsonNumber(int64(current-1)) || !strings.Contains(content, "INVALID_TOOL_ARGUMENTS") {
				t.Errorf("round %d correlated Tool error = %v", current, last)
			}
		}
		if current <= len(invalidArguments) {
			writeToolCall(w, "invalid-"+jsonNumber(int64(current)), "get_market_snapshot", invalidArguments[current-1])
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("invalid arguments handled", "invalid arguments handled", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 5, "max_tool_call": 4})
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", status, raw)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ModelCalls != 5 || run.ToolCalls != 4 {
		t.Fatalf("invalid argument attempt counters = %+v", run)
	}
	fixture.mu.Lock()
	requests := append([]string(nil), fixture.backendRequests...)
	fixture.mu.Unlock()
	if len(requests) != 2 || requests[0] != "GET /v1/accounts/"+testAccountID || requests[1] != "GET /v1/accounts/"+testAccountID {
		t.Fatalf("invalid arguments performed Backend I/O: %v", requests)
	}
}

func TestTicket16BatchAndLoopBudgetsFailWithoutExecutingOrSummarizingBeyondLimit(t *testing.T) {
	for _, test := range []struct {
		name       string
		limits     map[string]any
		calls      []any
		wantTools  int
		wantReason string
	}{
		{name: "multi tool response", limits: map[string]any{"max_tool_call": 1}, calls: []any{
			map[string]any{"id": "first", "name": "get_market_snapshot", "arguments": map[string]any{"symbol": "BTCUSDT"}},
			map[string]any{"id": "second", "name": "get_market_snapshot", "arguments": map[string]any{"symbol": "ETHUSDT"}},
		}, wantTools: 1, wantReason: "max_tool_call reached"},
		{name: "next decision exceeds loop", limits: map[string]any{"max_loop": 1}, calls: []any{
			map[string]any{"id": "first", "name": "get_market_snapshot", "arguments": map[string]any{"symbol": "BTCUSDT"}},
		}, wantTools: 1, wantReason: "max_loop reached"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.generate = func(w http.ResponseWriter, request *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
					Tools    []map[string]any `json:"tools"`
				}
				_ = json.NewDecoder(request.Body).Decode(&body)
				if len(body.Tools) == 0 {
					if !strings.Contains(body.Messages[len(body.Messages)-1]["content"].(string), test.wantReason) {
						t.Errorf("terminal instruction = %+v", body.Messages[len(body.Messages)-1])
					}
					writeFixtureJSON(w, http.StatusOK, finalResponse("budget reached", "budget reached", nil, nil))
					return
				}
				writeFixtureJSON(w, http.StatusOK, map[string]any{
					"content": nil, "tool_calls": test.calls, "finish_reason": "tool_call", "usage": nil,
				})
			}
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, test.limits)
			status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
				"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			if status != http.StatusAccepted {
				t.Fatalf("start status = %d", status)
			}
			run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
			if run.ToolCalls != test.wantTools || run.Summary == nil || run.Error != nil {
				t.Fatalf("budget failure = %+v", run)
			}
			if test.name == "multi tool response" && (!strings.Contains(string(run.Progress), `"type":"tool_denied"`) ||
				!strings.Contains(string(run.Progress), `"code":"MAX_TOOL_CALL_REACHED"`)) {
				t.Fatalf("last-slot denial was not audited: %s", run.Progress)
			}
			fixture.mu.Lock()
			requests := strings.Join(fixture.backendRequests, "\n")
			calls := fixture.generateCalls
			fixture.mu.Unlock()
			if strings.Contains(requests, "ETHUSDT") || calls != 2 {
				t.Fatalf("work exceeded budget: Gateway calls=%d Backend requests=%s", calls, requests)
			}
		})
	}
}

func lastMessageWithRole(messages []any, role string) map[string]any {
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		if message["role"] == role {
			return message
		}
	}
	return nil
}

func writeToolCall(w http.ResponseWriter, id, name string, arguments map[string]any) {
	writeFixtureJSON(w, http.StatusOK, map[string]any{
		"content": nil, "finish_reason": "tool_call", "usage": nil,
		"tool_calls": []any{map[string]any{"id": id, "name": name, "arguments": arguments}},
	})
}
