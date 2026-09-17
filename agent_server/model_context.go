package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxGatewayRequestBytes = 1 << 20
	maxModelMessageBytes   = 768 << 10
	compactStringRunes     = 512
)

type gatewayContinuation struct {
	AfterMessages int              `json:"after_messages"`
	Messages      []gatewayMessage `json:"messages"`
}

type continuationState struct {
	request   []gatewayMessage
	assistant gatewayMessage
}

type modelContextProjection struct {
	Messages       []gatewayMessage
	OriginalBytes  int
	ProjectedBytes int
	Compacted      int
}

func runtimeToolNames() []string {
	definitions := runtimeToolDefinitions()
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		name, _ := definition["name"].(string)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func toolDisclosureInstruction() string {
	return "\n\nThe available tool names are exactly: " + strings.Join(runtimeToolNames(), ", ") + ". Use only these names through the structured tool channel."
}

func projectModelContext(messages []gatewayMessage) (modelContextProjection, error) {
	original, err := json.Marshal(messages)
	if err != nil {
		return modelContextProjection{}, err
	}
	projection := modelContextProjection{
		Messages: cloneGatewayMessages(messages), OriginalBytes: len(original), ProjectedBytes: len(original),
	}
	if projection.ProjectedBytes <= maxModelMessageBytes {
		return projection, nil
	}

	toolNames := make(map[string]string)
	readResults := make([]int, 0)
	mutationResults := make([]int, 0)
	assistantContent := make([]int, 0)
	for index, message := range projection.Messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				toolNames[call.ID] = call.Name
			}
			if index > 1 && message.Content != nil && len(*message.Content) > 4096 {
				assistantContent = append(assistantContent, index)
			}
			continue
		}
		if message.Role != "tool" || message.ToolCallID == nil {
			continue
		}
		if isMutationTool(toolNames[*message.ToolCallID]) {
			mutationResults = append(mutationResults, index)
		} else {
			readResults = append(readResults, index)
		}
	}

	for _, index := range readResults {
		if projection.ProjectedBytes <= maxModelMessageBytes {
			break
		}
		message := &projection.Messages[index]
		name := toolNames[*message.ToolCallID]
		if replaceMessageContent(message, compactToolContent(name, *message.Content, false)) {
			projection.Compacted++
			projection.ProjectedBytes = encodedMessageBytes(projection.Messages)
		}
	}
	for _, index := range assistantContent {
		if projection.ProjectedBytes <= maxModelMessageBytes {
			break
		}
		message := &projection.Messages[index]
		replacement := fmt.Sprintf("[older assistant content compacted; original_utf8_bytes=%d]", len(*message.Content))
		if replaceMessageContent(message, replacement) {
			projection.Compacted++
			projection.ProjectedBytes = encodedMessageBytes(projection.Messages)
		}
	}
	for _, index := range mutationResults {
		if projection.ProjectedBytes <= maxModelMessageBytes {
			break
		}
		message := &projection.Messages[index]
		name := toolNames[*message.ToolCallID]
		if replaceMessageContent(message, compactToolContent(name, *message.Content, true)) {
			projection.Compacted++
			projection.ProjectedBytes = encodedMessageBytes(projection.Messages)
		}
	}
	if projection.ProjectedBytes > maxModelMessageBytes {
		return projection, errors.New("mandatory and recent Run context exceeds the Gateway request bound")
	}
	return projection, nil
}

func cloneGatewayMessages(messages []gatewayMessage) []gatewayMessage {
	cloned := make([]gatewayMessage, len(messages))
	copy(cloned, messages)
	for index := range cloned {
		cloned[index].ToolCalls = append([]gatewayTool(nil), messages[index].ToolCalls...)
	}
	return cloned
}

func encodedMessageBytes(messages []gatewayMessage) int {
	encoded, _ := json.Marshal(messages)
	return len(encoded)
}

func replaceMessageContent(message *gatewayMessage, replacement string) bool {
	if message.Content == nil || replacement == *message.Content || len(replacement) >= len(*message.Content) {
		return false
	}
	message.Content = &replacement
	return true
}

func isMutationTool(name string) bool {
	switch name {
	case "create_event", "delete_event", "place_order", "cancel_order", "close_position", "set_position_stops", "post_message":
		return true
	default:
		return name == ""
	}
}

func compactToolContent(name, content string, mutation bool) string {
	raw := json.RawMessage(content)
	if name == "get_ohlcv" {
		return string(filterToolResultForHistory(name, raw))
	}
	if name == "get_run_history" {
		return string(filterToolResultForHistory(name, raw))
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return fmt.Sprintf(`{"context_compacted":true,"tool":%q,"original_utf8_bytes":%d}`, name, len(content))
	}
	if envelope, ok := value.(map[string]any); ok && envelope["ok"] == false {
		encoded, _ := json.Marshal(compactJSONValue(envelope, 0))
		return string(encoded)
	}
	projected := map[string]any{
		"context_compacted":   true,
		"tool":                name,
		"original_utf8_bytes": len(content),
		"evidence":            compactJSONValue(value, 0),
	}
	if mutation {
		projected["mutation_evidence_preserved"] = true
	}
	encoded, _ := json.Marshal(projected)
	return string(encoded)
}

func compactJSONValue(value any, depth int) any {
	if depth >= 4 {
		return "[nested value compacted]"
	}
	switch typed := value.(type) {
	case string:
		if utf8.RuneCountInString(typed) <= compactStringRunes {
			return typed
		}
		runes := []rune(typed)
		return string(runes[:compactStringRunes]) + "…"
	case []any:
		if len(typed) <= 2 {
			items := make([]any, 0, len(typed))
			for _, item := range typed {
				items = append(items, compactJSONValue(item, depth+1))
			}
			return items
		}
		return map[string]any{
			"item_count": len(typed), "items_omitted": len(typed) - 2,
			"items_sample": []any{compactJSONValue(typed[0], depth+1), compactJSONValue(typed[len(typed)-1], depth+1)},
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 32 {
			keys = keys[:32]
		}
		result := make(map[string]any, len(keys)+1)
		for _, key := range keys {
			result[key] = compactJSONValue(typed[key], depth+1)
		}
		if len(keys) < len(typed) {
			result["fields_omitted"] = len(typed) - len(keys)
		}
		return result
	default:
		return value
	}
}

func continuationFor(state *continuationState, messages []gatewayMessage) *gatewayContinuation {
	if state == nil || len(state.request) == 0 || len(messages) <= len(state.request) ||
		!reflect.DeepEqual(messages[:len(state.request)], state.request) ||
		!reflect.DeepEqual(messages[len(state.request)], state.assistant) {
		return nil
	}
	delta := messages[len(state.request)+1:]
	if len(delta) == 0 {
		return nil
	}
	return &gatewayContinuation{AfterMessages: len(state.request), Messages: cloneGatewayMessages(delta)}
}

func acknowledgeContinuation(state *continuationState, request []gatewayMessage, response gatewayResponse) {
	if state == nil {
		return
	}
	state.request = cloneGatewayMessages(request)
	state.assistant = gatewayMessage{Role: "assistant", Content: response.Content, ToolCalls: append([]gatewayTool(nil), response.ToolCalls...)}
}
