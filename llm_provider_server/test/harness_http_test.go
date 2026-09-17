package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func TestHarnessProvidersAreUsableThroughGatewayHTTP(t *testing.T) {
	for _, kind := range []string{"codex", "claude_code"} {
		t.Run(kind, func(t *testing.T) {
			fixture, key, model, variable := "codex-fixture.exe", "fixture-codex-key", "gpt-5.4", "LLM_SERVER_CODEX_EXECUTABLE"
			if kind == "claude_code" {
				fixture, key, model, variable = "claude-fixture.exe", "fixture-claude-key", "claude-sonnet-4-6", "LLM_SERVER_CLAUDE_EXECUTABLE"
			}
			t.Setenv(variable, copyHarnessFixture(t, fixture))
			server := openTestServer(t, 5*time.Second)
			defer server.Close()
			body, _ := json.Marshal(map[string]any{"id": "fixture", "name": kind, "type": kind, "base_url": "", "default_model": model, "config": map[string]any{"auth_mode": "api_key"}})
			created := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, body)
			if created.Code != 201 {
				t.Fatalf("create %s = %d %s", kind, created.Code, created.Body)
			}
			body, _ = json.Marshal(map[string]string{"name": "fixture", "token": key})
			if response := request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", adminToken, body); response.Code != 201 {
				t.Fatal(response.Code, response.Body)
			}
			body, _ = json.Marshal(map[string]any{"provider": "fixture", "conversation_id": "ignored-by-non-claude", "messages": []any{map[string]string{"role": "user", "content": "hello"}}})
			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body)
			if response.Code != 200 {
				t.Fatalf("generate%s=%d %s", kind, response.Code, response.Body)
			}
			var result map[string]any
			if json.Unmarshal(response.Body.Bytes(), &result) != nil || result["finish_reason"] == nil {
				t.Fatal(response.Body)
			}
			bad := []byte(`{"provider":"fixture","messages":[{"role":"user","content":"hi"}],"options":{"shell":true}}`)
			assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, bad), 400, "REQUEST_INVALID")
		})
	}
}

func TestHarnessPromptPlacesToolsBeforeMessagesForCachePrefix(t *testing.T) {
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	server := openTestServer(t, 5*time.Second)
	defer server.Close()
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken,
		[]byte(`{"id":"cache-order","name":"cache order","type":"codex","base_url":"","default_model":"prompt-order-report","config":{"auth_mode":"api_key"}}`))
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body)
	}
	if response := request(t, server.Handler(), http.MethodPost, "/v1/providers/cache-order/tokens", adminToken,
		[]byte(`{"name":"fixture","token":"fixture-codex-key"}`)); response.Code != http.StatusCreated {
		t.Fatal(response.Code, response.Body)
	}
	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"cache-order","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"}],"tools":[{"name":"market.price","description":"price","input_schema":{"type":"object"}}]}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"tools-before-messages"`) {
		t.Fatalf("generate=%d %s", response.Code, response.Body)
	}
}

func TestNativeAndCodexIgnoreContinuationAndUseFullMessages(t *testing.T) {
	fullMessages := []map[string]any{
		{"role": "user", "content": "first"},
		{"role": "assistant", "content": "previous"},
		{"role": "user", "content": "latest"},
	}
	continuation := map[string]any{"after_messages": 1, "messages": fullMessages[2:]}

	t.Run("native OpenAI", func(t *testing.T) {
		seen := make(chan map[string]any, 1)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			seen <- body
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		}))
		defer upstream.Close()
		server := openTestServer(t, time.Second)
		defer server.Close()
		providerBody, _ := json.Marshal(map[string]any{
			"id": "native-openai", "name": "Native OpenAI", "type": "openai", "base_url": upstream.URL, "default_model": "model",
		})
		if response := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, providerBody); response.Code != http.StatusCreated {
			t.Fatalf("create native provider=%d %s", response.Code, response.Body)
		}
		createToken(t, server.Handler(), "native-openai", "main", "fixture-openai-key")
		body, _ := json.Marshal(map[string]any{"provider": "native-openai", "messages": fullMessages, "continuation": continuation})
		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body)
		if response.Code != http.StatusOK {
			t.Fatalf("generate=%d %s", response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "session_mode") || strings.Contains(response.Body.String(), "invocation_count") {
			t.Fatalf("native provider unexpectedly exposed session diagnostics: %s", response.Body)
		}
		wire := <-seen
		messages, _ := wire["messages"].([]any)
		if len(messages) != len(fullMessages) {
			t.Fatalf("native messages=%v", wire["messages"])
		}
		if _, leaked := wire["continuation"]; leaked {
			t.Fatalf("continuation leaked upstream: %+v", wire)
		}
	})

	t.Run("codex", func(t *testing.T) {
		t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
		server := openTestServer(t, 5*time.Second)
		defer server.Close()
		created := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken,
			[]byte(`{"id":"codex-context","name":"Codex","type":"codex","base_url":"","default_model":"continuation-full-context"}`))
		if created.Code != http.StatusCreated {
			t.Fatal(created.Code, created.Body)
		}
		if response := request(t, server.Handler(), http.MethodPost, "/v1/providers/codex-context/tokens", adminToken,
			[]byte(`{"name":"fixture","token":"fixture-codex-key"}`)); response.Code != http.StatusCreated {
			t.Fatal(response.Code, response.Body)
		}
		body, _ := json.Marshal(map[string]any{"provider": "codex-context", "messages": fullMessages, "continuation": continuation})
		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"messages=3 continuation_serialized=false"`) {
			t.Fatalf("generate=%d %s", response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "session_mode") || strings.Contains(response.Body.String(), "invocation_count") {
			t.Fatalf("Codex unexpectedly exposed session diagnostics: %s", response.Body)
		}
	})
}

