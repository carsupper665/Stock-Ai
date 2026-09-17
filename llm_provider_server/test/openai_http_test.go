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

func TestOpenAITextGenerationRequiresCredentialAndUsesChatCompletionsContract(t *testing.T) {
	var providerRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("OpenAI request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-openai-fixture" {
			t.Errorf("OpenAI Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			return
		}
		if body["model"] != "gpt-fixture-override" || body["temperature"] != 0.25 ||
			body["max_completion_tokens"] != float64(512) || body["reasoning_effort"] != "low" {
			t.Errorf("OpenAI model/options = %+v", body)
		}
		messages := body["messages"].([]any)
		if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "Be concise." {
			t.Errorf("OpenAI system message = %+v", messages[0])
		}
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"OpenAI answer","reasoning_content":"Provider supplied reasoning"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":21,"completion_tokens":5,"total_tokens":26,"prompt_tokens_details":{"cached_tokens":8}}
		}`))
	}))
	defer provider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createOpenAIProvider(t, server.Handler(), provider.URL+"/v1", "gpt-fixture-default")

	withoutCredential := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"openai-main","messages":[{"role":"user","content":"Hello"}]
	}`))
	assertError(t, withoutCredential, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")
	if providerRequests.Load() != 0 {
		t.Fatalf("OpenAI requests without credential = %d, want 0", providerRequests.Load())
	}

	createdToken := request(t, server.Handler(), http.MethodPost, "/v1/providers/openai-main/tokens", adminToken,
		[]byte(`{"name":"main","token":"sk-openai-fixture"}`))
	if createdToken.Code != http.StatusCreated {
		t.Fatalf("create OpenAI token status = %d, body = %s", createdToken.Code, createdToken.Body.String())
	}
	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"openai-main",
		"model":"gpt-fixture-override",
		"messages":[
			{"role":"system","content":"Be concise."},
			{"role":"user","content":"Hello"}
		],
		"options":{"temperature":0.25,"max_completion_tokens":512,"reasoning_effort":"low"}
	}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("OpenAI Generate status = %d, body = %s", generated.Code, generated.Body.String())
	}
	assertJSON(t, generated.Body.Bytes(), map[string]any{
		"content": "OpenAI answer", "reasoning": "Provider supplied reasoning", "tool_calls": []any{}, "finish_reason": "stop",
		"usage": map[string]any{
			"input_tokens": float64(21), "output_tokens": float64(5),
			"cached_tokens": float64(8), "total_tokens": float64(26),
		},
	})
	if providerRequests.Load() != 1 {
		t.Fatalf("OpenAI requests = %d, want exactly 1", providerRequests.Load())
	}
}

