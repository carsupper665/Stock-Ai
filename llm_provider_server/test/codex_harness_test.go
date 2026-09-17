package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
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

func TestCodexHarnessPinsCapabilitiesAndNormalizesStructuredToolReturn(t *testing.T) {
	attackerConfig := t.TempDir()
	t.Setenv("HOME", attackerConfig)
	t.Setenv("USERPROFILE", attackerConfig)
	t.Setenv("CODEX_HOME", attackerConfig)
	t.Setenv("CLAUDE_CONFIG_DIR", attackerConfig)
	t.Setenv("OPENAI_API_KEY", "attacker-key")
	executable := copyHarnessFixture(t, "codex-fixture.exe")
	harness := gateway.CodexWebHarness{Executable: executable}
	content := "Check BTC using current public information."
	response, err := harness.Generate(context.Background(), "fixture-codex-key", gateway.WebHarnessRequest{
		Model:    "gpt-5.4",
		Messages: []gateway.WebHarnessMessage{{Role: "user", Content: &content}},
		Tools: []gateway.WebHarnessTool{{
			Name: "market.price", Description: "Return the current market price.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"symbol":{"type":"string"}},"required":["symbol"]}`),
		}},
	})
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
		value(response.Usage.CachedTokens) != 4 || response.Usage.TotalTokens != nil {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCodexHarnessRejectsWrongVersionAndInvalidOutput(t *testing.T) {
	for _, test := range []struct {
		model string
		want  error
	}{
		{model: "wrong-version", want: gateway.ErrWebHarnessVersion},
		{model: "invalid-output", want: gateway.ErrWebHarnessResponse},
	} {
		t.Run(test.model, func(t *testing.T) {
			executable := copyHarnessFixture(t, "codex-fixture.exe")
			if test.model == "wrong-version" {
				executable = copyHarnessFixture(t, "codex-fixture-wrong-version.exe")
			}
			_, err := (gateway.CodexWebHarness{Executable: executable}).Generate(context.Background(), "fixture-codex-key",
				gateway.WebHarnessRequest{Model: test.model, Messages: []gateway.WebHarnessMessage{{Role: "user", Content: stringPointer("hello")}}})
			if !isError(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCodexHarnessCancellationKillsTheInvocation(t *testing.T) {
	executable := copyHarnessFixture(t, "codex-fixture.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (gateway.CodexWebHarness{Executable: executable}).Generate(ctx, "fixture-codex-key",
		gateway.WebHarnessRequest{Model: "hang", Messages: []gateway.WebHarnessMessage{{Role: "user", Content: stringPointer("hello")}}})
	if !isError(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestCodexHarnessRejectsOversizedOutputWithoutWaitingForTimeout(t *testing.T) {
	executable := copyHarnessFixture(t, "codex-fixture.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	_, err := (gateway.CodexWebHarness{Executable: executable}).Generate(ctx, "fixture-codex-key",
		gateway.WebHarnessRequest{Model: "oversized-output", Messages: []gateway.WebHarnessMessage{{Role: "user", Content: stringPointer("hello")}}})
	if !isError(err, gateway.ErrWebHarnessResponse) {
		t.Fatalf("error = %v, want invalid response", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("oversized output took %s", elapsed)
	}
}

func TestCodexHarnessCancellationKillsDescendants(t *testing.T) {
	if !processTreeTestSupported() {
		t.Skip("process liveness probe is unavailable on this platform")
	}
	executable := copyHarnessFixture(t, "codex-fixture.exe")
	marker := filepath.Join(t.TempDir(), "descendant.pid")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := (gateway.CodexWebHarness{Executable: executable}).Generate(ctx, "fixture-codex-key",
			gateway.WebHarnessRequest{Model: "spawn-descendant", Messages: []gateway.WebHarnessMessage{{Role: "user", Content: &marker}}})
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
	if alive, err := processIsAlive(pid); err != nil || !alive {
		t.Fatalf("descendant was not alive before cancellation: alive=%t error=%v", alive, err)
	}
	cancel()
	select {
	case err := <-result:
		if !isError(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Generate did not return after cancellation")
	}
	waitForProcessExit(t, pid)
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
			fmt.Println("codex-cli 0.144.0")
		} else {
			fmt.Println("codex-cli 0.145.0")
		}
		return 0
	}
	prompt, _ := io.ReadAll(os.Stdin)
	request := fixtureHarnessRequest(prompt)
	if strings.Contains(string(prompt), `"model":"hang"`) {
		time.Sleep(30 * time.Second)
		return 1
	}
	if !codexInvocationIsRestricted(request) {
		return 2
	}
	if strings.Contains(string(prompt), `"model":"invalid-output"`) {
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
		return 0
	}
	if request.Model == "oversized-output" {
		chunk := strings.Repeat("x", 64<<10)
		for index := 0; index < 32; index++ {
			fmt.Print(chunk)
		}
		return 0
	}
	if request.Model == "diagnostic-secret" {
		fmt.Printf("{\"type\":\"turn.failed\",\"error\":{\"message\":%q}}\n",
			"usage limit reached "+strings.Repeat("x", 295)+diagnosticSecret)
		fmt.Fprintln(os.Stderr, "stderr "+diagnosticSecret)
		return 0
	}
	if request.Model == "diagnostic-model-secret" {
		fmt.Printf("{\"type\":\"turn.failed\",\"error\":{\"message\":%q}}\n", "unknown model "+diagnosticSecret)
		return 0
	}
	if request.Model == "diagnostic-login-secret" {
		fmt.Printf("{\"type\":\"turn.failed\",\"error\":{\"message\":%q}}\n", "login expired "+diagnosticSecret)
		return 0
	}
	if request.Model == "diagnostic-stderr-secret" {
		fmt.Fprintln(os.Stderr, strings.Repeat("x", 295)+diagnosticSecret)
		return 1
	}
	if request.Model == "auth-failure" {
		fmt.Println(`{"type":"turn.failed","error":{"message":"authentication failed: 401"}}`)
		return 0
	}
	if strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), "-shutdown") {
		signal.Ignore(os.Interrupt)
		marker := *request.Messages[0].Content
		command := exec.Command(os.Args[0], "codex-descendant", marker+".descendant")
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Start(); err != nil {
			return 4
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(marker + ".descendant"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				_ = command.Process.Kill()
				return 5
			}
			time.Sleep(10 * time.Millisecond)
		}
		state, _ := json.Marshal(shutdownHarnessState{
			CLIPID: os.Getpid(), DescendantPID: command.Process.Pid, WorkingDirectory: mustWorkingDirectory(),
		})
		_ = command.Process.Release()
		if err := os.WriteFile(marker, state, 0o600); err != nil {
			return 6
		}
		time.Sleep(30 * time.Second)
		return 1
	}
	if request.Model == "spawn-descendant" {
		command := exec.Command(os.Args[0], "codex-descendant", *request.Messages[0].Content)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Start(); err != nil {
			return 4
		}
		_ = command.Process.Release()
		time.Sleep(30 * time.Second)
		return 1
	}
	if request.Model == "effort-report" {
		reported := fmt.Sprintf(`{"content":%q,"tool_calls":[],"finish_reason":"stop"}`, fixtureCodexEffort())
		fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":%q}}\n", reported)
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
		return 0
	}
	if request.Model == "prompt-order-report" {
		tools := bytes.Index(prompt, []byte(`"tools":`))
		messages := bytes.Index(prompt, []byte(`"messages":`))
		content := "messages-before-tools"
		if tools >= 0 && tools < messages {
			content = "tools-before-messages"
		}
		reported := fmt.Sprintf(`{"content":%q,"tool_calls":[],"finish_reason":"stop"}`, content)
		fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":%q}}\n", reported)
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
		return 0
	}
	if request.Model == "continuation-full-context" {
		content := fmt.Sprintf("messages=%d continuation_serialized=%t", len(request.Messages), bytes.Contains(prompt, []byte(`"continuation"`)))
		reported := fmt.Sprintf(`{"content":%q,"tool_calls":[],"finish_reason":"stop"}`, content)
		fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":%q}}\n", reported)
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
		return 0
	}
	result := `{"content":null,"tool_calls":[{"id":"call-price","name":"market.price","arguments":"{\"symbol\":\"BTCUSDT\"}"}],"finish_reason":"tool_call"}`
	fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":%q}}\n", result)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":4,"output_tokens":3}}`)
	return 0
}

func fixtureHarnessRequest(prompt []byte) gateway.WebHarnessRequest {
	var request gateway.WebHarnessRequest
	start := bytes.Index(prompt, []byte(`{"model"`))
	if start >= 0 {
		_ = json.Unmarshal(prompt[start:], &request)
	}
	return request
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

// Codex takes its effort as a TOML config override rather than a flag.
func fixtureCodexEffort() string {
	for _, argument := range os.Args {
		if strings.HasPrefix(argument, `model_reasoning_effort="`) {
			return strings.TrimSuffix(strings.TrimPrefix(argument, `model_reasoning_effort="`), `"`)
		}
	}
	return "none"
}

func codexInvocationIsRestricted(request gateway.WebHarnessRequest) bool {
	schemaPath := filepath.Join(mustWorkingDirectory(), "response-schema.json")
	expected := []string{
		"exec", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "-c", `web_search="live"`,
	}
	if effort := fixtureCodexEffort(); effort != "none" {
		expected = append(expected, "-c", `model_reasoning_effort="`+effort+`"`)
	}
	if request.Model != "cli-default" {
		expected = append(expected, "-m", request.Model)
	}
	for _, feature := range []string{
		"shell_tool", "apps", "multi_agent", "goals", "hooks", "memories", "remote_plugin", "plugins",
		"browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "image_generation",
		"code_mode", "code_mode_host", "skill_search", "tool_suggest", "workspace_dependencies",
	} {
		expected = append(expected, "--disable", feature)
	}
	expected = append(expected, "--json", "--output-schema", schemaPath, "-")
	if !reflect.DeepEqual(os.Args[1:], expected) {
		return false
	}
	local := strings.Contains(filepath.Base(os.Args[0]), "-local")
	if local {
		body, err := os.ReadFile(filepath.Join(mustWorkingDirectory(), "auth.json"))
		if err != nil || string(body) != `{"fixture_login":true}` || os.Getenv("CODEX_API_KEY") != "" || containsArgument("-m") {
			return false
		}
	}
	if (!local && os.Getenv("CODEX_API_KEY") != "fixture-codex-key") || os.Getenv("OPENAI_API_KEY") != "" {
		return false
	}
	workingDirectory := mustWorkingDirectory()
	for _, name := range []string{"HOME", "USERPROFILE", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if os.Getenv(name) != workingDirectory {
			return false
		}
	}
	allowedEnvironment := map[string]bool{
		"HOME": true, "USERPROFILE": true, "CODEX_HOME": true, "CLAUDE_CONFIG_DIR": true,
		"CODEX_API_KEY": true, "SystemRoot": true, "WINDIR": true, "TEMP": true, "TMP": true,
		"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !allowedEnvironment[name] {
			return false
		}
	}
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return false
	}
	expectedSchema := []byte(`{"type":"object","properties":{"content":{"type":["string","null"]},"reasoning":{"type":["string","null"]},"tool_calls":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"},"arguments":{"type":"string"}},"required":["id","name","arguments"],"additionalProperties":false}},"finish_reason":{"type":"string","enum":["stop","length","content_filter","tool_call"]}},"required":["content","tool_calls","finish_reason"],"additionalProperties":false}`)
	var compact bytes.Buffer
	if json.Compact(&compact, schema) != nil || !bytes.Equal(compact.Bytes(), expectedSchema) {
		return false
	}
	return strings.HasPrefix(filepath.Base(workingDirectory), "stock-ai-web-harness-")
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
