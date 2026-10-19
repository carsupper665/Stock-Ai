package gateway

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeIDP struct {
	t                *testing.T
	key              *rsa.PrivateKey
	mu               sync.Mutex
	nonce, challenge string
	loginCookie      *http.Cookie
	tokenCalls       int
	access           string
	discovery        map[string]any
	authorizeStatus  int
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func validConfig(environment string) Config {
	suffix := strings.ToLower(environment)
	clientID := "stock-ai-" + suffix
	issuer := "https://issuer.example/realms/" + clientID
	return Config{Environment: environment, Issuer: issuer, AuthorizationURL: issuer + "/authorize", TokenURL: issuer + "/token", JWKSURL: issuer + "/jwks", ClientID: clientID, ClientSecret: strings.Repeat("s", 40), TokenAuthMethod: "client_secret_basic", AccessTokenType: "at+jwt", Audience: "stock-ai-api-" + suffix, RedirectURI: "https://app.example/oauth2/callback", EndSessionURL: issuer + "/logout", PostLogoutURI: "https://app.example/", CookieName: "__Host-" + clientID + "-session", CookieKey: []byte("0123456789abcdef0123456789abcdef"), UserSubject: "user-123", SessionTTL: time.Hour, LoginTTL: 10 * time.Minute,
		BackendURL: "http://backend.test", AgentURL: "http://agent.test", LLMURL: "http://llm.test",
		AgentAdminToken: proxyAgentToken, LLMAdminToken: proxyAdminToken, LLMRuntimeToken: proxyRuntimeToken, BackendUserToken: proxyBackendToken}
}

func newFixture(t *testing.T) (*Server, *fakeIDP) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{t: t, key: key}
	f.access = f.signTyped("at+jwt", map[string]any{"iss": "https://issuer.example/realms/stock-ai-test", "aud": []string{"stock-ai-api-test", "account"}, "sub": "user-123", "exp": time.Now().Add(time.Hour).Unix()})
	cfg := validConfig("Test")
	issuer := cfg.Issuer
	f.discovery = map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "end_session_endpoint": issuer + "/logout", "token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"}}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		out := httptest.NewRecorder()
		f.serve(out, r)
		return out.Result(), nil
	})}
	return s, f
}

