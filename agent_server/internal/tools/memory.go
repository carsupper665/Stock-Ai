package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	MemoryDefaultLimit = 20
	memoryToolMax      = 100
)

func memoryDefinitions() []map[string]any {
	limit := map[string]any{"type": "integer", "minimum": 1, "maximum": memoryToolMax, "default": MemoryDefaultLimit}
	return []map[string]any{
		{"name": "memory_list", "description": "List unexpired Memory metadata for this Session.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"limit": limit}, "additionalProperties": false,
		}},
		{"name": "memory_search", "description": "Search unexpired Memory summary and type text for this Session.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "limit": limit},
			"required": []string{"query"}, "additionalProperties": false,
		}},
		{"name": "memory_read", "description": "Read one unexpired Memory from this Session by id.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"memory_id": map[string]any{"type": "string"}},
			"required": []string{"memory_id"}, "additionalProperties": false,
		}},
		{"name": "get_run_history", "description": "Read one completed, failed, or interrupted Run from this Session's History.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"run_id": map[string]any{"type": "integer", "minimum": 1}},
			"required": []string{"run_id"}, "additionalProperties": false,
		}},
	}
}

func MemoryLimitArguments(arguments json.RawMessage) (int, error) {
	var raw struct {
		Limit json.RawMessage `json:"limit"`
	}
	if !DecodeArguments(arguments, &raw) {
		return 0, errors.New("only limit is accepted")
	}
	limit := MemoryDefaultLimit
	if len(raw.Limit) == 0 {
		return limit, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw.Limit), []byte("null")) || json.Unmarshal(raw.Limit, &limit) != nil || limit < 1 || limit > memoryToolMax {
		return 0, fmt.Errorf("limit must be an integer from 1 through %d", memoryToolMax)
	}
	return limit, nil
}

func MemorySearchArguments(arguments json.RawMessage) (string, int, error) {
	var raw struct {
		Query json.RawMessage `json:"query"`
		Limit json.RawMessage `json:"limit"`
	}
	if !DecodeArguments(arguments, &raw) {
		return "", 0, errors.New("only query and limit are accepted")
	}
	query, err := RequiredString(raw.Query, "query")
	if err != nil {
		return "", 0, err
	}
	limit := MemoryDefaultLimit
	if len(raw.Limit) != 0 && (bytes.Equal(bytes.TrimSpace(raw.Limit), []byte("null")) || json.Unmarshal(raw.Limit, &limit) != nil || limit < 1 || limit > memoryToolMax) {
		return "", 0, fmt.Errorf("limit must be an integer from 1 through %d", memoryToolMax)
	}
	return query, limit, nil
}
