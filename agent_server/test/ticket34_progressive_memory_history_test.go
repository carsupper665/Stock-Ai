package test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket34MemoryToolsRejectExpiredCrossSessionAndEmptySearch(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	var expiredID, foreignID string
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		switch round.Add(1) {
		case 1:
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed local", "seed local", []any{
				map[string]any{"type": "fact", "summary": "local", "content": "local", "importance": 1},
			}, nil))
		case 2:
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed foreign", "seed foreign", []any{
				map[string]any{"type": "fact", "summary": "foreign", "content": "foreign", "importance": 1},
			}, nil))
		case 3:
			writeToolCall(w, "expired-read", "memory_read", map[string]any{"memory_id": expiredID})
		case 4:
			assertLastToolError(t, request, "MEMORY_NOT_FOUND")
			writeToolCall(w, "foreign-read", "memory_read", map[string]any{"memory_id": foreignID})
		case 5:
			assertLastToolError(t, request, "MEMORY_NOT_FOUND")
			writeToolCall(w, "empty-search", "memory_search", map[string]any{"query": "  "})
		case 6:
			assertLastToolError(t, request, "INVALID_TOOL_ARGUMENTS")
			writeFixtureJSON(w, http.StatusOK, finalResponse("edge reads done", "edge reads done", nil, nil))
		default:
			t.Fatal("unexpected Generate request")
		}
	}
	runtime, _, _ := fixture.open(t, "")
	local := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_local", "name": "local"})
	startRunForHistoryTest(t, runtime, local.ID, 1)
	expiredID, _ = listMemoryDetails(t, runtime.Handler(), local.ID, false)[0]["id"].(string)
	foreign := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_foreign", "name": "foreign"})
	startRunForHistoryTest(t, runtime, foreign.ID, 1)
	foreignID, _ = listMemoryDetails(t, runtime.Handler(), foreign.ID, false)[0]["id"].(string)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+local.ID+"/memories/"+expiredID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("expire local Memory = %d %s", status, raw)
	}
	stopSessionForMemoryTest(t, runtime.Handler(), local.ID)
	startRunForHistoryTest(t, runtime, local.ID, 2)
}

func TestTicket34MissingHistoryToolReturnsFixedErrorWithoutReadRepair(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if round.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", nil, nil))
			return
		}
		writeToolCall(w, "missing-history", "get_run_history", map[string]any{"run_id": 1})
	}
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	historyPath := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	if err := os.Remove(historyPath); err != nil {
		t.Fatalf("remove History shard: %v", err)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start History Tool Run = %d %s", status, raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.HealthError() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if runtime.HealthError() == nil {
		t.Fatal("missing History Tool read did not latch persistence fault")
	}
	run := getRunForTest(t, runtime, session.ID, 2)
	if !strings.Contains(string(run.Progress), "HISTORY_UNAVAILABLE") {
		t.Fatalf("History Tool fixed error absent from progress = %+v", run)
	}
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("History Tool regenerated missing file, stat error = %v", err)
	}
}

