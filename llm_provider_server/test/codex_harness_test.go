package test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	providers "stock-ai/llm-provider-server/internal/providers"
	webharness "stock-ai/llm-provider-server/internal/webharness"
)

func TestMain(m *testing.M) {
	name := strings.ToLower(filepath.Base(os.Args[0]))
	if strings.HasPrefix(name, "codex-fixture") {
		os.Exit(runCodexFixture())
	}
	if strings.HasPrefix(name, "claude-fixture") {
		os.Exit(runClaudeFixture())
	}
	os.Exit(m.Run())
}

func codexComplete(t *testing.T, ctx context.Context, server *webharness.CodexAppServer, key, model string, timeout time.Duration, messages []providers.Message, tools []providers.ToolSchema) (providers.GenerateResponse, error) {
	t.Helper()
	return server.Complete(ctx, providers.Model{Provider: "codex", ID: model, Config: json.RawMessage(`{"auth_mode":"api_key"}`)},
		providers.GenerateRequest{Provider: "codex", Messages: messages, Tools: tools},
		providers.Options{APIKey: key, Client: &http.Client{Timeout: timeout}})
}

func userMessages(content string) []providers.Message {
	return []providers.Message{{Role: "user", Content: &content}}
}

func providerErrorCode(err error) string {
	failure, ok := err.(providers.ProviderError)
	if !ok {
		return fmt.Sprintf("not a ProviderError: %v", err)
	}
	return failure.Code
}

