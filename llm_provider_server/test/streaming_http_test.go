package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveEvents answers like a streaming Provider: text/event-stream, each event flushed at once.
func serveEvents(t *testing.T, script func(r *http.Request, body map[string]any, send func(data string))) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Provider request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(data string) {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		script(r, body, send)
	}))
}

func compatibleChunk(delta string) string {
	return `{"choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`
}

func TestCompatibleStreamRunsPastTheTimeoutWhileChunksKeepArriving(t *testing.T) {
	provider := serveEvents(t, func(_ *http.Request, body map[string]any, send func(string)) {
		options, _ := body["stream_options"].(map[string]any)
		if body["stream"] != true || options["include_usage"] != true {
			t.Errorf("stream request = stream %v, stream_options %v", body["stream"], body["stream_options"])
		}
		send(compatibleChunk(`{"role":"assistant","content":""}`))
		for step := range 8 {
			time.Sleep(100 * time.Millisecond)
			send(compatibleChunk(fmt.Sprintf(`{"reasoning_content":"step %d. "}`, step)))
		}
		send(compatibleChunk(`{"content":"Final"}`))
		send(compatibleChunk(`{"content":" answer"}`))
		send(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		send(`{"choices":[],"usage":{"prompt_tokens":21,"completion_tokens":5,"total_tokens":26}}`)
		send(`[DONE]`)
	})
	defer provider.Close()
	server := openTestServer(t, 300*time.Millisecond)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSON(t, response.Body.Bytes(), map[string]any{
		"content": "Final answer", "finish_reason": "stop", "tool_calls": []any{},
		"reasoning": "step 0. step 1. step 2. step 3. step 4. step 5. step 6. step 7. ",
		"usage":     map[string]any{"input_tokens": 21, "output_tokens": 5, "cached_tokens": nil, "total_tokens": 26},
	})
}

func TestCompatibleStreamJoinsToolCallFragmentsByIndex(t *testing.T) {
	provider := serveEvents(t, func(_ *http.Request, body map[string]any, send func(string)) {
		name := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"].(string)
		fragment := func(index int, fields string) string {
			return compatibleChunk(fmt.Sprintf(`{"tool_calls":[{"index":%d%s}]}`, index, fields))
		}
		send(compatibleChunk(`{"role":"assistant","content":null}`))
		send(fragment(0, `,"id":"call-a","type":"function","function":{"name":"`+name+`","arguments":""}`))
		send(fragment(1, `,"id":"call-b","type":"function","function":{"name":"`+name+`","arguments":"{\"sym"}`))
		send(fragment(0, `,"function":{"arguments":"{\"symbol\":\"BTCUSDT\"}"}`))
		send(fragment(1, `,"function":{"arguments":"bol\":\"ETHUSDT\"}"}`))
		send(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		send(`[DONE]`)
	})
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"fixture","messages":[{"role":"user","content":"prices?"}],
		"tools":[{"name":"market.price","description":"Get price","input_schema":{"type":"object"}}]
	}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSON(t, response.Body.Bytes(), map[string]any{
		"content": nil, "finish_reason": "tool_call", "tool_calls": []any{
			map[string]any{"id": "call-a", "name": "market.price", "arguments": map[string]any{"symbol": "BTCUSDT"}},
			map[string]any{"id": "call-b", "name": "market.price", "arguments": map[string]any{"symbol": "ETHUSDT"}},
		},
	})
}

func TestCompatibleStreamThatGoesSilentTimesOut(t *testing.T) {
	release := make(chan struct{})
	provider := serveEvents(t, func(_ *http.Request, _ map[string]any, send func(string)) {
		send(compatibleChunk(`{"role":"assistant","reasoning_content":"thinking"}`))
		<-release
	})
	defer provider.Close()
	defer close(release)
	server := openTestServer(t, 200*time.Millisecond)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)

	started := time.Now()
	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	assertError(t, response, http.StatusGatewayTimeout, "PROVIDER_TIMEOUT")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("silent stream took %s to time out", elapsed)
	}
}

type streamLine struct {
	Type      string          `json:"type"`
	Content   string          `json:"content"`
	Reasoning string          `json:"reasoning"`
	Status    int             `json:"status"`
	Error     json.RawMessage `json:"error"`
	Response  json.RawMessage `json:"response"`
	at        time.Duration
}

func generateStream(t *testing.T, gatewayURL, body string) (*http.Response, []streamLine) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, gatewayURL+"/v1/generate", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	started := time.Now()
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var lines []streamLine
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var line streamLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("stream line %q: %v", scanner.Text(), err)
		}
		line.at = time.Since(started)
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return response, lines
}

