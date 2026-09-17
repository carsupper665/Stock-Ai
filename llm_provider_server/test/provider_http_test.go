package test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

const (
	adminToken          = "admin-secret"
	runtimeToken        = "runtime-secret"
	masterKey           = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	providerTableSchema = `CREATE TABLE providers (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, base_url TEXT NOT NULL,
		default_model TEXT, config_json TEXT NOT NULL, enabled INTEGER NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`
)

func TestAdminCanCreateAndReadProviderAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	createBody := []byte(`{"id":"local-qwen","name":"Local Qwen","type":"openai_compatible","base_url":"http://127.0.0.1:8000/v1","default_model":"qwen3","config":{},"enabled":true}`)
	response := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, createBody)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSON(t, response.Body.Bytes(), map[string]any{
		"id": "local-qwen", "name": "Local Qwen", "type": "openai_compatible",
		"base_url": "http://127.0.0.1:8000/v1", "default_model": "qwen3",
		"config": map[string]any{}, "enabled": true,
	})

	for _, token := range []string{"", runtimeToken} {
		denied := request(t, server.Handler(), http.MethodGet, "/v1/providers/local-qwen", token, nil)
		if denied.Code != http.StatusUnauthorized {
			t.Fatalf("management request with token %q status = %d", token, denied.Code)
		}
	}
	server.Close()

	restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	get := request(t, restarted.Handler(), http.MethodGet, "/v1/providers/local-qwen", adminToken, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", get.Code, get.Body.String())
	}
	assertJSON(t, get.Body.Bytes(), map[string]any{
		"id": "local-qwen", "name": "Local Qwen", "type": "openai_compatible",
		"base_url": "http://127.0.0.1:8000/v1", "default_model": "qwen3",
		"config": map[string]any{}, "enabled": true,
	})
}

func TestAdminCanListProvidersInStableOrder(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	empty := request(t, server.Handler(), http.MethodGet, "/v1/providers", adminToken, nil)
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty list status/body = %d/%q, want 200/[]", empty.Code, empty.Body.String())
	}
	createProviderWithID(t, server.Handler(), "z-provider", "Zed", "http://127.0.0.1:8001/v1", "z-model", true)
	createProviderWithID(t, server.Handler(), "a-provider", "Alpha", "http://127.0.0.1:8002/v1", "a-model", false)

	response := request(t, server.Handler(), http.MethodGet, "/v1/providers", adminToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("list content type = %q", response.Header().Get("Content-Type"))
	}
	var providers []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &providers); err != nil {
		t.Fatalf("invalid list JSON: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(providers))
	}
	if providers[0]["id"] != "a-provider" || providers[1]["id"] != "z-provider" {
		t.Fatalf("provider order = %v, want a-provider then z-provider", []any{providers[0]["id"], providers[1]["id"]})
	}
}

func TestProviderLifecycleMethodsAdvertiseAllowedOperations(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	for _, test := range []struct {
		path  string
		allow string
	}{
		{path: "/v1/providers", allow: "GET, POST"},
		{path: "/v1/providers/fixture", allow: "GET, PATCH, DELETE"},
	} {
		response := request(t, server.Handler(), http.MethodPut, test.path, adminToken, nil)
		assertError(t, response, http.StatusMethodNotAllowed, "REQUEST_INVALID")
		if got := response.Header().Get("Allow"); got != test.allow {
			t.Errorf("%s Allow = %q, want %q", test.path, got, test.allow)
		}
	}
}

