package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"stock-ai/llm-provider-server/internal/providers"
	"stock-ai/llm-provider-server/internal/webharness"
)

func isHarnessType(kind string) bool { return kind == "codex" || kind == "claude_code" }

// generateHarness runs claude_code through its CLI, with per-conversation transport sessions (spec §37).
// The CLI is not streamed: providerTimeout still bounds the whole invocation.
func (s *Server) generateHarness(reply *generateReply, r *http.Request, provider Provider, credential, model string, request providers.GenerateRequest, options map[string]json.RawMessage) {
	setup, err := webharness.ResolveHarness(provider.Type, provider.Config, credential, request.ConversationID != nil, options)
	if err != nil {
		reply.fail(provider, err)
		return
	}
	effort, authFile := setup.Effort, setup.AuthFile
	normalized := webharness.WebHarnessRequest{Model: model, Messages: normalizeHarnessMessages(request.Messages), Tools: make([]webharness.WebHarnessTool, 0, len(request.Tools))}
	for _, tool := range request.Tools {
		normalized.Tools = append(normalized.Tools, webharness.WebHarnessTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.providerTimeout)
	defer cancel()
	var response webharness.WebHarnessResponse
	var continuation *webharness.Continuation
	if request.Continuation != nil {
		continuation = &webharness.Continuation{AfterMessages: request.Continuation.AfterMessages, Messages: normalizeHarnessMessages(request.Continuation.Messages)}
	}
	var loginSnapshot []byte
	if request.ConversationID != nil && authFile != "" {
		loginSnapshot, err = webharness.ReadHarnessLogin(authFile)
		if err != nil {
			log.Printf("web_harness event=error harness=claude_code category=authentication")
			reply.fail(provider, providers.ProviderError{Status: 502, Code: "AUTHENTICATION_FAILED", Message: "CLI authentication failed. Refresh the Server login or Provider API Token."})
			return
		}
	}
	if request.ConversationID != nil {
		key := *request.ConversationID + "\x00" + claudeSessionSignature(provider, model, effort, credential, loginSnapshot, normalized.Tools)
		response, err = webharness.GenerateClaudeSession(ctx, s.claudeSessions, key, credential, loginSnapshot, effort, normalized, continuation)
	} else {
		response, err = (webharness.ClaudeCodeWebHarness{Executable: setup.Executable, AuthFile: authFile, Effort: effort}).Generate(ctx, credential, normalized)
	}
	if err == nil {
		calls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			calls = append(calls, providers.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		reply.succeed(providers.GenerateResponse{Content: response.Content, Reasoning: response.Reasoning,
			ToolCalls: calls, FinishReason: response.FinishReason, Usage: harnessUsage(response.Usage),
			NumTurns: response.NumTurns, TotalCostUSD: response.TotalCostUSD, SessionMode: response.SessionMode,
			FallbackReason: response.FallbackReason, InvocationCount: response.InvocationCount})
		return
	}
	reply.fail(provider, webharness.HarnessProviderError(provider.Type, err))
}

func normalizeHarnessMessages(messages []providers.Message) []webharness.WebHarnessMessage {
	normalized := make([]webharness.WebHarnessMessage, 0, len(messages))
	for _, message := range messages {
		item := webharness.WebHarnessMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID}
		for _, call := range message.ToolCalls {
			var arguments map[string]json.RawMessage
			_ = json.Unmarshal(call.Arguments, &arguments)
			canonical, _ := json.Marshal(arguments)
			item.ToolCalls = append(item.ToolCalls, webharness.WebHarnessToolCall{ID: call.ID, Name: call.Name, Arguments: canonical})
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func claudeSessionSignature(provider Provider, model, effort, credential string, loginSnapshot []byte, tools []webharness.WebHarnessTool) string {
	credentialHash := sha256.Sum256([]byte(credential))
	loginHash := sha256.Sum256(loginSnapshot)
	encoded, _ := json.Marshal(struct {
		ProviderID     string                      `json:"provider_id"`
		ProviderType   string                      `json:"provider_type"`
		ProviderConfig json.RawMessage             `json:"provider_config"`
		Model          string                      `json:"model"`
		Effort         string                      `json:"effort"`
		Credential     string                      `json:"credential"`
		Login          string                      `json:"login"`
		Tools          []webharness.WebHarnessTool `json:"tools"`
	}{provider.ID, provider.Type, provider.Config, model, effort, hex.EncodeToString(credentialHash[:]), hex.EncodeToString(loginHash[:]), tools})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func harnessUsage(value *webharness.WebHarnessUsage) *providers.Usage {
	if value == nil {
		return nil
	}
	return &providers.Usage{InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CachedTokens: value.CachedTokens, TotalTokens: value.TotalTokens}
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
		map[string]any{"type": "codex", "version": webharness.CodexHarnessVersion, "executable_configured": webharness.HarnessFileAvailable(webharness.HarnessPath("LLM_SERVER_CODEX_EXECUTABLE")), "local_login_configured": webharness.HarnessFileAvailable(webharness.HarnessPath("LLM_SERVER_CODEX_AUTH_FILE")), "auth_modes": []string{"api_key", "local_login"}, "default_model": "cli-default"},
		map[string]any{"type": "claude_code", "version": webharness.ClaudeCodeHarnessVersion, "executable_configured": webharness.HarnessFileAvailable(webharness.HarnessPath("LLM_SERVER_CLAUDE_EXECUTABLE")), "local_login_configured": webharness.HarnessFileAvailable(webharness.HarnessPath("LLM_SERVER_CLAUDE_AUTH_FILE")), "auth_modes": []string{"api_key", "local_login"}, "default_model": "claude-sonnet-4-6"},
	}})
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input struct {
		Model   *string                    `json:"model"`
		Prompt  string                     `json:"prompt"`
		Options map[string]json.RawMessage `json:"options"`
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
	if err := validateMappedOptions(input.Options); err != nil {
		writeError(w, 400, "REQUEST_INVALID", "options: "+err.Error())
		return
	}
	// Options let an operator try values before mapping them to a catalog level; without them
	// the call resolves through the catalog like a Session would.
	s.dispatchGenerate(w, r, providers.GenerateRequest{Provider: id, Model: input.Model,
		Messages: []providers.Message{{Role: "user", Content: &input.Prompt}}, Options: input.Options}, len(input.Options) == 0)
}
