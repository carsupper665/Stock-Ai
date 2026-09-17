package test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type ticket39Backend struct {
	mu               sync.Mutex
	requests         []string
	positionsStatus  int
	managementStatus int
}

func (backend *ticket39Backend) serve(w http.ResponseWriter, request *http.Request) {
	backend.mu.Lock()
	backend.requests = append(backend.requests, request.Method+" "+request.URL.RequestURI()+" "+request.Header.Get("Authorization"))
	positionsStatus, managementStatus := backend.positionsStatus, backend.managementStatus
	backend.mu.Unlock()
	switch {
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/accounts/"):
		if managementStatus != 0 {
			writeFixtureJSON(w, managementStatus, map[string]any{"error": "fixture_failure", "message": "management unavailable"})
			return
		}
		id := strings.TrimPrefix(request.URL.Path, "/v1/accounts/")
		writeFixtureJSON(w, http.StatusOK, map[string]any{"id": id, "status": "active", "token": "account-secret"})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/account":
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"id": testAccountID, "user_name": "monitor", "initial_balance": 1000, "balance": 1075,
			"status": "active", "created_at": "2026-09-15T01:00:00Z", "locked_margin": 50,
			"available": 1025, "unrealized_pnl": 25, "equity": 1100, "token": "must-never-leak",
		})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/positions":
		if positionsStatus != 0 {
			writeFixtureJSON(w, positionsStatus, map[string]any{"error": "market_unavailable", "message": "positions unavailable"})
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"positions": []any{
			map[string]any{"id": "pos_1", "symbol": "BTCUSDT", "unrealized_pnl": 25, "stop_loss_source": map[string]any{"session_id": "s_other", "run_id": 8}},
		}})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/ledger":
		writeFixtureJSON(w, http.StatusOK, map[string]any{"entries": []any{
			map[string]any{"seq": 9, "event": "order_filled", "order_id": "ord_1", "source": map[string]any{"session_id": "s_agent", "run_id": 3}},
			map[string]any{"seq": 8, "event": "order_filled", "order_id": "ord_manual", "source": nil},
		}})
	default:
		writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
	}
}

func (backend *ticket39Backend) resetRequests() {
	backend.mu.Lock()
	backend.requests = nil
	backend.mu.Unlock()
}

func (backend *ticket39Backend) requestSnapshot() []string {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]string(nil), backend.requests...)
}

func TestTicket39SessionDetailAggregatesLocalMonitoringAndActualBackendState(t *testing.T) {
	runtime, backend, databasePath := openTicket39Runtime(t)
	session := createStoppedSession(t, runtime, nil)
	seedTicket39Monitoring(t, databasePath, session.ID)
	backend.resetRequests()

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("aggregate detail = %d %s", status, raw)
	}
	if strings.Contains(string(raw), "account-secret") || strings.Contains(string(raw), "must-never-leak") {
		t.Fatalf("aggregate leaked Backend credential: %s", raw)
	}
	detail := decodeMap(t, raw)
	if detail["id"] != session.ID || detail["status"] != "running" || detail["current_run"] != nil {
		t.Fatalf("Session/status/current Run = %#v", detail)
	}
	last := detail["last_run"].(map[string]any)
	if last["status"] != "failed" || last["summary"] != nil || last["reason"] != "GATEWAY_ERROR: visible failure" {
		t.Fatalf("last Run fallback = %#v", last)
	}
	if detail["active_event_count"] != float64(1) || detail["pending_trigger_count"] != float64(1) || detail["total_runs"] != float64(1) || detail["total_tool_calls"] != float64(2) {
		t.Fatalf("local counts = %#v", detail)
	}
	if events := detail["active_events"].([]any); len(events) != 1 {
		t.Fatalf("active Events = %#v", events)
	}
	memory := detail["memory_summary"].(map[string]any)
	if memory["active_count"] != float64(1) || memory["expired_count"] != float64(1) {
		t.Fatalf("Memory summary = %#v", memory)
	}
	usage := detail["usage"].(map[string]any)
	if usage["model_calls"] != float64(1) || usage["total_tokens"] != float64(12) {
		t.Fatalf("Usage summary = %#v", usage)
	}
	trading := detail["trading"].(map[string]any)
	if trading["account"] == nil || len(trading["positions"].([]any)) != 1 || trading["position_count"] != float64(1) {
		t.Fatalf("Trading aggregate = %#v", trading)
	}
	pnl := trading["pnl"].(map[string]any)
	if pnl["net_realized"] != float64(75) || pnl["unrealized"] != float64(25) || pnl["total"] != float64(100) || pnl["scope"] != "account" {
		t.Fatalf("PnL = %#v", pnl)
	}
	if detail["runtime_health"].(map[string]any)["status"] != "ok" {
		t.Fatalf("Runtime health = %#v", detail["runtime_health"])
	}
	requests := backend.requestSnapshot()
	if len(requests) != 3 || !containsRequest39(requests, "GET /v1/accounts/"+testAccountID+" Bearer "+backendUserToken) ||
		!containsRequest39(requests, "GET /v1/account Bearer account-secret") || !containsRequest39(requests, "GET /v1/positions Bearer account-secret") {
		t.Fatalf("Backend aggregate requests = %#v", requests)
	}
}