func TestOpenAIToolCallsPreserveOpaqueIDsAndAliasOnlyFunctionNames(t *testing.T) {
	var requestNumber atomic.Int32
	var priceWireName, accountWireName string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-openai-fixture" {
			t.Errorf("OpenAI Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			return
		}
		switch requestNumber.Add(1) {
		case 1:
			tools := body["tools"].([]any)
			priceWireName = tools[0].(map[string]any)["function"].(map[string]any)["name"].(string)
			accountWireName = tools[1].(map[string]any)["function"].(map[string]any)["name"].(string)
			if priceWireName == "market.price" || accountWireName == "account.state" {
				t.Errorf("OpenAI tool names were not wire-aliased: %q %q", priceWireName, accountWireName)
			}
			choice := body["tool_choice"].(map[string]any)
			if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != priceWireName || body["parallel_tool_calls"] != true {
				t.Errorf("OpenAI tool options = %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
					map[string]any{"id": "call.price/opaque:1", "type": "function", "function": map[string]any{"name": priceWireName, "arguments": `{"symbol":"BTCUSDT"}`}},
					map[string]any{"id": "call_account-opaque.2", "type": "function", "function": map[string]any{"name": accountWireName, "arguments": `{}`}},
				}}, "finish_reason": "tool_calls",
			}}, "usage": map[string]any{"prompt_tokens": 30, "completion_tokens": 9, "total_tokens": 39}})
		case 2:
			messages := body["messages"].([]any)
			assistant := messages[1].(map[string]any)
			calls := assistant["tool_calls"].([]any)
			if calls[0].(map[string]any)["id"] != "call.price/opaque:1" ||
				calls[0].(map[string]any)["function"].(map[string]any)["name"] != priceWireName ||
				calls[1].(map[string]any)["id"] != "call_account-opaque.2" ||
				calls[1].(map[string]any)["function"].(map[string]any)["name"] != accountWireName {
				t.Errorf("OpenAI assistant tool history = %+v", assistant)
			}
			if messages[2].(map[string]any)["tool_call_id"] != "call.price/opaque:1" ||
				messages[3].(map[string]any)["tool_call_id"] != "call_account-opaque.2" {
				t.Errorf("OpenAI tool result IDs = %+v", messages)
			}
			if messages[2].(map[string]any)["content"] != "" {
				t.Errorf("OpenAI null tool result content = %#v, want empty string", messages[2].(map[string]any)["content"])
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`))
		default:
			t.Errorf("unexpected OpenAI request")
		}
	}))
	defer provider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createOpenAIProvider(t, server.Handler(), provider.URL+"/v1", "gpt-fixture")
	createOpenAIToken(t, server.Handler())

	first := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"openai-main",
		"messages":[{"role":"user","content":"Check price and account."}],
		"tools":[
			{"name":"market.price","description":"Get price","input_schema":{"type":"object","properties":{"symbol":{"type":"string"}}}},
			{"name":"account.state","description":"Get account","input_schema":{"type":"object"}}
		],
		"options":{"tool_choice":{"type":"function","function":{"name":"market.price"}},"parallel_tool_calls":true}
	}`))
	if first.Code != http.StatusOK {
		t.Fatalf("first OpenAI tool Generate status = %d, body = %s", first.Code, first.Body.String())
	}
	assertJSON(t, first.Body.Bytes(), map[string]any{
		"content": nil, "finish_reason": "tool_call",
		"tool_calls": []any{
			map[string]any{"id": "call.price/opaque:1", "name": "market.price", "arguments": map[string]any{"symbol": "BTCUSDT"}},
			map[string]any{"id": "call_account-opaque.2", "name": "account.state", "arguments": map[string]any{}},
		},
		"usage": map[string]any{"input_tokens": float64(30), "output_tokens": float64(9), "cached_tokens": nil, "total_tokens": float64(39)},
	})

	second := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"openai-main",
		"messages":[
			{"role":"user","content":"Check price and account."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call.price/opaque:1","name":"market.price","arguments":{"symbol":"BTCUSDT"}},
				{"id":"call_account-opaque.2","name":"account.state","arguments":{}}
			]},
			{"role":"tool","content":null,"tool_call_id":"call.price/opaque:1"},
			{"role":"tool","content":"{\"balance\":1000}","tool_call_id":"call_account-opaque.2"}
		],
		"tools":[
			{"name":"market.price","input_schema":{"type":"object"}},
			{"name":"account.state","input_schema":{"type":"object"}}
		]
	}`))
	if second.Code != http.StatusOK {
		t.Fatalf("second OpenAI tool Generate status = %d, body = %s", second.Code, second.Body.String())
	}
	assertJSON(t, second.Body.Bytes(), map[string]any{
		"content": "Done.", "finish_reason": "stop", "tool_calls": []any{}, "usage": nil,
	})
	if requestNumber.Load() != 2 {
		t.Fatalf("OpenAI requests = %d, want exactly 2", requestNumber.Load())
	}
}

func TestOpenAIErrorsAreNormalizedWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name           string
		upstreamStatus int
		upstreamBody   string
		wantStatus     int
		wantCode       string
	}{
		{name: "authentication", upstreamStatus: http.StatusUnauthorized,
			upstreamBody: `{"error":{"type":"invalid_request_error","code":"invalid_api_key"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "AUTHENTICATION_FAILED"},
		{name: "model not found", upstreamStatus: http.StatusNotFound,
			upstreamBody: `{"error":{"type":"invalid_request_error","param":"model","code":"model_not_found"}}`,
			wantStatus:   http.StatusBadRequest, wantCode: "MODEL_NOT_FOUND"},
		{name: "invalid OpenAI request", upstreamStatus: http.StatusBadRequest,
			upstreamBody: `{"error":{"type":"invalid_request_error","param":"temperature","code":null}}`,
			wantStatus:   http.StatusBadRequest, wantCode: "REQUEST_INVALID"},
		{name: "rate limit", upstreamStatus: http.StatusTooManyRequests,
			upstreamBody: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
			wantStatus:   http.StatusTooManyRequests, wantCode: "RATE_LIMITED"},
		{name: "invalid response", upstreamStatus: http.StatusOK,
			upstreamBody: `{"choices":[{"message":{"content":"bad usage"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "provider unavailable", upstreamStatus: http.StatusInternalServerError,
			upstreamBody: `{"error":{"type":"server_error"}}`,
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
			createOpenAIProvider(t, server.Handler(), provider.URL+"/v1", "gpt-fixture")
			createOpenAIToken(t, server.Handler())

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"openai-main","messages":[{"role":"user","content":"Hello"}]}`))
			assertError(t, response, test.wantStatus, test.wantCode)
			if calls.Load() != 1 {
				t.Fatalf("OpenAI calls = %d, want exactly 1", calls.Load())
			}
		})
	}
}

