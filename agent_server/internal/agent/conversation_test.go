package agent

import (
	"strings"
	"testing"
)

func TestInterruptedToolCallsGetAnUnknownOutcome(t *testing.T) {
	text := func(value string) *string { return &value }
	answered := "a"
	messages := []gatewayMessage{
		{Role: "user", Content: text(`{"trigger":"event","current_time":"t"}`)},
		{Role: "assistant", ToolCalls: []gatewayTool{{ID: "a", Name: "get_account"}, {ID: "b", Name: "place_order"}}},
		{Role: "tool", Content: text(`{"ok":true,"result":{}}`), ToolCallID: &answered},
	}
	completed := completeToolResults(messages)
	if len(completed) != 4 || completed[3].Role != "tool" || *completed[3].ToolCallID != "b" ||
		!strings.Contains(*completed[3].Content, "RUN_INTERRUPTED") || !strings.Contains(*completed[3].Content, `"outcome":"unknown"`) {
		t.Fatalf("completed = %+v", completed)
	}
	if again := completeToolResults(completed); len(again) != 4 {
		t.Fatalf("completing twice must not add results: %+v", again)
	}
}
