package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	agent "stock-ai/agent-server/internal/agent"
)

func TestTicket16ToolLoopThroughRealGatewayProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts the Gateway process")
	}
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode compatible Provider request: %v", err)
		}
		if providerCalls.Add(1) == 1 {
			tools := body["tools"].([]any)
			wireName := tools[0].(map[string]any)["function"].(map[string]any)["name"].(string)
			writeFixtureJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"content": nil, "tool_calls": []any{map[string]any{
					"id": "process-price", "type": "function", "function": map[string]any{
						"name": wireName, "arguments": `{"market":"crypto","symbol":"BTCUSDT"}`,
					},
				}}}, "finish_reason": "tool_calls",
			}}})
			return
		}
		messages := body["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if last["role"] != "tool" || last["tool_call_id"] != "process-price" {
			t.Errorf("compatible Provider Tool correlation = %v", last)
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": finalizationContent("real Gateway completed", "real Gateway completed")}, "finish_reason": "stop",
		}}})
	}))
	defer provider.Close()

	gatewayURL, gatewayRuntimeToken := startGatewayProcess(t, provider.URL)

	fixture := newRuntimeFixture()
	backend := httptest.NewServer(http.HandlerFunc(fixture.serveBackend))
	defer backend.Close()
	agentRuntime, err := agent.OpenAgentRuntime(agent.RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: agentAdminToken,
		BackendURL: backend.URL, BackendUserToken: backendUserToken,
		GatewayURL: gatewayURL, GatewayRuntimeToken: gatewayRuntimeToken,
		DefaultPrompt: "default mission", DefaultMaxLoop: 3, DefaultMaxToolCall: 2,
		RequestTimeout: 2 * time.Second, ToolTimeout: time.Second, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = agentRuntime.Close() })
	session := createStoppedSession(t, agentRuntime, nil)
	status, raw := runtimeRequest(t, agentRuntime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", status, raw)
	}
	run := waitForRunStatus(t, agentRuntime, session.ID, 1, "completed")
	if run.Summary == nil || *run.Summary != "real Gateway completed" || run.ModelCalls != 2 || run.ToolCalls != 1 {
		t.Fatalf("real Gateway Run = %+v", run)
	}
	if providerCalls.Load() != 2 {
		t.Fatalf("compatible Provider calls = %d, want 2", providerCalls.Load())
	}
}

// startGatewayProcess builds and starts the real llm-provider-server with one OpenAI-compatible
// Provider "fixture" at providerURL and the fixture-model catalog entry.
func startGatewayProcess(t *testing.T, providerURL string, environment ...string) (string, string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "llm-provider-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/llm-provider-server")
	build.Dir = "../../llm_provider_server"
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Gateway process: %v\n%s", err, output)
	}

	address := ticket16AvailableAddress(t)
	gatewayAdminToken := "ticket16-gateway-admin"
	gatewayRuntimeToken := "ticket16-gateway-runtime"
	catalog := `[{"model_name":"fixture-model","provider_id":"fixture","model":"provider-model","levels":{"high":{}}}]`
	command := exec.Command(binary)
	command.Env = append(append(os.Environ(), environment...),
		"LLM_SERVER_ADDR="+address,
		"LLM_SERVER_DB_PATH="+filepath.Join(t.TempDir(), "gateway.db"),
		"LLM_SERVER_ADMIN_TOKEN="+gatewayAdminToken,
		"LLM_SERVER_RUNTIME_TOKEN="+gatewayRuntimeToken,
		"LLM_SERVER_MASTER_KEY=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"LLM_SERVER_MODEL_CATALOG="+catalog,
	)
	var gatewayLogs bytes.Buffer
	command.Stdout = &gatewayLogs
	command.Stderr = &gatewayLogs
	if err := command.Start(); err != nil {
		t.Fatalf("start Gateway process: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	t.Cleanup(stop)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	readyContext, cancelReady := context.WithTimeout(context.Background(), 10*time.Second)
	if err := ticket16WaitForGateway(readyContext, client, address); err != nil {
		cancelReady()
		stop()
		t.Fatalf("Gateway process readiness: %v\n%s", err, gatewayLogs.String())
	}
	cancelReady()
	gatewayURL := "http://" + address
	providerBody, _ := json.Marshal(map[string]any{
		"id": "fixture", "name": "Fixture", "type": "openai_compatible",
		"base_url": providerURL, "default_model": "provider-model", "enabled": true,
	})
	status, raw := ticket16ProcessRequest(t, client, http.MethodPost, gatewayURL+"/v1/providers", gatewayAdminToken, providerBody)
	if status != http.StatusCreated {
		t.Fatalf("create process Provider status = %d body = %s", status, raw)
	}

	return gatewayURL, gatewayRuntimeToken
}

func ticket16AvailableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func ticket16WaitForGateway(ctx context.Context, client *http.Client, address string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/healthz", nil)
		if err != nil {
			return err
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			return fmt.Errorf("health status = %d", response.StatusCode)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: last health request: %v", ctx.Err(), requestErr)
		case <-ticker.C:
		}
	}
}

func ticket16ProcessRequest(t *testing.T, client *http.Client, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var responseBody bytes.Buffer
	if _, err := responseBody.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody.Bytes()
}

// The real Gateway and agent-server together: a model that streams its reasoning for three times
// both timeouts still completes, because each side times out only on silence.
func TestSlowStreamingModelCompletesThroughRealGatewayProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts the Gateway process")
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("Gateway did not ask the Provider to stream: %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(data string) {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		send(`{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		for step := range 10 {
			time.Sleep(300 * time.Millisecond)
			send(fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":"step %d "},"finish_reason":null}]}`, step))
		}
		content, _ := json.Marshal(finalizationContent("slow model finished", "slow model finished"))
		send(`{"choices":[{"index":0,"delta":{"content":` + string(content) + `},"finish_reason":"stop"}]}`)
		send(`[DONE]`)
	}))
	defer provider.Close()
	gatewayURL, gatewayRuntimeToken := startGatewayProcess(t, provider.URL, "LLM_SERVER_PROVIDER_TIMEOUT=1s")

	fixture := newRuntimeFixture()
	backend := httptest.NewServer(http.HandlerFunc(fixture.serveBackend))
	defer backend.Close()
	agentRuntime, err := agent.OpenAgentRuntime(agent.RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: agentAdminToken,
		BackendURL: backend.URL, BackendUserToken: backendUserToken,
		GatewayURL: gatewayURL, GatewayRuntimeToken: gatewayRuntimeToken,
		DefaultPrompt: "default mission", DefaultMaxLoop: 3, DefaultMaxToolCall: 2,
		RequestTimeout: time.Second, ToolTimeout: time.Second, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = agentRuntime.Close() })
	session := createStoppedSession(t, agentRuntime, nil)
	startSessionForTest(t, agentRuntime, session.ID)

	run := waitForRunStatusWithin(t, agentRuntime, session.ID, 1, 15*time.Second, "completed")
	if run.Summary == nil || *run.Summary != "slow model finished" || run.ModelCalls != 1 {
		t.Fatalf("slow streaming Run = summary %v, model calls %d", run.Summary, run.ModelCalls)
	}
	var progress []struct {
		Type      string `json:"type"`
		Reasoning string `json:"reasoning"`
	}
	_ = json.Unmarshal(run.Progress, &progress)
	for _, entry := range progress {
		if entry.Type == "model_call" && entry.Reasoning != "step 0 step 1 step 2 step 3 step 4 step 5 step 6 step 7 step 8 step 9 " {
			t.Fatalf("assembled reasoning = %q", entry.Reasoning)
		}
	}
}
