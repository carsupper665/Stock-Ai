package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyLogsUpstreamFailuresAndRequestsWithoutQueries(t *testing.T) {
	logs := captureLog(t)
	handler, cookie := proxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cfg := validConfig("Test")
	cfg.BackendURL, cfg.AgentURL, cfg.LLMURL = closed.URL, closed.URL, closed.URL
	down, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	down.sessions["sid"] = session{Subject: "user-123", User: "Alice", AccessToken: proxyAccessToken, Expires: down.now().Add(time.Hour)}
	for _, target := range []http.Handler{handler, down.Handler()} {
		for _, path := range []string{"/agent/api/v1/sessions?trace=query-value", "/healthz"} {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(cookie)
			target.ServeHTTP(httptest.NewRecorder(), request)
		}
	}

	text := logs.String()
	for _, want := range []string{
		"upstream agent-server rejected the service credential: GET /api/v1/sessions -> 401",
		"upstream agent-server unavailable: GET /api/v1/sessions:",
		"event=http_request method=GET path=/agent/api/v1/sessions status=502",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "query-value") || strings.Contains(text, "path=/healthz") {
		t.Fatalf("log exposed a query string or a passing health probe:\n%s", text)
	}
}

func TestLoginSuccessAndRejectionAreLogged(t *testing.T) {
	logs := captureLog(t)
	s, f := newFixture(t)
	if res := callback(t, s, f, begin(t, s, f, "/"), "ok", ""); res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/" {
		t.Fatalf("login = %d %s", res.Code, res.Header().Get("Location"))
	}
	if res := callback(t, s, f, "forged-state", "ok", ""); !strings.HasPrefix(res.Header().Get("Location"), "/login?auth_error=") {
		t.Fatalf("forged callback = %d %s", res.Code, res.Header().Get("Location"))
	}
	text := logs.String()
	if !strings.Contains(text, "OIDC login succeeded: user=") || !strings.Contains(text, "OIDC login rejected: invalid_callback") {
		t.Fatalf("login outcomes not logged:\n%s", text)
	}
}
