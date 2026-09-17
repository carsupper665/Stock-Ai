package test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket15ConcurrentStartCreatesOnePersistentFirstRunThroughGatewayHTTP(t *testing.T) {
	fixture := newRuntimeFixture()
	generateStarted := make(chan struct{})
	releaseGenerate := make(chan struct{})
	var startedOnce sync.Once
	var gatewayRequest map[string]any
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&gatewayRequest); err != nil {
			t.Errorf("decode Gateway request: %v", err)
		}
		startedOnce.Do(func() { close(generateStarted) })
		<-releaseGenerate
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("first run complete", "first run complete", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"prompt": "trade carefully"})

	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil); status != http.StatusNotFound {
		t.Fatalf("current before start status = %d, want 404", status)
	}
	const starts = 12
	statuses := make(chan int, starts)
	var callers sync.WaitGroup
	for range starts {
		callers.Add(1)
		go func() {
			defer callers.Done()
			status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
				"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			statuses <- status
		}()
	}
	callers.Wait()
	close(statuses)
	accepted := 0
	for status := range statuses {
		if status == http.StatusAccepted {
			accepted++
		} else if status != http.StatusConflict {
			t.Fatalf("concurrent start status = %d, want 202 or 409", status)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted starts = %d, want 1", accepted)
	}
	select {
	case <-generateStarted:
	case <-time.After(time.Second):
		t.Fatal("start did not reach Gateway")
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil)
	current := decodeBody[common.Run](t, raw)
	if status != http.StatusOK || current.RunID != 1 || current.Status != "running" || current.Trigger != "session_start" {
		t.Fatalf("current Run = %d %s", status, raw)
	}
	close(releaseGenerate)
	completed := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if completed.Summary == nil || *completed.Summary != "first run complete" || completed.ModelCalls != 1 || completed.ToolCalls != 0 {
		t.Fatalf("completed Run = %+v", completed)
	}
	if gatewayRequest["provider"] != "fixture" || gatewayRequest["model"] != "provider-model" {
		t.Fatalf("Gateway mapping = %v", gatewayRequest)
	}
	messages, _ := gatewayRequest["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("Gateway context = %v", gatewayRequest["messages"])
	}
	systemMessage, _ := messages[0].(map[string]any)
	systemContent, _ := systemMessage["content"].(string)
	if systemMessage["role"] != "system" || !strings.HasPrefix(systemContent, "trade carefully\n\n") ||
		!strings.Contains(systemContent, `{"output":"human-readable final decision","summary":"short summary (max 500 Unicode characters)","candidate_memories":[],"expire_memories":[]}`) {
		t.Fatalf("Gateway context = %v", gatewayRequest["messages"])
	}
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil); status != http.StatusConflict {
		t.Fatalf("repeated start status = %d, want 409", status)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs?page=1", agentAdminToken, nil)
	listed := decodeBody[struct {
		Runs []common.Run `json:"runs"`
	}](t, raw)
	if status != http.StatusOK || len(listed.Runs) != 1 || listed.Runs[0].RunID != 1 {
		t.Fatalf("Run list = %d %s", status, raw)
	}
}

func TestTicket15GatewayFailureAndInvalidAnswerFailWithoutSummaryOrExtraCall(t *testing.T) {
	for _, test := range []struct {
		name     string
		generate func(http.ResponseWriter, *http.Request)
	}{
		{name: "Gateway error", generate: func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "offline"}})
		}},
		{name: "invalid answer", generate: func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(w, http.StatusOK, map[string]any{"content": nil, "tool_calls": []any{}, "finish_reason": "stop", "usage": nil})
		}},
		{name: "incomplete answer", generate: func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(w, http.StatusOK, map[string]any{"content": "truncated", "tool_calls": []any{}, "finish_reason": "length", "usage": nil})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.generate = test.generate
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, nil)
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
				"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			if status != http.StatusAccepted {
				t.Fatalf("start status = %d body = %s", status, raw)
			}
			failed := waitForRunStatus(t, runtime, session.ID, 1, "failed")
			if failed.Summary != nil || failed.Error == nil || failed.ModelCalls != 4 {
				t.Fatalf("failed Run = %+v", failed)
			}
			fixture.mu.Lock()
			calls := fixture.generateCalls
			fixture.mu.Unlock()
			if calls != 4 {
				t.Fatalf("Gateway calls = %d, want initial attempt plus three bounded retries", calls)
			}
		})
	}
}

func TestTicket15DifferentSessionsRunIndependently(t *testing.T) {
	fixture := newRuntimeFixture()
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 2 {
			close(bothStarted)
		}
		<-release
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("independent", "independent", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	first := createStoppedSession(t, runtime, nil)
	second := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_second", "name": "second"})
	for _, session := range []common.Session{first, second} {
		if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
			"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil); status != http.StatusAccepted {
			t.Fatalf("start %s status = %d", session.ID, status)
		}
	}
	select {
	case <-bothStarted:
	case <-time.After(time.Second):
		t.Fatal("different Sessions did not enter Gateway independently")
	}
	for _, session := range []common.Session{first, second} {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
			"/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil)
		if status != http.StatusOK || !strings.Contains(string(raw), `"status":"running"`) {
			t.Fatalf("current Run for %s = %d %s", session.ID, status, raw)
		}
	}
	close(release)
	for _, session := range []common.Session{first, second} {
		waitForRunStatus(t, runtime, session.ID, 1, "completed")
	}
}

func TestTicket15CompletedRunPersistsAcrossRuntimeRestart(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil); status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	runtime, _, _ = fixture.open(t, databasePath)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/1", agentAdminToken, nil)
	persisted := decodeBody[common.Run](t, raw)
	if status != http.StatusOK || persisted.Status != "completed" || persisted.Summary == nil {
		t.Fatalf("persisted Run = %d %s", status, raw)
	}
}
