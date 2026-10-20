package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	agent "stock-ai/agent-server/internal/agent"
)

func TestRunContextCarriesMessagesTaggedSincePreviousRun(t *testing.T) {
	old := []any{
		map[string]any{"id": "msg_2", "user_name": "Bless", "content": "check the stop loss", "tags": []string{"Message Agent"}, "created_at": "2026-01-02T00:00:00Z"},
		map[string]any{"id": "msg_1", "user_name": "Other Agent", "content": "BTC funding flipped", "tags": []string{"Message Agent"}, "created_at": "2026-01-01T00:00:00Z"},
	}
	fixture := newMessageToolFixture()
	fixture.inbox = func(w http.ResponseWriter, request *http.Request, call int) {
		if request.Header.Get("Authorization") != "Bearer ticket-25-account-v1" || request.URL.RawQuery != "page=1&sort=desc&tag=Message+Agent" {
			t.Errorf("inbox request = %s auth=%q", request.URL.RequestURI(), request.Header.Get("Authorization"))
		}
		if call == 1 {
			messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": old, "page": 1, "has_more": true})
			return
		}
		latest := map[string]any{"id": "msg_3", "user_name": "Bless", "content": "reply please", "tags": []string{"Message Agent"}, "created_at": time.Now().UTC().Format(time.RFC3339Nano)}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": append([]any{latest}, old...), "page": 1, "has_more": true})
	}
	fixture.generate = func(w http.ResponseWriter, request *http.Request, _ int) {
		body := decodeMessageGatewayRequest(t, request)
		system, _ := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		if !strings.Contains(system, "arrive in the run context as inbox") || !strings.Contains(system, "must tag at least one recipient") {
			t.Errorf("system prompt lacks inbox guidance: %s", system)
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("inbox read", "inbox read"), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	runtime := fixture.open(t)
	session, first := startMessageRun(t, runtime)
	if first.Status != "completed" {
		t.Fatalf("first Run = %+v", first)
	}
	if ids, more := inboxOf(t, first); ids != "msg_2,msg_1" || !more {
		t.Fatalf("first Run inbox = %s has_more=%v; want every tagged message on the first page", ids, more)
	}

	for _, action := range []string{"stop", "start"} {
		if status, raw := messageRuntimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/"+action, nil); status >= 300 {
			t.Fatalf("%s Session = %d %s", action, status, raw)
		}
	}
	second := waitForMessageRunID(t, runtime, session.ID, 2)
	if ids, more := inboxOf(t, second); ids != "msg_3" || more {
		t.Fatalf("second Run inbox = %s has_more=%v; want only the message posted after the first Run started", ids, more)
	}
}

func inboxOf(t *testing.T, run agent.Run) (string, bool) {
	t.Helper()
	var context struct {
		Inbox *struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			HasMore bool `json:"has_more"`
		} `json:"inbox"`
	}
	if err := json.Unmarshal(run.InitialContext, &context); err != nil || context.Inbox == nil {
		t.Fatalf("initial context %s has no inbox (%v)", run.InitialContext, err)
	}
	ids := make([]string, 0, len(context.Inbox.Messages))
	for _, message := range context.Inbox.Messages {
		ids = append(ids, message.ID)
	}
	return strings.Join(ids, ","), context.Inbox.HasMore
}

func waitForMessageRunID(t *testing.T, runtime *agent.AgentRuntime, sessionID string, runID int) agent.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, body := messageRuntimeRequest(t, runtime.Handler(), http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/runs/%d", sessionID, runID), nil)
		if status == http.StatusOK {
			var run agent.Run
			if err := json.Unmarshal(body, &run); err == nil && run.Status != "running" {
				return run
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run %d did not finish; last response=%d %s", runID, status, body)
		}
	}
}
