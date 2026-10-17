package test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// cabinetScript answers main Generate calls from steps and records every request, so a test
// can read the tool results the Runtime fed back to the model.
type cabinetScript struct {
	mu       sync.Mutex
	requests []map[string]any
}

func (s *cabinetScript) handler(t *testing.T, step func(call int) map[string]any) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode generate request: %v", err)
		}
		s.mu.Lock()
		s.requests = append(s.requests, body)
		call := len(s.requests)
		s.mu.Unlock()
		writeFixtureJSON(w, http.StatusOK, step(call))
	}
}

// toolResults returns the tool messages of request call (1-based), decoded.
func (s *cabinetScript) toolResults(t *testing.T, call int) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]map[string]any, 0)
	messages := requestMessages(s.requests[call-1])
	for index := len(messages) - 1; index >= 0 && messages[index]["role"] == "tool"; index-- {
		results = append([]map[string]any{decodeMap(t, []byte(messageText(messages[index])))}, results...)
	}
	return results
}

func (s *cabinetScript) context(t *testing.T, call int) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return decodeMap(t, []byte(messageText(requestMessages(s.requests[call-1])[1])))
}

func memoryCall(id, name string, arguments map[string]any) map[string]any {
	return toolCall(id, name, arguments)
}

func resultOf(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	if envelope["ok"] != true {
		t.Fatalf("tool failed: %+v", envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	return result
}

func errorCode(envelope map[string]any) string {
	failure, _ := envelope["error"].(map[string]any)
	code, _ := failure["code"].(string)
	return code
}

func openCabinetDB(t *testing.T, databasePath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open cabinet database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedMemory(t *testing.T, db *sql.DB, sessionID, title, msg string, bringBack bool, createdAt time.Time) {
	t.Helper()
	at := createdAt.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if _, err := db.Exec(`INSERT INTO memories (session_id, title, msg, bring_back, created_at, updated_at, created_run_id, updated_run_id)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL)`, sessionID, title, msg, bringBack, at, at); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
}

func TestMemoryCabinetToolsSaveListGetAndEditDuringOneRun(t *testing.T) {
	fixture := newRuntimeFixture()
	script := &cabinetScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		switch call {
		case 1:
			return toolStep("save two",
				memoryCall("save-1", "memory_save", map[string]any{"title": "BTC 假突破教訓", "msg": "量能不足的突破不要追", "bring_back": true}),
				memoryCall("save-2", "memory_save", map[string]any{"title": "下輪計畫", "msg": "觀察 ETH"}))
		case 2:
			return toolStep("look", memoryCall("list", "memory_list", map[string]any{}), memoryCall("get", "memory_get", map[string]any{"id": 1}))
		case 3:
			return toolStep("fix plan", memoryCall("edit", "memory_edit", map[string]any{"id": 2, "msg": "觀察 ETH 4h 收盤"}))
		default:
			return finalResponse("done", "saved lessons")
		}
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 6, "max_tool_call": 10})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	saved := script.toolResults(t, 2)
	if first := resultOf(t, saved[0]); first["id"] != float64(1) || first["bring_back"] != true {
		t.Fatalf("save result = %+v", saved)
	}
	looked := script.toolResults(t, 3)
	listed := resultOf(t, looked[0])
	labels, _ := listed["memories"].([]any)
	if len(labels) != 2 || listed["stored"] != float64(2) || listed["capacity"] != float64(60) {
		t.Fatalf("memory_list = %+v", listed)
	}
	for _, raw := range labels {
		label := raw.(map[string]any)
		if len(label) != 3 || label["title"] == nil || label["created_at"] == nil {
			t.Fatalf("labels must carry only id, title and created_at: %+v", label)
		}
	}
	if labels[0].(map[string]any)["id"] != float64(2) {
		t.Fatalf("labels must be newest first: %+v", labels)
	}
	opened := resultOf(t, looked[1])
	if opened["msg"] != "量能不足的突破不要追" || opened["bring_back"] != true || opened["updated_at"] != nil || opened["created_run_id"] != nil {
		t.Fatalf("memory_get must return content without system fields: %+v", opened)
	}
	if edited := resultOf(t, script.toolResults(t, 4)[0]); edited["id"] != float64(2) {
		t.Fatalf("memory_edit = %+v", edited)
	}

	listedHTTP := listMemoryDetails(t, runtime.Handler(), session.ID)
	if len(listedHTTP) != 2 || listedHTTP[0]["msg"] != nil || listedHTTP[0]["created_run_id"] != float64(1) {
		t.Fatalf("HTTP list = %+v", listedHTTP)
	}
	detail := getMemoryDetail(t, runtime.Handler(), session.ID, 2)
	if detail["msg"] != "觀察 ETH 4h 收盤" || detail["updated_run_id"] != float64(1) || detail["title"] != "下輪計畫" {
		t.Fatalf("HTTP detail = %+v", detail)
	}
	status, raw := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if status != http.StatusOK || !strings.Contains(string(raw), `"memory_ids":["1","2"]`) {
		t.Fatalf("History shard memory_ids = %d %s", status, raw)
	}
}

func TestMemoryCabinetNextRunStartsWithBringBackRecentLabelsAndTime(t *testing.T) {
	fixture := newRuntimeFixture()
	script := &cabinetScript{}
	saves := make([]map[string]any, 0, 12)
	for index := 1; index <= 12; index++ {
		saves = append(saves, memoryCall("save-"+jsonNumber(int64(index)), "memory_save", map[string]any{
			"title": "note " + jsonNumber(int64(index)), "msg": "body " + jsonNumber(int64(index)), "bring_back": index == 1,
		}))
	}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("save", saves...)
		}
		return finalResponse("done", "run summary "+jsonNumber(int64(call)))
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4, "max_tool_call": 12})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	startRunForHistoryTest(t, runtime, session.ID, 2)

	context := script.context(t, 3)
	if _, err := time.Parse(time.RFC3339, context["current_time"].(string)); err != nil {
		t.Fatalf("current_time = %v", context["current_time"])
	}
	index, _ := context["memory_index"].([]any)
	if len(index) != 10 || index[0].(map[string]any)["id"] != float64(12) || index[9].(map[string]any)["id"] != float64(3) {
		t.Fatalf("memory_index must be the 10 newest labels: %+v", index)
	}
	recalled, _ := context["bring_back_memories"].([]any)
	if len(recalled) != 1 || recalled[0].(map[string]any)["msg"] != "body 1" {
		t.Fatalf("bring_back_memories = %+v", recalled)
	}
	if recent, _ := context["recent_run_summary"].(map[string]any); recent["summary"] != "run summary 2" {
		t.Fatalf("recent_run_summary = %+v", context["recent_run_summary"])
	}
	script.mu.Lock()
	system := messageText(requestMessages(script.requests[2])[0])
	script.mu.Unlock()
	if !strings.Contains(system, "Memory cabinet") || !strings.Contains(system, "Do not store trades") {
		t.Fatalf("system prompt lacks cabinet guidance: %s", system)
	}
}

