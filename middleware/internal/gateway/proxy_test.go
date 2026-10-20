package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	proxyAgentToken   = "agent-private-credential"
	proxyAdminToken   = "llm-admin-private-credential"
	proxyRuntimeToken = "llm-runtime-private-credential"
	proxyBackendToken = "backend-private-credential"
	proxyAccessToken  = "header.payload.oidc-access-token"
)

const unauthorizedBody = `{"error":"unauthorized","message":"A valid USER session is required."}`

type upstreamRequest struct {
	method, path, authorization string
	header                      http.Header
}

func proxyFixture(t *testing.T, upstream http.Handler) (http.Handler, *http.Cookie) {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	cfg := validConfig("Test")
	cfg.BackendURL, cfg.AgentURL, cfg.LLMURL = server.URL, server.URL, server.URL
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.sessions["sid"] = session{Subject: "user-123", User: "Alice", AccessToken: proxyAccessToken, Expires: s.now().Add(time.Hour)}
	return s.Handler(), &http.Cookie{Name: cfg.CookieName, Value: s.signID("sid")}
}

func recordingUpstream(requests chan upstreamRequest) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- upstreamRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Clone()}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

func TestProxyConfigRequiresUpstreamsAndDistinctCredentials(t *testing.T) {
	base := validConfig("Test")
	if err := base.Validate(); err != nil {
		t.Fatalf("valid proxy configuration rejected: %v", err)
	}
	invalid := map[string]func(*Config){
		"missing backend url":        func(c *Config) { c.BackendURL = "" },
		"missing agent url":          func(c *Config) { c.AgentURL = "" },
		"missing llm url":            func(c *Config) { c.LLMURL = "" },
		"upstream with credentials":  func(c *Config) { c.AgentURL = "http://user:pass@agent.test" },
		"upstream with query":        func(c *Config) { c.LLMURL = "http://llm.test?x=1" },
		"non-http upstream":          func(c *Config) { c.BackendURL = "ftp://backend.test" },
		"missing agent admin token":  func(c *Config) { c.AgentAdminToken = "" },
		"missing llm admin token":    func(c *Config) { c.LLMAdminToken = "" },
		"missing llm runtime token":  func(c *Config) { c.LLMRuntimeToken = "" },
		"missing backend user token": func(c *Config) { c.BackendUserToken = "" },
		"agent equals llm admin":     func(c *Config) { c.LLMAdminToken = c.AgentAdminToken },
		"agent equals runtime":       func(c *Config) { c.LLMRuntimeToken = c.AgentAdminToken },
		"agent equals backend":       func(c *Config) { c.BackendUserToken = c.AgentAdminToken },
		"llm admin equals runtime":   func(c *Config) { c.LLMRuntimeToken = c.LLMAdminToken },
		"llm admin equals backend":   func(c *Config) { c.BackendUserToken = c.LLMAdminToken },
		"runtime equals backend":     func(c *Config) { c.BackendUserToken = c.LLMRuntimeToken },
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			bad := base
			mutate(&bad)
			if _, err := New(bad); err == nil {
				t.Fatal("New accepted unsafe proxy configuration")
			}
		})
	}
}

