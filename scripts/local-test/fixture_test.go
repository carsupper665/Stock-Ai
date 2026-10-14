package main

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
	"testing"
	"time"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (fn fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func adapterJWTValid(f *fixture, token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var header map[string]any
	if json.Unmarshal(headerBytes, &header) != nil || header["alg"] != "RS256" || header["typ"] != "at+jwt" || header["kid"] != f.identity().KeyID {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, hash[:], signature) != nil {
		return false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]any
	if json.Unmarshal(body, &claims) != nil {
		return false
	}
	expires, ok := claims["exp"].(float64)
	return ok && expires > float64(time.Now().Unix()) && expires <= float64(time.Now().Unix()+45) &&
		claims["iss"] == f.identity().Issuer && claims["aud"] == f.identity().Audience && claims["sub"] == f.identity().Subject
}

func TestPrivateBackendAdapterCredentialScopeAndHeaders(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{key: key, config: fixtureConfig{Issuer: "https://app.localrelease:8443/realms/stock-ai-release",
		Audience: "stock-ai-api-release", Subject: "local-release-user", KeyID: "local-release-signing-key",
		BackendAdapter: true, MarketFixture: true, BackendSecret: strings.Repeat("private-service-", 4)}}
	calls := 0
	f.backendClient = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "backend:7794" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Auth-Request-Subject") != "" || r.Header.Get("Forwarded") != "" {
			t.Fatal("adapter forwarded an unapproved target/header")
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		code := http.StatusUnauthorized
		if adapterJWTValid(f, token) {
			code = 200
		}
		if token == "at_account-only" && r.URL.Path == "/v1/orders" && r.Method == "POST" {
			code = 201
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Set-Cookie": {"must-not-forward"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	invoke := func(method, path, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		request.Header.Set("Cookie", "unapproved")
		request.Header.Set("X-Auth-Request-Subject", "unapproved")
		request.Header.Set("Forwarded", "host=unapproved")
		response := httptest.NewRecorder()
		f.adapterHandler().ServeHTTP(response, request)
		if response.Header().Get("Set-Cookie") != "" {
			t.Fatal("adapter exposed unapproved upstream header")
		}
		return response
	}
	if invoke("GET", "/v1/accounts/acc_fixture", f.config.BackendSecret).Code != 200 {
		t.Fatal("service account lookup did not mint valid scoped OIDC")
	}
	if invoke("GET", "/v1/market/price?market=crypto&symbol=BTCUSDT", f.config.BackendSecret).Code != 200 {
		t.Fatal("price sampling did not mint valid scoped OIDC")
	}
	if invoke("POST", "/v1/orders", "at_account-only").Code != 201 {
		t.Fatal("Account credential was not preserved")
	}
	for _, test := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/v1/accounts/acc_fixture", "wrong-private-key", 403},
		{"GET", "/v1/accounts/acc_fixture", "", 401},
		{"GET", "/v1/market/price?symbol=BTCUSDT", "wrong-private-key", 401},
		{"POST", "/v1/orders", f.config.BackendSecret, 403},
		{"GET", "/v1/account", f.config.BackendSecret, 403},
		{"GET", "/v1/accounts", f.config.BackendSecret, 403},
		{"POST", "/v1/accounts/acc_fixture/token/reset", f.config.BackendSecret, 403},
		{"DELETE", "/v1/orders/id", "at_account-only", 403},
		{"GET", "/v1/accounts/acc_fixture%2f..%2fescape", f.config.BackendSecret, 403},
		{"GET", "/v1/accounts/acc_fixture?redirect=http://escape", f.config.BackendSecret, 400},
		{"GET", "http://escape/v1/accounts/acc_fixture", f.config.BackendSecret, 403},
	} {
		if got := invoke(test.method, test.path, test.token).Code; got != test.status {
			t.Fatalf("route/method/credential gate: got %d want %d", got, test.status)
		}
	}
	for _, change := range []string{"expired", "issuer", "audience", "subject"} {
		claims := map[string]any{"iss": f.config.Issuer, "aud": f.config.Audience, "sub": f.config.Subject, "exp": time.Now().Unix() + 45}
		switch change {
		case "expired":
			claims["exp"] = time.Now().Unix() - 1
		case "issuer":
			claims["iss"] = "wrong"
		case "audience":
			claims["aud"] = "wrong"
		case "subject":
			claims["sub"] = "wrong"
		}
		token, err := f.signToken("at+jwt", claims)
		if err != nil {
			t.Fatal(err)
		}
		if invoke("GET", "/v1/accounts/acc_fixture", token).Code != 403 {
			t.Fatal("unapproved token gained USER account privilege")
		}
		if invoke("GET", "/v1/market/price?symbol=BTCUSDT", token).Code != 401 {
			t.Fatal("unapproved token bypassed Backend verification")
		}
	}
	if calls < 3 {
		t.Fatal("adapter never exercised fixed upstream")
	}
	negative := httptest.NewRecorder()
	f.backendRejections(negative, httptest.NewRequest("GET", "/__fixture/backend-rejections", nil))
	var rejected struct {
		Statuses        map[string]int `json:"statuses"`
		TokensDisclosed bool           `json:"tokens_disclosed"`
	}
	if err := json.Unmarshal(negative.Body.Bytes(), &rejected); err != nil || len(rejected.Statuses) != 6 || rejected.TokensDisclosed {
		t.Fatal("Backend rejection probe did not retain sanitized statuses")
	}
	for _, code := range rejected.Statuses {
		if code != 401 {
			t.Fatal("invalid Backend identity accepted")
		}
	}
	metrics := httptest.NewRecorder()
	f.adapterCounters(metrics, httptest.NewRequest("GET", "/__fixture/adapter", nil))
	if strings.Contains(metrics.Body.String(), f.config.BackendSecret) || strings.Contains(metrics.Body.String(), "at_account-only") || !strings.Contains(metrics.Body.String(), `"USER GET /v1/accounts/:id":1`) {
		t.Fatal("unsafe/missing route provenance")
	}
}

