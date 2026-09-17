package test

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket14To18PredispatchPersistenceFailurePerformsNoExternalIO(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_predispatch", `
		BEFORE UPDATE OF progress_json ON runs
		BEGIN
			INSERT INTO fault_markers(name) VALUES ('predispatch');
			SELECT RAISE(FAIL, 'reject predispatch');
		END`)
	startSessionForTest(t, runtime, session.ID)
	waitForFaultMarker(t, databasePath, "predispatch")
	run := getRunForTest(t, runtime, session.ID, 1)
	if run.Status != "failed" || run.Summary != nil || run.Error == nil || !strings.Contains(*run.Error, "PERSISTENCE_ERROR") {
		t.Fatalf("predispatch persistence failure = %+v", run)
	}
	fixture.mu.Lock()
	modelRequests, generateCalls := fixture.modelRequests, fixture.generateCalls
	fixture.mu.Unlock()
	if modelRequests != 1 || generateCalls != 0 {
		t.Fatalf("external I/O after failed reservation: model requests=%d Generate=%d", modelRequests, generateCalls)
	}
}

func TestTicket14To18ToolResultPersistenceFailureStopsBeforeNextDecision(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeToolCall(w, "price", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
	}
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_result", `
		BEFORE UPDATE OF progress_json ON runs
		WHEN NEW.progress_json LIKE '%"ok":true%'
		BEGIN
			INSERT INTO fault_markers(name) VALUES ('result');
			SELECT RAISE(FAIL, 'reject result');
		END`)
	startSessionForTest(t, runtime, session.ID)
	waitForFaultMarker(t, databasePath, "result")
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if run.ToolCalls != 1 || run.Summary != nil || run.Error == nil || !strings.Contains(*run.Error, "PERSISTENCE_ERROR") {
		t.Fatalf("result persistence failure = %+v", run)
	}
	fixture.mu.Lock()
	generateCalls := fixture.generateCalls
	fixture.mu.Unlock()
	if generateCalls != 1 {
		t.Fatalf("Generate calls after result persistence failure = %d, want 1", generateCalls)
	}
}

func TestTicket14To18CompletionPersistenceFailureFallsBackToFailed(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_complete", `
		BEFORE UPDATE OF status ON runs WHEN NEW.status = 'completed'
		BEGIN
			SELECT RAISE(FAIL, 'reject complete');
		END`)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if run.Summary != nil || run.Output != nil || run.Error == nil || !strings.Contains(*run.Error, "PERSISTENCE_ERROR") {
		t.Fatalf("completion persistence fallback = %+v", run)
	}
}

func TestTicket14To18FailedStatePersistenceFailureLatchesUnavailability(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "offline"}})
	}
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	baseURL, shutdown := serveRuntimeForFaultTest(t, runtime)
	defer shutdown()
	session := createStoppedSession(t, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_failed", `
		BEFORE UPDATE OF status ON runs WHEN NEW.status = 'failed'
		BEGIN
			SELECT RAISE(FAIL, 'reject failed');
		END`)
	startSessionForTest(t, runtime, session.ID)
	waitForHTTPStatus(t, baseURL+"/health", "", http.StatusServiceUnavailable)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("admission after persistence fault = %d %s", status, raw)
	}
	run := getRunForTest(t, runtime, session.ID, 1)
	if run.Status != "running" || run.Summary != nil {
		t.Fatalf("unpersisted failed state was presented as success: %+v", run)
	}
}

func TestTicket14To18CloseCancelsWorkAndReturnsInterruptPersistenceError(t *testing.T) {
	fixture := newRuntimeFixture()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fixture.generate = func(_ http.ResponseWriter, request *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitTestSignal(t, started, "Generate before Close")
	installRejectingTrigger(t, databasePath, "reject_interrupt", `
		BEFORE UPDATE OF status ON runs WHEN NEW.status = 'interrupted'
		BEGIN
			INSERT INTO fault_markers(name) VALUES ('interrupt');
			SELECT RAISE(FAIL, 'reject interrupt');
		END`)
	err := runtime.Close()
	close(release)
	if err == nil || !strings.Contains(err.Error(), "interrupt") {
		t.Fatalf("Close() error = %v, want interruption persistence error", err)
	}
	if second := runtime.Close(); second == nil || second.Error() != err.Error() {
		t.Fatalf("second Close() error = %v, want stable %v", second, err)
	}
}

func TestTicket14To18StartInsertAndStopUpdateFailuresRollbackAtomically(t *testing.T) {
	t.Run("start insert", func(t *testing.T) {
		fixture := newRuntimeFixture()
		databasePath := filepath.Join(t.TempDir(), "agent.db")
		runtime, _, _ := fixture.open(t, databasePath)
		session := createStoppedSession(t, runtime, nil)
		installRejectingTrigger(t, databasePath, "reject_run_insert", `
			BEFORE INSERT ON runs
			BEGIN
				SELECT RAISE(FAIL, 'reject run insert');
			END`)
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
			"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
		if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
			t.Fatalf("start insert failure = %d %s", status, raw)
		}
		persisted := getSessionForTest(t, runtime, session.ID)
		if persisted.Status != "stopped" || persisted.CurrentRunID != 0 {
			t.Fatalf("start transaction was partially committed: %+v", persisted)
		}
		status, _ = runtimeRequest(t, runtime.Handler(), http.MethodGet,
			"/api/v1/sessions/"+session.ID+"/runs/1", agentAdminToken, nil)
		if status != http.StatusNotFound {
			t.Fatalf("rolled-back Run status = %d, want 404", status)
		}
	})

	t.Run("stop update", func(t *testing.T) {
		fixture := newRuntimeFixture()
		started := make(chan struct{})
		release := make(chan struct{})
		fixture.generate = func(_ http.ResponseWriter, request *http.Request) {
			close(started)
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}
		databasePath := filepath.Join(t.TempDir(), "agent.db")
		runtime, _, _ := fixture.open(t, databasePath)
		session := createStoppedSession(t, runtime, nil)
		startSessionForTest(t, runtime, session.ID)
		waitTestSignal(t, started, "Generate before stop rollback")
		installRejectingTrigger(t, databasePath, "reject_session_stop", `
			BEFORE UPDATE OF status ON sessions WHEN NEW.status = 'stopped'
			BEGIN
				SELECT RAISE(FAIL, 'reject session stop');
			END`)
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
			"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
		close(release)
		if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
			t.Fatalf("stop update failure = %d %s", status, raw)
		}
		persisted := getSessionForTest(t, runtime, session.ID)
		run := getRunForTest(t, runtime, session.ID, 1)
		if persisted.Status != "running" || run.Status != "running" {
			t.Fatalf("stop transaction was partially committed: Session=%+v Run=%+v", persisted, run)
		}
	})
}

func installRejectingTrigger(t *testing.T, databasePath, name, body string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open fault database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS fault_markers (name TEXT NOT NULL)`); err != nil {
		t.Fatalf("create fault marker table: %v", err)
	}
	if _, err := db.Exec("CREATE TRIGGER " + name + " " + body); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

