package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const geminiTestModel = "gemini-2.5-flash-lite"

func TestGeminiTextUsesV1BetaSystemOptionsAuthenticationAndNullableUsage(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	var providerRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/"+geminiTestModel+":generateContent" {
			t.Errorf("Gemini request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "gemini-fixture-key" {
			t.Errorf("Gemini x-goog-api-key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Gemini Authorization = %q, want none", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gemini request: %v", err)
			return
		}
		system := body["systemInstruction"].(map[string]any)["parts"].([]any)
		if len(system) != 2 || system[0].(map[string]any)["text"] != "Be concise." ||
			system[1].(map[string]any)["text"] != "Use UTC." {
			t.Errorf("Gemini systemInstruction = %+v", body["systemInstruction"])
		}
		contents := body["contents"].([]any)
		if len(contents) != 1 || contents[0].(map[string]any)["role"] != "user" ||
			contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] != "Hello" {
			t.Errorf("Gemini contents = %+v", contents)
		}
		config := body["generationConfig"].(map[string]any)
		if config["temperature"] != 0.25 || config["maxOutputTokens"] != float64(128) {
			t.Errorf("Gemini generationConfig = %+v", config)
		}
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"Gemini answer"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4,"cachedContentTokenCount":3,"totalTokenCount":16}
		}`))
	}))
	defer provider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)

	withoutCredential := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}]}`))
	assertError(t, withoutCredential, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")
	unsupportedWithoutCredential := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"gemini-main","model":"gemini-3.8-flash","messages":[{"role":"user","content":"Hello"}]}`))
	assertError(t, unsupportedWithoutCredential, http.StatusBadRequest, "MODEL_NOT_FOUND")
	if providerRequests.Load() != 0 {
		t.Fatalf("Gemini requests without credential = %d, want 0", providerRequests.Load())
	}

	createGeminiToken(t, server.Handler())
	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"gemini-main",
		"messages":[
			{"role":"system","content":"Be concise."},
			{"role":"user","content":"Hello"},
			{"role":"system","content":"Use UTC."}
		],
		"options":{"temperature":0.25,"maxOutputTokens":128}
	}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("Gemini Generate status = %d, body = %s", generated.Code, generated.Body.String())
	}
	assertJSON(t, generated.Body.Bytes(), map[string]any{
		"content": "Gemini answer", "tool_calls": []any{}, "finish_reason": "stop",
		"usage": map[string]any{
			"input_tokens": float64(12), "output_tokens": float64(4),
			"cached_tokens": float64(3), "total_tokens": float64(16),
		},
	})
	if providerRequests.Load() != 1 {
		t.Fatalf("Gemini requests = %d, want exactly 1", providerRequests.Load())
	}
}

func TestGeminiAgentGatewayContractCorrelatesMultipleSameNameCallsAndResults(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	var requestNumber atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gemini request: %v", err)
			return
		}
		switch requestNumber.Add(1) {
		case 1:
			declarations := body["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
			declaration := declarations[0].(map[string]any)
			if declaration["name"] != "get_market_snapshot" || declaration["description"] != "Get market" ||
				declaration["parameters"].(map[string]any)["type"] != "object" {
				t.Errorf("Gemini function declarations = %+v", declarations)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{"role": "model", "parts": []any{
						map[string]any{"text": "I will check both."},
						map[string]any{"functionCall": map[string]any{"id": "gemini/call:one", "name": "get_market_snapshot", "args": map[string]any{"symbol": "BTCUSDT"}}},
						map[string]any{"functionCall": map[string]any{"id": "gemini/call:two", "name": "get_market_snapshot", "args": map[string]any{"symbol": "ETHUSDT"}}},
					}}, "finishReason": "STOP",
				}},
			})
		case 2:
			contents := body["contents"].([]any)
			if len(contents) != 3 {
				t.Errorf("Gemini contents count = %d, want 3", len(contents))
				return
			}
			modelParts := contents[1].(map[string]any)["parts"].([]any)
			firstCall := modelParts[1].(map[string]any)["functionCall"].(map[string]any)
			secondCall := modelParts[2].(map[string]any)["functionCall"].(map[string]any)
			if firstCall["id"] != "gemini/call:one" || secondCall["id"] != "gemini/call:two" ||
				firstCall["name"] != secondCall["name"] {
				t.Errorf("Gemini model call history = %+v", modelParts)
			}
			resultParts := contents[2].(map[string]any)["parts"].([]any)
			if len(resultParts) != 3 {
				t.Errorf("Gemini result parts = %+v", resultParts)
				return
			}
			firstResult := resultParts[0].(map[string]any)["functionResponse"].(map[string]any)
			secondResult := resultParts[1].(map[string]any)["functionResponse"].(map[string]any)
			if firstResult["id"] != "gemini/call:one" || secondResult["id"] != "gemini/call:two" ||
				firstResult["name"] != "get_market_snapshot" || secondResult["name"] != "get_market_snapshot" ||
				firstResult["response"].(map[string]any)["price"] != float64(60000) ||
				secondResult["response"].(map[string]any)["price"] != float64(3000) {
				t.Errorf("Gemini correlated function results = %+v", resultParts)
			}
			if resultParts[2].(map[string]any)["text"] != "Compare." {
				t.Errorf("Gemini follow-up text = %+v", resultParts[2])
			}
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"BTC is higher."}]},"finishReason":"STOP"}]}`))
		default:
			t.Errorf("unexpected Gemini request")
		}
	}))
	defer provider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
	createGeminiToken(t, server.Handler())

	first := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"gemini-main",
		"messages":[{"role":"user","content":"Compare BTC and ETH."}],
		"tools":[{"name":"get_market_snapshot","description":"Get market","input_schema":{"type":"object","properties":{"symbol":{"type":"string"}}}}]
	}`))
	if first.Code != http.StatusOK {
		t.Fatalf("first Gemini tool Generate status = %d, body = %s", first.Code, first.Body.String())
	}
	assertJSON(t, first.Body.Bytes(), map[string]any{
		"content": "I will check both.", "finish_reason": "tool_call", "usage": nil,
		"tool_calls": []any{
			map[string]any{"id": "gemini/call:one", "name": "get_market_snapshot", "arguments": map[string]any{"symbol": "BTCUSDT"}},
			map[string]any{"id": "gemini/call:two", "name": "get_market_snapshot", "arguments": map[string]any{"symbol": "ETHUSDT"}},
		},
	})

	second := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"gemini-main",
		"messages":[
			{"role":"user","content":"Compare BTC and ETH."},
			{"role":"assistant","content":"I will check both.","tool_calls":[
				{"id":"gemini/call:one","name":"get_market_snapshot","arguments":{"symbol":"BTCUSDT"}},
				{"id":"gemini/call:two","name":"get_market_snapshot","arguments":{"symbol":"ETHUSDT"}}
			]},
			{"role":"tool","content":"{\"price\":60000}","tool_call_id":"gemini/call:one"},
			{"role":"tool","content":"{\"price\":3000}","tool_call_id":"gemini/call:two"},
			{"role":"user","content":"Compare."}
		],
		"tools":[{"name":"get_market_snapshot","description":"Get market","input_schema":{"type":"object"}}]
	}`))
	if second.Code != http.StatusOK {
		t.Fatalf("second Gemini tool Generate status = %d, body = %s", second.Code, second.Body.String())
	}
	assertJSON(t, second.Body.Bytes(), map[string]any{
		"content": "BTC is higher.", "finish_reason": "stop", "tool_calls": []any{}, "usage": nil,
	})
	if requestNumber.Load() != 2 {
		t.Fatalf("Gemini requests = %d, want exactly 2", requestNumber.Load())
	}
}

