package test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket27OpenRuntimeMigratesLegacyRunsWithoutDataLoss(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy-agent.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, account_id TEXT NOT NULL UNIQUE,
			model_name TEXT NOT NULL, model_level TEXT, prompt TEXT NOT NULL,
			max_loop INTEGER NOT NULL, max_tool_call INTEGER NOT NULL,
			status TEXT NOT NULL, current_run_id INTEGER NOT NULL,
			restart_context TEXT, created_at TEXT NOT NULL, started_at TEXT,
			stopped_at TEXT, deleted_at TEXT
		)`,
		`CREATE TABLE runs (
			session_id TEXT NOT NULL, run_id INTEGER NOT NULL, status TEXT NOT NULL,
			trigger TEXT NOT NULL, summary TEXT, output TEXT, error TEXT,
			progress_json TEXT NOT NULL, model_call_count INTEGER NOT NULL,
			tool_call_count INTEGER NOT NULL, start_time TEXT NOT NULL, end_time TEXT,
			PRIMARY KEY (session_id, run_id)
		)`,
		`INSERT INTO sessions (id, name, account_id, model_name, prompt, max_loop,
			max_tool_call, status, current_run_id, created_at, started_at, stopped_at)
			VALUES ('s_legacy', 'legacy session', 'acc_legacy', 'legacy-model', 'legacy prompt',
			4, 3, 'stopped', 1, '2026-09-13T10:00:00Z', '2026-09-13T10:01:00Z', '2026-09-13T10:02:00Z')`,
		`INSERT INTO runs (session_id, run_id, status, trigger, summary, output, error,
			progress_json, model_call_count, tool_call_count, start_time, end_time)
			VALUES ('s_legacy', 1, 'completed', 'session_start', 'legacy summary',
			'legacy output', NULL, '[]', 1, 0, '2026-09-13T10:01:00Z', '2026-09-13T10:02:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	var opened []*common.AgentRuntime
	t.Cleanup(func() {
		for _, runtime := range opened {
			_ = runtime.Close()
		}
	})
	open := func() *common.AgentRuntime {
		runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
			DatabasePath: databasePath, AdminToken: agentAdminToken,
			BackendURL: "http://127.0.0.1", BackendUserToken: backendUserToken,
			GatewayURL: "http://127.0.0.1", GatewayRuntimeToken: gatewayToken,
			DefaultPrompt: "default mission", DefaultMaxLoop: 4, DefaultMaxToolCall: 3,
			RequestTimeout: time.Second, ToolTimeout: time.Second, StopTimeout: time.Second,
		})
		if err != nil {
			t.Fatalf("OpenAgentRuntime() legacy migration error = %v", err)
		}
		opened = append(opened, runtime)
		return runtime
	}
	assertLegacyRun := func(runtime *common.AgentRuntime) {
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
			"/api/v1/sessions/s_legacy", agentAdminToken, nil)
		if status != http.StatusOK {
			t.Fatalf("legacy Session status = %d body = %s", status, raw)
		}
		session := decodeBody[common.Session](t, raw)
		if session.Name != "legacy session" || session.AccountID != "acc_legacy" || session.CurrentRunID != 1 {
			t.Fatalf("legacy Session changed by migration: %+v", session)
		}
		status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
			"/api/v1/sessions/s_legacy/runs/1", agentAdminToken, nil)
		if status != http.StatusOK {
			t.Fatalf("legacy Run status = %d body = %s", status, raw)
		}
		run := decodeBody[common.Run](t, raw)
		if run.Status != "completed" || run.Trigger != "session_start" || string(run.Triggers) != "null" ||
			run.Summary == nil || *run.Summary != "legacy summary" || run.Output == nil || *run.Output != "legacy output" {
			t.Fatalf("legacy Run changed by migration: %+v", run)
		}
	}

	runtime := open()
	assertLegacyRun(runtime)
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() after migration error = %v", err)
	}
	runtime = open()
	assertLegacyRun(runtime)
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() after idempotent reopen error = %v", err)
	}
}
