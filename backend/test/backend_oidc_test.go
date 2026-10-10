package test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/database"
)

const (
	oidcIssuer   = "https://idp.example/realms/test"
	oidcAudience = "stock-ai-api-test"
	oidcSubject  = "stable-user-subject"
	oidcKeyID    = "signing-key"
)

type jwksTransport struct {
	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	unavailable bool
	requests    int
}

type blockingTransport struct{}

func (blockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

func (t *jwksTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests++
	if t.unavailable {
		return nil, errors.New("private-jwks-host: unavailable")
	}
	keys := make([]map[string]string, 0, len(t.keys))
	for id, key := range t.keys {
		keys = append(keys, publicJWK(id, key))
	}
	body, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    request,
	}, nil
}

func (t *jwksTransport) setKeys(keys map[string]*rsa.PublicKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.keys = keys
}

func (t *jwksTransport) requestCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requests
}

func oidcTestAuthenticator(t *testing.T, transport *jwksTransport, clock *testClock) (*auth.Authenticator, *database.Store) {
	t.Helper()
	store, err := database.OpenSQLite(filepath.Join(t.TempDir(), "oidc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	settings := auth.OIDCSettings{
		Issuer: oidcIssuer, Audience: oidcAudience, Subject: oidcSubject,
		JWKSURL: "https://private-jwks-host.example/keys", TokenType: "at+jwt",
		Timeout: 100 * time.Millisecond, CacheTTL: 5 * time.Minute, RefreshCooldown: time.Minute,
		Now: clock.Now, Client: &http.Client{Transport: transport},
	}
	return auth.New(auth.Settings{UserName: "OIDC USER", OIDC: &settings}, store), store
}

func TestBackendOIDCAcceptanceMatrix(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	clock := &testClock{now: now}
	key := generateRSAKey(t)
	otherKey := generateRSAKey(t)
	transport := &jwksTransport{keys: map[string]*rsa.PublicKey{oidcKeyID: &key.PublicKey}}
	authenticator, _ := oidcTestAuthenticator(t, transport, clock)
	validClaims := map[string]any{
		"iss": oidcIssuer, "aud": []string{"another-api", oidcAudience}, "sub": oidcSubject,
		"exp": now.Add(time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix(),
	}

	valid := signJWT(t, key, "RS256", "at+jwt", oidcKeyID, validClaims)
	identity, err := authenticator.Resolve(context.Background(), valid)
	if err != nil || !identity.IsUser() || identity.Subject != oidcSubject || identity.UserName != "OIDC USER" {
		t.Fatalf("valid access token did not resolve expected USER: identity=%+v err=%v", identity, err)
	}
	if _, err := authenticator.Resolve(context.Background(), valid); err != nil {
		t.Fatalf("cached key rejected valid token: %v", err)
	}
	if requests := transport.requestCount(); requests != 1 {
		t.Fatalf("JWKS should be cached; requests=%d", requests)
	}

	cases := []struct {
		name      string
		key       *rsa.PrivateKey
		algorithm string
		tokenType string
		mutate    func(map[string]any)
	}{
		{name: "wrong issuer", key: key, algorithm: "RS256", tokenType: "at+jwt", mutate: func(c map[string]any) { c["iss"] = "https://wrong.example" }},
		{name: "wrong audience", key: key, algorithm: "RS256", tokenType: "at+jwt", mutate: func(c map[string]any) { c["aud"] = "wrong-api" }},
		{name: "wrong subject", key: key, algorithm: "RS256", tokenType: "at+jwt", mutate: func(c map[string]any) { c["sub"] = "another-user" }},
		{name: "wrong algorithm", key: key, algorithm: "RS512", tokenType: "at+jwt"},
		{name: "wrong signature", key: otherKey, algorithm: "RS256", tokenType: "at+jwt"},
		{name: "wrong token type", key: key, algorithm: "RS256", tokenType: "JWT"},
		{name: "expired", key: key, algorithm: "RS256", tokenType: "at+jwt", mutate: func(c map[string]any) { c["exp"] = now.Unix() }},
		{name: "not yet valid", key: key, algorithm: "RS256", tokenType: "at+jwt", mutate: func(c map[string]any) { c["nbf"] = now.Add(time.Second).Unix() }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			claims := cloneClaims(t, validClaims)
			if test.mutate != nil {
				test.mutate(claims)
			}
			token := signJWT(t, test.key, test.algorithm, test.tokenType, oidcKeyID, claims)
			if _, err := authenticator.Resolve(context.Background(), token); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("token should be rejected with generic error: %v", err)
			}
		})
	}
}