func TestGeminiCatalogLevelOptionsAndUnsupportedInputsFailBeforeProviderIO(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", `[{
		"model_name":"gemini-fixture","provider_id":"gemini-main","model":"gemini-2.5-flash-lite",
		"options":{"temperature":0.1},"levels":{"high":{"maxOutputTokens":64}}
	}]`)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gemini request: %v", err)
			return
		}
		config := body["generationConfig"].(map[string]any)
		if config["temperature"] != 0.1 || config["maxOutputTokens"] != float64(64) {
			t.Errorf("Gemini catalog level generationConfig = %+v", config)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{}}`))
	}))
	defer provider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
	createGeminiToken(t, server.Handler())

	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}],"options":{"model_level":"high"}}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("Gemini catalog Generate status = %d, body = %s", generated.Code, generated.Body.String())
	}
	assertJSON(t, generated.Body.Bytes(), map[string]any{
		"content": "ok", "tool_calls": []any{}, "finish_reason": "stop",
		"usage": map[string]any{"input_tokens": nil, "output_tokens": nil, "cached_tokens": nil, "total_tokens": nil},
	})

	unsupportedOption := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}],"options":{"stream":true}}`))
	assertError(t, unsupportedOption, http.StatusBadRequest, "REQUEST_INVALID")
	unsupportedModel := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"gemini-main","model":"gemini-3.8-flash","messages":[{"role":"user","content":"Hello"}]}`))
	assertError(t, unsupportedModel, http.StatusBadRequest, "MODEL_NOT_FOUND")
	invalidToolResult := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"gemini-main","messages":[
			{"role":"user","content":"Hello"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","name":"tool","arguments":{}}]},
			{"role":"tool","content":"not-json","tool_call_id":"call-1"}
		]
	}`))
	assertError(t, invalidToolResult, http.StatusBadRequest, "REQUEST_INVALID")
	missingConversation := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"gemini-main","messages":[{"role":"system","content":"Only an instruction"}]}`))
	assertError(t, missingConversation, http.StatusBadRequest, "REQUEST_INVALID")
	if calls.Load() != 1 {
		t.Fatalf("Gemini calls = %d, want only the valid level request", calls.Load())
	}
}

