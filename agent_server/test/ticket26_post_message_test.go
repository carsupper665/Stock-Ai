package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTicket26PostMessageUsesBoundIdentityThenReadsItBack(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		switch call {
		case 1:
			body := decodeMessageGatewayRequest(t, request)
			tool := messageToolByName(t, body, "post_message")
			schema := tool["input_schema"].(map[string]any)
			properties := schema["properties"].(map[string]any)
			if schema["additionalProperties"] != false || len(properties) != 2 || properties["content"].(map[string]any)["maxLength"] != float64(500) {
				t.Errorf("post_message schema = %#v", schema)
			}
			messageToolCall(w, "post-one", "post_message", map[string]any{"content": "  多位元留言  ", "tags": []string{"risk", "desk-b"}})
		case 2:
			result := messageToolResult(t, request, "post-one")
			encoded, _ := json.Marshal(result)
			if !strings.Contains(string(encoded), `"id":"msg_posted"`) || !strings.Contains(string(encoded), `"status":"posted"`) ||
				strings.Contains(string(encoded), "author_id") || strings.Contains(string(encoded), "content") {
				t.Errorf("post Tool Result = %s", encoded)
			}
			messageToolCall(w, "read-posted", "get_messages", map[string]any{})
		default:
			result := messageToolResult(t, request, "read-posted")
			encoded, _ := json.Marshal(result)
			if !strings.Contains(string(encoded), `"id":"msg_posted"`) || !strings.Contains(string(encoded), `"content":"多位元留言"`) {
				t.Errorf("read-back Tool Result = %s", encoded)
			}
			messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("message published and verified", "message published and verified", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
		}
	}
	fixture.message = func(w http.ResponseWriter, request *http.Request, call int) {
		switch call {
		case 1:
			if request.Method != http.MethodPost || request.URL.RawQuery != "" {
				t.Errorf("publish request = %s %s", request.Method, request.URL.RequestURI())
			}
			body, err := io.ReadAll(request.Body)
			if err != nil || string(body) != `{"content":"多位元留言","tags":["risk","desk-b"]}` {
				t.Errorf("canonical publish body = %q err=%v", body, err)
			}
			messageFixtureJSON(w, http.StatusCreated, map[string]any{
				"id": "msg_posted", "user_name": "you", "content": "多位元留言", "created_at": "2026-09-14T04:00:00Z",
				"author_id": messageAccountID, "author_type": "account",
			})
		case 2:
			if request.Method != http.MethodGet || request.URL.RawQuery != "page=1&sort=desc" {
				t.Errorf("read-back request = %s %s", request.Method, request.URL.RequestURI())
			}
			messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": []any{map[string]any{
				"id": "msg_posted", "user_name": "you", "content": "多位元留言", "created_at": "2026-09-14T04:00:00Z",
			}}})
		}
	}
	runtime := fixture.open(t)
	session, run := startMessageRun(t, runtime)
	if run.Status != "completed" || run.ToolCalls != 2 || run.ModelCalls != 3 {
		t.Fatalf("Run = %+v", run)
	}
	if run.SessionID != session.ID || run.RunID != 1 {
		t.Fatalf("Run ownership = %+v", run)
	}
	var progress []map[string]any
	if err := json.Unmarshal(run.Progress, &progress); err != nil {
		t.Fatalf("decode Run progress: %v", err)
	}
	var postTrace map[string]any
	for _, entry := range progress {
		if entry["type"] == "tool_call" && entry["tool_name"] == "post_message" {
			postTrace = entry
			break
		}
	}
	traceJSON, _ := json.Marshal(postTrace)
	if postTrace == nil || !strings.Contains(string(traceJSON), `"content":"  多位元留言  "`) ||
		!strings.Contains(string(traceJSON), `"id":"msg_posted"`) {
		t.Fatalf("post_message Run trace = %s", traceJSON)
	}
	for _, secret := range []string{"ticket-25-account-v1", messageBackendToken, messageGatewayToken} {
		if strings.Contains(string(run.Progress), secret) {
			t.Fatalf("Run progress leaked credential %q", secret)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.accountReads != 4 || fixture.messageCalls != 2 {
		t.Fatalf("account_reads=%d message_calls=%d", fixture.accountReads, fixture.messageCalls)
	}
}

func TestTicket26PostMessageValidationMatchesBackendBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		arguments   any
		wantBackend bool
		wantContent string
	}{
		{name: "blank", arguments: map[string]any{"content": " \t\n"}},
		{name: "over Unicode limit", arguments: map[string]any{"content": strings.Repeat("界", 501)}},
		{name: "duplicate tags", arguments: map[string]any{"content": "hi", "tags": []string{"risk", "risk"}}},
		{name: "blank tag", arguments: map[string]any{"content": "hi", "tags": []string{" "}}},
		{name: "forged author", arguments: map[string]any{"content": "hi", "author_id": "acc_other"}},
		{name: "forged token", arguments: map[string]any{"content": "hi", "account_token": "stolen"}},
		{name: "null root", arguments: nil},
		{name: "array root", arguments: []any{}},
		{name: "boolean root", arguments: false},
		{name: "number root", arguments: 1},
		{name: "string root", arguments: `{"content":"hi"} trailing`},
		{name: "mixed-case declared field", arguments: map[string]any{"CONTENT": "hi"}},
		{name: "null content", arguments: map[string]any{"content": nil}},
		{name: "exact Unicode limit after trim", arguments: map[string]any{"content": "  " + strings.Repeat("界", 500) + "  "}, wantBackend: true, wantContent: strings.Repeat("界", 500)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMessageToolFixture()
			fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
				if call == 1 {
					messageToolCall(w, "post-case", "post_message", test.arguments)
					return
				}
				result := messageToolResult(t, request, "post-case")
				if !test.wantBackend {
					errorBody, _ := result["error"].(map[string]any)
					if result["ok"] != false || errorBody["code"] != "INVALID_TOOL_ARGUMENTS" || errorBody["outcome"] != "not_executed" {
						t.Errorf("validation Tool Error = %#v", result)
					}
				}
				messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("validation observed", "validation observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			fixture.message = func(w http.ResponseWriter, request *http.Request, _ int) {
				var body struct {
					Content string `json:"content"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Content != test.wantContent {
					t.Errorf("Backend content length=%d err=%v", len([]rune(body.Content)), err)
				}
				messageFixtureJSON(w, http.StatusCreated, map[string]any{
					"id": "msg_boundary", "user_name": "you", "content": body.Content, "created_at": "2026-09-14T04:00:00Z",
				})
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

func TestTicket26PostMessageRejectsDisabledOrBackendRejectedRefreshedIdentityWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name             string
		disableOnAttempt bool
		wantCode         string
		wantMessageCalls int
	}{
		{name: "disabled bound Account", disableOnAttempt: true, wantCode: "ACCOUNT_DISABLED"},
		{name: "Backend rejects refreshed token", wantCode: "unauthorized", wantMessageCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMessageToolFixture()
			fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
				if call == 1 {
					if test.disableOnAttempt {
						fixture.mu.Lock()
						fixture.accountStatus = "disabled"
						fixture.mu.Unlock()
					}
					messageToolCall(w, "post-identity", "post_message", map[string]any{"content": "must not be retried"})
					return
				}
				result := messageToolResult(t, request, "post-identity")
				errorBody, _ := result["error"].(map[string]any)
				if result["ok"] != false || errorBody["code"] != test.wantCode || errorBody["outcome"] != "not_executed" {
					t.Errorf("identity Tool Error = %#v", result)
				}
				messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("identity failure observed", "identity failure observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			fixture.message = func(w http.ResponseWriter, _ *http.Request, _ int) {
				messageFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "invalid account token"})
			}
			runtime := fixture.open(t)
			_, run := startMessageRun(t, runtime)
			fixture.mu.Lock()
			accountReads, messageCalls := fixture.accountReads, fixture.messageCalls
			fixture.mu.Unlock()
			if run.Status != "completed" || run.ToolCalls != 1 || accountReads != 3 || messageCalls != test.wantMessageCalls {
				t.Fatalf("Run=%+v account_reads=%d message_calls=%d", run, accountReads, messageCalls)
			}
		})
	}
}

func TestTicket26PostMessageRejectsNullFieldAndMalformedSuccessWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "null success", body: `null`},
		{name: "null result field", body: `{"id":"msg_null","user_name":null,"content":"x","created_at":"2026-09-14T04:00:00Z"}`},
		{name: "malformed success", body: `{"id":"msg_partial"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMessageToolFixture()
			fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
				if call == 1 {
					messageToolCall(w, "post-invalid-success", "post_message", map[string]any{"content": "one attempt"})
					return
				}
				result := messageToolResult(t, request, "post-invalid-success")
				errorBody, _ := result["error"].(map[string]any)
				if errorBody["code"] != "BACKEND_RESPONSE_INVALID" || errorBody["outcome"] != "unknown" {
					t.Errorf("invalid success Tool Error = %#v", result)
				}
				messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("invalid response observed", "invalid response observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			fixture.message = func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
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

func TestTicket26PostMessagePartialErrorBodyTimeoutIsUnknownAndNotRetried(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.toolTimeout = 40 * time.Millisecond
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		if call == 1 {
			messageToolCall(w, "post-partial-error", "post_message", map[string]any{"content": "possibly stored"})
			return
		}
		result := messageToolResult(t, request, "post-partial-error")
		errorBody, _ := result["error"].(map[string]any)
		if errorBody["code"] != "TOOL_TIMEOUT" || errorBody["outcome"] != "unknown" {
			t.Errorf("partial error timeout = %#v", result)
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("timeout observed", "timeout observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	fixture.message = func(w http.ResponseWriter, request *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"internal_error"`)
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
		t.Fatal("Backend partial error body was not started")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Tool timeout did not cancel Backend partial error body")
	}
	fixture.mu.Lock()
	calls := fixture.messageCalls
	fixture.mu.Unlock()
	if run.Status != "completed" || run.ToolCalls != 1 || calls != 1 {
		t.Fatalf("Run=%+v Backend calls=%d", run, calls)
	}
}

func TestTicket26PostMessagePreservesExactBackendErrorAndMarksFiveHundredUnknown(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		if call == 1 {
			messageToolCall(w, "post-backend-error", "post_message", map[string]any{"content": "one attempt"})
			return
		}
		result := messageToolResult(t, request, "post-backend-error")
		errorBody, _ := result["error"].(map[string]any)
		if errorBody["code"] != "storage_exact" || errorBody["message"] != "Backend exact failure text" || errorBody["outcome"] != "unknown" {
			t.Errorf("Backend error Tool Result = %#v", result)
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("Backend error observed", "Backend error observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	fixture.message = func(w http.ResponseWriter, _ *http.Request, _ int) {
		messageFixtureJSON(w, http.StatusInternalServerError, map[string]any{"error": "storage_exact", "message": "Backend exact failure text"})
	}
	runtime := fixture.open(t)
	_, run := startMessageRun(t, runtime)
	fixture.mu.Lock()
	calls := fixture.messageCalls
	fixture.mu.Unlock()
	if run.Status != "completed" || run.ToolCalls != 1 || calls != 1 {
		t.Fatalf("Run=%+v Backend calls=%d", run, calls)
	}
}

func TestTicket26PostTimeoutAfterWriteIsUnknownAndNeverAutomaticallyRetried(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.toolTimeout = 40 * time.Millisecond
	var stored atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		switch call {
		case 1:
			messageToolCall(w, "post-timeout", "post_message", map[string]any{"content": "first attempt"})
		case 2:
			result := messageToolResult(t, request, "post-timeout")
			errorBody, _ := result["error"].(map[string]any)
			if errorBody["code"] != "TOOL_TIMEOUT" || errorBody["outcome"] != "unknown" || stored.Load() != 1 {
				t.Errorf("timeout result=%#v stored=%d", result, stored.Load())
			}
			messageToolCall(w, "post-explicit-second", "post_message", map[string]any{"content": "second LLM attempt"})
		default:
			result := messageToolResult(t, request, "post-explicit-second")
			if result["ok"] != true || stored.Load() != 2 {
				t.Errorf("second result=%#v stored=%d", result, stored.Load())
			}
			messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("uncertain write handled", "uncertain write handled", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
		}
	}
	fixture.message = func(w http.ResponseWriter, request *http.Request, call int) {
		stored.Add(1)
		if call == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"msg_uncertain"`))
			w.(http.Flusher).Flush()
			<-request.Context().Done()
			return
		}
		messageFixtureJSON(w, http.StatusCreated, map[string]any{
			"id": "msg_second", "user_name": "you", "content": "second LLM attempt", "created_at": "2026-09-14T04:00:01Z",
		})
	}
	runtime := fixture.open(t)
	_, run := startMessageRun(t, runtime)
	if run.Status != "completed" || run.ToolCalls != 2 || run.ModelCalls != 3 || stored.Load() != 2 {
		t.Fatalf("Run=%+v stored=%d", run, stored.Load())
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.messageCalls != 2 {
		t.Fatalf("Backend calls=%d, want only the two LLM attempts", fixture.messageCalls)
	}
}
