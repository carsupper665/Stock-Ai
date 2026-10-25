package test

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "stock-ai/agent-server/internal/agent"
)

func TestDeletedSessionReleasesClaudeTransport(t *testing.T) {
	for _, releaseStatus := range []int{200, 503} {
		t.Run(jsonNumber(int64(releaseStatus)), func(t *testing.T) {
			fixture := newRuntimeFixture()
			backend := httptest.NewServer(http.HandlerFunc(fixture.serveBackend))
			defer backend.Close()
			var calls atomic.Int32
			var released string
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/harnesses/claude/sessions/release" {
					fixture.serveGateway(w, r)
					return
				}
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+gatewayToken {
					t.Error("invalid release auth/method")
				}
				var body struct {
					ConversationID string `json:"conversation_id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				released = body.ConversationID
				calls.Add(1)
				writeFixtureJSON(w, releaseStatus, map[string]any{"secret": "must-not-be-logged"})
			}))
			defer gateway.Close()
			var logs lockedLog
			runtime, err := agent.OpenAgentRuntime(agent.RuntimeConfig{DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: agentAdminToken,
				BackendURL: backend.URL, BackendUserToken: backendUserToken, GatewayURL: gateway.URL, GatewayRuntimeToken: gatewayToken,
				DefaultPrompt: "mission", DefaultMaxLoop: 4, DefaultMaxToolCall: 3, RequestTimeout: time.Second, ToolTimeout: time.Second, StopTimeout: time.Second, Logger: log.New(&logs, "", 0)})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			session := createStoppedSession(t, runtime, nil)
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID, agentAdminToken, nil)
			if status != 200 || !strings.Contains(string(raw), `"status":"deleted"`) || calls.Load() != 1 || released != session.ID {
				t.Fatalf("delete = %d %s calls=%d released=%s", status, raw, calls.Load(), released)
			}
			if releaseStatus == 503 && !strings.Contains(logs.String(), "event=claude_session_release_error session_id="+session.ID) {
				t.Fatal("missing release failure log")
			}
			for _, secret := range []string{gatewayToken, "must-not-be-logged"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("release log leaked secret")
				}
			}
		})
	}
}

func TestClaudeSessionsContinueAcrossRunsThroughGatewayProcess(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "claude-fixture.exe")
	build := exec.Command("go", "test", "-c", "-o", cli, "./test")
	build.Dir = "../../llm_provider_server"
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build existing Claude CLI fixture: %v\n%s", err, output)
	}
	gatewayURL, token := startGatewayProcess(t, "http://127.0.0.1:9", "LLM_SERVER_CLAUDE_EXECUTABLE="+cli)
	client := &http.Client{Timeout: 15 * time.Second}
	for _, setup := range []struct {
		path string
		body string
	}{
		{"/v1/providers", `{"id":"claude","name":"Claude","type":"claude_code","base_url":"","default_model":"managed-agent","config":{"auth_mode":"api_key"}}`},
		{"/v1/providers/claude/tokens", `{"name":"fixture","token":"fixture-claude-key"}`},
	} {
		status, raw := ticket16ProcessRequest(t, client, http.MethodPost, gatewayURL+setup.path, "ticket16-gateway-admin", []byte(setup.body))
		if status != 201 {
			t.Fatalf("setup = %d %s", status, raw)
		}
	}
	for _, test := range []struct {
		mode, model, second string
		window              int
	}{
		{"semantic", "managed-agent", "cold", 128000},
		{"verbatim", "managed-agent", "resume", 128000},
		{"verbatim", "managed-agent-large", "cold", 8000},
	} {
		t.Run(test.mode+"/"+test.model, func(t *testing.T) {
			mapping, _ := json.Marshal(map[string]any{"model_name": "fixture-model", "provider_id": "claude", "model": test.model, "context_window": test.window, "levels": map[string]any{}})
			if status, raw := ticket16ProcessRequest(t, client, http.MethodPut, gatewayURL+"/v1/model-catalog/fixture-model", "ticket16-gateway-admin", mapping); status != 200 {
				t.Fatalf("mapping = %d %s", status, raw)
			}
			fixture := newRuntimeFixture()
			backend := httptest.NewServer(http.HandlerFunc(fixture.serveBackend))
			defer backend.Close()
			runtime, err := agent.OpenAgentRuntime(agent.RuntimeConfig{DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: agentAdminToken,
				BackendURL: backend.URL, BackendUserToken: backendUserToken, GatewayURL: gatewayURL, GatewayRuntimeToken: token,
				DefaultPrompt: "mission", DefaultMaxLoop: 4, DefaultMaxToolCall: 3, RequestTimeout: 10 * time.Second, ToolTimeout: time.Second, StopTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			session := createStoppedSession(t, runtime, map[string]any{"conversation_mode": test.mode})
			var directories []string
			for runID := int64(1); runID <= 3; runID++ {
				if runID == 1 {
					startSessionForTest(t, runtime, session.ID)
				} else {
					restartSessionRun(t, runtime, session.ID)
				}
				run := waitForRunStatusWithin(t, runtime, session.ID, runID, 15*time.Second, "completed")
				var report struct {
					Kind      string `json:"kind"`
					Directory string `json:"directory"`
				}
				if run.Output == nil || json.Unmarshal([]byte(*run.Output), &report) != nil {
					t.Fatalf("CLI report missing: %+v", run)
				}
				want := test.second
				if runID == 1 {
					want = "cold"
				}
				if runID == 3 && test.model == "managed-agent-large" {
					want = "resume"
				}
				if report.Kind != want {
					t.Fatalf("Run %d mode = %s want %s", runID, report.Kind, want)
				}
				var progress []map[string]any
				_ = json.Unmarshal(run.Progress, &progress)
				for _, entry := range progress {
					if entry["type"] == "model_call" && (entry["session_mode"] != want || entry["conversation_id"] != session.ID) {
						t.Fatalf("Run %d diagnostics = %v", runID, entry)
					}
				}
				directories = append(directories, report.Directory)
				if runID == 2 && test.model == "managed-agent-large" && len(compactionEntries(t, run.Progress)) == 0 {
					t.Fatal("compaction did not occur")
				}
			}
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID, agentAdminToken, nil)
			if status != 200 {
				t.Fatalf("delete = %d %s", status, raw)
			}
			for _, directory := range directories {
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatalf("Session delete retained %s: %v", directory, err)
				}
			}
		})
	}
}
