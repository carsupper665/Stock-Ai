package test

import (
	"strings"
	"testing"
)

func TestFinalizationPromptAvoidsResponseFieldNames(t *testing.T) {
	fixture := newRuntimeFixture()
	script := newLLMScript(respondStop("complete"))
	fixture.generate = script.handler(t)
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startSessionForTest(t, runtime, session.ID)
	request := script.next(t, "system prompt")
	messages := request["messages"].([]any)
	prompt := messages[0].(map[string]any)["content"].(string)
	for _, removed := range []string{"finish_reason", "recent_run_summary"} {
		if strings.Contains(prompt, removed) {
			t.Errorf("system prompt contains obsolete %q", removed)
		}
	}
	for _, required := range []string{
		"When your work is normally complete, end with a response that calls no tool and whose content is exactly one JSON object:",
		"summary is what later Runs read first to recall this Run.",
		"Your final JSON is never shown in the chat; to answer a tag, use post_chat.",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("system prompt missing %q", required)
		}
	}
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
}
