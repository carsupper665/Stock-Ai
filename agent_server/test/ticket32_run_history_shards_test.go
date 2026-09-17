package test

import (
	contextpkg "context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type cancelAfterHistoryRunLookupContext struct {
	contextpkg.Context
	errChecks atomic.Int32
}

func (context *cancelAfterHistoryRunLookupContext) Err() error {
	if context.errChecks.Add(1) >= 2 {
		return contextpkg.Canceled
	}
	return nil
}

func TestTicket32HistoryRequestCancellationAfterRunLookupIsLocal(t *testing.T) {
	fixture := newRuntimeFixture()
	var generation atomic.Int32
	unrelatedEntered := make(chan struct{})
	unrelatedCanceled := make(chan struct{})
	release := make(chan struct{})
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if generation.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusOK, finalResponse("history owner", "history owner", nil, nil))
			return
		}
		close(unrelatedEntered)
		select {
		case <-request.Context().Done():
			close(unrelatedCanceled)
		case <-release:
		}
	}
	runtime, _, _ := fixture.open(t, "")
	owner := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_history_owner", "name": "history owner"})
	startRunForHistoryTest(t, runtime, owner.ID, 1)
	unrelated := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_history_unrelated", "name": "unrelated"})
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+unrelated.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start unrelated Run = %d %s", status, raw)
	}
	<-unrelatedEntered

	request := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+owner.ID+"/runs/1/history", nil)
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	cancelContext := &cancelAfterHistoryRunLookupContext{Context: contextpkg.Background()}
	request = request.WithContext(cancelContext)
	recorder := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestTimeout || !strings.Contains(recorder.Body.String(), "REQUEST_CANCELED") {
		t.Fatalf("canceled History read = %d %s", recorder.Code, recorder.Body.String())
	}
	if cancelContext.errChecks.Load() < 2 {
		t.Fatalf("History cancellation was not observed after the successful Run lookup: Err checks=%d", cancelContext.errChecks.Load())
	}
	if runtime.HealthError() != nil {
		t.Fatalf("History request cancellation latched persistence fault: %v", runtime.HealthError())
	}
	select {
	case <-unrelatedCanceled:
		t.Fatal("History request cancellation canceled an unrelated active Run")
	default:
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken, map[string]any{
		"name": "subsequent mutation", "account_id": "acc_history_after_cancel", "model_name": "fixture-model",
	})
	if status != http.StatusCreated {
		t.Fatalf("mutation after canceled History read = %d %s", status, raw)
	}
	close(release)
}