func TestBackendOIDCJWKSUnavailableRotationAndBound(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	clock := &testClock{now: now}
	first := generateRSAKey(t)
	second := generateRSAKey(t)
	transport := &jwksTransport{keys: map[string]*rsa.PublicKey{oidcKeyID: &first.PublicKey}}
	authenticator, _ := oidcTestAuthenticator(t, transport, clock)
	claims := map[string]any{"iss": oidcIssuer, "aud": oidcAudience, "sub": oidcSubject, "exp": now.Add(10 * time.Minute).Unix()}

	if _, err := authenticator.Resolve(context.Background(), signJWT(t, first, "RS256", "at+jwt", oidcKeyID, claims)); err != nil {
		t.Fatalf("initial key failed: %v", err)
	}
	clock.Advance(time.Minute)
	transport.setKeys(map[string]*rsa.PublicKey{oidcKeyID: &second.PublicKey})
	if _, err := authenticator.Resolve(context.Background(), signJWT(t, second, "RS256", "at+jwt", oidcKeyID, claims)); err != nil {
		t.Fatalf("same-kid rotation was not refreshed safely: %v", err)
	}
	if requests := transport.requestCount(); requests != 2 {
		t.Fatalf("rotation should trigger one refresh; requests=%d", requests)
	}

	unavailable := &jwksTransport{unavailable: true}
	unavailableClock := &testClock{now: now}
	unavailableAuth, _ := oidcTestAuthenticator(t, unavailable, unavailableClock)
	unavailableToken := signJWT(t, first, "RS256", "at+jwt", oidcKeyID, claims)
	if _, err := unavailableAuth.Resolve(context.Background(), unavailableToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("unavailable JWKS must fail closed: %v", err)
	}
	if _, err := unavailableAuth.Resolve(context.Background(), unavailableToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("unavailable JWKS must remain fail closed during cooldown: %v", err)
	}
	if requests := unavailable.requestCount(); requests != 1 {
		t.Fatalf("unavailable JWKS was retried inside cooldown; requests=%d", requests)
	}
	unavailableClock.Advance(time.Minute)
	if _, err := unavailableAuth.Resolve(context.Background(), unavailableToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("unavailable JWKS must fail closed after retry: %v", err)
	}
	if requests := unavailable.requestCount(); requests != 2 {
		t.Fatalf("unavailable JWKS should retry once after cooldown; requests=%d", requests)
	}

	timeoutSettings := auth.OIDCSettings{
		Issuer: oidcIssuer, Audience: oidcAudience, Subject: oidcSubject,
		JWKSURL: "https://private-jwks-host.example/keys", TokenType: "at+jwt",
		Timeout: 25 * time.Millisecond, Now: func() time.Time { return now },
		Client: &http.Client{Transport: blockingTransport{}},
	}
	timeoutAuth := auth.New(auth.Settings{UserName: "USER", OIDC: &timeoutSettings}, mustOIDCStore(t))
	started := time.Now()
	if _, err := timeoutAuth.Resolve(context.Background(), signJWT(t, first, "RS256", "at+jwt", oidcKeyID, claims)); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("timed-out JWKS must fail closed: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("JWKS timeout was not bounded: %v", elapsed)
	}

	tooMany := make(map[string]*rsa.PublicKey)
	for i := 0; i < 33; i++ {
		tooMany[string(rune('a'+i))] = &first.PublicKey
	}
	boundedAuth, _ := oidcTestAuthenticator(t, &jwksTransport{keys: tooMany}, &testClock{now: now})
	if _, err := boundedAuth.Resolve(context.Background(), signJWT(t, first, "RS256", "at+jwt", "a", claims)); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("oversized JWKS must not enter cache: %v", err)
	}
}

