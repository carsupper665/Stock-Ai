package providers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxResponseBody = 1 << 20

func CompatibleCall(providerType, baseURL, model string, request GenerateRequest, options map[string]json.RawMessage, WireByRuntime map[string]string) (ProviderCall, error) {
	openAI := providerType == "openai"
	if openAI {
		var err error
		options, err = normalizeOpenAIOptions(options, request.Tools, WireByRuntime)
		if err != nil {
			return ProviderCall{}, err
		}
	}
	upstreamRequest := map[string]any{"model": model, "messages": compatibleMessages(request.Messages, WireByRuntime, openAI)}
	if len(request.Tools) != 0 {
		upstreamRequest["tools"] = compatibleTools(request.Tools, WireByRuntime)
	}
	for key, value := range options {
		if key == "model" || key == "messages" || key == "tools" {
			return ProviderCall{}, errors.New("options cannot replace model, messages, or tools.")
		}
		upstreamRequest[key] = value
	}
	body, err := json.Marshal(upstreamRequest)
	if err != nil {
		return ProviderCall{}, errors.New("Options contain an invalid value.")
	}
	return ProviderCall{URL: strings.TrimRight(baseURL, "/") + "/chat/completions", Headers: map[string]string{}, Body: body}, nil
}

// callProvider performs the single upstream request. It writes the transport, credential, and
// rate-limit errors shared by every Provider type and returns nil when it did.
func UpstreamStatusFailure(upstream *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(upstream.Body, maxUpstreamFailureBody))
	message := fmt.Sprintf("Provider returned HTTP %d.", upstream.StatusCode)
	if category := UpstreamFailureCategory(body); category != "" {
		message += " " + category
	}
	return ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", message}
}

