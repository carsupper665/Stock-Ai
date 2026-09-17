package test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestTicket28TwentyThreeTriggersArrivingDuringBusyRunAreConsumedTenTenThreeInArrivalOrder(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	base := clock.Now()
	batch := func(prefix string, count int, at time.Time) []map[string]any {
		calls := make([]map[string]any, 0, count)
		for index := 0; index < count; index++ {
			calls = append(calls, createEventCall(prefix+jsonNumber(int64(index)), "timer", timerParams(at)))
		}
		return calls
	}
	firstFired, secondFired, thirdFired := make(chan struct{}), make(chan struct{}), make(chan struct{})
	script := newLLMScript(
		respondToolCalls(batch("first-", 10, base.Add(time.Minute))...),
		holdUntil(firstFired, respondToolCalls(batch("second-", 10, base.Add(2*time.Minute))...)),
		holdUntil(secondFired, respondToolCalls(
			createEventCall("third-timer", "timer", timerParams(base.Add(3*time.Minute))),
			priceEventCall("third-above", "price_above", "BTCUSDT", 60000),
			priceEventCall("third-below", "price_below", "ETHUSDT", 1000),
		)),
		holdUntil(thirdFired, respondStop("busy Run done")),
		respondStop("batch one handled"),
		respondStop("batch two handled"),
		respondStop("batch three handled"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, map[string]any{"max_tool_call": 40, "max_loop": 10})
	startSessionForTest(t, runtime, session.ID)

	script.next(t, "decision 1")
	expected := make([]string, 0, 23)
	collect := func(request map[string]any, from int) {
		for _, result := range toolResults(t, request)[from:] {
			expected = append(expected, resultEvent(t, result).ID)
		}
	}
	collect(script.next(t, "decision 2"), 0)
	clock.AdvanceTo(t, base.Add(time.Minute))
	runEventRound(t, tasks)
	close(firstFired)
	collect(script.next(t, "decision 3"), 10)
	clock.AdvanceTo(t, base.Add(2*time.Minute))
	runEventRound(t, tasks)
	close(secondFired)
	collect(script.next(t, "decision 4"), 20)
	book.set("crypto", "BTCUSDT", 60000, base.Add(3*time.Minute))
	book.set("crypto", "ETHUSDT", 999.5, base.Add(3*time.Minute))
	clock.AdvanceTo(t, base.Add(3*time.Minute))
	runEventRound(t, tasks)
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("unfired events remain: %+v", events)
	}
	close(thirdFired)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	received := make([]string, 0, 23)
	for _, wantedCount := range []int{10, 10, 3} {
		request := script.next(t, "event batch")
		triggers := contextTriggers(t, request)
		if len(triggers) != wantedCount {
			t.Fatalf("batch size = %d, want %d: %s", len(triggers), wantedCount, contextText(request))
		}
		for _, trigger := range triggers {
			received = append(received, trigger["event_id"].(string))
		}
		if wantedCount == 3 && (triggers[0]["type"] != "timer" || triggers[1]["type"] != "price_above" || triggers[2]["type"] != "price_below") {
			t.Fatalf("mixed batch types were changed: %s", contextText(request))
		}
	}
	if strings.Join(received, ",") != strings.Join(expected, ",") {
		t.Fatalf("delivered order\n got %v\nwant %v", received, expected)
	}
	for runID := int64(2); runID <= 4; runID++ {
		run := waitForRunStatus(t, runtime, session.ID, runID, "completed")
		persisted := getRunTriggers(t, runtime, session.ID, runID)
		if run.Trigger != "event" || len(persisted.Triggers) != []int{10, 10, 3}[runID-2] {
			t.Fatalf("Run %d = %+v triggers = %d", runID, run, len(persisted.Triggers))
		}
	}
	script.expectNoRequest(t, "fifth Run")
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 4 {
		t.Fatalf("runs = %v, want exactly 4", ids)
	}
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil); status != http.StatusNotFound {
		t.Fatalf("idle Session current Run status = %d, want 404", status)
	}
}

