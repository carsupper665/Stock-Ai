package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompatibleToolCallsRoundTripAcrossMultipleResults(t *testing.T) {
	var requestNumber atomic.Int32
	var marketWireName, accountWireName string
	wireNamePattern := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		switch requestNumber.Add(1) {
		case 1:
			tools := body["tools"].([]any)
			function := tools[0].(map[string]any)["function"].(map[string]any)
			marketWireName = function["name"].(string)
			accountWireName = tools[1].(map[string]any)["function"].(map[string]any)["name"].(string)
			if tools[0].(map[string]any)["type"] != "function" || !wireNamePattern.MatchString(marketWireName) ||
				!wireNamePattern.MatchString(accountWireName) || marketWireName == "market.price" || accountWireName == "account.state" ||
				function["parameters"].(map[string]any)["type"] != "object" {
				t.Errorf("compatible tools = %+v", tools)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{"content": "I will check both.", "tool_calls": []any{
						map[string]any{"id": "call-price", "type": "function", "function": map[string]any{"name": marketWireName, "arguments": `{"symbol":"BTCUSDT"}`}},
						map[string]any{"id": "call-account", "type": "function", "function": map[string]any{"name": accountWireName, "arguments": `{}`}},
					}}, "finish_reason": "tool_calls",
				}}, "usage": map[string]any{"prompt_tokens": 10},
			})
		case 2:
			messages := body["messages"].([]any)
			assistant := messages[1].(map[string]any)
			calls := assistant["tool_calls"].([]any)
			firstFunction := calls[0].(map[string]any)["function"].(map[string]any)
			secondFunction := calls[1].(map[string]any)["function"].(map[string]any)
			if assistant["content"] != "I will check both." || firstFunction["arguments"] != `{"symbol":"BTCUSDT"}` ||
				firstFunction["name"] != marketWireName || secondFunction["name"] != accountWireName {
				t.Errorf("assistant tool history = %+v", assistant)
			}
			if messages[2].(map[string]any)["tool_call_id"] != "call-price" ||
				messages[3].(map[string]any)["tool_call_id"] != "call-account" {
				t.Errorf("tool result correlation = %+v", messages)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"content": nil, "tool_calls": []any{
					map[string]any{"id": "call-summary", "type": "function", "function": map[string]any{"name": marketWireName, "arguments": `{"symbol":"ETHUSDT"}`}},
				}}, "finish_reason": "tool_calls",
			}}})
		case 3:
			messages := body["messages"].([]any)
			assistant := messages[4].(map[string]any)
			function := assistant["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
			if function["name"] != marketWireName || messages[5].(map[string]any)["tool_call_id"] != "call-summary" {
				t.Errorf("third-round tool history = %+v", messages)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"BTC is 60000."},"finish_reason":"stop"}]}`))
		default:
			t.Errorf("unexpected provider request")
		}
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	first := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"fixture",
		"messages":[{"role":"user","content":"Check BTC and my account."}],
		"tools":[
			{"name":"market.price","description":"Get price","input_schema":{"type":"object","properties":{"symbol":{"type":"string"}},"required":["symbol"]}},
			{"name":"account.state","description":"Get account","input_schema":{"type":"object"}}
		]
	}`))
	if first.Code != http.StatusOK {
		t.Fatalf("first Generate status = %d, body = %s", first.Code, first.Body.String())
	}
	assertJSON(t, first.Body.Bytes(), map[string]any{
		"content": "I will check both.", "finish_reason": "tool_call",
		"tool_calls": []any{
			map[string]any{"id": "call-price", "name": "market.price", "arguments": map[string]any{"symbol": "BTCUSDT"}},
			map[string]any{"id": "call-account", "name": "account.state", "arguments": map[string]any{}},
		},
		"usage": map[string]any{"input_tokens": float64(10), "output_tokens": nil, "cached_tokens": nil, "total_tokens": nil},
	})

	second := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"fixture",
		"messages":[
			{"role":"user","content":"Check BTC and my account."},
			{"role":"assistant","content":"I will check both.","tool_calls":[
				{"id":"call-price","name":"market.price","arguments":{"symbol":"BTCUSDT"}},
				{"id":"call-account","name":"account.state","arguments":{}}
			]},
			{"role":"tool","content":"{\"price\":60000}","tool_call_id":"call-price"},
			{"role":"tool","content":"{\"balance\":1000}","tool_call_id":"call-account"}
		],
		"tools":[{"name":"market.price","description":"Get price","input_schema":{"type":"object"}}]
	}`))
	if second.Code != http.StatusOK {
		t.Fatalf("second Generate status = %d, body = %s", second.Code, second.Body.String())
	}
	assertJSON(t, second.Body.Bytes(), map[string]any{
		"content": nil, "finish_reason": "tool_call",
		"tool_calls": []any{map[string]any{"id": "call-summary", "name": "market.price", "arguments": map[string]any{"symbol": "ETHUSDT"}}},
		"usage":      nil,
	})

	third := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"fixture",
		"messages":[
			{"role":"user","content":"Check BTC and my account."},
			{"role":"assistant","content":"I will check both.","tool_calls":[
				{"id":"call-price","name":"market.price","arguments":{"symbol":"BTCUSDT"}},
				{"id":"call-account","name":"account.state","arguments":{}}
			]},
			{"role":"tool","content":"{\"price\":60000}","tool_call_id":"call-price"},
			{"role":"tool","content":"{\"balance\":1000}","tool_call_id":"call-account"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call-summary","name":"market.price","arguments":{"symbol":"ETHUSDT"}}
			]},
			{"role":"tool","content":"{\"price\":3000}","tool_call_id":"call-summary"}
		],
		"tools":[{"name":"market.price","description":"Get price","input_schema":{"type":"object"}}]
	}`))
	if third.Code != http.StatusOK {
		t.Fatalf("third Generate status = %d, body = %s", third.Code, third.Body.String())
	}
	assertJSON(t, third.Body.Bytes(), map[string]any{
		"content": "BTC is 60000.", "finish_reason": "stop", "tool_calls": []any{}, "usage": nil,
	})
	if got := requestNumber.Load(); got != 3 {
		t.Fatalf("provider requests = %d, want exactly 3; Gateway must not execute tools", got)
	}
}