func TestMemoryCabinetEnforcesLengthPinnedBudgetAndCapacity(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	script := &cabinetScript{}
	full := strings.Repeat("滿", 800)
	fixture.generate = script.handler(t, func(call int) map[string]any {
		switch call {
		case 1:
			return toolStep("limits",
				memoryCall("too-long", "memory_save", map[string]any{"title": "t", "msg": full + "x"}),
				memoryCall("pin-1", "memory_save", map[string]any{"title": "p1", "msg": full, "bring_back": true}),
				memoryCall("pin-2", "memory_save", map[string]any{"title": "p2", "msg": full, "bring_back": true}),
				memoryCall("pin-3", "memory_save", map[string]any{"title": "p3", "msg": full, "bring_back": true}),
				memoryCall("pin-4", "memory_save", map[string]any{"title": "p4", "msg": strings.Repeat("超", 700), "bring_back": true}),
				memoryCall("plain", "memory_save", map[string]any{"title": "plain", "msg": strings.Repeat("平", 700)}),
				memoryCall("pin-plain", "memory_edit", map[string]any{"id": 4, "bring_back": true}),
				memoryCall("pin-fits", "memory_save", map[string]any{"title": "p5", "msg": strings.Repeat("可", 600), "bring_back": true}))
		default:
			return finalResponse("done", "limits checked")
		}
	})
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4, "max_tool_call": 10})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	results := script.toolResults(t, 2)
	wanted := []string{"INVALID_TOOL_ARGUMENTS", "", "", "", "BRING_BACK_LIMIT", "", "BRING_BACK_LIMIT", ""}
	for index, code := range wanted {
		if errorCode(results[index]) != code {
			t.Fatalf("result %d = %+v, want error %q", index, results[index], code)
		}
	}

	db := openCabinetDB(t, databasePath)
	for index := 0; index < 55; index++ {
		seedMemory(t, db, session.ID, "filler", "x", false, time.Now())
	}
	script.mu.Lock()
	script.requests = nil
	script.mu.Unlock()
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("full", memoryCall("over", "memory_save", map[string]any{"title": "61", "msg": "no room"}),
				memoryCall("recycle", "memory_edit", map[string]any{"id": 5, "title": "reused", "msg": "still works"}))
		}
		return finalResponse("done", "capacity checked")
	})
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	startRunForHistoryTest(t, runtime, session.ID, 2)
	results = script.toolResults(t, 2)
	if errorCode(results[0]) != "MEMORY_FULL" || !strings.Contains(results[0]["error"].(map[string]any)["message"].(string), "memory_edit") ||
		errorCode(results[1]) != "" {
		t.Fatalf("capacity results = %+v", results)
	}
}

