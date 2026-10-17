package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func compactionModels(window int) []any {
	return []any{map[string]any{
		"model_name": "fixture-model", "provider_id": "fixture", "model": "provider-model",
		"options": map[string]any{}, "levels": map[string]any{"high": map[string]any{}},
		"context_window": window, "enabled": true, "available": true,
	}}
}

// largeSnapshots makes every market snapshot result carry padding runes, so read outputs
// dominate the estimated context.
func largeSnapshots(padding int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"market": "crypto", "symbol": request.URL.Query().Get("symbol") + strings.Repeat("~", padding),
			"price": 60000, "updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
}

func toolStep(content string, calls ...map[string]any) map[string]any {
	return map[string]any{"content": content, "finish_reason": "tool_call", "usage": nil, "tool_calls": calls}
}

func snapshotCall(id, symbol string) map[string]any {
	return toolCall(id, "get_market_snapshot", map[string]any{"symbol": symbol})
}

type compactionScript struct {
	mu         sync.Mutex
	main       []map[string]any
	compaction []map[string]any
	// compactedBefore[i] is the number of main calls made before compaction call i.
	compactedBefore []int
}

func (s *compactionScript) handler(t *testing.T, mainStep func(call int) map[string]any, compactionStep func(http.ResponseWriter)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode generate request: %v", err)
		}
		s.mu.Lock()
		conversationID, _ := body["conversation_id"].(string)
		if strings.HasSuffix(conversationID, "/compaction") {
			s.compaction = append(s.compaction, body)
			s.compactedBefore = append(s.compactedBefore, len(s.main))
			s.mu.Unlock()
			compactionStep(w)
			return
		}
		s.main = append(s.main, body)
		call := len(s.main)
		s.mu.Unlock()
		writeFixtureJSON(w, http.StatusOK, mainStep(call))
	}
}

func requestMessages(request map[string]any) []map[string]any {
	raw, _ := request["messages"].([]any)
	messages := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		message, _ := item.(map[string]any)
		messages = append(messages, message)
	}
	return messages
}

func messageRoles(messages []map[string]any) string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message["role"].(string))
	}
	return strings.Join(roles, ",")
}

func messageText(message map[string]any) string {
	text, _ := message["content"].(string)
	return text
}

func compactionEntries(t *testing.T, progress json.RawMessage) []map[string]any {
	t.Helper()
	var entries []map[string]any
	if err := json.Unmarshal(progress, &entries); err != nil {
		t.Fatalf("decode progress: %v", err)
	}
	found := make([]map[string]any, 0)
	for _, entry := range entries {
		if entry["type"] == "context_compaction" {
			found = append(found, entry)
		}
	}
	return found
}

func compactionStage(entry map[string]any) string {
	result, _ := entry["result"].(map[string]any)
	inner, _ := result["result"].(map[string]any)
	stage, _ := inner["stage"].(string)
	return stage
}

func failCompaction(t *testing.T) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		t.Errorf("unexpected LLM compaction call")
		writeFixtureJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "unexpected"}})
	}
}

func TestContextCompactionFoldsOldToolHistoryIntoSemanticLines(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(20000)
	fixture.market = largeSnapshots(32000)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		switch call {
		case 1:
			return toolStep("step one",
				snapshotCall("c1-read", "BTCUSDT"),
				toolCall("c1-bogus", "bogus_tool", map[string]any{}),
				createEventCall("c1-event", "timer", timerParams(mustParseTime(t, at))),
				snapshotCall("c1-read-again", "BTCUSDT"))
		case 2:
			return toolStep("step two", snapshotCall("c2-read", "BTCUSDT"))
		default:
			return finalResponse("done", "done")
		}
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 6, "max_tool_call": 10})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.main) != 3 {
		t.Fatalf("main generate calls = %d", len(script.main))
	}
	if roles := messageRoles(requestMessages(script.main[1])); roles != "system,user,assistant,tool,tool,tool,tool" {
		t.Fatalf("second request must stay uncompacted, roles = %s", roles)
	}
	messages := requestMessages(script.main[2])
	if roles := messageRoles(messages); roles != "system,user,user,assistant,tool" {
		t.Fatalf("compacted roles = %s", roles)
	}
	history := messageText(messages[2])
	for _, wanted := range []string{"[Context compacted]", "assistant: step one", "- create_event", `"ok":true`} {
		if !strings.Contains(history, wanted) {
			t.Fatalf("compacted history misses %q: %s", wanted, history)
		}
	}
	if strings.Count(history, "- get_market_snapshot") != 1 || strings.Contains(history, "bogus_tool") ||
		strings.Contains(history, "UNKNOWN_TOOL") || strings.Contains(history, "~~~~") {
		t.Fatalf("compacted history kept outputs, errors, or duplicates: %s", history)
	}
	if messageText(messages[3]) != "step two" || !strings.Contains(messageText(messages[4]), strings.Repeat("~", 32000)) {
		t.Fatalf("latest assistant turn must stay verbatim: %+v", messages[3:])
	}
	if _, exists := script.main[2]["continuation"]; exists {
		t.Fatalf("compacted request must not use continuation: %+v", script.main[2]["continuation"])
	}
	entries := compactionEntries(t, run.Progress)
	if len(entries) != 1 || compactionStage(entries[0]) != "rules" || entries[0]["error"] != nil {
		t.Fatalf("compaction trace = %+v", entries)
	}
}