func TestAdminCanPatchProviderWithDocumentedOmittedAndNullSemantics(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	createProvider(t, server.Handler(), "http://127.0.0.1:8000/v1", "old-model", true)
	before := providerTimes(t, request(t, server.Handler(), http.MethodGet, "/v1/providers/fixture", adminToken, nil).Body.Bytes())

	rename := request(t, server.Handler(), http.MethodPatch, "/v1/providers/fixture", adminToken,
		[]byte(`{"name":"Renamed"}`))
	if rename.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body = %s", rename.Code, rename.Body.String())
	}
	assertJSON(t, rename.Body.Bytes(), map[string]any{
		"id": "fixture", "name": "Renamed", "type": "openai_compatible",
		"base_url": "http://127.0.0.1:8000/v1", "default_model": "old-model",
		"config": map[string]any{}, "enabled": true,
	})
	afterRename := providerTimes(t, rename.Body.Bytes())
	if afterRename.created != before.created || afterRename.updated == before.updated {
		t.Fatalf("rename timestamps = %+v, original = %+v", afterRename, before)
	}
	cleared := request(t, server.Handler(), http.MethodPatch, "/v1/providers/fixture", adminToken,
		[]byte(`{"base_url":"http://127.0.0.1:9000/v1/","default_model":null,"config":null,"enabled":false}`))
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", cleared.Code, cleared.Body.String())
	}
	assertJSON(t, cleared.Body.Bytes(), map[string]any{
		"id": "fixture", "name": "Renamed", "base_url": "http://127.0.0.1:9000/v1",
		"default_model": nil, "config": map[string]any{}, "enabled": false,
	})
	afterClear := providerTimes(t, cleared.Body.Bytes())
	if afterClear.created != before.created || afterClear.updated == afterRename.updated {
		t.Fatalf("clear timestamps = %+v, prior = %+v", afterClear, afterRename)
	}
	server.Close()

	restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	get := request(t, restarted.Handler(), http.MethodGet, "/v1/providers/fixture", adminToken, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get after restart status = %d, body = %s", get.Code, get.Body.String())
	}
	assertJSON(t, get.Body.Bytes(), map[string]any{
		"name": "Renamed", "base_url": "http://127.0.0.1:9000/v1",
		"default_model": nil, "config": map[string]any{}, "enabled": false,
	})
}

func TestAdminCanDeleteOnlyTheRequestedProvider(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	createProviderWithID(t, server.Handler(), "remove-me", "Remove", "http://127.0.0.1:8001/v1", "model", true)
	createProviderWithID(t, server.Handler(), "keep-me", "Keep", "http://127.0.0.1:8002/v1", "model", true)

	deleted := request(t, server.Handler(), http.MethodDelete, "/v1/providers/remove-me", adminToken, nil)
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete status/body = %d/%q, want 204 with empty body", deleted.Code, deleted.Body.String())
	}
	assertError(t, request(t, server.Handler(), http.MethodGet, "/v1/providers/remove-me", adminToken, nil),
		http.StatusNotFound, "PROVIDER_NOT_FOUND")
	if get := request(t, server.Handler(), http.MethodGet, "/v1/providers/keep-me", adminToken, nil); get.Code != http.StatusOK {
		t.Fatalf("unrelated provider status = %d, body = %s", get.Code, get.Body.String())
	}
	server.Close()

	restarted, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	assertError(t, request(t, restarted.Handler(), http.MethodDelete, "/v1/providers/remove-me", adminToken, nil),
		http.StatusNotFound, "PROVIDER_NOT_FOUND")
	if get := request(t, restarted.Handler(), http.MethodGet, "/v1/providers/keep-me", adminToken, nil); get.Code != http.StatusOK {
		t.Fatalf("persisted unrelated provider status = %d, body = %s", get.Code, get.Body.String())
	}
}

