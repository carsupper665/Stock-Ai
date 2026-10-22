package test

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	llmproviderserver "stock-ai/llm-provider-server"
)

func TestGatewayLogsRequestsAndProviderErrorsWithoutSecrets(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	var terminal bytes.Buffer
	previousOutput, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&terminal)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	}()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"failed for ` + diagnosticSecret + `"}}`))
	}))
	defer upstream.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), upstream.URL, "model", true)
	createToken(t, server.Handler(), "fixture", "main", diagnosticSecret)

	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate?trace="+diagnosticSecret, runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)), http.StatusBadGateway, "PROVIDER_UNAVAILABLE")
	if response := request(t, server.Handler(), http.MethodGet, "/healthz", "", nil); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"version":"`+llmproviderserver.Version+`"`) {
		t.Fatalf("health = %d %s, want version %q", response.Code, response.Body.String(), llmproviderserver.Version)
	}

	logs := terminal.String()
	for _, want := range []string{
		"event=provider_error provider=fixture type=openai_compatible status=502 detail=PROVIDER_UNAVAILABLE",
		"event=http_request method=POST path=/v1/generate status=502",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, diagnosticSecret) || strings.Contains(logs, "path=/healthz") {
		t.Fatalf("log exposed a secret or a passing health probe:\n%s", logs)
	}
}
