package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

const (
	issuer      = "https://idp.localtest:9443/realms/stock-ai-test"
	clientID    = "stock-ai-test"
	clientKey   = "local-test-oidc-client-secret-only"
	providerKey = "local-test-provider-token-only"
	keyID       = "local-test-signing-key"
)

type authorization struct {
	redirectURI string
	challenge   string
	nonce       string
	scenario    string
}

type fixture struct {
	mu            sync.Mutex
	key           *rsa.PrivateKey
	codes         map[string]authorization
	scenarioNext  string
	outage        bool
	providerCalls int
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		for _, address := range []string{"127.0.0.1:9443", "127.0.0.1:18080"} {
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				os.Exit(1)
			}
			_ = conn.Close()
		}
		return
	}
	keyPEM, err := os.ReadFile("/fixture/jwks-signing-key.pem")
	if err != nil {
		log.Fatal("fixture signing key read failed")
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		log.Fatal("fixture signing key parse failed")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		log.Fatal("fixture signing key parse failed")
	}
	key, ok := parsedKey.(*rsa.PrivateKey)
	if !ok {
		log.Fatal("fixture signing key has invalid type")
	}
	f := &fixture{key: key, codes: make(map[string]authorization)}
	issuerMux := http.NewServeMux()
	issuerMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	issuerMux.HandleFunc("/realms/stock-ai-test/.well-known/openid-configuration", f.discovery)
	issuerMux.HandleFunc("/realms/stock-ai-test/authorize", f.authorize)
	issuerMux.HandleFunc("/realms/stock-ai-test/token", f.token)
	issuerMux.HandleFunc("/realms/stock-ai-test/jwks", f.jwks)
	issuerMux.HandleFunc("/realms/stock-ai-test/logout", f.logout)
	issuerMux.HandleFunc("/__fixture/next", f.nextCase)
	issuerMux.HandleFunc("/__fixture/outage", f.setOutage)
	providerMux := http.NewServeMux()
	providerMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	providerMux.HandleFunc("/v1/chat/completions", f.complete)

	go func() {
		log.Print("local fake-provider fixture listening on :18080")
		if err := http.ListenAndServe(":18080", providerMux); err != nil {
			log.Fatal("fake-provider fixture stopped")
		}
	}()
	log.Print("local deterministic OIDC/JWKS fixture listening on :9443")
	if err := http.ListenAndServeTLS(":9443", "/fixture/fullchain.pem", "/fixture/privkey.pem", issuerMux); err != nil {
		log.Fatalf("OIDC fixture stopped: %v", err)
	}
}

func (f *fixture) down(w http.ResponseWriter) bool {
	f.mu.Lock()
	down := f.outage
	f.mu.Unlock()
	if down {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_unavailable"})
	}
	return down
}