func TestHarnessLocalLoginUsesOnlyConfiguredAuthFileAndSupportsAdminTest(t *testing.T) {
	directory := t.TempDir()
	auth := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"fixture_login":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture-local.exe"))
	t.Setenv("LLM_SERVER_CODEX_AUTH_FILE", auth)
	server := openTestServer(t, 5*time.Second)
	defer server.Close()
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, []byte(`{"id":"local","name":"Local Codex","type":"codex","base_url":"","default_model":"cli-default","config":{"auth_mode":"local_login"}}`))
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	response := request(t, server.Handler(), http.MethodPost, "/v1/providers/local/test", adminToken, []byte(`{"prompt":"confirm"}`))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/providers/local/test", runtimeToken, []byte(`{}`)), 401, "AUTHENTICATION_FAILED")
	if err := os.Remove(auth); err != nil {
		t.Fatal(err)
	}
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/providers/local/test", adminToken, []byte(`{}`)), 503, "HARNESS_LOGIN_REQUIRED")
}

func TestHarnessStatusReportsActualServerConfiguration(t *testing.T) {
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", "")
	server := openTestServer(t, time.Second)
	defer server.Close()
	response := request(t, server.Handler(), http.MethodGet, "/v1/harnesses", adminToken, nil)
	if response.Code != 200 {
		t.Fatalf("status=%d %s", response.Code, response.Body)
	}
	assertError(t, request(t, server.Handler(), http.MethodGet, "/v1/harnesses", runtimeToken, nil), 401, "AUTHENTICATION_FAILED")
}