func TestProxyRequiresSessionBeforePrivateCredentialInjection(t *testing.T) {
	requests := make(chan upstreamRequest, 16)
	handler, cookie := proxyFixture(t, recordingUpstream(requests))
	tampered := *cookie
	tampered.Value += "x"

	for _, test := range []struct {
		name, method, path string
		prepare            func(*http.Request)
		status             int
	}{
		{name: "anonymous v1", path: "/v1/me", status: http.StatusUnauthorized},
		{name: "anonymous bare v1", path: "/v1", status: http.StatusUnauthorized},
		{name: "anonymous agent", path: "/agent/api/v1/sessions", status: http.StatusUnauthorized},
		{name: "anonymous llm", path: "/llm/v1/providers", status: http.StatusUnauthorized},
		{name: "spoofed identity headers", path: "/llm/v1/providers", status: http.StatusUnauthorized, prepare: func(r *http.Request) {
			r.Header.Set("X-Auth-Request-Access-Token", proxyAccessToken)
			r.Header.Set("X-Auth-Request-Subject", "user-123")
			r.Header.Set("Forwarded", "for=trusted.example")
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
		}},
		{name: "tampered session cookie", path: "/agent/api/v1/sessions", status: http.StatusUnauthorized, prepare: func(r *http.Request) {
			r.AddCookie(&tampered)
		}},
		{name: "account cannot elevate to agent", path: "/agent/api/v1/sessions", status: http.StatusForbidden, prepare: func(r *http.Request) {
			r.AddCookie(cookie)
			r.Header.Set("Authorization", "Bearer account-token")
		}},
		{name: "account cannot elevate to llm", path: "/llm/v1/providers", status: http.StatusForbidden, prepare: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer account-token")
		}},
		{name: "forbidden agent path", path: "/agent/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) { r.AddCookie(cookie) }},
		{name: "forbidden llm path", path: "/llm/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) { r.AddCookie(cookie) }},
		{name: "unknown agent API route", path: "/agent/api/v1/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) { r.AddCookie(cookie) }},
		{name: "unknown llm API route", path: "/llm/v1/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) { r.AddCookie(cookie) }},
		{name: "llm generate is not exposed", method: http.MethodPost, path: "/llm/v1/generate", status: http.StatusNotFound, prepare: func(r *http.Request) { r.AddCookie(cookie) }},
		{name: "wrong agent method", method: http.MethodPost, path: "/agent/api/v1/sessions/s_1/runs", status: http.StatusMethodNotAllowed, prepare: func(r *http.Request) { r.AddCookie(cookie) }},
		{name: "wrong llm method", method: http.MethodPost, path: "/llm/v1/models", status: http.StatusMethodNotAllowed, prepare: func(r *http.Request) { r.AddCookie(cookie) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := test.method
			if method == "" {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, test.path, nil)
			if test.prepare != nil {
				test.prepare(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.status == http.StatusUnauthorized && strings.TrimSpace(response.Body.String()) != unauthorizedBody {
				t.Fatalf("body=%s", response.Body.String())
			}
		})
	}
	select {
	case got := <-requests:
		t.Fatalf("denied request reached upstream: %+v", got)
	default:
	}
}

func TestProxyRecreatesOnlyCorrectUpstreamCredential(t *testing.T) {
	requests := make(chan upstreamRequest, 16)
	handler, cookie := proxyFixture(t, recordingUpstream(requests))
	cases := []struct{ method, path, authorization string }{
		{http.MethodGet, "/agent/health", "Bearer " + proxyAgentToken},
		{http.MethodGet, "/agent/api/v1/sessions?page=2", "Bearer " + proxyAgentToken},
		{http.MethodPost, "/agent/api/v1/sessions/s_1/runs/7/retry", "Bearer " + proxyAgentToken},
		{http.MethodGet, "/llm/v1/providers", "Bearer " + proxyAdminToken},
		{http.MethodGet, "/llm/v1/harnesses", "Bearer " + proxyAdminToken},
		{http.MethodPost, "/llm/v1/providers/codex-test/test", "Bearer " + proxyAdminToken},
		{http.MethodPatch, "/llm/v1/providers/codex-test/tokens/tok_1", "Bearer " + proxyAdminToken},
		{http.MethodPost, "/llm/v1/model-catalog", "Bearer " + proxyAdminToken},
		{http.MethodPut, "/llm/v1/model-catalog/model-1", "Bearer " + proxyAdminToken},
		{http.MethodGet, "/llm/v1/models", "Bearer " + proxyRuntimeToken},
		{http.MethodGet, "/v1/me", "Bearer " + proxyBackendToken},
		{http.MethodPost, "/v1/orders", "Bearer " + proxyBackendToken},
	}
	for _, test := range cases {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`))
		request.AddCookie(cookie)
		request.Header.Add("Cookie", "session=browser")
		request.Header.Set("Forwarded", "for=browser")
		request.Header.Set("X-Forwarded-For", "203.0.113.5")
		request.Header.Set("X-Real-IP", "203.0.113.5")
		request.Header.Set("X-Auth-Request-User", "spoofed")
		request.Header.Set("Proxy-Authorization", "Basic spoofed")
		request.Header.Set("Accept-Encoding", "gzip, br")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s returned %d: %s", test.method, test.path, response.Code, response.Body.String())
		}
		got := <-requests
		if got.authorization != test.authorization {
			t.Fatalf("%s credential=%q want %q", test.path, got.authorization, test.authorization)
		}
		if strings.Contains(got.header.Get("Accept-Encoding"), "br") {
			t.Fatalf("browser Accept-Encoding reached upstream: %q", got.header.Get("Accept-Encoding"))
		}
		if strings.HasPrefix(test.path, "/agent") && !strings.HasPrefix(got.path, strings.TrimPrefix(test.path, "/agent")) {
			t.Fatalf("agent path was not rewritten: %q", got.path)
		}
		for name, values := range got.header {
			lower := strings.ToLower(name)
			if lower == "cookie" || lower == "forwarded" || lower == "proxy-authorization" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-auth-request-") {
				t.Fatalf("untrusted header reached upstream: %s", name)
			}
			if strings.Contains(strings.Join(values, " "), proxyAccessToken) {
				t.Fatalf("OIDC access token reached upstream in %s", name)
			}
		}
	}
}

func TestProxyKeepsSessionUserAndAccountDelegationSeparate(t *testing.T) {
	requests := make(chan upstreamRequest, 4)
	handler, cookie := proxyFixture(t, recordingUpstream(requests))

	// The session user is represented by the Backend internal token, never the access token.
	userRequest := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	userRequest.AddCookie(cookie)
	userResponse := httptest.NewRecorder()
	handler.ServeHTTP(userResponse, userRequest)
	if got := <-requests; userResponse.Code != http.StatusOK || got.authorization != "Bearer "+proxyBackendToken {
		t.Fatalf("session user was not mapped to the Backend internal token: %d %q", userResponse.Code, got.authorization)
	}

	// Explicit /v1 Bearer is Account delegation and needs no session.
	accountRequest := httptest.NewRequest(http.MethodGet, "/v1/account", nil)
	accountRequest.Header.Set("Authorization", "Bearer opaque-account-token")
	accountResponse := httptest.NewRecorder()
	handler.ServeHTTP(accountResponse, accountRequest)
	if got := <-requests; accountResponse.Code != http.StatusOK || got.authorization != "Bearer opaque-account-token" {
		t.Fatalf("explicit Account delegation was not preserved: %d %q", accountResponse.Code, got.authorization)
	}

	// Even with a session, an explicit Bearer stays Account delegation.
	both := httptest.NewRequest(http.MethodGet, "/v1/account", nil)
	both.AddCookie(cookie)
	both.Header.Set("Authorization", "Bearer opaque-account-token")
	handler.ServeHTTP(httptest.NewRecorder(), both)
	if got := <-requests; got.authorization != "Bearer opaque-account-token" {
		t.Fatalf("session replaced Account delegation: %q", got.authorization)
	}

	for _, raw := range []string{"Basic account", "Bearer a.b.c", "Bearer bad token"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/account", nil)
		request.AddCookie(cookie)
		request.Header.Set("Authorization", raw)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("invalid Account syntax %q returned %d", raw, response.Code)
		}
	}
	select {
	case got := <-requests:
		t.Fatalf("invalid Account credential reached Backend: %+v", got)
	default:
	}
}

func TestProxyTranslatesPrivateAuthFailuresAndRejectsCredentialDisclosure(t *testing.T) {
	for _, test := range []struct {
		name       string
		upstream   http.Handler
		wantStatus int
		wantCode   string
	}{
		{name: "upstream 401", wantStatus: http.StatusBadGateway, wantCode: "upstream_authentication_failed", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(proxyAgentToken))
		})},
		{name: "upstream 403", wantStatus: http.StatusBadGateway, wantCode: "upstream_authentication_failed", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})},
		{name: "secret response header", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://example.test/?credential="+proxyAdminToken)
			w.WriteHeader(http.StatusFound)
		})},
		{name: "secret response body", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"debug":"` + proxyRuntimeToken + `"}`))
		})},
		{name: "gzip secret response body", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			compressed := gzip.NewWriter(w)
			_, _ = compressed.Write([]byte(`{"debug":"` + proxyAdminToken + `"}`))
			_ = compressed.Close()
		})},
		{name: "secret response trailer", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Trailer", "X-Debug-Credential")
			_, _ = w.Write([]byte(`{"ok":true}`))
			w.Header().Set("X-Debug-Credential", proxyAgentToken)
		})},
		{name: "unsupported response encoding", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			_, _ = w.Write([]byte("encoded-response"))
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, cookie := proxyFixture(t, test.upstream)
			request := httptest.NewRequest(http.MethodGet, "/agent/api/v1/sessions", nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body, _ := io.ReadAll(response.Result().Body)
			if response.Code != test.wantStatus || !strings.Contains(string(body), test.wantCode) {
				t.Fatalf("status=%d body=%s", response.Code, body)
			}
			wire := response.Header().Values("Authorization")
			all := string(body) + strings.Join(wire, " ") + response.Header().Get("Location")
			for _, secret := range []string{proxyAgentToken, proxyAdminToken, proxyRuntimeToken, proxyBackendToken} {
				if strings.Contains(all, secret) {
					t.Fatalf("response disclosed internal credential %q", secret)
				}
			}
		})
	}
}

