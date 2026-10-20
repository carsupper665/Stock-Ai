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

const geminiSupportedModel = "gemini-2.5-flash-lite"

var GeminiOptionNames = map[string]bool{
	"frequencyPenalty": true,
	"maxOutputTokens":  true,
	"presencePenalty":  true,
	"seed":             true,
	"stopSequences":    true,
	"temperature":      true,
	"topK":             true,
	"topP":             true,
}

// GoogleGenerativeAI is pi-ai's google-generative-ai Api: one streamGenerateContent call with an
// x-goog-api-key credential, limited to the model whose state needs no thought signatures.
type GoogleGenerativeAI struct{}

func (api GoogleGenerativeAI) Complete(ctx context.Context, model Model, request GenerateRequest, options Options) (GenerateResponse, error) {
	aliases := NewToolAliases(request)
	call, err := api.buildParams(model.BaseURL, model.ID, request, options.Options, aliases.WireByRuntime)
	if err != nil {
		return GenerateResponse{}, invalidRequest(err)
	}
	if options.APIKey == "" {
		return GenerateResponse{}, errCredentialRequired
	}
	call.headers["x-goog-api-key"] = options.APIKey
	response, err := postJSON(ctx, options, call)
	if err != nil {
		return GenerateResponse{}, err
	}
	defer response.Body.Close()
	return api.parseResponse(response, aliases.RuntimeByWire, HistoricalToolCallIDs(request.Messages), options)
}

func (api GoogleGenerativeAI) buildParams(baseURL, model string, request GenerateRequest, options map[string]json.RawMessage, WireByRuntime map[string]string) (providerCall, error) {
	if model != geminiSupportedModel {
		return providerCall{}, ProviderError{http.StatusBadRequest, "MODEL_NOT_FOUND", "Gemini model is outside the Gateway's supported stateless protocol scope."}
	}
	for name := range options {
		if !GeminiOptionNames[name] {
			return providerCall{}, fmt.Errorf("Gemini option %s is not supported by the normalized Generate contract", name)
		}
	}
	contents, err := geminiContents(request.Messages, WireByRuntime)
	if err != nil {
		return providerCall{}, err
	}
	if len(contents) == 0 {
		return providerCall{}, errors.New("Gemini requires at least one non-system message")
	}
	body := map[string]any{"contents": contents}
	if system := geminiSystem(request.Messages); len(system) != 0 {
		body["systemInstruction"] = map[string]any{"parts": system}
	}
	if len(request.Tools) != 0 {
		declarations := make([]map[string]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			declarations = append(declarations, map[string]any{
				"name": WireByRuntime[tool.Name], "description": tool.Description, "parameters": tool.InputSchema,
			})
		}
		body["tools"] = []map[string]any{{"functionDeclarations": declarations}}
	}
	if len(options) != 0 {
		body["generationConfig"] = options
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return providerCall{}, errors.New("Options contain an invalid value.")
	}
	return providerCall{
		url: strings.TrimRight(baseURL, "/") + "/models/" + model + ":streamGenerateContent?alt=sse", headers: map[string]string{}, body: encoded,
	}, nil
}

func geminiSystem(messages []Message) []map[string]any {
	parts := make([]map[string]any, 0)
	for _, message := range messages {
		if message.Role == "system" {
			parts = append(parts, map[string]any{"text": *message.Content})
		}
	}
	return parts
}

