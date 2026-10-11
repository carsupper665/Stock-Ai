package test

import (
	"bytes"
	"compress/gzip"
	"crypto/rsa"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"backend/internal/auth"
	"backend/internal/console"
)

const (
	consoleAgentToken   = "agent-private-credential"
	consoleAdminToken   = "llm-admin-private-credential"
	consoleRuntimeToken = "llm-runtime-private-credential"
)

type upstreamRequest struct {
	method, path, authorization string
	header                      http.Header
}

func consoleFixture(t *testing.T, upstream http.Handler) (http.Handler, *rsaFixture) {
	t.Helper()
	now := time.Unix(2_000_000_000, 0)
	clock := &testClock{now: now}
	key := generateRSAKey(t)
	transport := &jwksTransport{keys: map[string]*rsa.PublicKey{oidcKeyID: &key.PublicKey}}
	oidc := &auth.OIDCSettings{
		Issuer: oidcIssuer, Audience: oidcAudience, Subject: oidcSubject, JWKSURL: "https://private-jwks-host.example/keys", TokenType: "at+jwt",
		Now: clock.Now, Client: &http.Client{Transport: transport}, RefreshCooldown: time.Millisecond,
	}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	handler, err := console.New(console.Config{
		OIDC: oidc, AgentToken: consoleAgentToken, GatewayAdminToken: consoleAdminToken, GatewayRuntimeToken: consoleRuntimeToken,
		BackendURL: server.URL, AgentURL: server.URL, GatewayURL: server.URL,
		Public: fstest.MapFS{"index.html": {Data: []byte("<main>console</main>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, &rsaFixture{key: key, now: now}
}

type rsaFixture struct {
	key *rsa.PrivateKey
	now time.Time
}

func (f *rsaFixture) token(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	claims := map[string]any{"iss": oidcIssuer, "aud": oidcAudience, "sub": oidcSubject, "exp": f.now.Add(time.Hour).Unix()}
	if mutate != nil {
		mutate(claims)
	}
	return signJWT(t, f.key, "RS256", "at+jwt", oidcKeyID, claims)
}

func gatewayIdentity(request *http.Request, token, subject string) {
	request.Header.Set("X-Auth-Request-Access-Token", token)
	request.Header.Set("X-Auth-Request-Subject", subject)
	request.Header.Set("X-Auth-Request-User", "Alice")
	request.Header.Set("X-Auth-Request-Email", "alice@example.test")
}

func TestConsoleRequiresOIDCAndDistinctInternalCredentials(t *testing.T) {
	base := console.Config{
		AgentToken: consoleAgentToken, GatewayAdminToken: consoleAdminToken, GatewayRuntimeToken: consoleRuntimeToken,
		BackendURL: "http://backend.test", AgentURL: "http://agent.test", GatewayURL: "http://llm.test",
		Public: fstest.MapFS{"index.html": {Data: []byte("console")}},
	}
	if _, err := console.New(base); err == nil || !strings.Contains(err.Error(), "OIDC") {
		t.Fatalf("missing OIDC configuration was accepted: %v", err)
	}
	base.OIDC = &auth.OIDCSettings{Issuer: "http://idp.test/realm", Audience: oidcAudience, Subject: oidcSubject, JWKSURL: "https://idp.test/keys", TokenType: "at+jwt"}
	if _, err := console.New(base); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure OIDC issuer was accepted: %v", err)
	}
	base.OIDC.Issuer = oidcIssuer
	base.GatewayAdminToken = base.AgentToken
	if _, err := console.New(base); err == nil || !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("duplicate internal credentials were accepted: %v", err)
	}
}

func TestConsoleVerifiesOIDCBeforePrivateCredentialInjection(t *testing.T) {
	requests := make(chan upstreamRequest, 16)
	handler, identity := consoleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- upstreamRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Clone()}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	valid := identity.token(t, nil)

	for _, test := range []struct {
		name, method, path string
		prepare            func(*http.Request)
		status             int
	}{
		{name: "anonymous", path: "/agent/api/v1/sessions", status: http.StatusUnauthorized},
		{name: "spoofed identity headers", path: "/llm/v1/providers", status: http.StatusUnauthorized, prepare: func(r *http.Request) {
			gatewayIdentity(r, "not-a-token", oidcSubject)
			r.Header.Set("Forwarded", "for=trusted.example")
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
		}},
		{name: "wrong audience", path: "/agent/api/v1/sessions", status: http.StatusUnauthorized, prepare: func(r *http.Request) {
			gatewayIdentity(r, identity.token(t, func(c map[string]any) { c["aud"] = "wrong-api" }), oidcSubject)
		}},
		{name: "wrong token subject", path: "/llm/v1/providers", status: http.StatusUnauthorized, prepare: func(r *http.Request) {
			gatewayIdentity(r, identity.token(t, func(c map[string]any) { c["sub"] = "attacker" }), "attacker")
		}},
		{name: "mismatched asserted subject", path: "/agent/api/v1/sessions", status: http.StatusUnauthorized, prepare: func(r *http.Request) {
			gatewayIdentity(r, valid, "attacker")
		}},
		{name: "account cannot elevate to agent", path: "/agent/api/v1/sessions", status: http.StatusForbidden, prepare: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer account-token")
		}},
		{name: "authenticated forbidden agent path", path: "/agent/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) {
			gatewayIdentity(r, valid, oidcSubject)
		}},
		{name: "authenticated forbidden llm path", path: "/llm/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) {
			gatewayIdentity(r, valid, oidcSubject)
		}},
		{name: "unknown agent API route", path: "/agent/api/v1/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) {
			gatewayIdentity(r, valid, oidcSubject)
		}},
		{name: "unknown llm API route", path: "/llm/v1/not-allowed", status: http.StatusNotFound, prepare: func(r *http.Request) {
			gatewayIdentity(r, valid, oidcSubject)
		}},
		{name: "wrong agent method", method: http.MethodPost, path: "/agent/api/v1/sessions/s_1/runs", status: http.StatusMethodNotAllowed, prepare: func(r *http.Request) {
			gatewayIdentity(r, valid, oidcSubject)
		}},
		{name: "wrong llm method", method: http.MethodGet, path: "/llm/v1/generate", status: http.StatusMethodNotAllowed, prepare: func(r *http.Request) {
			gatewayIdentity(r, valid, oidcSubject)
		}},
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
		})
	}
	select {
	case got := <-requests:
		t.Fatalf("denied request reached upstream: %+v", got)
	default:
	}
}