func TestCompatibleToolNamesUseDeterministicCollisionSafeWireAliases(t *testing.T) {
	runtimeNames := []string{"market.price", "market_price", strings.Repeat("x", 128), "history.lookup"}
	wireNamePattern := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	var firstWireNames []string
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		tools := body["tools"].([]any)
		messages := body["messages"].([]any)
		wireNames := []string{
			tools[0].(map[string]any)["function"].(map[string]any)["name"].(string),
			tools[1].(map[string]any)["function"].(map[string]any)["name"].(string),
			tools[2].(map[string]any)["function"].(map[string]any)["name"].(string),
			messages[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"].(string),
		}
		seen := map[string]bool{}
		for _, name := range wireNames {
			if !wireNamePattern.MatchString(name) || seen[name] {
				t.Errorf("invalid or colliding wire aliases: %v", wireNames)
			}
			seen[name] = true
		}
		if wireNames[0] == runtimeNames[0] || wireNames[0] == wireNames[1] || wireNames[2] == runtimeNames[2] || wireNames[3] == runtimeNames[3] {
			t.Errorf("names were not safely aliased: runtime=%v wire=%v", runtimeNames, wireNames)
		}
		if firstWireNames == nil {
			firstWireNames = append([]string(nil), wireNames...)
		} else if !reflect.DeepEqual(firstWireNames, wireNames) {
			t.Errorf("wire aliases changed across equivalent rounds: first=%v next=%v", firstWireNames, wireNames)
		}
		requestNumber := requests.Add(1)
		calls := make([]any, 0, len(wireNames))
		for index, name := range wireNames {
			calls = append(calls, map[string]any{"id": fmt.Sprintf("new-%d-%d", requestNumber, index), "type": "function",
				"function": map[string]any{"name": name, "arguments": `{}`}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": nil, "tool_calls": calls}, "finish_reason": "tool_calls",
		}}})
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)
	requestBody := fmt.Sprintf(`{
		"provider":"fixture",
		"messages":[
			{"role":"user","content":"continue"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"history-id","name":"%s","arguments":{}}]},
			{"role":"tool","content":"done","tool_call_id":"history-id"}
		],
		"tools":[
			{"name":"%s","input_schema":{}},
			{"name":"%s","input_schema":{}},
			{"name":"%s","input_schema":{}}
		]
	}`, runtimeNames[3], runtimeNames[0], runtimeNames[1], runtimeNames[2])
	for round := 1; round <= 2; round++ {
		response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(requestBody))
		if response.Code != http.StatusOK {
			t.Fatalf("round %d status = %d, body = %s", round, response.Code, response.Body.String())
		}
		var normalized struct {
			ToolCalls []struct {
				Name string `json:"name"`
			} `json:"tool_calls"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &normalized); err != nil {
			t.Fatal(err)
		}
		if len(normalized.ToolCalls) != len(runtimeNames) {
			t.Fatalf("round %d tool calls = %+v", round, normalized.ToolCalls)
		}
		for index, call := range normalized.ToolCalls {
			if call.Name != runtimeNames[index] {
				t.Errorf("round %d call %d name = %q, want %q", round, index, call.Name, runtimeNames[index])
			}
		}
	}
}

func TestCompatibleToolRequestRejectsInvalidSchemaAndUncorrelatedResults(t *testing.T) {
	var providerRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerRequests.Add(1)
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	for _, body := range []string{
		`{"provider":"fixture","messages":[{"role":"tool","content":"result","tool_call_id":"missing"}]}`,
		`{"provider":"fixture","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"bad","description":"bad","input_schema":[]}]}`,
		`{"provider":"fixture","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":null,"tool_calls":[{"id":"same","name":"one","arguments":{}},{"id":"same","name":"two","arguments":{}}]}]}`,
	} {
		assertError(t, request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(body)),
			http.StatusBadRequest, "REQUEST_INVALID")
	}
	if got := providerRequests.Load(); got != 0 {
		t.Fatalf("provider requests for invalid tool inputs = %d", got)
	}
}