func TestBackendOIDCRefreshAmplificationIsBounded(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	clock := &testClock{now: now}
	signingKey := generateRSAKey(t)
	attackerKey := generateRSAKey(t)
	transport := &jwksTransport{keys: map[string]*rsa.PublicKey{oidcKeyID: &signingKey.PublicKey}}
	authenticator, _ := oidcTestAuthenticator(t, transport, clock)
	claims := map[string]any{"iss": oidcIssuer, "aud": oidcAudience, "sub": oidcSubject, "exp": now.Add(10 * time.Minute).Unix()}
	valid := signJWT(t, signingKey, "RS256", "at+jwt", oidcKeyID, claims)
	if _, err := authenticator.Resolve(context.Background(), valid); err != nil {
		t.Fatalf("initial token failed: %v", err)
	}
	if requests := transport.requestCount(); requests != 1 {
		t.Fatalf("initial verification should fetch once; requests=%d", requests)
	}

	clock.Advance(time.Minute)
	unknownKey := signJWT(t, signingKey, "RS256", "at+jwt", "random-attacker-kid", claims)
	resolveConcurrently(t, authenticator, unknownKey, 24)
	if requests := transport.requestCount(); requests != 2 {
		t.Fatalf("parallel unknown kid misses must share one refresh; requests=%d", requests)
	}
	for i := 0; i < 10; i++ {
		if _, err := authenticator.Resolve(context.Background(), unknownKey); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("unknown kid should fail closed: %v", err)
		}
	}
	if requests := transport.requestCount(); requests != 2 {
		t.Fatalf("repeated unknown kid bypassed cooldown; requests=%d", requests)
	}
	clock.Advance(time.Minute)
	if _, err := authenticator.Resolve(context.Background(), unknownKey); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("unknown kid should still fail closed after cooldown: %v", err)
	}
	if requests := transport.requestCount(); requests != 3 {
		t.Fatalf("unknown kid should allow one refresh after cooldown; requests=%d", requests)
	}

	clock.Advance(time.Minute)
	invalidSignature := signJWT(t, attackerKey, "RS256", "at+jwt", oidcKeyID, claims)
	resolveConcurrently(t, authenticator, invalidSignature, 24)
	if requests := transport.requestCount(); requests != 4 {
		t.Fatalf("parallel invalid signatures must share one refresh; requests=%d", requests)
	}
	for i := 0; i < 10; i++ {
		if _, err := authenticator.Resolve(context.Background(), invalidSignature); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("invalid signature should fail closed: %v", err)
		}
	}
	if requests := transport.requestCount(); requests != 4 {
		t.Fatalf("repeated invalid signatures bypassed cooldown; requests=%d", requests)
	}
	clock.Advance(time.Minute)
	if _, err := authenticator.Resolve(context.Background(), invalidSignature); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("invalid signature should fail closed after cooldown: %v", err)
	}
	if requests := transport.requestCount(); requests != 5 {
		t.Fatalf("invalid signature should allow one refresh after cooldown; requests=%d", requests)
	}
}

func TestBackendOIDCParallelRotationWaitersReuseRefresh(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	clock := &testClock{now: now}
	first := generateRSAKey(t)
	second := generateRSAKey(t)
	transport := &jwksTransport{keys: map[string]*rsa.PublicKey{"first-kid": &first.PublicKey}}
	authenticator, _ := oidcTestAuthenticator(t, transport, clock)
	claims := map[string]any{"iss": oidcIssuer, "aud": oidcAudience, "sub": oidcSubject, "exp": now.Add(10 * time.Minute).Unix()}
	if _, err := authenticator.Resolve(context.Background(), signJWT(t, first, "RS256", "at+jwt", "first-kid", claims)); err != nil {
		t.Fatalf("initial token failed: %v", err)
	}

	clock.Advance(time.Minute)
	transport.setKeys(map[string]*rsa.PublicKey{"second-kid": &second.PublicKey})
	rotated := signJWT(t, second, "RS256", "at+jwt", "second-kid", claims)
	resolveValidConcurrently(t, authenticator, rotated, 24)
	if requests := transport.requestCount(); requests != 2 {
		t.Fatalf("parallel rotation waiters did not reuse one refresh; requests=%d", requests)
	}
}

