package test

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const diagnosticSecret = "SYNTHETIC-GATEWAY-SECRET-7f91"

func TestProviderDiagnosticsDoNotExposeCredentials(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	for _, test := range []struct {
		name       string
		body       string
		wantDetail string
	}{
		{
			name:       "known quota code keeps a fixed useful category",
			body:       `{"error":{"code":"insufficient_quota","message":"quota failed for ` + diagnosticSecret + `"}}`,
			wantDetail: "quota",
		},
		{
			name:       "known model code keeps a fixed useful category",
			body:       `{"error":{"code":"model_not_found","message":"unknown model for ` + diagnosticSecret + `"}}`,
			wantDetail: "requested model",
		},
		{
			name:       "known expiry code keeps a fixed useful category",
			body:       `{"error":{"status":"token_expired","message":"login document ` + diagnosticSecret + `"}}`,
			wantDetail: "expired",
		},
		{
			name: "unknown JSON and plain prose stay private",
			body: strings.Repeat("x", 295) + diagnosticSecret + ` {"message":"` + diagnosticSecret + `"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(test.body))
			}))
			defer upstream.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createProvider(t, server.Handler(), upstream.URL, "model", true)
			createToken(t, server.Handler(), "fixture", "main", diagnosticSecret)

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
			assertError(t, response, http.StatusBadGateway, "PROVIDER_UNAVAILABLE")
			body := response.Body.String()
			if strings.Contains(body, diagnosticSecret) || strings.Contains(body, strings.Repeat("x", 100)) {
				t.Fatalf("public Provider diagnostic exposed upstream prose: %s", body)
			}
			if test.wantDetail != "" && !strings.Contains(strings.ToLower(body), test.wantDetail) {
				t.Fatalf("public Provider diagnostic lost safe category %q: %s", test.wantDetail, body)
			}
		})
	}
}

func TestHarnessOperationalLogsExcludePromptsCredentialsAndRawStderr(t *testing.T) {
	var terminal bytes.Buffer
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&terminal)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	}()

	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	server := openTestServer(t, 5*time.Second)
	defer server.Close()
	createCodexHarnessProvider(t, server.Handler(), "diagnostic-stderr-secret")
	promptSecret := "SYNTHETIC-PROMPT-SECRET-9224"
	body, _ := json.Marshal(map[string]any{
		"provider": "codex", "messages": []map[string]any{{"role": "user", "content": promptSecret}},
	})
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body), http.StatusBadGateway, "PROVIDER_UNAVAILABLE")

	logs := terminal.String()
	if !strings.Contains(logs, "web_harness event=error harness=codex category=unavailable") {
		t.Fatalf("safe operational event missing: %q", logs)
	}
	for _, forbidden := range []string{promptSecret, diagnosticSecret, "stderr"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("operational log exposed %q: %q", forbidden, logs)
		}
	}
}

func TestHarnessDiagnosticsDoNotExposeCredentials(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	for _, kind := range []string{"codex", "claude_code"} {
		t.Run(kind, func(t *testing.T) {
			variable, fixture, key := "LLM_SERVER_CODEX_EXECUTABLE", "codex-fixture.exe", "fixture-codex-key"
			if kind == "claude_code" {
				variable, fixture, key = "LLM_SERVER_CLAUDE_EXECUTABLE", "claude-fixture.exe", "fixture-claude-key"
			}
			t.Setenv(variable, copyHarnessFixture(t, fixture))
			server := openTestServer(t, 5*time.Second)
			defer server.Close()
			providerBody, _ := json.Marshal(map[string]any{
				"id": "cli", "name": kind, "type": kind, "base_url": "", "default_model": "diagnostic-secret",
			})
			if response := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, providerBody); response.Code != http.StatusCreated {
				t.Fatalf("create %s Provider = %d %s", kind, response.Code, response.Body)
			}
			tokenBody, _ := json.Marshal(map[string]string{"name": "fixture", "token": key})
			if response := request(t, server.Handler(), http.MethodPost, "/v1/providers/cli/tokens", adminToken, tokenBody); response.Code != http.StatusCreated {
				t.Fatalf("create %s token = %d %s", kind, response.Code, response.Body)
			}

			for _, diagnostic := range []struct {
				model, category, code string
			}{
				{model: "diagnostic-secret", category: "quota", code: "PROVIDER_UNAVAILABLE"},
				{model: "diagnostic-model-secret", category: "requested model", code: "PROVIDER_UNAVAILABLE"},
				{model: "diagnostic-login-secret", category: "expired", code: "AUTHENTICATION_FAILED"},
				{model: "diagnostic-stderr-secret", code: "PROVIDER_UNAVAILABLE"},
			} {
				body, _ := json.Marshal(map[string]any{
					"provider": "cli", "model": diagnostic.model,
					"messages": []any{map[string]string{"role": "user", "content": "hello"}},
				})
				response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body)
				assertError(t, response, http.StatusBadGateway, diagnostic.code)
				publicBody := response.Body.String()
				if strings.Contains(publicBody, diagnosticSecret) || strings.Contains(publicBody, strings.Repeat("x", 100)) {
					t.Fatalf("%s %s diagnostic exposed CLI prose: %s", kind, diagnostic.model, publicBody)
				}
				if diagnostic.category != "" && !strings.Contains(strings.ToLower(publicBody), diagnostic.category) {
					t.Fatalf("%s diagnostic lost safe %s category: %s", kind, diagnostic.category, publicBody)
				}
			}
		})
	}
}
