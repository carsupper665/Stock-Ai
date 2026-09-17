package test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func TestTokenLifecycleUsesStableEnabledSelectionAndNeverFallsBackAfterConfiguration(t *testing.T) {
	authorizations := make(chan string, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	first := createToken(t, server.Handler(), "fixture", "first", "secret-first")
	second := createToken(t, server.Handler(), "fixture", "second", "secret-second")
	selected, other := first, second
	selectedSecret, otherSecret := "secret-first", "secret-second"
	if second.CreatedAt < first.CreatedAt || (second.CreatedAt == first.CreatedAt && second.ID < first.ID) {
		selected, other = second, first
		selectedSecret, otherSecret = otherSecret, selectedSecret
	}

	generateAndExpectAuthorization(t, server.Handler(), authorizations, selectedSecret)
	disabled := request(t, server.Handler(), http.MethodPatch,
		"/v1/providers/fixture/tokens/"+selected.ID, adminToken, []byte(`{"enabled":false}`))
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable token status = %d, body = %s", disabled.Code, disabled.Body.String())
	}
	generateAndExpectAuthorization(t, server.Handler(), authorizations, otherSecret)

	replaced := request(t, server.Handler(), http.MethodPatch,
		"/v1/providers/fixture/tokens/"+other.ID, adminToken,
		[]byte(`{"name":"replacement","token":"secret-replaced"}`))
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace token status = %d, body = %s", replaced.Code, replaced.Body.String())
	}
	assertJSON(t, replaced.Body.Bytes(), map[string]any{
		"id": other.ID, "provider_id": "fixture", "name": "replacement",
		"masked": "sec****aced", "enabled": true,
	})
	if strings.Contains(replaced.Body.String(), "secret-replaced") {
		t.Fatalf("replacement response disclosed secret: %s", replaced.Body.String())
	}
	generateAndExpectAuthorization(t, server.Handler(), authorizations, "secret-replaced")
	enabled := request(t, server.Handler(), http.MethodPatch,
		"/v1/providers/fixture/tokens/"+selected.ID, adminToken, []byte(`{"enabled":true}`))
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable token status = %d, body = %s", enabled.Code, enabled.Body.String())
	}
	generateAndExpectAuthorization(t, server.Handler(), authorizations, selectedSecret)
	for _, tokenID := range []string{selected.ID, other.ID} {
		disabled = request(t, server.Handler(), http.MethodPatch,
			"/v1/providers/fixture/tokens/"+tokenID, adminToken, []byte(`{"enabled":false}`))
		if disabled.Code != http.StatusOK {
			t.Fatalf("disable all token status = %d, body = %s", disabled.Code, disabled.Body.String())
		}
	}
	unavailable := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, unavailable, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")

	for _, tokenID := range []string{selected.ID, other.ID} {
		deleted := request(t, server.Handler(), http.MethodDelete,
			"/v1/providers/fixture/tokens/"+tokenID, adminToken, nil)
		if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
			t.Fatalf("delete token status/body = %d/%q", deleted.Code, deleted.Body.String())
		}
	}
	unavailable = request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, unavailable, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")
	select {
	case authorization := <-authorizations:
		t.Fatalf("provider called after all configured tokens were deleted: %q", authorization)
	default:
	}
}

