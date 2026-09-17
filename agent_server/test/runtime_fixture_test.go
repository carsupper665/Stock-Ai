package test

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

const (
	agentAdminToken  = "agent-admin-secret"
	backendUserToken = "backend-user-secret"
	gatewayToken     = "gateway-runtime-secret"
	testAccountID    = "acc_fixture"
)

type runtimeFixture struct {
	mu              sync.Mutex
	accountStatus   string
	accountToken    string
	backendStatus   int
	modelsStatus    int
	models          any
	modelsHandler   func(http.ResponseWriter, *http.Request, int)
	generate        func(http.ResponseWriter, *http.Request)
	market          func(http.ResponseWriter, *http.Request)
	toolTimeout     time.Duration
	logger          *log.Logger
	backendRequests []string
	backendAuth     []string
	modelRequests   int
	generateCalls   int
}

func newRuntimeFixture() *runtimeFixture {
	return &runtimeFixture{
		accountStatus: "active",
		accountToken:  "account-token-v1",
		models: []any{map[string]any{
			"model_name": "fixture-model", "provider_id": "fixture", "model": "provider-model",
			"options": map[string]any{}, "levels": map[string]any{"high": map[string]any{}},
			"enabled": true, "available": true,
		}},
	}
}

func (f *runtimeFixture) open(t *testing.T, databasePath string) (*common.AgentRuntime, *httptest.Server, *httptest.Server) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(f.serveBackend))
	gateway := httptest.NewServer(http.HandlerFunc(f.serveGateway))
	t.Cleanup(backend.Close)
	t.Cleanup(gateway.Close)
	if databasePath == "" {
		databasePath = filepath.Join(t.TempDir(), "agent.db")
	}
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: databasePath, AdminToken: agentAdminToken,
		BackendURL: backend.URL, BackendUserToken: backendUserToken,
		GatewayURL: gateway.URL, GatewayRuntimeToken: gatewayToken,
		DefaultPrompt: "default mission", DefaultMaxLoop: 4, DefaultMaxToolCall: 3,
		RequestTimeout: time.Second, ToolTimeout: durationOr(f.toolTimeout, time.Second), StopTimeout: time.Second,
		Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, backend, gateway
}

func (f *runtimeFixture) serveBackend(w http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	f.backendRequests = append(f.backendRequests, request.Method+" "+request.URL.RequestURI())
	f.backendAuth = append(f.backendAuth, request.Header.Get("Authorization"))
	status, token, accountStatus := f.backendStatus, f.accountToken, f.accountStatus
	f.mu.Unlock()
	expectedToken := token
	if strings.HasPrefix(request.URL.Path, "/v1/accounts/") {
		expectedToken = backendUserToken
	}
	if request.Header.Get("Authorization") != "Bearer "+expectedToken {
		writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "bad token"})
		return
	}
	if status != 0 {
		writeFixtureJSON(w, status, map[string]any{"error": "fixture_error", "message": "fixture backend error"})
		return
	}
	switch {
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/accounts/acc_"):
		accountID := strings.TrimPrefix(request.URL.Path, "/v1/accounts/")
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"id": accountID, "user_name": "fixture", "status": accountStatus, "token": token,
			"initial_balance": 100000, "balance": 97500,
		})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/market/price":
		if f.market != nil {
			f.market(w, request)
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"market": request.URL.Query().Get("market"), "symbol": request.URL.Query().Get("symbol"), "price": 60000,
		})
	default:
		writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
	}
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func (f *runtimeFixture) serveGateway(w http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer "+gatewayToken {
		writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "AUTHENTICATION_FAILED"}})
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/v1/models" {
		f.mu.Lock()
		f.modelRequests++
		requestNumber := f.modelRequests
		status, models := f.modelsStatus, f.models
		handler := f.modelsHandler
		f.mu.Unlock()
		if handler != nil {
			handler(w, request, requestNumber)
			return
		}
		if status == 0 {
			status = http.StatusOK
		}
		writeFixtureJSON(w, status, map[string]any{"models": models})
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/generate" {
		f.mu.Lock()
		f.generateCalls++
		generate := f.generate
		f.mu.Unlock()
		if generate != nil {
			generate(w, request)
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content":    finalizationContent("completed fixture run", "completed fixture run", nil, nil),
			"tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
		return
	}
	writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "REQUEST_INVALID"}})
}

