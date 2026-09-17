package test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type runWithTriggers struct {
	RunID    int64            `json:"run_id"`
	Status   string           `json:"status"`
	Trigger  string           `json:"trigger"`
	Triggers []map[string]any `json:"triggers"`
}

func getRunTriggers(t *testing.T, runtime *common.AgentRuntime, sessionID string, runID int64) runWithTriggers {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+sessionID+"/runs/"+jsonNumber(runID), agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("get Run %d status = %d body = %s", runID, status, raw)
	}
	return decodeBody[runWithTriggers](t, raw)
}

func TestTicket27TimerWakesIdleSessionOnceAtAbsoluteUTCTime(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(createEventCall("call-timer", "timer", map[string]any{"at": at.In(time.FixedZone("plus8", 8*3600)).Format(time.RFC3339Nano)})),
		respondStop("armed timer"),
		respondStop("woke up"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	tasks := newEventTasks(runtime, clock)
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)

	script.next(t, "decision 1")
	created := resultEvent(t, lastToolResult(t, script.next(t, "decision 2")))
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if created.Type != "timer" || !strings.HasPrefix(created.ID, "ev_") {
		t.Fatalf("created event = %+v", created)
	}
	listed := listEventsHTTP(t, runtime, session.ID)
	if len(listed) != 1 || listed[0].ID != created.ID || listed[0].SessionID != session.ID || listed[0].CreatedRunID != 1 {
		t.Fatalf("listed events = %+v", listed)
	}
	persistedEvent := listed[0]
	var params struct {
		At string `json:"at"`
	}
	_ = json.Unmarshal(persistedEvent.Params, &params)
	if !eventTime(t, params.At).Equal(at) || !strings.HasSuffix(params.At, "Z") {
		t.Fatalf("timer at %q was not normalized to UTC %s", params.At, at)
	}
	if !eventTime(t, persistedEvent.ExpiresAt).Equal(at.Add(time.Hour)) {
		t.Fatalf("default timer expiry = %s, want at + 1h", persistedEvent.ExpiresAt)
	}

	clock.AdvanceTo(t, at.Add(-time.Nanosecond))
	runEventRound(t, tasks)
	script.expectNoRequest(t, "premature wake-up")
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 1 {
		t.Fatalf("timer fired before its time: %+v", remaining)
	}

	clock.AdvanceTo(t, at)
	runEventRound(t, tasks)
	woke := script.next(t, "event Run decision")
	assertInitialContextBasics(t, woke, 4, 3)
	triggers := contextTriggers(t, woke)
	if runContext(t, woke)["trigger"] != "event" || len(triggers) != 1 || triggers[0]["type"] != "timer" ||
		triggers[0]["event_id"] != created.ID || triggers[0]["created_run_id"] != float64(1) ||
		!eventTime(t, triggers[0]["fired_at"].(string)).Equal(at) {
		t.Fatalf("event Run context = %s", contextText(woke))
	}
	run := waitForRunStatus(t, runtime, session.ID, 2, "completed")
	persisted := getRunTriggers(t, runtime, session.ID, 2)
	if run.Trigger != "event" || len(persisted.Triggers) != 1 || persisted.Triggers[0]["event_id"] != created.ID {
		t.Fatalf("persisted event Run = %+v triggers = %+v", run, persisted.Triggers)
	}
	if remaining := listEventsHTTP(t, runtime, session.ID); len(remaining) != 0 {
		t.Fatalf("fired timer is still active: %+v", remaining)
	}

	clock.Advance(time.Hour)
	runEventRound(t, tasks)
	script.expectNoRequest(t, "second wake-up")
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 2 {
		t.Fatalf("timer fired more than once: runs = %v", ids)
	}
}