func TestMemoryCabinetListFiltersByTitleTimeAndIDWithTwentyCap(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4, "max_tool_call": 10})
	db := openCabinetDB(t, databasePath)
	now := time.Now().UTC()
	seedMemory(t, db, session.ID, "BTC 背離", "a", false, time.Date(2026, 6, 5, 3, 0, 0, 0, time.UTC))
	seedMemory(t, db, session.ID, "ETH 風控", "b", false, time.Date(2026, 6, 7, 23, 0, 0, 0, time.UTC))
	seedMemory(t, db, session.ID, "btc 量能", "c", false, time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC))
	seedMemory(t, db, session.ID, "近期一", "d", false, now.Add(-10*time.Hour))
	seedMemory(t, db, session.ID, "近期二", "e", false, now.Add(-time.Hour))
	for index := 0; index < 20; index++ {
		seedMemory(t, db, session.ID, "old filler", "f", false, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	}
	script := &cabinetScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("filters",
				memoryCall("query", "memory_list", map[string]any{"query": "BTC"}),
				memoryCall("dates", "memory_list", map[string]any{"from": "2026-06-05", "to": "2026-06-07"}),
				memoryCall("hours", "memory_list", map[string]any{"within_hours": 5}),
				memoryCall("ids", "memory_list", map[string]any{"after_id": 1, "before_id": 4}),
				memoryCall("all", "memory_list", map[string]any{}),
				memoryCall("bad", "memory_list", map[string]any{"limit": 21}))
		}
		return finalResponse("done", "filters checked")
	})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	results := script.toolResults(t, 2)
	ids := func(envelope map[string]any) []float64 {
		labels, _ := resultOf(t, envelope)["memories"].([]any)
		out := make([]float64, 0, len(labels))
		for _, label := range labels {
			out = append(out, label.(map[string]any)["id"].(float64))
		}
		return out
	}
	for index, want := range [][]float64{{3, 1}, {2, 1}, {5}, {3, 2}} {
		if got := ids(results[index]); !equalFloats(got, want) {
			t.Fatalf("filter %d ids = %v, want %v", index, got, want)
		}
	}
	if got := ids(results[4]); len(got) != 20 || got[0] != 25 || resultOf(t, results[4])["stored"] != float64(25) {
		t.Fatalf("unfiltered list must cap at 20 newest: %v", got)
	}
	if errorCode(results[5]) != "INVALID_TOOL_ARGUMENTS" {
		t.Fatalf("limit above 20 = %+v", results[5])
	}
}

func equalFloats(left, right []float64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestMemoryCabinetSavedMidRunSurvivesAFailedRun(t *testing.T) {
	fixture := newRuntimeFixture()
	script := &cabinetScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("note first", memoryCall("save", "memory_save", map[string]any{"title": "工具壞掉", "msg": "get_ohlcv 逾時"}))
		}
		return map[string]any{"content": `{"status":"failed","output":"market data unavailable","summary":"blocked"}`,
			"tool_calls": []any{}, "finish_reason": "stop", "usage": nil}
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if run.Error == nil || !strings.Contains(*run.Error, "TASK_FAILED") {
		t.Fatalf("Run = %+v", run)
	}
	if memories := listMemoryDetails(t, runtime.Handler(), session.ID); len(memories) != 1 || memories[0]["title"] != "工具壞掉" {
		t.Fatalf("mid-Run memory lost: %+v", memories)
	}
}

