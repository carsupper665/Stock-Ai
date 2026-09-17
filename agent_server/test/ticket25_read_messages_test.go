package test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTicket25GetMessagesSchemaMapsFiltersAndReturnsOnlyPublicFields(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		if call == 1 {
			body := decodeMessageGatewayRequest(t, request)
			tool := messageToolByName(t, body, "get_messages")
			schema := tool["input_schema"].(map[string]any)
			properties := schema["properties"].(map[string]any)
			if schema["additionalProperties"] != false || len(properties) != 3 ||
				properties["page"].(map[string]any)["default"] != float64(1) || properties["sort"].(map[string]any)["default"] != "desc" {
				t.Errorf("get_messages schema = %#v", schema)
			}
			if _, hasLimit := properties["limit"]; hasLimit {
				t.Errorf("get_messages must have a fixed page size: %#v", properties)
			}
			messageToolCall(w, "read-filtered", "get_messages", map[string]any{
				"tag": "risk", "page": 3, "sort": "asc",
			})
			return
		}
		result := messageToolResult(t, request, "read-filtered")
		encoded, _ := json.Marshal(result)
		text := string(encoded)
		for _, wanted := range []string{`"ok":true`, `"user_name":"you"`, `"user_name":"Other Agent"`, `"user_name":"[deleted]"`, `"tags":["risk"]`, `"page_size":10`} {
			if !strings.Contains(text, wanted) {
				t.Errorf("Tool Result %s does not contain %s", text, wanted)
			}
		}
		for _, private := range []string{"author_id", "author_type", "account_token", "private_top_level"} {
			if strings.Contains(text, private) {
				t.Errorf("Tool Result leaked %q: %s", private, text)
			}
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("messages reviewed", "messages reviewed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	fixture.message = func(w http.ResponseWriter, request *http.Request, _ int) {
		if request.Method != http.MethodGet {
			t.Errorf("message method = %s", request.Method)
		}
		query := request.URL.Query()
		want := url.Values{
			"tag": {"risk"}, "page": {"3"}, "sort": {"asc"},
		}
		if query.Encode() != want.Encode() {
			t.Errorf("Backend message query = %q, want %q", query.Encode(), want.Encode())
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{
			"private_top_level": "drop-me",
			"messages": []any{
				map[string]any{"id": "msg_self", "user_name": "you", "content": "mine", "tags": []string{"risk"}, "created_at": "2026-09-14T02:30:00Z", "author_id": messageAccountID, "author_type": "account", "account_token": "secret"},
				map[string]any{"id": "msg_other", "user_name": "Other Agent", "content": "other", "created_at": "2026-09-14T02:40:00Z", "author_id": "acc_other"},
				map[string]any{"id": "msg_deleted", "user_name": "[deleted]", "content": "old", "created_at": "2026-09-14T02:50:00Z"},
			},
		})
	}
	runtime := fixture.open(t)
	_, run := startMessageRun(t, runtime)
	if run.Status != "completed" || run.ToolCalls != 1 || run.ModelCalls != 2 {
		t.Fatalf("Run = %+v", run)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.accountReads != 3 || fixture.messageCalls != 1 || len(fixture.messageAuth) != 1 || fixture.messageAuth[0] != "Bearer "+fixture.accountToken {
		t.Fatalf("account_reads=%d message_calls=%d auth=%v", fixture.accountReads, fixture.messageCalls, fixture.messageAuth)
	}
}

func TestTicket25GetMessagesDefaultsCapAndArgumentErrorsUseOneAttemptEach(t *testing.T) {
	tests := []struct {
		name        string
		arguments   any
		wantError   string
		wantBackend bool
		wantQuery   string
	}{
		{name: "defaults", arguments: map[string]any{}, wantBackend: true, wantQuery: "page=1&sort=desc"},
		{name: "page tag and sort", arguments: map[string]any{"tag": "risk", "page": 2, "sort": "asc"}, wantBackend: true, wantQuery: "page=2&sort=asc&tag=risk"},
		{name: "zero page", arguments: map[string]any{"page": 0}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "noninteger page", arguments: map[string]any{"page": 1.5}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "bad sort", arguments: map[string]any{"sort": "sideways"}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "adjustable limit rejected", arguments: map[string]any{"limit": 1}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "old cursor rejected", arguments: map[string]any{"before": "2026-09-14T12:00:00Z"}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "unsupported author filter", arguments: map[string]any{"author_id": "acc_other"}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "null root", arguments: nil, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "array root", arguments: []any{}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "boolean root", arguments: true, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "number root", arguments: 1, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "string root", arguments: "{} trailing", wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "mixed-case declared field", arguments: map[string]any{"PAGE": 1}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "null tag", arguments: map[string]any{"tag": nil}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "null page", arguments: map[string]any{"page": nil}, wantError: "INVALID_TOOL_ARGUMENTS"},
		{name: "null sort", arguments: map[string]any{"sort": nil}, wantError: "INVALID_TOOL_ARGUMENTS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMessageToolFixture()
			fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
				if call == 1 {
					messageToolCall(w, "read-case", "get_messages", test.arguments)
					return
				}
				result := messageToolResult(t, request, "read-case")
				if test.wantError != "" {
					errorBody, _ := result["error"].(map[string]any)
					if result["ok"] != false || errorBody["code"] != test.wantError || errorBody["outcome"] != "not_executed" {
						t.Errorf("Tool Error = %#v", result)
					}
				}
				messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("case observed", "case observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			fixture.message = func(w http.ResponseWriter, request *http.Request, _ int) {
				if request.URL.RawQuery != test.wantQuery {
					t.Errorf("Backend query = %q, want %q", request.URL.RawQuery, test.wantQuery)
				}
				messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": []any{}})
			}
			runtime := fixture.open(t)
			_, run := startMessageRun(t, runtime)
			fixture.mu.Lock()
			calls, accountReads := fixture.messageCalls, fixture.accountReads
			fixture.mu.Unlock()
			if run.Status != "completed" || run.ToolCalls != 1 || (calls == 1) != test.wantBackend {
				t.Fatalf("Run=%+v Backend calls=%d", run, calls)
			}
			wantAccountReads := 2
			if test.wantBackend {
				wantAccountReads = 3
			}
			if accountReads != wantAccountReads {
				t.Fatalf("account reads=%d, want %d; invalid arguments must fail before identity I/O", accountReads, wantAccountReads)
			}
		})
	}
}

func TestTicket25InvalidRefreshedAccountTokenIsNotRetriedAsAnonymous(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		if call == 1 {
			fixture.mu.Lock()
			fixture.accountToken = "reset-token"
			fixture.mu.Unlock()
			messageToolCall(w, "read-auth", "get_messages", map[string]any{})
			return
		}
		result := messageToolResult(t, request, "read-auth")
		errorBody, _ := result["error"].(map[string]any)
		if result["ok"] != false || errorBody["code"] != "unauthorized" || errorBody["outcome"] != "not_executed" {
			t.Errorf("invalid-token Tool Error = %#v", result)
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("auth failure observed", "auth failure observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	fixture.message = func(w http.ResponseWriter, request *http.Request, _ int) {
		if request.Header.Get("Authorization") != "Bearer reset-token" {
			t.Errorf("refreshed Authorization = %q", request.Header.Get("Authorization"))
		}
		messageFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "invalid account token"})
	}
	runtime := fixture.open(t)
	_, run := startMessageRun(t, runtime)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if run.Status != "completed" || run.ToolCalls != 1 || fixture.accountReads != 3 || fixture.messageCalls != 1 {
		t.Fatalf("Run=%+v account_reads=%d message_calls=%d", run, fixture.accountReads, fixture.messageCalls)
	}
}

