package test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConversationModeAPI(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	for _, value := range []any{nil, "", "raw", "VERBATIM", 1, false} {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken,
			map[string]any{"name": "invalid", "account_id": testAccountID, "model_name": "fixture-model", "conversation_mode": value})
		if status != 400 || decodeMap(t, raw)["error"] != "INVALID_REQUEST" {
			t.Fatalf("mode %v = %d %s", value, status, raw)
		}
	}
	for i, mode := range []string{"semantic", "verbatim", "omitted"} {
		body := map[string]any{"name": mode, "account_id": "acc_mode_" + jsonNumber(int64(i)), "model_name": "fixture-model"}
		want := mode
		if mode != "omitted" {
			body["conversation_mode"] = mode
		} else {
			want = "semantic"
		}
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken, body)
		if status != 201 || decodeMap(t, raw)["conversation_mode"] != want {
			t.Fatalf("create %s = %d %s", mode, status, raw)
		}
		id := decodeMap(t, raw)["id"].(string)
		status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+id, agentAdminToken, nil)
		if status != 200 || decodeMap(t, raw)["conversation_mode"] != want {
			t.Fatalf("get mode = %d %s", status, raw)
		}
		status, _ = runtimeRequest(t, runtime.Handler(), http.MethodPatch, "/api/v1/sessions/"+id, agentAdminToken, map[string]any{"conversation_mode": "semantic"})
		if status != 405 {
			t.Fatalf("mode mutable: PATCH returned %d", status)
		}
	}
}

func TestVerbatimKeepsWholePrefixAcrossThreeRuns(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.market = largeSnapshots(4000)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		switch call {
		case 1:
			return toolStep("original response", snapshotCall("read", "BTCUSDT"))
		case 2:
			return map[string]any{"content": "NOT-JSON-FINAL", "finish_reason": "stop", "tool_calls": []any{}}
		default:
			return finalResponse("done "+jsonNumber(int64(call)), "summary")
		}
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"conversation_mode": "verbatim"})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	for id := int64(2); id <= 3; id++ {
		restartSessionRun(t, runtime, session.ID)
		waitForRunStatus(t, runtime, session.ID, id, "completed")
	}
	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.main) != 5 {
		t.Fatalf("calls = %d", len(script.main))
	}
	for index := 3; index < 5; index++ {
		before := requestMessages(script.main[index-1])
		before = append(before, map[string]any{"role": "assistant", "content": finalizationContent("done "+jsonNumber(int64(index)), "summary")})
		after := requestMessages(script.main[index])
		if !reflect.DeepEqual(before, after[:len(after)-1]) {
			t.Fatalf("Run prefix changed at request %d", index+1)
		}
	}
	encoded, _ := json.Marshal(requestMessages(script.main[4]))
	for _, original := range []string{strings.Repeat("~", 4000), "NOT-JSON-FINAL", "Terminal instruction", "available_tools"} {
		if !strings.Contains(string(encoded), original) {
			t.Fatalf("verbatim lost %q", original[:min(len(original), 50)])
		}
	}
}

func TestVerbatimCompactionBecomesNextPrefix(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(8000)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return finalResponse(strings.Repeat("長", 7000), "large first Run")
		}
		return finalResponse("small", "small")
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"conversation_mode": "verbatim"})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	restartSessionRun(t, runtime, session.ID)
	second := waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if len(compactionEntries(t, second.Progress)) == 0 {
		t.Fatal("compaction did not occur")
	}
	restartSessionRun(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 3, "completed")
	script.mu.Lock()
	defer script.mu.Unlock()
	secondMessages := requestMessages(script.main[1])
	if !strings.HasPrefix(messageText(secondMessages[1]), "[Context compacted]") {
		t.Fatalf("no new compaction baseline: %v", secondMessages)
	}
	want := append(secondMessages, map[string]any{"role": "assistant", "content": finalizationContent("small", "small")})
	third := requestMessages(script.main[2])
	if !reflect.DeepEqual(want, third[:len(third)-1]) {
		t.Fatal("compacted baseline changed in third Run")
	}
}

func TestVerbatimInterruptedToolGetsUnknownResult(t *testing.T) {
	for _, mode := range []string{"semantic", "verbatim"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRuntimeFixture()
			entered := make(chan struct{})
			fixture.market = func(w http.ResponseWriter, request *http.Request) { close(entered); <-request.Context().Done() }
			script := newLLMScript(respondToolCalls(snapshotCall("unfinished", "BTCUSDT")), respondStop("checked uncertain result"))
			fixture.generate = script.handler(t)
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, map[string]any{"conversation_mode": mode})
			startSessionForTest(t, runtime, session.ID)
			script.next(t, "tool call")
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("tool not entered")
			}
			stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
			waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
			startSessionForTest(t, runtime, session.ID)
			messages := requestMessages(script.next(t, "restarted conversation"))
			found := false
			for _, message := range messages {
				if message["role"] == "tool" && message["tool_call_id"] == "unfinished" {
					content := messageText(message)
					if !strings.Contains(content, "RUN_INTERRUPTED") || !strings.Contains(content, `"outcome":"unknown"`) {
						t.Fatalf("interrupted result = %s", content)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("missing interrupted tool result")
			}
			waitForRunStatus(t, runtime, session.ID, 2, "completed")
		})
	}
}

func TestVerbatimByteBoundCompactsLaterRuns(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(10000000)
	fixture.market = largeSnapshots(800000)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("read", snapshotCall("large", "BTCUSDT"))
		}
		return finalResponse("recovered", "recovered")
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"conversation_mode": "verbatim"})
	startSessionForTest(t, runtime, session.ID)
	// One tool result alone exceeds the bound; nothing earlier can fold, so this Run fails.
	if run := waitForRunStatus(t, runtime, session.ID, 1, "failed"); run.ErrorCode != "CONTEXT_LIMIT_EXCEEDED" {
		t.Fatalf("byte bound error = %v", run.Error)
	}
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 2, "completed")
	if !strings.Contains(string(run.Progress), `"type":"context_compaction"`) {
		t.Fatalf("Run 2 did not compact: %s", run.Progress)
	}
	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.main) != 2 {
		t.Fatalf("main calls = %d", len(script.main))
	}
	encoded, _ := json.Marshal(script.main[1]["messages"])
	if strings.Contains(string(encoded), strings.Repeat("~", 800000)) {
		t.Fatal("Run 2 resent the oversized tool result")
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/conversation", agentAdminToken, nil)
	if status != 200 || strings.Contains(string(raw), strings.Repeat("~", 800000)) {
		t.Fatal("compacted conversation is not the new baseline")
	}
}