func finalizationContent(output, summary string, memories, expirations any) string {
	if memories == nil {
		memories = []any{}
	}
	if expirations == nil {
		expirations = []any{}
	}
	encoded, _ := json.Marshal(map[string]any{
		"output": output, "summary": summary,
		"candidate_memories": memories, "expire_memories": expirations,
	})
	return string(encoded)
}

func finalResponse(output, summary string, candidates, expirations []any) map[string]any {
	if candidates == nil {
		candidates = []any{}
	}
	if expirations == nil {
		expirations = []any{}
	}
	return map[string]any{
		"content":    finalizationContent(output, summary, candidates, expirations),
		"tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
	}
}

func listMemoryDetails(t *testing.T, handler http.Handler, sessionID string, includeExpired bool) []map[string]any {
	t.Helper()
	path := "/api/v1/sessions/" + sessionID + "/memories?page=1"
	if includeExpired {
		path += "&include_expired=true"
	}
	status, raw := runtimeRequest(t, handler, http.MethodGet, path, agentAdminToken, nil)
	var response struct {
		Memories []map[string]any `json:"memories"`
	}
	if err := json.Unmarshal(raw, &response); status != http.StatusOK || err != nil {
		t.Fatalf("list Memories = %d %s error=%v", status, raw, err)
	}
	return response.Memories
}

func getMemoryDetail(t *testing.T, handler http.Handler, sessionID, memoryID string) map[string]any {
	t.Helper()
	status, raw := runtimeRequest(t, handler, http.MethodGet, "/api/v1/sessions/"+sessionID+"/memories/"+memoryID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("get Memory = %d %s", status, raw)
	}
	return decodeMap(t, raw)
}

func decodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode JSON = %s: %v", raw, err)
	}
	return result
}

func stopSessionForMemoryTest(t *testing.T, handler http.Handler, sessionID string) {
	t.Helper()
	status, raw := runtimeRequest(t, handler, http.MethodPost, "/api/v1/sessions/"+sessionID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop Session = %d %s", status, raw)
	}
}

func writeFixtureJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func runtimeRequest(t *testing.T, handler http.Handler, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func decodeBody[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	return value
}

func createStoppedSession(t *testing.T, runtime *common.AgentRuntime, overrides map[string]any) common.Session {
	t.Helper()
	body := map[string]any{"name": "fixture session", "account_id": testAccountID, "model_name": "fixture-model"}
	for key, value := range overrides {
		body[key] = value
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken, body)
	if status != http.StatusCreated {
		t.Fatalf("create Session status = %d body = %s", status, raw)
	}
	return decodeBody[common.Session](t, raw)
}

func waitForRunStatus(t *testing.T, runtime *common.AgentRuntime, sessionID string, runID int64, wanted ...string) common.Run {
	t.Helper()
	deadline := time.After(2 * time.Second)
	allowed := make(map[string]bool, len(wanted))
	for _, status := range wanted {
		allowed[status] = true
	}
	for {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
			"/api/v1/sessions/"+sessionID+"/runs/"+jsonNumber(runID), agentAdminToken, nil)
		if status == http.StatusOK {
			run := decodeBody[common.Run](t, raw)
			if allowed[run.Status] {
				return run
			}
		}
		select {
		case <-deadline:
			t.Fatalf("Run %d did not reach %v; last response = %d %s", runID, wanted, status, raw)
		default:
		}
	}
}

func jsonNumber(value int64) string {
	return strconv.FormatInt(value, 10)
}