func TestTicket25GetMessagesRejectsNullFieldAndMalformedSuccessWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "null success", body: `null`},
		{name: "null messages field", body: `{"messages":null}`},
		{name: "null message field", body: `{"messages":[{"id":null,"user_name":"you","content":"x","created_at":"2026-09-14T04:00:00Z"}]}`},
		{name: "malformed success", body: `{"messages":[`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMessageToolFixture()
			fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
				if call == 1 {
					messageToolCall(w, "read-invalid-success", "get_messages", map[string]any{})
					return
				}
				result := messageToolResult(t, request, "read-invalid-success")
				errorBody, _ := result["error"].(map[string]any)
				if errorBody["code"] != "BACKEND_RESPONSE_INVALID" || errorBody["outcome"] != "unknown" {
					t.Errorf("invalid success Tool Error = %#v", result)
				}
				messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("invalid response observed", "invalid response observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			fixture.message = func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, test.body)
			}
			runtime := fixture.open(t)
			_, run := startMessageRun(t, runtime)
			fixture.mu.Lock()
			calls := fixture.messageCalls
			fixture.mu.Unlock()
			if run.Status != "completed" || run.ToolCalls != 1 || calls != 1 {
				t.Fatalf("Run=%+v Backend calls=%d", run, calls)
			}
		})
	}
}

func TestTicket25GetMessagesPartialSuccessBodyTimeoutIsUnknownAndNotRetried(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.toolTimeout = 40 * time.Millisecond
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		if call == 1 {
			messageToolCall(w, "read-partial-timeout", "get_messages", map[string]any{})
			return
		}
		result := messageToolResult(t, request, "read-partial-timeout")
		errorBody, _ := result["error"].(map[string]any)
		if errorBody["code"] != "TOOL_TIMEOUT" || errorBody["outcome"] != "unknown" {
			t.Errorf("partial success timeout = %#v", result)
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("timeout observed", "timeout observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	fixture.message = func(w http.ResponseWriter, request *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"messages":[{"id":"partial"`)
		w.(http.Flusher).Flush()
		startOnce.Do(func() { close(started) })
		<-request.Context().Done()
		cancelOnce.Do(func() { close(canceled) })
	}
	runtime := fixture.open(t)
	_, run := startMessageRun(t, runtime)
	select {
	case <-started:
	default:
		t.Fatal("Backend partial success body was not started")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Tool timeout did not cancel Backend partial success body")
	}
	fixture.mu.Lock()
	calls := fixture.messageCalls
	fixture.mu.Unlock()
	if run.Status != "completed" || run.ToolCalls != 1 || calls != 1 {
		t.Fatalf("Run=%+v Backend calls=%d", run, calls)
	}
}
