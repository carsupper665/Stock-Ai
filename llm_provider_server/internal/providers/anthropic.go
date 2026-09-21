package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	anthropicVersion = "2023-06-01"
	// Messages API requires max_tokens; the Gateway supplies this ceiling when neither the model
	// catalog nor the request provides one (spec §35.2).
	anthropicDefaultMaxTokens = 16000
)

var AnthropicOptionNames = map[string]bool{
	"cache_control": true, "inference_geo": true, "max_tokens": true, "metadata": true, "output_config": true,
	"service_tier": true, "stop_sequences": true, "temperature": true, "thinking": true, "tool_choice": true,
	"top_k": true, "top_p": true,
}

func AnthropicCall(baseURL, model string, request GenerateRequest, options map[string]json.RawMessage, WireByRuntime map[string]string) (ProviderCall, error) {
	for name := range options {
		if !AnthropicOptionNames[name] {
			return ProviderCall{}, fmt.Errorf("Anthropic option %s is not supported by the normalized Generate contract", name)
		}
	}
	body := map[string]any{"model": model, "max_tokens": anthropicDefaultMaxTokens,
		"messages": anthropicMessages(request.Messages, WireByRuntime)}
	if system := anthropicSystem(request.Messages); len(system) != 0 {
		body["system"] = system
	}
	if len(request.Tools) != 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(tools, map[string]any{"name": WireByRuntime[tool.Name], "description": tool.Description,
				"input_schema": tool.InputSchema})
		}
		body["tools"] = tools
	}
	for key, value := range options {
		body[key] = value
	}
	if choice, exists := options["tool_choice"]; exists {
		normalizedChoice, err := anthropicToolChoice(choice, request.Tools, WireByRuntime)
		if err != nil {
			return ProviderCall{}, err
		}
		body["tool_choice"] = normalizedChoice
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ProviderCall{}, errors.New("Options contain an invalid value.")
	}
	return ProviderCall{URL: strings.TrimRight(baseURL, "/") + "/messages",
		Headers: map[string]string{"anthropic-version": anthropicVersion}, Body: encoded}, nil
}

func anthropicSystem(messages []Message) []map[string]any {
	system := make([]map[string]any, 0)
	for _, message := range messages {
		if message.Role == "system" {
			system = append(system, map[string]any{"type": "text", "text": *message.Content})
		}
	}
	return system
}

// Consecutive same-role turns are merged because the Messages API alternates user/assistant
// turns and expects every parallel tool_result inside one user turn.
func anthropicMessages(messages []Message, WireByRuntime map[string]string) []map[string]any {
	converted := make([]map[string]any, 0, len(messages))
	appendBlocks := func(role string, blocks ...map[string]any) {
		if last := len(converted) - 1; last >= 0 && converted[last]["role"] == role {
			converted[last]["content"] = append(converted[last]["content"].([]map[string]any), blocks...)
			return
		}
		converted = append(converted, map[string]any{"role": role, "content": blocks})
	}
	for _, message := range messages {
		switch message.Role {
		case "user":
			appendBlocks("user", map[string]any{"type": "text", "text": *message.Content})
		case "assistant":
			blocks := make([]map[string]any, 0, 1+len(message.ToolCalls))
			if message.Content != nil {
				blocks = append(blocks, map[string]any{"type": "text", "text": *message.Content})
			}
			for _, call := range message.ToolCalls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID,
					"name": WireByRuntime[call.Name], "input": call.Arguments})
			}
			appendBlocks("assistant", blocks...)
		case "tool":
			result := map[string]any{"type": "tool_result", "tool_use_id": *message.ToolCallID}
			if message.Content != nil {
				result["content"] = *message.Content
			}
			appendBlocks("user", result)
		}
	}
	return converted
}

