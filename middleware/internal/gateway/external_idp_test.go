package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buffer)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buffer
}

func TestConfigAcceptsExternalIdPClientAndIssuer(t *testing.T) {
	cfg := validConfig("Release")
	issuer := "https://login.example.com/application/o/stock/"
	cfg.ClientID = "c0ffee-stock-ai"
	cfg.Issuer = issuer
	cfg.AuthorizationURL = "https://login.example.com/application/o/authorize/"
	cfg.TokenURL = "https://login.example.com/application/o/token/"
	cfg.JWKSURL = "https://login.example.com/application/o/stock/jwks/"
	cfg.EndSessionURL = "https://login.example.com/application/o/stock/end-session/"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("external IdP configuration rejected: %v", err)
	}
	invalid := map[string]func(*Config){
		"token endpoint on another origin": func(c *Config) { c.TokenURL = "https://other.example.com/token" },
		"client id with whitespace":        func(c *Config) { c.ClientID = "stock ai" },
		"empty client id":                  func(c *Config) { c.ClientID = "" },
		"unknown token auth method":        func(c *Config) { c.TokenAuthMethod = "private_key_jwt" },
		"missing audience":                 func(c *Config) { c.Audience = "" },
		"missing access token type":        func(c *Config) { c.AccessTokenType = "" },
		"plain http issuer":                func(c *Config) { c.Issuer = "http://login.example.com/application/o/stock/" },
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			bad := cfg
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestTokenRequestSupportsClientSecretPost(t *testing.T) {
	s, f := newFixture(t)
	s.cfg.TokenAuthMethod = "client_secret_post"
	var sawBasic bool
	transport := s.client.Transport
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if _, _, ok := r.BasicAuth(); ok {
			sawBasic = true
		}
		return transport.RoundTrip(r)
	})
	state := begin(t, s, f, "/")
	res := callback(t, s, f, state, "ok", "")
	if res.Code != 303 || res.Header().Get("Location") != "/" || sawBasic {
		t.Fatalf("client_secret_post login=%d %s basic=%t", res.Code, res.Header().Get("Location"), sawBasic)
	}
}

func TestTokenRequestBasicCredentialsAreFormEncoded(t *testing.T) {
	s, _ := newFixture(t)
	s.cfg.ClientID = "stock:ai"
	s.cfg.ClientSecret = "a+b/c:d" + strings.Repeat("s", 32)
	var user, password string
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		user, password, _ = r.BasicAuth()
		out := httptest.NewRecorder()
		out.WriteHeader(http.StatusBadRequest)
		return out.Result(), nil
	})}
	_, _ = s.exchange(context.Background(), "code", "verifier")
	if user != "stock%3Aai" || password != "a%2Bb%2Fc%3Ad"+strings.Repeat("s", 32) {
		t.Fatalf("Basic credentials not RFC 6749 form-encoded: %q %q", user, password)
	}
}

func TestTokenExchangeFailureLogsOnlyTheOAuthErrorCode(t *testing.T) {
	logs := captureLog(t)
	s, f := newFixture(t)
	s.cfg.ClientSecret = strings.Repeat("x", 40)
	state := begin(t, s, f, "/")
	res := callback(t, s, f, state, "ok", "")
	if res.Header().Get("Location") != "/login?auth_error=login_failed" {
		t.Fatalf("rejected client did not fail login: %s", res.Header().Get("Location"))
	}
	text := logs.String()
	if !strings.Contains(text, "token endpoint returned 401 (error=invalid_client, OIDC_TOKEN_AUTH_METHOD=client_secret_basic)") {
		t.Fatalf("token failure not diagnosed: %q", text)
	}
	if strings.Contains(text, "echoed") || strings.Contains(text, s.cfg.ClientSecret) {
		t.Fatalf("token failure log leaked IdP description or secret: %q", text)
	}
}