func TestMemoryCabinetFinalizationRejectsRetiredMemoryFields(t *testing.T) {
	fixture := newRuntimeFixture()
	script := &cabinetScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return map[string]any{"content": `{"output":"done","summary":"done","candidate_memories":[],"expire_memories":[]}`,
				"tool_calls": []any{}, "finish_reason": "stop", "usage": nil}
		}
		return finalResponse("done", "done")
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if !strings.Contains(string(run.Progress), "status, output and summary only") {
		t.Fatalf("retired fields were not repaired: %s", run.Progress)
	}
}

func TestMemoryCabinetHTTPIsSessionScopedAndDeleteIsHard(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, databasePath)
	local := createStoppedSession(t, runtime, nil)
	foreign := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_foreign", "name": "foreign"})
	db := openCabinetDB(t, databasePath)
	seedMemory(t, db, local.ID, "local", "local msg", true, time.Now())
	path := "/api/v1/sessions/"

	if status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, path+local.ID+"/memories?page=1", agentAdminToken, nil); status != http.StatusBadRequest {
		t.Fatalf("query parameters = %d %s", status, raw)
	}
	if memories := listMemoryDetails(t, runtime.Handler(), foreign.ID); len(memories) != 0 {
		t.Fatalf("foreign cabinet = %+v", memories)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if status, raw := runtimeRequest(t, runtime.Handler(), method, path+foreign.ID+"/memories/1", agentAdminToken, nil); status != http.StatusNotFound {
			t.Fatalf("cross-Session %s = %d %s", method, status, raw)
		}
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, path+local.ID+"/memories/1", agentAdminToken, nil)
	if status != http.StatusOK || decodeMap(t, raw)["msg"] != "local msg" {
		t.Fatalf("DELETE = %d %s", status, raw)
	}
	if status, _ := runtimeRequest(t, runtime.Handler(), http.MethodGet, path+local.ID+"/memories/1", agentAdminToken, nil); status != http.StatusNotFound {
		t.Fatalf("deleted memory still readable: %d", status)
	}

	seedMemory(t, db, local.ID, "kept", "kept", false, time.Now())
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, path+local.ID+"/memories/2", agentAdminToken, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("DELETE on closed Runtime = %d %s", status, raw)
	}
	reopened, _, _ := newRuntimeFixture().open(t, databasePath)
	if detail := getMemoryDetail(t, reopened.Handler(), local.ID, 2); detail["title"] != "kept" {
		t.Fatalf("closed Runtime mutated memory: %+v", detail)
	}
}