// dist/.env points at tools/codex/codex.exe relative to the start directory, the same way
// LLM_SERVER_DB_PATH is relative; the status must not report such a CLI as unconfigured.
func TestHarnessRelativeExecutablePathResolvesAgainstWorkingDirectory(t *testing.T) {
	fixture := copyHarnessFixture(t, "codex-fixture.exe")
	t.Chdir(filepath.Dir(fixture))
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", filepath.Base(fixture))
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", "")
	server := openTestServer(t, time.Second)
	defer server.Close()
	response := request(t, server.Handler(), http.MethodGet, "/v1/harnesses", adminToken, nil)
	if response.Code != 200 {
		t.Fatalf("status=%d %s", response.Code, response.Body)
	}
	var status struct {
		Harnesses []struct {
			Type                 string `json:"type"`
			ExecutableConfigured bool   `json:"executable_configured"`
		} `json:"harnesses"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, harness := range status.Harnesses {
		if harness.ExecutableConfigured != (harness.Type == "codex") {
			t.Fatalf("%s executable_configured=%v: %s", harness.Type, harness.ExecutableConfigured, response.Body)
		}
	}
}

// The CLI is Server-controlled, so reasoning_effort is the only option a Harness model may
// carry, and each CLI has its own scale.
func TestHarnessCatalogModelsAcceptOnlyValidReasoningEffort(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, []byte(`{"id":"cli","name":"Claude Code","type":"claude_code","base_url":"","default_model":"claude-sonnet-4-6"}`))
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	for _, body := range []string{
		`{"model_name":"cli-temp","provider_id":"cli","model":"m1","options":{"temperature":0}}`,
		`{"model_name":"cli-bad","provider_id":"cli","model":"m2","options":{"reasoning_effort":"bogus"}}`,
		`{"model_name":"cli-minimal","provider_id":"cli","model":"m3","options":{"reasoning_effort":"minimal"}}`,
		`{"model_name":"cli-level","provider_id":"cli","model":"m4","levels":{"high":{"reasoning_effort":"turbo"}}}`,
	} {
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/model-catalog", adminToken, []byte(body)), 400, "REQUEST_INVALID")
	}
	accepted := request(t, server.Handler(), http.MethodPost, "/v1/model-catalog", adminToken,
		[]byte(`{"model_name":"cli-effort","provider_id":"cli","model":"m5","levels":{"low":{"reasoning_effort":"low"},"max":{"reasoning_effort":"max"}}}`))
	if accepted.Code != 201 {
		t.Fatal(accepted.Code, accepted.Body)
	}
}

func TestHarnessModelLevelReachesTheCLI(t *testing.T) {
	for _, test := range []struct {
		kind, variable, fixture, effort string
	}{
		{"codex", "LLM_SERVER_CODEX_EXECUTABLE", "codex-fixture.exe", "xhigh"},
		{"claude_code", "LLM_SERVER_CLAUDE_EXECUTABLE", "claude-fixture.exe", "max"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			t.Setenv(test.variable, copyHarnessFixture(t, test.fixture))
			server := openTestServer(t, 10*time.Second)
			defer server.Close()
			body, _ := json.Marshal(map[string]any{"id": "cli", "name": test.kind, "type": test.kind, "base_url": "", "default_model": "effort-report"})
			if response := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, body); response.Code != 201 {
				t.Fatal(response.Code, response.Body)
			}
			key := "fixture-codex-key"
			if test.kind == "claude_code" {
				key = "fixture-claude-key"
			}
			body, _ = json.Marshal(map[string]string{"name": "fixture", "token": key})
			if response := request(t, server.Handler(), http.MethodPost, "/v1/providers/cli/tokens", adminToken, body); response.Code != 201 {
				t.Fatal(response.Code, response.Body)
			}
			body, _ = json.Marshal(map[string]any{"model_name": "cli-effort", "provider_id": "cli", "model": "effort-report",
				"levels": map[string]any{"high": map[string]string{"reasoning_effort": test.effort}}})
			if response := request(t, server.Handler(), http.MethodPost, "/v1/model-catalog", adminToken, body); response.Code != 201 {
				t.Fatal(response.Code, response.Body)
			}
			for _, expected := range []struct {
				options, want string
			}{
				{`{"model_level":"high"}`, test.effort},
				{`{}`, "none"},
			} {
				generate := []byte(`{"provider":"cli","model":"effort-report","messages":[{"role":"user","content":"hi"}],"options":` + expected.options + `}`)
				response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, generate)
				if response.Code != 200 {
					t.Fatalf("generate=%d %s", response.Code, response.Body)
				}
				var result struct {
					Content *string `json:"content"`
				}
				if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Content == nil || *result.Content != expected.want {
					t.Fatalf("effort reaching the CLI = %s, want %q", response.Body, expected.want)
				}
			}
		})
	}
}

func TestHarnessErrorsCarrySafeActionableCategory(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 5*time.Second)
	defer server.Close()
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, []byte(`{"id":"cli","name":"Claude Code","type":"claude_code","base_url":"","default_model":"auth-failure"}`))
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	if response := request(t, server.Handler(), http.MethodPost, "/v1/providers/cli/tokens", adminToken, []byte(`{"name":"fixture","token":"fixture-claude-key"}`)); response.Code != 201 {
		t.Fatal(response.Code, response.Body)
	}
	response := request(t, server.Handler(), http.MethodPost, "/v1/providers/cli/test", adminToken, []byte(`{}`))
	assertError(t, response, 502, "AUTHENTICATION_FAILED")
	if !strings.Contains(response.Body.String(), "CLI authentication was rejected") {
		t.Fatalf("safe CLI category missing: %s", response.Body)
	}
}

func TestCodexGatewayMapsVersionAuthenticationOutputTimeoutAndCancellation(t *testing.T) {
	tests := []struct {
		name, fixture, model, code string
		status                     int
		timeout                    time.Duration
	}{
		{name: "version", fixture: "codex-fixture-wrong-version.exe", model: "gpt-5.4", status: http.StatusServiceUnavailable, code: "HARNESS_VERSION_MISMATCH", timeout: time.Second},
		{name: "authentication", fixture: "codex-fixture.exe", model: "auth-failure", status: http.StatusBadGateway, code: "AUTHENTICATION_FAILED", timeout: time.Second},
		{name: "invalid output", fixture: "codex-fixture.exe", model: "invalid-output", status: http.StatusBadGateway, code: "PROVIDER_RESPONSE_INVALID", timeout: time.Second},
		{name: "timeout", fixture: "codex-fixture.exe", model: "hang", status: http.StatusGatewayTimeout, code: "PROVIDER_TIMEOUT", timeout: 100 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, test.fixture))
			server := openTestServer(t, test.timeout)
			defer server.Close()
			createCodexHarnessProvider(t, server.Handler(), test.model)
			body, _ := json.Marshal(map[string]any{"provider": "codex", "messages": []map[string]any{{"role": "user", "content": "prompt"}}})
			assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body), test.status, test.code)
		})
	}

	t.Run("caller cancellation", func(t *testing.T) {
		t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
		server := openTestServer(t, 5*time.Second)
		defer server.Close()
		createCodexHarnessProvider(t, server.Handler(), "hang")
		body := []byte(`{"provider":"codex","messages":[{"role":"user","content":"prompt"}]}`)
		requestContext, cancel := context.WithCancel(context.Background())
		httpRequest := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(body)).WithContext(requestContext)
		httpRequest.Header.Set("Authorization", "Bearer "+runtimeToken)
		httpRequest.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			server.Handler().ServeHTTP(response, httpRequest)
			close(done)
		}()
		time.Sleep(100 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("canceled Codex Generate did not return")
		}
		assertError(t, response, http.StatusRequestTimeout, "REQUEST_CANCELED")
	})
}

// §32.4: cached_tokens is the subset already inside input_tokens. The Agent Runtime fails a
// model attempt with a bare GATEWAY_ERROR when that does not hold, so the Harness path must
// apply the Anthropic summing rule the native Anthropic path already applies.
func TestClaudeHarnessUsageKeepsCachedTokensInsideInputTokens(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, []byte(`{"id":"cli","name":"Claude Code","type":"claude_code","base_url":"","default_model":"claude-sonnet-4-6"}`))
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	if response := request(t, server.Handler(), http.MethodPost, "/v1/providers/cli/tokens", adminToken, []byte(`{"name":"fixture","token":"fixture-claude-key"}`)); response.Code != 201 {
		t.Fatal(response.Code, response.Body)
	}
	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"cli","messages":[{"role":"user","content":"hi"}]}`))
	if response.Code != 200 {
		t.Fatalf("generate=%d %s", response.Code, response.Body)
	}
	var result struct {
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			CachedTokens *int `json:"cached_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Usage == nil ||
		result.Usage.InputTokens == nil || result.Usage.CachedTokens == nil {
		t.Fatalf("usage missing: %s", response.Body)
	}
	if *result.Usage.CachedTokens > *result.Usage.InputTokens {
		t.Fatalf("cached_tokens %d exceeds input_tokens %d: %s", *result.Usage.CachedTokens, *result.Usage.InputTokens, response.Body)
	}
}

type managedClaudeReport struct {
	Kind              string `json:"kind"`
	SessionID         string `json:"session_id"`
	Directory         string `json:"directory"`
	Messages          int    `json:"messages"`
	AssistantMessages int    `json:"assistant_messages"`
	PromptBytes       int    `json:"prompt_bytes"`
	AuthOK            bool   `json:"auth_ok"`
}

type managedClaudeEnvelope struct {
	Content         *string `json:"content"`
	SessionMode     string  `json:"session_mode"`
	FallbackReason  string  `json:"fallback_reason"`
	InvocationCount int     `json:"invocation_count"`
}

func TestClaudeConversationLocalLoginSnapshotChangesColdWithoutAffectingInflightCall(t *testing.T) {
	directory := t.TempDir()
	authFile := filepath.Join(directory, "claude-login.json")
	if err := os.WriteFile(authFile, []byte(`{"fixture_login":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	t.Setenv("LLM_SERVER_CLAUDE_AUTH_FILE", authFile)
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	createClaudeLocalHarnessProvider(t, server.Handler(), "claude-local")

	barrier := filepath.Join(directory, "barrier")
	if err := os.Mkdir(barrier, 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"provider": "claude-local", "model": "managed-report", "conversation_id": "local/1",
		"messages": []map[string]any{{"role": "user", "content": "hold=" + barrier + ";expect-auth=a"}},
	})
	firstResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResponse <- request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body)
	}()
	waitForFixtureFile(t, filepath.Join(barrier, "started"))
	if err := os.WriteFile(authFile, []byte(`{"fixture_login":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(barrier, "release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := decodeManagedClaudeReport(t, <-firstResponse)
	if !first.AuthOK {
		t.Fatalf("in-flight call did not retain login snapshot: %+v", first)
	}
	second := generateManagedClaudeMessages(t, server.Handler(), "claude-local", "local/1", []map[string]any{{"role": "user", "content": "expect-auth=b"}})
	if !second.AuthOK || second.Kind != "cold" || second.SessionID == first.SessionID {
		t.Fatalf("changed login did not start cold from the new snapshot: first=%+v second=%+v", first, second)
	}

	if err := os.WriteFile(authFile, []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := generateClaudeRecorder(t, server.Handler(), "claude-local", "local/bad")
	assertError(t, bad, http.StatusBadGateway, "AUTHENTICATION_FAILED")
	if err := os.Remove(authFile); err != nil {
		t.Fatal(err)
	}
	missing := generateClaudeRecorder(t, server.Handler(), "claude-local", "local/missing")
	assertError(t, missing, http.StatusBadGateway, "AUTHENTICATION_FAILED")
}

func TestClaudeConversationFailedResumeWithConcurrentWaiterNeverInitializesUUIDTwice(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")
	initial := generateManagedClaudeMessages(t, server.Handler(), "claude-a", "concurrent/1", []map[string]any{{"role": "user", "content": "first"}})
	barrier := t.TempDir()
	messages := []map[string]any{
		{"role": "user", "content": "first"},
		{"role": "assistant", "content": "previous"},
		{"role": "user", "content": "resume-fail;hold=" + barrier},
	}
	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() {
		responses <- generateManagedClaudeRecorder(t, server.Handler(), "claude-a", "concurrent/1", messages)
	}()
	waitForFixtureFile(t, filepath.Join(barrier, "started"))
	go func() {
		responses <- generateManagedClaudeRecorder(t, server.Handler(), "claude-a", "concurrent/1", messages)
	}()
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(barrier, "release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := decodeManagedClaudeReport(t, <-responses)
	second := decodeManagedClaudeReport(t, <-responses)
	if first.Kind != "cold" || second.Kind != "cold" || first.SessionID == second.SessionID ||
		first.SessionID == initial.SessionID || second.SessionID == initial.SessionID {
		t.Fatalf("concurrent fallback reused a UUID: initial=%+v first=%+v second=%+v", initial, first, second)
	}
}

func TestClaudeConversationResumesWithOnlyNewProtocolMessages(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 30*time.Second)
	defer server.Close()
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")

	first := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"claude-a","model":"managed-report","conversation_id":"agent-1/7",
		"messages":[{"role":"system","content":"mission"},{"role":"user","content":"need-tool"}],
		"tools":[{"name":"fixture_tool","description":"fixture","input_schema":{"type":"object"}}]
	}`))
	if first.Code != http.StatusOK {
		t.Fatalf("first=%d %s", first.Code, first.Body)
	}
	var firstResult struct {
		ToolCalls    []map[string]any `json:"tool_calls"`
		NumTurns     *int             `json:"num_turns"`
		TotalCostUSD *float64         `json:"total_cost_usd"`
	}
	if json.Unmarshal(first.Body.Bytes(), &firstResult) != nil || len(firstResult.ToolCalls) != 1 || firstResult.NumTurns == nil || *firstResult.NumTurns != 2 || firstResult.TotalCostUSD == nil || *firstResult.TotalCostUSD != 0.125 {
		t.Fatalf("first response=%s", first.Body)
	}
	second := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"claude-a","model":"managed-report","conversation_id":"agent-1/7",
		"messages":[{"role":"system","content":"mission"},{"role":"user","content":"need-tool"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"managed-call","name":"fixture_tool","arguments":{}}]},
		{"role":"tool","content":"{\"ok\":true}","tool_call_id":"managed-call"}],
		"tools":[{"name":"fixture_tool","description":"fixture","input_schema":{"type":"object"}}]
	}`))
	report := decodeManagedClaudeReport(t, second)
	if report.Kind != "resume" || report.Messages != 2 {
		t.Fatalf("resume report=%+v body=%s", report, second.Body)
	}
	if _, err := os.Stat(filepath.Join(report.Directory, ".fixture-session.json")); err != nil {
		t.Fatalf("persistent marker missing: %v", err)
	}
}

func TestClaudeContinuationUsesExactDeltaWithoutExtraColdOrAssistantDuplication(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")

	fullMessages := []map[string]any{
		{"role": "system", "content": "mission"},
		{"role": "user", "content": "need-tool"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "managed-call", "name": "fixture_tool", "arguments": map[string]any{}}}},
		{"role": "tool", "content": `{"ok":true}`, "tool_call_id": "managed-call"},
	}
	startManagedToolConversation(t, server.Handler(), "legacy-bytes")
	legacyResponse := generateManagedClaudeRecorder(t, server.Handler(), "claude-a", "legacy-bytes", fullMessages)
	legacyEnvelope, legacy := decodeManagedClaudeEnvelope(t, legacyResponse)

	startManagedToolConversation(t, server.Handler(), "continuation-bytes")
	continuationBody, _ := json.Marshal(map[string]any{
		"provider": "claude-a", "model": "managed-report", "conversation_id": "continuation-bytes",
		"messages": fullMessages,
		"continuation": map[string]any{
			"after_messages": 2,
			"messages":       fullMessages[3:],
		},
	})
	continuationResponse := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, continuationBody)
	continuationEnvelope, continuation := decodeManagedClaudeEnvelope(t, continuationResponse)

	toolContent, callID := `{"ok":true}`, "managed-call"
	assistantMessages := []gateway.WebHarnessMessage{{
		Role: "assistant", ToolCalls: []gateway.WebHarnessToolCall{{ID: callID, Name: "fixture_tool", Arguments: json.RawMessage(`{}`)}},
	}, {
		Role: "tool", Content: &toolContent, ToolCallID: &callID,
	}}
	deltaMessages := assistantMessages[1:]
	legacyJSON, _ := json.Marshal(assistantMessages)
	deltaJSON, _ := json.Marshal(deltaMessages)
	const resumePrefix = "Continue with these new normalized messages. Return only the JSON object required by the output schema:\n"
	wantLegacyBytes := len(resumePrefix) + len(legacyJSON)
	wantContinuationBytes := len(resumePrefix) + len(deltaJSON)

	if legacy.Kind != "resume" || continuation.Kind != "resume" || legacy.Messages != 2 || continuation.Messages != 1 {
		t.Fatalf("legacy=%+v continuation=%+v", legacy, continuation)
	}
	if legacy.AssistantMessages != 1 || continuation.AssistantMessages != 0 {
		t.Fatalf("assistant duplication: legacy=%+v continuation=%+v", legacy, continuation)
	}
	if legacy.PromptBytes != wantLegacyBytes || continuation.PromptBytes != wantContinuationBytes || continuation.PromptBytes >= legacy.PromptBytes {
		t.Fatalf("resume bytes: legacy=%d want=%d continuation=%d want=%d", legacy.PromptBytes, wantLegacyBytes, continuation.PromptBytes, wantContinuationBytes)
	}
	for name, diagnostics := range map[string]managedClaudeEnvelope{"legacy": legacyEnvelope, "continuation": continuationEnvelope} {
		if diagnostics.SessionMode != "resume" || diagnostics.FallbackReason != "" || diagnostics.InvocationCount != 1 {
			t.Fatalf("%s diagnostics=%+v", name, diagnostics)
		}
	}
	t.Logf("exact fixture bytes: full_suffix=%d continuation_delta=%d; cold_invocations=0; duplicated_assistant_messages=0", legacy.PromptBytes, continuation.PromptBytes)
}

func TestClaudeInvalidOrUnprovableContinuationFallsBackToOneFullColdInvocation(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")

	validMessages := []map[string]any{
		{"role": "system", "content": "mission"},
		{"role": "user", "content": "need-tool"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "managed-call", "name": "fixture_tool", "arguments": map[string]any{}}}},
		{"role": "tool", "content": `{"ok":true}`, "tool_call_id": "managed-call"},
	}
	changedOutput := []map[string]any{
		{"role": "system", "content": "mission"},
		{"role": "user", "content": "need-tool"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "other-call", "name": "fixture_tool", "arguments": map[string]any{}}}},
		{"role": "tool", "content": `{"ok":true}`, "tool_call_id": "other-call"},
	}
	baselineEnvelope, baseline := decodeManagedClaudeEnvelope(t,
		generateManagedClaudeRecorder(t, server.Handler(), "claude-a", "cold-full-baseline", validMessages))
	if baselineEnvelope.SessionMode != "cold" || baselineEnvelope.FallbackReason != "" || baselineEnvelope.InvocationCount != 1 {
		t.Fatalf("cold baseline diagnostics=%+v", baselineEnvelope)
	}
	matchedFallbackBytes := 0
	tests := []struct {
		name, conversation, reason string
		start, compareFull         bool
		messages                   []map[string]any
		after                      int
		delta                      []map[string]any
	}{
		{name: "wrong acknowledged count", conversation: "bad-after", reason: "continuation_invalid", start: true, messages: validMessages, after: 1, delta: validMessages[3:]},
		{name: "previous output differs", conversation: "bad-output", reason: "continuation_invalid", start: true, messages: changedOutput, after: 2, delta: changedOutput[3:]},
		{name: "explicit delta differs", conversation: "bad-delta", reason: "continuation_invalid", start: true, messages: validMessages, after: 2, delta: []map[string]any{{"role": "user", "content": "omitted tool result"}}},
		{name: "empty delta", conversation: "empty-delta", reason: "continuation_invalid", start: true, messages: validMessages[:3], after: 2, delta: []map[string]any{}},
		{name: "no retained session", conversation: "missing-session", reason: "continuation_unavailable", compareFull: true, messages: validMessages, after: 2, delta: validMessages[3:]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.start {
				startManagedToolConversation(t, server.Handler(), test.conversation)
			}
			body, _ := json.Marshal(map[string]any{
				"provider": "claude-a", "model": "managed-report", "conversation_id": test.conversation,
				"messages": test.messages, "continuation": map[string]any{"after_messages": test.after, "messages": test.delta},
			})
			envelope, report := decodeManagedClaudeEnvelope(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body))
			if report.Kind != "cold" || report.Messages != len(test.messages) || envelope.SessionMode != "cold" ||
				envelope.FallbackReason != test.reason || envelope.InvocationCount != 1 {
				t.Fatalf("envelope=%+v report=%+v", envelope, report)
			}
			if test.compareFull && report.PromptBytes != baseline.PromptBytes {
				t.Fatalf("full cold fallback bytes=%d, baseline=%d", report.PromptBytes, baseline.PromptBytes)
			}
			if test.compareFull {
				matchedFallbackBytes = report.PromptBytes
			}
		})
	}
	t.Logf("full cold fallback bytes=%d, exact no-continuation baseline=%d", matchedFallbackBytes, baseline.PromptBytes)
}

func TestClaudeConversationFallsBackColdForMissingDirectoryChangedPrefixAndFailedResume(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 5*time.Second)
	defer server.Close()
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")

	for _, test := range []struct {
		name, conversation, secondContent     string
		removeDirectory, changePrefix, shrink bool
	}{
		{name: "missing directory", conversation: "agent/11", secondContent: "continued", removeDirectory: true},
		{name: "changed prefix", conversation: "agent/12", secondContent: "continued", changePrefix: true},
		{name: "failed resume", conversation: "agent/13", secondContent: "resume-fail"},
		{name: "shrunk prefix", conversation: "agent/14", shrink: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := generateManagedClaude(t, server.Handler(), "claude-a", test.conversation, "original", "first", "")
			if test.removeDirectory {
				if err := os.RemoveAll(first.Directory); err != nil {
					t.Fatal(err)
				}
			}
			prefix := "original"
			if test.changePrefix {
				prefix = "changed"
			}
			var second managedClaudeReport
			expectedMessages := 4
			if test.shrink {
				second = generateManagedClaudeMessages(t, server.Handler(), "claude-a", test.conversation, []map[string]any{{"role": "system", "content": prefix}})
				expectedMessages = 1
			} else {
				second = generateManagedClaude(t, server.Handler(), "claude-a", test.conversation, prefix, "first", test.secondContent)
			}
			if second.Kind != "cold" || second.SessionID == first.SessionID || second.Messages != expectedMessages {
				t.Fatalf("first=%+v second=%+v", first, second)
			}
		})
	}
	initial := generateManagedClaude(t, server.Handler(), "claude-a", "fallback-diagnostics", "mission", "first", "")
	response := generateManagedClaudeRecorder(t, server.Handler(), "claude-a", "fallback-diagnostics", []map[string]any{
		{"role": "system", "content": "mission"}, {"role": "user", "content": "first"},
		{"role": "assistant", "content": "previous"}, {"role": "user", "content": "resume-fail"},
	})
	envelope, report := decodeManagedClaudeEnvelope(t, response)
	if report.Kind != "cold" || report.SessionID == initial.SessionID || envelope.SessionMode != "cold" ||
		envelope.FallbackReason != "resume_failed" || envelope.InvocationCount != 2 {
		t.Fatalf("initial=%+v envelope=%+v report=%+v", initial, envelope, report)
	}
}

func TestClaudeConversationIsolationSerializationTTLAndShutdown(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	t.Setenv("LLM_SERVER_HARNESS_SESSION_TTL", "100ms")
	server := openTestServer(t, 5*time.Second)
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")
	createClaudeHarnessProvider(t, server.Handler(), "claude-b")

	var wait sync.WaitGroup
	reports := make(chan managedClaudeReport, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			reports <- generateManagedClaude(t, server.Handler(), "claude-a", "shared/1", "mission", "same", "")
		}()
	}
	wait.Wait()
	close(reports)
	sessionIDs := map[string]bool{}
	for report := range reports {
		if report.Kind != "cold" || sessionIDs[report.SessionID] {
			t.Fatalf("identical requests must execute cold with unique UUIDs: %+v", report)
		}
		sessionIDs[report.SessionID] = true
	}
	otherProvider := generateManagedClaude(t, server.Handler(), "claude-b", "shared/1", "mission", "same", "")
	if sessionIDs[otherProvider.SessionID] {
		t.Fatalf("provider identities shared a CLI session %q", otherProvider.SessionID)
	}
	low := generateManagedClaudeWithEffort(t, server.Handler(), "settings/1", "low")
	high := generateManagedClaudeWithEffort(t, server.Handler(), "settings/1", "high")
	if low.SessionID == high.SessionID || high.Kind != "cold" {
		t.Fatalf("changed settings reused a CLI session: low=%+v high=%+v", low, high)
	}
	ttl := generateManagedClaude(t, server.Handler(), "claude-a", "ttl/1", "mission", "ttl", "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := os.Stat(ttl.Directory)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TTL did not remove %q", ttl.Directory)
		}
		time.Sleep(25 * time.Millisecond)
	}
	retained := generateManagedClaude(t, server.Handler(), "claude-a", "shutdown/1", "mission", "shutdown", "")
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(retained.Directory); !os.IsNotExist(err) {
		t.Fatalf("Close retained session directory %q: %v", retained.Directory, err)
	}
}

func TestClaudeConversationValidationAndShutdownCancellation(t *testing.T) {
	t.Setenv("LLM_SERVER_CLAUDE_EXECUTABLE", copyHarnessFixture(t, "claude-fixture.exe"))
	server := openTestServer(t, 30*time.Second)
	createClaudeHarnessProvider(t, server.Handler(), "claude-a")
	for _, id := range []string{"", strings.Repeat("x", 129), "bad\nvalue"} {
		body, _ := json.Marshal(map[string]any{"provider": "claude-a", "conversation_id": id, "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body), http.StatusBadRequest, "REQUEST_INVALID")
	}
	for _, continuation := range []string{
		`{"after_messages":0,"messages":[]}`,
		`{"after_messages":-1,"messages":[]}`,
	} {
		body := []byte(`{"provider":"claude-a","conversation_id":"validation","messages":[{"role":"user","content":"hello"}],"continuation":` + continuation + `}`)
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body), http.StatusBadRequest, "REQUEST_INVALID")
	}
	body := []byte(`{"provider":"claude-a","model":"hang","conversation_id":"shutdown/2","messages":[{"role":"user","content":"hang"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- server.Close() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Gateway Close did not cancel managed Claude invocation")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestHarnessSessionTTLConfigurationMustBePositive(t *testing.T) {
	for _, value := range []string{"invalid", "0s", "-1s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LLM_SERVER_HARNESS_SESSION_TTL", value)
			server, err := gateway.Open(filepath.Join(t.TempDir(), "gateway.db"), adminToken, runtimeToken, masterKey, time.Second)
			if server != nil {
				_ = server.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "LLM_SERVER_HARNESS_SESSION_TTL") {
				t.Fatalf("Open() server=%v error=%v", server, err)
			}
		})
	}
}