func TestTicket32HistoryRequestCancellationDuringRunLookupIsLocal(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	ctx, cancel := contextpkg.WithCancel(contextpkg.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+session.ID+"/runs/1/history", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	recorder := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestTimeout || !strings.Contains(recorder.Body.String(), "REQUEST_CANCELED") {
		t.Fatalf("canceled Run lookup = %d %s", recorder.Code, recorder.Body.String())
	}
	if runtime.HealthError() != nil {
		t.Fatalf("canceled Run lookup latched persistence fault: %v", runtime.HealthError())
	}
}

func TestTicket32HistoryRunLookupPersistenceFailureLatches(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open fault DB: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE runs`); err != nil {
		_ = db.Close()
		t.Fatalf("drop Runs table: %v", err)
	}
	_ = db.Close()
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("Run lookup persistence failure = %d %s", status, raw)
	}
	if runtime.HealthError() == nil {
		t.Fatal("durable Run lookup failure did not latch persistence health")
	}
}

func TestTicket32MalformedHistoryIsRejectedByHTTPAndRepairedAtStartup(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	historyPath := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	malformed := `{"session_id":"` + session.ID + `","start_run_id":1,"end_run_id":20,"runs":[{"run_id":1}]}`
	if err := os.WriteFile(historyPath, []byte(malformed), 0o600); err != nil {
		t.Fatalf("write malformed History: %v", err)
	}
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("malformed History response = %d %s", status, raw)
	}
	if err := runtime.Close(); err == nil {
		t.Fatal("Close omitted malformed History persistence fault")
	}
	reopenedFixture := newRuntimeFixture()
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	status, raw = getHistoryShard(t, reopened.Handler(), session.ID, 1)
	if status != http.StatusOK || strings.Contains(string(raw), malformed) {
		t.Fatalf("startup did not repair malformed History = %d %s", status, raw)
	}
}

func TestTicket32NoncanonicalHistoryIndexIsRejectedByHTTP(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	canonicalPath := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	body, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical History: %v", err)
	}
	noncanonicalName := session.ID + "_1-21.json"
	noncanonicalPath := filepath.Join(databasePath+".history", noncanonicalName)
	body = []byte(strings.Replace(string(body), `"end_run_id":20`, `"end_run_id":21`, 1))
	if err := os.WriteFile(noncanonicalPath, body, 0o600); err != nil {
		t.Fatalf("write noncanonical History: %v", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open History DB: %v", err)
	}
	if _, err := db.Exec(`UPDATE run_history_files SET end_run_id = 21, file_path = ? WHERE session_id = ? AND start_run_id = 1`, noncanonicalName, session.ID); err != nil {
		_ = db.Close()
		t.Fatalf("replace History index: %v", err)
	}
	_ = db.Close()
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("noncanonical History response = %d %s", status, raw)
	}
}

func TestTicket32HistoryValidationAllowsUnknownMigratedOptionalFacts(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	path := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read History: %v", err)
	}
	body = []byte(strings.Replace(string(body), `"run_id":1`, `"run_id":1,"migrated_optional":{"source":"legacy"}`, 1))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write migrated History: %v", err)
	}
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusOK || !strings.Contains(string(raw), `"migrated_optional"`) || runtime.HealthError() != nil {
		t.Fatalf("valid migrated History = %d %s health=%v", status, raw, runtime.HealthError())
	}
}

func TestTicket32StopRefreshesOnlyAffectedOlderEventHistoryShard(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, sessionID, release := prepareRun21WithOldEvent(t, databasePath, fixture)
	defer close(release)
	status, before := getHistoryShard(t, runtime.Handler(), sessionID, 1)
	if status != http.StatusOK || strings.Contains(string(before), `"outcome":"cleared_by_stop"`) {
		t.Fatalf("History before stop = %d %s", status, before)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+sessionID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop Run 21 = %d %s", status, raw)
	}
	status, older := getHistoryShard(t, runtime.Handler(), sessionID, 1)
	if status != http.StatusOK || !strings.Contains(string(older), `"outcome":"cleared_by_stop"`) ||
		!strings.Contains(string(older), `"created_run_id":1`) {
		t.Fatalf("refreshed older History = %d %s", status, older)
	}
	status, current := getHistoryShard(t, runtime.Handler(), sessionID, 21)
	if status != http.StatusOK || !strings.Contains(string(current), `"run_id":21`) || !strings.Contains(string(current), `"status":"interrupted"`) {
		t.Fatalf("Run 21 History = %d %s", status, current)
	}
}

func TestTicket32MultiShardHistoryFailureRestoresEarlierFilesInReverse(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, sessionID, release := prepareRun21WithOldEvent(t, databasePath, fixture)
	defer close(release)
	_, previous := getHistoryShard(t, runtime.Handler(), sessionID, 1)
	installRejectingTrigger(t, databasePath, "reject_second_history_range", `
		BEFORE INSERT ON run_history_files WHEN NEW.start_run_id = 21
		BEGIN SELECT RAISE(FAIL, 'reject second History range'); END`)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+sessionID+"/stop", agentAdminToken, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("stop with second-range failure = %d %s", status, raw)
	}
	path := filepath.Join(databasePath+".history", sessionID+"_1-20.json")
	restored, err := os.ReadFile(path)
	if err != nil || string(restored) != string(previous) {
		t.Fatalf("first staged shard was not restored: error=%v\nprevious=%s\nrestored=%s", err, previous, restored)
	}
	if _, err := os.Stat(filepath.Join(databasePath+".history", sessionID+"_21-40.json")); !os.IsNotExist(err) {
		t.Fatalf("failed second shard remained visible: %v", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open rollback DB: %v", err)
	}
	defer db.Close()
	var activeEvents, loggedEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE owner_session_id = ?`, sessionID).Scan(&activeEvents); err != nil {
		t.Fatalf("count active Events: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE session_id = ? AND outcome = 'cleared_by_stop'`, sessionID).Scan(&loggedEvents); err != nil {
		t.Fatalf("count Event logs: %v", err)
	}
	if activeEvents != 1 || loggedEvents != 0 {
		t.Fatalf("failed multi-shard transaction was partial: active=%d logged=%d", activeEvents, loggedEvents)
	}
}

func prepareRun21WithOldEvent(t *testing.T, databasePath string, fixture *runtimeFixture) (*common.AgentRuntime, string, chan struct{}) {
	t.Helper()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	if err := runtime.Close(); err != nil {
		t.Fatalf("close seed Runtime: %v", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open seed DB: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for runID := int64(1); runID <= 20; runID++ {
		if _, err := db.Exec(`INSERT INTO runs
			(session_id, run_id, status, trigger, triggers_json, summary, output, error, progress_json,
			 model_call_count, tool_call_count, start_time, end_time)
			VALUES (?, ?, 'completed', 'session_restart', NULL, 'seed', 'seed', NULL, '[]', 1, 0, ?, ?)`,
			session.ID, runID, now, now); err != nil {
			_ = db.Close()
			t.Fatalf("seed Run %d: %v", runID, err)
		}
	}
	if _, err := db.Exec(`UPDATE sessions SET current_run_id = 20, status = 'stopped' WHERE id = ?`, session.ID); err != nil {
		_ = db.Close()
		t.Fatalf("advance Session: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed DB: %v", err)
	}

	reopenedFixture := newRuntimeFixture()
	entered := make(chan struct{})
	release := make(chan struct{})
	reopenedFixture.generate = func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	db, err = sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open Event seed DB: %v", err)
	}
	eventCreated := time.Now().UTC()
	eventDue := eventCreated.Add(time.Hour)
	eventExpires := eventCreated.Add(2 * time.Hour)
	eventLayout := "2006-01-02T15:04:05.000000000Z07:00"
	if _, err := db.Exec(`INSERT INTO events
		(id, owner_session_id, created_run_id, type, params_json, market, symbol, due_at, state_json, created_at, expires_at)
		VALUES ('ev_history_old', ?, 1, 'timer', ?, NULL, NULL, ?, NULL, ?, ?)`,
		session.ID, `{"at":"`+eventDue.Format(time.RFC3339Nano)+`"}`, eventDue.Format(eventLayout),
		eventCreated.Format(eventLayout), eventExpires.Format(eventLayout)); err != nil {
		_ = db.Close()
		t.Fatalf("seed old Event: %v", err)
	}
	_ = db.Close()
	status, raw := runtimeRequest(t, reopened.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run 21 = %d %s", status, raw)
	}
	<-entered
	return reopened, session.ID, release
}

func TestTicket32StartupRepairsCorruptShardAndMissingIndexFromTerminalRuns(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	historyPath := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	if err := os.WriteFile(historyPath, []byte(`{"partial":`), 0o600); err != nil {
		t.Fatalf("corrupt History file: %v", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open repair fixture DB: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM run_history_files WHERE session_id = ?`, session.ID); err != nil {
		_ = db.Close()
		t.Fatalf("delete History index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close repair fixture DB: %v", err)
	}

	reopenedFixture := newRuntimeFixture()
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	status, raw := getHistoryShard(t, reopened.Handler(), session.ID, 1)
	shard := decodeBody[historyShardView](t, raw)
	if status != http.StatusOK || len(shard.Runs) != 1 || shard.Runs[0].RunID != 1 || shard.Runs[0].Status != "completed" {
		t.Fatalf("repaired History = %d %s", status, raw)
	}
}

func TestTicket32MissingShardReadDoesNotRegenerateAndLatchesPersistenceFault(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	historyPath := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	if err := os.Remove(historyPath); err != nil {
		t.Fatalf("remove History shard: %v", err)
	}
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("missing History response = %d %s", status, raw)
	}
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("History GET regenerated file, stat error = %v", err)
	}
	if runtime.HealthError() == nil {
		t.Fatal("missing durable History did not latch the persistence fault")
	}
}

func TestTicket32HistoryIndexFailureExposesNoHalfShardOrFalseCompletion(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run 1 = %d body=%s", status, raw)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop after Run 1 = %d body=%s", status, raw)
	}
	installRejectingTrigger(t, databasePath, "reject_history_index", `
		BEFORE UPDATE ON run_history_files
		BEGIN SELECT RAISE(FAIL, 'reject History index'); END`)
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run 2 = %d body=%s", status, raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.HealthError() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if runtime.HealthError() == nil {
		t.Fatal("History index failure did not latch persistence fault")
	}
	run := getRunForTest(t, runtime, session.ID, 2)
	if run.Status == "completed" || run.Summary != nil || run.Output != nil {
		t.Fatalf("History index failure exposed false completion = %+v", run)
	}
	historyPath := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	retained, err := os.ReadFile(historyPath)
	if err != nil || !strings.Contains(string(retained), `"run_id":1`) || strings.Contains(string(retained), `"run_id":2`) {
		t.Fatalf("rolled-back History replacement = %s error=%v", retained, err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open fault fixture DB: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_history_index`); err != nil {
		_ = db.Close()
		t.Fatalf("remove rejecting trigger: %v", err)
	}
	_ = db.Close()
	if err := runtime.Close(); err == nil {
		t.Fatal("Close omitted the previously latched persistence fault")
	}

	reopenedFixture := newRuntimeFixture()
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	status, raw = getHistoryShard(t, reopened.Handler(), session.ID, 2)
	if status != http.StatusOK || !strings.Contains(string(raw), "CRASH_RECOVERY") {
		t.Fatalf("recovered interrupted Run History = %d %s", status, raw)
	}
	if reopenedRun := getRunForTest(t, reopened, session.ID, 2); reopenedRun.Status != "interrupted" || reopenedRun.Summary != nil || reopenedRun.Output != nil {
		t.Fatalf("uncommitted Run changed on restart = %+v", reopenedRun)
	}
}

func TestTicket32HistoryWriteFailurePreservesPreviousShardAndNeverCompletesNewRun(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	historyDirectory := databasePath + ".history"
	backupDirectory := historyDirectory + ".backup"
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run 1 = %d", status)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop after Run 1 = %d %s", status, raw)
	}
	if err := os.Rename(historyDirectory, backupDirectory); err != nil {
		t.Fatalf("move History directory: %v", err)
	}
	if err := os.WriteFile(historyDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block History directory: %v", err)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run 2 = %d %s", status, raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.HealthError() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if runtime.HealthError() == nil {
		t.Fatal("History write failure did not latch persistence fault")
	}
	if run := getRunForTest(t, runtime, session.ID, 2); run.Status == "completed" || run.Summary != nil || run.Output != nil {
		t.Fatalf("History write failure exposed false completion = %+v", run)
	}
	if err := os.Remove(historyDirectory); err != nil {
		t.Fatalf("remove blocking History path: %v", err)
	}
	if err := os.Rename(backupDirectory, historyDirectory); err != nil {
		t.Fatalf("restore History directory: %v", err)
	}
	status, raw = getHistoryShard(t, runtime.Handler(), session.ID, 1)
	shard := decodeBody[historyShardView](t, raw)
	if status != http.StatusOK || len(shard.Runs) != 1 || shard.Runs[0].RunID != 1 {
		t.Fatalf("previous shard after write fault = %d %s", status, raw)
	}
}

func TestTicket32RuntimeClosePersistsInterruptedHistoryForRestartAudit(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.generate = func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run = %d %s", status, raw)
	}
	<-entered
	err := runtime.Close()
	close(release)
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopenedFixture := newRuntimeFixture()
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	status, raw = getHistoryShard(t, reopened.Handler(), session.ID, 1)
	shard := decodeBody[historyShardView](t, raw)
	if status != http.StatusOK || len(shard.Runs) != 1 || shard.Runs[0].Status != "interrupted" ||
		shard.Runs[0].Summary != nil || shard.Runs[0].Output != nil || shard.Runs[0].Error == nil {
		t.Fatalf("Close/restart History = %d %s", status, raw)
	}
}

type historyShardView struct {
	SessionID  string           `json:"session_id"`
	StartRunID int64            `json:"start_run_id"`
	EndRunID   int64            `json:"end_run_id"`
	Runs       []historyRunView `json:"runs"`
}

type historyRunView struct {
	RunID      int64           `json:"run_id"`
	Status     string          `json:"status"`
	Summary    *string         `json:"summary"`
	Output     *string         `json:"output"`
	Error      *string         `json:"error"`
	Iterations json.RawMessage `json:"iterations"`
}

func getHistoryShard(t *testing.T, runtime http.Handler, sessionID string, runID int64) (int, []byte) {
	t.Helper()
	return runtimeRequest(t, runtime, http.MethodGet,
		"/api/v1/sessions/"+sessionID+"/runs/"+jsonNumber(runID)+"/history", agentAdminToken, nil)
}

func TestTicket32CompletedRunIsImmediatelyAvailableThroughWholeShardHTTP(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", status, raw)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	status, raw = getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusOK {
		t.Fatalf("history status = %d body = %s", status, raw)
	}
	if contentType := "application/json"; contentType != http.DetectContentType(raw) && len(raw) == 0 {
		t.Fatalf("empty History response")
	}
	shard := decodeBody[historyShardView](t, raw)
	if shard.SessionID != session.ID || shard.StartRunID != 1 || shard.EndRunID != 20 || len(shard.Runs) != 1 {
		t.Fatalf("History shard = %+v", shard)
	}
	run := shard.Runs[0]
	if run.RunID != 1 || run.Status != "completed" || run.Summary == nil || run.Output == nil || len(run.Iterations) == 0 {
		t.Fatalf("History Run = %+v", run)
	}
}