func testFixture(t *testing.T) (*fixture, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("generate fixture test key")
	}
	f := &fixture{key: key, codes: make(map[string]authorization)}
	return f, httptest.NewServer(f.issuerHandler())
}

func TestLocalReleaseMarketRoutesAndProvenance(t *testing.T) {
	f := &fixture{config: fixtureConfig{Issuer: "https://app.localrelease:8443", MarketFixture: true}}
	server := httptest.NewTLSServer(f.marketHandler())
	defer server.Close()
	for _, route := range []string{"/api/v3/ticker/price?symbol=BTCUSDT", "/api/v3/exchangeInfo?symbol=BTCUSDT",
		"/api/v3/klines?symbol=BTCUSDT&interval=1m&startTime=60000&endTime=179999&limit=2"} {
		response, err := server.Client().Get(server.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || response.Header.Get("X-Stock-AI-Market-Fixture") != "stock-ai-local-binance-v1" {
			t.Fatal("local market response lacks provenance")
		}
		var body any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		switch {
		case strings.Contains(route, "ticker/price"):
			if body.(map[string]any)["price"] != "100.00000000" {
				t.Fatal("non-deterministic ticker")
			}
		case strings.Contains(route, "klines"):
			rows := body.([]any)
			if len(rows) != 2 || len(rows[0].([]any)) != 12 || rows[0].([]any)[0] != float64(60000) {
				t.Fatal("invalid Binance candle layout")
			}
		}
	}
	metrics := httptest.NewRecorder()
	f.marketCounters(metrics, httptest.NewRequest("GET", "/__fixture/market", nil))
	var counters struct {
		Routes        map[string]int `json:"routes"`
		ExternalCalls int            `json:"external_calls"`
	}
	if err := json.Unmarshal(metrics.Body.Bytes(), &counters); err != nil {
		t.Fatal(err)
	}
	if counters.ExternalCalls != 0 || len(counters.Routes) != 3 {
		t.Fatal("missing local route counters")
	}
	for _, count := range counters.Routes {
		if count != 1 {
			t.Fatal("incorrect local request count")
		}
	}
	wrong := httptest.NewRecorder()
	f.marketHandler().ServeHTTP(wrong, httptest.NewRequest("GET", "/api/v3/ticker/price?symbol=UNKNOWN", nil))
	if wrong.Code != 400 {
		t.Fatal("unknown symbol accepted")
	}
	// Test's existing auxiliary identity must not expose the new market surface.
	legacy := &fixture{}
	denied := httptest.NewRecorder()
	legacy.marketHandler().ServeHTTP(denied, httptest.NewRequest("GET", "/api/v3/ticker/price?symbol=BTCUSDT", nil))
	if denied.Code != 404 {
		t.Fatal("market fixture unexpectedly enabled for Test")
	}
}

func TestReleaseProviderMarketOrderCompletionSequence(t *testing.T) {
	f := &fixture{config: fixtureConfig{Issuer: "https://app.localrelease:8443", MarketFixture: true, ProviderSecret: "private-fixture-only"}}
	for toolResults, expected := range map[int]string{0: "get_market_snapshot", 1: "place_order", 2: ""} {
		messages := []any{map[string]any{"role": "user", "content": "fixture"}}
		for index := 0; index < toolResults; index++ {
			messages = append(messages, map[string]any{"role": "tool", "content": "fixture tool result"})
		}
		body, _ := json.Marshal(map[string]any{"messages": messages, "tools": []any{map[string]string{"name": "place_order"}}})
		request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer private-fixture-only")
		response := httptest.NewRecorder()
		f.complete(response, request)
		if response.Code != 200 {
			t.Fatal("Release generation failed")
		}
		var result struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		message := result.Choices[0].Message
		if expected == "" {
			if message.Content == "" || len(message.ToolCalls) != 0 {
				t.Fatal("missing run completion")
			}
		} else {
			if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != expected {
				t.Fatal("incorrect tool sequence")
			}
			if expected == "place_order" && (!strings.Contains(message.ToolCalls[0].Function.Arguments, `"stop_loss":90`) || !strings.Contains(message.ToolCalls[0].Function.Arguments, `"take_profit":110`)) {
				t.Fatal("order lacks stops")
			}
		}
	}
}

func TestReleaseIdentityIsolation(t *testing.T) {
	f, oldServer := testFixture(t)
	oldServer.Close()
	f.config = fixtureConfig{Issuer: "https://app.localrelease:8443/realms/stock-ai-release",
		ClientID: "stock-ai-release", ClientSecret: strings.Repeat("release-secret", 4),
		Audience: "stock-ai-api-release", Subject: "local-release-user", KeyID: "local-release-signing-key",
		RedirectURI: "https://app.localrelease:8443/oauth2/callback", LogoutURI: "https://app.localrelease:8443/"}
	server := httptest.NewServer(f.issuerHandler())
	defer server.Close()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for _, redirect := range []string{f.config.RedirectURI, "https://app.localtest/oauth2/callback"} {
		query := url.Values{"client_id": {f.config.ClientID}, "response_type": {"code"}, "code_challenge_method": {"S256"},
			"redirect_uri": {redirect}, "state": {"release-state"}, "nonce": {"release-nonce"}, "code_challenge": {pkce("release-verifier")}}
		response, err := client.Get(server.URL + "/realms/stock-ai-release/authorize?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if redirect != f.config.RedirectURI {
			if response.StatusCode != http.StatusBadRequest {
				t.Fatal("accepted Test redirect")
			}
			continue
		}
		if response.StatusCode != http.StatusFound {
			t.Fatal("Release authorization failed")
		}
		location, _ := url.Parse(response.Header.Get("Location"))
		code := location.Query().Get("code")
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {"release-verifier"}}
		for _, secret := range []string{clientKey, f.config.ClientSecret} {
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/realms/stock-ai-release/token", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.SetBasicAuth(f.config.ClientID, secret)
			tokenResponse, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			if secret == clientKey {
				tokenResponse.Body.Close()
				if tokenResponse.StatusCode != http.StatusUnauthorized {
					t.Fatal("accepted Test secret")
				}
				continue
			}
			if tokenResponse.StatusCode != http.StatusOK {
				t.Fatal("Release token exchange failed")
			}
			var raw map[string]json.RawMessage
			if err := json.NewDecoder(tokenResponse.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			tokenResponse.Body.Close()
			var accessToken string
			_ = json.Unmarshal(raw["access_token"], &accessToken)
			parts := strings.Split(accessToken, ".")
			if len(parts) != 3 {
				t.Fatal("missing signed token")
			}
			body, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			_ = json.Unmarshal(body, &claims)
			if claims["iss"] != f.config.Issuer || claims["aud"] != f.config.Audience || claims["sub"] != f.config.Subject {
				t.Fatal("Release token reused Test claims")
			}
		}
	}
	response, err := client.Get(server.URL + "/realms/stock-ai-release/logout?post_logout_redirect_uri=" + url.QueryEscape("https://app.localtest/"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatal("accepted Test logout redirect")
	}
}

func TestDiscoveryRoute(t *testing.T) {
	_, server := testFixture(t)
	defer server.Close()

	response, err := http.Get(server.URL + "/realms/stock-ai-test/.well-known/openid-configuration")
	if err != nil {
		t.Fatal("request discovery")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("discovery status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var document map[string]any
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatal("decode discovery response")
	}
	if document["issuer"] != issuer || document["jwks_uri"] != issuer+"/jwks" {
		t.Fatalf("discovery omitted stable issuer/JWKS endpoint")
	}
}

func TestAuthorizationCodeTokenRoute(t *testing.T) {
	f, server := testFixture(t)
	defer server.Close()

	verifier := "local-test-pkce-verifier"
	redirectURI := "https://app.localtest/oauth2/callback"
	query := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirectURI},
		"state":                 {"test-state"},
		"nonce":                 {"test-nonce"},
		"code_challenge":        {pkce(verifier)},
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/realms/stock-ai-test/authorize?"+query.Encode(), nil)
	if err != nil {
		t.Fatal("create authorization request")
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("request authorization route")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("authorization status = %d, want %d", response.StatusCode, http.StatusFound)
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal("parse callback redirect")
	}
	code := location.Query().Get("code")
	if code == "" || location.Query().Get("state") != "test-state" {
		t.Fatal("authorization route did not return a code and matching state")
	}

	form := url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}}
	tokenRequest, err := http.NewRequest(http.MethodPost, server.URL+"/realms/stock-ai-test/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal("create token request")
	}
	tokenRequest.SetBasicAuth(clientID, clientKey)
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse, err := client.Do(tokenRequest)
	if err != nil {
		t.Fatal("request token route")
	}
	defer tokenResponse.Body.Close()
	if tokenResponse.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d, want %d", tokenResponse.StatusCode, http.StatusOK)
	}
	var token map[string]any
	if err := json.NewDecoder(tokenResponse.Body).Decode(&token); err != nil {
		t.Fatal("decode token response")
	}
	if token["access_token"] == "" || token["id_token"] == "" {
		t.Fatal("token route omitted signed access or identity token")
	}
	if _, remains := f.codes[code]; remains {
		t.Fatal("token route did not consume the single-use authorization code")
	}
}