func TestTicket39LedgerAndPositionsUseBackendHTTPAndPreserveSourceAudit(t *testing.T) {
	runtime, backend, _ := openTicket39Runtime(t)
	session := createStoppedSession(t, runtime, nil)
	backend.resetRequests()

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/ledger?before_seq=10", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("Session Ledger = %d %s", status, raw)
	}
	ledger := decodeMap(t, raw)
	if ledger["scope"] != "account" || ledger["page_size"] != float64(100) || ledger["before_seq"] != float64(10) {
		t.Fatalf("Ledger envelope = %#v", ledger)
	}
	entries := ledger["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["source"] == nil || entries[1].(map[string]any)["source"] != nil {
		t.Fatalf("Ledger sources = %#v", entries)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/positions", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("Session Positions = %d %s", status, raw)
	}
	positions := decodeMap(t, raw)
	if positions["scope"] != "account" || len(positions["positions"].([]any)) != 1 {
		t.Fatalf("Positions envelope = %#v", positions)
	}
	requests := backend.requestSnapshot()
	if !containsRequest39(requests, "GET /v1/ledger?before_seq=10&limit=100 Bearer account-secret") ||
		!containsRequest39(requests, "GET /v1/positions Bearer account-secret") {
		t.Fatalf("Backend monitoring requests = %#v", requests)
	}
	for _, request := range requests {
		if strings.Contains(request, "session_id=") {
			t.Fatalf("account Ledger was incorrectly filtered, hiding unattributed changes: %s", request)
		}
	}
}

func TestTicket39BackendFailureIsExplicitAndDoesNotReplaceLiveStateWithSummary(t *testing.T) {
	runtime, backend, _ := openTicket39Runtime(t)
	session := createStoppedSession(t, runtime, nil)
	backend.mu.Lock()
	backend.positionsStatus = http.StatusServiceUnavailable
	backend.mu.Unlock()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("partial aggregate = %d %s", status, raw)
	}
	trading := decodeMap(t, raw)["trading"].(map[string]any)
	if trading["account"] == nil || trading["positions"] != nil || trading["pnl"] == nil {
		t.Fatalf("partial Trading fields = %#v", trading)
	}
	errorsBody := trading["errors"].(map[string]any)
	if errorsBody["account"] != nil || errorsBody["positions"].(map[string]any)["code"] != "BACKEND_UNAVAILABLE" {
		t.Fatalf("partial Trading errors = %#v", errorsBody)
	}

	backend.mu.Lock()
	backend.managementStatus = http.StatusServiceUnavailable
	backend.mu.Unlock()
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID+"/positions", agentAdminToken, nil)
	if status != http.StatusBadGateway || !strings.Contains(string(raw), `"error":"BACKEND_UNAVAILABLE"`) {
		t.Fatalf("unavailable Positions = %d %s", status, raw)
	}
}

