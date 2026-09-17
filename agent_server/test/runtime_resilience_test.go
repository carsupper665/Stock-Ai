package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRuntimeInitialContextIncludesCompactAccountAndLimits(t *testing.T) {
	fixture := newRuntimeFixture()
	var requestBody map[string]any
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		response := finalResponse("done", "done", nil, nil)
		response["reasoning"] = "provider supplied reasoning"
		writeFixtureJSON(w, http.StatusOK, response)
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 7, "max_tool_call": 9})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	messages := requestBody["messages"].([]any)
	contextText := messages[1].(map[string]any)["content"].(string)
	var contextBody map[string]any
	if err := json.Unmarshal([]byte(contextText), &contextBody); err != nil {
		t.Fatalf("decode initial context %q: %v", contextText, err)
	}
	snapshot, _ := contextBody["account_snapshot"].(map[string]any)
	if contextBody["max_loop"] != float64(7) || contextBody["max_tool_call"] != float64(9) ||
		snapshot["ok"] != true || snapshot["id"] != testAccountID || snapshot["user_name"] != "fixture" || snapshot["status"] != "active" ||
		snapshot["initial_balance"] != float64(100000) || snapshot["balance"] != float64(97500) {
		t.Fatalf("initial context = %s", contextText)
	}
	if strings.Contains(contextText, fixture.accountToken) || strings.Contains(contextText, backendUserToken) {
		t.Fatalf("initial context leaked a credential: %s", contextText)
	}
	fixture.mu.Lock()
	requests := strings.Join(fixture.backendRequests, "\n")
	fixture.mu.Unlock()
	if strings.Count(requests, "GET /v1/accounts/"+testAccountID) != 2 || strings.Contains(requests, "/v1/account\n") {
		t.Fatalf("account snapshot did not use the existing account lookup seam: %s", requests)
	}
	run := getRunForTest(t, runtime, session.ID, 1)
	if !strings.Contains(string(run.Progress), `"reasoning":"provider supplied reasoning"`) || !strings.Contains(string(run.Progress), `"attempt":1`) {
		t.Fatalf("provider response fields were not retained: %s", run.Progress)
	}
}

func TestManualRetryCreatesBoundedNewRunWithoutReplayingTools(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	var retryRequest map[string]any
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := calls.Add(1)
		if current == 1 {
			writeToolCall(w, "price-once", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
			return
		}
		if current <= 5 {
			writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
				"code": "PROVIDER_UNAVAILABLE", "message": "temporary",
			}})
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&retryRequest); err != nil {
			t.Errorf("decode retry Generate request: %v", err)
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("retry complete", "retry complete", nil, nil))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4, "max_tool_call": 3})
	startSessionForTest(t, runtime, session.ID)
	failed := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if failed.ToolCalls != 1 || failed.ModelCalls != 5 {
		t.Fatalf("failed Run attempts = %+v", failed)
	}

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/runs/1/retry", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", status, raw)
	}
	retried := waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if retried.Trigger != "manual_retry" || retried.RetryAttempt != 1 || retried.RetryOfRunID == nil || *retried.RetryOfRunID != 1 {
		t.Fatalf("retry Run = %+v", retried)
	}
	messages := retryRequest["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("retry replayed old protocol messages: %+v", messages)
	}
	contextText := messages[1].(map[string]any)["content"].(string)
	if !containsAll(contextText, `"trigger":"manual_retry"`, `"max_loop":4`, `"max_tool_call":3`,
		`"failed_run_id":1`, `"status":"failed"`, `"tool_name":"get_market_snapshot"`, `"error_code":"PROVIDER_UNAVAILABLE"`, `"audit_reference"`) ||
		strings.Contains(contextText, "BTCUSDT") || strings.Contains(contextText, fixture.accountToken) {
		t.Fatalf("retry context was unsafe or incomplete: %s", contextText)
	}
	fixture.mu.Lock()
	requests := strings.Join(fixture.backendRequests, "\n")
	fixture.mu.Unlock()
	if strings.Count(requests, "GET /v1/market/price?market=crypto&symbol=BTCUSDT") != 1 {
		t.Fatalf("manual retry replayed a Tool call: %s", requests)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/runs/1/retry", agentAdminToken, nil)
	if status != http.StatusConflict || !strings.Contains(string(raw), "RUN_NOT_LATEST") {
		t.Fatalf("historical retry status=%d body=%s", status, raw)
	}
}