func resolveConcurrently(t *testing.T, authenticator *auth.Authenticator, token string, count int) {
	t.Helper()
	start := make(chan struct{})
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := authenticator.Resolve(context.Background(), token)
			errorsFound <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("concurrent invalid token did not fail closed: %v", err)
		}
	}
}

func resolveValidConcurrently(t *testing.T, authenticator *auth.Authenticator, token string, count int) {
	t.Helper()
	start := make(chan struct{})
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := authenticator.Resolve(context.Background(), token)
			errorsFound <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent rotated token failed: %v", err)
		}
	}
}

func mustOIDCStore(t *testing.T) *database.Store {
	t.Helper()
	store, err := database.OpenSQLite(filepath.Join(t.TempDir(), "timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestBackendOIDCAccountSeparationStaticPolicyAndDisclosure(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	clock := &testClock{now: now}
	key := generateRSAKey(t)
	transport := &jwksTransport{keys: map[string]*rsa.PublicKey{oidcKeyID: &key.PublicKey}}
	authenticator, store := oidcTestAuthenticator(t, transport, clock)
	accountToken := "at_account_opaque"
	if err := store.CreateAccount(context.Background(), &database.Account{
		ID: "acc_1", UserName: "Account", Token: accountToken,
		InitialBalance: 100, Balance: 100, Status: database.AccountActive,
	}); err != nil {
		t.Fatal(err)
	}
	identity, err := authenticator.Resolve(context.Background(), accountToken)
	if err != nil || identity.Kind != auth.KindAccount || identity.IsUser() || identity.AccountID != "acc_1" {
		t.Fatalf("Account token semantics changed: identity=%+v err=%v", identity, err)
	}

	invalidJWT := "private-token.claims.signature"
	if err := store.CreateAccount(context.Background(), &database.Account{
		ID: "acc_2", UserName: "Must Not Fall Back", Token: invalidJWT,
		InitialBalance: 100, Balance: 100, Status: database.AccountActive,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = authenticator.Resolve(context.Background(), invalidJWT)
	if !errors.Is(err, auth.ErrInvalidToken) || err.Error() != auth.ErrInvalidToken.Error() {
		t.Fatalf("invalid OIDC token did not fail generically: %v", err)
	}
	for _, forbidden := range []string{invalidJWT, "claims", "private-jwks-host"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("authentication error disclosed sensitive detail %q: %v", forbidden, err)
		}
	}

	disabled := auth.New(auth.Settings{UserName: "USER", StaticUserToken: "static-secret"}, store)
	if _, err := disabled.Resolve(context.Background(), "static-secret"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("static USER token worked without explicit opt-in: %v", err)
	}
	enabled := auth.New(auth.Settings{UserName: "USER", StaticUserToken: "static-secret", AllowStaticUserToken: true}, store)
	if identity, err := enabled.Resolve(context.Background(), "static-secret"); err != nil || !identity.IsUser() {
		t.Fatalf("explicit development static token did not work: identity=%+v err=%v", identity, err)
	}
}

func generateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func publicJWK(keyID string, key *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": keyID,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}
}

func signJWT(t *testing.T, key *rsa.PrivateKey, algorithm, tokenType, keyID string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": algorithm, "typ": tokenType, "kid": keyID}
	encodedHeader := encodeJSON(t, header)
	encodedClaims := encodeJSON(t, claims)
	signed := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func cloneClaims(t *testing.T, claims map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}
