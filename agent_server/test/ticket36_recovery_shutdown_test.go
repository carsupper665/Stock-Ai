package test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTicket36RecoversRunningSessionWithoutReplayingWorkOrClearingQueues(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	initialFixture := newRuntimeFixture()
	initial, _, _ := initialFixture.open(t, databasePath)
	session := createStoppedSession(t, initial, nil)
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial Runtime: %v", err)
	}

	db := openTicket36Database(t, databasePath)
	progressEntries := make([]map[string]any, 0, 8)
	for index := 0; index < 8; index++ {
		progressEntries = append(progressEntries, map[string]any{
			"type": "tool_call", "iteration": index + 1, "tool_call_id": "old-call", "tool_name": "place_order",
			"arguments":  map[string]any{"symbol": "BTCUSDT", "audit": strings.Repeat("large-recovery-trace-", 200)},
			"result":     map[string]any{"ok": false, "error": map[string]any{"code": "TOOL_INTERRUPTED", "outcome": "unknown"}},
			"started_at": "2026-09-15T01:00:00Z",
		})
	}
	encodedProgress, _ := json.Marshal(progressEntries)
	progress := string(encodedProgress)
	if _, err := db.Exec(`UPDATE sessions SET status='running', current_run_id=1, started_at='2026-09-15T01:00:00Z' WHERE id=?`, session.ID); err != nil {
		t.Fatalf("seed running Session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO runs
		(session_id,run_id,status,trigger,triggers_json,summary,output,error,progress_json,model_call_count,tool_call_count,start_time)
		VALUES (?,1,'running','event','[{"event_id":"ev_old","created_run_id":1}]','must-not-be-reused','must-not-be-reused',NULL,?,0,8,'2026-09-15T01:00:00Z')`, session.ID, progress); err != nil {
		t.Fatalf("seed running Run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO events
		(id,owner_session_id,created_run_id,type,params_json,due_at,state_json,created_at,expires_at)
		VALUES ('ev_live',?,1,'timer','{"at":"2099-01-01T00:00:00.000000000Z"}','2099-01-01T00:00:00.000000000Z',NULL,'2026-09-15T01:00:00.000000000Z','2099-01-01T01:00:00.000000000Z')`, session.ID); err != nil {
		t.Fatalf("seed active Event: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO pending_triggers (session_id,payload_json,created_at)
		VALUES (?, '{"type":"timer","event_id":"ev_pending","created_run_id":1}', '2026-09-15T01:00:01.000000000Z')`, session.ID); err != nil {
		t.Fatalf("seed pending Trigger: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close recovery seed database: %v", err)
	}

	recoveryContext := make(chan string, 1)
	release := make(chan struct{})
	recoveryFixture := newRuntimeFixture()
	recoveryFixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode recovery Generate: %v", err)
			return
		}
		recoveryContext <- contextText(body)
		select {
		case <-release:
			writeFixtureJSON(w, http.StatusOK, finalResponse("recovered", "recovered", nil, nil))
		case <-request.Context().Done():
		}
	}
	recovered, _, _ := recoveryFixture.open(t, databasePath)

	oldRun := waitForRunStatus(t, recovered, session.ID, 1, "interrupted")
	if oldRun.Summary != nil || oldRun.Output != nil || oldRun.Error == nil || !strings.Contains(*oldRun.Error, "CRASH_RECOVERY") ||
		!strings.Contains(string(oldRun.Progress), "large-recovery-trace-") {
		t.Fatalf("recovered old Run = %+v", oldRun)
	}
	newRun := waitForRunStatus(t, recovered, session.ID, 2, "running")
	if newRun.Trigger != "server_recovery" || newRun.ModelCalls > 1 || newRun.ToolCalls != 0 {
		t.Fatalf("server_recovery Run = %+v", newRun)
	}

	select {
	case text := <-recoveryContext:
		if !containsAll(text, `"trigger":"server_recovery"`, `"previous_run_id":1`, `"status":"interrupted"`,
			`"max_loop":4`, `"max_tool_call":3`, `"account_snapshot":{"balance":97500`, `"user_name":"fixture"`,
			`"active_events"`, `"ev_live"`, `"pending_triggers"`, `"ev_pending"`, `"progress_summary"`,
			`"recent_progress"`, `"progress_omitted":3`, `"place_order"`, `"data_gaps"`) {
			t.Fatalf("recovery context omitted persisted facts: %s", text)
		}
		if len(text) >= 6000 || strings.Contains(text, "large-recovery-trace-") || strings.Contains(text, `"arguments"`) ||
			strings.Contains(text, "must-not-be-reused") || strings.Contains(text, "recent_run_summary") {
			t.Fatalf("recovery context reused an old summary/output: %s", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server_recovery Generate was not reached")
	}
	if events := listEventsHTTP(t, recovered, session.ID); len(events) != 1 || events[0].ID != "ev_live" {
		t.Fatalf("active Events after recovery = %+v", events)
	}
	assertTicket36PendingCount(t, databasePath, session.ID, 1)
	status, raw := runtimeRequest(t, recovered.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/1/history", agentAdminToken, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"status":"interrupted"`) {
		t.Fatalf("interrupted History = %d %s", status, raw)
	}

	close(release)
	waitForRunStatus(t, recovered, session.ID, 2, "completed")
}

func TestTicket36GracefulShutdownPreservesDesiredStateAndRecoveryIsReentrant(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	fixture.generate = func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run = %d %s", status, raw)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "running")
	if err := runtime.Close(); err != nil {
		t.Fatalf("graceful Runtime close: %v", err)
	}

	db := openTicket36Database(t, databasePath)
	var sessionStatus, runStatus, reason string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE id=?`, session.ID).Scan(&sessionStatus); err != nil {
		t.Fatalf("read desired Session status: %v", err)
	}
	if err := db.QueryRow(`SELECT status,error FROM runs WHERE session_id=? AND run_id=1`, session.ID).Scan(&runStatus, &reason); err != nil {
		t.Fatalf("read shutdown Run: %v", err)
	}
	if sessionStatus != "running" || runStatus != "interrupted" || !strings.Contains(reason, "SERVER_SHUTDOWN") {
		t.Fatalf("shutdown state = session %q Run %q reason %q", sessionStatus, runStatus, reason)
	}
	_ = db.Close()

	firstRecovery := newRuntimeFixture()
	firstRecovery.generate = func(w http.ResponseWriter, request *http.Request) { <-request.Context().Done() }
	reopened, _, _ := firstRecovery.open(t, databasePath)
	waitForRunStatus(t, reopened, session.ID, 2, "running")
	if err := reopened.Close(); err != nil {
		t.Fatalf("close first recovery: %v", err)
	}

	secondRecovery := newRuntimeFixture()
	secondRecovery.generate = func(w http.ResponseWriter, request *http.Request) { <-request.Context().Done() }
	reopenedAgain, _, _ := secondRecovery.open(t, databasePath)
	run2 := waitForRunStatus(t, reopenedAgain, session.ID, 2, "interrupted")
	run3 := waitForRunStatus(t, reopenedAgain, session.ID, 3, "running")
	if run2.Summary != nil || run3.Trigger != "server_recovery" {
		t.Fatalf("reentrant recovery Runs = old %+v new %+v", run2, run3)
	}
}

func TestTicket36InterruptsOrphanRunsButDoesNotRestartStoppedOrDeletedSessions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	stopped := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_stopped"})
	deleted := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_deleted"})
	if err := runtime.Close(); err != nil {
		t.Fatalf("close seed Runtime: %v", err)
	}
	db := openTicket36Database(t, databasePath)
	for _, item := range []struct{ id, status string }{{stopped.ID, "stopped"}, {deleted.ID, "deleted"}} {
		if _, err := db.Exec(`UPDATE sessions SET status=?, current_run_id=1 WHERE id=?`, item.status, item.id); err != nil {
			t.Fatalf("seed %s Session: %v", item.status, err)
		}
		if _, err := db.Exec(`INSERT INTO runs (session_id,run_id,status,trigger,progress_json,model_call_count,tool_call_count,start_time)
			VALUES (?,1,'running','session_start','[]',0,0,'2026-09-15T01:00:00Z')`, item.id); err != nil {
			t.Fatalf("seed orphan Run: %v", err)
		}
	}
	_ = db.Close()

	var calls int
	recoveryFixture := newRuntimeFixture()
	recoveryFixture.generate = func(w http.ResponseWriter, request *http.Request) { calls++ }
	recovered, _, _ := recoveryFixture.open(t, databasePath)
	for _, id := range []string{stopped.ID, deleted.ID} {
		run := waitForRunStatus(t, recovered, id, 1, "interrupted")
		if run.Summary != nil || run.Output != nil {
			t.Fatalf("orphan Run was summarized: %+v", run)
		}
		status, raw := runtimeRequest(t, recovered.Handler(), http.MethodGet, "/api/v1/sessions/"+id+"/runs/2", agentAdminToken, nil)
		if status != http.StatusNotFound {
			t.Fatalf("non-running Session recovered = %d %s", status, raw)
		}
	}
	if calls != 0 {
		t.Fatalf("Gateway recovery calls = %d, want 0", calls)
	}
}

func openTicket36Database(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open Ticket 36 database: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func assertTicket36PendingCount(t *testing.T, path, sessionID string, wanted int) {
	t.Helper()
	db := openTicket36Database(t, path)
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_triggers WHERE session_id=?`, sessionID).Scan(&count); err != nil {
		t.Fatalf("count pending Triggers: %v", err)
	}
	if count != wanted {
		t.Fatalf("pending Trigger count = %d, want %d", count, wanted)
	}
}