func anthropicToolChoice(choice json.RawMessage, tools []ToolSchema, WireByRuntime map[string]string) (json.RawMessage, error) {
	var parsed struct {
		Type                   string  `json:"type"`
		Name                   *string `json:"name,omitempty"`
		DisableParallelToolUse *bool   `json:"disable_parallel_tool_use,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(choice))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&parsed) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return nil, errors.New("Anthropic tool_choice must be an object with type auto, any, none, or tool")
	}
	switch parsed.Type {
	case "auto", "any", "none":
		if parsed.Name != nil {
			return nil, errors.New("Anthropic tool_choice name is only valid with type tool")
		}
	case "tool":
		if parsed.Name == nil {
			return nil, errors.New("Anthropic tool_choice tool requires a name")
		}
		declared := false
		for _, tool := range tools {
			declared = declared || tool.Name == *parsed.Name
		}
		if !declared {
			return nil, errors.New("Anthropic tool_choice tool must name a declared tool")
		}
		wireName := WireByRuntime[*parsed.Name]
		parsed.Name = &wireName
	default:
		return nil, errors.New("Anthropic tool_choice must be an object with type auto, any, none, or tool")
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return nil, errors.New("Anthropic tool_choice is invalid")
	}
	return normalized, nil
}

func NormalizeAnthropicResponse(upstream *http.Response, RuntimeByWire map[string]string, historyIDs map[string]bool) (GenerateResponse, error) {
	switch upstream.StatusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return GenerateResponse{}, ProviderError{http.StatusBadRequest, "REQUEST_INVALID", "Anthropic rejected the generated request."}
	case http.StatusNotFound:
		body, err := ReadProviderBody(upstream.Body)
		if err != nil {
			if IsTimeout(err) {
				return GenerateResponse{}, err
			}
			return GenerateResponse{}, ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", "Provider request failed."}
		}
		if anthropicModelNotFound(body) {
			return GenerateResponse{}, ProviderError{http.StatusBadRequest, "MODEL_NOT_FOUND", "Anthropic does not provide the requested model to this credential."}
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
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type     string          `json:"type"`
			Text     *string         `json:"text"`
			Thinking *string         `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason *string `json:"stop_reason"`
		Usage      *struct {
			InputTokens              *int `json:"input_tokens"`
			OutputTokens             *int `json:"output_tokens"`
			CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&response) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) ||
		response.Type != "message" || response.Role != "assistant" || response.StopReason == nil {
		return GenerateResponse{}, errors.New("invalid Anthropic response")
	}
	var text strings.Builder
	var reasoning strings.Builder
	hasText := false
	hasReasoning := false
	toolCalls := make([]ToolCall, 0)
	seenIDs := make(map[string]bool, len(historyIDs))
	for id := range historyIDs {
		seenIDs[id] = true
	}
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			if block.Text == nil {
				return GenerateResponse{}, errors.New("invalid Anthropic response")
			}
			text.WriteString(*block.Text)
			hasText = true
		case "tool_use":
			runtimeName := block.Name
			if original, exists := RuntimeByWire[runtimeName]; exists {
				runtimeName = original
			}
			call := ToolCall{ID: block.ID, Name: runtimeName, Arguments: block.Input}
			if ValidateToolCall(call) != nil || seenIDs[call.ID] {
				return GenerateResponse{}, errors.New("invalid Anthropic response")
			}
			seenIDs[call.ID] = true
			var object map[string]json.RawMessage
			_ = json.Unmarshal(block.Input, &object)
			call.Arguments, _ = json.Marshal(object)
			toolCalls = append(toolCalls, call)
		case "thinking":
			if block.Thinking == nil {
				return GenerateResponse{}, errors.New("invalid Anthropic response")
			}
			reasoning.WriteString(*block.Thinking)
			hasReasoning = true
		case "redacted_thinking":
		default:
			return GenerateResponse{}, errors.New("invalid Anthropic response")
		}
	}
	var finishReason string
	switch *response.StopReason {
	case "tool_use":
		if len(toolCalls) == 0 {
			return GenerateResponse{}, errors.New("invalid Anthropic response")
		}
		finishReason = "tool_call"
	case "end_turn", "stop_sequence":
		finishReason = "stop"
	case "max_tokens", "model_context_window_exceeded":
		finishReason = "length"
	case "refusal":
		finishReason = "content_filter"
	default:
		return GenerateResponse{}, errors.New("invalid Anthropic response")
	}
	if finishReason != "tool_call" && len(toolCalls) != 0 {
		return GenerateResponse{}, errors.New("invalid Anthropic response")
	}
	normalized := GenerateResponse{ToolCalls: toolCalls, FinishReason: finishReason}
	if hasText {
		content := text.String()
		normalized.Content = &content
	}
	if hasReasoning {
		value := reasoning.String()
		normalized.Reasoning = &value
	}
	if response.Usage != nil {
		reported := response.Usage
		if negativeTokens(reported.InputTokens, reported.OutputTokens, reported.CacheCreationInputTokens, reported.CacheReadInputTokens) {
			return GenerateResponse{}, errors.New("invalid Anthropic response")
		}
		// Anthropic input_tokens excludes cache writes and reads; the normalized value is the whole prompt (spec §32.4).
		normalized.Usage = &Usage{
			InputTokens:  SumTokens(reported.InputTokens, reported.CacheCreationInputTokens, reported.CacheReadInputTokens),
			OutputTokens: reported.OutputTokens,
			CachedTokens: reported.CacheReadInputTokens,
		}
	}
	return normalized, nil
}

func anthropicModelNotFound(body []byte) bool {
	var response struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&response) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return false
	}
	return response.Error.Type == "not_found_error" && strings.HasPrefix(response.Error.Message, "model:")
}