func TestOpenAIFinishReasonsAndRefusalsNormalizeWithoutInventingText(t *testing.T) {
	for _, test := range []struct {
		name         string
		upstreamBody string
		wantStatus   int
		wantContent  any
		wantFinish   string
		wantUsage    any
	}{
		{name: "length", upstreamBody: `{"choices":[{"index":0,"message":{"role":"assistant","content":"Partial"},"finish_reason":"length"}]}`,
			wantStatus: http.StatusOK, wantContent: "Partial", wantFinish: "length"},
		{name: "length after reasoning budget with no visible output", upstreamBody: `{"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"length"}],"usage":{"prompt_tokens":8,"completion_tokens":64,"total_tokens":72}}`,
			wantStatus: http.StatusOK, wantContent: nil, wantFinish: "length",
			wantUsage: map[string]any{"input_tokens": float64(8), "output_tokens": float64(64), "cached_tokens": nil, "total_tokens": float64(72)}},
		{name: "content filtered with omitted content", upstreamBody: `{"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"content_filter"}]}`,
			wantStatus: http.StatusOK, wantContent: nil, wantFinish: "content_filter"},
		{name: "refusal text", upstreamBody: `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"I cannot help with that."},"finish_reason":"stop"}]}`,
			wantStatus: http.StatusOK, wantContent: "I cannot help with that.", wantFinish: "stop"},
		{name: "missing assistant role", upstreamBody: `{"choices":[{"index":0,"message":{"content":"bad"},"finish_reason":"stop"}]}`,
			wantStatus: http.StatusBadGateway},
		{name: "multiple choices", upstreamBody: `{"choices":[
			{"index":0,"message":{"role":"assistant","content":"one"},"finish_reason":"stop"},
			{"index":1,"message":{"role":"assistant","content":"two"},"finish_reason":"stop"}]}`,
			wantStatus: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.upstreamBody))
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createOpenAIProvider(t, server.Handler(), provider.URL+"/v1", "gpt-fixture")
			createOpenAIToken(t, server.Handler())

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"openai-main","messages":[{"role":"user","content":"Hello"}]}`))
			if test.wantStatus != http.StatusOK {
				assertError(t, response, test.wantStatus, "PROVIDER_RESPONSE_INVALID")
				return
			}
			if response.Code != http.StatusOK {
				t.Fatalf("OpenAI Generate status = %d, body = %s", response.Code, response.Body.String())
			}
			assertJSON(t, response.Body.Bytes(), map[string]any{
				"content": test.wantContent, "finish_reason": test.wantFinish, "tool_calls": []any{}, "usage": test.wantUsage,
			})
		})
	}
}

func TestOpenAINullBoundariesDoNotRelaxCompatibleToolOrLengthSemantics(t *testing.T) {
	t.Run("compatible tool result remains null on wire", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode compatible request: %v", err)
				return
			}
			messages := body["messages"].([]any)
			if content := messages[2].(map[string]any)["content"]; content != nil {
				t.Errorf("compatible null tool result content = %#v, want null", content)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Done."},"finish_reason":"stop"}]}`))
		}))
		defer provider.Close()
		server := openTestServer(t, time.Second)
		defer server.Close()
		createProvider(t, server.Handler(), provider.URL+"/v1", "compatible-model", true)

		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
			"provider":"fixture",
			"messages":[
				{"role":"user","content":"Check."},
				{"role":"assistant","content":null,"tool_calls":[{"id":"opaque/id","name":"market.price","arguments":{}}]},
				{"role":"tool","content":null,"tool_call_id":"opaque/id"}
			],
			"tools":[{"name":"market.price","input_schema":{"type":"object"}}]
		}`))
		if response.Code != http.StatusOK {
			t.Fatalf("compatible Generate status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("compatible length still requires content", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":null},"finish_reason":"length"}],"usage":{"prompt_tokens":8,"completion_tokens":64,"total_tokens":72}}`))
		}))
		defer provider.Close()
		server := openTestServer(t, time.Second)
		defer server.Close()
		createProvider(t, server.Handler(), provider.URL+"/v1", "compatible-model", true)

		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"Think."}]}`))
		assertError(t, response, http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID")
	})
}

func TestOpenAIRejectsOptionsThatChangeTheNormalizedResponseShape(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createOpenAIProvider(t, server.Handler(), provider.URL+"/v1", "gpt-fixture")
	createOpenAIToken(t, server.Handler())

	for _, options := range []string{
		`{"stream":true}`,
		`{"stream_options":{"include_usage":true}}`,
		`{"n":2}`,
		`{"modalities":["audio"],"audio":{"format":"mp3","voice":"alloy"}}`,
		`{"functions":[]}`,
		`{"function_call":"auto"}`,
		`{"unknown_openai_option":true}`,
		`{"tool_choice":{"type":"function","function":{"name":"undeclared.tool"}}}`,
		`{"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[]}}}`,
	} {
		body := []byte(`{"provider":"openai-main","messages":[{"role":"user","content":"Hello"}],"options":` + options + `}`)
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body),
			http.StatusBadRequest, "REQUEST_INVALID")
	}
	if calls.Load() != 0 {
		t.Fatalf("OpenAI calls for unsupported options = %d, want 0", calls.Load())
	}
}

