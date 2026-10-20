package test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "stock-ai/agent-server/internal/agent"
)

// streamGateway answers Generate as the LLM Provider Server does for Accept: application/x-ndjson.
func streamGateway(t *testing.T, w http.ResponseWriter, request *http.Request) func(line any) {
	t.Helper()
	if request.Header.Get("Accept") != "application/x-ndjson" {
		t.Errorf("Generate Accept = %q, want application/x-ndjson", request.Header.Get("Accept"))
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	return func(line any) {
		encoded, _ := json.Marshal(line)
		_, _ = w.Write(append(encoded, '\n'))
		w.(http.Flusher).Flush()
	}
}

func waitForRunStatusWithin(t *testing.T, runtime *agent.AgentRuntime, sessionID string, runID int64, within time.Duration, wanted string) agent.Run {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+sessionID+"/runs/"+jsonNumber(runID), agentAdminToken, nil)
		if status == http.StatusOK {
			if run := decodeBody[agent.Run](t, raw); run.Status == wanted {
				return run
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run %d did not become %s within %s; last = %d %s", runID, wanted, within, status, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func modelCallErrors(t *testing.T, run agent.Run) []string {
	t.Helper()
	var progress []struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(run.Progress, &progress); err != nil {
		t.Fatal(err)
	}
	var errors []string
	for _, entry := range progress {
		if entry.Type == "model_call" {
			errors = append(errors, entry.Error)
		}
	}
	return errors
}

func TestGenerateStreamOutlastsTheRequestTimeoutWhileTheGatewayKeepsPinging(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		send := streamGateway(t, w, request)
		send(map[string]any{"type": "delta", "reasoning": "thinking for a while"})
		for range 8 {
			time.Sleep(300 * time.Millisecond)
			send(map[string]any{"type": "ping"})
		}
		send(map[string]any{"type": "done", "response": finalResponse("slow but done", "slow but done")})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)

	run := waitForRunStatusWithin(t, runtime, session.ID, 1, 10*time.Second, "completed")
	fixture.mu.Lock()
	generateCalls := fixture.generateCalls
	fixture.mu.Unlock()
	if run.ModelCalls != 1 || generateCalls != 1 {
		t.Fatalf("model calls = %d, Generate calls = %d; a 2.4s stream with a 1s RequestTimeout must not retry", run.ModelCalls, generateCalls)
	}
	if run.Output == nil || *run.Output != "slow but done" {
		t.Fatalf("output = %v", run.Output)
	}
}

func TestGenerateStreamThatGoesSilentTimesOutAndIsRetried(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		send := streamGateway(t, w, request)
		if calls.Add(1) == 1 {
			send(map[string]any{"type": "delta", "content": "started"})
			<-request.Context().Done()
			return
		}
		send(map[string]any{"type": "done", "response": finalResponse("second try", "second try")})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)

	run := waitForRunStatusWithin(t, runtime, session.ID, 1, 10*time.Second, "completed")
	errors := modelCallErrors(t, run)
	if len(errors) != 2 || errors[0] != "request canceled or timed out: context deadline exceeded" || errors[1] != "" {
		t.Fatalf("model call errors = %q", errors)
	}
}

func TestGenerateStreamErrorLinesAreClassifiedLikeHTTPErrors(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		send := streamGateway(t, w, request)
		if calls.Add(1) == 1 {
			send(map[string]any{"type": "error", "status": 504, "error": map[string]any{"code": "PROVIDER_TIMEOUT", "message": "Provider request timed out."}})
			return
		}
		send(map[string]any{"type": "error", "status": 502, "error": map[string]any{"code": "AUTHENTICATION_FAILED", "message": "Provider rejected its configured credential."}})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)

	run := waitForRunStatusWithin(t, runtime, session.ID, 1, 5*time.Second, "failed")
	if run.Error == nil || *run.Error != "GATEWAY_ERROR: AUTHENTICATION_FAILED: Provider rejected its configured credential." || run.ModelCalls != 2 {
		t.Fatalf("failed Run = error %v, model calls %d; PROVIDER_TIMEOUT retries once, AUTHENTICATION_FAILED stops", run.Error, run.ModelCalls)
	}
}

type sseEvent struct{ name, data string }

