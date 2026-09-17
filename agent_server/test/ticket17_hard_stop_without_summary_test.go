package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket17HardStopCancelsWaitingModelAndPreservesInterruptedFactsWithoutSummary(t *testing.T) {
	fixture := newRuntimeFixture()
	modelStarted := make(chan struct{})
	releaseModel := make(chan struct{})
	var once sync.Once
	fixture.generate = func(_ http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(modelStarted) })
		<-releaseModel
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil); status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitSignal(t, modelStarted, "model request")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop status = %d body = %s", status, raw)
	}
	close(releaseModel)
	run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	if run.Summary != nil || run.Error == nil || !strings.Contains(*run.Error, "HARD_STOP") || run.ModelCalls != 1 {
		t.Fatalf("interrupted Run = %+v", run)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	stopped := decodeBody[common.Session](t, raw)
	if status != http.StatusOK || stopped.Status != "stopped" || !strings.Contains(string(stopped.RestartContext), `"status":"interrupted"`) {
		t.Fatalf("stopped Session = %d %s", status, raw)
	}
	fixture.mu.Lock()
	calls := fixture.generateCalls
	fixture.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Gateway calls after stop = %d, want no summary call", calls)
	}
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil); status != http.StatusOK {
		t.Fatalf("repeated stop status = %d, want 200", status)
	}
}

func TestTicket17HardStopDuringToolAndNextDecisionPreservesCompletedProgress(t *testing.T) {
	for _, stopAt := range []string{"tool", "next_model"} {
		t.Run(stopAt, func(t *testing.T) {
			fixture := newRuntimeFixture()
			toolStarted := make(chan struct{})
			nextModelStarted := make(chan struct{})
			releaseBlocked := make(chan struct{})
			fixture.market = func(w http.ResponseWriter, request *http.Request) {
				if stopAt == "tool" {
					close(toolStarted)
					select {
					case <-request.Context().Done():
					case <-releaseBlocked:
					}
					return
				}
				writeFixtureJSON(w, http.StatusOK, map[string]any{"market": "crypto", "symbol": "BTCUSDT", "price": 60000})
			}
			var round int
			var roundMu sync.Mutex
			fixture.generate = func(w http.ResponseWriter, request *http.Request) {
				roundMu.Lock()
				round++
				current := round
				roundMu.Unlock()
				if current == 1 {
					writeToolCall(w, "call-price", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
					return
				}
				close(nextModelStarted)
				select {
				case <-request.Context().Done():
				case <-releaseBlocked:
				}
			}
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, nil)
			status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
				"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			if status != http.StatusAccepted {
				t.Fatalf("start status = %d", status)
			}
			if stopAt == "tool" {
				waitSignal(t, toolStarted, "tool request")
			} else {
				waitSignal(t, nextModelStarted, "next model request")
			}
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
				"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
			if status != http.StatusOK {
				t.Fatalf("stop status = %d body = %s", status, raw)
			}
			close(releaseBlocked)
			run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
			if run.Summary != nil || run.ToolCalls != 1 || (stopAt == "next_model" && run.ModelCalls != 2) {
				t.Fatalf("interrupted progress = %+v", run)
			}
			var progress []map[string]any
			if err := json.Unmarshal(run.Progress, &progress); err != nil || len(progress) < 2 {
				t.Fatalf("saved progress = %s error=%v", run.Progress, err)
			}
			toolStep := progress[1]
			arguments, _ := toolStep["arguments"].(map[string]any)
			if toolStep["tool_call_id"] != "call-price" || arguments["symbol"] != "BTCUSDT" {
				t.Fatalf("interrupted Tool facts = %v", toolStep)
			}
			fixture.mu.Lock()
			calls := fixture.generateCalls
			fixture.mu.Unlock()
			wantCalls := 1
			if stopAt == "next_model" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("Gateway calls = %d, want %d and no stop summary", calls, wantCalls)
			}
		})
	}
}

func TestTicket17LateCanceledResponseCannotOverwriteInterruptedTerminalState(t *testing.T) {
	fixture := newRuntimeFixture()
	started := make(chan struct{})
	releaseLateResponse := make(chan struct{})
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-releaseLateResponse
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("too late", "too late", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	waitSignal(t, started, "late model request")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop status = %d body = %s", status, raw)
	}
	close(releaseLateResponse)
	run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	if run.Status != "interrupted" || run.Summary != nil || run.Output != nil {
		t.Fatalf("late response overwrote Run = %+v", run)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("%s did not occur", name)
	}
}