func UpstreamFailureCategory(body []byte) string {
	var failure struct {
		Error struct {
			Code   string `json:"code"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"error"`
		Code   string `json:"code"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &failure) != nil {
		return ""
	}
	for _, value := range []string{failure.Error.Code, failure.Error.Type, failure.Error.Status, failure.Code, failure.Type, failure.Status} {
		switch strings.ToLower(value) {
		case "model_not_found", "model_not_found_error", "unknown_model":
			return "Provider does not provide the requested model."
		case "insufficient_quota", "quota_exceeded", "resource_exhausted", "billing_hard_limit_reached", "usage_limit_reached":
			return "Provider quota is exhausted."
		case "authentication_expired", "login_expired", "oauth_token_expired", "token_expired":
			return "Provider login or credential has expired."
		}
	}
	return ""
}

func ValidateGenerateRequest(request *GenerateRequest) error {
	request.Provider = strings.TrimSpace(request.Provider)
	if !ProviderIDPattern.MatchString(request.Provider) {
		return errors.New("provider must be a valid provider ID")
	}
	if len(request.Messages) == 0 {
		return errors.New("messages must contain at least one message")
	}
	if request.ConversationID != nil {
		if *request.ConversationID == "" || utf8.RuneCountInString(*request.ConversationID) > 128 || strings.IndexFunc(*request.ConversationID, unicode.IsControl) >= 0 {
			return errors.New("conversation_id must be 1-128 characters with no control characters")
		}
	}
	if request.Continuation != nil && request.Continuation.AfterMessages <= 0 {
		return errors.New("continuation.after_messages must be positive")
	}
	pendingCalls := map[string]bool{}
	seenCalls := map[string]bool{}
	for _, message := range request.Messages {
		switch message.Role {
		case "system", "user":
			if message.Content == nil || len(message.ToolCalls) != 0 || message.ToolCallID != nil {
				return errors.New("system and user messages require string content and no tool fields")
			}
		case "assistant":
			if message.ToolCallID != nil || (message.Content == nil && len(message.ToolCalls) == 0) {
				return errors.New("assistant messages require content or tool_calls and no tool_call_id")
			}
			for _, call := range message.ToolCalls {
				if err := ValidateToolCall(call); err != nil {
					return err
				}
				if seenCalls[call.ID] {
					return errors.New("tool call IDs must be unique")
				}
				seenCalls[call.ID] = true
				pendingCalls[call.ID] = true
			}
		case "tool":
			if len(message.ToolCalls) != 0 || message.ToolCallID == nil || strings.TrimSpace(*message.ToolCallID) == "" || !pendingCalls[*message.ToolCallID] {
				return errors.New("tool messages must reference an unresolved assistant tool call")
			}
			delete(pendingCalls, *message.ToolCallID)
		default:
			return errors.New("message role must be system, user, assistant, or tool")
		}
	}
	toolNames := map[string]bool{}
	for _, tool := range request.Tools {
		if !ModelNamePattern.MatchString(tool.Name) || !ValidJSONObject(tool.InputSchema) || toolNames[tool.Name] {
			return errors.New("tools require unique valid names and object input_schema values")
		}
		toolNames[tool.Name] = true
	}
	return nil
}

func ValidateToolCall(call ToolCall) error {
	if strings.TrimSpace(call.ID) == "" || !ModelNamePattern.MatchString(call.Name) || !ValidJSONObject(call.Arguments) {
		return errors.New("tool calls require an ID, valid name, and object arguments")
	}
	return nil
}

func ValidJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || IsJSONNull(value) {
		return false
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(value))
	if decoder.Decode(&object) != nil || object == nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func NewToolAliases(request GenerateRequest) ToolAliases {
	names := map[string]bool{}
	for _, tool := range request.Tools {
		names[tool.Name] = true
	}
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			names[call.Name] = true
		}
	}
	sortedNames := make([]string, 0, len(names))
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	aliases := ToolAliases{WireByRuntime: map[string]string{}, RuntimeByWire: map[string]string{}}
	for _, name := range sortedNames {
		if compatibleToolNamePattern.MatchString(name) {
			aliases.WireByRuntime[name] = name
			aliases.RuntimeByWire[name] = name
		}
	}
	for _, name := range sortedNames {
		if _, exists := aliases.WireByRuntime[name]; exists {
			continue
		}
		for attempt := 0; ; attempt++ {
			wireName := compatibleToolAlias(name, attempt)
			if _, collision := aliases.RuntimeByWire[wireName]; collision {
				continue
			}
			aliases.WireByRuntime[name] = wireName
			aliases.RuntimeByWire[wireName] = name
			break
		}
	}
	return aliases
}

func compatibleToolAlias(name string, attempt int) string {
	var prefix strings.Builder
	for _, character := range name {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			prefix.WriteRune(character)
		} else {
			prefix.WriteByte('_')
		}
	}
	seed := name
	if attempt != 0 {
		seed = fmt.Sprintf("%s\x00%d", name, attempt)
	}
	digest := sha256.Sum256([]byte(seed))
	suffix := hex.EncodeToString(digest[:8])
	base := prefix.String()
	maxBaseLength := 64 - len(suffix) - 1
	if len(base) > maxBaseLength {
		base = base[:maxBaseLength]
	}
	if base == "" {
		base = "tool"
	}
	return base + "_" + suffix
}

func HistoricalToolCallIDs(messages []Message) map[string]bool {
	ids := map[string]bool{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			ids[call.ID] = true
		}
	}
	return ids
}

func compatibleMessages(messages []Message, WireByRuntime map[string]string, openAI bool) []map[string]any {
	converted := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		item := map[string]any{"role": message.Role, "content": message.Content}
		if openAI && message.Role == "tool" && message.Content == nil {
			item["content"] = ""
		}
		if message.ToolCallID != nil {
			item["tool_call_id"] = *message.ToolCallID
		}
		if len(message.ToolCalls) != 0 {
			calls := make([]map[string]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{
					"name": WireByRuntime[call.Name], "arguments": string(call.Arguments),
				}})
			}
			item["tool_calls"] = calls
		}
		converted = append(converted, item)
	}
	return converted
}

func compatibleTools(tools []ToolSchema, WireByRuntime map[string]string) []map[string]any {
	converted := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		converted = append(converted, map[string]any{"type": "function", "function": map[string]any{
			"name": WireByRuntime[tool.Name], "description": tool.Description, "parameters": tool.InputSchema,
		}})
	}
	return converted
}

func normalizeOpenAIOptions(options map[string]json.RawMessage, tools []ToolSchema, WireByRuntime map[string]string) (map[string]json.RawMessage, error) {
	for name := range options {
		if !OpenAIOptionNames[name] {
			return nil, fmt.Errorf("OpenAI option %s is not supported by the normalized Generate contract", name)
		}
	}
	choice, exists := options["tool_choice"]
	if !exists {
		return options, nil
	}
	var mode string
	if json.Unmarshal(choice, &mode) == nil {
		if mode != "none" && mode != "auto" && mode != "required" {
			return nil, errors.New("OpenAI tool_choice must be none, auto, required, or a declared function")
		}
		return options, nil
	}
	var named struct {
		Type     string `json:"type"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	decoder := json.NewDecoder(bytes.NewReader(choice))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&named) != nil || named.Type != "function" || named.Function == nil ||
		!errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return nil, errors.New("OpenAI tool_choice must be none, auto, required, or a declared function")
	}
	declared := false
	for _, tool := range tools {
		declared = declared || tool.Name == named.Function.Name
	}
	if !declared {
		return nil, errors.New("OpenAI tool_choice function must name a declared tool")
	}
	named.Function.Name = WireByRuntime[named.Function.Name]
	normalizedChoice, err := json.Marshal(named)
	if err != nil {
		return nil, errors.New("OpenAI tool_choice is invalid")
	}
	normalized := make(map[string]json.RawMessage, len(options))
	for key, value := range options {
		normalized[key] = value
	}
	normalized["tool_choice"] = normalizedChoice
	return normalized, nil
}

