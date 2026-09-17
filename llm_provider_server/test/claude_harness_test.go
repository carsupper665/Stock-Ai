package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func TestClaudeCodeHarnessUsesRestrictedToolAllowlistAndNormalizesText(t *testing.T) {
	attackerConfig := t.TempDir()
	t.Setenv("HOME", attackerConfig)
	t.Setenv("USERPROFILE", attackerConfig)
	t.Setenv("CODEX_HOME", attackerConfig)
	t.Setenv("CLAUDE_CONFIG_DIR", attackerConfig)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "attacker-token")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "attacker-token")
	executable := copyHarnessFixture(t, "claude-fixture.exe")
	response, err := (gateway.ClaudeCodeWebHarness{Executable: executable}).Generate(context.Background(), "fixture-claude-key",
		gateway.WebHarnessRequest{Model: "claude-sonnet-4-6", Messages: []gateway.WebHarnessMessage{{Role: "user", Content: stringPointer("search the web")}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content == nil || *response.Content != "Grounded answer." || response.FinishReason != "stop" || len(response.ToolCalls) != 0 {
		t.Fatalf("response = %+v", response)
	}
	if response.Usage == nil || value(response.Usage.InputTokens) != 6309 || value(response.Usage.OutputTokens) != 826 ||
		value(response.Usage.CachedTokens) != 6207 || response.Usage.TotalTokens != nil {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if value(response.Usage.CachedTokens) > value(response.Usage.InputTokens) {
		t.Fatalf("cached_tokens must stay a subset of input_tokens: %+v", response.Usage)
	}
}

func TestClaudeCodeHarnessNormalizesAuthenticationFailureAndInvalidOutput(t *testing.T) {
	for _, test := range []struct {
		model string
		want  error
	}{
		{model: "auth-failure", want: gateway.ErrWebHarnessAuthentication},
		{model: "invalid-output", want: gateway.ErrWebHarnessResponse},
		{model: "invalid-diagnostics", want: gateway.ErrWebHarnessResponse},
		{model: "wrong-version", want: gateway.ErrWebHarnessVersion},
	} {
		t.Run(test.model, func(t *testing.T) {
			executable := copyHarnessFixture(t, "claude-fixture.exe")
			if test.model == "wrong-version" {
				executable = copyHarnessFixture(t, "claude-fixture-wrong-version.exe")
			}
			_, err := (gateway.ClaudeCodeWebHarness{Executable: executable}).Generate(context.Background(), "fixture-claude-key",
				gateway.WebHarnessRequest{Model: test.model, Messages: []gateway.WebHarnessMessage{{Role: "user", Content: stringPointer("hello")}}})
			if !isError(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestClaudeCodeHarnessCancellationKillsTheInvocation(t *testing.T) {
	executable := copyHarnessFixture(t, "claude-fixture.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (gateway.ClaudeCodeWebHarness{Executable: executable}).Generate(ctx, "fixture-claude-key",
		gateway.WebHarnessRequest{Model: "hang", Messages: []gateway.WebHarnessMessage{{Role: "user", Content: stringPointer("hello")}}})
	if !isError(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestClaudeCodeHarnessWithoutConversationRemainsStateless(t *testing.T) {
	executable := copyHarnessFixture(t, "claude-fixture.exe")
	response, err := (gateway.ClaudeCodeWebHarness{Executable: executable}).Generate(context.Background(), "fixture-claude-key",
		gateway.WebHarnessRequest{Model: "stateless-report", Messages: []gateway.WebHarnessMessage{{Role: "user", Content: stringPointer("hello")}}})
	if err != nil || response.Content == nil {
		t.Fatalf("Generate() response=%+v error=%v", response, err)
	}
	if _, err := os.Stat(*response.Content); !os.IsNotExist(err) {
		t.Fatalf("stateless working directory retained: %q error=%v", *response.Content, err)
	}
}

func runClaudeFixture() int {
	if containsArgument("--version") {
		if os.Getenv("CODEX_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
			return 13
		}
		if strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), "wrong-version") {
			fmt.Println("2.1.268 (Claude Code)")
		} else {
			fmt.Println("2.1.269 (Claude Code)")
		}
		return 0
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if fixturePromptModel(prompt) == "hang" {
		time.Sleep(30 * time.Second)
		return 1
	}
	if containsArgument("--session-id") || containsArgument("--resume") {
		return runManagedClaudeFixture(prompt)
	}
	if !claudeInvocationIsRestricted() {
		return 2
	}
	if strings.Contains(string(prompt), `"model":"auth-failure"`) {
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"authentication failed: 401"}`)
		return 1
	}
	if strings.Contains(string(prompt), `"model":"invalid-output"`) {
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"structured_output":{"content":null}}`)
		return 0
	}
	if strings.Contains(string(prompt), `"model":"invalid-diagnostics"`) {
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"structured_output":{"content":"bad","tool_calls":[],"finish_reason":"stop"},"num_turns":-1,"total_cost_usd":-0.1}`)
		return 0
	}
	if strings.Contains(string(prompt), `"model":"diagnostic-secret"`) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": "usage limit reached " + strings.Repeat("x", 295) + diagnosticSecret,
		})
		fmt.Fprintln(os.Stderr, "stderr "+diagnosticSecret)
		return 0
	}
	if strings.Contains(string(prompt), `"model":"diagnostic-model-secret"`) {
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"unknown model ` + diagnosticSecret + `"}`)
		return 0
	}
	if strings.Contains(string(prompt), `"model":"diagnostic-login-secret"`) {
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"login expired ` + diagnosticSecret + `"}`)
		return 0
	}
	if strings.Contains(string(prompt), `"model":"diagnostic-stderr-secret"`) {
		fmt.Fprintln(os.Stderr, strings.Repeat("x", 295)+diagnosticSecret)
		return 1
	}
	if strings.Contains(string(prompt), `"model":"effort-report"`) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"type": "result", "subtype": "success", "is_error": false,
			"structured_output": map[string]any{"content": fixtureClaudeEffort(), "tool_calls": []any{}, "finish_reason": "stop"},
		})
		return 0
	}
	if strings.Contains(string(prompt), `"model":"stateless-report"`) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"type": "result", "subtype": "success", "is_error": false,
			"structured_output": map[string]any{"content": mustWorkingDirectory(), "tool_calls": []any{}, "finish_reason": "stop"},
		})
		return 0
	}
	output := map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"structured_output": map[string]any{"content": "Grounded answer.", "tool_calls": []any{}, "finish_reason": "stop"},
		"usage":             map[string]any{"input_tokens": 2, "output_tokens": 826, "cache_creation_input_tokens": 100, "cache_read_input_tokens": 6207},
	}
	_ = json.NewEncoder(os.Stdout).Encode(output)
	return 0
}

func fixturePromptModel(prompt []byte) string {
	parts := strings.SplitN(string(prompt), "The normalized request follows:\n", 2)
	if len(parts) != 2 {
		return ""
	}
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal([]byte(parts[1]), &request)
	return request.Model
}

type managedClaudeMarker struct {
	SessionID string `json:"session_id"`
}

func runManagedClaudeFixture(prompt []byte) int {
	if !managedClaudeInvocationIsRestricted() {
		return 21
	}
	workingDirectory := mustWorkingDirectory()
	markerPath := filepath.Join(workingDirectory, ".fixture-session.json")
	if containsArgument("--session-id") {
		sessionID := argumentValue("--session-id")
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(sessionID) || containsArgument("--resume") {
			return 22
		}
		if _, err := os.Stat(markerPath); err == nil {
			return 29
		}
		marker, _ := json.Marshal(managedClaudeMarker{SessionID: sessionID})
		if os.WriteFile(markerPath, marker, 0600) != nil {
			return 23
		}
		var request gateway.WebHarnessRequest
		parts := strings.SplitN(string(prompt), "The normalized request follows:\n", 2)
		if len(parts) != 2 || json.Unmarshal([]byte(parts[1]), &request) != nil || strings.Index(parts[1], `"tools"`) > strings.Index(parts[1], `"messages"`) {
			return 24
		}
		authOK := managedFixtureAuth(request.Messages, workingDirectory)
		content := fmt.Sprintf(`{"kind":"cold","session_id":%q,"directory":%q,"messages":%d,"assistant_messages":%d,"prompt_bytes":%d,"auth_ok":%t}`,
			sessionID, workingDirectory, len(request.Messages), countAssistantMessages(request.Messages), len(prompt), authOK)
		calls := []any{}
		finishReason := "stop"
		if len(request.Messages) == 2 && request.Messages[1].Content != nil && *request.Messages[1].Content == "need-tool" {
			content = ""
			calls = []any{map[string]any{"id": "managed-call", "name": "fixture_tool", "arguments": `{}`}}
			finishReason = "tool_call"
		}
		return writeManagedClaudeResult(content, calls, finishReason)
	}
	data, err := os.ReadFile(markerPath)
	var marker managedClaudeMarker
	if err != nil || json.Unmarshal(data, &marker) != nil || marker.SessionID != argumentValue("--resume") || containsArgument("--model") || containsArgument("--effort") || containsArgument("--session-id") {
		return 25
	}
	prefix := "Continue with these new normalized messages. Return only the JSON object required by the output schema:\n"
	if !strings.HasPrefix(string(prompt), prefix) {
		return 26
	}
	var messages []gateway.WebHarnessMessage
	if json.Unmarshal(prompt[len(prefix):], &messages) != nil {
		return 27
	}
	if len(messages) == 0 {
		return 28
	}
	for _, message := range messages {
		if message.Content != nil && strings.Contains(*message.Content, "resume-fail") {
			_ = managedFixtureAuth(messages, workingDirectory)
			fmt.Fprintln(os.Stderr, "fixture resume failed")
			return 1
		}
	}
	content := fmt.Sprintf(`{"kind":"resume","session_id":%q,"directory":%q,"messages":%d,"assistant_messages":%d,"prompt_bytes":%d,"auth_ok":%t}`,
		marker.SessionID, workingDirectory, len(messages), countAssistantMessages(messages), len(prompt), managedFixtureAuth(messages, workingDirectory))
	return writeManagedClaudeResult(content, []any{}, "stop")
}

func countAssistantMessages(messages []gateway.WebHarnessMessage) int {
	count := 0
	for _, message := range messages {
		if message.Role == "assistant" {
			count++
		}
	}
	return count
}

func managedFixtureAuth(messages []gateway.WebHarnessMessage, workingDirectory string) bool {
	expected := ""
	barrier := ""
	for _, message := range messages {
		if message.Content == nil {
			continue
		}
		for _, field := range strings.Split(*message.Content, ";") {
			if value, ok := strings.CutPrefix(field, "expect-auth="); ok {
				expected = value
			}
			if value, ok := strings.CutPrefix(field, "hold="); ok {
				barrier = value
			}
		}
	}
	if barrier != "" {
		_ = os.WriteFile(filepath.Join(barrier, "started"), []byte("started"), 0o600)
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(barrier, "release")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if expected == "" {
		return true
	}
	data, err := os.ReadFile(filepath.Join(workingDirectory, ".credentials.json"))
	var login struct {
		FixtureLogin string `json:"fixture_login"`
	}
	return err == nil && json.Unmarshal(data, &login) == nil && login.FixtureLogin == expected
}

func writeManagedClaudeResult(content string, calls []any, finishReason string) int {
	var normalizedContent any = content
	if finishReason == "tool_call" {
		normalizedContent = nil
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"structured_output": map[string]any{"content": normalizedContent, "tool_calls": calls, "finish_reason": finishReason},
		"num_turns":         2, "total_cost_usd": 0.125,
	})
	return 0
}

func argumentValue(name string) string {
	index := argumentIndex(name)
	if index < 0 || index+1 >= len(os.Args) {
		return ""
	}
	return os.Args[index+1]
}

func managedClaudeInvocationIsRestricted() bool {
	joined := "\x00" + strings.Join(os.Args[1:], "\x00") + "\x00"
	for _, argument := range []string{
		"\x00--restricted\x00", "\x00--safe-mode\x00", "\x00--setting-sources\x00\x00", "\x00--strict-mcp-config\x00",
		"\x00--tools\x00WebSearch,WebFetch\x00", "\x00--allowedTools\x00WebSearch,WebFetch\x00", "\x00--disallowedTools\x00mcp__*\x00",
		"\x00--permission-mode\x00dontAsk\x00", "\x00--permission-prompts\x00none\x00", "\x00--disable-slash-commands\x00", "\x00--no-chrome\x00",
	} {
		if !strings.Contains(joined, argument) {
			return false
		}
	}
	if containsArgument("--no-session-persistence") {
		return false
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "fixture-claude-key" {
		return true
	}
	return os.Getenv("ANTHROPIC_API_KEY") == "" && harnessFixtureLoginValid(filepath.Join(mustWorkingDirectory(), ".credentials.json"))
}

func harnessFixtureLoginValid(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Valid(data)
}

func fixtureClaudeEffort() string {
	for index, argument := range os.Args {
		if argument == "--effort" && index+1 < len(os.Args) {
			return os.Args[index+1]
		}
	}
	return "none"
}

func claudeInvocationIsRestricted() bool {
	joined := "\x00" + strings.Join(os.Args[1:], "\x00") + "\x00"
	required := []string{
		"\x00--restricted\x00", "\x00--safe-mode\x00", "\x00--setting-sources\x00\x00", "\x00--strict-mcp-config\x00",
		"\x00--tools\x00WebSearch,WebFetch\x00", "\x00--allowedTools\x00WebSearch,WebFetch\x00",
		"\x00--disallowedTools\x00mcp__*\x00", "\x00--permission-mode\x00dontAsk\x00",
		"\x00--permission-prompts\x00none\x00", "\x00--no-session-persistence\x00", "\x00--disable-slash-commands\x00",
		"\x00--no-chrome\x00", "\x00-p\x00", "\x00--output-format\x00json\x00", "\x00--json-schema\x00",
	}
	for _, argument := range required {
		if !strings.Contains(joined, argument) {
			return false
		}
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "fixture-claude-key" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		return false
	}
	workingDirectory := mustWorkingDirectory()
	for _, name := range []string{"HOME", "USERPROFILE", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if os.Getenv(name) != workingDirectory {
			return false
		}
	}
	if os.Getenv("PATH") != "" || os.Getenv("APPDATA") != "" {
		return false
	}
	schemaIndex := argumentIndex("--json-schema")
	if schemaIndex < 0 || schemaIndex+1 >= len(os.Args) || !json.Valid([]byte(os.Args[schemaIndex+1])) {
		return false
	}
	return strings.HasPrefix(filepath.Base(mustWorkingDirectory()), "stock-ai-web-harness-")
}
