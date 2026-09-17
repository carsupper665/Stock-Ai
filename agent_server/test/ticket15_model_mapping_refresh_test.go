package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTicket15EachDecisionRefreshesCurrentModelMapping(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.modelsHandler = func(w http.ResponseWriter, _ *http.Request, requestNumber int) {
		providerModel := "provider-model-a"
		if requestNumber >= 3 {
			providerModel = "provider-model-b"
		}
		writeModelCatalog(w, http.StatusOK, providerModel, true, map[string]any{"high": map[string]any{}})
	}
	var mu sync.Mutex
	models := make([]string, 0, 2)
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		mu.Lock()
		models = append(models, body.Model)
		round := len(models)
		mu.Unlock()
		if round == 1 {
			writeToolCall(w, "price", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("mapped twice", "mapped twice", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"model_level": "high"})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 2 || models[0] != "provider-model-a" || models[1] != "provider-model-b" {
		t.Fatalf("per-decision mapped models = %v, want [provider-model-a provider-model-b]", models)
	}
}

func TestTicket15MappingFailureBeforeLaterDecisionStopsWithoutStaleGenerate(t *testing.T) {
	for _, test := range []struct {
		name          string
		secondCatalog func(http.ResponseWriter)
		wantError     string
	}{
		{name: "model unavailable", secondCatalog: func(w http.ResponseWriter) {
			writeModelCatalog(w, http.StatusOK, "provider-model", false, map[string]any{"high": map[string]any{}})
		}, wantError: "MODEL_UNAVAILABLE"},
		{name: "model removed", secondCatalog: func(w http.ResponseWriter) {
			writeFixtureJSON(w, http.StatusOK, map[string]any{"models": []any{}})
		}, wantError: "MODEL_NOT_FOUND"},
		{name: "level removed", secondCatalog: func(w http.ResponseWriter) {
			writeModelCatalog(w, http.StatusOK, "provider-model", true, map[string]any{})
		}, wantError: "INVALID_REQUEST"},
		{name: "catalog unavailable", secondCatalog: func(w http.ResponseWriter) {
			writeFixtureJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": "INTERNAL_ERROR"}})
		}, wantError: "GATEWAY_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.modelsHandler = func(w http.ResponseWriter, _ *http.Request, requestNumber int) {
				if requestNumber < 3 {
					writeModelCatalog(w, http.StatusOK, "provider-model", true, map[string]any{"high": map[string]any{}})
					return
				}
				test.secondCatalog(w)
			}
			fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
				writeToolCall(w, "price", "get_market_snapshot", map[string]any{"symbol": "BTCUSDT"})
			}
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, map[string]any{"model_level": "high"})
			startSessionForTest(t, runtime, session.ID)
			run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
			if run.Summary != nil || run.Error == nil || !strings.Contains(*run.Error, test.wantError) || run.ModelCalls != 1 {
				t.Fatalf("mapping failure Run = %+v", run)
			}
			fixture.mu.Lock()
			generateCalls := fixture.generateCalls
			fixture.mu.Unlock()
			if generateCalls != 1 {
				t.Fatalf("Generate calls = %d, want no stale second Generate", generateCalls)
			}
		})
	}
}

func TestTicket15StopDuringDecisionModelLookupDispatchesNoGenerate(t *testing.T) {
	fixture := newRuntimeFixture()
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	fixture.modelsHandler = func(w http.ResponseWriter, request *http.Request, requestNumber int) {
		if requestNumber == 1 {
			writeModelCatalog(w, http.StatusOK, "provider-model", true, map[string]any{})
			return
		}
		close(lookupStarted)
		select {
		case <-request.Context().Done():
		case <-releaseLookup:
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitTestSignal(t, lookupStarted, "decision model lookup")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop status = %d body = %s", status, raw)
	}
	close(releaseLookup)
	run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	if run.ModelCalls != 0 {
		t.Fatalf("Generate dispatches = %d, want 0", run.ModelCalls)
	}
	fixture.mu.Lock()
	generateCalls := fixture.generateCalls
	fixture.mu.Unlock()
	if generateCalls != 0 {
		t.Fatalf("Generate calls after stopped lookup = %d", generateCalls)
	}
}

func TestTicket15InFlightGenerateKeepsItsResolvedMapping(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.modelsHandler = func(w http.ResponseWriter, _ *http.Request, requestNumber int) {
		model := "provider-model-a"
		if requestNumber >= 3 {
			model = "provider-model-b"
		}
		writeModelCatalog(w, http.StatusOK, model, true, map[string]any{})
	}
	generateStarted := make(chan struct{})
	releaseGenerate := make(chan struct{})
	var requestedModel string
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		requestedModel = body.Model
		close(generateStarted)
		<-releaseGenerate
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent("done", "done", nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	waitTestSignal(t, generateStarted, "in-flight Generate")
	fixture.mu.Lock()
	fixture.models = []any{map[string]any{"model_name": "fixture-model", "provider_id": "fixture", "model": "provider-model-b", "levels": map[string]any{}, "enabled": true, "available": true}}
	fixture.mu.Unlock()
	close(releaseGenerate)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if requestedModel != "provider-model-a" {
		t.Fatalf("in-flight Generate model = %q, want provider-model-a", requestedModel)
	}
}

func writeModelCatalog(w http.ResponseWriter, status int, providerModel string, available bool, levels map[string]any) {
	writeFixtureJSON(w, status, map[string]any{"models": []any{map[string]any{
		"model_name": "fixture-model", "provider_id": "fixture", "model": providerModel,
		"options": map[string]any{}, "levels": levels, "enabled": true, "available": available,
	}}})
}

func startSessionForTest(t *testing.T, runtime interface{ Handler() http.Handler }, sessionID string) {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+sessionID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", status, raw)
	}
}

func waitTestSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("%s did not occur", name)
	}
}