func TestCodexAppServerDeclaresToolsNativelyAndReturnsTheFirstCall(t *testing.T) {
	attackerConfig := t.TempDir()
	t.Setenv("HOME", attackerConfig)
	t.Setenv("USERPROFILE", attackerConfig)
	t.Setenv("CODEX_HOME", attackerConfig)
	t.Setenv("CLAUDE_CONFIG_DIR", attackerConfig)
	t.Setenv("OPENAI_API_KEY", "attacker-key")
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	server := webharness.NewCodexAppServer()
	defer server.Close()
	response, err := codexComplete(t, context.Background(), server, "fixture-codex-key", "gpt-5.4", 5*time.Second,
		userMessages("Check BTC using current public information."), []providers.ToolSchema{{
			Name: "market.price", Description: "Return the current market price.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"symbol":{"type":"string"}},"required":["symbol"]}`),
		}})
	if err != nil {
		t.Fatal(err)
	}
	if response.FinishReason != "tool_call" || response.Content != nil || len(response.ToolCalls) != 1 {
		t.Fatalf("response = %+v", response)
	}
	call := response.ToolCalls[0]
	if call.ID != "call-price" || call.Name != "market.price" || string(call.Arguments) != `{"symbol":"BTCUSDT"}` {
		t.Fatalf("tool call = %+v", call)
	}
	if response.Usage == nil || value(response.Usage.InputTokens) != 11 || value(response.Usage.OutputTokens) != 3 ||
		value(response.Usage.CachedTokens) != 4 || value(response.Usage.TotalTokens) != 14 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCodexAppServerContinuesFromInjectedToolResult(t *testing.T) {
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	server := webharness.NewCodexAppServer()
	defer server.Close()
	question, result := "price?", `{"price":65000.5}`
	callID := "call-price"
	messages := []providers.Message{
		{Role: "user", Content: &question},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: callID, Name: "market.price", Arguments: json.RawMessage(`{"symbol":"BTCUSDT"}`)}}},
		{Role: "tool", ToolCallID: &callID, Content: &result},
	}
	tools := []providers.ToolSchema{{Name: "market.price", Description: "price", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	response, err := codexComplete(t, context.Background(), server, "fixture-codex-key", "tool-result-report", 5*time.Second, messages, tools)
	if err != nil {
		t.Fatal(err)
	}
	if response.FinishReason != "stop" || response.Content == nil || *response.Content != "items=3 input=0 result="+result {
		t.Fatalf("response = %+v content=%v", response, value2(response.Content))
	}
}

func TestCodexAppServerRejectsWrongVersionAndInvalidOutput(t *testing.T) {
	for _, test := range []struct{ fixture, model, code string }{
		{fixture: "codex-fixture-wrong-version.exe", model: "gpt-5.4", code: "HARNESS_VERSION_MISMATCH"},
		{fixture: "codex-fixture.exe", model: "invalid-output", code: "PROVIDER_RESPONSE_INVALID"},
	} {
		t.Run(test.model+"/"+test.fixture, func(t *testing.T) {
			t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, test.fixture))
			server := webharness.NewCodexAppServer()
			defer server.Close()
			_, err := codexComplete(t, context.Background(), server, "fixture-codex-key", test.model, 5*time.Second, userMessages("hello"), nil)
			if providerErrorCode(err) != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestCodexAppServerTimeoutAndCancellationKeepTheServerUsable(t *testing.T) {
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	server := webharness.NewCodexAppServer()
	defer server.Close()
	started := time.Now()
	_, err := codexComplete(t, context.Background(), server, "fixture-codex-key", "hang", 200*time.Millisecond, userMessages("hello"), nil)
	if providerErrorCode(err) != "PROVIDER_TIMEOUT" || time.Since(started) > 3*time.Second {
		t.Fatalf("timeout error = %v after %s", err, time.Since(started))
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err = codexComplete(t, ctx, server, "fixture-codex-key", "hang", 5*time.Second, userMessages("hello"), nil)
	if providerErrorCode(err) != "REQUEST_CANCELED" {
		t.Fatalf("cancel error = %v", err)
	}
	first, err := codexComplete(t, context.Background(), server, "fixture-codex-key", "pid-report", 5*time.Second, userMessages("hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := codexComplete(t, context.Background(), server, "fixture-codex-key", "pid-report", 5*time.Second, userMessages("hello"), nil)
	if err != nil || *first.Content != *second.Content {
		t.Fatalf("one credential must reuse one app-server: %v %v err=%v", value2(first.Content), value2(second.Content), err)
	}
	other, err := codexComplete(t, context.Background(), server, "fixture-codex-key-2", "pid-report", 5*time.Second, userMessages("hello"), nil)
	if err != nil || *other.Content == *first.Content {
		t.Fatalf("another credential must get its own app-server: %v err=%v", value2(other.Content), err)
	}
}

func TestCodexAppServerRejectsOversizedOutputWithoutWaitingForTimeout(t *testing.T) {
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	server := webharness.NewCodexAppServer()
	defer server.Close()
	started := time.Now()
	_, err := codexComplete(t, context.Background(), server, "fixture-codex-key", "oversized-output", 5*time.Second, userMessages("hello"), nil)
	if providerErrorCode(err) != "PROVIDER_RESPONSE_INVALID" || time.Since(started) > 3*time.Second {
		t.Fatalf("oversized output error = %v after %s", err, time.Since(started))
	}
}

func TestCodexAppServerCloseKillsDescendants(t *testing.T) {
	if !processTreeTestSupported() {
		t.Skip("process liveness probe is unavailable on this platform")
	}
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", copyHarnessFixture(t, "codex-fixture.exe"))
	server := webharness.NewCodexAppServer()
	marker := filepath.Join(t.TempDir(), "descendant.pid")
	result := make(chan error, 1)
	go func() {
		_, err := codexComplete(t, context.Background(), server, "fixture-codex-key", "spawn-descendant", 30*time.Second, userMessages(marker), nil)
		result <- err
	}()
	pid := waitForDescendantPID(t, marker)
	defer func() {
		if alive, _ := processIsAlive(pid); alive {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
			}
		}
	}()
	server.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Generate succeeded after Close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Generate did not return after Close")
	}
	waitForProcessExit(t, pid)
}

func value2(text *string) string {
	if text == nil {
		return "<nil>"
	}
	return *text
}

// --- fake `codex app-server` ---

type codexFixtureThread struct {
	id, model, instructions string
	tools                   []struct{ Name, Description string }
	injected                []json.RawMessage
	raw                     []byte
}

func runCodexFixture() int {
	if len(os.Args) == 3 && os.Args[1] == "codex-descendant" {
		signal.Ignore(os.Interrupt)
		if err := os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			return 3
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if containsArgument("--version") {
		if os.Getenv("CODEX_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
			return 13
		}
		if strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), "wrong-version") {
			fmt.Println("codex-cli 0.161.0")
		} else {
			fmt.Println("codex-cli 0.162.1")
		}
		return 0
	}
	if !codexAppServerIsRestricted() {
		return 2
	}
	return serveCodexFixture()
}

func serveCodexFixture() int {
	local := strings.Contains(filepath.Base(os.Args[0]), "-local")
	shutdown := strings.Contains(filepath.Base(os.Args[0]), "-shutdown")
	output := json.NewEncoder(os.Stdout)
	emit := func(method string, params any) { _ = output.Encode(map[string]any{"method": method, "params": params}) }
	reply := func(id json.RawMessage, result any) { _ = output.Encode(map[string]any{"id": id, "result": result}) }
	fail := func(id json.RawMessage, message string) {
		_ = output.Encode(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": message}})
	}
	completeWith := func(thread *codexFixtureThread, text string) {
		emit("item/completed", map[string]any{"threadId": thread.id, "item": map[string]any{"type": "agentMessage", "text": text}})
		emit("turn/completed", map[string]any{"threadId": thread.id, "turn": map[string]any{"id": "turn-" + thread.id, "status": "completed"}})
	}
	failTurn := func(thread *codexFixtureThread, message string) {
		emit("error", map[string]any{"threadId": thread.id, "willRetry": false, "error": map[string]any{"message": message}})
		emit("turn/completed", map[string]any{"threadId": thread.id, "turn": map[string]any{"id": "turn-" + thread.id, "status": "failed", "error": map[string]any{"message": message}}})
	}
	threads := map[string]*codexFixtureThread{}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			return 4
		}
		var scope struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(message.Params, &scope)
		thread := threads[scope.ThreadID]
		switch message.Method {
		case "initialize":
			var params struct {
				Capabilities struct {
					ExperimentalAPI bool `json:"experimentalApi"`
				} `json:"capabilities"`
			}
			if json.Unmarshal(message.Params, &params) != nil || !params.Capabilities.ExperimentalAPI {
				fail(message.ID, "dynamicTools require experimentalApi")
				continue
			}
			reply(message.ID, map[string]string{"userAgent": "codex-fixture"})
		case "initialized":
		case "account/login/start":
			var params struct{ Type, APIKey string }
			_ = json.Unmarshal(message.Params, &params)
			if local || params.Type != "apiKey" || !strings.HasPrefix(params.APIKey, "fixture-codex-key") {
				fail(message.ID, "invalid api key: 401")
				continue
			}
			reply(message.ID, map[string]string{"type": "apiKey"})
		case "account/read":
			body, err := os.ReadFile(filepath.Join(mustWorkingDirectory(), "auth.json"))
			if err != nil || string(body) != `{"fixture_login":true}` {
				reply(message.ID, map[string]any{"account": nil})
				continue
			}
			reply(message.ID, map[string]any{"account": map[string]string{"type": "chatgpt"}})
		case "thread/start":
			var params struct {
				Model            string `json:"model"`
				BaseInstructions string `json:"baseInstructions"`
				Ephemeral        bool   `json:"ephemeral"`
				Sandbox          string `json:"sandbox"`
				ApprovalPolicy   string `json:"approvalPolicy"`
				DynamicTools     []struct{ Name, Description string }
			}
			if json.Unmarshal(message.Params, &params) != nil || !params.Ephemeral || params.Sandbox != "read-only" || params.ApprovalPolicy != "never" {
				fail(message.ID, "unrestricted thread")
				continue
			}
			if params.Model == "" {
				params.Model = "cli-default"
			}
			thread := &codexFixtureThread{id: fmt.Sprintf("thread-%d", len(threads)+1), model: params.Model, instructions: params.BaseInstructions,
				tools: params.DynamicTools, raw: append([]byte(nil), message.Params...)}
			threads[thread.id] = thread
			reply(message.ID, map[string]any{"thread": map[string]string{"id": thread.id}})
		case "thread/inject_items":
			var params struct{ Items []json.RawMessage }
			_ = json.Unmarshal(message.Params, &params)
			thread.injected = append(thread.injected, params.Items...)
			thread.raw = append(thread.raw, message.Params...)
			reply(message.ID, map[string]any{})
		case "turn/start":
			var params struct {
				Input  []struct{ Text string }
				Effort string `json:"effort"`
			}
			_ = json.Unmarshal(message.Params, &params)
			thread.raw = append(thread.raw, message.Params...)
			reply(message.ID, map[string]any{"turn": map[string]string{"id": "turn-" + thread.id}})
			if shutdown || thread.model == "spawn-descendant" {
				marker := params.Input[0].Text
				descendant := marker
				if shutdown {
					descendant = marker + ".descendant"
				}
				command := exec.Command(os.Args[0], "codex-descendant", descendant)
				command.Stdout, command.Stderr = io.Discard, io.Discard
				if command.Start() != nil {
					return 5
				}
				if shutdown {
					for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
						if _, err := os.Stat(descendant); err == nil {
							break
						}
					}
					state, _ := json.Marshal(shutdownHarnessState{CLIPID: os.Getpid(), DescendantPID: command.Process.Pid, WorkingDirectory: mustWorkingDirectory()})
					_ = os.WriteFile(marker, state, 0o600)
				}
				_ = command.Process.Release()
				continue
			}
			effort := params.Effort
			if effort == "" {
				effort = "none"
			}
			switch thread.model {
			case "hang":
			case "invalid-output":
				emit("turn/completed", map[string]any{"threadId": thread.id, "turn": map[string]any{"id": "turn-" + thread.id, "status": "completed"}})
			case "auth-failure":
				failTurn(thread, "authentication failed: 401")
			case "oversized-output":
				fmt.Print(strings.Repeat("x", 2<<20))
			case "diagnostic-secret":
				failTurn(thread, "usage limit reached "+strings.Repeat("x", 295)+diagnosticSecret)
			case "diagnostic-model-secret":
				failTurn(thread, "unknown model "+diagnosticSecret)
			case "diagnostic-login-secret":
				failTurn(thread, "login expired "+diagnosticSecret)
			case "diagnostic-stderr-secret":
				fmt.Fprintln(os.Stderr, strings.Repeat("x", 295)+diagnosticSecret)
				return 1
			case "effort-report":
				completeWith(thread, effort)
			case "pid-report":
				completeWith(thread, strconv.Itoa(os.Getpid()))
			case "prompt-order-report":
				order := "messages-before-tools"
				if len(thread.tools) != 0 && len(thread.injected) != 0 {
					order = "tools-before-messages"
				}
				completeWith(thread, order)
			case "tool-disclosure-report":
				descriptions := make([]string, 0, len(thread.tools))
				for _, tool := range thread.tools {
					descriptions = append(descriptions, tool.Description)
				}
				completeWith(thread, "tools="+strings.Join(descriptions, ","))
			case "continuation-full-context":
				completeWith(thread, fmt.Sprintf("messages=%d continuation_serialized=%t", len(thread.injected)+len(params.Input), strings.Contains(string(thread.raw), `"continuation"`)))
			case "tool-result-report":
				var last struct{ Type, Output string }
				_ = json.Unmarshal(thread.injected[len(thread.injected)-1], &last)
				completeWith(thread, fmt.Sprintf("items=%d input=%d result=%s", len(thread.injected), len(params.Input), last.Output))
			default:
				if len(thread.tools) == 0 {
					completeWith(thread, "ok")
					continue
				}
				_ = output.Encode(map[string]any{"id": 900, "method": "item/tool/call", "params": map[string]any{
					"threadId": thread.id, "turnId": "turn-" + thread.id, "callId": "call-price", "tool": thread.tools[0].Name,
					"arguments": map[string]string{"symbol": "BTCUSDT"}}})
			}
		case "turn/interrupt":
			emit("thread/tokenUsage/updated", map[string]any{"threadId": thread.id, "tokenUsage": map[string]any{
				"total": map[string]int{"inputTokens": 11, "cachedInputTokens": 4, "outputTokens": 3, "totalTokens": 14}}})
			emit("turn/completed", map[string]any{"threadId": thread.id, "turn": map[string]any{"id": "turn-" + thread.id, "status": "interrupted"}})
			reply(message.ID, map[string]any{})
		case "thread/unsubscribe":
			reply(message.ID, map[string]string{"status": "unsubscribed"})
		}
	}
	return 0
}

// The Gateway launches app-server with the pinned capability flags and an isolated environment.
func codexAppServerIsRestricted() bool {
	expected := []string{"app-server", "-c", `web_search="live"`}
	for _, feature := range []string{
		"shell_tool", "apps", "multi_agent", "goals", "hooks", "memories", "remote_plugin", "plugins",
		"browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "image_generation",
		"skill_search", "tool_suggest", "workspace_dependencies",
	} {
		expected = append(expected, "--disable", feature)
	}
	if !reflect.DeepEqual(os.Args[1:], expected) || os.Getenv("CODEX_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		return false
	}
	workingDirectory := mustWorkingDirectory()
	for _, name := range []string{"HOME", "USERPROFILE", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if os.Getenv(name) != workingDirectory {
			return false
		}
	}
	allowedEnvironment := map[string]bool{
		"HOME": true, "USERPROFILE": true, "CODEX_HOME": true, "CLAUDE_CONFIG_DIR": true, "SystemRoot": true, "WINDIR": true,
		"TEMP": true, "TMP": true, "HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !allowedEnvironment[name] {
			return false
		}
	}
	return strings.HasPrefix(filepath.Base(workingDirectory), "stock-ai-codex-app-server-")
}

func waitForDescendantPID(t *testing.T, marker string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value, err := os.ReadFile(marker)
		if err == nil {
			pid, conversionErr := strconv.Atoi(string(value))
			if conversionErr != nil {
				t.Fatalf("invalid descendant PID %q", value)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant did not start")
	return 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		alive, err := processIsAlive(pid)
		if err != nil {
			t.Fatal(err)
		}
		if !alive {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived cancellation", pid)
}

func copyHarnessFixture(t *testing.T, name string) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), name)
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return destination
}

func containsArgument(want string) bool { return argumentIndex(want) >= 0 }

func argumentIndex(want string) int {
	for index, argument := range os.Args {
		if argument == want {
			return index
		}
	}
	return -1
}

func mustWorkingDirectory() string {
	directory, _ := os.Getwd()
	return directory
}

func stringPointer(value string) *string { return &value }

func value(number *int) int {
	if number == nil {
		return -1
	}
	return *number
}

func isError(got, want error) bool {
	return got != nil && (got == want || strings.Contains(got.Error(), want.Error()))
}