func TestTicket28PersistenceFailureAtEventToPendingBoundaryLosesNothing(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
		respondStop("armed"),
		respondStop("server recovered"),
		respondStop("woke after repair"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	created := resultEvent(t, lastToolResult(t, script.next(t, "decision 2")))
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	installRejectingTrigger(t, databasePath, "reject_pending", `
		BEFORE INSERT ON pending_triggers
		BEGIN
			INSERT INTO fault_markers(name) VALUES ('pending');
			SELECT RAISE(FAIL, 'reject pending');
		END`)

	clock.AdvanceTo(t, at)
	if err := runtime.LocalEventTask(clock, time.Second).Run(t.Context()); err == nil || !strings.Contains(err.Error(), "reject pending") {
		t.Fatalf("round error = %v, want injected pending failure", err)
	}
	script.expectNoRequest(t, "wake-up despite failed claim")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].ID != created.ID {
		t.Fatalf("event lost by rolled-back claim: %+v", events)
	}
	if err := runtime.HealthError(); err == nil {
		t.Fatal("persistence fault was not latched")
	}
	if err := runtime.Close(); err == nil {
		t.Fatal("Close() should report the latched fault")
	}

	dropTrigger(t, databasePath, "reject_pending")
	runtime, _, _ = fixture.open(t, databasePath)
	if recovery := script.next(t, "server recovery"); !strings.Contains(contextText(recovery), `"trigger":"server_recovery"`) {
		t.Fatal("missing recovery Run")
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	runEventRound(t, newEventTasks(runtime, clock))
	woke := script.next(t, "wake-up after repair")
	triggers := contextTriggers(t, woke)
	if len(triggers) != 1 || triggers[0]["event_id"] != created.ID {
		t.Fatalf("repaired wake-up context = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
	runEventRound(t, newEventTasks(runtime, clock))
	script.expectNoRequest(t, "duplicate consumption")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("consumed event still active: %+v", events)
	}
}

func TestTicket28PersistenceFailureAtPendingToRunBoundaryKeepsQueueOrderWithoutDuplicates(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	first := clock.Now().Add(time.Minute)
	second := clock.Now().Add(2 * time.Minute)
	script := newLLMScript(
		respondToolCalls(
			createEventCall("first", "timer", timerParams(first)),
			createEventCall("second", "timer", timerParams(second)),
		),
		respondStop("armed"),
		respondStop("server recovered"),
		respondStop("both handled"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	results := toolResults(t, script.next(t, "decision 2"))
	firstEvent, secondEvent := resultEvent(t, results[0]), resultEvent(t, results[1])
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	installRejectingTrigger(t, databasePath, "reject_event_run", `
		BEFORE INSERT ON runs WHEN NEW.trigger = 'event'
		BEGIN
			INSERT INTO fault_markers(name) VALUES ('event_run');
			SELECT RAISE(FAIL, 'reject event run');
		END`)

	clock.AdvanceTo(t, first)
	if err := runtime.LocalEventTask(clock, time.Second).Run(t.Context()); err == nil || !strings.Contains(err.Error(), "reject event run") {
		t.Fatalf("round error = %v, want injected Run failure", err)
	}
	script.expectNoRequest(t, "Run despite failed dispatch")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 2 {
		t.Fatalf("rolled-back round changed events: %+v", events)
	}
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("runs after rolled-back dispatch = %v", ids)
	}
	_ = runtime.Close()

	dropTrigger(t, databasePath, "reject_event_run")
	runtime, _, _ = fixture.open(t, databasePath)
	if recovery := script.next(t, "server recovery"); !strings.Contains(contextText(recovery), `"trigger":"server_recovery"`) {
		t.Fatal("missing recovery Run")
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	clock.AdvanceTo(t, second)
	runEventRound(t, newEventTasks(runtime, clock))
	woke := script.next(t, "batch after repair")
	triggers := contextTriggers(t, woke)
	if len(triggers) != 2 || triggers[0]["event_id"] != firstEvent.ID || triggers[1]["event_id"] != secondEvent.ID {
		t.Fatalf("repaired batch = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
	script.expectNoRequest(t, "duplicate batch")
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 3 {
		t.Fatalf("runs after repair = %v", ids)
	}
}

func TestTicket28FailedRunDoesNotBlockQueuedTriggersAndKeepsItsTerminalState(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	release := make(chan struct{})
	respondCut := func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": "cut", "tool_calls": []any{}, "finish_reason": "length", "usage": nil})
	}
	script := newLLMScript(
		respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
		holdUntil(release, respondCut),
		respondCut,
		respondCut,
		respondCut,
		respondStop("handled after failure"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	script.next(t, "busy decision 2")
	clock.AdvanceTo(t, at)
	runEventRound(t, tasks)
	close(release)
	script.next(t, "finalization repair 1")
	script.next(t, "finalization repair 2")
	script.next(t, "finalization repair 3")
	failed := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	woke := script.next(t, "queued trigger after failed Run")
	if len(contextTriggers(t, woke)) != 1 {
		t.Fatalf("queued trigger context = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	again := getRunForTest(t, runtime, session.ID, 1)
	if again.Status != "failed" || again.Error == nil || *again.Error != *failed.Error || again.Summary != nil {
		t.Fatalf("failed Run terminal state changed: %+v", again)
	}
	if session := getSessionForTest(t, runtime, session.ID); session.Status != "running" || session.CurrentRunID != 2 {
		t.Fatalf("Session after queued dispatch = %+v", session)
	}
}

func TestTicket28FinishDispatchInsertFailureKeepsQueueAndCompletedRun(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	release := make(chan struct{})
	script := newLLMScript(
		respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
		holdUntil(release, respondStop("Run one completed")),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "create timer")
	script.next(t, "Run one entered")
	clock.AdvanceTo(t, at)
	runEventRound(t, newEventTasks(runtime, clock))
	installRejectingTrigger(t, databasePath, "reject_finish_dispatch", `
		BEFORE INSERT ON runs WHEN NEW.trigger = 'event'
		BEGIN
			SELECT RAISE(FAIL, 'reject finish dispatch');
		END`)
	close(release)
	deadline := time.After(2 * time.Second)
	for runtime.HealthError() == nil {
		select {
		case <-deadline:
			t.Fatal("finishActive did not attempt pending dispatch")
		default:
			goruntime.Gosched()
		}
	}
	if err := runtime.Close(); err == nil || !strings.Contains(err.Error(), "reject finish dispatch") {
		t.Fatalf("Close() error = %v, want finish dispatch persistence fault", err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open persisted queue: %v", err)
	}
	defer db.Close()
	var runStatus string
	if err := db.QueryRow(`SELECT status FROM runs WHERE session_id = ? AND run_id = 1`, session.ID).Scan(&runStatus); err != nil || runStatus != "completed" {
		t.Fatalf("Run one status = %q error=%v", runStatus, err)
	}
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM pending_triggers WHERE session_id = ?`, session.ID); count != 1 {
		t.Fatalf("pending queue count = %d, want 1", count)
	}
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM runs WHERE session_id = ? AND run_id = 2`, session.ID); count != 0 {
		t.Fatalf("failed successor insert left %d Run rows", count)
	}
	var currentRunID int64
	if err := db.QueryRow(`SELECT current_run_id FROM sessions WHERE id = ?`, session.ID).Scan(&currentRunID); err != nil || currentRunID != 1 {
		t.Fatalf("current_run_id = %d error=%v, want 1", currentRunID, err)
	}
}

func TestTicket28LatePendingDeleteFailureRollsBackClaimRunAndRunID(t *testing.T) {
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
	installRejectingTrigger(t, databasePath, "reject_pending_delete", `
		BEFORE DELETE ON pending_triggers
		BEGIN
			SELECT RAISE(FAIL, 'reject pending delete');
		END`)

	clock.AdvanceTo(t, at)
	err := runtime.LocalEventTask(clock, time.Second).Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "reject pending delete") {
		t.Fatalf("round error = %v, want late pending DELETE failure", err)
	}
	script.expectNoRequest(t, "Run after rolled-back pending DELETE")
	if err := runtime.Close(); err == nil {
		t.Fatal("Close() should report the latched pending DELETE fault")
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open rolled-back database: %v", err)
	}
	defer db.Close()
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM events WHERE id = ?`, created.ID); count != 1 {
		t.Fatalf("active Event count = %d, want 1", count)
	}
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM pending_triggers WHERE session_id = ?`, session.ID); count != 0 {
		t.Fatalf("rolled-back pending count = %d, want 0", count)
	}
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM runs WHERE session_id = ?`, session.ID); count != 1 {
		t.Fatalf("Run count = %d, want 1", count)
	}
	var currentRunID int64
	if err := db.QueryRow(`SELECT current_run_id FROM sessions WHERE id = ?`, session.ID).Scan(&currentRunID); err != nil || currentRunID != 1 {
		t.Fatalf("current_run_id = %d error=%v, want 1", currentRunID, err)
	}
}

func TestTicket28EventLogInsertFailureRollsBackActiveEventDelete(t *testing.T) {
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
	installRejectingTrigger(t, databasePath, "reject_event_log", `
		BEFORE INSERT ON event_log
		BEGIN
			SELECT RAISE(FAIL, 'reject event log');
		END`)

	clock.AdvanceTo(t, at)
	err := runtime.LocalEventTask(clock, time.Second).Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "reject event log") {
		t.Fatalf("round error = %v, want Event log failure", err)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].ID != created.ID {
		t.Fatalf("Event delete was not rolled back: %+v", events)
	}
	if err := runtime.Close(); err == nil {
		t.Fatal("Close() should report the latched Event log fault")
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open Event log database: %v", err)
	}
	defer db.Close()
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM event_log WHERE event_id = ?`, created.ID); count != 0 {
		t.Fatalf("rolled-back Event log count = %d, want 0", count)
	}
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM pending_triggers WHERE session_id = ?`, session.ID); count != 0 {
		t.Fatalf("rolled-back pending count = %d, want 0", count)
	}
}

func TestTicket28StopAndDeleteHaveDeterministicOrderingWithPendingDispatch(t *testing.T) {
	mutations := []struct {
		name   string
		method string
		path   func(string) string
		status string
	}{
		{name: "stop", method: http.MethodPost, path: func(id string) string { return "/api/v1/sessions/" + id + "/stop" }, status: "stopped"},
		{name: "delete", method: http.MethodDelete, path: func(id string) string { return "/api/v1/sessions/" + id }, status: "deleted"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name+" wins", func(t *testing.T) {
			fixture := newRuntimeFixture()
			clock := newEventClock()
			at := clock.Now().Add(time.Minute)
			releaseRun := make(chan struct{})
			script := newLLMScript(
				respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
				holdUntil(releaseRun, respondStop("busy Run done")),
			)
			fixture.generate = script.handler(t)
			runtime, _, _ := fixture.open(t, "")
			tasks := newEventTasks(runtime, clock)
			session := createStoppedSession(t, runtime, nil)
			startSessionForTest(t, runtime, session.ID)
			script.next(t, "create timer")
			script.next(t, "busy Run entered")
			clock.AdvanceTo(t, at)
			runEventRound(t, tasks)

			status, raw := runtimeRequest(t, runtime.Handler(), mutation.method, mutation.path(session.ID), agentAdminToken, nil)
			close(releaseRun)
			if status != http.StatusOK {
				t.Fatalf("%s = %d %s", mutation.name, status, raw)
			}
			if run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted"); run.Summary != nil {
				t.Fatalf("interrupted Run has summary: %+v", run)
			}
			persisted := getSessionForTest(t, runtime, session.ID)
			if persisted.Status != mutation.status || len(startedRunIDs(t, runtime, session.ID)) != 1 {
				t.Fatalf("Session after %s = %+v", mutation.name, persisted)
			}
			var restart struct {
				Pending []json.RawMessage `json:"pending_triggers"`
			}
			if err := json.Unmarshal(persisted.RestartContext, &restart); err != nil || len(restart.Pending) != 1 {
				t.Fatalf("restart facts after %s = %s error=%v", mutation.name, persisted.RestartContext, err)
			}
			if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodGet,
				"/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil); status != http.StatusNotFound {
				t.Fatalf("current Run exists after %s", mutation.name)
			}
			clock.Advance(time.Hour)
			runEventRound(t, tasks)
			script.expectNoRequest(t, "request after stop/delete barrier")
			if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
				t.Fatalf("future round dispatched after %s: %v", mutation.name, ids)
			}
		})

		t.Run("dispatch wins before "+mutation.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			clock := newEventClock()
			at := clock.Now().Add(time.Minute)
			releaseRunOne := make(chan struct{})
			releaseRunTwo := make(chan struct{})
			script := newLLMScript(
				respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
				holdUntil(releaseRunOne, respondStop("Run one done")),
				holdUntil(releaseRunTwo, respondStop("Run two done")),
			)
			fixture.generate = script.handler(t)
			runtime, _, _ := fixture.open(t, "")
			tasks := newEventTasks(runtime, clock)
			session := createStoppedSession(t, runtime, nil)
			startSessionForTest(t, runtime, session.ID)
			script.next(t, "create timer")
			script.next(t, "Run one entered")
			clock.AdvanceTo(t, at)
			runEventRound(t, tasks)
			close(releaseRunOne)
			successor := script.next(t, "successor entered")
			if triggers := contextTriggers(t, successor); len(triggers) != 1 {
				t.Fatalf("successor triggers = %v", triggers)
			}
			if run := getRunForTest(t, runtime, session.ID, 1); run.Status != "completed" {
				t.Fatalf("Run one overlapped successor entry: %+v", run)
			}

			status, raw := runtimeRequest(t, runtime.Handler(), mutation.method, mutation.path(session.ID), agentAdminToken, nil)
			close(releaseRunTwo)
			if status != http.StatusOK {
				t.Fatalf("%s = %d %s", mutation.name, status, raw)
			}
			if run := waitForRunStatus(t, runtime, session.ID, 2, "interrupted"); run.Summary != nil {
				t.Fatalf("successor interruption = %+v", run)
			}
			persisted := getSessionForTest(t, runtime, session.ID)
			if persisted.Status != mutation.status || persisted.CurrentRunID != 2 {
				t.Fatalf("Session after successor %s = %+v", mutation.name, persisted)
			}
			var restart struct {
				Triggers []json.RawMessage `json:"triggers"`
				Pending  []json.RawMessage `json:"pending_triggers"`
			}
			if err := json.Unmarshal(persisted.RestartContext, &restart); err != nil || len(restart.Triggers) != 1 || len(restart.Pending) != 0 {
				t.Fatalf("successor restart facts = %s error=%v", persisted.RestartContext, err)
			}
			clock.Advance(time.Hour)
			runEventRound(t, tasks)
			script.expectNoRequest(t, "request after successor stop/delete barrier")
			if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 2 {
				t.Fatalf("future round created extra Run after %s: %v", mutation.name, ids)
			}
		})
	}
}

func TestTicket28RuntimeCloseKeepsPendingTriggersInArrivalOrderForRecovery(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	first := clock.Now().Add(time.Minute)
	second := clock.Now().Add(2 * time.Minute)
	release := make(chan struct{})
	recoveryRelease := make(chan struct{})
	script := newLLMScript(
		respondToolCalls(createEventCall("first", "timer", timerParams(first)), createEventCall("second", "timer", timerParams(second))),
		holdUntil(release, respondStop("never persisted")),
		holdUntil(recoveryRelease, respondStop("recovery inspected pending")),
		respondStop("recovered batch"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	results := toolResults(t, script.next(t, "busy decision 2"))
	firstEvent, secondEvent := resultEvent(t, results[0]), resultEvent(t, results[1])
	clock.AdvanceTo(t, first)
	runEventRound(t, newEventTasks(runtime, clock))
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(release)

	runtime, _, _ = fixture.open(t, databasePath)
	if recovery := script.next(t, "server recovery before pending dispatch"); !strings.Contains(contextText(recovery), `"trigger":"server_recovery"`) {
		t.Fatal("missing recovery Run")
	}
	if run := getRunForTest(t, runtime, session.ID, 1); run.Status != "interrupted" {
		t.Fatalf("Run 1 after runtime close = %+v", run)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].ID != secondEvent.ID {
		t.Fatalf("runtime close changed active events: %+v", events)
	}
	clock.AdvanceTo(t, second)
	runEventRound(t, newEventTasks(runtime, clock))
	close(recoveryRelease)
	triggers := contextTriggers(t, script.next(t, "batch after reopen"))
	if len(triggers) != 2 || triggers[0]["event_id"] != firstEvent.ID || triggers[1]["event_id"] != secondEvent.ID {
		t.Fatalf("recovered batch = %v", triggers)
	}
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
}

func databaseCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count persisted rows: %v", err)
	}
	return count
}