func TestFixtureControlRoutes(t *testing.T) {
	f, server := testFixture(t)
	defer server.Close()

	response, err := http.Post(server.URL+"/__fixture/next", "application/json", strings.NewReader(`{"scenario":"wrong-subject"}`))
	if err != nil {
		t.Fatal("request next-case control")
	}
	var next map[string]string
	if err := json.NewDecoder(response.Body).Decode(&next); err != nil {
		t.Fatal("decode next-case response")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || next["accepted"] != "wrong-subject" || f.scenarioNext != "wrong-subject" {
		t.Fatal("next-case route did not queue the requested scenario")
	}

	for _, enabled := range []bool{true, false} {
		body := `{"enabled":true}`
		if !enabled {
			body = `{"enabled":false}`
		}
		control, err := http.Post(server.URL+"/__fixture/outage", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal("request outage control")
		}
		var result map[string]bool
		if err := json.NewDecoder(control.Body).Decode(&result); err != nil {
			t.Fatal("decode outage response")
		}
		control.Body.Close()
		if control.StatusCode != http.StatusOK || result["outage"] != enabled {
			t.Fatalf("outage control status=%d result=%v want=%v", control.StatusCode, result["outage"], enabled)
		}
		discovery, err := http.Get(server.URL + "/realms/stock-ai-test/.well-known/openid-configuration")
		if err != nil {
			t.Fatal("request discovery during outage control")
		}
		discovery.Body.Close()
		want := http.StatusOK
		if enabled {
			want = http.StatusServiceUnavailable
		}
		if discovery.StatusCode != want {
			t.Fatalf("discovery status while outage=%v: got %d want %d", enabled, discovery.StatusCode, want)
		}
	}
}