func TestContextCompactionDropsRejectedFinalResponsesAndRepairInstructions(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(500)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		switch call {
		case 1:
			return map[string]any{"content": "INVALID-ONE", "tool_calls": []any{}, "finish_reason": "stop", "usage": nil}
		case 2:
			return map[string]any{"content": "INVALID-TWO", "tool_calls": []any{}, "finish_reason": "stop", "usage": nil}
		default:
			return finalResponse("done", "done")
		}
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	script.mu.Lock()
	defer script.mu.Unlock()
	messages := requestMessages(script.main[2])
	if roles := messageRoles(messages); roles != "system,user,user,assistant,user" {
		t.Fatalf("compacted roles = %s", roles)
	}
	encoded, _ := json.Marshal(messages)
	if strings.Contains(string(encoded), "INVALID-ONE") || messageText(messages[3]) != "INVALID-TWO" ||
		!strings.HasPrefix(messageText(messages[2]), "[Context compacted]") ||
		!strings.HasPrefix(messageText(messages[4]), "Terminal instruction") {
		t.Fatalf("retries were not dropped: %s", encoded)
	}
}

// analysisSteps returns tool-calling decisions whose CJK analysis keeps growing the context;
// each folded line is capped at 1000 runes, so a 10000-token window eventually needs the LLM pass.
func analysisSteps(call int) map[string]any {
	if call <= 8 {
		label := "ANALYSIS-" + jsonNumber(int64(call))
		return toolStep(label+" "+strings.Repeat("分", 1500), snapshotCall("read-"+jsonNumber(int64(call)), "BTCUSDT"))
	}
	return finalResponse("done", "done")
}

func TestContextCompactionSummarizesLargeHistoryWithOneToolLessCall(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(10000)
	script := &compactionScript{}
	fixture.generate = script.handler(t, analysisSteps, func(w http.ResponseWriter) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": "SUMMARY-TEXT: BTC read repeatedly, no order placed", "tool_calls": []any{}, "finish_reason": "stop",
			"usage": map[string]any{"input_tokens": 3000, "output_tokens": 20},
		})
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 12, "max_tool_call": 12})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.compaction) == 0 {
		t.Fatalf("no LLM compaction call; main calls = %d", len(script.main))
	}
	summaryRequest := script.compaction[0]
	tools, _ := summaryRequest["tools"].([]any)
	summaryMessages := requestMessages(summaryRequest)
	if len(tools) != 0 || messageRoles(summaryMessages) != "system,user" ||
		!strings.Contains(messageText(summaryMessages[0]), "Summarize") || !strings.Contains(messageText(summaryMessages[1]), "ANALYSIS-1") {
		t.Fatalf("compaction request = %.500v", summaryRequest)
	}
	next := script.compactedBefore[0]
	messages := requestMessages(script.main[next])
	history := messageText(messages[2])
	if !strings.HasPrefix(history, "[Context compacted]") || !strings.Contains(history, "SUMMARY-TEXT") || strings.Contains(history, "ANALYSIS") {
		t.Fatalf("summary did not replace history: %.300s", history)
	}
	if !strings.HasPrefix(messageText(messages[3]), "ANALYSIS-"+jsonNumber(int64(next))) {
		t.Fatalf("latest assistant turn must stay verbatim: %.100s", messageText(messages[3]))
	}
	entries := compactionEntries(t, run.Progress)
	var llm map[string]any
	for _, entry := range entries {
		if compactionStage(entry) == "llm" {
			llm = entry
			break
		}
	}
	usage, _ := llm["usage"].(map[string]any)
	if llm == nil || usage["input_tokens"] != float64(3000) || !strings.Contains(messageText(llm), "SUMMARY-TEXT") {
		t.Fatalf("compaction trace = %+v", entries)
	}
}

func TestContextCompactionFallsBackToRulesWhenSummaryFails(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(10000)
	script := &compactionScript{}
	fixture.generate = script.handler(t, analysisSteps, func(w http.ResponseWriter) {
		writeFixtureJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "down"}})
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 12, "max_tool_call": 12})
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.compaction) == 0 {
		t.Fatalf("summary was never attempted; main calls = %d", len(script.main))
	}
	history := messageText(requestMessages(script.main[script.compactedBefore[0]])[2])
	if !strings.Contains(history, "assistant: ANALYSIS-1") || strings.Contains(history, strings.Repeat("分", 1000)) ||
		strings.Count(history, "- get_market_snapshot") != 1 {
		t.Fatalf("rule-compacted history must keep capped assistant lines and one deduplicated read: %.300s", history)
	}
	failedSummaries := 0
	for _, entry := range compactionEntries(t, run.Progress) {
		if compactionStage(entry) != "rules" {
			t.Fatalf("failed summary must keep the rules stage: %+v", entry)
		}
		if errText, _ := entry["error"].(string); strings.Contains(errText, "PROVIDER_UNAVAILABLE") {
			failedSummaries++
		}
	}
	if failedSummaries == 0 {
		t.Fatal("failed summary error was not traced")
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
