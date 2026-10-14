package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testFixture(t *testing.T) (*fixture, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("generate fixture test key")
	}
	f := &fixture{key: key, codes: make(map[string]authorization)}
	return f, httptest.NewServer(f.issuerHandler())
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
