package test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "stock-ai/agent-server/internal/agent"
)

func TestTicket34MissingHistoryToolReturnsFixedErrorWithoutReadRepair(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if round.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed"))
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
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed"))
		case 2:
			writeToolCall(w, "unknown-run", "get_run_history", map[string]any{"run_id": 19})
		case 3:
			assertLastToolError(t, request, "RUN_NOT_FOUND")
			writeFixtureJSON(w, http.StatusOK, finalResponse("not found handled", "not found handled"))
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
					writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed"))
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

func startRunForHistoryTest(t *testing.T, runtime *agent.AgentRuntime, sessionID string, runID int64) {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+sessionID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run %d status=%d body=%s", runID, status, raw)
	}
	waitForRunStatus(t, runtime, sessionID, runID, "completed")
}
