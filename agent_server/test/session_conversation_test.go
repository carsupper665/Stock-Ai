package test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNextRunContinuesTheSessionConversationWithReadResultsAsDigests(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.market = largeSnapshots(4000)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		switch call {
		case 1:
			return toolStep("checking BTC", snapshotCall("r1-read", "BTCUSDT"), createEventCall("r1-event", "timer", timerParams(mustParseTime(t, at))))
		case 2:
			return map[string]any{"content": "NOT-JSON-FINAL", "tool_calls": []any{}, "finish_reason": "stop", "usage": nil}
		case 3:
			return finalResponse("run one done", "run one done")
		default:
			return finalResponse("run two done", "run two done")
		}
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	first := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	restartSessionRun(t, runtime, session.ID)
	second := waitForRunStatus(t, runtime, session.ID, 2, "completed")

	var firstTrace, secondTrace []map[string]any
	_ = json.Unmarshal(first.Progress, &firstTrace)
	_ = json.Unmarshal(second.Progress, &secondTrace)
	if firstTrace[0]["type"] != "model_call" || firstTrace[0]["context_tokens"] == nil || firstTrace[0]["context_window"] != float64(128000) {
		t.Fatalf("Run 1 must start with a model call recording its context use: %+v", firstTrace[0])
	}
	resume, _ := secondTrace[0]["result"].(map[string]any)
	detail, _ := resume["result"].(map[string]any)
	if secondTrace[0]["type"] != "conversation_resume" || detail["from_run_id"] != float64(1) || detail["messages"] != float64(5) {
		t.Fatalf("Run 2 must record which conversation it continued: %+v", secondTrace[0])
	}

	script.mu.Lock()
	defer script.mu.Unlock()
	messages := requestMessages(script.main[len(script.main)-1])
	if roles := messageRoles(messages); roles != "system,user,assistant,tool,tool,assistant,user" {
		t.Fatalf("Run 2 roles = %s", roles)
	}
	if messageText(messages[2]) != "checking BTC" || !strings.Contains(messageText(messages[5]), "run one done") {
		t.Fatalf("Run 1 assistant turns must carry over verbatim: %+v", messages)
	}
	read, event := messageText(messages[3]), messageText(messages[4])
	if !strings.Contains(read, `"digest"`) || strings.Contains(read, strings.Repeat("~", 400)) {
		t.Fatalf("read result must be a short digest: %s", read)
	}
	if strings.Contains(event, `"digest"`) || !strings.Contains(event, `"ok":true`) {
		t.Fatalf("mutation result must stay whole: %s", event)
	}
	encoded, _ := json.Marshal(messages)
	if strings.Contains(string(encoded), "NOT-JSON-FINAL") || strings.Contains(string(encoded), "Terminal instruction") {
		t.Fatalf("rejected final and repair instruction must not carry over: %s", encoded)
	}
	earlier := decodeMap(t, []byte(messageText(messages[1])))
	if earlier["trigger"] != "session_start" || earlier["available_tools"] != nil || earlier["memory_index"] != nil {
		t.Fatalf("earlier wake-up message must keep only lasting facts: %+v", earlier)
	}
	if current := decodeMap(t, []byte(messageText(messages[6]))); current["trigger"] != "session_restart" || current["available_tools"] == nil {
		t.Fatalf("current wake-up message = %+v", current)
	}

	status, raw := runtimeRequest(t, runtime.Handler(), "GET", "/api/v1/sessions/"+session.ID+"/conversation", agentAdminToken, nil)
	var view struct {
		Messages []map[string]any `json:"messages"`
		RunID    int64            `json:"run_id"`
		Tokens   int              `json:"estimated_tokens"`
	}
	if err := json.Unmarshal(raw, &view); status != 200 || err != nil || view.RunID != 2 || view.Tokens <= 0 || len(view.Messages) != 7 {
		t.Fatalf("conversation = %d %s", status, raw)
	}
}

func TestRepeatedIdenticalToolFailuresCarryOverOnce(t *testing.T) {
	fixture := newRuntimeFixture()
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call <= 5 {
			return toolStep("", toolCall("bogus-"+jsonNumber(int64(call)), "bogus_tool", map[string]any{"n": call}))
		}
		return finalResponse("done", "done")
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 8, "max_tool_call": 8})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	restartSessionRun(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 2, "completed")

	script.mu.Lock()
	defer script.mu.Unlock()
	messages := requestMessages(script.main[len(script.main)-1])
	if roles := messageRoles(messages); roles != "system,user,assistant,tool,assistant,user" {
		t.Fatalf("Run 2 roles = %s", roles)
	}
	failure := decodeMap(t, []byte(messageText(messages[3])))
	if failure["repeated"] != float64(5) || failure["ok"] != false {
		t.Fatalf("folded failure = %s", messageText(messages[3]))
	}
}

func TestRunStartingOverBudgetFoldsEarlierRunsFirst(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(8000)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return finalResponse(strings.Repeat("長", 7000), "big run")
		}
		return finalResponse("small run", "small run")
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	restartSessionRun(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 2, "completed")

	script.mu.Lock()
	defer script.mu.Unlock()
	messages := requestMessages(script.main[len(script.main)-1])
	if roles := messageRoles(messages); roles != "system,user,user" {
		t.Fatalf("Run 2 roles = %s", roles)
	}
	history := messageText(messages[1])
	if !strings.HasPrefix(history, "[Context compacted]") || !strings.Contains(history, "wake: ") || strings.Contains(history, strings.Repeat("長", 1001)) {
		t.Fatalf("earlier Run must fold into one compacted message: %.200s", history)
	}
	if decodeMap(t, []byte(messageText(messages[2])))["trigger"] != "session_restart" {
		t.Fatalf("current wake-up message must stay verbatim: %.200s", messageText(messages[2]))
	}
	if entries := compactionEntries(t, run.Progress); len(entries) != 1 || compactionStage(entries[0]) != "rules" {
		t.Fatalf("compaction trace = %+v", entries)
	}
}
