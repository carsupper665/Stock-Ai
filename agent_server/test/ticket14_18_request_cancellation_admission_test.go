package test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTicket14To18CanceledStartIsLocalAndDoesNotConsumeRunID(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	baseURL, shutdown := serveRuntimeForFaultTest(t, runtime)
	defer shutdown()
	session := createStoppedSession(t, runtime, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, raw := runtimeRequestWithContext(t, ctx, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", nil)
	if status != http.StatusRequestTimeout || !strings.Contains(string(raw), "REQUEST_CANCELED") {
		t.Fatalf("pre-canceled start = %d %s", status, raw)
	}
	waitForHTTPStatus(t, baseURL+"/health", "", http.StatusOK)
	persisted := getSessionForTest(t, runtime, session.ID)
	if persisted.Status != "stopped" || persisted.CurrentRunID != 0 {
		t.Fatalf("pre-canceled start changed Session = %+v", persisted)
	}
	status, _ = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/1", agentAdminToken, nil)
	if status != http.StatusNotFound {
		t.Fatalf("pre-canceled start created Run 1: status=%d", status)
	}

	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.RunID != 1 {
		t.Fatalf("normal start after cancellation = %+v", run)
	}
}

func TestTicket14To18CanceledStopAndDeleteDoNotFaultOrCancelOtherWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		path   func(string) string
	}{
		{name: "stop", method: http.MethodPost, path: func(id string) string { return "/api/v1/sessions/" + id + "/stop" }},
		{name: "delete", method: http.MethodDelete, path: func(id string) string { return "/api/v1/sessions/" + id }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			generateContext := make(chan context.Context, 1)
			release := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			fixture.generate = func(w http.ResponseWriter, request *http.Request) {
				generateContext <- request.Context()
				select {
				case <-request.Context().Done():
					return
				case <-release:
					writeFixtureJSON(w, http.StatusOK, map[string]any{
						"content": finalizationContent("unrelated completed", "unrelated completed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
					})
				}
			}
			runtime, _, _ := fixture.open(t, "")
			baseURL, shutdown := serveRuntimeForFaultTest(t, runtime)
			defer shutdown()
			activeSession := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_active"})
			targetSession := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_target"})
			startSessionForTest(t, runtime, activeSession.ID)
			var activeContext context.Context
			select {
			case activeContext = <-generateContext:
			case <-time.After(2 * time.Second):
				t.Fatal("unrelated Generate did not start")
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			status, raw := runtimeRequestWithContext(t, ctx, runtime.Handler(), test.method, test.path(targetSession.ID), nil)
			if status != http.StatusRequestTimeout || !strings.Contains(string(raw), "REQUEST_CANCELED") {
				t.Fatalf("pre-canceled %s = %d %s", test.name, status, raw)
			}
			if err := activeContext.Err(); err != nil {
				t.Fatalf("pre-canceled %s canceled unrelated Generate: %v", test.name, err)
			}
			waitForHTTPStatus(t, baseURL+"/health", "", http.StatusOK)
			persisted := getSessionForTest(t, runtime, targetSession.ID)
			if persisted.Status != "stopped" || persisted.DeletedAt != nil {
				t.Fatalf("pre-canceled %s changed target Session = %+v", test.name, persisted)
			}
			close(release)
			released = true
			waitForRunStatus(t, runtime, activeSession.ID, 1, "completed")
		})
	}
}

func TestTicket15CallerCancellationAfterStartCommitDoesNotCancelRun(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelingResponseWriter{
		header:  make(http.Header),
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	done := make(chan struct{})
	go func() {
		runtime.Handler().ServeHTTP(writer, request)
		close(done)
	}()
	waitTestSignal(t, writer.reached, "committed start response")
	cancel()
	close(writer.release)
	waitTestSignal(t, done, "start handler return")
	if writer.status != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", writer.status, writer.body.String())
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.Summary == nil {
		t.Fatalf("post-commit caller cancellation canceled Run = %+v", run)
	}
}

func TestTicket14CreateRechecksPersistenceAfterDependencyWait(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	seed := createStoppedSession(t, runtime, nil)
	dependencyStarted := make(chan struct{})
	releaseDependency := make(chan struct{})
	var once sync.Once
	fixture.modelsHandler = func(w http.ResponseWriter, _ *http.Request, _ int) {
		once.Do(func() { close(dependencyStarted) })
		<-releaseDependency
		writeFixtureJSON(w, http.StatusOK, map[string]any{"models": fixture.models})
	}

	type response struct {
		status int
		body   []byte
	}
	created := make(chan response, 1)
	go func() {
		status, body := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken,
			map[string]any{"name": "late create", "account_id": "acc_late", "model_name": "fixture-model"})
		created <- response{status: status, body: body}
	}()
	waitTestSignal(t, dependencyStarted, "create dependency lookup")
	installRejectingTrigger(t, databasePath, "reject_admission_run", `
		BEFORE INSERT ON runs
		BEGIN
			SELECT RAISE(FAIL, 'reject admission run');
		END`)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+seed.ID+"/start", agentAdminToken, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("fault setup = %d %s", status, raw)
	}
	close(releaseDependency)
	result := <-created
	if result.status != http.StatusServiceUnavailable || !strings.Contains(string(result.body), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("create admitted after persistence fault = %d %s", result.status, result.body)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions", agentAdminToken, nil)
	if status != http.StatusOK || strings.Contains(string(raw), "acc_late") {
		t.Fatalf("late Account binding was persisted: status=%d body=%s", status, raw)
	}
}

func TestTicket15FaultedAdmissionDoesNotAllocateRunOrDispatchGenerate(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_admission_start", `
		BEFORE INSERT ON runs
		BEGIN
			SELECT RAISE(FAIL, 'reject admission start');
		END`)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("faulting start status = %d", status)
	}
	dropTrigger(t, databasePath, "reject_admission_start")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("start after fault = %d %s", status, raw)
	}
	persisted := getSessionForTest(t, runtime, session.ID)
	if persisted.CurrentRunID != 0 || persisted.Status != "stopped" {
		t.Fatalf("faulted admission allocated Run = %+v", persisted)
	}
	fixture.mu.Lock()
	generateCalls := fixture.generateCalls
	fixture.mu.Unlock()
	if generateCalls != 0 {
		t.Fatalf("faulted admission dispatched Generate %d times", generateCalls)
	}
}

func TestTicket14CloseWinsAgainstCreateWaitingOnDependency(t *testing.T) {
	fixture := newRuntimeFixture()
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	dependencyStarted := make(chan struct{})
	releaseDependency := make(chan struct{})
	fixture.modelsHandler = func(w http.ResponseWriter, _ *http.Request, _ int) {
		close(dependencyStarted)
		<-releaseDependency
		writeFixtureJSON(w, http.StatusOK, map[string]any{"models": fixture.models})
	}
	type response struct {
		status int
		body   []byte
	}
	created := make(chan response, 1)
	go func() {
		status, body := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken,
			map[string]any{"name": "closing create", "account_id": "acc_closing", "model_name": "fixture-model"})
		created <- response{status: status, body: body}
	}()
	waitTestSignal(t, dependencyStarted, "create dependency before Close")
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(releaseDependency)
	result := <-created
	if result.status != http.StatusServiceUnavailable {
		t.Fatalf("create after Close = %d %s", result.status, result.body)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE account_id = 'acc_closing'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("create persisted after Close: count=%d error=%v", count, err)
	}
}

func TestTicket15PersistenceFaultCancelsPreadmittedRun(t *testing.T) {
	fixture := newRuntimeFixture()
	generateStarted := make(chan struct{})
	generateCanceled := make(chan struct{})
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(generateStarted)
		<-request.Context().Done()
		close(generateCanceled)
	}
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitTestSignal(t, generateStarted, "preadmitted Generate")
	installRejectingTrigger(t, databasePath, "reject_fault_create", `
		BEFORE INSERT ON sessions
		BEGIN
			SELECT RAISE(FAIL, 'reject fault create');
		END`)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken,
		map[string]any{"name": "fault", "account_id": "acc_fault", "model_name": "fixture-model"})
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("faulting create = %d %s", status, raw)
	}
	waitTestSignal(t, generateCanceled, "preadmitted Generate cancellation")
}

type cancelingResponseWriter struct {
	header  http.Header
	status  int
	body    bytes.Buffer
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *cancelingResponseWriter) Header() http.Header { return w.header }

func (w *cancelingResponseWriter) WriteHeader(status int) {
	w.status = status
	w.once.Do(func() { close(w.reached) })
	<-w.release
}

func (w *cancelingResponseWriter) Write(body []byte) (int, error) { return w.body.Write(body) }

func runtimeRequestWithContext(t *testing.T, ctx context.Context, handler http.Handler, method, path string, body any) (int, []byte) {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func dropTrigger(t *testing.T, databasePath, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("DROP TRIGGER " + name); err != nil {
		t.Fatalf("drop trigger %s: %v", name, err)
	}
}