func TestTicket32FixedBoundariesPersistAcrossRestartAndDeletedSessionAudit(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)

	for runID := int64(1); runID <= 41; runID++ {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
			"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
		if status != http.StatusAccepted {
			t.Fatalf("start Run %d status = %d body = %s", runID, status, raw)
		}
		waitForRunStatus(t, runtime, session.ID, runID, "completed")
		if runID != 41 {
			status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost,
				"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
			if status != http.StatusOK {
				t.Fatalf("stop after Run %d status = %d body = %s", runID, status, raw)
			}
		}
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("delete Session status = %d body = %s", status, raw)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopenedFixture := newRuntimeFixture()
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	for _, test := range []struct {
		runID int64
		start int64
		end   int64
		count int
	}{
		{runID: 1, start: 1, end: 20, count: 20},
		{runID: 20, start: 1, end: 20, count: 20},
		{runID: 21, start: 21, end: 40, count: 20},
		{runID: 40, start: 21, end: 40, count: 20},
		{runID: 41, start: 41, end: 60, count: 1},
	} {
		status, raw := getHistoryShard(t, reopened.Handler(), session.ID, test.runID)
		if status != http.StatusOK {
			t.Fatalf("Run %d History status = %d body = %s", test.runID, status, raw)
		}
		shard := decodeBody[historyShardView](t, raw)
		if shard.StartRunID != test.start || shard.EndRunID != test.end || len(shard.Runs) != test.count {
			t.Fatalf("Run %d shard = %+v", test.runID, shard)
		}
	}
}

