package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

const (
	messageAgentToken   = "ticket-25-agent-admin"
	messageBackendToken = "ticket-25-backend-user"
	messageGatewayToken = "ticket-25-gateway-runtime"
	messageAccountID    = "acc_message_fixture"
)

type messageToolFixture struct {
	mu sync.Mutex

	accountToken  string
	accountStatus string
	accountReads  int
	messageCalls  int
	messageAuth   []string
	message       func(http.ResponseWriter, *http.Request, int)
	generate      func(http.ResponseWriter, *http.Request, int)
	generateCalls int
	toolTimeout   time.Duration
}

func newMessageToolFixture() *messageToolFixture {
	return &messageToolFixture{accountToken: "ticket-25-account-v1", accountStatus: "active"}
}

func (f *messageToolFixture) open(t *testing.T) *common.AgentRuntime {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(f.serveBackend))
	gateway := httptest.NewServer(http.HandlerFunc(f.serveGateway))
	t.Cleanup(backend.Close)
	t.Cleanup(gateway.Close)
	timeout := f.toolTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: messageAgentToken,
		BackendURL: backend.URL, BackendUserToken: messageBackendToken,
		GatewayURL: gateway.URL, GatewayRuntimeToken: messageGatewayToken,
		DefaultPrompt: "message fixture mission", DefaultMaxLoop: 6, DefaultMaxToolCall: 6,
		RequestTimeout: time.Second, ToolTimeout: timeout, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func (f *messageToolFixture) serveBackend(w http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/accounts/") {
		if request.Header.Get("Authorization") != "Bearer "+messageBackendToken {
			messageFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "bad USER token"})
			return
		}
		f.mu.Lock()
		f.accountReads++
		token, status := f.accountToken, f.accountStatus
		f.mu.Unlock()
		messageFixtureJSON(w, http.StatusOK, map[string]any{
			"id": messageAccountID, "user_name": "Message Agent", "status": status, "token": token,
		})
		return
	}
	if request.URL.Path == "/v1/messages" {
		f.mu.Lock()
		f.messageCalls++
		call := f.messageCalls
		f.messageAuth = append(f.messageAuth, request.Header.Get("Authorization"))
		handler := f.message
		f.mu.Unlock()
		if handler == nil {
			messageFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "message fixture not configured"})
			return
		}
		handler(w, request, call)
		return
	}
	messageFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "fixture route not found"})
}

func (f *messageToolFixture) serveGateway(w http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer "+messageGatewayToken {
		messageFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "AUTHENTICATION_FAILED"}})
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/v1/models" {
		messageFixtureJSON(w, http.StatusOK, map[string]any{"models": []any{map[string]any{
			"model_name": "message-model", "provider_id": "fixture", "model": "provider-model",
			"levels": map[string]any{}, "enabled": true, "available": true,
		}}})
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/generate" {
		f.mu.Lock()
		f.generateCalls++
		call, handler := f.generateCalls, f.generate
		f.mu.Unlock()
		if handler == nil {
			messageFixtureJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "FIXTURE_MISSING"}})
			return
		}
		handler(w, request, call)
		return
	}
	messageFixtureJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "REQUEST_INVALID"}})
}

func messageFixtureJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func messageToolCall(w http.ResponseWriter, id, name string, arguments any) {
	messageFixtureJSON(w, http.StatusOK, map[string]any{
		"content": nil, "finish_reason": "tool_call",
		"tool_calls": []any{map[string]any{"id": id, "name": name, "arguments": arguments}},
	})
}

func decodeMessageGatewayRequest(t *testing.T, request *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode Gateway request: %v", err)
	}
	return body
}

func messageToolResult(t *testing.T, request *http.Request, callID string) map[string]any {
	t.Helper()
	body := decodeMessageGatewayRequest(t, request)
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("Gateway messages = %#v", body["messages"])
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok || last["role"] != "tool" || last["tool_call_id"] != callID {
		t.Fatalf("correlated Tool Result = %#v", last)
	}
	content, ok := last["content"].(string)
	if !ok {
		t.Fatalf("Tool Result content = %#v", last["content"])
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		t.Fatalf("decode Tool Result %q: %v", content, err)
	}
	return result
}

func startMessageRun(t *testing.T, runtime *common.AgentRuntime) (common.Session, common.Run) {
	return startMessageRunForAccount(t, runtime, messageAccountID)
}

func startMessageRunForAccount(t *testing.T, runtime *common.AgentRuntime, accountID string) (common.Session, common.Run) {
	t.Helper()
	status, body := messageRuntimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", map[string]any{
		"name": "message fixture", "account_id": accountID, "model_name": "message-model",
	})
	if status != http.StatusCreated {
		t.Fatalf("create Session status=%d body=%s", status, body)
	}
	var session common.Session
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	status, body = messageRuntimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Session status=%d body=%s", status, body)
	}
	return session, waitForMessageRun(t, runtime, session.ID)
}

func waitForMessageRun(t *testing.T, runtime *common.AgentRuntime, sessionID string) common.Run {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		status, body := messageRuntimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+sessionID+"/runs/1", nil)
		if status == http.StatusOK {
			var run common.Run
			if err := json.Unmarshal(body, &run); err == nil && run.Status != "running" {
				return run
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("message Run did not finish; last response=%d %s", status, body)
		default:
		}
	}
}

func messageRuntimeRequest(t *testing.T, handler http.Handler, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal Agent request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer "+messageAgentToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func messageToolByName(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	tools, ok := body["tools"].([]any)
	if !ok {
		t.Fatalf("Gateway tools = %#v", body["tools"])
	}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["name"] == name {
			return tool
		}
	}
	t.Fatalf("Tool %q is not registered", name)
	return nil
}