func createClaudeHarnessProvider(t *testing.T, handler http.Handler, id string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "name": id, "type": "claude_code", "base_url": "", "default_model": "managed-report", "config": map[string]any{"auth_mode": "api_key"}})
	if response := request(t, handler, http.MethodPost, "/v1/providers", adminToken, body); response.Code != http.StatusCreated {
		t.Fatalf("create provider=%d %s", response.Code, response.Body)
	}
	body, _ = json.Marshal(map[string]string{"name": "fixture", "token": "fixture-claude-key"})
	if response := request(t, handler, http.MethodPost, "/v1/providers/"+id+"/tokens", adminToken, body); response.Code != http.StatusCreated {
		t.Fatalf("create token=%d %s", response.Code, response.Body)
	}
}

func createCodexHarnessProvider(t *testing.T, handler http.Handler, model string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": "codex", "name": "Codex", "type": "codex", "base_url": "", "default_model": model})
	if response := request(t, handler, http.MethodPost, "/v1/providers", adminToken, body); response.Code != http.StatusCreated {
		t.Fatalf("create Codex provider=%d %s", response.Code, response.Body)
	}
	if response := request(t, handler, http.MethodPost, "/v1/providers/codex/tokens", adminToken,
		[]byte(`{"name":"fixture","token":"fixture-codex-key"}`)); response.Code != http.StatusCreated {
		t.Fatalf("create Codex token=%d %s", response.Code, response.Body)
	}
}

