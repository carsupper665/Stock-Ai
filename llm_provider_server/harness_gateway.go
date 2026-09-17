package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type webHarnessContinuation struct {
	afterMessages int
	messages      []WebHarnessMessage
}

func isHarnessType(kind string) bool { return kind == "codex" || kind == "claude_code" }

// Each CLI names its own effort scale, and neither rejects an unknown value: Codex passes it
// to the API and Claude Code warns and silently uses its default. The Gateway validates instead,
// so a typo cannot quietly downgrade every Run.
var harnessEffortLevels = map[string]map[string]bool{
	"codex":       {"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true},
	"claude_code": {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
}

func harnessEffort(kind string, options map[string]json.RawMessage) (string, error) {
	for name := range options {
		if name != "reasoning_effort" {
			return "", errors.New("CLI Harness accepts only the reasoning_effort option; executable, authentication files and capabilities are Server-controlled")
		}
	}
	raw, exists := options["reasoning_effort"]
	if !exists {
		return "", nil
	}
	var effort string
	if json.Unmarshal(raw, &effort) != nil || !harnessEffortLevels[kind][effort] {
		return "", fmt.Errorf("reasoning_effort for %s must be one of %s", kind, strings.Join(sortedKeys(harnessEffortLevels[kind]), ", "))
	}
	return effort, nil
}

func sortedKeys(values map[string]bool) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Server) generateHarness(w http.ResponseWriter, r *http.Request, provider Provider, credential, model string, request generateRequest, options map[string]json.RawMessage) {
	effort, err := harnessEffort(provider.Type, options)
	if err != nil {
		writeError(w, 400, "REQUEST_INVALID", err.Error())
		return
	}
	var configuration struct {
		AuthMode string `json:"auth_mode"`
	}
	if json.Unmarshal(provider.Config, &configuration) != nil {
		writeError(w, 400, "PROVIDER_INVALID_CONFIG", "Invalid Harness configuration.")
		return
	}
	executable, authFile := s.codexExecutable, s.codexAuthFile
	if provider.Type == "claude_code" {
		executable, authFile = s.claudeExecutable, s.claudeAuthFile
	}
	if !harnessFileAvailable(executable) {
		log.Printf("web_harness event=error harness=%s category=not_configured", provider.Type)
		writeError(w, 503, "HARNESS_NOT_CONFIGURED", "Configure and install the pinned CLI executable on the Gateway Server, then restart it.")
		return
	}
	if configuration.AuthMode == "local_login" {
		managedClaude := provider.Type == "claude_code" && request.ConversationID != nil
		if authFile == "" || (!managedClaude && !harnessFileAvailable(authFile)) {
			log.Printf("web_harness event=error harness=%s category=login_required", provider.Type)
			writeError(w, 503, "HARNESS_LOGIN_REQUIRED", "The Server has no configured CLI login file. Log in on the Server and configure its authentication file.")
			return
		}
	} else {
		authFile = ""
		if credential == "" {
			log.Printf("web_harness event=error harness=%s category=credential_unavailable", provider.Type)
			writeError(w, 503, "TOKEN_UNAVAILABLE", "Add an enabled Provider API Token, or select configured local CLI login.")
			return
		}
	}
	normalized := WebHarnessRequest{Model: model, Messages: normalizeHarnessMessages(request.Messages), Tools: make([]WebHarnessTool, 0, len(request.Tools))}
	for _, tool := range request.Tools {
		normalized.Tools = append(normalized.Tools, WebHarnessTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.httpClient.Timeout)
	defer cancel()
	var response WebHarnessResponse
	var continuation *webHarnessContinuation
	if request.Continuation != nil {
		continuation = &webHarnessContinuation{afterMessages: request.Continuation.AfterMessages, messages: normalizeHarnessMessages(request.Continuation.Messages)}
	}
	var loginSnapshot []byte
	if provider.Type == "claude_code" && request.ConversationID != nil && authFile != "" {
		loginSnapshot, err = readHarnessLogin(authFile)
		if err != nil {
			log.Printf("web_harness event=error harness=claude_code category=authentication")
			writeError(w, 502, "AUTHENTICATION_FAILED", "CLI authentication failed. Refresh the Server login or Provider API Token.")
			return
		}
	}
	if provider.Type == "codex" {
		response, err = (CodexWebHarness{Executable: executable, AuthFile: authFile, Effort: effort}).Generate(ctx, credential, normalized)
	} else if request.ConversationID != nil {
		key := *request.ConversationID + "\x00" + claudeSessionSignature(provider, model, effort, credential, loginSnapshot, normalized.Tools)
		response, err = s.generateClaudeSession(ctx, key, credential, loginSnapshot, effort, normalized, continuation)
	} else {
		response, err = (ClaudeCodeWebHarness{Executable: executable, AuthFile: authFile, Effort: effort}).Generate(ctx, credential, normalized)
	}
	if err == nil {
		calls := make([]toolCall, 0, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			calls = append(calls, toolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		writeJSON(w, http.StatusOK, generateResponse{Content: response.Content, Reasoning: response.Reasoning,
			ToolCalls: calls, FinishReason: response.FinishReason, Usage: harnessUsage(response.Usage),
			NumTurns: response.NumTurns, TotalCostUSD: response.TotalCostUSD, SessionMode: response.SessionMode,
			FallbackReason: response.FallbackReason, InvocationCount: response.InvocationCount})
		return
	}
	log.Printf("web_harness event=error harness=%s category=%s", provider.Type, harnessErrorCategory(err))
	detail := ""
	if reason := HarnessDetail(err); reason != "" {
		detail = " CLI: " + reason
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, 504, "PROVIDER_TIMEOUT", "CLI invocation exceeded the Provider timeout.")
	case errors.Is(err, context.Canceled):
		writeError(w, 408, "REQUEST_CANCELED", "CLI invocation was canceled.")
	case errors.Is(err, ErrWebHarnessAuthentication):
		writeError(w, 502, "AUTHENTICATION_FAILED", "CLI authentication failed. Refresh the Server login or Provider API Token."+detail)
	case errors.Is(err, ErrWebHarnessVersion):
		writeError(w, 503, "HARNESS_VERSION_MISMATCH", "Install the pinned version shown in the Harness status panel.")
	case errors.Is(err, ErrWebHarnessResponse):
		writeError(w, 502, "PROVIDER_RESPONSE_INVALID", "CLI returned invalid structured output."+detail)
	default:
		writeError(w, 502, "PROVIDER_UNAVAILABLE", "CLI invocation failed. Verify the model ID, authentication and installed executable on the Server."+detail)
	}
}

func normalizeHarnessMessages(messages []message) []WebHarnessMessage {
	normalized := make([]WebHarnessMessage, 0, len(messages))
	for _, message := range messages {
		item := WebHarnessMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID}
		for _, call := range message.ToolCalls {
			var arguments map[string]json.RawMessage
			_ = json.Unmarshal(call.Arguments, &arguments)
			canonical, _ := json.Marshal(arguments)
			item.ToolCalls = append(item.ToolCalls, WebHarnessToolCall{ID: call.ID, Name: call.Name, Arguments: canonical})
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func harnessErrorCategory(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrWebHarnessAuthentication):
		return "authentication"
	case errors.Is(err, ErrWebHarnessVersion):
		return "version"
	case errors.Is(err, ErrWebHarnessResponse):
		return "response"
	default:
		return "unavailable"
	}
}

func claudeSessionSignature(provider Provider, model, effort, credential string, loginSnapshot []byte, tools []WebHarnessTool) string {
	credentialHash := sha256.Sum256([]byte(credential))
	loginHash := sha256.Sum256(loginSnapshot)
	encoded, _ := json.Marshal(struct {
		ProviderID     string           `json:"provider_id"`
		ProviderType   string           `json:"provider_type"`
		ProviderConfig json.RawMessage  `json:"provider_config"`
		Model          string           `json:"model"`
		Effort         string           `json:"effort"`
		Credential     string           `json:"credential"`
		Login          string           `json:"login"`
		Tools          []WebHarnessTool `json:"tools"`
	}{provider.ID, provider.Type, provider.Config, model, effort, hex.EncodeToString(credentialHash[:]), hex.EncodeToString(loginHash[:]), tools})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func harnessUsage(value *WebHarnessUsage) *usage {
	if value == nil {
		return nil
	}
	return &usage{InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CachedTokens: value.CachedTokens, TotalTokens: value.TotalTokens}
}

// Relative paths in .env resolve against the working directory like LLM_SERVER_DB_PATH does;
// harnessFileAvailable and prepareHarness still only accept absolute paths.
func harnessPath(variable string) string {
	value := os.Getenv(variable)
	if value == "" {
		return ""
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	return absolute
}

func harnessFileAvailable(file string) bool {
	if !filepath.IsAbs(file) {
		return false
	}
	info, err := os.Stat(file)
	return err == nil && info.Mode().IsRegular()
}

func (s *Server) harnessStatus(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, s.adminToken) {
		writeError(w, 401, "AUTHENTICATION_FAILED", "Valid admin credentials are required.")
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, 200, map[string]any{"harnesses": []any{
		map[string]any{"type": "codex", "version": codexHarnessVersion, "executable_configured": harnessFileAvailable(s.codexExecutable), "local_login_configured": harnessFileAvailable(s.codexAuthFile), "auth_modes": []string{"api_key", "local_login"}, "default_model": "cli-default"},
		map[string]any{"type": "claude_code", "version": claudeCodeHarnessVersion, "executable_configured": harnessFileAvailable(s.claudeExecutable), "local_login_configured": harnessFileAvailable(s.claudeAuthFile), "auth_modes": []string{"api_key", "local_login"}, "default_model": "claude-sonnet-4-6"},
	}})
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input struct {
		Model  *string `json:"model"`
		Prompt string  `json:"prompt"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, "REQUEST_INVALID", err.Error())
		return
	}
	if input.Prompt == "" {
		input.Prompt = "Reply with a short confirmation that this provider invocation works."
	}
	if len(input.Prompt) > 8192 {
		writeError(w, 400, "REQUEST_INVALID", "Test prompt must not exceed 8192 bytes.")
		return
	}
	body, _ := json.Marshal(generateRequest{Provider: id, Model: input.Model, Messages: []message{{Role: "user", Content: &input.Prompt}}})
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Authorization", "Bearer "+s.runtimeToken)
	request.Header.Set("Content-Type", "application/json")
	s.generate(w, request)
}
