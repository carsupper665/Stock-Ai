package test

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPlanAgentContinuationDisclosureAuditAndDiagnostics(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	var mu sync.Mutex
	requests := make([]map[string]any, 0, 2)
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		if calls.Add(1) == 1 {
			writeToolCall(w, "price", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
			return
		}
		response := finalResponse("done", "done", nil, nil)
		response["session_mode"] = "continued"
		response["fallback_reason"] = "none"
		response["invocation_count"] = 1
		writeFixtureJSON(w, http.StatusOK, response)
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")

	mu.Lock()
	got := append([]map[string]any(nil), requests...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("Generate requests = %d", len(got))
	}
	firstMessages := got[0]["messages"].([]any)
	system := firstMessages[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "available tool names are exactly") || !strings.Contains(system, "get_market_snapshot") || !strings.Contains(system, "place_order") {
		t.Fatalf("tool disclosure missing from system prompt: %s", system)
	}
	continuation, ok := got[1]["continuation"].(map[string]any)
	if !ok || continuation["after_messages"] != float64(2) {
		t.Fatalf("continuation = %#v", got[1]["continuation"])
	}
	delta := continuation["messages"].([]any)
	if len(delta) != 1 || delta[0].(map[string]any)["role"] != "tool" {
		t.Fatalf("continuation delta = %#v", delta)
	}
	if len(got[1]["messages"].([]any)) != 4 {
		t.Fatalf("full fallback messages were not retained: %#v", got[1]["messages"])
	}
	var initial map[string]any
	if err := json.Unmarshal(run.InitialContext, &initial); err != nil {
		t.Fatalf("initial context audit = %s: %v", run.InitialContext, err)
	}
	tools, _ := initial["available_tools"].([]any)
	if len(tools) < 10 {
		t.Fatalf("available tool audit = %#v", tools)
	}
	if !strings.Contains(string(run.Progress), `"session_mode":"continued"`) ||
		!strings.Contains(string(run.Progress), `"invocation_count":1`) ||
		!strings.Contains(string(run.Progress), `"request_bytes":`) {
		t.Fatalf("Generate diagnostics missing from trace: %s", run.Progress)
	}
	status, history := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/runs/1/history", agentAdminToken, nil)
	if status != http.StatusOK || !strings.Contains(string(history), `"initial_context":{"account_id"`) {
		t.Fatalf("History initial context = %d %s", status, history)
	}
}

func TestPlanAgentTransientContinuationFailureRetriesWithFullMessages(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	var sawContinuation, retryHadContinuation bool
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			writeToolCall(w, "price", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
		case 2:
			sawContinuation = len(body["continuation"]) != 0
			writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "temporary"}})
		default:
			retryHadContinuation = len(body["continuation"]) != 0
			var messages []any
			_ = json.Unmarshal(body["messages"], &messages)
			if len(messages) != 4 {
				t.Errorf("retry full messages = %d", len(messages))
			}
			writeFixtureJSON(w, http.StatusOK, finalResponse("done", "done", nil, nil))
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if !sawContinuation || retryHadContinuation || run.ModelCalls != 3 {
		t.Fatalf("continuation retry: first=%v retry=%v Run=%+v", sawContinuation, retryHadContinuation, run)
	}
}

func TestPlanAgentTerminalAttemptsAreIndependentAndMonotonic(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1, 2:
			writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "temporary"}})
		case 3, 4:
			writeFixtureJSON(w, http.StatusOK, map[string]any{"content": "invalid", "tool_calls": []any{}, "finish_reason": "stop", "usage": nil})
		case 5:
			writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "temporary"}})
		default:
			writeFixtureJSON(w, http.StatusOK, finalResponse("fourth works", "fourth works", nil, nil))
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 1})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ModelCalls != 6 || run.Output == nil || *run.Output != "fourth works" {
		t.Fatalf("terminal allowance Run = %+v calls=%d", run, calls.Load())
	}
	var progress []map[string]any
	if err := json.Unmarshal(run.Progress, &progress); err != nil {
		t.Fatal(err)
	}
	attempts := make([]int, 0, 4)
	for _, entry := range progress {
		if entry["type"] == "model_call" && entry["attempt_type"] == "terminal" {
			attempts = append(attempts, int(entry["attempt"].(float64)))
		}
	}
	if len(attempts) != 4 || attempts[0] != 1 || attempts[1] != 2 || attempts[2] != 3 || attempts[3] != 4 {
		t.Fatalf("terminal attempts = %v trace=%s", attempts, run.Progress)
	}
}

func TestPlanAgentTerminalFourthInvalidAttemptExhaustsAllowance(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": "invalid", "tool_calls": []any{}, "finish_reason": "stop", "usage": nil})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 1})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if calls.Load() != 4 || run.ModelCalls != 4 || run.Error == nil || !strings.Contains(*run.Error, "FINALIZATION_INVALID") {
		t.Fatalf("terminal exhaustion calls=%d Run=%+v", calls.Load(), run)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		if !strings.Contains(string(run.Progress), `"attempt":`+jsonNumber(int64(attempt))) {
			t.Fatalf("terminal attempt %d missing: %s", attempt, run.Progress)
		}
	}
}

