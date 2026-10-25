package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

const claudeReleasePath = "/v1/harnesses/claude/sessions/release"

func TestClaudeSessionReleaseHTTP(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")
	createClaudeHarnessProvider(t, server.Handler(), "claude-b")
	for _, token := range []string{"", "wrong", adminToken} {
		assertError(t, request(t, server.Handler(), http.MethodPost, claudeReleasePath, token, []byte(`{"conversation_id":"agent"}`)), 401, "AUTHENTICATION_FAILED")
	}
	for _, body := range []string{`{}`, `{"conversation_id":""}`, `{"conversation_id":"bad\nvalue"}`, `{"conversation_id":null}`, `{"conversation_id":1}`, `{"conversation_id":"agent","extra":true}`} {
		assertError(t, request(t, server.Handler(), http.MethodPost, claudeReleasePath, runtimeToken, []byte(body)), 400, "REQUEST_INVALID")
	}
	a := generateManagedClaude(t, server.Handler(), "claude-a", "agent", "mission", "one", "")
	b := generateManagedClaude(t, server.Handler(), "claude-b", "agent", "mission", "one", "")
	compact := generateManagedClaude(t, server.Handler(), "claude-a", "agent/1/compaction", "mission", "one", "")
	other := generateManagedClaude(t, server.Handler(), "claude-a", "agent-other", "mission", "one", "")
	for range 2 {
		response := request(t, server.Handler(), http.MethodPost, claudeReleasePath, runtimeToken, []byte(`{"conversation_id":"agent"}`))
		if response.Code != 200 || response.Body.String() != "{\"released\":true}\n" {
			t.Fatalf("release = %d %s", response.Code, response.Body)
		}
	}
	for _, directory := range []string{a.Directory, b.Directory, compact.Directory} {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatalf("directory retained: %s err=%v", directory, err)
		}
	}
	if _, err := os.Stat(other.Directory); err != nil {
		t.Fatal("release removed unrelated session", err)
	}
	assertError(t, request(t, server.Handler(), http.MethodGet, claudeReleasePath, runtimeToken, nil), 405, "REQUEST_INVALID")
}

func TestClaudeReleaseWaitsForActiveInvocationExit(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")
	barrier := t.TempDir()
	body, _ := json.Marshal(map[string]any{"provider": "claude-a", "model": "managed-report", "conversation_id": "active", "messages": []map[string]any{{"role": "user", "content": "hold=" + barrier}}})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body) }()
	waitForFixtureFile(t, filepath.Join(barrier, "started"))
	response := request(t, server.Handler(), http.MethodPost, claudeReleasePath, runtimeToken, []byte(`{"conversation_id":"active"}`))
	if response.Code != 200 {
		t.Fatalf("release active = %d %s", response.Code, response.Body)
	}
	newBody := []byte(`{"provider":"claude-a","model":"managed-report","conversation_id":"active","messages":[{"role":"user","content":"must not reopen while retiring"}]}`)
	if next := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, newBody); next.Code == 200 {
		t.Fatal("retiring transport accepted a new invocation")
	}
	if err := os.WriteFile(filepath.Join(barrier, "release"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	report := decodeManagedClaudeReport(t, <-done)
	if _, err := os.Stat(report.Directory); !os.IsNotExist(err) {
		t.Fatalf("active directory not reclaimed after exit: %v", err)
	}
}

func TestClaudeDefaultTTLIs24Hours(t *testing.T) {
	t.Setenv("LLM_SERVER_HARNESS_SESSION_TTL", "")
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	synctest.Test(t, func(t *testing.T) {
		server := openTestServer(t, 10*time.Second)
		defer server.Close()
		createClaudeHarnessProvider(t, server.Handler(), "claude-a")
		report := generateManagedClaude(t, server.Handler(), "claude-a", "ttl-default", "mission", "one", "")
		time.Sleep(24*time.Hour - time.Nanosecond)
		synctest.Wait()
		if _, err := os.Stat(report.Directory); err != nil {
			t.Fatal("default TTL removed session before 24h", err)
		}
		time.Sleep(time.Minute + time.Nanosecond)
		synctest.Wait()
		if _, err := os.Stat(report.Directory); !os.IsNotExist(err) {
			t.Fatal("default TTL retained session after 24h", err)
		}
	})
}