func (f *fixture) discovery(w http.ResponseWriter, _ *http.Request) {
	if f.down(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"jwks_uri":                              issuer + "/jwks",
		"end_session_endpoint":                  issuer + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fixture) authorize(w http.ResponseWriter, r *http.Request) {
	if f.down(w) {
		return
	}
	q := r.URL.Query()
	if q.Get("client_id") != clientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("redirect_uri") == "" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_authorization_request"})
		return
	}
	f.mu.Lock()
	scenario := f.scenarioNext
	f.scenarioNext = ""
	f.mu.Unlock()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Scheme != "https" || redirect.Host != "app.localtest" || redirect.Path != "/oauth2/callback" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_redirect_uri"})
		return
	}
	if scenario == "denial" {
		redirectQuery := redirect.Query()
		redirectQuery.Set("error", "access_denied")
		redirectQuery.Set("state", q.Get("state"))
		redirect.RawQuery = redirectQuery.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
		return
	}
	code, err := randomString(32)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_error"})
		return
	}
	f.mu.Lock()
	f.codes[code] = authorization{redirectURI: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), scenario: scenario}
	f.mu.Unlock()
	redirectQuery := redirect.Query()
	redirectQuery.Set("code", code)
	redirectQuery.Set("state", q.Get("state"))
	redirect.RawQuery = redirectQuery.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (f *fixture) token(w http.ResponseWriter, r *http.Request) {
	if f.down(w) {
		return
	}
	client, secret, ok := r.BasicAuth()
	if !ok || client != clientID || secret != clientKey {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	code := r.Form.Get("code")
	f.mu.Lock()
	a, exists := f.codes[code]
	delete(f.codes, code)
	f.mu.Unlock()
	if !exists || r.Form.Get("redirect_uri") != a.redirectURI || r.Form.Get("code_verifier") == "" || pkce(r.Form.Get("code_verifier")) != a.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	subject := "local-test-user"
	tokenIssuer, audience := issuer, clientID
	apiAudience := "stock-ai-api-test"
	expiresIn := int64(3600)
	exp := time.Now().Add(time.Hour).Unix()
	accessExp := exp
	switch a.scenario {
	case "wrong-subject":
		subject = "unexpected-local-user"
	case "expired-token":
		exp = time.Now().Add(-time.Minute).Unix()
	case "short-token":
		expiresIn = 2
		accessExp = time.Now().Add(2 * time.Second).Unix()
	case "expired-access-token":
		accessExp = time.Now().Add(-time.Minute).Unix()
	case "issuer-mismatch":
		tokenIssuer = "https://wrong.localtest/realms/stock-ai-test"
	case "audience-mismatch":
		audience, apiAudience = "wrong-client", "wrong-api"
	}
	idClaims := map[string]any{
		"iss": tokenIssuer, "aud": audience, "sub": subject, "nonce": a.nonce,
		"preferred_username": "local-test-user", "email": "local-test@example.invalid", "exp": exp,
	}
	accessClaims := map[string]any{"iss": tokenIssuer, "aud": apiAudience, "sub": subject, "exp": accessExp, "nbf": time.Now().Add(-time.Minute).Unix()}
	idToken, err := f.signToken("JWT", idClaims)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_error"})
		return
	}
	accessToken, err := f.signToken("at+jwt", accessClaims)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": accessToken, "id_token": idToken, "token_type": "Bearer", "expires_in": expiresIn})
}

func (f *fixture) jwks(w http.ResponseWriter, _ *http.Request) {
	if f.down(w) {
		return
	}
	n := base64.RawURLEncoding.EncodeToString(f.key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.PublicKey.E)).Bytes())
	writeJSON(w, http.StatusOK, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": keyID, "n": n, "e": e}}})
}

func (f *fixture) logout(w http.ResponseWriter, r *http.Request) {
	if f.down(w) {
		return
	}
	postLogout := r.URL.Query().Get("post_logout_redirect_uri")
	if postLogout != "https://app.localtest/" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_logout_uri"})
		return
	}
	http.Redirect(w, r, postLogout, http.StatusFound)
}

func (f *fixture) nextCase(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Scenario string `json:"scenario"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_control"})
		return
	}
	allowed := map[string]bool{"success": true, "wrong-subject": true, "expired-token": true, "expired-access-token": true, "short-token": true, "issuer-mismatch": true, "audience-mismatch": true, "denial": true}
	if !allowed[input.Scenario] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_scenario"})
		return
	}
	f.mu.Lock()
	f.scenarioNext = input.Scenario
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"accepted": input.Scenario})
}

func (f *fixture) setOutage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_control"})
		return
	}
	f.mu.Lock()
	f.outage = input.Enabled
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"outage": input.Enabled})
}

func (f *fixture) complete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+providerKey {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_api_key"})
		return
	}
	var request struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	f.mu.Lock()
	f.providerCalls++
	f.mu.Unlock()
	for _, message := range request.Messages {
		if message.Role == "tool" {
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{
				"role": "assistant", "content": `{"output":"Local fixture placed the requested limit order.","summary":"Limit order completed by local Test fixture.","candidate_memories":[],"expire_memories":[]}`,
			}, "finish_reason": "stop"}}})
			return
		}
	}
	args := `{"market":"crypto","symbol":"BTCUSDT","product":"spot","side":"buy","type":"limit","quantity":0.001,"price":100}`
	writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{
		"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call-local-limit-order", "type": "function", "function": map[string]string{"name": "place_order", "arguments": args}}},
	}, "finish_reason": "tool_calls"}}})
}

func (f *fixture) signToken(typ string, claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": keyID, "typ": typ})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func pkce(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func randomString(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