func TestProxyReturnsIdentityBodyAndClearsPrivateTrailers(t *testing.T) {
	handler, cookie := proxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "" {
			t.Errorf("browser Accept-Encoding reached upstream: %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Trailer", "X-Auth-Request-User, X-Safe-Trailer")
		compressed := gzip.NewWriter(w)
		_, _ = compressed.Write([]byte(`{"ok":true}`))
		_ = compressed.Close()
		w.Header().Set("X-Auth-Request-User", "spoofed")
		w.Header().Set("X-Safe-Trailer", "discard-me")
	}))
	request := httptest.NewRequest(http.MethodGet, "/agent/api/v1/sessions", nil)
	request.AddCookie(cookie)
	request.Header.Set("Accept-Encoding", "gzip, br")
	request.Header.Set("Range", "bytes=0-")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), []byte(`{"ok":true}`)) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	result := response.Result()
	if result.Header.Get("Content-Encoding") != "" || result.Header.Get("Trailer") != "" || len(result.Trailer) != 0 {
		t.Fatalf("encoded/trailer metadata escaped: header=%v trailer=%v", result.Header, result.Trailer)
	}
}

func TestOAuth2AuthEndpointIsRemoved(t *testing.T) {
	handler, cookie := proxyFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached upstream")
	}))
	request := httptest.NewRequest(http.MethodGet, "https://app.example/oauth2/auth", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Header().Get("X-Auth-Request-Access-Token") != "" {
		t.Fatalf("/oauth2/auth=%d headers=%v", response.Code, response.Header())
	}
}

func TestProxyForwardsTheRunStreamAsEachEventArrives(t *testing.T) {
	release := make(chan struct{})
	handler, cookie := proxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions/s_1/runs/3/stream" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		_, _ = w.Write([]byte("event: delta\ndata: {\"content\":\"first\"}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("event: end\ndata: {}\n\n"))
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/agent/api/v1/sessions/s_1/runs/3/stream", nil)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
		response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream = %d %q %q", response.StatusCode, response.Header.Get("Content-Type"), response.Header.Get("X-Accel-Buffering"))
	}
	lines := bufio.NewReader(response.Body)
	first := make(chan string, 1)
	go func() { line, _ := lines.ReadString('\n'); first <- line }()
	select {
	case line := <-first:
		if line != "event: delta\n" {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first event was held back until the upstream finished")
	}
	close(release)
	rest, _ := io.ReadAll(lines)
	if !strings.HasSuffix(string(rest), "event: end\ndata: {}\n\n") {
		t.Fatalf("rest of stream = %q", rest)
	}

	post := httptest.NewRequest(http.MethodPost, "/agent/api/v1/sessions/s_1/runs/3/stream", nil)
	post.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, post)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST stream = %d, want 405", recorder.Code)
	}
}