func TestSecurityGateHeaders(t *testing.T) {
	s, _ := newFixture(t)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://app.example/oauth2/me", nil))
	for name, want := range map[string]string{
		"Cache-Control": "no-store", "Pragma": "no-cache",
		"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
	} {
		if got := response.Header().Get(name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
}

func TestHealthEndpointIsUnauthenticatedAndNoContent(t *testing.T) {
	s, _ := newFixture(t)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway:4180/healthz", nil))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("health response = %d %q, want 204 with empty body", response.Code, response.Body.String())
	}
}

func (f *fakeIDP) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/realms/stock-ai-test/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(f.discovery)
	case "/realms/stock-ai-test/authorize":
		if f.authorizeStatus != 0 {
			http.Error(w, "Invalid parameter: redirect_uri", f.authorizeStatus)
			return
		}
		_, _ = io.WriteString(w, "<html>login</html>")
	case "/realms/stock-ai-test/jwks":
		n := base64.RawURLEncoding.EncodeToString(f.key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "key-1", "n": n, "e": e}}})
	case "/realms/stock-ai-test/token":
		f.mu.Lock()
		f.tokenCalls++
		nonce, challenge := f.nonce, f.challenge
		f.mu.Unlock()
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", 400)
			return
		}
		id, secret, basic := r.BasicAuth()
		if !basic {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		} else if r.PostForm.Has("client_secret") {
			http.Error(w, "two client authentication methods", 400)
			return
		}
		if decodedID, err := url.QueryUnescape(id); basic && err == nil {
			id = decodedID
		}
		if decodedSecret, err := url.QueryUnescape(secret); basic && err == nil {
			secret = decodedSecret
		}
		if id != "stock-ai-test" || secret != strings.Repeat("s", 40) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"echoed client secret"}`)
			return
		}
		code := r.Form.Get("code")
		digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge || code == "pkce-fail" {
			http.Error(w, "pkce", 400)
			return
		}
		if code == "exchange-fail" {
			http.Error(w, "exchange", 503)
			return
		}
		if code == "bad-nonce" {
			nonce = "wrong"
		}
		idToken := f.sign(map[string]any{"iss": "https://issuer.example/realms/stock-ai-test", "aud": "stock-ai-test", "sub": "user-123", "nonce": nonce, "preferred_username": "Alice", "email": "alice@example.test", "exp": time.Now().Add(time.Hour).Unix()})
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.access, "id_token": idToken, "token_type": "Bearer", "expires_in": 3600})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeIDP) sign(claims map[string]any) string { return f.signTyped("JWT", claims) }

func (f *fakeIDP) signTyped(typ string, claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	unsigned := enc(map[string]string{"alg": "RS256", "kid": "key-1", "typ": typ}) + "." + enc(claims)
	d := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, d[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func begin(t *testing.T, s *Server, f *fakeIDP, returnTo string) (state string) {
	t.Helper()
	req := httptest.NewRequest("GET", "https://app.example/oauth2/start?return="+url.QueryEscape(returnTo), nil)
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, req)
	if res.Code != 302 {
		t.Fatalf("start=%d %s", res.Code, res.Body.String())
	}
	location, err := url.Parse(res.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := location.Query()
	for _, name := range []string{"state", "nonce", "code_challenge"} {
		if q.Get(name) == "" {
			t.Fatalf("missing %s", name)
		}
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("not authorization code + PKCE: %s", location)
	}
	f.mu.Lock()
	f.nonce = q.Get("nonce")
	f.challenge = q.Get("code_challenge")
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == s.loginCookieName() {
			f.loginCookie = cookie
		}
	}
	f.mu.Unlock()
	if f.loginCookie == nil || f.loginCookie.Name != "__Secure-stock-ai-test-login" || !f.loginCookie.HttpOnly || !f.loginCookie.Secure || f.loginCookie.SameSite != http.SameSiteLaxMode || f.loginCookie.Path != "/oauth2/callback" || f.loginCookie.Domain != "" {
		t.Fatal("secure browser-bound login cookie missing")
	}
	return q.Get("state")
}
func callback(t *testing.T, s *Server, f *fakeIDP, state, code, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "https://app.example/oauth2/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	if cookie != "" {
		req.Header.Set("Cookie", s.cfg.CookieName+"="+cookie)
	}
	req.AddCookie(f.loginCookie)
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, req)
	return res
}
func activeCookie(t *testing.T, res *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, c := range res.Result().Cookies() {
		if c.Name == name && c.MaxAge > 0 {
			found = c
		}
	}
	if found == nil {
		t.Fatal("active session cookie missing")
	}
	return found
}

func TestBrowserFirstLoginRefreshAndSessionFixation(t *testing.T) {
	s, f := newFixture(t)
	state := begin(t, s, f, "/accounts?tab=1")
	planted := "attacker.chosen"
	res := callback(t, s, f, state, "ok", planted)
	if res.Code != 303 || res.Header().Get("Location") != "/accounts?tab=1" {
		t.Fatalf("callback=%d %s", res.Code, res.Header().Get("Location"))
	}
	cookie := activeCookie(t, res, s.cfg.CookieName)
	if cookie.Value == planted || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("unsafe or fixed cookie: %#v", cookie)
	}
	if strings.Contains(cookie.Value, f.access) {
		t.Fatal("access token leaked in cookie")
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "https://app.example/oauth2/me", nil)
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("refresh %d=%d", i, out.Code)
		}
		body := out.Body.String()
		if !strings.Contains(body, "Alice") || strings.Contains(body, f.access) {
			t.Fatalf("unsafe /me: %s", body)
		}
	}
	tampered := *cookie
	tampered.Value += "x"
	req := httptest.NewRequest("GET", "https://app.example/oauth2/me", nil)
	req.AddCookie(&tampered)
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusUnauthorized {
		t.Fatalf("tampered session cookie=%d", out.Code)
	}
}

func TestBrowserExpiryAndLogout(t *testing.T) {
	s, f := newFixture(t)
	base := time.Now()
	s.now = func() time.Time { return base }
	state := begin(t, s, f, "/")
	cookie := activeCookie(t, callback(t, s, f, state, "ok", ""), s.cfg.CookieName)
	base = base.Add(2 * time.Hour)
	req := httptest.NewRequest("GET", "https://app.example/oauth2/me", nil)
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatalf("expired session=%d", out.Code)
	}
	s.now = time.Now
	state = begin(t, s, f, "/")
	cookie = activeCookie(t, callback(t, s, f, state, "ok", ""), s.cfg.CookieName)
	bad := httptest.NewRequest("POST", "https://app.example/oauth2/logout", nil)
	bad.AddCookie(cookie)
	out = httptest.NewRecorder()
	s.Handler().ServeHTTP(out, bad)
	if out.Code != 403 {
		t.Fatalf("cross-site logout=%d", out.Code)
	}
	request := httptest.NewRequest("POST", "https://app.example/oauth2/logout", nil)
	request.Header.Set("Origin", "https://app.example")
	request.AddCookie(cookie)
	out = httptest.NewRecorder()
	s.Handler().ServeHTTP(out, request)
	if out.Code != 303 || !strings.HasPrefix(out.Header().Get("Location"), "https://issuer.example/realms/stock-ai-test/logout?") {
		t.Fatalf("logout=%d %s", out.Code, out.Header().Get("Location"))
	}
	req = httptest.NewRequest("GET", "https://app.example/oauth2/me", nil)
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatalf("logged-out session=%d", out.Code)
	}
}

func TestCallbackIsOneTimeAndAllFailuresStayUnauthenticated(t *testing.T) {
	tests := []struct {
		name, code string
		denial     bool
	}{{"nonce", "bad-nonce", false}, {"pkce", "pkce-fail", false}, {"exchange", "exchange-fail", false}, {"denial", "", true}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newFixture(t)
			state := begin(t, s, f, "/")
			var out *httptest.ResponseRecorder
			if tc.denial {
				req := httptest.NewRequest("GET", "https://app.example/oauth2/callback?state="+state+"&error=access_denied&error_description=secret", nil)
				req.AddCookie(f.loginCookie)
				out = httptest.NewRecorder()
				s.Handler().ServeHTTP(out, req)
			} else {
				out = callback(t, s, f, state, tc.code, "")
			}
			if out.Code != 303 || !strings.HasPrefix(out.Header().Get("Location"), "/login?auth_error=") {
				t.Fatalf("unsafe failure: %d %s", out.Code, out.Header().Get("Location"))
			}
			if strings.Contains(out.Header().Get("Location"), "secret") {
				t.Fatal("IdP error leaked")
			}
			if c := out.Result().Cookies(); len(c) == 0 || c[len(c)-1].MaxAge >= 0 {
				t.Fatal("session was not cleared")
			}
		})
	}
	s, f := newFixture(t)
	state := begin(t, s, f, "/")
	first := callback(t, s, f, state, "ok", "")
	if first.Code != 303 {
		t.Fatal("first callback failed")
	}
	replay := callback(t, s, f, state, "ok", "")
	if replay.Code != 303 || replay.Header().Get("Location") != "/login?auth_error=invalid_callback" {
		t.Fatalf("replay not rejected: %d %s", replay.Code, replay.Header().Get("Location"))
	}
	invalid := callback(t, s, f, "unknown", "ok", "")
	if invalid.Header().Get("Location") != "/login?auth_error=invalid_callback" {
		t.Fatal("invalid state not rejected")
	}
}

func TestAuthorizationURLAndResponsesContainNoSecrets(t *testing.T) {
	s, f := newFixture(t)
	state := begin(t, s, f, "//evil.example/")
	res := callback(t, s, f, state, "ok", "")
	raw := res.Header().Get("Location") + res.Body.String()
	for _, secret := range []string{s.cfg.ClientSecret, f.access} {
		if strings.Contains(raw, secret) {
			t.Fatal("credential leaked to browser response")
		}
	}
	if res.Header().Get("Location") != "/" {
		t.Fatalf("open redirect: %s", res.Header().Get("Location"))
	}
}

func TestConfigEnforcesEnvironmentIdentity(t *testing.T) {
	s, _ := newFixture(t)
	bad := s.cfg
	bad.Environment = "Release"
	if bad.Validate() == nil {
		t.Fatal("Test client/cookie accepted in Release")
	}
	bad = s.cfg
	bad.CookieName = "stock-ai-test-session"
	if bad.Validate() == nil {
		t.Fatal("non-Host cookie accepted")
	}
}

func TestLoginTransactionCookiePrefixIsAcceptedByBrowsers(t *testing.T) {
	for _, environment := range []string{"Test", "Release"} {
		t.Run(environment, func(t *testing.T) {
			cfg := validConfig(environment)
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			cookie := s.loginCookie("opaque", 60)
			wantName := "__Secure-stock-ai-" + strings.ToLower(environment) + "-login"
			if cookie.Name != wantName || !strings.HasPrefix(cookie.Name, "__Secure-") || cookie.Path != "/oauth2/callback" || !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" {
				t.Fatalf("transaction cookie violates prefix/path contract: %#v", cookie)
			}
			if strings.HasPrefix(cookie.Name, "__Host-") {
				t.Fatal("a narrow-path transaction cookie must not use the __Host- prefix")
			}
		})
	}
}

func TestConfigRejectsLoopbackAndLocalhostAliasesInBothEnvironments(t *testing.T) {
	setHost := func(raw, host string) string {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		u.Host = host
		return u.String()
	}
	for _, environment := range []string{"Test", "Release"} {
		for _, host := range []string{"localhost", "localhost.", "login.localhost", "127.0.0.1", "127.42.0.9", "[::1]"} {
			for _, target := range []string{"issuer", "redirect"} {
				name := environment + "/" + target + "/" + strings.ReplaceAll(host, ":", "_")
				t.Run(name, func(t *testing.T) {
					cfg := validConfig(environment)
					if target == "issuer" {
						cfg.Issuer = setHost(cfg.Issuer, host)
					} else {
						cfg.RedirectURI = setHost(cfg.RedirectURI, host)
						cfg.PostLogoutURI = setHost(cfg.PostLogoutURI, host)
					}
					if cfg.Validate() == nil {
						t.Fatalf("%s accepted local host %q", environment, host)
					}
				})
			}
		}
	}
}

func TestJWKSRejectsWeakEvenExponentAndDuplicateSigningKeys(t *testing.T) {
	strong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	jwk := func(key *rsa.PrivateKey, kid string, exponent []byte) map[string]string {
		return map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(exponent)}
	}
	encode := func(keys ...map[string]string) []byte {
		raw, err := json.Marshal(map[string]any{"keys": keys})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	valid := jwk(strong, "key-1", []byte{1, 0, 1})
	tests := map[string][]byte{
		"modulus below 2048 bits": encode(jwk(weak, "key-1", []byte{1, 0, 1})),
		"even exponent":           encode(jwk(strong, "key-1", []byte{4})),
		"duplicate kid":           encode(valid, valid),
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseJWKS(document); err == nil {
				t.Fatal("unsafe JWKS was accepted")
			}
		})
	}
	keys, err := parseJWKS(encode(valid))
	if err != nil || keys["key-1"] == nil {
		t.Fatalf("valid 2048-bit odd-exponent key rejected: %v", err)
	}
}

func TestTokenRequestUsesConfidentialClientAndVerifier(t *testing.T) {
	s, f := newFixture(t)
	state := begin(t, s, f, "/")
	res := callback(t, s, f, state, "ok", "")
	if res.Code != 303 {
		body, _ := io.ReadAll(res.Result().Body)
		t.Fatalf("callback failed: %s", body)
	}
	f.mu.Lock()
	calls := f.tokenCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("token calls=%d", calls)
	}
}
