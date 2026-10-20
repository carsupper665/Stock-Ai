package test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"stock-ai/agent-server/internal/eventloop"
	"stock-ai/agent-server/internal/server"
)

// lockedLog collects log lines written concurrently by Run, task, and HTTP goroutines.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestPriceSamplingFailureRecoveryAndEventLifecycleAreLogged(t *testing.T) {
	fixture := newRuntimeFixture()
	var logs lockedLog
	fixture.logger = log.New(&logs, "", 0)
	clock := newEventClock()
	book := newPriceBook()
	script := newLLMScript(
		respondToolCalls(priceEventCall("above", "price_above", "BTCUSDT", 60000)),
		respondStop("armed"),
		respondStop("handled above"),
	)
	fixture.generate = script.handler(t)
	runtime := openEventRuntime(t, fixture, book, "")
	tasks := newEventTasks(runtime, clock)
	session, armed := armPriceEvents(t, script, runtime, nil)

	book.fail("crypto", "BTCUSDT", http.StatusUnauthorized)
	runEventRound(t, tasks)
	runEventRound(t, tasks)
	clock.Advance(time.Second)
	book.set("crypto", "BTCUSDT", 60000, clock.Now())
	runEventRound(t, tasks)
	script.next(t, "wake-up after sampling recovered")
	waitForRunStatus(t, runtime, session.ID, 2, "completed")

	out := logs.String()
	if count := strings.Count(out, "event=price_sample_error instrument=crypto/BTCUSDT detail=backend_status_401"); count != 1 {
		t.Fatalf("repeated sampling failure logged %d times, want once:\n%s", count, out)
	}
	for _, want := range []string{
		"event=run_start session_id=" + session.ID + " run_id=1 detail=session_start",
		"event=event_created session_id=" + session.ID + " run_id=1 detail=price_above." + armed[0].ID,
		"event=price_sample_recovered instrument=crypto/BTCUSDT detail=ok",
		"event=event_fired session_id=" + session.ID + " run_id=1 detail=price_above." + armed[0].ID,
		"event=run_start session_id=" + session.ID + " run_id=2 detail=event",
		"event=run_terminal session_id=" + session.ID + " run_id=2 detail=completed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestServerLogsRequestsAndRepeatedTaskErrorsOnce(t *testing.T) {
	var logs lockedLog
	loop, err := eventloop.NewEventLoop()
	if err != nil {
		t.Fatal(err)
	}
	var rounds sync.WaitGroup
	rounds.Add(3)
	var calls int
	task := server.BackgroundTask{Name: "failing-task", Interval: 5 * time.Millisecond, Run: func(context.Context) error {
		calls++
		if calls <= 3 {
			defer rounds.Done()
		}
		return errors.New("backend unreachable")
	}}
	srv, err := server.NewServer(loop, []server.BackgroundTask{task}, nil, log.New(&logs, "", 0), time.Second,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(listener); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	rounds.Wait()
	for _, path := range []string{"/health", "/api/v1/sessions?token=query-secret"} {
		response, err := http.Get("http://" + listener.Addr().String() + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}

	out := logs.String()
	if count := strings.Count(out, "event=background_task_error task=failing-task detail=backend unreachable"); count != 1 {
		t.Fatalf("repeated task error logged %d times, want once:\n%s", count, out)
	}
	if !strings.Contains(out, "event=http_request method=GET path=/api/v1/sessions status=418") {
		t.Fatalf("request was not logged:\n%s", out)
	}
	if strings.Contains(out, "query-secret") || strings.Contains(out, "path=/health") {
		t.Fatalf("access log leaked a query string or logged a passing health probe:\n%s", out)
	}
}