func TestOpenAITimeoutAndCallerCancellationStopTheSingleUpstreamRequest(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
		}))
		defer provider.Close()
		server := openTestServer(t, 20*time.Millisecond)
		defer server.Close()
		createOpenAIProvider(t, server.Handler(), provider.URL+"/v1", "gpt-fixture")
		createOpenAIToken(t, server.Handler())

		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"openai-main","messages":[{"role":"user","content":"Hello"}]}`))
		assertError(t, response, http.StatusGatewayTimeout, "PROVIDER_TIMEOUT")
		select {
		case <-started:
		default:
			t.Fatal("OpenAI timeout request never started")
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
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[`))
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(upstreamCancelled)
		}))
		defer provider.Close()
		server := openTestServer(t, time.Second)
		defer server.Close()
		createOpenAIProvider(t, server.Handler(), provider.URL+"/v1", "gpt-fixture")
		createOpenAIToken(t, server.Handler())

		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewBufferString(
			`{"provider":"openai-main","messages":[{"role":"user","content":"Hello"}]}`)).WithContext(ctx)
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
			t.Fatal("cancelled OpenAI request never started")
		}
		cancel()
		select {
		case <-upstreamCancelled:
		case <-time.After(time.Second):
			t.Fatal("caller cancellation did not reach OpenAI")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("OpenAI Generate did not return after caller cancellation")
		}
		assertError(t, response, http.StatusBadGateway, "PROVIDER_UNAVAILABLE")
		if calls.Load() != 1 {
			t.Fatalf("cancelled OpenAI calls = %d, want exactly 1", calls.Load())
		}
	})
}

func createOpenAIProvider(t *testing.T, handler http.Handler, baseURL, defaultModel string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": "openai-main", "name": "OpenAI", "type": "openai",
		"base_url": baseURL, "default_model": defaultModel, "config": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodPost, "/v1/providers", adminToken, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create OpenAI provider status = %d, body = %s", response.Code, response.Body.String())
	}
}

func createOpenAIToken(t *testing.T, handler http.Handler) {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v1/providers/openai-main/tokens", adminToken,
		[]byte(`{"name":"main","token":"sk-openai-fixture"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("create OpenAI token status = %d, body = %s", response.Code, response.Body.String())
	}
}