func TestConsoleRecreatesOnlyCorrectUpstreamCredential(t *testing.T) {
	requests := make(chan upstreamRequest, 16)
	handler, identity := consoleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- upstreamRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Clone()}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	valid := identity.token(t, nil)
	cases := []struct{ method, path, authorization string }{
		{http.MethodGet, "/agent/health", "Bearer " + consoleAgentToken},
		{http.MethodGet, "/agent/api/v1/sessions?page=2", "Bearer " + consoleAgentToken},
		{http.MethodGet, "/llm/v1/providers", "Bearer " + consoleAdminToken},
		{http.MethodGet, "/llm/v1/harnesses", "Bearer " + consoleAdminToken},
		{http.MethodPost, "/llm/v1/providers/codex-test/test", "Bearer " + consoleAdminToken},
		{http.MethodPatch, "/llm/v1/providers/codex-test/tokens/tok_1", "Bearer " + consoleAdminToken},
		{http.MethodPost, "/llm/v1/model-catalog", "Bearer " + consoleAdminToken},
		{http.MethodPut, "/llm/v1/model-catalog/model-1", "Bearer " + consoleAdminToken},
		{http.MethodGet, "/llm/v1/models", "Bearer " + consoleRuntimeToken},
		{http.MethodPost, "/llm/v1/generate", "Bearer " + consoleRuntimeToken},
		{http.MethodPost, "/agent/api/v1/sessions/s_1/runs/7/retry", "Bearer " + consoleAgentToken},
	}
	for _, test := range cases {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`))
		gatewayIdentity(request, valid, oidcSubject)
		request.Header.Set("Cookie", "session=browser")
		request.Header.Set("Forwarded", "for=browser")
		request.Header.Set("X-Forwarded-For", "203.0.113.5")
		request.Header.Set("X-Real-IP", "203.0.113.5")
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
		for name := range got.header {
			lower := strings.ToLower(name)
			if lower != "authorization" && (lower == "cookie" || lower == "forwarded" || lower == "proxy-authorization" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-auth-request-")) {
				t.Fatalf("untrusted header reached upstream: %s", name)
			}
		}
	}
}

func TestConsoleKeepsOIDCUserAndAccountDelegationSeparate(t *testing.T) {
	requests := make(chan upstreamRequest, 4)
	handler, identity := consoleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- upstreamRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Clone()}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	valid := identity.token(t, nil)

	userRequest := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	gatewayIdentity(userRequest, valid, oidcSubject)
	userResponse := httptest.NewRecorder()
	handler.ServeHTTP(userResponse, userRequest)
	if userResponse.Code != http.StatusOK || (<-requests).authorization != "Bearer "+valid {
		t.Fatal("OIDC USER token was not recreated for Backend")
	}

	accountRequest := httptest.NewRequest(http.MethodGet, "/v1/account", nil)
	accountRequest.Header.Set("Authorization", "Bearer opaque-account-token")
	// A logged-in browser may also have Gateway headers. Explicit /v1 Bearer
	// semantics remain Account delegation and do not become USER auth.
	gatewayIdentity(accountRequest, valid, oidcSubject)
	accountResponse := httptest.NewRecorder()
	handler.ServeHTTP(accountResponse, accountRequest)
	if accountResponse.Code != http.StatusOK || (<-requests).authorization != "Bearer opaque-account-token" {
		t.Fatal("explicit Account delegation was not preserved")
	}

	for _, raw := range []string{"Basic account", "Bearer a.b.c", "Bearer bad token"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/account", nil)
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

func TestConsoleTranslatesPrivateAuthFailuresAndRejectsCredentialDisclosure(t *testing.T) {
	for _, test := range []struct {
		name       string
		upstream   http.Handler
		wantStatus int
		wantCode   string
	}{
		{name: "upstream 401", wantStatus: http.StatusBadGateway, wantCode: "upstream_authentication_failed", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(consoleAgentToken))
		})},
		{name: "upstream 403", wantStatus: http.StatusBadGateway, wantCode: "upstream_authentication_failed", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})},
		{name: "secret response header", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://example.test/?credential="+consoleAdminToken)
			w.WriteHeader(http.StatusFound)
		})},
		{name: "secret response body", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"debug":"` + consoleRuntimeToken + `"}`))
		})},
		{name: "gzip secret response body", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			compressed := gzip.NewWriter(w)
			_, _ = compressed.Write([]byte(`{"debug":"` + consoleAdminToken + `"}`))
			_ = compressed.Close()
		})},
		{name: "secret response trailer", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Trailer", "X-Debug-Credential")
			_, _ = w.Write([]byte(`{"ok":true}`))
			w.Header().Set("X-Debug-Credential", consoleAgentToken)
		})},
		{name: "unsupported response encoding", wantStatus: http.StatusBadGateway, wantCode: "upstream_response_rejected", upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			_, _ = w.Write([]byte("encoded-response"))
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, identity := consoleFixture(t, test.upstream)
			request := httptest.NewRequest(http.MethodGet, "/agent/api/v1/sessions", nil)
			gatewayIdentity(request, identity.token(t, nil), oidcSubject)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body, _ := io.ReadAll(response.Result().Body)
			if response.Code != test.wantStatus || !strings.Contains(string(body), test.wantCode) {
				t.Fatalf("status=%d body=%s", response.Code, body)
			}
			wire := response.Header().Values("Authorization")
			all := string(body) + strings.Join(wire, " ") + response.Header().Get("Location")
			for _, secret := range []string{consoleAgentToken, consoleAdminToken, consoleRuntimeToken} {
				if strings.Contains(all, secret) {
					t.Fatalf("response disclosed internal credential %q", secret)
				}
			}
		})
	}
}

func TestConsoleReturnsIdentityBodyAndClearsPrivateTrailers(t *testing.T) {
	handler, identity := consoleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	request.Header.Set("Accept-Encoding", "gzip, br")
	request.Header.Set("Range", "bytes=0-")
	gatewayIdentity(request, identity.token(t, nil), oidcSubject)
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

func TestConsoleServesOnlySafePublicPaths(t *testing.T) {
	handler, _ := consoleFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("public request reached upstream")
	}))
	for _, path := range []string{"/.env", "/../.env", "/assets/missing.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s=%d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sessions/s_1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "console") {
		t.Fatal(response.Code, response.Body.String())
	}
}
