package test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLegacyRestartContextIsCompactedOnlyAtPromptInjection(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	initialFixture := newRuntimeFixture()
	initial, _, _ := initialFixture.open(t, databasePath)
	session := createStoppedSession(t, initial, nil)
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial Runtime: %v", err)
	}

	progress := make([]map[string]any, 0, 9)
	for index := 0; index < 9; index++ {
		progress = append(progress, map[string]any{
			"type": "model_call", "iteration": index + 1, "finish_reason": "tool_call",
			"content": strings.Repeat("large-legacy-trace-", 200),
		})
	}
	legacy := map[string]any{
		"previous_run_id": 1, "status": "interrupted", "reason": "UNKNOWN_TOOL_OUTCOME",
		"model_call_count": 9, "tool_call_count": 0, "progress": progress,
		"cleared_events":   []any{map[string]any{"id": "ev_full_audit", "params": map[string]any{"at": "2026-09-16T00:00:00Z"}}},
		"pending_triggers": []any{map[string]any{"event_id": "ev_pending_audit", "type": "timer"}},
	}
	encodedLegacy, _ := json.Marshal(legacy)
	db := openTicket36Database(t, databasePath)
	if _, err := db.Exec(`UPDATE sessions SET current_run_id=1,restart_context=? WHERE id=?`, string(encodedLegacy), session.ID); err != nil {
		t.Fatalf("seed legacy restart context: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO runs (session_id,run_id,status,trigger,error,progress_json,model_call_count,tool_call_count,start_time,end_time)
		VALUES (?,1,'interrupted','session_start','UNKNOWN_TOOL_OUTCOME','[]',0,0,'2026-09-15T00:00:00Z','2026-09-15T00:01:00Z')`, session.ID); err != nil {
		t.Fatalf("seed previous Run: %v", err)
	}
	_ = db.Close()

	prompt := make(chan string, 1)
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		prompt <- contextText(body)
		writeFixtureJSON(w, http.StatusOK, finalResponse("done", "done", nil, nil))
	}
	runtime, _, _ := fixture.open(t, databasePath)
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	text := <-prompt
	if len(text) >= 6000 || strings.Contains(text, "large-legacy-trace-") || strings.Contains(text, `"progress":[`) ||
		!containsAll(text, `"previous_run_id":1`, `"reason":"UNKNOWN_TOOL_OUTCOME"`, `"progress_omitted":4`,
			`"ev_full_audit"`, `"ev_pending_audit"`) {
		t.Fatalf("legacy restart prompt was not safely compacted: %s", text)
	}

	db = openTicket36Database(t, databasePath)
	defer db.Close()
	var persisted string
	if err := db.QueryRow(`SELECT restart_context FROM sessions WHERE id=?`, session.ID).Scan(&persisted); err != nil {
		t.Fatalf("read legacy restart context: %v", err)
	}
	if !strings.Contains(persisted, "large-legacy-trace-") {
		t.Fatalf("legacy audit context was destructively migrated: %s", persisted)
	}
}

func TestRestartContextKeepsOnlyCountsAndFiveRecentTraceStatuses(t *testing.T) {
	fixture := newRuntimeFixture()
	seventhStarted := make(chan struct{})
	releaseSeventh := make(chan struct{})
	var calls atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := calls.Add(1)
		if current <= 6 {
			writeToolCall(w, "large-call-"+jsonNumber(int64(current)), "missing_tool", map[string]any{
				"payload": strings.Repeat("large-argument-", 200),
			})
			return
		}
		close(seventhStarted)
		select {
		case <-request.Context().Done():
		case <-releaseSeventh:
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 10, "max_tool_call": 10})
	startSessionForTest(t, runtime, session.ID)
	waitTestSignal(t, seventhStarted, "seventh model call")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", status, raw)
	}
	close(releaseSeventh)
	waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	stopped := getSessionForTest(t, runtime, session.ID)
	if len(stopped.RestartContext) >= 4000 || strings.Contains(string(stopped.RestartContext), `"arguments"`) ||
		strings.Contains(string(stopped.RestartContext), `"result"`) || strings.Contains(string(stopped.RestartContext), "large-argument-") {
		t.Fatalf("restart context was not compacted (%d bytes): %s", len(stopped.RestartContext), stopped.RestartContext)
	}
	var context struct {
		Status          string           `json:"status"`
		Reason          string           `json:"reason"`
		ProgressSummary map[string]int   `json:"progress_summary"`
		RecentProgress  []map[string]any `json:"recent_progress"`
		ProgressOmitted int              `json:"progress_omitted"`
	}
	if err := json.Unmarshal(stopped.RestartContext, &context); err != nil {
		t.Fatalf("decode restart context %s: %v", stopped.RestartContext, err)
	}
	if context.Status != "interrupted" || !strings.Contains(context.Reason, "HARD_STOP") ||
		context.ProgressSummary["model_call_count"] != 7 || context.ProgressSummary["tool_call_count"] != 6 ||
		context.ProgressSummary["trace_count"] != 13 || len(context.RecentProgress) != 5 || context.ProgressOmitted != 8 {
		t.Fatalf("restart context = %s", stopped.RestartContext)
	}
	foundRecentTool := false
	for _, entry := range context.RecentProgress {
		if entry["tool_name"] == "missing_tool" {
			foundRecentTool = true
		}
		for key := range entry {
			switch key {
			case "type", "iteration", "tool_name", "finish_reason", "error_code", "status":
			default:
				t.Fatalf("recent progress retained %q: %s", key, stopped.RestartContext)
			}
		}
	}
	if !foundRecentTool {
		t.Fatalf("restart context omitted recent Tool name: %s", stopped.RestartContext)
	}
}
