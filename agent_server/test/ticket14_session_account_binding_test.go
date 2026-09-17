package test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket14CreatesPersistsAndListsImmutableAccountBoundSessionWithoutSecrets(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)

	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", "", map[string]any{}); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create status = %d, want 401", status)
	}
	session := createStoppedSession(t, runtime, map[string]any{"model_level": "high"})
	if session.Status != "stopped" || session.CurrentRunID != 0 || session.Prompt != "default mission" || session.MaxLoop != 4 || session.MaxToolCall != 3 {
		t.Fatalf("created Session defaults = %+v", session)
	}
	encoded, _ := json.Marshal(session)
	if strings.Contains(string(encoded), fixture.accountToken) || strings.Contains(string(encoded), backendUserToken) {
		t.Fatalf("Session response leaked a credential: %s", encoded)
	}
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPatch, "/api/v1/sessions/"+session.ID, agentAdminToken, map[string]any{"name": "changed"}); status != http.StatusMethodNotAllowed {
		t.Fatalf("PATCH Session status = %d, want 405", status)
	}

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions?page=1", agentAdminToken, nil)
	listed := decodeBody[struct {
		Sessions []common.Session `json:"sessions"`
		PageSize int              `json:"page_size"`
	}](t, raw)
	if status != http.StatusOK || listed.PageSize != 100 || len(listed.Sessions) != 1 || listed.Sessions[0].ID != session.ID {
		t.Fatalf("list response = %d %s", status, raw)
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	runtime, _, _ = fixture.open(t, databasePath)
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	persisted := decodeBody[common.Session](t, raw)
	if status != http.StatusOK || persisted.AccountID != testAccountID || persisted.ModelLevel == nil || *persisted.ModelLevel != "high" {
		t.Fatalf("persisted Session = %d %s", status, raw)
	}
}

func TestTicket14ExistingServerMountsRuntimeRoutes(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	loop, err := common.NewEventLoop()
	if err != nil {
		t.Fatalf("NewEventLoop() error = %v", err)
	}
	server, err := common.NewServer(loop, nil, nil, nil, time.Second, runtime.Handler())
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := server.Start(listener); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = server.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Wait(ctx)
	})
	body := strings.NewReader(`{"name":"mounted","account_id":"acc_mounted","model_name":"fixture-model"}`)
	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/api/v1/sessions", body)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("mounted Session request error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("mounted Session status = %d, want 201", response.StatusCode)
	}
}

func TestTicket14RejectsAccountModelLevelLimitAndSharingFailures(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*runtimeFixture)
		overrides  map[string]any
		wantStatus int
		wantCode   string
	}{
		{name: "missing account", overrides: map[string]any{"account_id": ""}, wantStatus: 400, wantCode: "INVALID_REQUEST"},
		{name: "unknown account", configure: func(f *runtimeFixture) { f.backendStatus = 404 }, wantStatus: 404, wantCode: "ACCOUNT_NOT_FOUND"},
		{name: "disabled account", configure: func(f *runtimeFixture) { f.accountStatus = "disabled" }, wantStatus: 409, wantCode: "ACCOUNT_DISABLED"},
		{name: "Backend admin auth", configure: func(f *runtimeFixture) { f.backendStatus = 401 }, wantStatus: 502, wantCode: "BACKEND_AUTH_FAILED"},
		{name: "Backend unavailable", configure: func(f *runtimeFixture) { f.backendStatus = 503 }, wantStatus: 502, wantCode: "BACKEND_UNAVAILABLE"},
		{name: "unknown model", overrides: map[string]any{"model_name": "missing"}, wantStatus: 400, wantCode: "MODEL_NOT_FOUND"},
		{name: "unavailable model", configure: func(f *runtimeFixture) {
			f.models = []any{map[string]any{"model_name": "fixture-model", "enabled": true, "available": false, "levels": map[string]any{}}}
		}, wantStatus: 409, wantCode: "MODEL_UNAVAILABLE"},
		{name: "unsupported level", overrides: map[string]any{"model_level": "max"}, wantStatus: 400, wantCode: "INVALID_REQUEST"},
		{name: "zero loop", overrides: map[string]any{"max_loop": 0}, wantStatus: 400, wantCode: "INVALID_REQUEST"},
		{name: "negative tools", overrides: map[string]any{"max_tool_call": -1}, wantStatus: 400, wantCode: "INVALID_REQUEST"},
		{name: "null loop", overrides: map[string]any{"max_loop": nil}, wantStatus: 400, wantCode: "INVALID_REQUEST"},
		{name: "null tools", overrides: map[string]any{"max_tool_call": nil}, wantStatus: 400, wantCode: "INVALID_REQUEST"},
		{name: "null prompt", overrides: map[string]any{"prompt": nil}, wantStatus: 400, wantCode: "INVALID_REQUEST"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			if test.configure != nil {
				test.configure(fixture)
			}
			runtime, _, _ := fixture.open(t, "")
			body := map[string]any{"name": "fixture", "account_id": testAccountID, "model_name": "fixture-model"}
			for key, value := range test.overrides {
				body[key] = value
			}
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken, body)
			var failure struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(raw, &failure)
			if status != test.wantStatus || failure.Error != test.wantCode {
				t.Fatalf("response = %d %s, want %d %s", status, raw, test.wantStatus, test.wantCode)
			}
		})
	}

	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken,
		map[string]any{"name": "second", "account_id": testAccountID, "model_name": "fixture-model"})
	if status != http.StatusConflict || !strings.Contains(string(raw), "ACCOUNT_ALREADY_BOUND") {
		t.Fatalf("shared Account response = %d %s", status, raw)
	}
}
