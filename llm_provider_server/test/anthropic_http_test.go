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

func TestAnthropicTextGenerationRequiresCredentialAndUsesMessagesContract(t *testing.T) {
	var providerRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := providerRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("Anthropic request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-ant-fixture" {
			t.Errorf("Anthropic x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("Anthropic anthropic-version = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Anthropic Authorization = %q, want none", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Anthropic request: %v", err)
			return
		}
		wantMaxTokens := float64(16000)
		if requestNumber == 2 {
			wantMaxTokens = 512
		}
		if body["model"] != "claude-fixture-override" || body["max_tokens"] != wantMaxTokens || body["temperature"] != 0.25 {
			t.Errorf("Anthropic model/options = %+v", body)
		}
		if thinking, _ := body["thinking"].(map[string]any); thinking["type"] != "adaptive" {
			t.Errorf("Anthropic thinking = %+v", body["thinking"])
		}
		if output, _ := body["output_config"].(map[string]any); output["effort"] != "low" {
			t.Errorf("Anthropic output_config = %+v", body["output_config"])
		}
		system := body["system"].([]any)
		if len(system) != 1 || system[0].(map[string]any)["type"] != "text" || system[0].(map[string]any)["text"] != "Be concise." {
			t.Errorf("Anthropic system = %+v", body["system"])
		}
		messages := body["messages"].([]any)
		userContent := messages[0].(map[string]any)["content"].([]any)
		if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" ||
			userContent[0].(map[string]any)["type"] != "text" || userContent[0].(map[string]any)["text"] != "Hello" {
			t.Errorf("Anthropic messages = %+v", messages)
		}
		if _, hasTools := body["tools"]; hasTools {
			t.Errorf("Anthropic tools sent without declared tools: %+v", body["tools"])
		}
		_, _ = w.Write([]byte(`{
			"id":"msg_01","type":"message","role":"assistant","model":"claude-fixture-override",
			"content":[{"type":"thinking","thinking":"Provider supplied analysis"},{"type":"text","text":"Anthropic answer"}],
			"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":3,"cache_read_input_tokens":8}
		}`))
	}))
	defer provider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture-default")

	withoutCredential := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"anthropic-main","messages":[{"role":"user","content":"Hello"}]
	}`))
	assertError(t, withoutCredential, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE")
	if providerRequests.Load() != 0 {
		t.Fatalf("Anthropic requests without credential = %d, want 0", providerRequests.Load())
	}

	createAnthropicToken(t, server.Handler())
	generated := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"anthropic-main",
		"model":"claude-fixture-override",
		"messages":[
			{"role":"system","content":"Be concise."},
			{"role":"user","content":"Hello"}
		],
		"options":{"temperature":0.25,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}
	}`))
	if generated.Code != http.StatusOK {
		t.Fatalf("Anthropic Generate status = %d, body = %s", generated.Code, generated.Body.String())
	}
	assertJSON(t, generated.Body.Bytes(), map[string]any{
		"content": "Anthropic answer", "reasoning": "Provider supplied analysis", "tool_calls": []any{}, "finish_reason": "stop",
		"usage": map[string]any{
			"input_tokens": float64(21), "output_tokens": float64(5),
			"cached_tokens": float64(8), "total_tokens": nil,
		},
	})

	overridden := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"anthropic-main",
		"model":"claude-fixture-override",
		"messages":[{"role":"user","content":"Hello"},{"role":"system","content":"Be concise."}],
		"options":{"max_tokens":512,"temperature":0.25,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}
	}`))
	if overridden.Code != http.StatusOK {
		t.Fatalf("Anthropic max_tokens override status = %d, body = %s", overridden.Code, overridden.Body.String())
	}
	if providerRequests.Load() != 2 {
		t.Fatalf("Anthropic requests = %d, want exactly 2", providerRequests.Load())
	}
}

func TestAnthropicToolUseRoundTripPreservesIDsAndAliasesNames(t *testing.T) {
	var requestNumber atomic.Int32
	var priceWireName, accountWireName string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "sk-ant-fixture" {
			t.Errorf("Anthropic x-api-key = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Anthropic request: %v", err)
			return
		}
		switch requestNumber.Add(1) {
		case 1:
			tools := body["tools"].([]any)
			priceWireName = tools[0].(map[string]any)["name"].(string)
			accountWireName = tools[1].(map[string]any)["name"].(string)
			if priceWireName == "market.price" || accountWireName == "account.state" {
				t.Errorf("Anthropic tool names were not wire-aliased: %q %q", priceWireName, accountWireName)
			}
			if tools[0].(map[string]any)["description"] != "Get price" || tools[0].(map[string]any)["input_schema"] == nil {
				t.Errorf("Anthropic tool schema = %+v", tools[0])
			}
			choice := body["tool_choice"].(map[string]any)
			if choice["type"] != "tool" || choice["name"] != priceWireName || choice["disable_parallel_tool_use"] != false {
				t.Errorf("Anthropic tool_choice = %+v", choice)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_02", "type": "message", "role": "assistant", "model": "claude-fixture",
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "", "signature": "sig"},
					map[string]any{"type": "text", "text": "I will check both."},
					map[string]any{"type": "tool_use", "id": "toolu_01price", "name": priceWireName, "input": map[string]any{"symbol": "BTCUSDT"}},
					map[string]any{"type": "tool_use", "id": "toolu_02account", "name": accountWireName, "input": map[string]any{}},
				},
				"stop_reason": "tool_use", "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 30, "output_tokens": 9},
			})
		case 2:
			messages := body["messages"].([]any)
			if len(messages) != 3 {
				t.Errorf("Anthropic message count = %d, want 3 after merging tool results and the follow-up text", len(messages))
				return
			}
			assistant := messages[1].(map[string]any)
			blocks := assistant["content"].([]any)
			if assistant["role"] != "assistant" || len(blocks) != 3 ||
				blocks[0].(map[string]any)["text"] != "I will check both." ||
				blocks[1].(map[string]any)["id"] != "toolu_01price" || blocks[1].(map[string]any)["name"] != priceWireName ||
				blocks[1].(map[string]any)["input"].(map[string]any)["symbol"] != "BTCUSDT" ||
				blocks[2].(map[string]any)["id"] != "toolu_02account" || blocks[2].(map[string]any)["name"] != accountWireName {
				t.Errorf("Anthropic assistant history = %+v", assistant)
			}
			results := messages[2].(map[string]any)
			resultBlocks := results["content"].([]any)
			if results["role"] != "user" || len(resultBlocks) != 3 {
				t.Errorf("Anthropic tool result turn = %+v", results)
				return
			}
			first := resultBlocks[0].(map[string]any)
			second := resultBlocks[1].(map[string]any)
			followUp := resultBlocks[2].(map[string]any)
			if first["type"] != "tool_result" || first["tool_use_id"] != "toolu_01price" {
				t.Errorf("Anthropic first tool result = %+v", first)
			}
			if _, hasContent := first["content"]; hasContent {
				t.Errorf("Anthropic null tool result content = %#v, want omitted", first["content"])
			}
			if second["tool_use_id"] != "toolu_02account" || second["content"] != `{"balance":1000}` {
				t.Errorf("Anthropic second tool result = %+v", second)
			}
			if followUp["type"] != "text" || followUp["text"] != "Summarize." {
				t.Errorf("Anthropic follow-up text = %+v", followUp)
			}
			_, _ = w.Write([]byte(`{"id":"msg_03","type":"message","role":"assistant","content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}`))
		default:
			t.Errorf("unexpected Anthropic request")
		}
	}))
	defer provider.Close()

	server := openTestServer(t, time.Second)
	defer server.Close()
	createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture")
	createAnthropicToken(t, server.Handler())

	first := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"anthropic-main",
		"messages":[{"role":"user","content":"Check price and account."}],
		"tools":[
			{"name":"market.price","description":"Get price","input_schema":{"type":"object","properties":{"symbol":{"type":"string"}}}},
			{"name":"account.state","description":"Get account","input_schema":{"type":"object"}}
		],
		"options":{"tool_choice":{"type":"tool","name":"market.price","disable_parallel_tool_use":false}}
	}`))
	if first.Code != http.StatusOK {
		t.Fatalf("first Anthropic tool Generate status = %d, body = %s", first.Code, first.Body.String())
	}
	assertJSON(t, first.Body.Bytes(), map[string]any{
		"content": "I will check both.", "finish_reason": "tool_call",
		"tool_calls": []any{
			map[string]any{"id": "toolu_01price", "name": "market.price", "arguments": map[string]any{"symbol": "BTCUSDT"}},
			map[string]any{"id": "toolu_02account", "name": "account.state", "arguments": map[string]any{}},
		},
		"usage": map[string]any{"input_tokens": float64(30), "output_tokens": float64(9), "cached_tokens": nil, "total_tokens": nil},
	})

	second := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"anthropic-main",
		"messages":[
			{"role":"user","content":"Check price and account."},
			{"role":"assistant","content":"I will check both.","tool_calls":[
				{"id":"toolu_01price","name":"market.price","arguments":{"symbol":"BTCUSDT"}},
				{"id":"toolu_02account","name":"account.state","arguments":{}}
			]},
			{"role":"tool","content":null,"tool_call_id":"toolu_01price"},
			{"role":"tool","content":"{\"balance\":1000}","tool_call_id":"toolu_02account"},
			{"role":"user","content":"Summarize."}
		],
		"tools":[
			{"name":"market.price","input_schema":{"type":"object"}},
			{"name":"account.state","input_schema":{"type":"object"}}
		]
	}`))
	if second.Code != http.StatusOK {
		t.Fatalf("second Anthropic tool Generate status = %d, body = %s", second.Code, second.Body.String())
	}
	assertJSON(t, second.Body.Bytes(), map[string]any{
		"content": "Done.", "finish_reason": "stop", "tool_calls": []any{}, "usage": nil,
	})
	if requestNumber.Load() != 2 {
		t.Fatalf("Anthropic requests = %d, want exactly 2", requestNumber.Load())
	}
}

func TestAnthropicErrorsAreNormalizedWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name           string
		upstreamStatus int
		upstreamBody   string
		wantStatus     int
		wantCode       string
	}{
		{name: "authentication", upstreamStatus: http.StatusUnauthorized,
			upstreamBody: `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "AUTHENTICATION_FAILED"},
		{name: "permission", upstreamStatus: http.StatusForbidden,
			upstreamBody: `{"type":"error","error":{"type":"permission_error","message":"not allowed"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "AUTHENTICATION_FAILED"},
		{name: "model not found", upstreamStatus: http.StatusNotFound,
			upstreamBody: `{"type":"error","error":{"type":"not_found_error","message":"model: claude-missing"},"request_id":"req_1"}`,
			wantStatus:   http.StatusBadRequest, wantCode: "MODEL_NOT_FOUND"},
		{name: "other not found", upstreamStatus: http.StatusNotFound,
			upstreamBody: `{"type":"error","error":{"type":"not_found_error","message":"Not Found"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_UNAVAILABLE"},
		{name: "invalid request", upstreamStatus: http.StatusBadRequest,
			upstreamBody: `{"type":"error","error":{"type":"invalid_request_error","message":"messages: first message must use the user role"}}`,
			wantStatus:   http.StatusBadRequest, wantCode: "REQUEST_INVALID"},
		{name: "request too large", upstreamStatus: http.StatusRequestEntityTooLarge,
			upstreamBody: `{"type":"error","error":{"type":"request_too_large","message":"too large"}}`,
			wantStatus:   http.StatusBadRequest, wantCode: "REQUEST_INVALID"},
		{name: "rate limit", upstreamStatus: http.StatusTooManyRequests,
			upstreamBody: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			wantStatus:   http.StatusTooManyRequests, wantCode: "RATE_LIMITED"},
		{name: "overloaded", upstreamStatus: 529,
			upstreamBody: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_UNAVAILABLE"},
		{name: "api error", upstreamStatus: http.StatusInternalServerError,
			upstreamBody: `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_UNAVAILABLE"},
		{name: "negative usage", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"bad"}],"stop_reason":"end_turn","usage":{"input_tokens":-1,"output_tokens":1}}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "unknown block", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}],"stop_reason":"end_turn"}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "trailing json", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"} {}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "missing stop reason", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":null}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "pause turn", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"pause_turn"}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "tool_use stop without tool block", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"tool_use"}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "end_turn with tool block", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"market_price","input":{}}],"stop_reason":"end_turn"}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "truncated tool block", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"market_price","input":{}}],"stop_reason":"max_tokens"}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "non-object tool input", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"market_price","input":"BTC"}],"stop_reason":"tool_use"}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
		{name: "wrong role", upstreamStatus: http.StatusOK,
			upstreamBody: `{"type":"message","role":"user","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`,
			wantStatus:   http.StatusBadGateway, wantCode: "PROVIDER_RESPONSE_INVALID"},
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
			createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture")
			createAnthropicToken(t, server.Handler())

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"anthropic-main","messages":[{"role":"user","content":"Hello"}]}`))
			assertError(t, response, test.wantStatus, test.wantCode)
			if calls.Load() != 1 {
				t.Fatalf("Anthropic calls = %d, want exactly 1", calls.Load())
			}
		})
	}
}

func TestAnthropicStopReasonsAndUsageNormalizeWithoutInventingText(t *testing.T) {
	for _, test := range []struct {
		name         string
		upstreamBody string
		wantContent  any
		wantFinish   string
		wantUsage    any
	}{
		{name: "empty end_turn", upstreamBody: `{"type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`,
			wantContent: nil, wantFinish: "stop",
			wantUsage: map[string]any{"input_tokens": float64(5), "output_tokens": float64(2), "cached_tokens": nil, "total_tokens": nil}},
		{name: "max_tokens with only thinking", upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"abc"}],"stop_reason":"max_tokens","usage":{"input_tokens":8,"output_tokens":64,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`,
			wantContent: nil, wantFinish: "length",
			wantUsage: map[string]any{"input_tokens": float64(8), "output_tokens": float64(64), "cached_tokens": float64(0), "total_tokens": nil}},
		{name: "max_tokens with partial text", upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"Partial"}],"stop_reason":"max_tokens"}`,
			wantContent: "Partial", wantFinish: "length"},
		{name: "stop_sequence", upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"Until here"}],"stop_reason":"stop_sequence","stop_sequence":"END"}`,
			wantContent: "Until here", wantFinish: "stop"},
		{name: "refusal without output", upstreamBody: `{"type":"message","role":"assistant","content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"}}`,
			wantContent: nil, wantFinish: "content_filter"},
		{name: "context window exceeded", upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"text","text":"Truncated"}],"stop_reason":"model_context_window_exceeded"}`,
			wantContent: "Truncated", wantFinish: "length"},
		{name: "text blocks concatenate and thinking is ignored", upstreamBody: `{"type":"message","role":"assistant","content":[{"type":"redacted_thinking","data":"x"},{"type":"text","text":"First. "},{"type":"text","text":"Second."}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":2}}`,
			wantContent: "First. Second.", wantFinish: "stop",
			wantUsage: map[string]any{"input_tokens": float64(6), "output_tokens": float64(3), "cached_tokens": float64(2), "total_tokens": nil}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.upstreamBody))
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture")
			createAnthropicToken(t, server.Handler())

			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"anthropic-main","messages":[{"role":"user","content":"Hello"}]}`))
			if response.Code != http.StatusOK {
				t.Fatalf("Anthropic Generate status = %d, body = %s", response.Code, response.Body.String())
			}
			assertJSON(t, response.Body.Bytes(), map[string]any{
				"content": test.wantContent, "finish_reason": test.wantFinish, "tool_calls": []any{}, "usage": test.wantUsage,
			})
		})
	}
}

func TestAnthropicRejectsOptionsThatChangeTheNormalizedResponseShape(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture")
	createAnthropicToken(t, server.Handler())

	for _, options := range []string{
		`{"stream":true}`,
		`{"system":"override"}`,
		`{"messages":[]}`,
		`{"model":"other"}`,
		`{"tools":[]}`,
		`{"mcp_servers":[]}`,
		`{"betas":["fast-mode-2026-02-01"]}`,
		`{"fallbacks":"default"}`,
		`{"speed":"fast"}`,
		`{"unknown_anthropic_option":true}`,
		`{"tool_choice":"auto"}`,
		`{"tool_choice":{"type":"function","name":"market.price"}}`,
		`{"tool_choice":{"type":"tool"}}`,
		`{"tool_choice":{"type":"tool","name":"undeclared.tool"}}`,
		`{"tool_choice":{"type":"auto","name":"market.price"}}`,
	} {
		body := []byte(`{"provider":"anthropic-main","messages":[{"role":"user","content":"Hello"}],` +
			`"tools":[{"name":"market.price","input_schema":{"type":"object"}}],"options":` + options + `}`)
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, body),
			http.StatusBadRequest, "REQUEST_INVALID")
	}
	if calls.Load() != 0 {
		t.Fatalf("Anthropic calls for unsupported options = %d, want 0", calls.Load())
	}
}

func TestAnthropicTimeoutAndCallerCancellationStopTheSingleUpstreamRequest(t *testing.T) {
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
		createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture")
		createAnthropicToken(t, server.Handler())

		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
			[]byte(`{"provider":"anthropic-main","messages":[{"role":"user","content":"Hello"}]}`))
		assertError(t, response, http.StatusGatewayTimeout, "PROVIDER_TIMEOUT")
		select {
		case <-started:
		default:
			t.Fatal("Anthropic timeout request never started")
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
			_, _ = w.Write([]byte(`{"type":"message","content":[`))
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(upstreamCancelled)
		}))
		defer provider.Close()
		server := openTestServer(t, time.Second)
		defer server.Close()
		createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture")
		createAnthropicToken(t, server.Handler())

		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewBufferString(
			`{"provider":"anthropic-main","messages":[{"role":"user","content":"Hello"}]}`)).WithContext(ctx)
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
			t.Fatal("cancelled Anthropic request never started")
		}
		cancel()
		select {
		case <-upstreamCancelled:
		case <-time.After(time.Second):
			t.Fatal("caller cancellation did not reach Anthropic")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Anthropic Generate did not return after caller cancellation")
		}
		assertError(t, response, http.StatusBadGateway, "PROVIDER_UNAVAILABLE")
		if calls.Load() != 1 {
			t.Fatalf("cancelled Anthropic calls = %d, want exactly 1", calls.Load())
		}
	})
}

func createAnthropicProvider(t *testing.T, handler http.Handler, baseURL, defaultModel string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": "anthropic-main", "name": "Anthropic", "type": "anthropic",
		"base_url": baseURL, "default_model": defaultModel, "config": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodPost, "/v1/providers", adminToken, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Anthropic provider status = %d, body = %s", response.Code, response.Body.String())
	}
}

func createAnthropicToken(t *testing.T, handler http.Handler) {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v1/providers/anthropic-main/tokens", adminToken,
		[]byte(`{"name":"main","token":"sk-ant-fixture"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("create Anthropic token status = %d, body = %s", response.Code, response.Body.String())
	}
}