func TestTicket32FailedAndInterruptedHistoryRetainsFactsWithoutSummaryOrReplay(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		fixture := newRuntimeFixture()
		fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": "visible partial answer", "tool_calls": []any{}, "finish_reason": "length", "usage": nil,
			})
		}
		runtime, _, _ := fixture.open(t, "")
		session := createStoppedSession(t, runtime, nil)
		status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
		if status != http.StatusAccepted {
			t.Fatalf("start status = %d", status)
		}
		waitForRunStatus(t, runtime, session.ID, 1, "failed")
		status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
		shard := decodeBody[historyShardView](t, raw)
		if status != http.StatusOK || len(shard.Runs) != 1 || shard.Runs[0].Summary != nil || shard.Runs[0].Output != nil ||
			shard.Runs[0].Error == nil || !strings.Contains(string(shard.Runs[0].Iterations), "visible partial answer") {
			t.Fatalf("failed History = status %d %+v body=%s", status, shard, raw)
		}
		fixture.mu.Lock()
		calls := fixture.generateCalls
		fixture.mu.Unlock()
		if calls != 4 {
			t.Fatalf("Gateway calls = %d, want bounded finalization repairs", calls)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		fixture := newRuntimeFixture()
		entered := make(chan struct{})
		release := make(chan struct{})
		fixture.generate = func(_ http.ResponseWriter, request *http.Request) {
			close(entered)
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}
		runtime, _, _ := fixture.open(t, "")
		session := createStoppedSession(t, runtime, nil)
		status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
		if status != http.StatusAccepted {
			t.Fatalf("start status = %d", status)
		}
		<-entered
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
		if status != http.StatusOK && status != http.StatusAccepted {
			t.Fatalf("stop status = %d body = %s", status, raw)
		}
		close(release)
		waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
		status, raw = getHistoryShard(t, runtime.Handler(), session.ID, 1)
		shard := decodeBody[historyShardView](t, raw)
		if status != http.StatusOK || shard.Runs[0].Summary != nil || shard.Runs[0].Output != nil || shard.Runs[0].Error == nil ||
			!strings.Contains(*shard.Runs[0].Error, "HARD_STOP") {
			t.Fatalf("interrupted History = status %d %+v", status, shard)
		}
	})
}

