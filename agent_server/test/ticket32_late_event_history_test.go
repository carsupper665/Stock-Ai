package test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTicket32LateEventOutcomesRefreshCompletedHistoryImmediately(t *testing.T) {
	for _, outcome := range []string{"deleted_by_user", "expired"} {
		t.Run(outcome, func(t *testing.T) {
			fixture := newRuntimeFixture()
			clock := newEventClock()
			at := clock.Now().Add(time.Minute)
			script := newLLMScript(respondToolCalls(createEventCall("create", "timer", timerParams(at))), respondStop("armed"))
			fixture.generate = script.handler(t)
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, nil)
			startSessionForTest(t, runtime, session.ID)
			script.next(t, "create event")
			event := resultEvent(t, lastToolResult(t, script.next(t, "finish creator")))
			waitForRunStatus(t, runtime, session.ID, 1, "completed")
			status, before := getHistoryShard(t, runtime.Handler(), session.ID, 1)
			if status != 200 || strings.Contains(string(before), `"outcome":"`+outcome+`"`) {
				t.Fatalf("before=%d %s", status, before)
			}
			if outcome == "deleted_by_user" {
				status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/events/"+event.ID, agentAdminToken, nil)
				if status != 200 {
					t.Fatalf("delete=%d %s", status, raw)
				}
			} else {
				clock.Advance(2 * time.Hour)
				if err := runtime.LocalEventTask(clock, time.Second).Run(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			status, after := getHistoryShard(t, runtime.Handler(), session.ID, 1)
			if status != 200 || !strings.Contains(string(after), `"outcome":"`+outcome+`"`) {
				t.Fatalf("late outcome missing=%d %s", status, after)
			}
			script.expectNoRequest(t, "audit refresh must not invoke a model")
		})
	}
}