// readRunStream connects to the Run stream and delivers each event; the channel closes with the stream.
func readRunStream(t *testing.T, serverURL, sessionID string, runID int64) <-chan sseEvent {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, serverURL+"/api/v1/sessions/"+sessionID+"/runs/"+jsonNumber(runID)+"/stream", nil)
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
		response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream = %d %q %q", response.StatusCode, response.Header.Get("Content-Type"), response.Header.Get("X-Accel-Buffering"))
	}
	events := make(chan sseEvent, 64)
	go func() {
		defer close(events)
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		var event sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if event.name != "" {
					events <- event
				}
				event = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				event.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				event.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return events
}

func nextEvent(t *testing.T, events <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case event, open := <-events:
		if !open {
			t.Fatal("Run stream closed early")
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("no Run stream event")
	}
	return sseEvent{}
}

func TestRunStreamSendsTheOpenModelCallTextToLateViewersAndEndsWithTheRun(t *testing.T) {
	fixture := newRuntimeFixture()
	firstDelta := make(chan struct{})
	more := make(chan struct{})
	finish := make(chan struct{})
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		send := streamGateway(t, w, request)
		send(map[string]any{"type": "delta", "reasoning": "Check BTC "})
		send(map[string]any{"type": "delta", "content": "Looking"})
		close(firstDelta)
		// A failed test must not leave the Run waiting: shutdown cancels this request.
		select {
		case <-more:
		case <-request.Context().Done():
			return
		}
		send(map[string]any{"type": "delta", "content": " closer"})
		select {
		case <-finish:
		case <-request.Context().Done():
			return
		}
		send(map[string]any{"type": "done", "response": finalResponse("done", "done")})
	}
	runtime, _, _ := fixture.open(t, "")
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitTestSignal(t, firstDelta, "first streamed delta")
	time.Sleep(50 * time.Millisecond)

	events := readRunStream(t, server.URL, session.ID, 1)
	call := nextEvent(t, events)
	var index struct{ Index int }
	if call.name != "call" || json.Unmarshal([]byte(call.data), &index) != nil {
		t.Fatalf("first event = %+v, want the open model call", call)
	}
	if snapshot := nextEvent(t, events); snapshot.name != "delta" || snapshot.data != `{"content":"Looking","reasoning":"Check BTC "}` {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	close(more)
	if delta := nextEvent(t, events); delta.name != "delta" || delta.data != `{"content":" closer"}` {
		t.Fatalf("live delta = %+v", delta)
	}
	close(finish)
	if end := nextEvent(t, events); end.name != "end" {
		t.Fatalf("after the Run = %+v, want end", end)
	}

	run := waitForRunStatusWithin(t, runtime, session.ID, 1, 2*time.Second, "completed")
	var progress []struct {
		Type    string  `json:"type"`
		Content *string `json:"content"`
	}
	_ = json.Unmarshal(run.Progress, &progress)
	if index.Index >= len(progress) || progress[index.Index].Type != "model_call" {
		t.Fatalf("call index %d does not name a model_call in %s", index.Index, run.Progress)
	}
}

func TestRunStreamEndsAtOnceWhenTheRunIsNotRunning(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()
	session := createStoppedSession(t, runtime, nil)

	events := readRunStream(t, server.URL, session.ID, 7)
	if end := nextEvent(t, events); end.name != "end" {
		t.Fatalf("stream of a Run that is not running = %+v", end)
	}
}

func TestRunStreamEndsWhenTheRuntimeShutsDown(t *testing.T) {
	fixture := newRuntimeFixture()
	streaming := make(chan struct{})
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		send := streamGateway(t, w, request)
		send(map[string]any{"type": "delta", "content": "working"})
		close(streaming)
		<-request.Context().Done()
	}
	runtime, _, _ := fixture.open(t, "")
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitTestSignal(t, streaming, "streamed delta")

	events := readRunStream(t, server.URL, session.ID, 1)
	nextEvent(t, events)
	nextEvent(t, events)
	quiesced := make(chan error, 1)
	go func() { quiesced <- runtime.Quiesce() }()
	if end := nextEvent(t, events); end.name != "end" {
		t.Fatalf("stream during shutdown = %+v, want end", end)
	}
	select {
	case <-quiesced:
	case <-time.After(3 * time.Second):
		t.Fatal("Quiesce did not return while a Run stream was open")
	}
}
