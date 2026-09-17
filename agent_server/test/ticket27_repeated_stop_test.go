package test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A second stop on an already stopped Session must not erase the cleared_events /
// pending_triggers facts that the first stop saved: those facts exist nowhere else.
func TestTicket27RepeatedStopKeepsClearedEventFactsInRestartContext(t *testing.T) {
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
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	script.next(t, "busy decision 2")
	clock.AdvanceTo(t, soon)
	runEventRound(t, tasks)

	stop := func() {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
		if status != http.StatusOK {
			t.Fatalf("stop = %d %s", status, raw)
		}
	}
	stop()
	close(release)
	waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	first := getSessionForTest(t, runtime, session.ID).RestartContext

	stop()
	second := getSessionForTest(t, runtime, session.ID).RestartContext
	var facts struct {
		ClearedEvents   []json.RawMessage `json:"cleared_events"`
		PendingTriggers []json.RawMessage `json:"pending_triggers"`
	}
	if err := json.Unmarshal(second, &facts); err != nil {
		t.Fatalf("decode restart context %s: %v", second, err)
	}
	if len(facts.ClearedEvents) != 1 || len(facts.PendingTriggers) != 1 {
		t.Fatalf("second stop lost the cleared facts:\nfirst  = %s\nsecond = %s", first, second)
	}
}