func TestAccessTokenThatBackendWouldRejectFailsLogin(t *testing.T) {
	issuer := "https://issuer.example/realms/stock-ai-test"
	valid := map[string]any{"iss": issuer, "aud": "stock-ai-api-test", "sub": "user-123", "exp": time.Now().Add(time.Hour).Unix()}
	with := func(key string, value any) map[string]any {
		claims := map[string]any{}
		for k, v := range valid {
			claims[k] = v
		}
		claims[key] = value
		return claims
	}
	tests := []struct {
		name, typ, reason string
		claims            map[string]any
		opaque            bool
	}{
		{"typ JWT while at+jwt is configured", "JWT", `header typ="JWT" but OIDC_TOKEN_TYPE="at+jwt"`, valid, false},
		{"audience mapper missing", "at+jwt", `does not contain OIDC_AUDIENCE="stock-ai-api-test"`, with("aud", "account"), false},
		{"issuer differs", "at+jwt", `but OIDC_ISSUER=`, with("iss", "https://issuer.example/other"), false},
		{"subject differs", "at+jwt", "sub does not match OIDC_USER_SUBJECT", with("sub", "someone-else"), false},
		{"opaque token", "", "access token is not a JWT", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			s, f := newFixture(t)
			if tc.opaque {
				f.access = "opaque-reference-token"
			} else {
				f.access = f.signTyped(tc.typ, tc.claims)
			}
			state := begin(t, s, f, "/")
			res := callback(t, s, f, state, "ok", "")
			if res.Header().Get("Location") != "/login?auth_error=login_failed" {
				t.Fatalf("mismatched access token still logged in: %s", res.Header().Get("Location"))
			}
			for _, c := range res.Result().Cookies() {
				if c.Name == s.cfg.CookieName && c.MaxAge > 0 {
					t.Fatal("session cookie issued for a token the backend rejects")
				}
			}
			if !strings.Contains(logs.String(), tc.reason) || strings.Contains(logs.String(), f.access) {
				t.Fatalf("log %q lacks reason %q or leaks the token", logs.String(), tc.reason)
			}
		})
	}
}

func TestPreflightReportsExternalIdPMismatches(t *testing.T) {
	s, _ := newFixture(t)
	var out bytes.Buffer
	if err := s.Preflight(context.Background(), &out); err != nil {
		t.Fatalf("preflight failed for a matching IdP: %v", err)
	}
	for _, want := range []string{"Redirect URI:    https://app.example/oauth2/callback", "Post-logout URI: https://app.example/"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("preflight output lacks %q:\n%s", want, out.String())
		}
	}
	tests := map[string]struct {
		mutate func(*Server, *fakeIDP)
		want   string
	}{
		"issuer mismatch":    {func(_ *Server, f *fakeIDP) { f.discovery["issuer"] = "https://issuer.example/realms/other" }, "discovery issuer="},
		"token URL mismatch": {func(_ *Server, f *fakeIDP) { f.discovery["token_endpoint"] = "https://issuer.example/oauth/token" }, "OIDC_TOKEN_URL="},
		"auth method missing": {func(s *Server, f *fakeIDP) {
			s.cfg.TokenAuthMethod = "client_secret_post"
			delete(f.discovery, "token_endpoint_auth_methods_supported")
		}, "OIDC_TOKEN_AUTH_METHOD=client_secret_post is not supported"},
		"redirect not registered": {func(_ *Server, f *fakeIDP) { f.authorizeStatus = http.StatusBadRequest }, "register it in the IdP client stock-ai-test"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s, f := newFixture(t)
			tc.mutate(s, f)
			err := s.Preflight(context.Background(), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("preflight error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestPreflightExplainsIncompleteCertificateChain(t *testing.T) {
	idp := httptest.NewTLSServer(http.NotFoundHandler())
	defer idp.Close()
	s, _ := newFixture(t)
	s.cfg.Issuer = "https://idp.example.com/realms/stock-ai-test" // covered by the httptest certificate
	s.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, idp.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}}
	err := s.Preflight(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "OIDC_CA_BUNDLE") {
		t.Fatalf("untrusted IdP chain not explained: %v", err)
	}
}