func TestGeminiOpaqueThoughtStateAndMalformedResponsesFailClosedWithoutInventedIDs(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	responses := []string{
		`not-json`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]} {}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"tool","args":{}},"thoughtSignature":"opaque"}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"hidden","thought":true}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool","args":{}}}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"tool","args":[]}}]},"finishReason":"STOP"}]}`,
		`{"candidates":[]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"thoughtsTokenCount":1}}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":-1}}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
	}
	for index, upstreamBody := range responses {
		t.Run(string(rune('A'+index)), func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(upstreamBody))
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
			createGeminiToken(t, server.Handler())

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}],"tools":[{"name":"tool","input_schema":{"type":"object"}}]}`))
			assertError(t, response, http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID")
			if calls.Load() != 1 {
				t.Fatalf("Gemini calls = %d, want exactly 1 without retry", calls.Load())
			}
		})
	}
}

func TestGeminiFinishReasonsNormalizeWithoutInventingContent(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	for _, test := range []struct {
		name         string
		upstreamBody string
		wantContent  any
		wantFinish   string
		wantUsage    any
	}{
		{name: "max tokens with partial text",
			upstreamBody: `{"candidates":[{"content":{"role":"model","parts":[{"text":"Partial"}]},"finishReason":"MAX_TOKENS"}]}`,
			wantContent:  "Partial", wantFinish: "length"},
		{name: "safety without content",
			upstreamBody: `{"candidates":[{"finishReason":"SAFETY"}]}`,
			wantContent:  nil, wantFinish: "content_filter"},
		{name: "blocked prompt without candidates",
			upstreamBody: `{"promptFeedback":{"blockReason":"BLOCKLIST","safetyRatings":[]},"usageMetadata":{"promptTokenCount":4,"totalTokenCount":4}}`,
			wantContent:  nil, wantFinish: "content_filter", wantUsage: map[string]any{
				"input_tokens": float64(4), "output_tokens": nil, "cached_tokens": nil, "total_tokens": float64(4),
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.upstreamBody))
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
			createGeminiToken(t, server.Handler())

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}]}`))
			if response.Code != http.StatusOK {
				t.Fatalf("Gemini Generate status = %d, body = %s", response.Code, response.Body.String())
			}
			assertJSON(t, response.Body.Bytes(), map[string]any{
				"content": test.wantContent, "tool_calls": []any{}, "finish_reason": test.wantFinish,
				"usage": test.wantUsage,
			})
		})
	}
}

func TestGeminiHTTPErrorMappingAndNoRetry(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	for _, test := range []struct {
		name           string
		upstreamStatus int
		upstreamBody   string
		wantStatus     int
		wantCode       string
	}{
		{name: "authentication", upstreamStatus: http.StatusUnauthorized,
			upstreamBody: `{"error":{"code":401,"message":"bad key","status":"UNAUTHENTICATED"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "AUTHENTICATION_FAILED"},
		{name: "permission", upstreamStatus: http.StatusForbidden,
			upstreamBody: `{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "AUTHENTICATION_FAILED"},
		{name: "rate limit", upstreamStatus: http.StatusTooManyRequests,
			upstreamBody: `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`,
			wantStatus:   http.StatusTooManyRequests, wantCode: "RATE_LIMITED"},
		{name: "model unavailable", upstreamStatus: http.StatusNotFound,
			upstreamBody: `{"error":{"code":404,"message":"missing","status":"NOT_FOUND"}}`,
			wantStatus:   http.StatusBadRequest, wantCode: "MODEL_NOT_FOUND"},
		{name: "invalid request", upstreamStatus: http.StatusBadRequest,
			upstreamBody: `{"error":{"code":400,"message":"invalid","status":"INVALID_ARGUMENT"}}`,
			wantStatus:   http.StatusBadRequest, wantCode: "REQUEST_INVALID"},
		{name: "unavailable", upstreamStatus: http.StatusInternalServerError,
			upstreamBody: `{"error":{"code":500,"message":"failed","status":"INTERNAL"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.upstreamStatus)
				_, _ = w.Write([]byte(test.upstreamBody))
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
			createGeminiToken(t, server.Handler())

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}]}`))
			assertError(t, response, test.wantStatus, test.wantCode)
			if calls.Load() != 1 {
				t.Fatalf("Gemini calls = %d, want exactly 1 without retry", calls.Load())
			}
		})
	}
}

func TestGeminiTimeoutAndCallerCancellationStopTheSingleUpstreamRequest(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	t.Run("timeout", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
		}))
		defer provider.Close()
		server := openTestServer(t, 20*time.Millisecond)
		defer server.Close()
		createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
		createGeminiToken(t, server.Handler())

		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}]}`))
		assertError(t, response, http.StatusGatewayTimeout, "PROVIDER_TIMEOUT")
		select {
		case <-started:
		default:
			t.Fatal("Gemini timeout request never started")
		}
		close(release)
		released = true
	})

	t.Run("caller cancellation", func(t *testing.T) {
		started := make(chan struct{})
		upstreamCancelled := make(chan struct{})
		var calls atomic.Int32
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"candidates":[`))
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(upstreamCancelled)
		}))
		defer provider.Close()
		server := openTestServer(t, time.Second)
		defer server.Close()
		createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
		createGeminiToken(t, server.Handler())

		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewBufferString(
			`{"provider":"gemini-main","messages":[{"role":"user","content":"Hello"}]}`)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+runtimeToken)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			server.Handler().ServeHTTP(response, req)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("cancelled Gemini request never started")
		}
		cancel()
		select {
		case <-upstreamCancelled:
		case <-time.After(time.Second):
			t.Fatal("caller cancellation did not reach Gemini")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Gemini Generate did not return after cancellation")
		}
		assertError(t, response, http.StatusBadGateway, "PROVIDER_UNAVAILABLE")
		if calls.Load() != 1 {
			t.Fatalf("cancelled Gemini calls = %d, want exactly 1", calls.Load())
		}
	})
}

func createGeminiProvider(t *testing.T, handler http.Handler, baseURL, defaultModel string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": "gemini-main", "name": "Gemini", "type": "gemini",
		"base_url": baseURL, "default_model": defaultModel, "config": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodPost, "/v1/providers", adminToken, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Gemini provider status = %d, body = %s", response.Code, response.Body.String())
	}
}

func createGeminiToken(t *testing.T, handler http.Handler) {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v1/providers/gemini-main/tokens", adminToken,
		[]byte(`{"name":"main","token":"gemini-fixture-key"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("create Gemini token status = %d, body = %s", response.Code, response.Body.String())
	}
}