func TestGenerateStreamsDeltasPingsAndTheFinalResponseAsNDJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a Gateway ping")
	}
	provider := serveEvents(t, func(_ *http.Request, _ map[string]any, send func(string)) {
		send(compatibleChunk(`{"role":"assistant","reasoning_content":"thinking"}`))
		time.Sleep(5500 * time.Millisecond)
		send(compatibleChunk(`{"content":"done"}`))
		send(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		send(`[DONE]`)
	})
	defer provider.Close()
	server := openTestServer(t, 10*time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)
	gateway := httptest.NewServer(server.Handler())
	defer gateway.Close()

	response, lines := generateStream(t, gateway.URL, `{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("stream status = %d, Content-Type = %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	var kinds []string
	for _, line := range lines {
		kinds = append(kinds, line.Type)
	}
	if strings.Join(kinds, ",") != "delta,ping,delta,done" {
		t.Fatalf("stream lines = %v", kinds)
	}
	if lines[0].Reasoning != "thinking" || lines[0].at > 2*time.Second {
		t.Fatalf("first delta = %+v, want reasoning before the Provider finishes", lines[0])
	}
	if lines[2].Content != "done" {
		t.Fatalf("content delta = %+v", lines[2])
	}
	assertJSON(t, lines[3].Response, map[string]any{"content": "done", "reasoning": "thinking", "finish_reason": "stop"})
}

func TestGenerateStreamCarriesProviderFailuresButRequestErrorsStayJSON(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createProvider(t, server.Handler(), provider.URL, "model", true)
	gateway := httptest.NewServer(server.Handler())
	defer gateway.Close()

	response, lines := generateStream(t, gateway.URL, `{"provider":"fixture","messages":[{"role":"user","content":"hello"}]}`)
	if response.StatusCode != http.StatusOK || len(lines) != 1 || lines[0].Type != "error" || lines[0].Status != http.StatusBadGateway ||
		!bytes.Contains(lines[0].Error, []byte(`"code":"PROVIDER_UNAVAILABLE"`)) {
		t.Fatalf("Provider failure stream = %d %+v", response.StatusCode, lines)
	}

	missing, _ := http.NewRequest(http.MethodPost, gateway.URL+"/v1/generate",
		strings.NewReader(`{"provider":"missing","messages":[{"role":"user","content":"hello"}]}`))
	missing.Header.Set("Authorization", "Bearer "+runtimeToken)
	missing.Header.Set("Content-Type", "application/json")
	missing.Header.Set("Accept", "application/x-ndjson")
	result, err := http.DefaultClient.Do(missing)
	if err != nil {
		t.Fatal(err)
	}
	result.Body.Close()
	if result.StatusCode != http.StatusNotFound || result.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unknown Provider = %d %q, want a JSON 404 before any stream starts", result.StatusCode, result.Header.Get("Content-Type"))
	}
}

func TestAnthropicStreamAssemblesThinkingTextAndToolUse(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("Anthropic stream = %v", body["stream"])
		}
		name := body["tools"].([]any)[0].(map[string]any)["name"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check "}}`,
			`{"type":"ping"}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the price."}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Looking it up."}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"` + name + `","input":{}}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"symbol\":"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"BTCUSDT\"}"}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":30}}`,
			`{"type":"message_stop"}`,
		} {
			var kind struct{ Type string }
			_ = json.Unmarshal([]byte(event), &kind)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind.Type, event)
		}
	}))
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createAnthropicProvider(t, server.Handler(), provider.URL+"/v1", "claude-fixture")
	createAnthropicToken(t, server.Handler())

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken, []byte(`{
		"provider":"anthropic-main","messages":[{"role":"user","content":"price?"}],
		"tools":[{"name":"market.price","description":"Get price","input_schema":{"type":"object"}}]
	}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSON(t, response.Body.Bytes(), map[string]any{
		"content": "Looking it up.", "reasoning": "Check the price.", "finish_reason": "tool_call",
		"tool_calls": []any{map[string]any{"id": "toolu_1", "name": "market.price", "arguments": map[string]any{"symbol": "BTCUSDT"}}},
		"usage":      map[string]any{"input_tokens": 14, "output_tokens": 30, "cached_tokens": 4, "total_tokens": nil},
	})
}

func TestGeminiStreamAssemblesTextChunks(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	provider := serveEvents(t, func(r *http.Request, _ map[string]any, send func(string)) {
		if r.URL.RawQuery != "alt=sse" {
			t.Errorf("Gemini query = %q", r.URL.RawQuery)
		}
		send(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"index":0}]}`)
		send(`{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`)
	})
	defer provider.Close()
	server := openTestServer(t, time.Second)
	defer server.Close()
	createGeminiProvider(t, server.Handler(), provider.URL+"/v1beta", geminiTestModel)
	createGeminiToken(t, server.Handler())

	response := request(t, server.Handler(), http.MethodPost, "/v1/generate", runtimeToken,
		[]byte(`{"provider":"gemini-main","messages":[{"role":"user","content":"hi"}]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSON(t, response.Body.Bytes(), map[string]any{
		"content": "Hello world", "finish_reason": "stop",
		"usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "cached_tokens": nil, "total_tokens": 5},
	})
}
