package test

import (
	"net/http"
	"strings"
	"testing"
)

func TestModelReportedFailureDoesNotCompleteRun(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content":    `{"status":"failed","output":"Tool host unavailable; unable to execute.","summary":"Unable to execute."}`,
			"tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start: %d %s", status, raw)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "failed")
	if run.Error == nil || !strings.HasPrefix(*run.Error, "TASK_FAILED:") || run.ModelCalls != 1 || run.ToolCalls != 0 {
		t.Fatalf("model-reported failure must fail immediately: %+v", run)
	}
	if run.Summary != nil {
		t.Fatal("failed Run published a completion summary")
	}
}
