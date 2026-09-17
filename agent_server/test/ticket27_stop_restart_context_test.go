package test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestTicket27StopClearsEventsIntoRestartContextWithoutRebuildingThem(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	soon := clock.Now().Add(time.Minute)
	later := clock.Now().Add(2 * time.Hour)
	release := make(chan struct{})
	script := newLLMScript(
		respondToolCalls(
			createEventCall("soon", "timer", timerParams(soon)),
			createEventCall("later", "timer", timerParams(later)),
		),
		holdUntil(release, respondStop("never delivered")),
		respondStop("restarted"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	results := toolResults(t, script.next(t, "busy decision 2"))
	soonEvent, laterEvent := resultEvent(t, results[0]), resultEvent(t, results[1])

	clock.AdvanceTo(t, soon)
	runEventRound(t, tasks)
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].ID != laterEvent.ID {
		t.Fatalf("events after firing during busy Run = %+v", events)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop = %d %s", status, raw)
	}
	close(release)
	waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("stop left active events: %+v", events)
	}
	stopped := getSessionForTest(t, runtime, session.ID)
	var restart struct {
		PreviousRunID   int64            `json:"previous_run_id"`
		Status          string           `json:"status"`
		ClearedEvents   []map[string]any `json:"cleared_events"`
		PendingTriggers []map[string]any `json:"pending_triggers"`
	}
	if err := json.Unmarshal(stopped.RestartContext, &restart); err != nil {
		t.Fatalf("decode restart context %s: %v", stopped.RestartContext, err)
	}
	if restart.PreviousRunID != 1 || restart.Status != "interrupted" || len(restart.ClearedEvents) != 1 ||
		restart.ClearedEvents[0]["id"] != laterEvent.ID || restart.ClearedEvents[0]["type"] != "timer" ||
		len(restart.PendingTriggers) != 1 || restart.PendingTriggers[0]["event_id"] != soonEvent.ID {
		t.Fatalf("restart context = %s", stopped.RestartContext)
	}

	startSessionForTest(t, runtime, session.ID)
	restarted := script.next(t, "restart decision")
	assertInitialContextBasics(t, restarted, 4, 3)
	context := runContext(t, restarted)
	restartContext, _ := context["restart_context"].(map[string]any)
	if context["trigger"] != "session_restart" || restartContext == nil || context["triggers"] != nil {
		t.Fatalf("restart context delivered = %s", contextText(restarted))
	}
	cleared, _ := restartContext["cleared_events"].([]any)
	pending, _ := restartContext["pending_triggers"].([]any)
	if len(cleared) != 1 || len(pending) != 1 {
		t.Fatalf("restart context facts = %s", contextText(restarted))
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("restart rebuilt events: %+v", events)
	}
	clock.AdvanceTo(t, later)
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up from a cleared timer")
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 2 {
		t.Fatalf("cleared events fired after restart: runs = %v", ids)
	}
}

func TestTicket27DeleteClearsEventsAndDeletedSessionNeverWakes(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(createEventCall("timer", "timer", timerParams(at))),
		respondStop("armed"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	script.next(t, "decision 2")
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil); status != http.StatusOK {
		t.Fatalf("delete = %d %s", status, raw)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("deleted Session kept events: %+v", events)
	}
	deleted := getSessionForTest(t, runtime, session.ID)
	var restart struct {
		ClearedEvents []map[string]any `json:"cleared_events"`
	}
	_ = json.Unmarshal(deleted.RestartContext, &restart)
	if len(restart.ClearedEvents) != 1 {
		t.Fatalf("deleted Session audit context = %s", deleted.RestartContext)
	}
	clock.AdvanceTo(t, at)
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up of deleted Session")
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("deleted Session woke: runs = %v", ids)
	}
}
