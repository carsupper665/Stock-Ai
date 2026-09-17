package test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTicket38ToolStatsAggregateKnownDurationsErrorsAndUnknownNames(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	other := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_stats_other", "name": "other stats"})
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open stats fixture database: %v", err)
	}
	defer db.Close()
	progress1 := `[
		{"type":"tool_call","tool_name":"get_market_snapshot","started_at":"2026-09-15T01:00:00Z","ended_at":"2026-09-15T01:00:00.010Z","duration_ms":10,"result":{"ok":true,"result":{}}},
		{"type":"tool_call","tool_name":"get_market_snapshot","started_at":"2026-09-15T01:00:01Z","ended_at":"2026-09-15T01:00:01.020Z","duration_ms":20,"result":{"ok":false,"error":{"code":"INVALID_TOOL_ARGUMENTS","outcome":"not_executed"}}},
		{"type":"tool_call","tool_name":"invented_by_model","started_at":"2026-09-15T01:00:02Z","ended_at":"2026-09-15T01:00:02.030Z","duration_ms":30,"result":{"ok":false,"error":{"code":"UNKNOWN_TOOL","outcome":"not_executed"}}}
	]`
	progress2 := `[{"type":"tool_call","tool_name":"get_market_snapshot","started_at":"2026-09-15T01:01:00Z","result":{"ok":false,"error":{"code":"TOOL_INTERRUPTED","outcome":"unknown"}}}]`
	if _, err := db.Exec(`INSERT INTO runs (session_id,run_id,status,trigger,progress_json,model_call_count,tool_call_count,start_time,end_time,error)
		VALUES (?,1,'completed','session_start',?,1,3,'2026-09-15T01:00:00Z','2026-09-15T01:00:03Z',NULL),
		       (?,2,'interrupted','session_restart',?,1,1,'2026-09-15T01:01:00Z','2026-09-15T01:01:01Z','HARD_STOP')`,
		session.ID, progress1, session.ID, progress2); err != nil {
		t.Fatalf("insert stats fixture runs: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO runs (session_id,run_id,status,trigger,progress_json,model_call_count,tool_call_count,start_time,end_time)
		VALUES (?,1,'completed','session_start',?,1,1,'2026-09-15T01:02:00Z','2026-09-15T01:02:01Z')`, other.ID,
		`[{"type":"tool_call","tool_name":"get_market_snapshot","duration_ms":999,"result":{"ok":true,"result":{}}}]`); err != nil {
		t.Fatalf("insert other Session stats: %v", err)
	}

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/tools/stats", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("tool stats = %d %s", status, raw)
	}
	response := decodeMap(t, raw)
	tools := toolStatsByName(t, response)
	market := tools["get_market_snapshot"]
	if market["call_count"] != float64(3) || market["success_count"] != float64(1) || market["error_count"] != float64(2) ||
		market["in_flight_count"] != float64(0) || market["latency_sample_count"] != float64(2) ||
		market["avg_latency_ms"] != float64(15) || market["max_latency_ms"] != float64(20) || market["last_latency_ms"] != float64(20) {
		t.Fatalf("market stats = %#v", market)
	}
	unknown := tools["_unknown"]
	if unknown["call_count"] != float64(1) || unknown["error_count"] != float64(1) || unknown["avg_latency_ms"] != float64(30) {
		t.Fatalf("unknown stats = %#v", unknown)
	}
	if noSamples := tools["memory_list"]; noSamples["call_count"] != float64(0) || noSamples["avg_latency_ms"] != nil || noSamples["max_latency_ms"] != nil || noSamples["last_latency_ms"] != nil {
		t.Fatalf("no-sample stats = %#v", noSamples)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close stats Runtime: %v", err)
	}
	reopenedFixture := newRuntimeFixture()
	reopened, _, _ := reopenedFixture.open(t, databasePath)
	status, raw = runtimeRequest(t, reopened.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/tools/stats", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("tool stats after restart = %d %s", status, raw)
	}
	market = toolStatsByName(t, decodeMap(t, raw))["get_market_snapshot"]
	if market["call_count"] != float64(3) || market["latency_sample_count"] != float64(2) || market["max_latency_ms"] != float64(20) {
		t.Fatalf("restarted or cross-Session stats = %#v", market)
	}
}

func TestTicket38RuntimeCountsValidationTimeoutUnknownAndSuccessAttemptsOnce(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.toolTimeout = 50 * time.Millisecond
	var generation atomic.Int32
	var marketCalls atomic.Int32
	fixture.market = func(w http.ResponseWriter, request *http.Request) {
		if marketCalls.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusOK, map[string]any{"market": "crypto", "symbol": "BTCUSDT", "price": 60000})
			return
		}
		<-request.Context().Done()
	}
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		switch generation.Add(1) {
		case 1:
			writeToolCall(w, "unknown", "model_invented_tool", map[string]any{})
		case 2:
			assertLastToolError(t, request, "UNKNOWN_TOOL")
			writeToolCall(w, "invalid", "get_market_snapshot", map[string]any{})
		case 3:
			assertLastToolError(t, request, "INVALID_TOOL_ARGUMENTS")
			writeToolCall(w, "success", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
		case 4:
			writeToolCall(w, "timeout", "get_market_snapshot", map[string]any{"symbol": "ETHUSDT"})
		case 5:
			assertLastToolError(t, request, "TOOL_TIMEOUT")
			writeFixtureJSON(w, http.StatusOK, finalResponse("stats done", "stats done", nil, nil))
		default:
			t.Fatal("unexpected Generate request")
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 6, "max_tool_call": 5})
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start stats Run = %d %s", status, raw)
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/tools/stats", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("tool stats = %d %s", status, raw)
	}
	tools := toolStatsByName(t, decodeMap(t, raw))
	market := tools["get_market_snapshot"]
	if market["call_count"] != float64(3) || market["success_count"] != float64(1) || market["error_count"] != float64(2) ||
		market["in_flight_count"] != float64(0) || market["latency_sample_count"] != float64(3) {
		t.Fatalf("instrumented market stats = %#v", market)
	}
	unknown := tools["_unknown"]
	if unknown["call_count"] != float64(1) || unknown["success_count"] != float64(0) || unknown["error_count"] != float64(1) || unknown["latency_sample_count"] != float64(1) {
		t.Fatalf("instrumented unknown stats = %#v", unknown)
	}
}

func TestTicket38InFlightAttemptHasNoFalseResultOrLatencyAndBecomesCanceledError(t *testing.T) {
	fixture := newRuntimeFixture()
	toolEntered := make(chan struct{})
	fixture.market = func(_ http.ResponseWriter, request *http.Request) {
		close(toolEntered)
		<-request.Context().Done()
	}
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeToolCall(w, "blocked", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start blocked Tool Run = %d %s", status, raw)
	}
	select {
	case <-toolEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Tool was not reached")
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/tools/stats", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("in-flight stats = %d %s", status, raw)
	}
	market := toolStatsByName(t, decodeMap(t, raw))["get_market_snapshot"]
	if market["call_count"] != float64(1) || market["success_count"] != float64(0) || market["error_count"] != float64(0) ||
		market["in_flight_count"] != float64(1) || market["latency_sample_count"] != float64(0) || market["avg_latency_ms"] != nil {
		t.Fatalf("in-flight market stats = %#v", market)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop blocked Tool Run = %d %s", status, raw)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/tools/stats", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("canceled stats = %d %s", status, raw)
	}
	market = toolStatsByName(t, decodeMap(t, raw))["get_market_snapshot"]
	if market["call_count"] != float64(1) || market["success_count"] != float64(0) || market["error_count"] != float64(1) || market["in_flight_count"] != float64(0) {
		t.Fatalf("canceled market stats = %#v", market)
	}
}

func toolStatsByName(t *testing.T, response map[string]any) map[string]map[string]any {
	t.Helper()
	values, ok := response["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %#v", response["tools"])
	}
	result := make(map[string]map[string]any, len(values))
	for _, raw := range values {
		value := raw.(map[string]any)
		name, _ := value["tool_name"].(string)
		result[name] = value
	}
	return result
}
