package test

import (
	"database/sql"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func changeEventCall(id, symbol string, basePrice float64, direction string, pct float64) map[string]any {
	return createEventCall(id, "price_change_pct", map[string]any{
		"symbol": symbol, "base_price": basePrice, "direction": direction, "pct": pct,
	})
}

func TestTicket31PriceChangePctUsesPersistedBaseAndInclusiveThresholdInBothDirections(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(
			changeEventCall("up", "BTCUSDT", 50000, "up", 5),
			changeEventCall("down", "ETHUSDT", 2000, "down", 5),
			changeEventCall("zero-base", "BTCUSDT", 0, "up", 5),
			changeEventCall("negative-base", "BTCUSDT", -1, "up", 5),
			changeEventCall("zero-pct", "BTCUSDT", 50000, "up", 0),
			changeEventCall("bad-direction", "BTCUSDT", 50000, "sideways", 5),
			toolCall("string-base", "create_event", map[string]any{"type": "price_change_pct", "params": map[string]any{"symbol": "BTCUSDT", "base_price": "50000", "direction": "up", "pct": 5}}),
		),
		respondStop("armed"),
		respondStop("recovered"),
		respondStop("handled up"),
		respondStop("handled down"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)
	session := createStoppedSession(t, runtime, map[string]any{"max_tool_call": 10})
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	results := toolResults(t, script.next(t, "decision 2"))
	for _, invalid := range results[2:] {
		if toolErrorCode(invalid) != "INVALID_TOOL_ARGUMENTS" {
			t.Fatalf("invalid change params accepted: %v", invalid)
		}
	}
	up, down := resultEvent(t, results[0]), resultEvent(t, results[1])
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	feed := func(tasks eventTasks, btc, eth float64) {
		t.Helper()
		clock.Advance(time.Second)
		book.set("crypto", "BTCUSDT", btc, clock.Now())
		book.set("crypto", "ETHUSDT", eth, clock.Now())
		runEventRound(t, tasks)
	}
	tasks := newEventTasks(runtime, clock)
	feed(tasks, 52499.99, 1900.01)
	feed(tasks, 47500, 2100)
	script.expectNoRequest(t, "wake-up below the percentage threshold")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 2 {
		t.Fatalf("change events changed before threshold: %+v", events)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	runtime = openEventRuntime(t, fixture, book, databasePath)
	tasks = newEventTasks(runtime, clock)
	completeRecoveryRun(t, script, runtime, session.ID, 2)
	feed(tasks, 52500, 1900)
	woke := script.next(t, "percentage wake-up after reload")
	triggers := contextTriggers(t, woke)
	if len(triggers) != 2 {
		t.Fatalf("change triggers = %s", contextText(woke))
	}
	upMatch, _ := triggers[0]["matched"].(map[string]any)
	downMatch, _ := triggers[1]["matched"].(map[string]any)
	if triggers[0]["event_id"] != up.ID || upMatch["base_price"] != float64(50000) || upMatch["price"] != float64(52500) ||
		upMatch["change_pct"] != float64(5) || upMatch["condition"] != "change_pct >= 5" ||
		triggers[1]["event_id"] != down.ID || downMatch["change_pct"] != float64(-5) || downMatch["condition"] != "change_pct <= -5" {
		t.Fatalf("change facts = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("fired change events still active: %+v", events)
	}
}

func eventLogOutcomes(t *testing.T, databasePath, eventID string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open log database: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT outcome FROM event_log WHERE event_id = ? ORDER BY id`, eventID)
	if err != nil {
		t.Fatalf("query event_log: %v", err)
	}
	defer rows.Close()
	outcomes := make([]string, 0)
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			t.Fatalf("scan event_log: %v", err)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func TestTicket31ResidentTaskExpiresEventsExactlyOnceWithTraceAndWithoutTouchingOtherData(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	expiresAt := clock.Now().Add(5 * time.Minute)
	script := newLLMScript(
		respondToolCalls(
			toolCall("above", "create_event", map[string]any{
				"type": "price_above", "params": map[string]any{"symbol": "BTCUSDT", "price": 60000},
				"expires_at": expiresAt.Format(time.RFC3339Nano),
			}),
			toolCall("timer", "create_event", map[string]any{
				"type": "timer", "params": timerParams(expiresAt.Add(-time.Second)),
				"expires_at": expiresAt.Format(time.RFC3339Nano),
			}),
			priceEventCall("keep", "price_below", "BTCUSDT", 1),
		),
		respondStop("armed"),
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
	session, events := armPriceEvents(t, script, runtime, nil)
	above, timer, keep := events[0], events[1], events[2]

	// Rounds only ever observe whole-minute clock values, so the timer (due 1s before expiry)
	// is first seen when it is already expired: the shared expiry rule wins over firing.
	for clock.Now().Before(expiresAt) {
		clock.Advance(time.Minute)
	}
	remaining := waitForEventCount(t, runtime, session.ID, 1)
	if remaining[0].ID != keep.ID {
		t.Fatalf("resident cleanup removed the wrong event: %+v", remaining)
	}
	sampled := book.requestCount("crypto", "BTCUSDT")
	clock.Advance(time.Minute)
	book.set("crypto", "BTCUSDT", 61000, clock.Now())
	clock.Advance(time.Minute)
	waitForSampleCount(t, book, "crypto", "BTCUSDT", sampled+1)
	script.expectNoRequest(t, "wake-up from expired events")
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("expired events created Runs: %v", ids)
	}
	for _, expired := range []common.Event{above, timer} {
		if outcomes := eventLogOutcomes(t, databasePath, expired.ID); len(outcomes) != 1 || outcomes[0] != "expired" {
			t.Fatalf("event %s log = %v, want exactly one expired trace", expired.ID, outcomes)
		}
	}
	if outcomes := eventLogOutcomes(t, databasePath, keep.ID); len(outcomes) != 0 {
		t.Fatalf("unexpired event has trace %v", outcomes)
	}
	if run := getRunForTest(t, runtime, session.ID, 1); run.Status != "completed" || run.Summary == nil {
		t.Fatalf("cleanup changed Run 1: %+v", run)
	}
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTicket31StoppingOneSessionKeepsOtherSessionsUpdatingAndKeepsShutdownRecoverable(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(priceEventCall("a", "price_above", "BTCUSDT", 60000)),
		respondStop("armed A"),
		respondToolCalls(priceEventCall("b", "price_above", "BTCUSDT", 60000), createEventCall("timer", "timer", timerParams(clock.Now().Add(time.Hour)))),
		respondStop("armed B"),
		respondStop("B woke"),
	)
	fixture.generate = script.handler(t)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)
	tasks := newEventTasks(runtime, clock)
	sessionA, eventsA := armPriceEvents(t, script, runtime, nil)
	sessionB, eventsB := armPriceEvents(t, script, runtime, map[string]any{"account_id": "acc_b"})

	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+sessionA.ID+"/stop", agentAdminToken, nil); status != http.StatusOK {
		t.Fatalf("stop A status = %d", status)
	}
	if outcomes := eventLogOutcomes(t, databasePath, eventsA[0].ID); len(outcomes) != 1 || outcomes[0] != "cleared_by_stop" {
		t.Fatalf("stop trace = %v", outcomes)
	}
	book.set("crypto", "BTCUSDT", 60000, clock.Now())
	runEventRound(t, tasks)
	if len(contextTriggers(t, script.next(t, "Session B wake-up after A stopped"))) != 1 {
		t.Fatal("Session B did not wake")
	}
	waitForRunStatus(t, runtime, sessionB.ID, 2, "completed")
	if outcomes := eventLogOutcomes(t, databasePath, eventsB[0].ID); len(outcomes) != 1 || outcomes[0] != "fired" {
		t.Fatalf("fired trace = %v", outcomes)
	}
	if ids := startedRunIDs(t, runtime, sessionA.ID); len(ids) != 1 {
		t.Fatalf("stopped Session A woke: %v", ids)
	}

	// Runtime shutdown is not a user stop: Session B's timer must survive for recovery.
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	runtime = openEventRuntime(t, fixture, book, databasePath)
	if events := listEventsHTTP(t, runtime, sessionB.ID); len(events) != 1 || events[0].ID != eventsB[1].ID {
		t.Fatalf("shutdown cleared recoverable events: %+v", events)
	}
	if outcomes := eventLogOutcomes(t, databasePath, eventsB[1].ID); len(outcomes) != 0 {
		t.Fatalf("shutdown wrote a terminal trace: %v", outcomes)
	}
}
