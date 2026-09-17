package test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTicket37UsageAggregatesReportedSubsetsAndServerModelIdentities(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open usage fixture database: %v", err)
	}
	defer db.Close()
	started := "2026-09-15T01:00:00Z"
	ended := "2026-09-15T01:00:01Z"
	progress1 := `[
		{"type":"model_call","iteration":1,"model_name":"fixture-model","provider_id":"provider-a","model":"actual-a","started_at":"2026-09-15T01:00:00Z","ended_at":"2026-09-15T01:00:00.010Z","duration_ms":10,"usage":{"input_tokens":10,"output_tokens":3,"cached_tokens":4,"total_tokens":13}},
		{"type":"model_call","iteration":2,"model_name":"fixture-model","provider_id":"provider-a","model":"actual-a","started_at":"2026-09-15T01:00:00.020Z","ended_at":"2026-09-15T01:00:00.030Z","duration_ms":10,"usage":{"input_tokens":5,"output_tokens":null,"cached_tokens":null,"total_tokens":null}}
	]`
	progress2 := `[{"type":"model_call","iteration":1,"model_name":"fixture-model","started_at":"2026-09-15T01:01:00Z","ended_at":"2026-09-15T01:01:00.005Z","duration_ms":5}]`
	if _, err := db.Exec(`INSERT INTO runs (session_id,run_id,status,trigger,progress_json,model_call_count,tool_call_count,start_time,end_time,error)
		VALUES (?,1,'completed','session_start',?,2,0,?,?,NULL),
		       (?,2,'failed','session_restart',?,1,0,?,?,'GATEWAY_ERROR')`,
		session.ID, progress1, started, ended, session.ID, progress2, started, ended); err != nil {
		t.Fatalf("insert usage fixture runs: %v", err)
	}

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/usage", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("usage response = %d %s", status, raw)
	}
	response := decodeMap(t, raw)
	assertUsageValues(t, response, 3, 15, 3, 4, 13, map[string]float64{
		"input_tokens": 1, "output_tokens": 2, "cached_tokens": 2, "total_tokens": 2,
	})
	byRun, ok := response["by_run"].([]any)
	if !ok || len(byRun) != 2 {
		t.Fatalf("by_run = %#v", response["by_run"])
	}
	assertUsageValues(t, byRun[0].(map[string]any), 2, 15, 3, 4, 13, map[string]float64{
		"input_tokens": 0, "output_tokens": 1, "cached_tokens": 1, "total_tokens": 1,
	})
	byModel, ok := response["by_model"].([]any)
	if !ok || len(byModel) != 2 {
		t.Fatalf("by_model = %#v", response["by_model"])
	}
	resolved := byModel[0].(map[string]any)
	if resolved["model_name"] != "fixture-model" || resolved["provider_id"] != "provider-a" || resolved["model"] != "actual-a" {
		t.Fatalf("resolved model identity = %#v", resolved)
	}
	unresolved := byModel[1].(map[string]any)
	if unresolved["model_name"] != "fixture-model" || unresolved["provider_id"] != nil || unresolved["model"] != nil {
		t.Fatalf("unresolved model identity = %#v", unresolved)
	}
	assertUsageValues(t, unresolved, 1, nil, nil, nil, nil, map[string]float64{
		"input_tokens": 1, "output_tokens": 1, "cached_tokens": 1, "total_tokens": 1,
	})
}

func TestTicket37CurrentRunReportsBudgetActivityElapsedAndUnknownUsage(t *testing.T) {
	fixture := newRuntimeFixture()
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		close(entered)
		select {
		case <-release:
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": finalizationContent("done", "done", nil, nil), "tool_calls": []any{}, "finish_reason": "stop",
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 2, "cached_tokens": 3, "total_tokens": 10},
			})
		case <-request.Context().Done():
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4, "max_tool_call": 3})
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run = %d %s", status, raw)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Generate was not reached")
	}

	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("current Run = %d %s", status, raw)
	}
	current := decodeMap(t, raw)
	if current["model_call_count"] != float64(1) || current["decision_count"] != float64(1) || current["max_loop"] != float64(4) || current["loop_remaining"] != float64(3) ||
		current["tool_call_count"] != float64(0) || current["max_tool_call"] != float64(3) || current["tool_call_remaining"] != float64(3) {
		t.Fatalf("current budgets = %#v", current)
	}
	if current["current_activity"] != "waiting_for_model" || current["model_name"] != "fixture-model" || current["provider_id"] != "fixture" || current["model"] != "provider-model" {
		t.Fatalf("current activity/source = %#v", current)
	}
	if elapsed, ok := current["elapsed_ms"].(float64); !ok || elapsed < 0 {
		t.Fatalf("elapsed_ms = %#v", current["elapsed_ms"])
	}
	assertUsageValues(t, current, 1, nil, nil, nil, nil, map[string]float64{
		"input_tokens": 1, "output_tokens": 1, "cached_tokens": 1, "total_tokens": 1,
	})
	close(release)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil)
	if status != http.StatusNotFound || !strings.Contains(string(raw), "waiting for an Event") {
		t.Fatalf("waiting Session current response = %d %s", status, raw)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop Session = %d %s", status, raw)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil)
	if status != http.StatusNotFound || !strings.Contains(string(raw), "Stopped Session") {
		t.Fatalf("stopped Session current response = %d %s", status, raw)
	}
}

