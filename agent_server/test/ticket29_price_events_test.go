package test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

// armPriceEvents starts a Session whose first Run creates the given events and completes.
func armPriceEvents(t *testing.T, script *llmScript, runtime *common.AgentRuntime, overrides map[string]any) (common.Session, []common.Event) {
	t.Helper()
	session := createStoppedSession(t, runtime, overrides)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	results := toolResults(t, script.next(t, "decision 2"))
	eventIDs := make([]string, 0, len(results))
	for _, result := range results {
		eventIDs = append(eventIDs, resultEvent(t, result).ID)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	persisted := listEventsHTTP(t, runtime, session.ID)
	byID := make(map[string]common.Event, len(persisted))
	for _, event := range persisted {
		byID[event.ID] = event
	}
	events := make([]common.Event, 0, len(eventIDs))
	for _, id := range eventIDs {
		event, ok := byID[id]
		if !ok {
			t.Fatalf("created Event %s is not active: %+v", id, persisted)
		}
		events = append(events, event)
	}
	return session, events
}

func TestTicket29PriceAboveAndBelowFireOnceInclusiveOfThreshold(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(
			priceEventCall("above", "price_above", "btcusdt", 60000),
			createEventCall("below", "price_below", map[string]any{"market": "Crypto", "symbol": "ETHUSDT", "price": 1000}),
		),
		respondStop("armed"),
		respondStop("handled above"),
		respondStop("handled below"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session, events := armPriceEvents(t, script, runtime, nil)
	var params struct {
		Market string  `json:"market"`
		Symbol string  `json:"symbol"`
		Price  float64 `json:"price"`
	}
	_ = json.Unmarshal(events[0].Params, &params)
	if params.Market != "crypto" || params.Symbol != "BTCUSDT" || params.Price != 60000 || string(events[0].State) != "null" ||
		!eventTime(t, events[0].ExpiresAt).Equal(eventTime(t, events[0].CreatedAt).Add(24*time.Hour)) {
		t.Fatalf("price_above event = %+v params = %+v", events[0], params)
	}

	sampleAt := clock.Now()
	book.set("crypto", "BTCUSDT", 59999.99, sampleAt)
	book.set("crypto", "ETHUSDT", 1000.01, sampleAt)
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up below threshold")
	if book.requestCount("crypto", "BTCUSDT") != 1 || book.requestCount("crypto", "ETHUSDT") != 1 {
		t.Fatalf("Backend requests = %v, want one per instrument", book.requests)
	}

	clock.Advance(time.Second)
	sampleAt = clock.Now()
	book.set("crypto", "BTCUSDT", 60000, sampleAt)
	book.set("crypto", "ETHUSDT", 1000, sampleAt)
	runEventRound(t, tasks)
	woke := script.next(t, "wake-up at threshold")
	triggers := contextTriggers(t, woke)
	matched, _ := triggers[0]["matched"].(map[string]any)
	if len(triggers) != 2 || triggers[0]["event_id"] != events[0].ID || triggers[1]["event_id"] != events[1].ID ||
		matched["price"] != float64(60000) || matched["condition"] != "price >= 60000" ||
		!eventTime(t, matched["updated_at"].(string)).Equal(sampleAt) ||
		triggers[1]["matched"].(map[string]any)["condition"] != "price <= 1000" {
		t.Fatalf("price triggers = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 0 {
		t.Fatalf("fired price events still active: %+v", remaining)
	}

	clock.Advance(time.Second)
	book.set("crypto", "BTCUSDT", 70000, clock.Now())
	runEventRound(t, tasks)
	script.expectNoRequest(t, "second wake-up")
	if book.requestCount("crypto", "BTCUSDT") != 2 {
		t.Fatalf("Backend was queried without watchers: %v", book.requests)
	}
}

func TestTicket29SharedInstrumentIsSampledOncePerRoundAcrossSessionsAndWatchSetFollowsEvents(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(priceEventCall("a", "price_above", "BTCUSDT", 60000)),
		respondStop("armed A"),
		respondToolCalls(priceEventCall("b", "price_above", "BTCUSDT", 65000), createEventCall("t", "timer", timerParams(clock.Now().Add(time.Hour)))),
		respondStop("armed B"),
		respondStop("woke"),
		respondStop("woke"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	sessionA, _ := armPriceEvents(t, script, runtime, nil)
	sessionB, eventsB := armPriceEvents(t, script, runtime, map[string]any{"account_id": "acc_b"})

	book.set("crypto", "BTCUSDT", 61000, clock.Now())
	runEventRound(t, tasks)
	if book.requestCount("crypto", "BTCUSDT") != 1 {
		t.Fatalf("shared instrument was sampled %d times in one round", book.requestCount("crypto", "BTCUSDT"))
	}
	if len(contextTriggers(t, script.next(t, "Session A wake-up"))) != 1 {
		t.Fatal("Session A did not receive its trigger")
	}
	waitForRunStatus(t, runtime, sessionA.ID, 2, "completed")
	if events := listEventsHTTP(t, runtime, sessionB.ID); len(events) != 2 {
		t.Fatalf("Session B events changed by Session A's trigger: %+v", events)
	}
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodDelete,
		"/api/v1/sessions/"+sessionB.ID+"/events/"+eventsB[0].ID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("delete price event status = %d", status)
	}
	clock.Advance(time.Second)
	book.set("crypto", "BTCUSDT", 66000, clock.Now())
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up after delete")
	if book.requestCount("crypto", "BTCUSDT") != 1 {
		t.Fatalf("instrument without watchers was still sampled: %v", book.requests)
	}
}

func TestTicket29InvalidStaleOutOfOrderAndExpiredSamplesNeverWake(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	expiresAt := clock.Now().Add(10 * time.Minute)
	script := newLLMScript(
		respondToolCalls(
			toolCall("above", "create_event", map[string]any{
				"type": "price_above", "params": map[string]any{"symbol": "BTCUSDT", "price": 60000},
				"expires_at": expiresAt.Format(time.RFC3339Nano),
			}),
			priceEventCall("bad-price", "price_above", "BTCUSDT", 0),
			toolCall("string-price", "create_event", map[string]any{"type": "price_below", "params": map[string]any{"symbol": "BTCUSDT", "price": "60000"}}),
			toolCall("null-market", "create_event", map[string]any{"type": "price_below", "params": map[string]any{"market": nil, "symbol": "BTCUSDT", "price": 1}}),
		),
		respondStop("armed"),
		respondStop("woke"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, map[string]any{"max_tool_call": 10})
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	results := toolResults(t, script.next(t, "decision 2"))
	for _, invalid := range results[1:] {
		if toolErrorCode(invalid) != "INVALID_TOOL_ARGUMENTS" {
			t.Fatalf("invalid price params accepted: %v", invalid)
		}
	}
	event := resultEvent(t, results[0])
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	persisted := listEventsHTTP(t, runtime, session.ID)
	if len(persisted) != 1 || persisted[0].ID != event.ID || !eventTime(t, persisted[0].ExpiresAt).Equal(expiresAt) {
		t.Fatalf("explicit expiry event = %+v, want %s", persisted, expiresAt)
	}

	noWake := func(name string) {
		t.Helper()
		runEventRound(t, tasks)
		script.expectNoRequest(t, name)
		if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 {
			t.Fatalf("%s: event changed: %+v", name, events)
		}
	}
	book.fail("crypto", "BTCUSDT", http.StatusBadGateway)
	noWake("Backend error")
	book.setRaw("crypto", "BTCUSDT", map[string]any{"market": "crypto", "symbol": "BTCUSDT", "price": "61000", "updated_at": clock.Now().Format(time.RFC3339Nano)})
	noWake("malformed price")
	book.setRaw("crypto", "BTCUSDT", map[string]any{"market": "crypto", "symbol": "BTCUSDT", "price": 61000})
	noWake("missing source time")
	book.set("crypto", "BTCUSDT", 61000, clock.Now().Add(-31*time.Second))
	noWake("stale sample")
	fresh := clock.Now()
	book.set("crypto", "BTCUSDT", 59000, fresh)
	noWake("fresh sample below threshold")
	book.set("crypto", "BTCUSDT", 61000, fresh.Add(-time.Second))
	noWake("out-of-order sample")
	book.set("crypto", "BTCUSDT", 61000, fresh)
	noWake("duplicate sample")

	clock.AdvanceTo(t, expiresAt)
	book.set("crypto", "BTCUSDT", 61000, clock.Now())
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up from expired event")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("expired price event still active: %+v", events)
	}
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("expired event fired: runs = %v", ids)
	}
}

func TestTicket29PriceMatchDuringBusyRunIsQueuedAndTimerOnlySessionsSampleNothing(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	release := make(chan struct{})
	script := newLLMScript(
		respondToolCalls(priceEventCall("above", "price_above", "BTCUSDT", 60000), createEventCall("timer", "timer", timerParams(clock.Now().Add(time.Hour)))),
		holdUntil(release, respondStop("busy done")),
		respondStop("handled queued price trigger"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	script.next(t, "busy decision 2")
	book.set("crypto", "BTCUSDT", 60000, clock.Now())
	runEventRound(t, tasks)
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].Type != "timer" {
		t.Fatalf("events after queued price match = %+v", events)
	}
	close(release)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	triggers := contextTriggers(t, script.next(t, "queued price trigger"))
	if len(triggers) != 1 || triggers[0]["type"] != "price_above" {
		t.Fatalf("queued trigger = %v", triggers)
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	clock.Advance(time.Second)
	runEventRound(t, tasks)
	if book.requestCount("crypto", "BTCUSDT") != 1 {
		t.Fatalf("timer-only Session caused Backend sampling: %v", book.requests)
	}
}

// The local and price tasks claim in separate transactions (spec §63.4): a timer and a price
// match observed together wake an idle Session in consecutive Runs and share one Run only when
// both queue behind a busy Run. Either way each trigger is delivered exactly once, in claim order.
func TestTicket29TimerAndPriceMatchClaimedSeparatelyAreDeliveredExactlyOnceInClaimOrder(t *testing.T) {
	t.Run("idle Session gets consecutive Runs", func(t *testing.T) {
		fixture := newRuntimeFixture()
		clock := newEventClock()
		book := newPriceBook()
		at := clock.Now().Add(time.Minute)
		script := newLLMScript(
			respondToolCalls(priceEventCall("above", "price_above", "BTCUSDT", 60000), createEventCall("timer", "timer", timerParams(at))),
			respondStop("armed"),
			respondStop("handled timer"),
			respondStop("handled price"),
		)
		fixture.generate = script.handler(t)
		runtime := openEventRuntime(t, fixture, book, "")
		tasks := newEventTasks(runtime, clock)
		session, events := armPriceEvents(t, script, runtime, nil)
		clock.AdvanceTo(t, at)
		book.set("crypto", "BTCUSDT", 60000, clock.Now())
		runEventRound(t, tasks)
		if triggers := contextTriggers(t, script.next(t, "timer wake-up")); len(triggers) != 1 || triggers[0]["event_id"] != events[1].ID {
			t.Fatalf("first Run triggers = %v, want only the timer", triggers)
		}
		if triggers := contextTriggers(t, script.next(t, "price wake-up")); len(triggers) != 1 || triggers[0]["event_id"] != events[0].ID {
			t.Fatalf("second Run triggers = %v, want only the price event", triggers)
		}
		waitForRunStatus(t, runtime, session.ID, 3, "completed")
		script.expectNoRequest(t, "fourth Run")
		if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 3 {
			t.Fatalf("runs = %v, want exactly 3", ids)
		}
	})

	t.Run("busy Session gets one Run with both", func(t *testing.T) {
		fixture := newRuntimeFixture()
		clock := newEventClock()
		book := newPriceBook()
		at := clock.Now().Add(time.Minute)
		release := make(chan struct{})
		script := newLLMScript(
			respondToolCalls(priceEventCall("above", "price_above", "BTCUSDT", 60000), createEventCall("timer", "timer", timerParams(at))),
			holdUntil(release, respondStop("busy done")),
			respondStop("handled both"),
		)
		fixture.generate = script.handler(t)
		runtime := openEventRuntime(t, fixture, book, "")
		tasks := newEventTasks(runtime, clock)
		session := createStoppedSession(t, runtime, nil)
		startSessionForTest(t, runtime, session.ID)
		script.next(t, "decision 1")
		results := toolResults(t, script.next(t, "busy decision 2"))
		above, timer := resultEvent(t, results[0]), resultEvent(t, results[1])
		clock.AdvanceTo(t, at)
		book.set("crypto", "BTCUSDT", 60000, clock.Now())
		runEventRound(t, tasks)
		if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
			t.Fatalf("events after both claims = %+v", events)
		}
		close(release)
		waitForRunStatus(t, runtime, session.ID, 1, "completed")
		triggers := contextTriggers(t, script.next(t, "queued wake-up"))
		if len(triggers) != 2 || triggers[0]["event_id"] != timer.ID || triggers[1]["event_id"] != above.ID {
			t.Fatalf("queued triggers = %v, want the timer claim first and then the price claim", triggers)
		}
		waitForRunStatus(t, runtime, session.ID, 2, "completed")
		script.expectNoRequest(t, "third Run")
		if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 2 {
			t.Fatalf("runs = %v, want exactly 2", ids)
		}
	})
}

// The Event expires while Backend is still answering. The sample is fresh when it arrives, but
// the claim runs after sampling and must see the expiry: the price task neither fires nor
// expires the Event, and the local task writes the single expired trace.
func TestTicket29PriceClaimUsesClockAfterSamplingSoExpiryWins(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	expiresAt := clock.Now().Add(5 * time.Second)
	script := newLLMScript(
		respondToolCalls(toolCall("above", "create_event", map[string]any{
			"type": "price_above", "params": map[string]any{"symbol": "BTCUSDT", "price": 60000},
			"expires_at": expiresAt.Format(time.RFC3339Nano),
		})),
		respondStop("armed"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)
	tasks := newEventTasks(runtime, clock)
	session, events := armPriceEvents(t, script, runtime, nil)

	book.handle("crypto", "BTCUSDT", func(w http.ResponseWriter, _ *http.Request) {
		clock.Advance(expiresAt.Sub(clock.Now()))
		writeFixtureJSON(w, http.StatusOK, quoteBody("crypto", "BTCUSDT", 61000, clock.Now()))
	})
	if err := tasks.price.Run(context.Background()); err != nil {
		t.Fatalf("price round error = %v", err)
	}
	script.expectNoRequest(t, "wake-up from an event that expired during sampling")
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 1 {
		t.Fatalf("price task fired or expired the event itself: %+v", remaining)
	}
	if err := tasks.local.Run(context.Background()); err != nil {
		t.Fatalf("local round error = %v", err)
	}
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 0 {
		t.Fatalf("expired event still active: %+v", remaining)
	}
	if outcomes := eventLogOutcomes(t, databasePath, events[0].ID); len(outcomes) != 1 || outcomes[0] != "expired" {
		t.Fatalf("event log = %v, want exactly one expired trace", outcomes)
	}
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("expired event fired: runs = %v", ids)
	}
}

// Sample validity is judged when Backend's answer arrives; fired_at is the claim instant after
// sampling. Neither uses the clock value read before the round's first request.
func TestTicket29PriceTriggerTimesAndFreshnessUseTheClockAtSampleArrival(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(
			priceEventCall("btc", "price_above", "BTCUSDT", 60000),
			priceEventCall("eth", "price_above", "ETHUSDT", 1000),
			priceEventCall("sol", "price_above", "SOLUSDT", 100),
		),
		respondStop("armed"),
		respondStop("handled btc and eth"),
		respondStop("handled sol"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session, events := armPriceEvents(t, script, runtime, nil)

	// Backend answers 2s after the round started; the sample is fresh when it arrives.
	book.handle("crypto", "BTCUSDT", func(w http.ResponseWriter, _ *http.Request) {
		clock.Advance(2 * time.Second)
		writeFixtureJSON(w, http.StatusOK, quoteBody("crypto", "BTCUSDT", 61000, clock.Now()))
	})
	// A sample stamped at its own arrival is fresh even though the round started 31s earlier.
	book.handle("crypto", "ETHUSDT", func(w http.ResponseWriter, _ *http.Request) {
		clock.Advance(31 * time.Second)
		writeFixtureJSON(w, http.StatusOK, quoteBody("crypto", "ETHUSDT", 1001, clock.Now()))
	})
	// A sample stamped 31s before it arrives is stale at arrival, whatever the round's start clock was.
	book.handle("crypto", "SOLUSDT", func(w http.ResponseWriter, _ *http.Request) {
		stamp := clock.Now()
		clock.Advance(31 * time.Second)
		writeFixtureJSON(w, http.StatusOK, quoteBody("crypto", "SOLUSDT", 101, stamp))
	})
	if err := tasks.price.Run(context.Background()); err != nil {
		t.Fatalf("price round error = %v", err)
	}
	claimAt := clock.Now()
	woke := script.next(t, "wake-up for btc and eth")
	triggers := contextTriggers(t, woke)
	if len(triggers) != 2 || triggers[0]["event_id"] != events[0].ID || triggers[1]["event_id"] != events[1].ID {
		t.Fatalf("triggers = %s", contextText(woke))
	}
	for _, trigger := range triggers {
		firedAt := eventTime(t, trigger["fired_at"].(string))
		updatedAt := eventTime(t, trigger["matched"].(map[string]any)["updated_at"].(string))
		if !firedAt.Equal(claimAt) || firedAt.Before(updatedAt) {
			t.Fatalf("trigger %s fired_at = %s updated_at = %s, want fired_at = claim instant %s", trigger["event_id"], firedAt, updatedAt, claimAt)
		}
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 1 || remaining[0].ID != events[2].ID {
		t.Fatalf("stale-at-arrival sample changed events: %+v", remaining)
	}

	book.handle("crypto", "SOLUSDT", nil)
	clock.Advance(time.Second)
	book.set("crypto", "SOLUSDT", 101, clock.Now())
	if err := tasks.price.Run(context.Background()); err != nil {
		t.Fatalf("price round error = %v", err)
	}
	if triggers := contextTriggers(t, script.next(t, "wake-up for sol")); len(triggers) != 1 || triggers[0]["event_id"] != events[2].ID {
		t.Fatalf("sol triggers = %v", triggers)
	}
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
}

// Without a valid sample the price task has nothing to claim and must not open a transaction that
// touches Event rows; expiry belongs to the local task alone.
func TestTicket29PriceTaskWithoutValidSamplesTouchesNoEventRows(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	expiresAt := clock.Now().Add(5 * time.Minute)
	script := newLLMScript(
		respondToolCalls(toolCall("above", "create_event", map[string]any{
			"type": "price_above", "params": map[string]any{"symbol": "BTCUSDT", "price": 60000},
			"expires_at": expiresAt.Format(time.RFC3339Nano),
		})),
		respondStop("armed"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)
	tasks := newEventTasks(runtime, clock)
	session, events := armPriceEvents(t, script, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_event_delete", `
		BEFORE DELETE ON events
		BEGIN
			SELECT RAISE(FAIL, 'reject event delete');
		END`)

	clock.AdvanceTo(t, expiresAt)
	book.fail("crypto", "BTCUSDT", http.StatusBadGateway)
	if err := tasks.price.Run(context.Background()); err != nil {
		t.Fatalf("price round without samples error = %v", err)
	}
	if err := runtime.HealthError(); err != nil {
		t.Fatalf("price round without samples touched Event rows: %v", err)
	}
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 1 {
		t.Fatalf("events after price round = %+v", remaining)
	}
	dropTrigger(t, databasePath, "reject_event_delete")
	if err := tasks.local.Run(context.Background()); err != nil {
		t.Fatalf("local round error = %v", err)
	}
	if outcomes := eventLogOutcomes(t, databasePath, events[0].ID); len(outcomes) != 1 || outcomes[0] != "expired" {
		t.Fatalf("event log = %v, want exactly one expired trace", outcomes)
	}
	script.expectNoRequest(t, "wake-up from an expired event")
}

// A sample stamped in the future is as broken as a stale one: it must neither fire nor be
// remembered, so genuine later samples are not rejected as out-of-order and a cross baseline
// is never persisted from it. The bound is symmetric and inclusive at 30s.
func TestTicket29FutureDatedSampleIsIgnoredAndDoesNotPoisonBaselines(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(
			priceEventCall("cross", "price_cross_above", "BTCUSDT", 60000),
			priceEventCall("level", "price_above", "ETHUSDT", 1000),
			priceEventCall("bound", "price_above", "SOLUSDT", 100),
		),
		respondStop("armed"),
		respondStop("handled"),
		respondStop("handled"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session, events := armPriceEvents(t, script, runtime, nil)
	cross, level, bound := events[0], events[1], events[2]

	baselineAt := clock.Now()
	book.set("crypto", "BTCUSDT", 59000, baselineAt)
	book.set("crypto", "ETHUSDT", 900, baselineAt)
	book.set("crypto", "SOLUSDT", 101, baselineAt.Add(30*time.Second+time.Nanosecond))
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up from the baseline round")
	if state := eventState(t, runtime, session.ID, cross.ID); state == nil || state.Price != 59000 {
		t.Fatalf("cross baseline = %+v, want 59000", state)
	}

	book.set("crypto", "BTCUSDT", 61000, baselineAt.Add(31*time.Second))
	book.set("crypto", "ETHUSDT", 1001, baselineAt.Add(31*time.Second))
	runEventRound(t, tasks)
	script.expectNoRequest(t, "wake-up from future-dated samples")
	if state := eventState(t, runtime, session.ID, cross.ID); state == nil || state.Price != 59000 || !eventTime(t, state.UpdatedAt).Equal(baselineAt) {
		t.Fatalf("future-dated sample replaced the cross baseline: %+v", state)
	}
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 3 {
		t.Fatalf("future-dated samples fired events: %+v", remaining)
	}

	book.set("crypto", "BTCUSDT", 60000, baselineAt.Add(time.Second))
	book.set("crypto", "ETHUSDT", 1001, baselineAt.Add(time.Second))
	book.set("crypto", "SOLUSDT", 101, baselineAt.Add(30*time.Second))
	runEventRound(t, tasks)
	woke := script.next(t, "wake-up after the future-dated samples were ignored")
	triggers := contextTriggers(t, woke)
	if len(triggers) != 3 || triggers[0]["event_id"] != cross.ID || triggers[1]["event_id"] != level.ID || triggers[2]["event_id"] != bound.ID ||
		triggers[0]["matched"].(map[string]any)["previous_price"] != float64(59000) {
		t.Fatalf("triggers after ignored future samples = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
}

// The two tasks registered by cmd/agent-server run through the real EventLoop: one clock step
// makes the price task sample once, claim the level match and wake Session A with the deciding
// facts while the local task wakes Session B's timer; each Event has exactly one fired trace.
func TestTicket29PriceEventTaskFiresThroughServerEventLoopWithTriggerFacts(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	roundAt := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(priceEventCall("above", "price_above", "BTCUSDT", 60000)),
		respondStop("armed price"),
		respondToolCalls(createEventCall("timer", "timer", timerParams(roundAt))),
		respondStop("armed timer"),
		respondStop("woke"),
		respondStop("woke"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)
	loop := common.NewEventLoopWithClock(clock)
	server, err := common.NewServer(loop, newEventTasks(runtime, clock).all(), nil, nil, time.Second, runtime.Handler())
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
	t.Cleanup(func() { _ = server.Shutdown() })
	priceSession, priceEvents := armPriceEvents(t, script, runtime, map[string]any{"name": "price session", "account_id": "acc_price"})
	timerSession, timerEvents := armPriceEvents(t, script, runtime, map[string]any{"name": "timer session", "account_id": "acc_timer"})

	book.set("crypto", "BTCUSDT", 61000, roundAt)
	clock.Advance(time.Minute)
	woke := map[string]map[string]any{}
	for range 2 {
		request := script.next(t, "resident wake-up")
		woke[runContext(t, request)["session_name"].(string)] = request
	}
	priceTriggers := contextTriggers(t, woke["price session"])
	if len(priceTriggers) != 1 {
		t.Fatalf("price Session context = %s", contextText(woke["price session"]))
	}
	matched, _ := priceTriggers[0]["matched"].(map[string]any)
	if priceTriggers[0]["type"] != "price_above" || priceTriggers[0]["event_id"] != priceEvents[0].ID || priceTriggers[0]["created_run_id"] != float64(1) ||
		matched["price"] != float64(61000) || matched["condition"] != "price >= 60000" ||
		!eventTime(t, matched["updated_at"].(string)).Equal(roundAt) || !eventTime(t, priceTriggers[0]["fired_at"].(string)).Equal(roundAt) {
		t.Fatalf("price trigger = %s", contextText(woke["price session"]))
	}
	if timerTriggers := contextTriggers(t, woke["timer session"]); len(timerTriggers) != 1 || timerTriggers[0]["event_id"] != timerEvents[0].ID {
		t.Fatalf("timer Session context = %s", contextText(woke["timer session"]))
	}
	waitForRunStatus(t, runtime, priceSession.ID, 2, "completed")
	waitForRunStatus(t, runtime, timerSession.ID, 2, "completed")
	if count := book.requestCount("crypto", "BTCUSDT"); count != 1 {
		t.Fatalf("Backend sampled %d times in one round, want 1", count)
	}
	for _, fired := range []common.Event{priceEvents[0], timerEvents[0]} {
		if outcomes := eventLogOutcomes(t, databasePath, fired.ID); len(outcomes) != 1 || outcomes[0] != "fired" {
			t.Fatalf("event %s log = %v, want exactly one fired trace", fired.ID, outcomes)
		}
	}
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := runtime.HealthError(); err != nil {
		t.Fatalf("shutdown latched a persistence fault: %v", err)
	}
}

// A user delete or Session stop that commits while Backend is still answering wins: the claim
// re-reads active rows inside its transaction, so the arriving match finds nothing to fire and
// the Event keeps its single user-initiated terminal trace.
func TestTicket29DeleteOrStopCommittedDuringSamplingNeverFires(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		path    func(sessionID, eventID string) string
		outcome string
	}{
		{name: "user delete", method: http.MethodDelete, outcome: "deleted_by_user",
			path: func(sessionID, eventID string) string { return "/api/v1/sessions/" + sessionID + "/events/" + eventID }},
		{name: "user stop", method: http.MethodPost, outcome: "cleared_by_stop",
			path: func(sessionID, _ string) string { return "/api/v1/sessions/" + sessionID + "/stop" }},
	}
	for _, mutation := range cases {
		t.Run(mutation.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			clock := newEventClock()
			book := newPriceBook()
			script := newLLMScript(
				respondToolCalls(priceEventCall("above", "price_above", "BTCUSDT", 60000)),
				respondStop("armed"),
			)
			fixture.generate = script.handler(t)
			databasePath := filepath.Join(t.TempDir(), "agent.db")
			runtime := openEventRuntime(t, fixture, book, databasePath)
			tasks := newEventTasks(runtime, clock)
			session, events := armPriceEvents(t, script, runtime, nil)

			statuses := make(chan int, 1)
			book.handle("crypto", "BTCUSDT", func(w http.ResponseWriter, _ *http.Request) {
				status, _ := runtimeRequest(t, runtime.Handler(), mutation.method, mutation.path(session.ID, events[0].ID), agentAdminToken, nil)
				statuses <- status
				writeFixtureJSON(w, http.StatusOK, quoteBody("crypto", "BTCUSDT", 61000, clock.Now()))
			})
			if err := tasks.price.Run(context.Background()); err != nil {
				t.Fatalf("price round error = %v", err)
			}
			if status := <-statuses; status != http.StatusOK {
				t.Fatalf("%s during sampling status = %d", mutation.name, status)
			}
			script.expectNoRequest(t, "wake-up after "+mutation.name)
			if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
				t.Fatalf("arriving sample fired after %s: runs = %v", mutation.name, ids)
			}
			if outcomes := eventLogOutcomes(t, databasePath, events[0].ID); len(outcomes) != 1 || outcomes[0] != mutation.outcome {
				t.Fatalf("event log after %s = %v", mutation.name, outcomes)
			}
			if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
				t.Fatalf("events after %s = %+v", mutation.name, events)
			}
		})
	}
}

// A SQLite failure inside the price claim rolls the whole round back: the Event stays active
// without a trace, no Run starts, the fault is latched, and after repair the same sample fires
// the Event exactly once.
func TestTicket29PriceClaimPersistenceFailureKeepsEventAndFiresOnceAfterRepair(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(priceEventCall("above", "price_above", "BTCUSDT", 60000)),
		respondStop("armed"),
		respondStop("recovered"),
		respondStop("woke after repair"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)
	tasks := newEventTasks(runtime, clock)
	session, events := armPriceEvents(t, script, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_price_pending", `
		BEFORE INSERT ON pending_triggers
		BEGIN
			SELECT RAISE(FAIL, 'reject price pending');
		END`)

	book.set("crypto", "BTCUSDT", 61000, clock.Now())
	if err := tasks.price.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "reject price pending") {
		t.Fatalf("price round error = %v, want injected pending failure", err)
	}
	script.expectNoRequest(t, "wake-up despite failed price claim")
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 1 || remaining[0].ID != events[0].ID {
		t.Fatalf("event lost by rolled-back price claim: %+v", remaining)
	}
	if outcomes := eventLogOutcomes(t, databasePath, events[0].ID); len(outcomes) != 0 {
		t.Fatalf("rolled-back claim left a trace: %v", outcomes)
	}
	if err := runtime.HealthError(); err == nil {
		t.Fatal("price claim failure was not latched")
	}
	if err := runtime.Close(); err == nil {
		t.Fatal("Close() should report the latched fault")
	}

	dropTrigger(t, databasePath, "reject_price_pending")
	runtime = openEventRuntime(t, fixture, book, databasePath)
	tasks = newEventTasks(runtime, clock)
	recovery := recoveryContext(t, completeRecoveryRun(t, script, runtime, session.ID, 2))
	if active, _ := recovery["active_events"].([]any); len(active) != 1 || active[0].(map[string]any)["id"] != events[0].ID {
		t.Fatalf("recovery context active events = %v", recovery["active_events"])
	}
	runEventRound(t, tasks)
	if triggers := contextTriggers(t, script.next(t, "wake-up after repair")); len(triggers) != 1 || triggers[0]["event_id"] != events[0].ID {
		t.Fatalf("repaired wake-up triggers = %v", triggers)
	}
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
	runEventRound(t, tasks)
	script.expectNoRequest(t, "duplicate price fire")
	if outcomes := eventLogOutcomes(t, databasePath, events[0].ID); len(outcomes) != 1 || outcomes[0] != "fired" {
		t.Fatalf("event log after repair = %v, want exactly one fired trace", outcomes)
	}
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 3 {
		t.Fatalf("runs after repair = %v", ids)
	}
}
