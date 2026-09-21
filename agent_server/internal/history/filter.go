package history

import "encoding/json"

// FilterToolResult shrinks tool results before they are recorded in a run's
// iteration history: large candle lists become a count plus first/last
// samples, and run-history payloads drop their bulk arrays.
func FilterToolResult(toolName string, result json.RawMessage) json.RawMessage {
	if toolName == "get_run_history" {
		return FilterRunHistoryToolResult(result)
	}
	if toolName != "get_ohlcv" {
		return result
	}
	var wrapper struct {
		OK     bool                       `json:"ok"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(result, &wrapper) != nil || !wrapper.OK || wrapper.Result == nil {
		return result
	}
	field := "rows"
	var candles []json.RawMessage
	if json.Unmarshal(wrapper.Result[field], &candles) != nil {
		field = "candles"
	}
	if json.Unmarshal(wrapper.Result[field], &candles) != nil {
		return result
	}
	delete(wrapper.Result, field)
	sample := make([]json.RawMessage, 0, 2)
	if len(candles) != 0 {
		sample = append(sample, candles[0])
		if len(candles) > 1 {
			sample = append(sample, candles[len(candles)-1])
		}
	}
	wrapper.Result["candle_count"], _ = json.Marshal(len(candles))
	wrapper.Result[field+"_sample"], _ = json.Marshal(sample)
	wrapper.Result["candles_omitted"], _ = json.Marshal(maxInt(0, len(candles)-len(sample)))
	filtered, err := json.Marshal(wrapper)
	if err != nil {
		return result
	}
	return filtered
}

// FilterRunHistoryToolResult replaces the bulk arrays of a run-history tool
// result with counts and a bounded output preview.
func FilterRunHistoryToolResult(result json.RawMessage) json.RawMessage {
	var wrapper struct {
		OK     bool                       `json:"ok"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(result, &wrapper) != nil || !wrapper.OK || wrapper.Result == nil {
		return result
	}
	if iterations, exists := wrapper.Result["iterations"]; exists {
		var entries []json.RawMessage
		if json.Unmarshal(iterations, &entries) == nil {
			wrapper.Result["iteration_count"], _ = json.Marshal(len(entries))
		}
		delete(wrapper.Result, "iterations")
	}
	if events, exists := wrapper.Result["event_log"]; exists {
		var entries []json.RawMessage
		if json.Unmarshal(events, &entries) == nil {
			wrapper.Result["event_count"], _ = json.Marshal(len(entries))
		}
		delete(wrapper.Result, "event_log")
	}
	if output, exists := wrapper.Result["output"]; exists {
		var text *string
		if json.Unmarshal(output, &text) == nil && text != nil {
			wrapper.Result["output_bytes"], _ = json.Marshal(len(*text))
			preview := *text
			if len(preview) > 2048 {
				preview = preview[:2048]
			}
			wrapper.Result["output_preview"], _ = json.Marshal(preview)
		}
		delete(wrapper.Result, "output")
	}
	delete(wrapper.Result, "initial_context")
	filtered, err := json.Marshal(wrapper)
	if err != nil {
		return result
	}
	return filtered
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