func isOpenAIModelNotFound(reader io.Reader) (bool, error) {
	body, err := ReadProviderBody(reader)
	if err != nil {
		return false, err
	}
	var response struct {
		Error struct {
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&response) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return false, nil
	}
	return response.Error.Code == "model_not_found" || response.Error.Param == "model", nil
}

const maxUpstreamFailureBody = 8 << 10

func NormalizeCompatibleResponse(upstream *http.Response, openAI bool, RuntimeByWire map[string]string, historyIDs map[string]bool) (GenerateResponse, error) {
	if openAI && (upstream.StatusCode == http.StatusBadRequest || upstream.StatusCode == http.StatusNotFound) {
		modelNotFound, err := isOpenAIModelNotFound(upstream.Body)
		if err != nil {
			if IsTimeout(err) {
				return GenerateResponse{}, err
			}
			return GenerateResponse{}, ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", "Provider request failed."}
		}
		if modelNotFound {
			return GenerateResponse{}, ProviderError{http.StatusBadRequest, "MODEL_NOT_FOUND", "OpenAI does not provide the requested model to this credential."}
		}
		if upstream.StatusCode == http.StatusBadRequest {
			return GenerateResponse{}, ProviderError{http.StatusBadRequest, "REQUEST_INVALID", "OpenAI rejected the generated request."}
		}
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return GenerateResponse{}, UpstreamStatusFailure(upstream)
	}
	body, err := ReadProviderBody(upstream.Body)
	if err != nil {
		return GenerateResponse{}, err
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content          *string `json:"content"`
				Reasoning        *string `json:"reasoning"`
				ReasoningContent *string `json:"reasoning_content"`
				Refusal          *string `json:"refusal"`
				Role             string  `json:"role"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string  `json:"name"`
						Arguments *string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			TotalTokens      *int `json:"total_tokens"`
			PromptDetails    *struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&response); err != nil || len(response.Choices) == 0 || response.Choices[0].FinishReason == "" {
		return GenerateResponse{}, errors.New("invalid compatible response")
	}
	if openAI && (len(response.Choices) != 1 || response.Choices[0].Message.Role != "assistant") {
		return GenerateResponse{}, errors.New("invalid OpenAI response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return GenerateResponse{}, errors.New("invalid compatible response")
	}
	if response.Usage != nil && ((response.Usage.PromptTokens != nil && *response.Usage.PromptTokens < 0) ||
		(response.Usage.CompletionTokens != nil && *response.Usage.CompletionTokens < 0) ||
		(response.Usage.TotalTokens != nil && *response.Usage.TotalTokens < 0) ||
		(response.Usage.PromptDetails != nil && response.Usage.PromptDetails.CachedTokens != nil && *response.Usage.PromptDetails.CachedTokens < 0)) {
		return GenerateResponse{}, errors.New("invalid compatible response")
	}
	choice := response.Choices[0]
	content := choice.Message.Content
	reasoning := choice.Message.ReasoningContent
	if reasoning == nil {
		reasoning = choice.Message.Reasoning
	}
	if openAI && content == nil && choice.Message.Refusal != nil {
		content = choice.Message.Refusal
	}
	finishReason := choice.FinishReason
	toolCalls := make([]ToolCall, 0, len(choice.Message.ToolCalls))
	if len(choice.Message.ToolCalls) != 0 {
		if finishReason != "tool_calls" {
			return GenerateResponse{}, errors.New("invalid compatible response")
		}
		finishReason = "tool_call"
		seenIDs := make(map[string]bool, len(historyIDs)+len(choice.Message.ToolCalls))
		for id := range historyIDs {
			seenIDs[id] = true
		}
		for _, CompatibleCall := range choice.Message.ToolCalls {
			if CompatibleCall.Type != "function" || CompatibleCall.Function.Arguments == nil ||
				!compatibleToolNamePattern.MatchString(CompatibleCall.Function.Name) {
				return GenerateResponse{}, errors.New("invalid compatible response")
			}
			arguments := json.RawMessage(*CompatibleCall.Function.Arguments)
			runtimeName := CompatibleCall.Function.Name
			if original, exists := RuntimeByWire[runtimeName]; exists {
				runtimeName = original
			}
			call := ToolCall{ID: CompatibleCall.ID, Name: runtimeName, Arguments: arguments}
			if ValidateToolCall(call) != nil || seenIDs[call.ID] {
				return GenerateResponse{}, errors.New("invalid compatible response")
			}
			seenIDs[call.ID] = true
			var object map[string]json.RawMessage
			_ = json.Unmarshal(arguments, &object)
			canonical, _ := json.Marshal(object)
			call.Arguments = canonical
			toolCalls = append(toolCalls, call)
		}
	} else {
		allowsNullContent := openAI && (finishReason == "length" || finishReason == "content_filter")
		if (content == nil && !allowsNullContent) || finishReason == "tool_calls" ||
			(finishReason != "stop" && finishReason != "length" && finishReason != "content_filter") {
			return GenerateResponse{}, errors.New("invalid compatible response")
		}
	}
	normalized := GenerateResponse{
		Content: content, Reasoning: reasoning, ToolCalls: toolCalls, FinishReason: finishReason,
	}
	if response.Usage != nil {
		normalized.Usage = &Usage{InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens,
			TotalTokens: response.Usage.TotalTokens}
		if response.Usage.PromptDetails != nil {
			normalized.Usage.CachedTokens = response.Usage.PromptDetails.CachedTokens
		}
	}
	return normalized, nil
}

func IsJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func ReadProviderBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBody {
		return nil, errors.New("provider response exceeds 1 MiB")
	}
	return body, nil
}

// sumTokens adds the Usage components a Provider actually reported; it is null only when it reported none.
func IsTimeout(err error) bool {
	var timeoutError interface{ Timeout() bool }
	return errors.As(err, &timeoutError) && timeoutError.Timeout()
}
