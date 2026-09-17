package test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func TestModelManagementPersistsAndGeneratesWithoutRestart(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "[]")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"new provider works"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "gateway.db")
	server, err := gateway.Open(path, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { server.Close() }()
	createProvider(t, server.Handler(), upstream.URL, "new-model", true)
	body := []byte(`{"model_name":"new-choice","provider_id":"fixture","model":"new-model","options":{},"levels":{"high":{"temperature":0}},"enabled":true}`)
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/model-catalog", runtimeToken, body), http.StatusUnauthorized, "AUTHENTICATION_FAILED")
	created := request(t, server.Handler(), http.MethodPost, "/v1/model-catalog", adminToken, body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/model-catalog", adminToken, body), http.StatusConflict, "MODEL_ALREADY_EXISTS")
	listed := request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if !strings.Contains(listed.Body.String(), `"model_name":"new-choice"`) {
		t.Fatal(listed.Body)
	}
	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","model":"new-model","messages":[{"role":"user","content":"hello"}],"options":{"model_level":"high"}}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("generate=%d %s", generated.Code, generated.Body)
	}
	server.Close()
	server, err = gateway.Open(path, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	listed = request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if !strings.Contains(listed.Body.String(), "new-choice") {
		t.Fatal("saved mapping lost", listed.Body)
	}
	invalid := []byte(`{"model_name":"new-choice","provider_id":"fixture","model":"new-model","levels":{"invalid":{}}}`)
	assertError(t, request(t, server.Handler(), http.MethodPut, "/v1/model-catalog/new-choice", adminToken, invalid), http.StatusBadRequest, "REQUEST_INVALID")
	removed := request(t, server.Handler(), http.MethodDelete, "/v1/model-catalog/new-choice", adminToken, nil)
	if removed.Code != http.StatusNoContent {
		t.Fatal(removed.Code, removed.Body)
	}
	server.Close()
	server, err = gateway.Open(path, adminToken, runtimeToken, masterKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	listed = request(t, server.Handler(), http.MethodGet, "/v1/models", runtimeToken, nil)
	if strings.Contains(listed.Body.String(), "new-choice") {
		t.Fatal("deleted mapping restored", listed.Body)
	}
}