func TestProviderUpdateUsesSnapshotForInFlightGenerateAndNewSettingsThereafter(t *testing.T) {
	oldStarted := make(chan string, 1)
	releaseOld := make(chan struct{})
	oldReleased := false
	defer func() {
		if !oldReleased {
			close(releaseOld)
		}
	}()
	oldProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		oldStarted <- body["model"].(string)
		<-releaseOld
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"old answer"},"finish_reason":"stop"}]}`))
	}))
	defer oldProvider.Close()
	var newRequests atomic.Int32
	newModels := make(chan string, 1)
	newProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newRequests.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		newModels <- body["model"].(string)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"new answer"},"finish_reason":"stop"}]}`))
	}))
	defer newProvider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), oldProvider.URL, "old-model", true)

	inFlight := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		inFlight <- request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	}()
	var inFlightModel string
	select {
	case inFlightModel = <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("in-flight Generate did not reach old provider")
	}
	if inFlightModel != "old-model" {
		t.Fatalf("in-flight model = %q, want old-model", inFlightModel)
	}
	updated := request(t, server.Handler(), http.MethodPatch, "/v1/providers/fixture", adminToken,
		[]byte(`{"base_url":"`+newProvider.URL+`","default_model":"new-model","enabled":false}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	close(releaseOld)
	oldReleased = true
	if response := <-inFlight; response.Code != http.StatusOK {
		t.Fatalf("in-flight generate status = %d, body = %s", response.Code, response.Body.String())
	}

	disabled := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, disabled, http.StatusConflict, "PROVIDER_DISABLED")
	if got := newRequests.Load(); got != 0 {
		t.Fatalf("new provider requests while disabled = %d, want 0", got)
	}
	enabled := request(t, server.Handler(), http.MethodPatch, "/v1/providers/fixture", adminToken,
		[]byte(`{"enabled":true}`))
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", enabled.Code, enabled.Body.String())
	}
	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("new generate status = %d, body = %s", generated.Code, generated.Body.String())
	}
	select {
	case model := <-newModels:
		if model != "new-model" {
			t.Fatalf("new model = %q, want new-model", model)
		}
	case <-time.After(time.Second):
		t.Fatal("Generate did not reach updated provider")
	}
}

func TestProviderDeleteDoesNotInterruptInFlightGenerate(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	inFlight := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		inFlight <- request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("in-flight Generate did not reach provider")
	}
	deleted := request(t, server.Handler(), http.MethodDelete, "/v1/providers/fixture", adminToken, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	close(release)
	released = true
	if response := <-inFlight; response.Code != http.StatusOK {
		t.Fatalf("in-flight generate status = %d, body = %s", response.Code, response.Body.String())
	}
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)),
		http.StatusNotFound, "PROVIDER_NOT_FOUND")
}

func TestInvalidProviderPatchIsAtomicAndIDIsImmutable(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), "http://127.0.0.1:8000/v1", "model", true)

	for _, test := range []struct {
		name string
		body string
		code string
	}{
		{name: "empty", body: `{}`, code: "PROVIDER_INVALID_CONFIG"},
		{name: "top-level null", body: `null`, code: "REQUEST_INVALID"},
		{name: "id", body: `{"id":"other"}`, code: "REQUEST_INVALID"},
		{name: "null required field", body: `{"name":null}`, code: "PROVIDER_INVALID_CONFIG"},
		{name: "null enabled", body: `{"enabled":null}`, code: "PROVIDER_INVALID_CONFIG"},
		{name: "unsupported type", body: `{"type":"unsupported"}`, code: "PROVIDER_INVALID_CONFIG"},
		{name: "nonempty config", body: `{"config":{"secret":"must-not-be-stored"}}`, code: "PROVIDER_INVALID_CONFIG"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, server.Handler(), http.MethodPatch, "/v1/providers/fixture", adminToken, []byte(test.body))
			assertError(t, response, http.StatusBadRequest, test.code)
			get := request(t, server.Handler(), http.MethodGet, "/v1/providers/fixture", adminToken, nil)
			assertJSON(t, get.Body.Bytes(), map[string]any{
				"id": "fixture", "name": "Fixture", "type": "openai_compatible",
				"base_url": "http://127.0.0.1:8000/v1", "default_model": "model",
				"config": map[string]any{}, "enabled": true,
			})
		})
	}
	assertError(t, request(t, server.Handler(), http.MethodPatch, "/v1/providers/missing", adminToken,
		[]byte(`{"name":"Missing"}`)), http.StatusNotFound, "PROVIDER_NOT_FOUND")
}

func TestProviderLifecycleDatabaseReadFailuresReturnInternalError(t *testing.T) {
	dbPath := prepareProviderDatabase(t, `CREATE TABLE providers (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, base_url TEXT NOT NULL,
		default_model TEXT, config_json TEXT NOT NULL, enabled INTEGER NOT NULL, created_at TEXT NOT NULL
	)`)
	server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	assertError(t, request(t, server.Handler(), http.MethodGet, "/v1/providers", adminToken, nil),
		http.StatusInternalServerError, "INTERNAL_ERROR")
	assertError(t, request(t, server.Handler(), http.MethodPatch, "/v1/providers/fixture", adminToken,
		[]byte(`{"name":"Updated"}`)), http.StatusInternalServerError, "INTERNAL_ERROR")
}

func TestProviderLifecycleWriteFailuresReturnInternalErrorAndPreserveProvider(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
		method  string
		body    []byte
	}{
		{
			name: "patch update", method: http.MethodPatch, body: []byte(`{"name":"Updated"}`),
			trigger: `CREATE TRIGGER reject_provider_update BEFORE UPDATE ON providers
				BEGIN SELECT RAISE(FAIL, 'update rejected'); END`,
		},
		{
			name: "delete", method: http.MethodDelete,
			trigger: `CREATE TRIGGER reject_provider_delete BEFORE DELETE ON providers
				BEGIN SELECT RAISE(FAIL, 'delete rejected'); END`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath := prepareProviderDatabase(t, providerTableSchema,
				`INSERT INTO providers VALUES ('fixture', 'Fixture', 'openai_compatible',
				'http://127.0.0.1:8000/v1', 'model', '{}', 1,
				'2026-09-12T10:00:00Z', '2026-09-12T10:00:00Z')`, test.trigger)
			server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()

			response := request(t, server.Handler(), test.method, "/v1/providers/fixture", adminToken, test.body)
			assertError(t, response, http.StatusInternalServerError, "INTERNAL_ERROR")
			get := request(t, server.Handler(), http.MethodGet, "/v1/providers/fixture", adminToken, nil)
			if get.Code != http.StatusOK {
				t.Fatalf("read after failed %s status = %d, body = %s", test.name, get.Code, get.Body.String())
			}
			assertJSON(t, get.Body.Bytes(), map[string]any{
				"id": "fixture", "name": "Fixture", "base_url": "http://127.0.0.1:8000/v1",
				"default_model": "model", "config": map[string]any{}, "enabled": true,
				"updated_at": "2026-09-12T10:00:00Z",
			})
		})
	}
}

func TestGenerateUsesProviderDefaultModelAndNormalizesText(t *testing.T) {
	var upstreamRequest map[string]any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &upstreamRequest); err != nil {
			t.Errorf("upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fixture answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer provider.Close()

	server, err := gateway.Open(filepath.Join(t.TempDir(), "gateway.db"), adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL+"/v1", "fixture-model", true)

	generate := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}],"options":{}}`))
	if generate.Code != http.StatusOK {
		t.Fatalf("generate status = %d, body = %s", generate.Code, generate.Body.String())
	}
	assertJSON(t, generate.Body.Bytes(), map[string]any{
		"content": "fixture answer", "tool_calls": []any{}, "finish_reason": "stop",
		"usage": map[string]any{"input_tokens": float64(7), "output_tokens": float64(3),
			"cached_tokens": nil, "total_tokens": nil},
	})
	if upstreamRequest["model"] != "fixture-model" {
		t.Errorf("upstream model = %v", upstreamRequest["model"])
	}
	wantMessages := []any{map[string]any{"role": "user", "content": "hello"}}
	wantJSON, _ := json.Marshal(wantMessages)
	gotJSON, _ := json.Marshal(upstreamRequest["messages"])
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("upstream messages = %s, want %s", gotJSON, wantJSON)
	}
}

