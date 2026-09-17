package test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

// tradingBackend is a local Backend HTTP fixture with one Account Token per Account.
// Requests under /v1/accounts/ require the configured USER credential; every other
// route requires the current token of the Account named by the Session context and
// is answered by handle, which receives the authenticated Account ID.
type tradingBackend struct {
	mu       sync.Mutex
	tokens   map[string]string
	statuses map[string]string
	requests []tradingRequest
	handle   func(w http.ResponseWriter, request *http.Request, accountID string, body []byte)
	server   *httptest.Server
}

type tradingRequest struct {
	AccountID string
	Method    string
	URI       string
	Body      string
}

func newTradingBackend(t *testing.T, handle func(w http.ResponseWriter, request *http.Request, accountID string, body []byte)) *tradingBackend {
	t.Helper()
	backend := &tradingBackend{tokens: map[string]string{}, statuses: map[string]string{}, handle: handle}
	backend.server = httptest.NewServer(http.HandlerFunc(backend.serve))
	t.Cleanup(backend.server.Close)
	return backend
}

func (b *tradingBackend) addAccount(accountID, token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens[accountID] = token
	b.statuses[accountID] = "active"
}

func (b *tradingBackend) setToken(accountID, token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens[accountID] = token
}

func (b *tradingBackend) setStatus(accountID, status string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.statuses[accountID] = status
}

func (b *tradingBackend) serve(w http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	authorization := request.Header.Get("Authorization")
	if strings.HasPrefix(request.URL.Path, "/v1/accounts/") {
		if authorization != "Bearer "+backendUserToken {
			writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "bad USER token"})
			return
		}
		accountID := strings.TrimPrefix(request.URL.Path, "/v1/accounts/")
		b.mu.Lock()
		token, known := b.tokens[accountID]
		status := b.statuses[accountID]
		b.mu.Unlock()
		if !known {
			writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "帳號不存在"})
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"id": accountID, "user_name": accountID, "status": status, "token": token})
		return
	}
	b.mu.Lock()
	accountID := ""
	for id, token := range b.tokens {
		if authorization == "Bearer "+token {
			accountID = id
		}
	}
	if accountID != "" {
		b.requests = append(b.requests, tradingRequest{AccountID: accountID, Method: request.Method, URI: request.URL.RequestURI(), Body: string(body)})
	}
	b.mu.Unlock()
	if accountID == "" {
		writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "token 無效或帳號已停用"})
		return
	}
	b.handle(w, request, accountID, body)
}

func (b *tradingBackend) authenticatedRequests() []tradingRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]tradingRequest(nil), b.requests...)
}

// scriptedGateway drives one Session per Account through a fixed list of Tool Calls,
// one per model decision, then finishes with a plain stop response. Every correlated
// Tool Result the runtime sends back is captured by tool_call_id.
type scriptedGateway struct {
	mu      sync.Mutex
	scripts map[string][]scriptedCall
	rounds  map[string]int
	results map[string]string
	tools   map[string]json.RawMessage
}

type scriptedCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func newScriptedGateway() *scriptedGateway {
	return &scriptedGateway{scripts: map[string][]scriptedCall{}, rounds: map[string]int{}, results: map[string]string{}, tools: map[string]json.RawMessage{}}
}

// script replaces the Account's Tool Call list and restarts it from the first call.
func (g *scriptedGateway) script(accountID string, calls ...scriptedCall) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.scripts[accountID] = calls
	g.rounds[accountID] = 0
}