func TestTicket34UnknownRunInExistingShardIsNotMisclassifiedAsPersistenceFault(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		switch round.Add(1) {
		case 1:
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", nil, nil))
		case 2:
			writeToolCall(w, "unknown-run", "get_run_history", map[string]any{"run_id": 19})
		case 3:
			assertLastToolError(t, request, "RUN_NOT_FOUND")
			writeFixtureJSON(w, http.StatusOK, finalResponse("not found handled", "not found handled", nil, nil))
		default:
			t.Fatal("unexpected Generate request")
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	startRunForHistoryTest(t, runtime, session.ID, 2)
	if runtime.HealthError() != nil {
		t.Fatalf("unknown Run latched persistence fault: %v", runtime.HealthError())
	}
}

func TestTicket34MalformedAndNoncanonicalHistoryAreRejectedByTool(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(t *testing.T, databasePath, sessionID string)
	}{
		{
			name: "missing required terminal facts",
			corrupt: func(t *testing.T, databasePath, sessionID string) {
				t.Helper()
				path := filepath.Join(databasePath+".history", sessionID+"_1-20.json")
				body := `{"session_id":"` + sessionID + `","start_run_id":1,"end_run_id":20,"runs":[{"run_id":1}]}`
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatalf("write malformed History: %v", err)
				}
			},
		},
		{
			name: "noncanonical indexed range",
			corrupt: func(t *testing.T, databasePath, sessionID string) {
				t.Helper()
				canonicalPath := filepath.Join(databasePath+".history", sessionID+"_1-20.json")
				body, err := os.ReadFile(canonicalPath)
				if err != nil {
					t.Fatalf("read canonical History: %v", err)
				}
				name := sessionID + "_1-21.json"
				body = []byte(strings.Replace(string(body), `"end_run_id":20`, `"end_run_id":21`, 1))
				if err := os.WriteFile(filepath.Join(databasePath+".history", name), body, 0o600); err != nil {
					t.Fatalf("write noncanonical History: %v", err)
				}
				db, err := sql.Open("sqlite", databasePath)
				if err != nil {
					t.Fatalf("open History DB: %v", err)
				}
				defer db.Close()
				if _, err := db.Exec(`UPDATE run_history_files SET end_run_id = 21, file_path = ? WHERE session_id = ? AND start_run_id = 1`, name, sessionID); err != nil {
					t.Fatalf("replace History index: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "agent.db")
			fixture := newRuntimeFixture()
			var round atomic.Int32
			fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
				if round.Add(1) == 1 {
					writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", nil, nil))
					return
				}
				writeToolCall(w, "invalid-history", "get_run_history", map[string]any{"run_id": 1})
			}
			runtime, _, _ := fixture.open(t, databasePath)
			session := createStoppedSession(t, runtime, nil)
			startRunForHistoryTest(t, runtime, session.ID, 1)
			stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
			test.corrupt(t, databasePath, session.ID)
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			if status != http.StatusAccepted {
				t.Fatalf("start History Tool Run = %d %s", status, raw)
			}
			deadline := time.Now().Add(3 * time.Second)
			for runtime.HealthError() == nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if runtime.HealthError() == nil {
				t.Fatal("invalid History Tool read did not latch persistence fault")
			}
			run := getRunForTest(t, runtime, session.ID, 2)
			if !strings.Contains(string(run.Progress), "HISTORY_UNAVAILABLE") {
				t.Fatalf("History Tool fixed error absent from progress = %+v", run)
			}
		})
	}
}

func TestTicket34MemoryHTTPListIsPaginatedMetadataAndDetailIsSessionScoped(t *testing.T) {
	fixture := newRuntimeFixture()
	var generation atomic.Int32
	candidates := make([]any, 0, 101)
	for index := 1; index <= 101; index++ {
		candidates = append(candidates, map[string]any{
			"type": "fact", "summary": fmt.Sprintf("Memory %02d", index),
			"content": fmt.Sprintf("PRIVATE-CONTENT-%02d", index), "importance": (index-1)%3 + 1,
		})
	}
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := int(generation.Add(1))
		if current > 1 {
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			text := contextText(body)
			if strings.Contains(text, "PRIVATE-CONTENT") {
				t.Errorf("large initial context included Memory content: %s", text)
			}
			var runContext map[string]any
			_ = json.Unmarshal([]byte(text), &runContext)
			index, _ := runContext["memory_index"].([]any)
			if len(index) != 20 {
				t.Errorf("initial Memory index length = %d, want bounded 20", len(index))
			}
		}
		start := (current - 1) * 20
		end := start + 20
		if end > len(candidates) {
			end = len(candidates)
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content":    finalizationContent("seeded", "Seeded memories", candidates[start:end], nil),
			"tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	for runID := int64(1); runID <= 6; runID++ {
		startRunForHistoryTest(t, runtime, session.ID, runID)
		if runID < 6 {
			if status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil); status != http.StatusOK {
				t.Fatalf("stop = %d %s", status, raw)
			}
		}
	}

	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/memories?page=1", agentAdminToken, nil)
	if status != http.StatusOK || strings.Contains(string(raw), "PRIVATE-CONTENT") {
		t.Fatalf("Memory metadata page = %d %s", status, raw)
	}
	var firstPage struct {
		Memories []map[string]any `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &firstPage); err != nil || len(firstPage.Memories) != 100 {
		t.Fatalf("first page = %s error=%v", raw, err)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/memories?page=2", agentAdminToken, nil)
	var secondPage struct {
		Memories []map[string]any `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &secondPage); status != http.StatusOK || err != nil || len(secondPage.Memories) != 1 {
		t.Fatalf("second page = %d %s error=%v", status, raw, err)
	}
	memoryID, _ := secondPage.Memories[0]["id"].(string)
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/memories/"+memoryID, agentAdminToken, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), "PRIVATE-CONTENT") {
		t.Fatalf("Memory detail = %d %s", status, raw)
	}
	other := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_other", "name": "other"})
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+other.ID+"/memories/"+memoryID, agentAdminToken, nil)
	if status != http.StatusNotFound || !strings.Contains(string(raw), "MEMORY_NOT_FOUND") {
		t.Fatalf("cross-Session Memory detail = %d %s", status, raw)
	}
	for _, query := range []string{"?page=", "?include_expired=", "?page=1&page=2", "?unknown=true"} {
		status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
			"/api/v1/sessions/"+session.ID+"/memories"+query, agentAdminToken, nil)
		if status != http.StatusBadRequest || !strings.Contains(string(raw), "INVALID_REQUEST") {
			t.Fatalf("invalid Memory list query %q = %d %s", query, status, raw)
		}
	}
}