func TestTokenPatchIsAtomicScopedAndKeepsInFlightCredentialSnapshot(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.Header.Get("Authorization")
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)
	createProviderWithID(t, server.Handler(), "other", "Other", provider.URL, "model", true)
	token := createToken(t, server.Handler(), "fixture", "original", "secret-old")

	invalid := request(t, server.Handler(), http.MethodPatch,
		"/v1/providers/fixture/tokens/"+token.ID, adminToken,
		[]byte(`{"name":"must-not-stick","token":"invalid secret"}`))
	assertError(t, invalid, http.StatusBadRequest, "REQUEST_INVALID")
	listed := listTokens(t, server.Handler(), "fixture")
	if len(listed) != 1 || listed[0].Name != "original" {
		t.Fatalf("invalid patch partially changed token: %+v", listed)
	}
	assertError(t, request(t, server.Handler(), http.MethodPatch,
		"/v1/providers/other/tokens/"+token.ID, adminToken, []byte(`{"name":"stolen"}`)),
		http.StatusNotFound, "TOKEN_NOT_FOUND")

	inFlight := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		inFlight <- request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	}()
	select {
	case authorization := <-started:
		if authorization != "Bearer secret-old" {
			t.Fatalf("in-flight authorization = %q", authorization)
		}
	case <-time.After(time.Second):
		t.Fatal("Generate did not reach provider")
	}
	patched := request(t, server.Handler(), http.MethodPatch,
		"/v1/providers/fixture/tokens/"+token.ID, adminToken, []byte(`{"token":"secret-new"}`))
	if patched.Code != http.StatusOK {
		t.Fatalf("replace in-flight token status = %d, body = %s", patched.Code, patched.Body.String())
	}
	close(release)
	if response := <-inFlight; response.Code != http.StatusOK {
		t.Fatalf("in-flight Generate status = %d, body = %s", response.Code, response.Body.String())
	}

	next := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		next <- request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	}()
	select {
	case authorization := <-started:
		if authorization != "Bearer secret-new" {
			t.Fatalf("next authorization = %q", authorization)
		}
	case <-time.After(time.Second):
		t.Fatal("next Generate did not reach provider")
	}
	if response := <-next; response.Code != http.StatusOK {
		t.Fatalf("next Generate status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDeletingProviderAtomicallyDeletesOnlyItsTokens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	createProviderWithID(t, server.Handler(), "remove", "Remove", "http://127.0.0.1:1", "model", true)
	createProviderWithID(t, server.Handler(), "keep", "Keep", "http://127.0.0.1:1", "model", true)
	createToken(t, server.Handler(), "remove", "remove", "secret-remove")
	createToken(t, server.Handler(), "keep", "keep", "secret-keep")

	deleted := request(t, server.Handler(), http.MethodDelete, "/v1/providers/remove", adminToken, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete provider status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for providerID, want := range map[string]int{"remove": 0, "keep": 1} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM provider_tokens WHERE provider_id = ?`, providerID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("tokens for %s = %d, want %d", providerID, count, want)
		}
	}
}

func TestTokenStateMigrationBackfillsPre06AndFalseFlagsWithoutChangingTokenlessProviders(t *testing.T) {
	for _, operation := range []string{"disable", "delete"} {
		t.Run("pre06 "+operation, func(t *testing.T) {
			var upstreamRequests int
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamRequests++
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"anonymous"},"finish_reason":"stop"}]}`))
			}))
			defer provider.Close()
			dbPath, token := preparePre06TokenDatabase(t, provider.URL)
			server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "disable" {
				response := request(t, server.Handler(), http.MethodPatch,
					"/v1/providers/fixture/tokens/"+token.ID, adminToken, []byte(`{"enabled":false}`))
				if response.Code != http.StatusOK {
					t.Fatalf("disable migrated token status = %d, body = %s", response.Code, response.Body.String())
				}
			} else {
				response := request(t, server.Handler(), http.MethodDelete,
					"/v1/providers/fixture/tokens/"+token.ID, adminToken, nil)
				if response.Code != http.StatusNoContent {
					t.Fatalf("delete migrated token status = %d, body = %s", response.Code, response.Body.String())
				}
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			response := request(t, restarted.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
			assertError(t, response, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")
			if upstreamRequests != 0 {
				t.Fatalf("anonymous upstream requests after migrated token %s = %d", operation, upstreamRequests)
			}
		})
	}

	t.Run("existing false flag is repaired", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
		}))
		defer provider.Close()
		dbPath := filepath.Join(t.TempDir(), "gateway.db")
		server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		createProvider(t, server.Handler(), provider.URL, "model", true)
		token := createToken(t, server.Handler(), "fixture", "main", "secret-main")
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		db := openSQLite(t, dbPath)
		if _, err := db.Exec(`UPDATE providers SET tokens_configured = 0 WHERE id = 'fixture'`); err != nil {
			t.Fatal(err)
		}
		db.Close()
		restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		disabled := request(t, restarted.Handler(), http.MethodPatch,
			"/v1/providers/fixture/tokens/"+token.ID, adminToken, []byte(`{"enabled":false}`))
		if disabled.Code != http.StatusOK {
			t.Fatalf("disable repaired token status = %d, body = %s", disabled.Code, disabled.Body.String())
		}
		assertError(t, request(t, restarted.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)),
			http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")
	})

	t.Run("never configured remains tokenless", func(t *testing.T) {
		authorization := make(chan string, 1)
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authorization <- r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
		}))
		defer provider.Close()
		dbPath := filepath.Join(t.TempDir(), "gateway.db")
		server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		createProvider(t, server.Handler(), provider.URL, "model", true)
		server.Close()
		restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		response := request(t, restarted.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
		if response.Code != http.StatusOK {
			t.Fatalf("tokenless Generate status = %d, body = %s", response.Code, response.Body.String())
		}
		if got := <-authorization; got != "" {
			t.Fatalf("tokenless Authorization = %q", got)
		}
	})
}

func TestTokenStateMigrationRollsBackSchemaChangeOnBackfillFailure(t *testing.T) {
	dbPath := prepareProviderDatabase(t, providerTableSchema,
		`CREATE TABLE provider_tokens (id TEXT PRIMARY KEY)`,
		`INSERT INTO providers VALUES ('fixture', 'Fixture', 'openai_compatible',
		'http://127.0.0.1:1', 'model', '{}', 1,
		'2026-09-12T10:00:00Z', '2026-09-12T10:00:00Z')`)
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if server != nil {
		server.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "initialize provider token state") {
		t.Fatalf("Open migration error = %v", err)
	}
	db := openSQLite(t, dbPath)
	defer db.Close()
	if databaseColumnExists(t, db, "providers", "tokens_configured") {
		t.Fatal("failed migration committed tokens_configured schema change")
	}
}