func TestMemoryCabinetMigratesUnexpiredLegacyMemories(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := newRuntimeFixture().open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	db := openCabinetDB(t, databasePath)
	for _, statement := range []string{
		`DROP TABLE memories`,
		`CREATE TABLE memories (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, run_id INTEGER NOT NULL,
			candidate_index INTEGER NOT NULL, type TEXT NOT NULL, summary TEXT NOT NULL,
			content TEXT NOT NULL, importance INTEGER NOT NULL, run_start_at TEXT NOT NULL, run_end_at TEXT NOT NULL,
			is_expired INTEGER NOT NULL DEFAULT 0, expired_at TEXT, expired_source TEXT, expired_run_id INTEGER,
			expiration_reason TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("build legacy table: %v", err)
		}
	}
	for index, expired := range []int{0, 1} {
		if _, err := db.Exec(`INSERT INTO memories VALUES (?, ?, 1, ?, 'lesson', ?, ?, 3, 'x', 'x', ?, NULL, NULL, NULL, NULL,
			'2026-09-15T01:01:00.5Z', '2026-09-15T01:01:00.5Z')`, "m_"+strings.Repeat(jsonNumber(int64(index)), 32), session.ID, index,
			"legacy summary "+jsonNumber(int64(index)), strings.Repeat("長", 900), expired); err != nil {
			t.Fatalf("seed legacy memory: %v", err)
		}
	}
	db.Close()

	reopened, _, _ := newRuntimeFixture().open(t, databasePath)
	memories := listMemoryDetails(t, reopened.Handler(), session.ID)
	if len(memories) != 1 || memories[0]["title"] != "legacy summary 0" || memories[0]["bring_back"] != false ||
		memories[0]["created_at"] != "2026-09-15T01:01:00.500Z" {
		t.Fatalf("migrated cabinet = %+v", memories)
	}
	detail := getMemoryDetail(t, reopened.Handler(), session.ID, memories[0]["id"])
	if msg := detail["msg"].(string); len([]rune(msg)) != 800 {
		t.Fatalf("migrated msg runes = %d", len([]rune(msg)))
	}
	var legacyRows int
	if err := openCabinetDB(t, databasePath).QueryRow(`SELECT COUNT(*) FROM memories_legacy`).Scan(&legacyRows); err != nil || legacyRows != 2 {
		t.Fatalf("legacy table kept rows = %d, %v", legacyRows, err)
	}
}

func TestMemoryCabinetDeleteKeepsHistoryCreationID(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	script := &cabinetScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("save", memoryCall("save", "memory_save", map[string]any{"title": "lesson", "msg": "remember this"}))
		}
		return finalResponse("done", "done")
	})
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	_, before := getHistoryShard(t, runtime.Handler(), session.ID, 1)
	if !strings.Contains(string(before), `"memory_ids":["1"]`) {
		t.Fatalf("missing initial ID: %s", before)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/1", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("delete = %d %s", status, raw)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, _, _ := newRuntimeFixture().open(t, databasePath)
	_, after := getHistoryShard(t, reopened.Handler(), session.ID, 1)
	if !strings.Contains(string(after), `"memory_ids":["1"]`) {
		t.Fatalf("creation ID lost after DELETE and restart: %s", after)
	}
}

func TestMemoryCabinetLegacyHistoryKeepsOriginalMemoryIDs(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusOK, finalResponse("done", "done"))
	}
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	db := openCabinetDB(t, databasePath)
	for _, statement := range []string{
		`DROP TABLE memories`,
		`CREATE TABLE memories (
		 id TEXT PRIMARY KEY, session_id TEXT NOT NULL, run_id INTEGER NOT NULL,
		 candidate_index INTEGER NOT NULL, summary TEXT NOT NULL, content TEXT NOT NULL,
		 is_expired INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	oldID := "m_" + strings.Repeat("1", 32)
	if _, err := db.Exec(`INSERT INTO memories VALUES (?, ?, 1, 0, 'lesson', 'content', 0, '2026-09-15T01:01:00Z', '2026-09-15T01:01:00Z')`, oldID, session.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	historyPath := filepath.Join(databasePath+".history", session.ID+"_1-20.json")
	body, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	var shard map[string]any
	if err := json.Unmarshal(body, &shard); err != nil {
		t.Fatal(err)
	}
	shard["runs"].([]any)[0].(map[string]any)["memory_ids"] = []string{oldID}
	body, err = json.Marshal(shard)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, _, _ := newRuntimeFixture().open(t, databasePath)
	_, after := getHistoryShard(t, reopened.Handler(), session.ID, 1)
	if !strings.Contains(string(after), oldID) {
		t.Fatalf("legacy ID replaced during startup migration: %s", after)
	}
}

type blockingResponseWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingResponseWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func TestMemoryCabinetDeleteReleasesMutexBeforeWritingResponse(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	runtime, _, _ := newRuntimeFixture().open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	db := openCabinetDB(t, databasePath)
	seedMemory(t, db, session.ID, "lesson", "body", false, time.Now())
	writer := &blockingResponseWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/1", nil)
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	done := make(chan struct{})
	go func() { defer close(done); runtime.Handler().ServeHTTP(writer, request) }()
	defer func() { close(writer.release); <-done }()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("DELETE did not reach response")
	}
	healthDone := make(chan struct{})
	go func() { runtime.HealthError(); close(healthDone) }()
	select {
	case <-healthDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("slow DELETE response blocks Runtime health mutex")
	}
}

func TestMemoryCabinetBringBackBudgetCountsNULCharacters(t *testing.T) {
	fixture := newRuntimeFixture()
	script := &cabinetScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		if call == 1 {
			calls := make([]map[string]any, 0, 4)
			for i := 1; i <= 4; i++ {
				calls = append(calls, memoryCall("save-"+jsonNumber(int64(i)), "memory_save", map[string]any{"title": "pinned", "msg": "\x00" + strings.Repeat("x", 799), "bring_back": true}))
			}
			return toolStep("save", calls...)
		}
		return finalResponse("done", "done")
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 4, "max_tool_call": 10})
	startRunForHistoryTest(t, runtime, session.ID, 1)
	results := script.toolResults(t, 2)
	if len(results) != 4 {
		t.Fatalf("expected four tool results, got %d", len(results))
	}
	if errorCode(results[3]) == "" {
		t.Fatalf("four pinned 800-rune messages passed 3000-rune budget: %+v", results)
	}
}