func TestPlanAgentHarnessSetupFailuresAreNotRetried(t *testing.T) {
	for _, code := range []string{"HARNESS_LOGIN_REQUIRED", "HARNESS_VERSION_MISMATCH", "HARNESS_NOT_CONFIGURED", "PROVIDER_INVALID_CONFIG"} {
		t.Run(code, func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": code, "message": "setup required"}})
			}
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, nil)
			startSessionForTest(t, runtime, session.ID)
			run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
			fixture.mu.Lock()
			calls := fixture.generateCalls
			fixture.mu.Unlock()
			if calls != 1 || run.ModelCalls != 1 || run.Error == nil || !strings.Contains(*run.Error, code) {
				t.Fatalf("nonretryable setup error calls=%d Run=%+v", calls, run)
			}
		})
	}
}

func TestPlanAgentOperationalLogsExcludeGatewayDetails(t *testing.T) {
	const secret = "raw-cli-secret-sentinel"
	fixture := newRuntimeFixture()
	var logs bytes.Buffer
	fixture.logger = log.New(&logs, "", 0)
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
			"code": "AUTHENTICATION_FAILED", "message": "login failed: " + secret,
		}})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), "AUTHENTICATION_FAILED") {
		t.Fatalf("unsafe or missing operational log: %s", logs.String())
	}
}

func TestPlanAgentRunHistoryReadDoesNotRepersistNestedHistory(t *testing.T) {
	fixture := newRuntimeFixture()
	largeOutput := strings.Repeat("history-output-", 8000)
	var mu sync.Mutex
	runCalls := map[string]int{}
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		mu.Lock()
		runCalls[body.ConversationID]++
		call := runCalls[body.ConversationID]
		mu.Unlock()
		if strings.HasSuffix(body.ConversationID, "/1") {
			writeFixtureJSON(w, http.StatusOK, finalResponse(largeOutput, "large prior Run", nil, nil))
			return
		}
		if call == 1 {
			writeToolCall(w, "history", "get_run_history", map[string]any{"run_id": 1})
			return
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("done", "history inspected", nil, nil))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop = %d %s", status, raw)
	}
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if len(run.Progress) > 40<<10 || !strings.Contains(string(run.Progress), `"iteration_count":1`) ||
		!strings.Contains(string(run.Progress), `"output_preview"`) || strings.Contains(string(run.Progress), largeOutput) {
		t.Fatalf("nested History was not filtered; bytes=%d trace=%s", len(run.Progress), run.Progress)
	}
}

func TestPlanAgentRepairsMemoryReferenceBeforeAtomicPublication(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	missing := "m_00000000000000000000000000000000"
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		candidate := []any{map[string]any{"type": "fact", "summary": "one", "content": "only once", "importance": 2}}
		if calls.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusOK, finalResponse("invalid ref", "invalid ref", candidate,
				[]any{map[string]any{"id": missing, "reason": "obsolete"}}))
			return
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("fixed", "fixed", candidate, nil))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	memories := listMemoryDetails(t, runtime.Handler(), session.ID, true)
	if calls.Load() != 2 || run.ModelCalls != 2 || len(memories) != 1 || memories[0]["summary"] != "one" {
		t.Fatalf("reference repair calls=%d Run=%+v Memories=%#v", calls.Load(), run, memories)
	}
	if !strings.Contains(string(run.Progress), "previous memory references were invalid") {
		t.Fatalf("repair instruction not audited: %s", run.Progress)
	}
}

func TestPlanAgentBoundsContextWithoutOrphaningToolResults(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	large := strings.Repeat("x", 700<<10)
	var thirdSize int64
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := calls.Add(1)
		if current == 3 {
			thirdSize = request.ContentLength
			var payload struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode compacted request: %v", err)
			}
			for index, message := range payload.Messages {
				if message["role"] == "tool" && (index == 0 || payload.Messages[index-1]["role"] != "assistant") {
					t.Errorf("orphan tool result at %d", index)
				}
			}
			writeFixtureJSON(w, http.StatusOK, finalResponse("done", "done", nil, nil))
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": large, "tool_calls": []any{map[string]any{"id": "price-" + jsonNumber(int64(current)), "name": "get_market_snapshot", "arguments": map[string]any{"symbol": "BTCUSDT"}}},
			"finish_reason": "tool_call", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 3, "max_tool_call": 3})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if thirdSize <= 0 || thirdSize > 1<<20 || !strings.Contains(string(run.Progress), `"context_compacted_messages":1`) {
		t.Fatalf("bounded request bytes=%d trace=%s", thirdSize, run.Progress)
	}
}

func TestPlanAgentRejectsOversizedMandatoryContextBeforeGenerateDispatch(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"prompt": strings.Repeat("p", 800<<10)})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	fixture.mu.Lock()
	generateCalls := fixture.generateCalls
	fixture.mu.Unlock()
	if generateCalls != 0 || run.ModelCalls != 0 || run.Error == nil || !strings.Contains(*run.Error, "CONTEXT_LIMIT_EXCEEDED") {
		t.Fatalf("oversized mandatory context dispatched: calls=%d Run=%+v", generateCalls, run)
	}
}
