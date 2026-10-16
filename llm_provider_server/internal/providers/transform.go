package providers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Request validation and tool-name/ID handling shared by every Api (pi-ai's transform-messages role).

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

func IsJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
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
