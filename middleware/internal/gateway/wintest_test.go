//go:build wintest

package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func winTestServer(t *testing.T, backend string) *Server {
	t.Helper()
	web := t.TempDir()
	if err := os.MkdirAll(filepath.Join(web, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{"index.html": "<!doctype html>app", "assets/app.js": "console.log(1)"} {
		if err := os.WriteFile(filepath.Join(web, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	secrets := t.TempDir()
	for name, logical := range map[string]string{
		"AGENT_SERVER_ADMIN_TOKEN": "agent_admin_token", "LLM_SERVER_ADMIN_TOKEN": "llm_admin_token",
		"LLM_SERVER_RUNTIME_TOKEN": "llm_runtime_token", "BACKEND_USER_TOKEN": "backend_middleware_token",
	} {
		path := filepath.Join(secrets, "stock-ai-test_"+logical)
		if err := os.WriteFile(path, []byte(strings.Repeat(logical[:1], 8)+strings.Repeat("0123456789", 4)+logical), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name+"_FILE", path)
	}
	t.Setenv("STOCK_AI_ENVIRONMENT", "Test")
	t.Setenv("BACKEND_URL", backend)
	t.Setenv("AGENT_SERVER_URL", backend)
	t.Setenv("LLM_SERVER_URL", backend)
	t.Setenv("WEB_DIR", web)
	t.Setenv("USER_NAME", "Tester")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() = %v", err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return s
}

func TestWinTestSignsEveryRequestInAsUSER(t *testing.T) {
	var authorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accounts":[]}`))
	}))
	t.Cleanup(upstream.Close)
	s := winTestServer(t, upstream.URL)

	me := httptest.NewRecorder()
	s.Handler().ServeHTTP(me, httptest.NewRequest(http.MethodGet, "/oauth2/me", nil))
	var user map[string]any
	if me.Code != http.StatusOK || json.Unmarshal(me.Body.Bytes(), &user) != nil || user["user_name"] != "Tester" {
		t.Fatalf("/oauth2/me without a cookie = %d %s", me.Code, me.Body.String())
	}
	proxied := httptest.NewRecorder()
	s.Handler().ServeHTTP(proxied, httptest.NewRequest(http.MethodGet, "/v1/accounts", nil))
	if proxied.Code != http.StatusOK || authorization != "Bearer "+s.cfg.BackendUserToken {
		t.Fatalf("/v1/accounts = %d, upstream Authorization %q", proxied.Code, authorization)
	}
	start := httptest.NewRecorder()
	s.Handler().ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/oauth2/start", nil))
	if start.Code != http.StatusSeeOther || start.Header().Get("Location") != "/" {
		t.Fatalf("/oauth2/start = %d %s", start.Code, start.Header().Get("Location"))
	}
}

func TestWinTestServesTheFrontendWithIndexFallback(t *testing.T) {
	s := winTestServer(t, "http://127.0.0.1:1")
	health := httptest.NewRecorder()
	s.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/middleware/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"version"`) {
		t.Fatalf("/middleware/healthz = %d %s", health.Code, health.Body.String())
	}
	for path, want := range map[string]string{"/": "<!doctype html>app", "/sessions/s_1": "<!doctype html>app", "/assets/app.js": "console.log(1)"} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("GET %s = %d %q", path, response.Code, response.Body.String())
		}
	}
}

func TestWinTestRefusesAnythingButTestEnvironment(t *testing.T) {
	winTestServer(t, "http://127.0.0.1:1")
	t.Setenv("STOCK_AI_ENVIRONMENT", "Release")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("the win-test middleware must not start as Release")
	}
}