func TestTicket32OHLCVHistoryIsFilteredButModelSeesFullResultAndUsage(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	modelSawMiddle := atomic.Bool{}
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if round.Add(1) == 1 {
			writeToolCall(w, "history-ohlcv", "get_ohlcv", map[string]any{
				"symbol": "BTCUSDT", "interval": "1m", "start_time": "2026-09-13T10:00:00Z", "end_time": "2026-09-13T11:00:00Z",
			})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		last, _ := messages[len(messages)-1].(map[string]any)
		content, _ := last["content"].(string)
		modelSawMiddle.Store(strings.Contains(content, `1789293660000`))
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content":    finalizationContent("candles retained selectively", "candles retained selectively", nil, nil),
			"tool_calls": []any{}, "finish_reason": "stop",
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 3, "cached_tokens": nil, "total_tokens": 15},
		})
	}
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"market": "crypto", "symbol": "BTCUSDT", "interval": "1m",
			"candles": []any{
				map[string]any{"open_time": "2026-09-13T10:00:00Z", "open": 1, "high": 1, "low": 1, "close": 1, "volume": 1},
				map[string]any{"open_time": "2026-09-13T10:01:00Z", "open": 2, "high": 2, "low": 2, "close": 2, "volume": 2},
				map[string]any{"open_time": "2026-09-13T10:02:00Z", "open": 3, "high": 3, "low": 3, "close": 3, "volume": 3},
			},
		})
	})
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusOK || !modelSawMiddle.Load() || strings.Contains(string(raw), `1789293660000`) ||
		!strings.Contains(string(raw), `"candle_count":3`) || !strings.Contains(string(raw), `"input_tokens":12`) {
		t.Fatalf("filtered History status=%d model_saw_middle=%v body=%s", status, modelSawMiddle.Load(), raw)
	}
}

func TestTicket32HistoryIncludesExistingEventLogFacts(t *testing.T) {
	fixture := newRuntimeFixture()
	script := newLLMScript(
		respondToolCalls(createEventCall("create-history-event", "timer", timerParams(time.Now().Add(time.Hour)))),
		func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
			event := resultEvent(t, lastToolResult(t, body))
			respondToolCalls(toolCall("delete-history-event", "delete_event", map[string]any{"event_id": event.ID}))(w, nil, nil)
		},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content":    finalizationContent("event lifecycle recorded", "event lifecycle recorded", nil, nil),
				"tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
			})
		},
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusOK || !strings.Contains(string(raw), `"outcome":"deleted_by_agent"`) ||
		!strings.Contains(string(raw), `"tool_name":"create_event"`) {
		t.Fatalf("Event History status=%d body=%s", status, raw)
	}
}
