package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAICompletions is pi-ai's openai-completions Api: one non-streaming /chat/completions call.
// OpenAI selects the native OpenAI rules (option allowlist, required credential, model-not-found);
// otherwise any OpenAI-compatible Provider, which may run without a credential.
type OpenAICompletions struct{ OpenAI bool }

func (api OpenAICompletions) Complete(ctx context.Context, model Model, request GenerateRequest, options Options) (GenerateResponse, error) {
	aliases := NewToolAliases(request)
	call, err := api.buildParams(model.BaseURL, model.ID, request, options.Options, aliases.WireByRuntime)
	if err != nil {
		return GenerateResponse{}, invalidRequest(err)
	}
	if options.APIKey != "" {
		call.headers["Authorization"] = "Bearer " + options.APIKey
	} else if api.OpenAI {
		return GenerateResponse{}, errCredentialRequired
	}
	response, err := postJSON(ctx, options.Client, call)
	if err != nil {
		return GenerateResponse{}, err
	}
	defer response.Body.Close()
	return api.parseResponse(response, aliases.RuntimeByWire, HistoricalToolCallIDs(request.Messages))
}

func (api OpenAICompletions) buildParams(baseURL, model string, request GenerateRequest, options map[string]json.RawMessage, WireByRuntime map[string]string) (providerCall, error) {
	openAI := api.OpenAI
	if openAI {
		var err error
		options, err = normalizeOpenAIOptions(options, request.Tools, WireByRuntime)
		if err != nil {
			return providerCall{}, err
		}
	}
	upstreamRequest := map[string]any{"model": model, "messages": compatibleMessages(request.Messages, WireByRuntime, openAI)}
	if len(request.Tools) != 0 {
		upstreamRequest["tools"] = compatibleTools(request.Tools, WireByRuntime)
	}
	for key, value := range options {
		if key == "model" || key == "messages" || key == "tools" {
			return providerCall{}, errors.New("options cannot replace model, messages, or tools.")
		}
		upstreamRequest[key] = value
	}
	body, err := json.Marshal(upstreamRequest)
	if err != nil {
		return providerCall{}, errors.New("Options contain an invalid value.")
	}
	return providerCall{url: strings.TrimRight(baseURL, "/") + "/chat/completions", headers: map[string]string{}, body: body}, nil
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
	body, err := readProviderBody(reader)
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

func (api OpenAICompletions) parseResponse(upstream *http.Response, RuntimeByWire map[string]string, historyIDs map[string]bool) (GenerateResponse, error) {
	openAI := api.OpenAI
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
		return GenerateResponse{}, upstreamStatusFailure(upstream)
	}
	body, err := readProviderBody(upstream.Body)
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