func TestManualRetryStopsAfterThreeNewRuns(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
			"code": "AUTHENTICATION_FAILED", "message": "fixed failure",
		}})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "failed")

	for attempt := 1; attempt <= 3; attempt++ {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
			"/api/v1/sessions/"+session.ID+"/runs/"+jsonNumber(int64(attempt))+"/retry", agentAdminToken, nil)
		if status != http.StatusAccepted {
			t.Fatalf("retry %d status=%d body=%s", attempt, status, raw)
		}
		run := waitForRunStatus(t, runtime, session.ID, int64(attempt+1), "failed")
		if run.RetryAttempt != attempt || run.RetryOfRunID == nil || *run.RetryOfRunID != 1 {
			t.Fatalf("retry %d metadata = %+v", attempt, run)
		}
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/runs/4/retry", agentAdminToken, nil)
	if status != http.StatusConflict || !strings.Contains(string(raw), "RETRY_LIMIT_REACHED") {
		t.Fatalf("fourth retry status=%d body=%s", status, raw)
	}
}

func TestRuntimeRetriesTransientGenerateInPlaceAndCountsEveryAttempt(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
				"code": "PROVIDER_UNAVAILABLE", "message": "temporary",
			}})
			return
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("recovered", "recovered", nil, nil))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 1})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if calls.Load() != 3 || run.ModelCalls != 3 {
		t.Fatalf("retry calls=%d Run=%+v", calls.Load(), run)
	}
	var progress []map[string]any
	if err := json.Unmarshal(run.Progress, &progress); err != nil || len(progress) != 3 {
		t.Fatalf("retry progress=%s error=%v", run.Progress, err)
	}
	if progress[0]["error"] == nil || progress[1]["error"] == nil || progress[2]["finish_reason"] != "stop" {
		t.Fatalf("retry traces = %s", run.Progress)
	}
}

func TestRuntimeKeepsConversationAcrossRetriesAndPublishesClaudeDiagnostics(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	var mu sync.Mutex
	var conversations []string
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		mu.Lock()
		conversations = append(conversations, body.ConversationID)
		mu.Unlock()
		if calls.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "temporary"}})
			return
		}
		response := finalResponse("done", "done", nil, nil)
		response["num_turns"] = 3
		response["total_cost_usd"] = 0.25
		writeFixtureJSON(w, http.StatusOK, response)
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 1})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	wantConversation := session.ID + "/1"
	mu.Lock()
	gotConversations := append([]string(nil), conversations...)
	mu.Unlock()
	if len(gotConversations) != 2 || gotConversations[0] != wantConversation || gotConversations[1] != wantConversation {
		t.Fatalf("Generate conversation IDs=%v want %q", gotConversations, wantConversation)
	}
	var progress []map[string]any
	if err := json.Unmarshal(run.Progress, &progress); err != nil || len(progress) != 2 {
		t.Fatalf("public progress=%s error=%v", run.Progress, err)
	}
	if progress[0]["conversation_id"] != wantConversation || progress[1]["conversation_id"] != wantConversation ||
		progress[1]["num_turns"] != float64(3) || progress[1]["total_cost_usd"] != 0.25 {
		t.Fatalf("public diagnostics trace=%s", run.Progress)
	}
	if _, present := progress[0]["num_turns"]; present {
		t.Fatalf("absent diagnostics became zero: %s", run.Progress)
	}
}

func TestRuntimeDoesNotRetryAuthenticationFailure(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
			"code": "AUTHENTICATION_FAILED", "message": "bad credential",
		}})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	fixture.mu.Lock()
	calls := fixture.generateCalls
	fixture.mu.Unlock()
	if calls != 1 || run.ModelCalls != 1 || run.Error == nil || !strings.Contains(*run.Error, "AUTHENTICATION_FAILED") {
		t.Fatalf("authentication failure calls=%d Run=%+v", calls, run)
	}
}

func TestRuntimeRepairsMalformedFinalEnvelopeWithToolsDisabled(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := calls.Add(1)
		var body struct {
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		if current == 1 {
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": "not-json", "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
			})
			return
		}
		if len(body.Tools) != 0 || len(body.Messages) != 4 || body.Messages[2]["role"] != "assistant" ||
			body.Messages[3]["role"] != "user" || !strings.Contains(body.Messages[3]["content"].(string), "do not call tools") {
			t.Errorf("repair request was not protocol-valid and tool-disabled: %+v", body)
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("fixed", "fixed", nil, nil))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 1})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if calls.Load() != 2 || run.ModelCalls != 2 || run.Output == nil || *run.Output != "fixed" {
		t.Fatalf("final repair calls=%d Run=%+v", calls.Load(), run)
	}
}

func TestRuntimeBudgetExhaustionForcesToolDisabledFinalization(t *testing.T) {
	fixture := newRuntimeFixture()
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := calls.Add(1)
		var body struct {
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		if current == 1 {
			writeToolCall(w, "price", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
			return
		}
		if len(body.Tools) != 0 || !strings.Contains(body.Messages[len(body.Messages)-1]["content"].(string), "max_loop reached") {
			t.Errorf("budget finalization request = %+v", body)
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("budget summary", "budget summary", nil, nil))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 1, "max_tool_call": 3})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if calls.Load() != 2 || run.ModelCalls != 2 || run.ToolCalls != 1 || run.Error != nil {
		t.Fatalf("budget finalization calls=%d Run=%+v", calls.Load(), run)
	}
}