func createClaudeLocalHarnessProvider(t *testing.T, handler http.Handler, id string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "name": id, "type": "claude_code", "base_url": "", "default_model": "managed-report", "config": map[string]any{"auth_mode": "local_login"}})
	if response := request(t, handler, http.MethodPost, "/v1/providers", adminToken, body); response.Code != http.StatusCreated {
		t.Fatalf("create local provider=%d %s", response.Code, response.Body)
	}
}

func startManagedToolConversation(t *testing.T, handler http.Handler, conversation string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"provider": "claude-a", "model": "managed-report", "conversation_id": conversation,
		"messages": []map[string]any{{"role": "system", "content": "mission"}, {"role": "user", "content": "need-tool"}},
	})
	response := request(t, handler, http.MethodPost, "/v1/generate", runtimeToken, body)
	if response.Code != http.StatusOK {
		t.Fatalf("initial generate=%d %s", response.Code, response.Body)
	}
	var result struct {
		ToolCalls       []map[string]any `json:"tool_calls"`
		SessionMode     string           `json:"session_mode"`
		FallbackReason  string           `json:"fallback_reason"`
		InvocationCount int              `json:"invocation_count"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.ToolCalls) != 1 || result.SessionMode != "cold" ||
		result.FallbackReason != "" || result.InvocationCount != 1 {
		t.Fatalf("initial response=%s", response.Body)
	}
}

func generateManagedClaude(t *testing.T, handler http.Handler, provider, conversation, system, first, continuation string) managedClaudeReport {
	t.Helper()
	messages := []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": first}}
	if continuation != "" {
		messages = append(messages, map[string]any{"role": "assistant", "content": "previous"}, map[string]any{"role": "user", "content": continuation})
	}
	body, _ := json.Marshal(map[string]any{"provider": provider, "model": "managed-report", "conversation_id": conversation, "messages": messages})
	return decodeManagedClaudeReport(t, request(t, handler, http.MethodPost, "/v1/generate", runtimeToken, body))
}

func generateManagedClaudeWithEffort(t *testing.T, handler http.Handler, conversation, effort string) managedClaudeReport {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"provider": "claude-a", "model": "managed-report", "conversation_id": conversation,
		"messages": []map[string]any{{"role": "user", "content": "settings"}},
		"options":  map[string]any{"reasoning_effort": effort},
	})
	return decodeManagedClaudeReport(t, request(t, handler, http.MethodPost, "/v1/generate", runtimeToken, body))
}

func generateManagedClaudeMessages(t *testing.T, handler http.Handler, provider, conversation string, messages []map[string]any) managedClaudeReport {
	t.Helper()
	return decodeManagedClaudeReport(t, generateManagedClaudeRecorder(t, handler, provider, conversation, messages))
}

func generateManagedClaudeRecorder(t *testing.T, handler http.Handler, provider, conversation string, messages []map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"provider": provider, "model": "managed-report", "conversation_id": conversation, "messages": messages})
	return request(t, handler, http.MethodPost, "/v1/generate", runtimeToken, body)
}

func generateClaudeRecorder(t *testing.T, handler http.Handler, provider, conversation string) *httptest.ResponseRecorder {
	t.Helper()
	return generateManagedClaudeRecorder(t, handler, provider, conversation, []map[string]any{{"role": "user", "content": "hello"}})
}

func waitForFixtureFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture barrier %q was not reached", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodeManagedClaudeReport(t *testing.T, response *httptest.ResponseRecorder) managedClaudeReport {
	t.Helper()
	envelope, report := decodeManagedClaudeEnvelope(t, response)
	_ = envelope
	return report
}

func decodeManagedClaudeEnvelope(t *testing.T, response *httptest.ResponseRecorder) (managedClaudeEnvelope, managedClaudeReport) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("generate=%d %s", response.Code, response.Body)
	}
	var envelope managedClaudeEnvelope
	var report managedClaudeReport
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Content == nil || json.Unmarshal([]byte(*envelope.Content), &report) != nil {
		t.Fatalf("invalid managed report: %s", response.Body)
	}
	return envelope, report
}
