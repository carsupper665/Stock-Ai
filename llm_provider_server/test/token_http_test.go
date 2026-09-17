package test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func TestAdminCanCreateTokenAndGenerateWithCredential(t *testing.T) {
	receivedAuthorization := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthorization <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"authenticated answer"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	createProvider(t, server.Handler(), provider.URL, "fixture-model", true)

	created := request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", adminToken,
		[]byte(`{"name":"main","token":"sk-super-secret-9Ab2"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create token status = %d, body = %s", created.Code, created.Body.String())
	}
	assertJSON(t, created.Body.Bytes(), map[string]any{
		"provider_id": "fixture", "name": "main", "masked": "sk-****9Ab2", "enabled": true,
	})
	var tokenResponse map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &tokenResponse); err != nil {
		t.Fatal(err)
	}
	_, hasToken := tokenResponse["token"]
	_, hasCiphertext := tokenResponse["secret_encrypted"]
	if tokenResponse["id"] == "" || hasToken || hasCiphertext {
		t.Fatalf("unsafe or incomplete token response: %s", created.Body.String())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	generated := request(t, restarted.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("generate status = %d, body = %s", generated.Code, generated.Body.String())
	}
	if got := <-receivedAuthorization; got != "Bearer sk-super-secret-9Ab2" {
		t.Fatalf("provider Authorization = %q", got)
	}
}

func TestGatewayRejectsWrongMasterKeyForStoredTokens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	createProvider(t, server.Handler(), "http://127.0.0.1:1", "fixture-model", true)
	secret := "sk-do-not-disclose-9Ab2"
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", adminToken,
		[]byte(`{"name":"main","token":"`+secret+`"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create token status = %d, body = %s", created.Code, created.Body.String())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted string
	if err := db.QueryRow(`SELECT secret_encrypted FROM provider_tokens`).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if encrypted == secret || strings.Contains(encrypted, secret) {
		t.Fatalf("database contains plaintext token: %q", encrypted)
	}

	wrongKey := "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
	wrongServer, err := gateway.Open(dbPath, adminToken, runtimeToken, wrongKey, time.Second)
	if wrongServer != nil {
		wrongServer.Close()
	}
	if err == nil {
		t.Fatal("Open accepted a wrong master key for stored tokens")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), encrypted) {
		t.Fatalf("startup error disclosed credential material: %v", err)
	}
}

func TestAdminListsOnlyMaskedTokenMetadata(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), "http://127.0.0.1:1", "fixture-model", true)

	for _, body := range []string{
		`{"name":"short","token":"abc"}`,
		`{"name":"long","token":"sk-super-secret-9Ab2"}`,
	} {
		created := request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", adminToken, []byte(body))
		if created.Code != http.StatusCreated {
			t.Fatalf("create token status = %d, body = %s", created.Code, created.Body.String())
		}
	}

	listed := request(t, server.Handler(), http.MethodGet, "/v1/providers/fixture/tokens", adminToken, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list tokens status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var tokens []map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("token count = %d, want 2", len(tokens))
	}
	masks := map[string]string{}
	for _, token := range tokens {
		_, hasToken := token["token"]
		_, hasCiphertext := token["secret_encrypted"]
		if hasToken || hasCiphertext {
			t.Fatalf("unsafe token list response: %s", listed.Body.String())
		}
		masks[token["name"].(string)] = token["masked"].(string)
	}
	if masks["short"] != "****" || masks["long"] != "sk-****9Ab2" {
		t.Fatalf("masks = %v", masks)
	}
}

func TestTokenManagementRejectsInvalidOwnershipInputAndWrongRole(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), "http://127.0.0.1:1", "fixture-model", true)

	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/providers/missing/tokens", adminToken,
		[]byte(`{"name":"main","token":"secret"}`)), http.StatusNotFound, "PROVIDER_NOT_FOUND")
	for _, invalid := range []string{"", "   ", " secret", "secret ", "sec ret", "line\nbreak"} {
		body, _ := json.Marshal(map[string]string{"name": "main", "token": invalid})
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", adminToken,
			body), http.StatusBadRequest, "REQUEST_INVALID")
	}
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", runtimeToken,
		[]byte(`{"name":"main","token":"must-not-leak"}`)), http.StatusUnauthorized, "AUTHENTICATION_FAILED")
	listed := request(t, server.Handler(), http.MethodGet, "/v1/providers/fixture/tokens", adminToken, nil)
	if listed.Code != http.StatusOK || strings.TrimSpace(listed.Body.String()) != "[]" {
		t.Fatalf("tokens after rejected creates = %d/%q, want 200/[]", listed.Code, listed.Body.String())
	}
}

func TestCorruptTokenNeverFallsBackToPlaintext(t *testing.T) {
	var requests int
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer provider.Close()
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	createProvider(t, server.Handler(), provider.URL, "fixture-model", true)
	secret := "sk-never-fallback"
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", adminToken,
		[]byte(`{"name":"main","token":"`+secret+`"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create token status = %d, body = %s", created.Code, created.Body.String())
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE provider_tokens SET secret_encrypted = 'corrupt-ciphertext'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, generated, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")
	if requests != 0 || strings.Contains(generated.Body.String(), secret) || strings.Contains(generated.Body.String(), "corrupt-ciphertext") {
		t.Fatalf("unsafe corrupt-token behavior: requests=%d body=%s", requests, generated.Body.String())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second); err == nil {
		restarted.Close()
		t.Fatal("Open accepted corrupt stored token ciphertext")
	}
}

func TestGenerateNormalizesProviderCredentialRejectionWithoutDisclosure(t *testing.T) {
	secret := "sk-rejected-secret"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("provider Authorization = %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"upstream echoed sk-rejected-secret"}`))
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "fixture-model", true)
	created := request(t, server.Handler(), http.MethodPost, "/v1/providers/fixture/tokens", adminToken,
		[]byte(`{"name":"main","token":"`+secret+`"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create token status = %d, body = %s", created.Code, created.Body.String())
	}

	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, generated, http.StatusBadGateway, "AUTHENTICATION_FAILED")
	if strings.Contains(generated.Body.String(), secret) {
		t.Fatalf("Generate error disclosed credential: %s", generated.Body.String())
	}
}

func TestGatewayRejectsMissingAndMalformedMasterKeysBeforeOpeningDatabase(t *testing.T) {
	for _, key := range []string{"", "not-base64", "c2hvcnQ=", masterKey + "\n"} {
		dbPath := filepath.Join(t.TempDir(), "must-not-exist.db")
		server, err := gateway.Open(dbPath, adminToken, runtimeToken, key, time.Second)
		if server != nil {
			server.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "LLM_SERVER_MASTER_KEY") {
			t.Fatalf("Open key %q error = %v", key, err)
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Fatalf("invalid key opened database: stat error = %v", statErr)
		}
	}
}