func TestCurrentRunDerivesDecisionBudgetWithoutCountingRetriesOrTerminalAttempts(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4})
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open decision fixture database: %v", err)
	}
	defer db.Close()
	progress := `[
		{"type":"model_call","attempt_type":"decision","iteration":1,"error":"PROVIDER_UNAVAILABLE: hidden detail"},
		{"type":"model_call","attempt_type":"decision","iteration":1,"error":"PROVIDER_UNAVAILABLE: hidden detail"},
		{"type":"model_call","attempt_type":"terminal","iteration":2,"finish_reason":"stop"}
	]`
	if _, err := db.Exec(`INSERT INTO runs (session_id,run_id,status,trigger,progress_json,model_call_count,tool_call_count,start_time)
		VALUES (?,1,'running','session_start',?,3,0,'2026-09-15T01:00:00Z')`, session.ID, progress); err != nil {
		t.Fatalf("insert decision Run: %v", err)
	}
	if _, err := db.Exec(`UPDATE sessions SET status='running',current_run_id=1 WHERE id=?`, session.ID); err != nil {
		t.Fatalf("activate decision Run: %v", err)
	}

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil)
	current := decodeMap(t, raw)
	if status != http.StatusOK || current["model_call_count"] != float64(3) || current["decision_count"] != float64(1) || current["loop_remaining"] != float64(3) {
		t.Fatalf("derived current decision budget = %d %#v", status, current)
	}

	historical := `[
		{"type":"model_call","iteration":1,"error":"old retry"},
		{"type":"model_call","iteration":1,"error":"old retry"},
		{"type":"model_call","iteration":2,"finish_reason":"stop"}
	]`
	if _, err := db.Exec(`UPDATE runs SET progress_json=? WHERE session_id=? AND run_id=1`, historical, session.ID); err != nil {
		t.Fatalf("replace with historical trace: %v", err)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/runs/current", agentAdminToken, nil)
	current = decodeMap(t, raw)
	if status != http.StatusOK || current["decision_count"] != float64(2) || current["loop_remaining"] != float64(2) {
		t.Fatalf("historical unknown attempts were not conservative = %d %#v", status, current)
	}
}

func TestTicket37FailedModelAttemptRemainsUnknownAfterRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "PROVIDER_UNAVAILABLE", "message": "down"}})
	}
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start failed Run = %d %s", status, raw)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if err := runtime.Close(); err != nil {
		t.Fatalf("close first Runtime: %v", err)
	}

	reopenedFixture := newRuntimeFixture()
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	status, raw = runtimeRequest(t, reopened.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/usage", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("usage after restart = %d %s", status, raw)
	}
	response := decodeMap(t, raw)
	assertUsageValues(t, response, 4, nil, nil, nil, nil, map[string]float64{
		"input_tokens": 4, "output_tokens": 4, "cached_tokens": 4, "total_tokens": 4,
	})
}

func assertUsageValues(t *testing.T, value map[string]any, calls int, input, output, cached, total any, unknown map[string]float64) {
	t.Helper()
	if value["model_calls"] != float64(calls) || !sameJSONNumber(value["input_tokens"], input) || !sameJSONNumber(value["output_tokens"], output) ||
		!sameJSONNumber(value["cached_tokens"], cached) || !sameJSONNumber(value["total_tokens"], total) {
		encoded, _ := json.Marshal(value)
		t.Fatalf("usage values = %s", encoded)
	}
	got, ok := value["unknown_attempts"].(map[string]any)
	if !ok {
		t.Fatalf("unknown_attempts = %#v", value["unknown_attempts"])
	}
	for name, wanted := range unknown {
		if got[name] != wanted {
			t.Fatalf("unknown_attempts.%s = %#v, want %v", name, got[name], wanted)
		}
	}
}

func sameJSONNumber(got, wanted any) bool {
	if number, ok := wanted.(int); ok {
		return got == float64(number)
	}
	return got == wanted
}
