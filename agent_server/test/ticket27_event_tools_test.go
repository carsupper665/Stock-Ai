package test

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket27CreateEventValidatesArgumentsLimitAndOwnership(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	future := clock.Now().Add(time.Minute)
	invalidCalls := []map[string]any{
		toolCall("bad-type", "create_event", map[string]any{"type": "order_filled", "params": map[string]any{}}),
		toolCall("separate-stop-type", "create_event", map[string]any{"type": "stop_loss", "params": map[string]any{"position_id": "pos_1"}}),
		toolCall("position-id-missing", "create_event", map[string]any{"type": "position_close", "params": map[string]any{}}),
		toolCall("position-extra-param", "create_event", map[string]any{"type": "position_close", "params": map[string]any{"position_id": "pos_1", "reason": "stop_loss"}}),
		toolCall("bad-params", "create_event", map[string]any{"type": "timer", "params": nil}),
		toolCall("past", "create_event", map[string]any{"type": "timer", "params": timerParams(clock.Now().Add(-time.Second))}),
		toolCall("unknown-field", "create_event", map[string]any{"type": "timer", "params": map[string]any{"at": future.Format(time.RFC3339), "repeat": true}}),
		toolCall("no-offset", "create_event", map[string]any{"type": "timer", "params": map[string]any{"at": "2030-01-01 00:00:00"}}),
		toolCall("expiry-before-at", "create_event", map[string]any{"type": "timer", "params": timerParams(future), "expires_at": future.Format(time.RFC3339)}),
		toolCall("extra-top-level", "create_event", map[string]any{"type": "timer", "params": timerParams(future), "session_id": "s_other"}),
		toolCall("delete-missing", "delete_event", map[string]any{"event_id": "ev_0000000000000000"}),
		toolCall("list-with-args", "list_events", map[string]any{"session_id": "s_other"}),
	}
	limitCalls := make([]map[string]any, 0, 11)
	for index := 0; index < 11; index++ {
		limitCalls = append(limitCalls, createEventCall("limit-"+jsonNumber(int64(index)), "timer", timerParams(future.Add(time.Duration(index)*time.Second))))
	}
	script := newLLMScript(
		respondToolCalls(invalidCalls...),
		respondToolCalls(limitCalls...),
		respondToolCalls(toolCall("list", "list_events", map[string]any{})),
		respondStop("done"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_tool_call": 40, "max_loop": 10})
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")

	results := toolResults(t, script.next(t, "decision 2"))
	if len(results) != len(invalidCalls) {
		t.Fatalf("invalid-call results = %d, want %d", len(results), len(invalidCalls))
	}
	for index, result := range results {
		wanted := "INVALID_TOOL_ARGUMENTS"
		if invalidCalls[index]["id"] == "delete-missing" {
			wanted = "EVENT_NOT_FOUND"
		}
		if result["ok"] != false || toolErrorCode(result) != wanted || result["error"].(map[string]any)["outcome"] != "not_executed" {
			t.Fatalf("call %s result = %v, want %s", invalidCalls[index]["id"], result, wanted)
		}
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("invalid calls created events: %+v", events)
	}

	results = toolResults(t, script.next(t, "decision 3"))[len(invalidCalls):]
	if len(results) != 11 {
		t.Fatalf("limit results = %d", len(results))
	}
	for index := 0; index < 10; index++ {
		if results[index]["ok"] != true {
			t.Fatalf("event %d within the limit failed: %v", index+1, results[index])
		}
	}
	if toolErrorCode(results[10]) != "EVENT_LIMIT_EXCEEDED" {
		t.Fatalf("11th event result = %v", results[10])
	}
	listed := toolResults(t, script.next(t, "decision 4"))
	listResult := listed[len(listed)-1]["result"].(map[string]any)["events"].([]any)
	if len(listResult) != 10 {
		t.Fatalf("list_events returned %d events, want 10", len(listResult))
	}
	first := listResult[0].(map[string]any)
	if len(first) != 3 || first["id"] == "" || first["type"] != "timer" || first["threshold"] == "" || first["params"] != nil || first["session_id"] != nil {
		t.Fatalf("list_events did not use compact Tool view: %v", first)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 10 {
		t.Fatalf("Control Panel sees %d events, want 10", len(events))
	}
}

func TestTicket27ControlPanelCanListAndDeleteButNeverCreateEvents(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(createEventCall("call-timer", "timer", timerParams(at))),
		respondStop("armed"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	other := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_other"})
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	created := resultEvent(t, lastToolResult(t, script.next(t, "decision 2")))
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/events", agentAdminToken, map[string]any{"type": "timer", "params": timerParams(at)})
	if status != http.StatusMethodNotAllowed || !strings.Contains(string(raw), "METHOD_NOT_ALLOWED") {
		t.Fatalf("user POST event = %d %s", status, raw)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/s_missing/events", agentAdminToken, nil)
	if status != http.StatusNotFound || !strings.Contains(string(raw), "SESSION_NOT_FOUND") {
		t.Fatalf("unknown Session events = %d %s", status, raw)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+other.ID+"/events/"+created.ID, agentAdminToken, nil)
	if status != http.StatusNotFound || !strings.Contains(string(raw), "EVENT_NOT_FOUND") {
		t.Fatalf("cross-Session delete = %d %s", status, raw)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 {
		t.Fatalf("cross-Session delete removed the event: %+v", events)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID+"/events/"+created.ID, agentAdminToken, nil)
	deleted := decodeBody[common.Event](t, raw)
	if status != http.StatusOK || deleted.ID != created.ID || deleted.Type != "timer" {
		t.Fatalf("user delete = %d %s", status, raw)
	}
	status, _ = runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID+"/events/"+created.ID, agentAdminToken, nil)
	if status != http.StatusNotFound {
		t.Fatalf("repeated delete status = %d, want 404", status)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("deleted event still listed: %+v", events)
	}
	clock.AdvanceTo(t, at)
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up from deleted timer")
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("deleted timer still fired: runs = %v", ids)
	}
}

func TestTicket27ExpiredTimerIsRemovedWithoutWakingAndAgentCanDeleteOwnEvent(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(
			createEventCall("expiring", "timer", timerParams(at)),
			createEventCall("deleted", "timer", timerParams(at)),
		),
		func(w http.ResponseWriter, request *http.Request, body map[string]any) {
			// Runs on the fixture goroutine: extract without t.Fatalf so a failure cannot strand the request.
			results := toolResults(t, body)
			created, _ := results[1]["result"].(map[string]any)
			eventID, _ := created["id"].(string)
			if eventID == "" {
				t.Errorf("second create_event result = %v", results[1])
			}
			respondToolCalls(toolCall("delete", "delete_event", map[string]any{"event_id": eventID}))(w, request, body)
		},
		respondStop("armed"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	script.next(t, "decision 2")
	deleted := resultEvent(t, lastToolResult(t, script.next(t, "decision 3")))
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	events := listEventsHTTP(t, runtime, session.ID)
	if len(events) != 1 || events[0].ID == deleted.ID {
		t.Fatalf("events after agent delete = %+v", events)
	}

	clock.AdvanceTo(t, eventTime(t, events[0].ExpiresAt))
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up from expired timer")
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 0 {
		t.Fatalf("expired timer still active: %+v", remaining)
	}
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("expired timer fired: runs = %v", ids)
	}
}

func TestTicket27CanceledDeleteIsLocalButDatabaseFailureLatchesRuntime(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
		respondStop("armed"),
		respondStop("unrelated completed"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "create timer")
	created := resultEvent(t, lastToolResult(t, script.next(t, "armed")))
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	unrelated := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_unrelated"})
	startSessionForTest(t, runtime, unrelated.ID)
	script.next(t, "unrelated Run")
	waitForRunStatus(t, runtime, unrelated.ID, 1, "completed")

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	status, raw := runtimeRequestWithContext(t, canceledContext, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID+"/events/"+created.ID, nil)
	if status != http.StatusRequestTimeout || !strings.Contains(string(raw), "REQUEST_CANCELED") {
		t.Fatalf("pre-canceled DELETE = %d %s", status, raw)
	}
	if err := runtime.HealthError(); err != nil {
		t.Fatalf("request cancellation latched Runtime fault: %v", err)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].ID != created.ID {
		t.Fatalf("pre-canceled DELETE changed Event: %+v", events)
	}
	if run := getRunForTest(t, runtime, unrelated.ID, 1); run.Status != "completed" || run.Summary == nil {
		t.Fatalf("pre-canceled DELETE changed unrelated Run: %+v", run)
	}

	installRejectingTrigger(t, databasePath, "reject_event_delete", `
		BEFORE DELETE ON events
		BEGIN
			SELECT RAISE(FAIL, 'reject Event delete');
		END`)
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID+"/events/"+created.ID, agentAdminToken, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("database-failed DELETE = %d %s", status, raw)
	}
	if err := runtime.HealthError(); err == nil || !strings.Contains(err.Error(), "reject Event delete") {
		t.Fatalf("database failure was not latched: %v", err)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].ID != created.ID {
		t.Fatalf("database-failed DELETE changed Event: %+v", events)
	}
	if run := getRunForTest(t, runtime, unrelated.ID, 1); run.Status != "completed" || run.Summary == nil {
		t.Fatalf("database-failed DELETE changed unrelated Run: %+v", run)
	}
}

func TestTicket27FireAndDeleteOrdersProduceOneTerminalLog(t *testing.T) {
	t.Run("fire commits first", func(t *testing.T) {
		fixture := newRuntimeFixture()
		clock := newEventClock()
		at := clock.Now().Add(time.Minute)
		script := newLLMScript(
			respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
			respondStop("armed"),
			respondStop("fired"),
		)
		fixture.generate = script.handler(t)
		databasePath := filepath.Join(t.TempDir(), "agent.db")
		runtime, _, _ := fixture.open(t, databasePath)
		session := createStoppedSession(t, runtime, nil)
		startSessionForTest(t, runtime, session.ID)
		script.next(t, "create timer")
		created := resultEvent(t, lastToolResult(t, script.next(t, "armed")))
		waitForRunStatus(t, runtime, session.ID, 1, "completed")
		clock.AdvanceTo(t, at)
		runEventRound(t, newEventTasks(runtime, clock))
		script.next(t, "fired Run")
		waitForRunStatus(t, runtime, session.ID, 2, "completed")
		status, _ := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
			"/api/v1/sessions/"+session.ID+"/events/"+created.ID, agentAdminToken, nil)
		if status != http.StatusNotFound {
			t.Fatalf("DELETE after fire status = %d, want 404", status)
		}
		assertSingleEventOutcome(t, databasePath, created.ID, "fired")
	})

	t.Run("delete commits first", func(t *testing.T) {
		fixture := newRuntimeFixture()
		clock := newEventClock()
		at := clock.Now().Add(time.Minute)
		script := newLLMScript(
			respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
			respondStop("armed"),
		)
		fixture.generate = script.handler(t)
		databasePath := filepath.Join(t.TempDir(), "agent.db")
		runtime, _, _ := fixture.open(t, databasePath)
		session := createStoppedSession(t, runtime, nil)
		startSessionForTest(t, runtime, session.ID)
		script.next(t, "create timer")
		created := resultEvent(t, lastToolResult(t, script.next(t, "armed")))
		waitForRunStatus(t, runtime, session.ID, 1, "completed")
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
			"/api/v1/sessions/"+session.ID+"/events/"+created.ID, agentAdminToken, nil)
		if status != http.StatusOK {
			t.Fatalf("DELETE before fire = %d %s", status, raw)
		}
		clock.AdvanceTo(t, at)
		runEventRound(t, newEventTasks(runtime, clock))
		script.expectNoRequest(t, "Run after Event delete")
		if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
			t.Fatalf("deleted Event fired: %v", ids)
		}
		assertSingleEventOutcome(t, databasePath, created.ID, "deleted_by_user")
	})
}

func assertSingleEventOutcome(t *testing.T, databasePath, eventID, wanted string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open Event log database: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT outcome FROM event_log WHERE event_id = ? ORDER BY id`, eventID)
	if err != nil {
		t.Fatalf("query Event outcomes: %v", err)
	}
	defer rows.Close()
	var outcomes []string
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			t.Fatalf("scan Event outcome: %v", err)
		}
		outcomes = append(outcomes, outcome)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read Event outcomes: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0] != wanted {
		t.Fatalf("Event outcomes = %v, want [%s]", outcomes, wanted)
	}
}