func TestGenerateRejectsProviderResponseWithTrailingJSON(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]} {}`))
	}))
	defer provider.Close()
	server, err := gateway.Open(filepath.Join(t.TempDir(), "gateway.db"), adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "fixture-model", true)

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, response, http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID")
}

func TestGenerateDoesNotSilentlyDropUnexpectedToolCalls(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer","tool_calls":[{"id":"call-1"}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "fixture-model", true)

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, response, http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID")
}

func TestGenerateOverridesModelAndKeepsUnknownUsageFieldsNull(t *testing.T) {
	model := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		model <- request["model"].(string)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5}}`))
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "default-model", true)

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","model":"override-model","messages":[{"role":"user","content":"hello"}]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSON(t, response.Body.Bytes(), map[string]any{
		"usage": map[string]any{"input_tokens": float64(5), "output_tokens": nil,
			"cached_tokens": nil, "total_tokens": nil},
	})
	if got := <-model; got != "override-model" {
		t.Errorf("upstream model = %q", got)
	}
}

func TestGenerateRejectsUnavailableProviderStatesBeforeExternalCall(t *testing.T) {
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()

	generateBody := []byte(`{"provider":"missing","messages":[{"role":"user","content":"hello"}]}`)
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, generateBody),
		http.StatusNotFound, "PROVIDER_NOT_FOUND")
	createProvider(t, server.Handler(), provider.URL, "model", false)
	generateBody = []byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, generateBody),
		http.StatusConflict, "PROVIDER_DISABLED")
	if requests != 0 {
		t.Fatalf("external requests = %d, want 0", requests)
	}
}