func TestTicket39SessionListHasFixedPaginationAndExplicitDeletedVisibility(t *testing.T) {
	runtime, _, databasePath := openTicket39Runtime(t)
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open list fixture database: %v", err)
	}
	defer db.Close()
	for i := 0; i < 101; i++ {
		status := "stopped"
		if i == 100 {
			status = "deleted"
		}
		id := fmt.Sprintf("s_list_%03d", i)
		created := time.Date(2026, 9, 15, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO sessions
			(id,name,account_id,model_name,prompt,max_loop,max_tool_call,status,current_run_id,created_at,deleted_at)
			VALUES (?,?,?,?,?,4,3,?,0,?,?)`, id, id, "acc_"+id, "fixture-model", "mission", status, created, nullableDeleted39(status, created)); err != nil {
			t.Fatalf("insert Session %d: %v", i, err)
		}
	}

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions?page=1", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("default Session list = %d %s", status, raw)
	}
	listed := decodeMap(t, raw)
	if listed["page_size"] != float64(100) || len(listed["sessions"].([]any)) != 100 || listed["include_deleted"] != false {
		t.Fatalf("default pagination = %#v", listed)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions?page=2&include_deleted=true", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("deleted-visible Session list = %d %s", status, raw)
	}
	listed = decodeMap(t, raw)
	if len(listed["sessions"].([]any)) != 1 || listed["include_deleted"] != true {
		t.Fatalf("deleted-visible pagination = %#v", listed)
	}
}

func openTicket39Runtime(t *testing.T) (*common.AgentRuntime, *ticket39Backend, string) {
	t.Helper()
	backendFixture := &ticket39Backend{}
	backend := httptest.NewServer(http.HandlerFunc(backendFixture.serve))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/v1/models" {
			writeFixtureJSON(w, http.StatusOK, map[string]any{"models": []any{map[string]any{
				"model_name": "fixture-model", "provider_id": "fixture", "model": "provider-model",
				"levels": map[string]any{}, "enabled": true, "available": true,
			}}})
			return
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("done", "done", nil, nil))
	}))
	t.Cleanup(backend.Close)
	t.Cleanup(gateway.Close)
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: databasePath, AdminToken: agentAdminToken,
		BackendURL: backend.URL, BackendUserToken: backendUserToken,
		GatewayURL: gateway.URL, GatewayRuntimeToken: gatewayToken,
		DefaultPrompt: "mission", DefaultMaxLoop: 4, DefaultMaxToolCall: 3,
		RequestTimeout: time.Second, ToolTimeout: time.Second, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Open Ticket 39 Runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, backendFixture, databasePath
}

func seedTicket39Monitoring(t *testing.T, databasePath, sessionID string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open aggregate seed database: %v", err)
	}
	defer db.Close()
	progress := `[{"type":"model_call","iteration":1,"model_name":"fixture-model","provider_id":"fixture","model":"provider-model","usage":{"input_tokens":10,"output_tokens":2,"cached_tokens":1,"total_tokens":12}},{"type":"tool_call","tool_name":"get_account","result":{"ok":true,"result":{}}},{"type":"tool_call","tool_name":"place_order","result":{"ok":false,"error":{"code":"invalid_request","outcome":"not_executed"}}}]`
	if _, err := db.Exec(`INSERT INTO runs
		(session_id,run_id,status,trigger,summary,output,error,progress_json,model_call_count,tool_call_count,start_time,end_time)
		VALUES (?,1,'failed','session_start',NULL,NULL,'GATEWAY_ERROR: visible failure',?,1,2,'2026-09-15T01:00:00Z','2026-09-15T01:01:00Z')`, sessionID, progress); err != nil {
		t.Fatalf("seed aggregate Run: %v", err)
	}
	if _, err := db.Exec(`UPDATE sessions SET status='running',current_run_id=1,started_at='2026-09-15T01:00:00Z' WHERE id=?`, sessionID); err != nil {
		t.Fatalf("seed waiting Session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO events (id,owner_session_id,created_run_id,type,params_json,due_at,created_at,expires_at)
		VALUES ('ev_monitor',?,1,'timer','{"at":"2099-01-01T00:00:00.000000000Z"}','2099-01-01T00:00:00.000000000Z','2026-09-15T01:00:00.000000000Z','2099-01-01T01:00:00.000000000Z')`, sessionID); err != nil {
		t.Fatalf("seed aggregate Event: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO pending_triggers (session_id,payload_json,created_at) VALUES (?, '{}', '2026-09-15T01:00:00.000000000Z')`, sessionID); err != nil {
		t.Fatalf("seed aggregate pending Trigger: %v", err)
	}
	for index, expired := range []int{0, 1} {
		id := "m_" + strings.Repeat(string(rune('a'+index)), 32)
		if _, err := db.Exec(`INSERT INTO memories
			(id,session_id,run_id,candidate_index,type,summary,content,importance,run_start_at,run_end_at,is_expired,created_at,updated_at)
			VALUES (?,?,1,?,'fact','summary','content',1,'2026-09-15T01:00:00Z','2026-09-15T01:01:00Z',?,'2026-09-15T01:01:00Z','2026-09-15T01:01:00Z')`, id, sessionID, index, expired); err != nil {
			t.Fatalf("seed aggregate Memory: %v", err)
		}
	}
}

func containsRequest39(requests []string, wanted string) bool {
	for _, request := range requests {
		if request == wanted {
			return true
		}
	}
	return false
}

func nullableDeleted39(status, at string) any {
	if status == "deleted" {
		return at
	}
	return nil
}