func TestCompatibleToolResponseRejectsMalformedCalls(t *testing.T) {
	responses := []string{
		`{"choices":[{"message":{"content":null,"tool_calls":[{"type":"function","function":{"name":"tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"tool","arguments":"[]"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"tool","arguments":"{} {}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"tool","arguments":"{}"}}]},"finish_reason":"stop"}]}`,
	}
	for index, upstreamResponse := range responses {
		t.Run(string(rune('A'+index)), func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(upstreamResponse))
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createProvider(t, server.Handler(), provider.URL, "model", true)
			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
				[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"tool","input_schema":{"type":"object"}}]}`))
			assertError(t, response, http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID")
		})
	}
}

func TestCompatibleToolResponseRejectsIDsAlreadyUsedInHistory(t *testing.T) {
	for _, test := range []struct {
		name  string
		calls string
	}{
		{name: "single collision", calls: `[{"id":"used-id","type":"function","function":{"name":"tool","arguments":"{}"}}]`},
		{name: "collision among multiple output calls", calls: `[
			{"id":"fresh-id","type":"function","function":{"name":"tool","arguments":"{}"}},
			{"id":"used-id","type":"function","function":{"name":"tool","arguments":"{}"}}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var providerRequests atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				providerRequests.Add(1)
				_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":null,"tool_calls":%s},"finish_reason":"tool_calls"}]}`, test.calls)
			}))
			defer provider.Close()
			server := openTestServer(t, time.Second)
			defer server.Close()
			createProvider(t, server.Handler(), provider.URL, "model", true)
			response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
				"provider":"fixture",
				"messages":[
					{"role":"user","content":"continue"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"used-id","name":"tool","arguments":{}}]},
					{"role":"tool","content":"done","tool_call_id":"used-id"}
				],
				"tools":[{"name":"tool","input_schema":{}}]
			}`))
			assertError(t, response, http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID")
			if providerRequests.Load() != 1 {
				t.Fatalf("provider requests = %d, want 1 without retry", providerRequests.Load())
			}
		})
	}
}

func TestCompatibleRateLimitAndCancellationAreNormalizedWithoutRetry(t *testing.T) {
	var rateRequests atomic.Int32
	rateProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rateRequests.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer rateProvider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), rateProvider.URL, "model", true)
	limited := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, limited, http.StatusTooManyRequests, "RATE_LIMITED")
	if rateRequests.Load() != 1 {
		t.Fatalf("rate-limited requests = %d, want 1", rateRequests.Load())
	}
	server.Close()

	started := make(chan struct{})
	upstreamCancelled := make(chan struct{})
	cancelProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[`))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(upstreamCancelled)
	}))
	defer cancelProvider.Close()
	cancelServer := openTestServer(t, time.Second)
	defer cancelServer.Close()
	createProvider(t, cancelServer.Handler(), cancelProvider.URL, "model", true)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewBufferString(
		`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		cancelServer.Handler().ServeHTTP(recorder, req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancel fixture did not receive request")
	}
	cancel()
	select {
	case <-upstreamCancelled:
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not reach provider request")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Generate did not return after cancellation")
	}
	assertError(t, recorder, http.StatusBadGateway, "PROVIDER_UNAVAILABLE")
}