func TestGenerateReportsMissingModelAndInvalidRequests(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), "http://127.0.0.1:1", "", true)
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)),
		http.StatusBadRequest, "MODEL_NOT_FOUND")

	invalidBodies := [][]byte{
		[]byte(`not-json`),
		[]byte(`{"provider":"fixture","messages":[]}`),
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}],"unknown":true}`),
		[]byte(`{"provider":"fixture","messages":[{"role":"tool","content":"hello"}]}`),
	}
	for _, body := range invalidBodies {
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body),
			http.StatusBadRequest, "REQUEST_INVALID")
	}
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", adminToken, invalidBodies[1]),
		http.StatusUnauthorized, "AUTHENTICATION_FAILED")
}

func TestGenerateNormalizesTimeoutFailureAndInvalidResponse(t *testing.T) {
	releaseTimeoutFixture := make(chan struct{})
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		timeout   time.Duration
		status    int
		errorCode string
	}{
		{
			name: "timeout", timeout: 20 * time.Millisecond, status: http.StatusGatewayTimeout, errorCode: "PROVIDER_TIMEOUT",
			handler: func(http.ResponseWriter, *http.Request) { <-releaseTimeoutFixture },
		},
		{
			name: "failed status", timeout: time.Second, status: http.StatusBadGateway, errorCode: "PROVIDER_UNAVAILABLE",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		},
		{
			name: "invalid response", timeout: time.Second, status: http.StatusBadGateway, errorCode: "PROVIDER_RESPONSE_INVALID",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"choices":[]}`)) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := httptest.NewServer(test.handler)
			defer provider.Close()
			if test.name == "timeout" {
				defer close(releaseTimeoutFixture)
			}
			server := openTestServer(t, test.timeout)
			defer server.Close()
			createProvider(t, server.Handler(), provider.URL, "model", true)
			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
			assertError(t, response, test.status, test.errorCode)
		})
	}
}