func (g *scriptedGateway) generate(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Messages []struct {
			Role       string  `json:"role"`
			Content    *string `json:"content"`
			ToolCallID string  `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Messages) < 2 || body.Messages[1].Content == nil {
		writeFixtureJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "REQUEST_INVALID"}})
		return
	}
	var context struct {
		AccountID string `json:"account_id"`
	}
	_ = json.Unmarshal([]byte(*body.Messages[1].Content), &context)
	g.mu.Lock()
	for _, tool := range body.Tools {
		g.tools[tool.Name] = tool.InputSchema
	}
	for _, message := range body.Messages {
		if message.Role == "tool" && message.Content != nil {
			g.results[message.ToolCallID] = *message.Content
		}
	}
	round := g.rounds[context.AccountID]
	g.rounds[context.AccountID] = round + 1
	calls := g.scripts[context.AccountID]
	g.mu.Unlock()
	if round < len(calls) {
		writeToolCall(w, calls[round].ID, calls[round].Name, calls[round].Arguments)
		return
	}
	writeFixtureJSON(w, http.StatusOK, map[string]any{
		"content": finalizationContent("script finished", "script finished", nil, nil), "tool_calls": []any{}, "finish_reason": "stop",
	})
}

func (g *scriptedGateway) result(t *testing.T, callID string) string {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	result, ok := g.results[callID]
	if !ok {
		t.Fatalf("no correlated Tool Result for %s; captured=%v", callID, g.results)
	}
	return result
}

func (g *scriptedGateway) schema(t *testing.T, toolName string) map[string]any {
	t.Helper()
	g.mu.Lock()
	raw, ok := g.tools[toolName]
	g.mu.Unlock()
	if !ok {
		t.Fatalf("tool %s was not declared to the Gateway", toolName)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode %s schema: %v", toolName, err)
	}
	return schema
}

type toolOK struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Outcome string `json:"outcome"`
	} `json:"error"`
}

func decodeToolResult(t *testing.T, raw string) toolOK {
	t.Helper()
	var result toolOK
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode Tool Result %q: %v", raw, err)
	}
	return result
}

func openTradingRuntime(t *testing.T, backend *tradingBackend, gateway *scriptedGateway, toolTimeout time.Duration) *common.AgentRuntime {
	t.Helper()
	fixture := newRuntimeFixture()
	fixture.generate = gateway.generate
	return openCrossServiceRuntime(t, backend.server.URL, backendUserToken, fixture, toolTimeout)
}

func startSessionRun(t *testing.T, runtime *common.AgentRuntime, session common.Session) {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", status, raw)
	}
}

// mutationErrorCase drives one trading mutation twice (attempt + LLM retry) against a
// Backend fixture and checks the correlated Tool Error, outcome, budget and I/O count.
type mutationErrorCase struct {
	name        string
	arguments   map[string]any
	backend     func(http.ResponseWriter, *http.Request)
	toolTimeout time.Duration
	wantCode    string
	wantMessage string
	wantOutcome string
	wantIO      int
}

// backendFailureCases are the transport/status outcomes shared by every mutation tool.
func backendFailureCases(arguments map[string]any) []mutationErrorCase {
	return []mutationErrorCase{
		{name: "Backend 5xx is unknown", arguments: arguments, wantCode: "internal_error", wantOutcome: "unknown", wantIO: 1,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error", "message": "交易操作失敗"})
			}},
		{name: "connection dropped after send is unknown", arguments: arguments, wantCode: "BACKEND_UNAVAILABLE", wantOutcome: "unknown", wantIO: 1,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				_ = connection.(*net.TCPConn).SetLinger(0)
				_ = connection.Close()
			}},
		{name: "timeout is unknown", arguments: arguments, wantCode: "TOOL_TIMEOUT", wantOutcome: "unknown", wantIO: 1, toolTimeout: 20 * time.Millisecond,
			backend: func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }},
	}
}

func runMutationErrorCases(t *testing.T, toolName string, cases []mutationErrorCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			backend := newTradingBackend(t, func(w http.ResponseWriter, request *http.Request, _ string, _ []byte) {
				calls.Add(1)
				if test.backend == nil {
					t.Errorf("unexpected Backend request %s %s", request.Method, request.URL.RequestURI())
					return
				}
				test.backend(w, request)
			})
			backend.addAccount(testAccountID, "tok-mutation")
			gateway := newScriptedGateway()
			gateway.script(testAccountID,
				scriptedCall{ID: "attempt", Name: toolName, Arguments: test.arguments},
				scriptedCall{ID: "retry", Name: toolName, Arguments: test.arguments},
			)
			runtime := openTradingRuntime(t, backend, gateway, durationOr(test.toolTimeout, time.Second))
			session := createStoppedSession(t, runtime, nil)
			startSessionRun(t, runtime, session)
			run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
			attempt := decodeToolResult(t, gateway.result(t, "attempt"))
			retry := decodeToolResult(t, gateway.result(t, "retry"))
			if run.ToolCalls != 2 || attempt.OK || attempt.Error.Code != test.wantCode || attempt.Error.Outcome != test.wantOutcome ||
				retry.Error.Code != test.wantCode || int(calls.Load()) != 2*test.wantIO {
				t.Fatalf("tool_calls=%d attempt=%s retry=%s backend_calls=%d", run.ToolCalls, gateway.result(t, "attempt"), gateway.result(t, "retry"), calls.Load())
			}
			if test.wantMessage != "" && !strings.Contains(attempt.Error.Message, test.wantMessage) {
				t.Fatalf("Backend message not preserved: %s", gateway.result(t, "attempt"))
			}
		})
	}
}
