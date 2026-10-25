package test

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type panicToolTransport struct{ base http.RoundTripper }

func (p panicToolTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := p.base.RoundTrip(request)
	if err == nil && request.URL.Path == "/v1/market/price" {
		response.Body = panicToolBody{response.Body}
	}
	return response, err
}

type panicToolBody struct{ io.ReadCloser }

func (panicToolBody) Read([]byte) (int, error) { panic("synthetic-panic-secret") }

func TestRunPanicIsolation(t *testing.T) {
	if os.Getenv("TEST_RUN_PANIC_CHILD") != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunPanicIsolation$", "-test.timeout=20s")
		command.Env = append(os.Environ(), "TEST_RUN_PANIC_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("Run panic escaped process isolation: %v\n%s", err, output)
		}
		return
	}
	base := http.DefaultTransport
	http.DefaultTransport = panicToolTransport{base}
	t.Cleanup(func() { http.DefaultTransport = base })
	fixture := newRuntimeFixture()
	var logs lockedLog
	fixture.logger = log.New(&logs, "", 0)
	clock := newEventClock()
	releasePanic, releaseOther := make(chan struct{}), make(chan struct{})
	script := newLLMScript(
		respondToolCalls(createEventCall("pending", "timer", timerParams(clock.Now().Add(time.Minute)))),
		holdUntil(releasePanic, respondToolCalls(toolCall("panic", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"}))),
		holdUntil(releaseOther, respondStop("other Session survived")),
		respondStop("pending trigger survived"),
	)
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	other := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_other"})
	startSessionForTest(t, runtime, session.ID)
	script.next(t, "arm pending trigger")
	script.next(t, "before tool panic")
	startSessionForTest(t, runtime, other.ID)
	script.next(t, "other Session concurrently running")
	clock.Advance(time.Minute)
	if err := runtime.LocalEventTask(clock, time.Second).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(releasePanic)
	failed := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if failed.Error == nil || !strings.HasPrefix(*failed.Error, "RUN_PANIC:") || failed.Summary != nil {
		t.Fatalf("panic Run = %+v", failed)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/runs/1", agentAdminToken, nil)
	if status != http.StatusOK || decodeMap(t, raw)["error_code"] != "RUN_PANIC" {
		t.Fatalf("panic error_code = %d %s", status, raw)
	}
	if trigger := runContext(t, script.next(t, "pending successor"))["trigger"]; trigger != "event" {
		t.Fatalf("successor trigger = %v", trigger)
	}
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	close(releaseOther)
	waitForRunStatus(t, runtime, other.ID, 1, "completed")
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, want := range []string{"event=run_panic session_id=" + session.ID + " run_id=1", "goroutine ", "internal/agent/loop.go", "panicToolBody.Read"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing panic evidence %q in log: %s", want, output)
		}
	}
	for _, secret := range []string{"synthetic-panic-secret", agentAdminToken, backendUserToken, gatewayToken, "account-token-v1"} {
		if strings.Contains(output, secret) || strings.Contains(string(raw), secret) {
			t.Fatalf("panic exposed secret %q", secret)
		}
	}
}
