package test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestTicket33StructuredFinalResponseCompletesWithoutExtraDecisionAndPublishesMemories(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("wait for confirmation", "Weak-volume breakout; waiting.", []any{
				map[string]any{"type": "market_observation", "summary": "Breakout volume was weak.", "content": "BTC crossed resistance without confirming volume.", "importance": 2},
				map[string]any{"type": "risk_rule", "summary": "Require volume confirmation.", "content": "Do not chase this setup without volume confirmation.", "importance": 3},
			}, nil),
			"tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 1})
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", status, raw)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.Summary == nil || *run.Summary != "Weak-volume breakout; waiting." || run.Output == nil || *run.Output != "wait for confirmation" || run.ModelCalls != 1 {
		t.Fatalf("completed Run = %+v", run)
	}
	fixture.mu.Lock()
	calls := fixture.generateCalls
	fixture.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Generate calls = %d, want one counted decision and no finalization call", calls)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/memories", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("Memory list status = %d body = %s", status, raw)
	}
	listed := decodeBody[struct {
		Memories []struct {
			ID         string `json:"id"`
			SessionID  string `json:"session_id"`
			RunID      int64  `json:"run_id"`
			Importance int    `json:"importance"`
			RunStartAt string `json:"run_start_at"`
			RunEndAt   string `json:"run_end_at"`
		} `json:"memories"`
	}](t, raw)
	if len(listed.Memories) != 2 || listed.Memories[0].Importance != 3 {
		t.Fatalf("Memories = %s", raw)
	}
	for _, memory := range listed.Memories {
		if memory.ID == "" || memory.SessionID != session.ID || memory.RunID != 1 || memory.RunStartAt != run.StartedAt || run.EndedAt == nil || memory.RunEndAt != *run.EndedAt {
			t.Fatalf("Memory metadata = %+v Run=%+v", memory, run)
		}
	}
	status, history := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusOK || !strings.Contains(string(history), listed.Memories[0].ID) || !strings.Contains(string(history), listed.Memories[1].ID) {
		t.Fatalf("History references status=%d body=%s", status, history)
	}
}

func TestTicket33InvalidFinalizationFailsWithoutRepairOrMemory(t *testing.T) {
	for _, content := range []string{
		"plain final text",
		`{"output":"x","summary":"ok"}`,
		`{"output":null,"summary":"ok","candidate_memories":[],"expire_memories":[]}`,
		`{"output":"x","summary":"","candidate_memories":[],"expire_memories":[]}`,
		`{"output":"x","summary":"ok","candidate_memories":[{"type":"fact","summary":"x","content":"x","importance":4}],"expire_memories":[]}`,
		`{"output":"x","summary":"ok","candidate_memories":[],"expire_memories":[{"id":"not-a-memory-id","reason":"stale"}]}`,
		`{"output":"x","summary":"ok","candidate_memories":[],"expire_memories":[],"session_id":"forged"}`,
	} {
		t.Run(content, func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusOK, map[string]any{"content": content, "tool_calls": []any{}, "finish_reason": "stop", "usage": nil})
			}
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, nil)
			status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			if status != http.StatusAccepted {
				t.Fatalf("start status = %d", status)
			}
			run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
			if run.Summary != nil || run.Output != nil || run.Error == nil || !strings.Contains(*run.Error, "FINALIZATION_INVALID") {
				t.Fatalf("invalid finalization Run = %+v", run)
			}
			fixture.mu.Lock()
			calls := fixture.generateCalls
			fixture.mu.Unlock()
			if calls != 4 {
				t.Fatalf("Generate calls = %d, want initial final response plus three repairs", calls)
			}
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/memories", agentAdminToken, nil)
			if status != http.StatusOK || !strings.Contains(string(raw), `"memories":[]`) {
				t.Fatalf("Memories status=%d body=%s", status, raw)
			}
		})
	}
}

func TestTicket33MemoryTransactionFailureCannotPublishPartialMemoryOrCompletedRun(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("would complete", "would complete", []any{
				map[string]any{"type": "fact", "summary": "first", "content": "first", "importance": 1},
				map[string]any{"type": "fact", "summary": "second", "content": "second", "importance": 2},
			}, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	installRejectingTrigger(t, databasePath, "reject_second_memory", `
		BEFORE INSERT ON memories WHEN NEW.candidate_index = 1
		BEGIN SELECT RAISE(FAIL, 'reject second memory'); END`)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if run.Summary != nil || run.Output != nil || run.Error == nil || !strings.Contains(*run.Error, "PERSISTENCE_ERROR") {
		t.Fatalf("failed finalization Run = %+v", run)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/memories", agentAdminToken, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"memories":[]`) {
		t.Fatalf("published Memories status=%d body=%s", status, raw)
	}
}