func geminiContents(messages []Message, WireByRuntime map[string]string) ([]map[string]any, error) {
	contents := make([]map[string]any, 0, len(messages))
	callNames := make(map[string]string)
	appendParts := func(role string, parts ...map[string]any) {
		if last := len(contents) - 1; last >= 0 && contents[last]["role"] == role {
			contents[last]["parts"] = append(contents[last]["parts"].([]map[string]any), parts...)
			return
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	for _, message := range messages {
		switch message.Role {
		case "system":
		case "user":
			appendParts("user", map[string]any{"text": *message.Content})
		case "assistant":
			parts := make([]map[string]any, 0, 1+len(message.ToolCalls))
			if message.Content != nil {
				parts = append(parts, map[string]any{"text": *message.Content})
			}
			for _, call := range message.ToolCalls {
				wireName := WireByRuntime[call.Name]
				callNames[call.ID] = wireName
				parts = append(parts, map[string]any{"functionCall": map[string]any{
					"id": call.ID, "name": wireName, "args": call.Arguments,
				}})
			}
			appendParts("model", parts...)
		case "tool":
			result, err := geminiToolResult(message.Content)
			if err != nil {
				return nil, err
			}
			id := *message.ToolCallID
			appendParts("user", map[string]any{"functionResponse": map[string]any{
				"id": id, "name": callNames[id], "response": result,
			}})
		}
	}
	return contents, nil
}

func geminiToolResult(content *string) (map[string]json.RawMessage, error) {
	if content == nil || !ValidJSONObject(json.RawMessage(*content)) {
		return nil, errors.New("Gemini tool result content must contain one JSON object")
	}
	var result map[string]json.RawMessage
	_ = json.Unmarshal([]byte(*content), &result)
	return result, nil
}

func (api GoogleGenerativeAI) parseResponse(upstream *http.Response, RuntimeByWire map[string]string, historyIDs map[string]bool, options Options) (GenerateResponse, error) {
	switch upstream.StatusCode {
	case http.StatusBadRequest:
		return GenerateResponse{}, ProviderError{http.StatusBadRequest, "REQUEST_INVALID", "Gemini rejected the generated request."}
	case http.StatusNotFound:
		return GenerateResponse{}, ProviderError{http.StatusBadRequest, "MODEL_NOT_FOUND", "Gemini does not provide the requested model to this credential."}
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return GenerateResponse{}, upstreamStatusFailure(upstream)
	}
	body, err := readResponseBody(upstream, func(reader io.Reader) ([]byte, error) { return assembleGeminiStream(reader, options) })
	if err != nil {
		return GenerateResponse{}, err
	}
	var response struct {
		Candidates []struct {
			Content *struct {
				Role  string            `json:"role"`
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback *struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Usage *struct {
			PromptTokens    *int `json:"promptTokenCount"`
			CandidateTokens *int `json:"candidatesTokenCount"`
			CachedTokens    *int `json:"cachedContentTokenCount"`
			TotalTokens     *int `json:"totalTokenCount"`
			ThoughtsTokens  *int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&response) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return GenerateResponse{}, errors.New("invalid Gemini response")
	}
	if response.Usage != nil && (negativeTokens(response.Usage.PromptTokens, response.Usage.CandidateTokens,
		response.Usage.CachedTokens, response.Usage.TotalTokens, response.Usage.ThoughtsTokens) ||
		(response.Usage.ThoughtsTokens != nil && *response.Usage.ThoughtsTokens != 0)) {
		return GenerateResponse{}, errors.New("unsupported Gemini thought state")
	}
	normalized := GenerateResponse{ToolCalls: []ToolCall{}}
	if response.Usage != nil {
		normalized.Usage = &Usage{InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CandidateTokens,
			CachedTokens: response.Usage.CachedTokens, TotalTokens: response.Usage.TotalTokens}
	}
	if len(response.Candidates) == 0 {
		if response.PromptFeedback == nil || !geminiPromptBlockReason(response.PromptFeedback.BlockReason) {
			return GenerateResponse{}, errors.New("invalid Gemini response")
		}
		normalized.FinishReason = "content_filter"
		return normalized, nil
	}
	if len(response.Candidates) != 1 || (response.PromptFeedback != nil && response.PromptFeedback.BlockReason != "") {
		return GenerateResponse{}, errors.New("invalid Gemini response")
	}
	candidate := response.Candidates[0]
	var text strings.Builder
	hasText := false
	toolCalls := make([]ToolCall, 0)
	seenIDs := make(map[string]bool, len(historyIDs))
	for id := range historyIDs {
		seenIDs[id] = true
	}
	if candidate.Content != nil {
		if candidate.Content.Role != "model" {
			return GenerateResponse{}, errors.New("invalid Gemini response")
		}
		for _, rawPart := range candidate.Content.Parts {
			partText, call, err := normalizeGeminiPart(rawPart, RuntimeByWire)
			if err != nil {
				return GenerateResponse{}, err
			}
			if partText != nil {
				text.WriteString(*partText)
				hasText = true
				continue
			}
			if seenIDs[call.ID] {
				return GenerateResponse{}, errors.New("invalid Gemini response")
			}
			seenIDs[call.ID] = true
			toolCalls = append(toolCalls, *call)
		}
	}
	finishReason, err := normalizeGeminiFinishReason(candidate.FinishReason, hasText, len(toolCalls) != 0)
	if err != nil {
		return GenerateResponse{}, err
	}
	normalized.ToolCalls = toolCalls
	normalized.FinishReason = finishReason
	if hasText {
		content := text.String()
		normalized.Content = &content
	}
	return normalized, nil
}

func geminiPromptBlockReason(reason string) bool {
	switch reason {
	case "SAFETY", "OTHER", "BLOCKLIST", "PROHIBITED_CONTENT", "IMAGE_SAFETY":
		return true
	default:
		return false
	}
}

func normalizeGeminiPart(raw json.RawMessage, RuntimeByWire map[string]string) (*string, *ToolCall, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 {
		return nil, nil, errors.New("unsupported Gemini protocol state")
	}
	if textValue, exists := fields["text"]; exists {
		var text string
		if json.Unmarshal(textValue, &text) != nil {
			return nil, nil, errors.New("invalid Gemini response")
		}
		return &text, nil, nil
	}
	callValue, exists := fields["functionCall"]
	if !exists {
		return nil, nil, errors.New("unsupported Gemini protocol state")
	}
	var function struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	decoder := json.NewDecoder(bytes.NewReader(callValue))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&function) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) ||
		!compatibleToolNamePattern.MatchString(function.Name) {
		return nil, nil, errors.New("invalid Gemini response")
	}
	runtimeName := function.Name
	if original, exists := RuntimeByWire[runtimeName]; exists {
		runtimeName = original
	}
	call := ToolCall{ID: function.ID, Name: runtimeName, Arguments: function.Args}
	if ValidateToolCall(call) != nil {
		return nil, nil, errors.New("invalid Gemini response")
	}
	var arguments map[string]json.RawMessage
	_ = json.Unmarshal(call.Arguments, &arguments)
	call.Arguments, _ = json.Marshal(arguments)
	return nil, &call, nil
}

func normalizeGeminiFinishReason(reason string, hasText, hasCalls bool) (string, error) {
	if hasCalls {
		if reason != "STOP" {
			return "", errors.New("invalid Gemini response")
		}
		return "tool_call", nil
	}
	switch reason {
	case "STOP":
		if !hasText {
			return "", errors.New("invalid Gemini response")
		}
		return "stop", nil
	case "MAX_TOKENS":
		return "length", nil
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return "content_filter", nil
	default:
		return "", errors.New("invalid Gemini response")
	}
}

// assembleGeminiStream folds streamGenerateContent chunks into one GenerateContentResponse: each
// candidate collects its parts in order, and the last finish reason, prompt feedback and usage win.
func assembleGeminiStream(reader io.Reader, options Options) ([]byte, error) {
	type streamedCandidate struct {
		role         string
		parts        []json.RawMessage
		finishReason string
	}
	var candidates []*streamedCandidate
	var promptFeedback, usage json.RawMessage
	var size streamText
	err := readServerSentEvents(reader, func(_ string, data []byte) (bool, error) {
		var chunk struct {
			Candidates []struct {
				Index   int `json:"index"`
				Content *struct {
					Role  string            `json:"role"`
					Parts []json.RawMessage `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			PromptFeedback json.RawMessage `json:"promptFeedback"`
			Usage          json.RawMessage `json:"usageMetadata"`
			Error          json.RawMessage `json:"error"`
		}
		if json.Unmarshal(data, &chunk) != nil {
			return false, errors.New("invalid Gemini stream")
		}
		if len(chunk.Error) != 0 && !IsJSONNull(chunk.Error) {
			return false, errStreamFailed
		}
		if len(chunk.PromptFeedback) != 0 {
			promptFeedback = chunk.PromptFeedback
		}
		if len(chunk.Usage) != 0 {
			usage = chunk.Usage
		}
		for _, candidate := range chunk.Candidates {
			if candidate.Index < 0 || candidate.Index > len(candidates) {
				return false, errors.New("invalid Gemini stream")
			}
			if candidate.Index == len(candidates) {
				candidates = append(candidates, &streamedCandidate{})
			}
			target := candidates[candidate.Index]
			if candidate.FinishReason != "" {
				target.finishReason = candidate.FinishReason
			}
			if candidate.Content == nil {
				continue
			}
			if candidate.Content.Role != "" {
				target.role = candidate.Content.Role
			}
			for _, part := range candidate.Content.Parts {
				target.parts = append(target.parts, part)
				if err := size.add(string(part)); err != nil {
					return false, err
				}
				var text struct {
					Text string `json:"text"`
				}
				if candidate.Index == 0 && json.Unmarshal(part, &text) == nil {
					options.delta(text.Text, "")
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	assembled := map[string]any{}
	list := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		item := map[string]any{"finishReason": candidate.finishReason}
		if candidate.role != "" || candidate.parts != nil {
			item["content"] = map[string]any{"role": candidate.role, "parts": candidate.parts}
		}
		list = append(list, item)
	}
	if len(list) != 0 {
		assembled["candidates"] = list
	}
	if promptFeedback != nil {
		assembled["promptFeedback"] = promptFeedback
	}
	if usage != nil {
		assembled["usageMetadata"] = usage
	}
	return json.Marshal(assembled)
}
