package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Definitions returns every tool exposed to the model: market, event, trading, message,
// and memory tools, plus the skill tools when skills are enabled.
func Definitions(skillsEnabled bool) []map[string]any {
	definitions := marketDefinitions()
	definitions = append(definitions, eventSchemas()...)
	definitions = append(definitions, tradingDefinitions()...)
	definitions = append(definitions, messageDefinitions()...)
	definitions = append(definitions, memoryDefinitions()...)
	if skillsEnabled {
		definitions = append(definitions, skillDefinitions()...)
	}
	return definitions
}

func IsMutation(name string) bool {
	switch name {
	case "create_event", "delete_event", "place_order", "cancel_order", "close_position", "set_position_stops", "post_chat",
		"memory_save", "memory_edit":
		return true
	default:
		return name == ""
	}
}

func ErrorResult(code, message, outcome string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]string{
		"code": code, "message": message, "outcome": outcome,
	}})
	return encoded
}

func SuccessResult(result any) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": result})
	return encoded
}

func DecodeArguments(arguments json.RawMessage, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func ParseMarketSymbol(rawMarket, rawSymbol json.RawMessage) (string, string, error) {
	symbol, err := RequiredString(rawSymbol, "symbol")
	if err != nil {
		return "", "", err
	}
	market := "crypto"
	if len(rawMarket) != 0 {
		if bytes.Equal(bytes.TrimSpace(rawMarket), []byte("null")) || json.Unmarshal(rawMarket, &market) != nil {
			return "", "", errors.New("market must be a string")
		}
		market = strings.TrimSpace(market)
		if market == "" {
			market = "crypto"
		}
	}
	return market, strings.TrimSpace(symbol), nil
}

func RequiredString(raw json.RawMessage, name string) (string, error) {
	var value string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a nonempty string", name)
	}
	return strings.TrimSpace(value), nil
}

func SelectFields(source map[string]json.RawMessage, fields ...string) map[string]json.RawMessage {
	selected := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		if value, ok := source[field]; ok {
			selected[field] = value
		}
	}
	return selected
}