func TestTicket27ResidentEventTaskFiresTimerThroughServerEventLoop(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	at := clock.Now().Add(time.Minute)
	script := newLLMScript(
		respondToolCalls(createEventCall("call-timer", "timer", timerParams(at))),
		respondStop("armed timer"),
		respondStop("woke up"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
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

	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "decision 1")
	script.next(t, "decision 2")
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	for clock.Now().Before(at) {
		clock.Advance(time.Second)
	}
	woke := script.next(t, "resident task wake-up")
	if len(contextTriggers(t, woke)) != 1 {
		t.Fatalf("resident task context = %s", contextText(woke))
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTicket27LocalEventsProgressWhilePriceSamplingIsBlocked(t *testing.T) {
	fixture := newRuntimeFixture()
	clock := newEventClock()
	book := newPriceBook()
	base := clock.Now()
	priceExpires := base.Add(90 * time.Second)
	firstTimerAt := base.Add(time.Minute)
	secondTimerAt := base.Add(2 * time.Minute)
	script := newLLMScript(
		respondToolCalls(toolCall("price", "create_event", map[string]any{
			"type": "price_above", "params": map[string]any{"symbol": "BTCUSDT", "price": 60000},
			"expires_at": priceExpires.Format(time.RFC3339Nano),
		})),
		respondStop("price armed"),
		respondToolCalls(
			createEventCall("timer-1", "timer", timerParams(firstTimerAt)),
			createEventCall("timer-2", "timer", timerParams(secondTimerAt)),
		),
		respondStop("timers armed"),
		respondStop("first timer handled"),
		respondStop("second timer handled"),
	)
	fixture.generate = script.handler(t)
	// Only Server shutdown may end the blocked request: the per-request timeout is kept out of reach.
	fixture.toolTimeout = time.Minute
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime := openEventRuntime(t, fixture, book, databasePath)

	priceSession := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_price"})
	startSessionForTest(t, runtime, priceSession.ID)
	script.next(t, "price create")
	priceEvent := resultEvent(t, lastToolResult(t, script.next(t, "price armed")))
	waitForRunStatus(t, runtime, priceSession.ID, 1, "completed")

	timerSession := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_timer"})
	startSessionForTest(t, runtime, timerSession.ID)
	script.next(t, "timer create")
	timerResults := toolResults(t, script.next(t, "timers armed"))
	firstTimer := resultEvent(t, timerResults[0])
	secondTimer := resultEvent(t, timerResults[1])
	waitForRunStatus(t, runtime, timerSession.ID, 1, "completed")

	priceStarted := make(chan struct{})
	priceCanceled := make(chan struct{})
	var startedOnce, canceledOnce sync.Once
	book.handle("crypto", "BTCUSDT", func(_ http.ResponseWriter, request *http.Request) {
		startedOnce.Do(func() { close(priceStarted) })
		<-request.Context().Done()
		canceledOnce.Do(func() { close(priceCanceled) })
	})
	localTask := runtime.LocalEventTask(clock, 30*time.Second)
	priceTask := runtime.PriceEventTask(clock, 30*time.Second)
	loop := common.NewEventLoopWithClock(clock)
	server, err := common.NewServer(loop, []common.BackgroundTask{localTask, priceTask}, nil, nil, time.Second, runtime.Handler())
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

	clock.Advance(30 * time.Second)
	waitTestSignal(t, priceStarted, "blocked price request")
	clock.WaitUntilArmedAt(t, base.Add(time.Minute))
	clock.Advance(30 * time.Second)
	firstWake := script.next(t, "first timer while price is blocked")
	if triggers := contextTriggers(t, firstWake); len(triggers) != 1 || triggers[0]["event_id"] != firstTimer.ID {
		t.Fatalf("first timer triggers = %v", triggers)
	}
	waitForRunStatus(t, runtime, timerSession.ID, 2, "completed")

	clock.WaitUntilArmedAt(t, base.Add(90*time.Second))
	clock.Advance(30 * time.Second)
	clock.WaitUntilArmedAt(t, base.Add(2*time.Minute))
	if events := listEventsHTTP(t, runtime, priceSession.ID); len(events) != 0 {
		t.Fatalf("expired price event remains active: %+v", events)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+priceSession.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop price Session = %d %s", status, raw)
	}

	clock.Advance(30 * time.Second)
	secondWake := script.next(t, "second timer after other Session stopped")
	if triggers := contextTriggers(t, secondWake); len(triggers) != 1 || triggers[0]["event_id"] != secondTimer.ID {
		t.Fatalf("second timer triggers = %v", triggers)
	}
	waitForRunStatus(t, runtime, timerSession.ID, 3, "completed")
	if ids := startedRunIDs(t, runtime, timerSession.ID); len(ids) != 3 {
		t.Fatalf("timer Event was claimed more than once: %v", ids)
	}

	select {
	case <-priceCanceled:
		t.Fatal("blocked price request ended before Server shutdown")
	default:
	}
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitTestSignal(t, priceCanceled, "price request cancellation by shutdown")
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	for _, task := range []common.BackgroundTask{localTask, priceTask} {
		if err := loop.Wait(waitContext, task.Name); !errors.Is(err, common.ErrLoopStopped) {
			t.Fatalf("Wait(%s) = %v, want ErrLoopStopped", task.Name, err)
		}
	}
	if err := runtime.HealthError(); err != nil {
		t.Fatalf("shutdown cancellation latched persistence fault: %v", err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open Event log database: %v", err)
	}
	defer db.Close()
	for _, eventID := range []string{priceEvent.ID, firstTimer.ID, secondTimer.ID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE event_id = ?`, eventID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("event %s terminal log count = %d error = %v", eventID, count, err)
		}
	}
}