func TestTokenAndProviderWriteFailuresRollbackCredentialState(t *testing.T) {
	for _, operation := range []string{"replace token", "delete provider"} {
		t.Run(operation, func(t *testing.T) {
			authorization := make(chan string, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization <- r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
			}))
			defer provider.Close()
			dbPath := filepath.Join(t.TempDir(), "gateway.db")
			server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			createProvider(t, server.Handler(), provider.URL, "model", true)
			created := createToken(t, server.Handler(), "fixture", "original", "secret-old")
			beforeCiphertext := tokenCiphertext(t, dbPath, created.ID)
			db := openSQLite(t, dbPath)
			trigger := `CREATE TRIGGER reject_token_update BEFORE UPDATE ON provider_tokens
				BEGIN SELECT RAISE(FAIL, 'token update rejected'); END`
			if operation == "delete provider" {
				trigger = `CREATE TRIGGER reject_provider_delete BEFORE DELETE ON providers
					BEGIN SELECT RAISE(FAIL, 'provider delete rejected'); END`
			}
			if _, err := db.Exec(trigger); err != nil {
				db.Close()
				t.Fatal(err)
			}
			db.Close()

			if operation == "replace token" {
				response := request(t, server.Handler(), http.MethodPatch,
					"/v1/providers/fixture/tokens/"+created.ID, adminToken,
					[]byte(`{"name":"changed","token":"secret-new"}`))
				assertError(t, response, http.StatusInternalServerError, "INTERNAL_ERROR")
			} else {
				response := request(t, server.Handler(), http.MethodDelete, "/v1/providers/fixture", adminToken, nil)
				assertError(t, response, http.StatusInternalServerError, "INTERNAL_ERROR")
			}
			after := listTokens(t, server.Handler(), "fixture")
			if len(after) != 1 || after[0] != created {
				t.Fatalf("credential metadata changed after failed %s: before=%+v after=%+v", operation, created, after)
			}
			if afterCiphertext := tokenCiphertext(t, dbPath, created.ID); afterCiphertext != beforeCiphertext {
				t.Fatalf("ciphertext changed after failed %s", operation)
			}
			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
			if response.Code != http.StatusOK {
				t.Fatalf("Generate after failed %s status = %d, body = %s", operation, response.Code, response.Body.String())
			}
			if got := <-authorization; got != "Bearer secret-old" {
				t.Fatalf("authorization after failed %s = %q", operation, got)
			}
		})
	}
}

func preparePre06TokenDatabase(t *testing.T, providerURL string) (string, ProviderTokenResponse) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	createProvider(t, server.Handler(), providerURL, "model", true)
	token := createToken(t, server.Handler(), "fixture", "main", "secret-main")
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	db := openSQLite(t, dbPath)
	if _, err := db.Exec(`ALTER TABLE providers DROP COLUMN tokens_configured`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath, token
}

func openSQLite(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func databaseColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var columnID int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}

func tokenCiphertext(t *testing.T, dbPath, tokenID string) string {
	t.Helper()
	db := openSQLite(t, dbPath)
	defer db.Close()
	var ciphertext string
	if err := db.QueryRow(`SELECT secret_encrypted FROM provider_tokens WHERE id = ?`, tokenID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	return ciphertext
}

func createToken(t *testing.T, handler http.Handler, providerID, name, secret string) ProviderTokenResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "token": secret})
	response := request(t, handler, http.MethodPost, "/v1/providers/"+providerID+"/tokens", adminToken, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create token status = %d, body = %s", response.Code, response.Body.String())
	}
	var token ProviderTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	return token
}

func listTokens(t *testing.T, handler http.Handler, providerID string) []ProviderTokenResponse {
	t.Helper()
	response := request(t, handler, http.MethodGet, "/v1/providers/"+providerID+"/tokens", adminToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list tokens status = %d, body = %s", response.Code, response.Body.String())
	}
	var tokens []ProviderTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	return tokens
}

func generateAndExpectAuthorization(t *testing.T, handler http.Handler, authorizations <-chan string, secret string) {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("Generate status = %d, body = %s", response.Code, response.Body.String())
	}
	if authorization := <-authorizations; authorization != "Bearer "+secret {
		t.Fatalf("provider Authorization = %q, want bearer for selected token", authorization)
	}
}

type ProviderTokenResponse struct {
	ID         string `json:"id"`
	ProviderID string `json:"provider_id"`
	Name       string `json:"name"`
	Masked     string `json:"masked"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}
