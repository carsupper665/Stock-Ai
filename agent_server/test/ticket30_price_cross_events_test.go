package test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type crossState struct {
	Price     float64 `json:"price"`
	UpdatedAt string  `json:"updated_at"`
}

func eventState(t *testing.T, runtime *common.AgentRuntime, sessionID, eventID string) *crossState {
	t.Helper()
	for _, event := range listEventsHTTP(t, runtime, sessionID) {
		if event.ID != eventID {
			continue
		}
		if string(event.State) == "null" {
			return nil
		}
		var state crossState
		if err := json.Unmarshal(event.State, &state); err != nil {
			t.Fatalf("decode state %s: %v", event.State, err)
		}
		return &state
	}
	t.Fatalf("event %s is not active", eventID)
	return nil
}

func TestTicket30CrossEventsFireOnlyAtTheActualCrossingWithFirstSampleAsBaseline(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(
			priceEventCall("up", "price_cross_above", "BTCUSDT", 60000),
			priceEventCall("down", "price_cross_below", "ETHUSDT", 1000),
		),
		respondStop("armed"),
		respondStop("handled crossing"),
		respondStop("handled crossing"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session, events := armPriceEvents(t, script, runtime, nil)
	up, down := events[0], events[1]

	feed := func(btc, eth float64) time.Time {
		t.Helper()
		clock.Advance(time.Second)
		book.set("crypto", "BTCUSDT", btc, clock.Now())
		book.set("crypto", "ETHUSDT", eth, clock.Now())
		runEventRound(t, tasks)
		return clock.Now()
	}
	// First samples only initialize even though BTC is already above and ETH already below.
	firstAt := feed(61000, 900)
	script.expectNoRequest(t, "wake-up from first sample")
	if state := eventState(t, runtime, session.ID, up.ID); state == nil || state.Price != 61000 || !eventTime(t, state.UpdatedAt).Equal(firstAt) {
		t.Fatalf("cross_above baseline after first sample = %+v", state)
	}
	// Touching the threshold from the wrong side is not a crossing.
	feed(60000, 1000)
	feed(60001, 999)
	script.expectNoRequest(t, "wake-up without crossing")
	// Moving to the other side without ever being strictly beyond it does not arm a crossing either.
	feed(60000, 1000)
	feed(59999, 1001)
	script.expectNoRequest(t, "wake-up when leaving the threshold")
	if state := eventState(t, runtime, session.ID, up.ID); state == nil || state.Price != 59999 {
		t.Fatalf("cross_above baseline = %+v, want 59999", state)
	}
	// previous 59999 < 60000 <= 60000 and previous 1001 > 1000 >= 1000: both cross at equality.
	crossedAt := feed(60000, 1000)
	woke := script.next(t, "crossing wake-up")
	triggers := contextTriggers(t, woke)
	if len(triggers) != 2 {
		t.Fatalf("crossing triggers = %s", contextText(woke))
	}
	upMatch, _ := triggers[0]["matched"].(map[string]any)
	downMatch, _ := triggers[1]["matched"].(map[string]any)
	if triggers[0]["event_id"] != up.ID || upMatch["previous_price"] != float64(59999) || upMatch["price"] != float64(60000) ||
		upMatch["condition"] != "previous_price < 60000 <= price" || !eventTime(t, upMatch["updated_at"].(string)).Equal(crossedAt) ||
		!eventTime(t, upMatch["previous_updated_at"].(string)).Equal(crossedAt.Add(-time.Second)) ||
		triggers[1]["event_id"] != down.ID || downMatch["previous_price"] != float64(1001) || downMatch["condition"] != "previous_price > 1000 >= price" {
		t.Fatalf("crossing facts = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	feed(59000, 1100)
	feed(61000, 900)
	script.expectNoRequest(t, "second crossing after one-shot")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("cross events still active: %+v", events)
	}
}

func TestTicket30CrossBaselineSurvivesRestartAndRejectsStaleDuplicateAndReversedSamples(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(priceEventCall("up", "price_cross_above", "BTCUSDT", 60000)),
		respondStop("armed"),
		respondStop("recovered"),
		respondStop("handled crossing"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)
	session, events := armPriceEvents(t, script, runtime, nil)
	up := events[0]

	baselineAt := clock.Now()
	book.set("crypto", "BTCUSDT", 59000, baselineAt)
	runEventRound(t, newEventTasks(runtime, clock))
	if state := eventState(t, runtime, session.ID, up.ID); state == nil || state.Price != 59000 {
		t.Fatalf("baseline before restart = %+v", state)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	runtime = openEventRuntime(t, fixture, book, databasePath)
	tasks := newEventTasks(runtime, clock)
	// The persisted baseline is what the restarted Runtime compares against and what the
	// server_recovery Run sees in recovery_context.active_events.
	recovery := recoveryContext(t, completeRecoveryRun(t, script, runtime, session.ID, 2))
	active, _ := recovery["active_events"].([]any)
	if len(active) != 1 || active[0].(map[string]any)["id"] != up.ID ||
		active[0].(map[string]any)["state"].(map[string]any)["price"] != float64(59000) {
		t.Fatalf("recovery context active events = %v", recovery["active_events"])
	}
	if state := eventState(t, runtime, session.ID, up.ID); state == nil || state.Price != 59000 {
		t.Fatalf("baseline after restart = %+v", state)
	}
	// A sample older than the persisted baseline must neither fire nor replace it.
	book.set("crypto", "BTCUSDT", 61000, baselineAt.Add(-time.Second))
	runEventRound(t, tasks)
	// A repeated baseline sample carries no new information.
	book.set("crypto", "BTCUSDT", 61000, baselineAt)
	runEventRound(t, tasks)
	// A stale sample is ignored even though it is newer than the baseline.
	clock.Advance(time.Minute)
	book.set("crypto", "BTCUSDT", 61000, clock.Now().Add(-31*time.Second))
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up from stale, duplicate, or reversed samples")
	if state := eventState(t, runtime, session.ID, up.ID); state == nil || state.Price != 59000 || !eventTime(t, state.UpdatedAt).Equal(baselineAt) {
		t.Fatalf("baseline polluted = %+v", state)
	}

	book.set("crypto", "BTCUSDT", 60000, clock.Now())
	runEventRound(t, tasks)
	matched, _ := contextTriggers(t, script.next(t, "crossing after restart"))[0]["matched"].(map[string]any)
	if matched["previous_price"] != float64(59000) || matched["price"] != float64(60000) {
		t.Fatalf("crossing after restart = %v", matched)
	}
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
}