func TestGenerateTimesOutWhileReadingProviderResponseBody(t *testing.T) {
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[`))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer provider.Close()
	defer close(release)
	server := openTestServer(t, 20*time.Millisecond)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, response, http.StatusGatewayTimeout, "PROVIDER_TIMEOUT")
}

func TestGenerateEnforcesExactProviderResponseLimit(t *testing.T) {
	responseJSON := `{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`
	for _, test := range []struct {
		name   string
		size   int
		status int
		code   string
	}{
		{name: "exactly one MiB", size: 1 << 20, status: http.StatusOK},
		{name: "one byte over", size: (1 << 20) + 1, status: http.StatusBadGateway, code: "PROVIDER_RESPONSE_INVALID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := responseJSON + strings.Repeat(" ", test.size-len(responseJSON))
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createProvider(t, server.Handler(), provider.URL, "model", true)

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
			if test.code == "" {
				if response.Code != test.status {
					t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
				}
				return
			}
			assertError(t, response, test.status, test.code)
		})
	}
}

func TestGenerateDoesNotFollowProviderRedirects(t *testing.T) {
	for _, redirectStatus := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(redirectStatus), func(t *testing.T) {
			var targetRequests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				targetRequests.Add(1)
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"redirected"},"finish_reason":"stop"}]}`))
			}))
			defer target.Close()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", target.URL+"/stolen")
				w.WriteHeader(redirectStatus)
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createProvider(t, server.Handler(), provider.URL, "model", true)

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"secret prompt"}]}`))
			assertError(t, response, http.StatusBadGateway, "PROVIDER_UNAVAILABLE")
			if got := targetRequests.Load(); got != 0 {
				t.Fatalf("redirect target requests = %d, want 0", got)
			}
		})
	}
}

func TestCreateProviderDistinguishesDuplicateIDFromDatabaseFailure(t *testing.T) {
	server := openTestServer(t, time.Second)
	createProvider(t, server.Handler(), "http://127.0.0.1:8000/v1", "model", true)
	duplicate := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken,
		[]byte(`{"id":"fixture","name":"Fixture","type":"openai_compatible","base_url":"http://127.0.0.1:8000/v1"}`))
	assertError(t, duplicate, http.StatusConflict, "PROVIDER_INVALID_CONFIG")
	server.Close()

	dbPath := filepath.Join(t.TempDir(), "fault.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE providers (
		id TEXT PRIMARY KEY, name TEXT NOT NULL CHECK (name <> 'Fault'), type TEXT NOT NULL,
		base_url TEXT NOT NULL, default_model TEXT, config_json TEXT NOT NULL, enabled INTEGER NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	faultServer, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer faultServer.Close()
	fault := request(t, faultServer.Handler(), http.MethodPost, "/v1/providers", adminToken,
		[]byte(`{"id":"fault","name":"Fault","type":"openai_compatible","base_url":"http://127.0.0.1:8000/v1"}`))
	assertError(t, fault, http.StatusInternalServerError, "INTERNAL_ERROR")
}

func TestAuthorizationRequiresExactBearerScheme(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	body := []byte(`{"id":"fixture","name":"Fixture","type":"openai_compatible","base_url":"http://127.0.0.1:8000/v1"}`)
	for _, authorization := range []string{adminToken, "bearer " + adminToken, "Basic " + adminToken, "Bearer" + adminToken} {
		req := httptest.NewRequest(http.MethodPost, "/v1/providers", bytes.NewReader(body))
		req.Header.Set("Authorization", authorization)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		assertError(t, response, http.StatusUnauthorized, "AUTHENTICATION_FAILED")
	}
}

func TestHealthIsPublicAndDoesNotContactProviders(t *testing.T) {
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)
	response := request(t, server.Handler(), http.MethodGet, "/healthz", "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("health status/content-type = %d/%q", response.Code, response.Header().Get("Content-Type"))
	}
	assertJSON(t, response.Body.Bytes(), map[string]any{"status": "ok"})
	if requests != 0 {
		t.Fatalf("external requests = %d, want 0", requests)
	}
}

func TestCreateProviderRejectsInvalidConfiguration(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	invalid := []byte(`{"id":"bad id","name":"Bad","type":"anthropic","base_url":"file:///tmp/model","enabled":true}`)
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, invalid),
		http.StatusBadRequest, "PROVIDER_INVALID_CONFIG")
}

func openTestServer(t *testing.T, timeout time.Duration) *gateway.Server {
	t.Helper()
	server, err := gateway.Open(filepath.Join(t.TempDir(), "gateway.db"), adminToken, runtimeToken, masterKey, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func prepareProviderDatabase(t *testing.T, statements ...string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("prepare provider database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func createProvider(t *testing.T, handler http.Handler, baseURL, defaultModel string, enabled bool) {
	t.Helper()
	createProviderWithID(t, handler, "fixture", "Fixture", baseURL, defaultModel, enabled)
}

func createProviderWithID(t *testing.T, handler http.Handler, id, name, baseURL, defaultModel string, enabled bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"id": id, "name": name, "type": "openai_compatible",
		"base_url": baseURL, "default_model": defaultModel, "enabled": enabled,
	})
	response := request(t, handler, http.MethodPost, "/v1/providers", adminToken, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create fixture provider status = %d, body = %s", response.Code, response.Body.String())
	}
}

func request(t *testing.T, handler http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func assertJSON(t *testing.T, body []byte, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", body, err)
	}
	for key, value := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Fatalf("missing %q in %s", key, body)
		}
		wantJSON, _ := json.Marshal(value)
		gotJSON, _ := json.Marshal(gotValue)
		if !bytes.Equal(wantJSON, gotJSON) {
			t.Errorf("%s = %s, want %s", key, gotJSON, wantJSON)
		}
	}
}

func assertError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("error Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid error JSON: %v", err)
	}
	if body.Error.Code != code {
		t.Errorf("error code = %q, want %q", body.Error.Code, code)
	}
}

func providerTimes(t *testing.T, body []byte) struct{ created, updated string } {
	t.Helper()
	var provider struct {
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(body, &provider); err != nil {
		t.Fatalf("invalid provider JSON: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, provider.CreatedAt); err != nil {
		t.Fatalf("invalid created_at %q: %v", provider.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, provider.UpdatedAt); err != nil {
		t.Fatalf("invalid updated_at %q: %v", provider.UpdatedAt, err)
	}
	return struct{ created, updated string }{provider.CreatedAt, provider.UpdatedAt}
}