func waitForFaultMarker(t *testing.T, databasePath, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open marker database: %v", err)
	}
	defer db.Close()
	deadline := time.After(2 * time.Second)
	for {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM fault_markers WHERE name = ?`, name).Scan(&count); err == nil && count > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("fault marker %q was not written", name)
		default:
		}
	}
}

func getRunForTest(t *testing.T, runtime *common.AgentRuntime, sessionID string, runID int64) common.Run {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+sessionID+"/runs/"+jsonNumber(runID), agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("get Run status = %d body = %s", status, raw)
	}
	return decodeBody[common.Run](t, raw)
}

func getSessionForTest(t *testing.T, runtime *common.AgentRuntime, sessionID string) common.Session {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+sessionID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("get Session status = %d body = %s", status, raw)
	}
	return decodeBody[common.Session](t, raw)
}

func serveRuntimeForFaultTest(t *testing.T, runtime *common.AgentRuntime) (string, func()) {
	t.Helper()
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
	return "http://" + listener.Addr().String(), func() {
		_ = server.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Wait(ctx)
	}
}

func waitForHTTPStatus(t *testing.T, endpoint, token string, wanted int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		request, _ := http.NewRequest(http.MethodGet, endpoint, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == wanted {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s did not return %d", endpoint, wanted)
		default:
		}
	}
}
