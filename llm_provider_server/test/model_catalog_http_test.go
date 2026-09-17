package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func TestRuntimeModelCatalogDrivesCompatibleGenerateLevelOptions(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", `[
		{"model_name":"fixture-reasoning","provider_id":"fixture","model":"actual-model",
		 "options":{"temperature":0,"reasoning_effort":"base"},
		 "levels":{"low":{"reasoning_effort":"low"},"high":{"reasoning_effort":"high"}}}
	]`)
	var requestCount atomic.Int32
	upstreamRequests := make(chan map[string]any, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		select {
		case upstreamRequests <- body:
		default:
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "actual-model", true)

	response := request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("models status = %d, body = %s", response.Code, response.Body.String())
	}
	var catalog struct {
		Models []struct {
			ModelName  string                    `json:"model_name"`
			ProviderID string                    `json:"provider_id"`
			Model      string                    `json:"model"`
			Options    map[string]any            `json:"options"`
			Levels     map[string]map[string]any `json:"levels"`
			Enabled    bool                      `json:"enabled"`
			Available  bool                      `json:"available"`
		} `json:"models"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("models = %+v", catalog.Models)
	}
	mapping := catalog.Models[0]
	if mapping.ModelName != "fixture-reasoning" || mapping.ProviderID != "fixture" || mapping.Model != "actual-model" ||
		!mapping.Enabled || !mapping.Available || mapping.Options["temperature"] != float64(0) ||
		mapping.Options["reasoning_effort"] != "base" || mapping.Levels["high"]["reasoning_effort"] != "high" {
		t.Fatalf("unexpected model mapping: %+v", mapping)
	}

	withoutLevel := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","model":"actual-model","messages":[{"role":"user","content":"hello"}]}`))
	if withoutLevel.Code != http.StatusOK {
		t.Fatalf("Generate without level status = %d, body = %s", withoutLevel.Code, withoutLevel.Body.String())
	}
	baseRequest := <-upstreamRequests
	if baseRequest["model"] != "actual-model" || baseRequest["temperature"] != float64(0) || baseRequest["reasoning_effort"] != "base" {
		t.Fatalf("upstream base-options request = %+v", baseRequest)
	}
	if _, exists := baseRequest["model_level"]; exists {
		t.Fatalf("gateway-only model_level leaked upstream: %+v", baseRequest)
	}

	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","model":"actual-model","messages":[{"role":"user","content":"hello"}],"options":{"model_level":"high"}}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("generate status = %d, body = %s", generated.Code, generated.Body.String())
	}
	assertJSON(t, generated.Body.Bytes(), map[string]any{
		"usage": map[string]any{
			"input_tokens": float64(9), "output_tokens": float64(2),
			"cached_tokens": float64(4), "total_tokens": float64(11),
		},
	})
	upstream := <-upstreamRequests
	if upstream["model"] != "actual-model" || upstream["temperature"] != float64(0) || upstream["reasoning_effort"] != "high" {
		t.Fatalf("upstream mapped request = %+v", upstream)
	}
	if _, exists := upstream["model_level"]; exists {
		t.Fatalf("gateway-only model_level leaked upstream: %+v", upstream)
	}

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unsupported level", body: `{"provider":"fixture","model":"actual-model","messages":[{"role":"user","content":"hello"}],"options":{"model_level":"max"}}`},
		{name: "null level", body: `{"provider":"fixture","model":"actual-model","messages":[{"role":"user","content":"hello"}],"options":{"model_level":null}}`},
		{name: "unmapped model", body: `{"provider":"fixture","model":"unknown-model","messages":[{"role":"user","content":"hello"}],"options":{"model_level":"high"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(test.body)),
				http.StatusBadRequest, "REQUEST_INVALID")
		})
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("provider requests after invalid levels = %d, want 2 successful requests only", got)
	}
}

func TestOpenedModelCatalogIsUnaffectedByEnvironmentAndProviderUpdates(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", `[
		{"model_name":"configured-name","provider_id":"fixture","model":"configured-actual",
		 "options":{"temperature":0.25}}
	]`)
	var oldRequests atomic.Int32
	oldProvider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		oldRequests.Add(1)
	}))
	defer oldProvider.Close()
	newRequests := make(chan map[string]any, 1)
	newProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode updated provider request: %v", err)
		}
		newRequests <- body
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
	}))
	defer newProvider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), oldProvider.URL, "old-default", true)

	t.Setenv("LLM_SERVER_MODEL_CATALOG", `[
		{"model_name":"environment-name","provider_id":"fixture","model":"environment-actual"}
	]`)
	patched := request(t, server.Handler(), http.MethodPatch, "/v1/providers/fixture", adminToken,
		[]byte(`{"base_url":"`+newProvider.URL+`","default_model":"new-default"}`))
	if patched.Code != http.StatusOK {
		t.Fatalf("patch Provider status = %d, body = %s", patched.Code, patched.Body.String())
	}

	listed := request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("models after updates status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var existing struct {
		Models []struct {
			Name      string         `json:"model_name"`
			Model     string         `json:"model"`
			Options   map[string]any `json:"options"`
			Available bool           `json:"available"`
		} `json:"models"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &existing); err != nil {
		t.Fatal(err)
	}
	if len(existing.Models) != 1 || existing.Models[0].Name != "configured-name" ||
		existing.Models[0].Model != "configured-actual" || existing.Models[0].Options["temperature"] != 0.25 ||
		!existing.Models[0].Available {
		t.Fatalf("opened catalog changed after environment/Provider update: %+v", existing.Models)
	}

	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","model":"configured-actual","messages":[{"role":"user","content":"hello"}]}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("Generate after Provider update status = %d, body = %s", generated.Code, generated.Body.String())
	}
	upstream := <-newRequests
	if upstream["model"] != "configured-actual" || upstream["temperature"] != 0.25 {
		t.Fatalf("updated Provider lost configured mapping: %+v", upstream)
	}
	if got := oldRequests.Load(); got != 0 {
		t.Fatalf("old Provider URL received %d requests after update", got)
	}

	fresh := openTestServer(t, time.Second)
	defer fresh.Close()
	freshList := request(t, fresh.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	var reopened struct {
		Models []struct {
			Name  string `json:"model_name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(freshList.Body.Bytes(), &reopened); err != nil {
		t.Fatal(err)
	}
	if freshList.Code != http.StatusOK || len(reopened.Models) != 1 || reopened.Models[0].Name != "environment-name" ||
		reopened.Models[0].Model != "environment-actual" {
		t.Fatalf("new Open did not read updated environment: status=%d models=%+v", freshList.Code, reopened.Models)
	}
}

func TestModelCatalogReportsConfiguredAndProviderAvailabilityInStableOrder(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", `[
		{"model_name":"z-disabled","provider_id":"disabled","model":"z-model","enabled":false},
		{"model_name":"a-ready","provider_id":"ready","model":"a-model"},
		{"model_name":"m-missing","provider_id":"missing","model":"m-model"}
	]`)
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProviderWithID(t, server.Handler(), "ready", "Ready", "http://127.0.0.1:1", "a-model", true)
	createProviderWithID(t, server.Handler(), "disabled", "Disabled", "http://127.0.0.1:1", "z-model", true)

	assertError(t, request(t, server.Handler(), http.MethodGet, "/v1/models", adminToken, nil),
		http.StatusUnauthorized, "AUTHENTICATION_FAILED")
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/models", runtimeToken, nil),
		http.StatusMethodNotAllowed, "REQUEST_INVALID")
	response := request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	var body struct {
		Models []struct {
			Name      string `json:"model_name"`
			Enabled   bool   `json:"enabled"`
			Available bool   `json:"available"`
		} `json:"models"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 3 || body.Models[0].Name != "a-ready" || body.Models[1].Name != "m-missing" || body.Models[2].Name != "z-disabled" {
		t.Fatalf("model order = %+v", body.Models)
	}
	if !body.Models[0].Enabled || !body.Models[0].Available || body.Models[1].Available || body.Models[2].Enabled || body.Models[2].Available {
		t.Fatalf("model availability = %+v", body.Models)
	}

	patched := request(t, server.Handler(), http.MethodPatch, "/v1/providers/ready", adminToken, []byte(`{"enabled":false}`))
	if patched.Code != http.StatusOK {
		t.Fatalf("disable provider status = %d, body = %s", patched.Code, patched.Body.String())
	}
	response = request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Models[0].Available {
		t.Fatalf("disabled provider model remained available: %+v", body.Models[0])
	}
	deleted := request(t, server.Handler(), http.MethodDelete, "/v1/providers/ready", adminToken, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete provider status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	response = request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Models[0].Available {
		t.Fatalf("deleted provider model remained available: %+v", body.Models[0])
	}

	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	empty := openTestServer(t, time.Second)
	defer empty.Close()
	emptyResponse := request(t, empty.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if emptyResponse.Code != http.StatusOK || strings.TrimSpace(emptyResponse.Body.String()) != `{"models":[]}` {
		t.Fatalf("empty models status/body = %d/%q", emptyResponse.Code, emptyResponse.Body.String())
	}
}

func TestGatewayRejectsAmbiguousOrInvalidModelCatalogAtStartup(t *testing.T) {
	for _, test := range []struct {
		name    string
		catalog string
	}{
		{name: "duplicate name", catalog: `[
			{"model_name":"same","provider_id":"one","model":"first"},
			{"model_name":"same","provider_id":"two","model":"second"}]`},
		{name: "ambiguous provider model", catalog: `[
			{"model_name":"one","provider_id":"provider","model":"same"},
			{"model_name":"two","provider_id":"provider","model":"same"}]`},
		{name: "invalid level", catalog: `[
			{"model_name":"one","provider_id":"provider","model":"model","levels":{"turbo":{}}}]`},
		{name: "null level options", catalog: `[
			{"model_name":"one","provider_id":"provider","model":"model","levels":{"low":null}}]`},
		{name: "reserved option", catalog: `[
			{"model_name":"one","provider_id":"provider","model":"model","options":{"model":"override"}}]`},
		{name: "null enabled", catalog: `[
			{"model_name":"one","provider_id":"provider","model":"model","enabled":null}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LLM_SERVER_MODEL_CATALOG", test.catalog)
			dbPath := filepath.Join(t.TempDir(), "must-not-open.db")
			server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, time.Second)
			if server != nil {
				server.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "LLM_SERVER_MODEL_CATALOG") {
				t.Fatalf("Open catalog error = %v", err)
			}
		})
	}
}