func TestTicket34AgentProgressivelySearchesReadsMemoryThenOneRunHistory(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	var selectedID string
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		current := round.Add(1)
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		switch current {
		case 1:
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": finalizationContent("first complete", "First Run summary", []any{
					map[string]any{"type": "market", "summary": "Weak breakout", "content": "SELECTED-CONTENT: weak volume invalidated the breakout.", "importance": 3},
					map[string]any{"type": "unrelated", "summary": "Funding note", "content": "UNSELECTED-CONTENT: funding was neutral.", "importance": 1},
				}, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
			})
		case 2:
			context := contextText(body)
			if !strings.Contains(context, `"summary":"First Run summary"`) || !strings.Contains(context, `"summary":"Weak breakout"`) ||
				strings.Contains(context, "SELECTED-CONTENT") || strings.Contains(context, "UNSELECTED-CONTENT") ||
				strings.Contains(context, `"restart_context"`) || strings.Contains(context, `"output":"first complete"`) {
				t.Errorf("initial progressive context = %s", context)
			}
			assertToolDeclared(t, body, "memory_search")
			assertToolDeclared(t, body, "memory_read")
			assertToolDeclared(t, body, "get_run_history")
			writeToolCall(w, "search-memory", "memory_search", map[string]any{"query": "weak", "limit": 10})
		case 3:
			result := lastToolResult(t, body)
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "SELECTED-CONTENT") || strings.Contains(string(raw), "UNSELECTED-CONTENT") {
				t.Errorf("search returned Memory content: %s", raw)
			}
			resultBody, _ := result["result"].(map[string]any)
			memories, _ := resultBody["memories"].([]any)
			if len(memories) != 1 {
				t.Fatalf("search Memories = %s", raw)
			}
			selected, _ := memories[0].(map[string]any)
			selectedID, _ = selected["id"].(string)
			writeToolCall(w, "read-memory", "memory_read", map[string]any{"memory_id": selectedID})
		case 4:
			result := lastToolResult(t, body)
			raw, _ := json.Marshal(result)
			if !strings.Contains(string(raw), "SELECTED-CONTENT") || strings.Contains(string(raw), "UNSELECTED-CONTENT") {
				t.Errorf("Memory read result = %s", raw)
			}
			writeToolCall(w, "read-history", "get_run_history", map[string]any{"run_id": 1})
		case 5:
			result := lastToolResult(t, body)
			raw, _ := json.Marshal(result)
			if !strings.Contains(string(raw), `"run_id":1`) || !strings.Contains(string(raw), `"summary":"First Run summary"`) ||
				!strings.Contains(string(raw), selectedID) {
				t.Errorf("Run History Tool result = %s", raw)
			}
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content":    finalizationContent("progressive read complete", "Progressive read complete", nil, nil),
				"tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
			})
		default:
			t.Fatalf("unexpected Generate round %d", current)
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 5, "max_tool_call": 4})
	startRunForHistoryTest(t, runtime, session.ID, 1)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", status, raw)
	}
	startRunForHistoryTest(t, runtime, session.ID, 2)
	if round.Load() != 5 {
		t.Fatalf("Generate rounds = %d", round.Load())
	}
}

func assertToolDeclared(t *testing.T, body map[string]any, name string) {
	t.Helper()
	tools, _ := body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["name"] == name {
			return
		}
	}
	t.Fatalf("Tool %s was not declared", name)
}

func assertLastToolError(t *testing.T, request *http.Request, code string) {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode Tool follow-up: %v", err)
	}
	result := lastToolResult(t, body)
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"code":"`+code+`"`) {
		t.Fatalf("Tool error = %s, want %s", raw, code)
	}
}

func startRunForHistoryTest(t *testing.T, runtime *common.AgentRuntime, sessionID string, runID int64) {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+sessionID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run %d status=%d body=%s", runID, status, raw)
	}
	waitForRunStatus(t, runtime, sessionID, runID, "completed")
}
