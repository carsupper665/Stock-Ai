package test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	common "stock-ai/agent-server"
)

func TestTicket18RestartCreatesNewRunWithInterruptedFactsAndDoesNotReplayWork(t *testing.T) {
	fixture := newRuntimeFixture()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var round atomic.Int32
	var restartRequest map[string]any
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if round.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&restartRequest); err != nil {
			t.Errorf("decode restart Gateway request: %v", err)
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("restart complete", "restart complete", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil); status != http.StatusAccepted {
		t.Fatalf("initial start status = %d", status)
	}
	waitSignal(t, firstStarted, "initial model request")
	if status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil); status != http.StatusOK {
		t.Fatalf("stop status = %d body = %s", status, raw)
	}
	close(releaseFirst)

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	started := decodeBody[common.Run](t, raw)
	if status != http.StatusAccepted || started.RunID != 2 || started.Trigger != "session_restart" {
		t.Fatalf("restart response = %d %s", status, raw)
	}
	restarted := waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if restarted.Summary == nil || *restarted.Summary != "restart complete" {
		t.Fatalf("restarted Run = %+v", restarted)
	}
	messages, _ := restartRequest["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("restart messages = %v", restartRequest["messages"])
	}
	contextText, _ := messages[1].(map[string]any)["content"].(string)
	if !strings.Contains(contextText, `"trigger":"session_restart"`) ||
		!strings.Contains(contextText, `"status":"interrupted"`) ||
		!strings.Contains(contextText, `"previous_run_id":1`) {
		t.Fatalf("restart context omitted interrupted facts: %s", contextText)
	}
	fixture.mu.Lock()
	calls := fixture.generateCalls
	fixture.mu.Unlock()
	if calls != 2 {
		t.Fatalf("Gateway calls = %d, want one interrupted attempt and one restart decision", calls)
	}
	first := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	if first.Summary != nil || first.Output != nil {
		t.Fatalf("interrupted Run was summarized or replayed: %+v", first)
	}
}

func TestTicket18SoftDeletePreservesAuditAndExcludesSessionFromListAcrossRestart(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"status":"deleted"`) {
		t.Fatalf("delete response = %d %s", status, raw)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions", agentAdminToken, nil)
	listed := decodeBody[struct {
		Sessions []common.Session `json:"sessions"`
	}](t, raw)
	if status != http.StatusOK || len(listed.Sessions) != 0 {
		t.Fatalf("deleted Session list = %d %s", status, raw)
	}
	for method, path := range map[string]string{
		http.MethodPost:  "/api/v1/sessions/" + session.ID + "/start",
		http.MethodPatch: "/api/v1/sessions/" + session.ID,
	} {
		status, _ := runtimeRequest(t, runtime.Handler(), method, path, agentAdminToken, map[string]any{})
		if status != http.StatusConflict && status != http.StatusMethodNotAllowed {
			t.Fatalf("%s deleted Session status = %d", method, status)
		}
	}
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/restore", agentAdminToken, nil); status != http.StatusNotFound {
		t.Fatalf("restore route status = %d, want 404", status)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	runtime, _, _ = fixture.open(t, databasePath)
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	persisted := decodeBody[common.Session](t, raw)
	if status != http.StatusOK || persisted.Status != "deleted" || persisted.DeletedAt == nil || persisted.AccountID != testAccountID {
		t.Fatalf("persisted deleted Session = %d %s", status, raw)
	}
}

func TestTicket18DeletingRunningSessionHardStopsRunAndCannotBeUndoneByLateResponse(t *testing.T) {
	fixture := newRuntimeFixture()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("late completion", "late completion", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil); status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitSignal(t, started, "running delete model request")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"status":"deleted"`) {
		t.Fatalf("running delete response = %d %s", status, raw)
	}
	close(release)
	run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	if run.Summary != nil || run.Output != nil {
		t.Fatalf("late response changed deleted Run = %+v", run)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/1", agentAdminToken, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"status":"interrupted"`) {
		t.Fatalf("deleted audit Run = %d %s", status, raw)
	}
}

func TestTicket18ConcurrentRestartAndDeleteAlwaysEndsDeletedWithoutTerminalOverwrite(t *testing.T) {
	fixture := newRuntimeFixture()
	release := make(chan struct{})
	fixture.generate = func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)

	var wait sync.WaitGroup
	startStatus := make(chan int, 1)
	deleteStatus := make(chan int, 1)
	wait.Add(2)
	go func() {
		defer wait.Done()
		status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
			"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
		startStatus <- status
	}()
	go func() {
		defer wait.Done()
		status, _ := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
			"/api/v1/sessions/"+session.ID, agentAdminToken, nil)
		deleteStatus <- status
	}()
	wait.Wait()
	if got := <-deleteStatus; got != http.StatusOK {
		t.Fatalf("concurrent delete status = %d", got)
	}
	if got := <-startStatus; got != http.StatusAccepted && got != http.StatusConflict {
		t.Fatalf("concurrent start status = %d", got)
	}
	close(release)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	deleted := decodeBody[common.Session](t, raw)
	if status != http.StatusOK || deleted.Status != "deleted" {
		t.Fatalf("final Session = %d %s", status, raw)
	}
	if deleted.CurrentRunID == 1 {
		run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
		if run.Status != "interrupted" {
			t.Fatalf("concurrent Run = %+v", run)
		}
	}
}

func TestTicket18DeletedSessionCannotReleaseAccountBinding(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil); status != http.StatusOK {
		t.Fatalf("delete status = %d", status)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken,
		map[string]any{"name": "replacement", "account_id": testAccountID, "model_name": "fixture-model"})
	if status != http.StatusConflict || !strings.Contains(string(raw), "ACCOUNT_ALREADY_BOUND") {
		t.Fatalf("replacement binding = %d %s", status, raw)
	}
}
