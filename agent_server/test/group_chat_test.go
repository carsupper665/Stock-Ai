package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func chatMessage(id, content string, created time.Time) map[string]any {
	return map[string]any{"id": id, "user_name": "Bless", "content": content, "tags": []string{"Message Agent"}, "created_at": created.UTC().Format(time.RFC3339Nano)}
}

func messageIDs(t *testing.T, result map[string]any) string {
	t.Helper()
	inner, _ := result["result"].(map[string]any)
	raw, _ := inner["messages"].([]any)
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		ids = append(ids, item.(map[string]any)["id"].(string))
	}
	return strings.Join(ids, ",")
}

func TestGetChatWithoutPageReadsOnlyUnreadOldestFirst(t *testing.T) {
	now := time.Now()
	fixture := newMessageToolFixture()
	fixture.message = func(w http.ResponseWriter, request *http.Request, call int) {
		switch call {
		case 1:
			if request.URL.RawQuery != "page=1&sort=desc" {
				t.Errorf("first unread read = %s", request.URL.RawQuery)
			}
			messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": []any{chatMessage("m2", "two", now), chatMessage("m1", "one", now.Add(-time.Second))}, "page": 1, "has_more": false})
		default:
			if request.URL.RawQuery != "after=m2&page=1&sort=asc" {
				t.Errorf("next unread read = %s", request.URL.RawQuery)
			}
			messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": []any{chatMessage("m3", "three", now.Add(time.Second))}, "page": 1, "has_more": false})
		}
	}
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		switch call {
		case 1:
			messageToolCall(w, "read-1", "get_chat", map[string]any{})
		case 2:
			if result := messageToolResult(t, request, "read-1"); messageIDs(t, result) != "m1,m2" {
				t.Errorf("first unread = %#v", result)
			}
			messageToolCall(w, "read-2", "get_chat", map[string]any{})
		case 3:
			if result := messageToolResult(t, request, "read-2"); messageIDs(t, result) != "m3" {
				t.Errorf("second unread = %#v", result)
			}
			messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("read", "read"), "tool_calls": []any{}, "finish_reason": "stop"})
		}
	}
	runtime := fixture.open(t)
	if _, run := startMessageRun(t, runtime); run.Status != "completed" {
		t.Fatalf("Run = %+v", run)
	}
}

func TestTagArrivingDuringRunIsDeliveredBeforeTheNextDecisionOnce(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.inbox = func(w http.ResponseWriter, request *http.Request, call int) {
		if call == 2 {
			messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": []any{chatMessage("tag-1", "please check ETH", time.Now())}, "page": 1, "has_more": false})
			return
		}
		emptyInbox(w)
	}
	fixture.message = func(w http.ResponseWriter, _ *http.Request, _ int) { emptyInbox(w) }
	var mentions []int
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		body := decodeMessageGatewayRequest(t, request)
		encoded, _ := json.Marshal(body["messages"])
		mentions = append(mentions, strings.Count(string(encoded), "please check ETH"))
		if call == 2 {
			messages := body["messages"].([]any)
			last, _ := messages[len(messages)-1].(map[string]any)["content"].(string)
			if !strings.HasPrefix(last, "[Group chat]") || !strings.Contains(last, "please check ETH") {
				t.Errorf("mention must be the last message before decision 2: %s", last)
			}
		}
		if call <= 2 {
			messageToolCall(w, "browse-"+jsonNumber(int64(call)), "get_chat", map[string]any{"page": 1})
			return
		}
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("seen", "seen"), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	runtime := fixture.open(t)
	_, run := startMessageRun(t, runtime)
	if run.Status != "completed" {
		t.Fatalf("Run = %+v", run)
	}
	if len(mentions) != 3 || mentions[0] != 0 || mentions[1] != 1 || mentions[2] != 1 {
		t.Fatalf("mention occurrences per request = %v, want 0,1,1", mentions)
	}
	var trace []map[string]any
	_ = json.Unmarshal(run.Progress, &trace)
	recorded := 0
	for _, entry := range trace {
		if content, _ := entry["content"].(string); entry["type"] == "chat_mention" && strings.Contains(content, "please check ETH") && !strings.HasPrefix(content, "[Group chat]") {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("the delivered tag must be recorded once in the Run trace: %s", run.Progress)
	}
}

func TestAccountWithChatReadDisabledGetsNoInbox(t *testing.T) {
	fixture := newMessageToolFixture()
	fixture.accountExtra = map[string]any{"chat_read": false}
	fixture.generate = func(w http.ResponseWriter, _ *http.Request, _ int) {
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("quiet", "quiet"), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	runtime := fixture.open(t)
	if _, run := startMessageRun(t, runtime); run.Status != "completed" {
		t.Fatalf("Run = %+v", run)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.inboxCalls != 0 {
		t.Fatalf("inbox read %d times for an account that may not read the chat", fixture.inboxCalls)
	}
}

func TestTagsFromWhileReadingWasDisabledAreNotDelivered(t *testing.T) {
	enabled := time.Now().UTC()
	fixture := newMessageToolFixture()
	fixture.accountExtra = map[string]any{"chat_read": true, "chat_read_since": enabled.Format(time.RFC3339Nano)}
	fixture.inbox = func(w http.ResponseWriter, _ *http.Request, _ int) {
		messageFixtureJSON(w, http.StatusOK, map[string]any{"messages": []any{chatMessage("during-ban", "old tag", enabled.Add(-time.Minute))}, "page": 1, "has_more": false})
	}
	fixture.generate = func(w http.ResponseWriter, _ *http.Request, _ int) {
		messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("quiet", "quiet"), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	runtime := fixture.open(t)
	_, run := startMessageRun(t, runtime)
	if run.Status != "completed" || strings.Contains(string(run.InitialContext), "during-ban") {
		t.Fatalf("Run = %+v", run)
	}
}

func TestPostChatPreservesModerationErrorsAndWakeUpShape(t *testing.T) {
	for _, tc := range []struct {
		code    string
		status  int
		message string
	}{
		{"chat_muted", 403, "你已被管理者禁言，原因：洗版；剩餘 10 分鐘（到 2026-10-25T00:10:00Z）"},
		{"chat_rate_limited", 429, "發言太頻繁：每 1 分鐘最多 3 則，約 1 分鐘後可再發言"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			fixture := newMessageToolFixture()
			fixture.accountExtra = map[string]any{"chat_muted_until": "2026-10-25T00:10:00Z", "chat_mute_reason": "洗版", "chat_rate_minutes": 1, "chat_rate_count": 3}
			fixture.message = func(w http.ResponseWriter, r *http.Request, _ int) {
				if r.Method != "POST" {
					t.Errorf("method = %s", r.Method)
				}
				messageFixtureJSON(w, tc.status, map[string]any{"error": tc.code, "message": tc.message})
			}
			fixture.generate = func(w http.ResponseWriter, r *http.Request, call int) {
				if call == 1 {
					body := decodeMessageGatewayRequest(t, r)
					tool := messageToolByName(t, body, "post_chat")
					description, _ := tool["description"].(string)
					if !strings.Contains(description, "chat_muted") || !strings.Contains(description, "chat_rate_limited") {
						t.Error("post_chat must describe moderation errors")
					}
					messageToolCall(w, "moderated", "post_chat", map[string]any{"content": "hi"})
					return
				}
				result := messageToolResult(t, r, "moderated")
				failure, _ := result["error"].(map[string]any)
				if len(result) != 2 || result["ok"] != false || len(failure) != 3 || failure["code"] != tc.code || failure["message"] != tc.message || failure["outcome"] != "not_executed" {
					t.Errorf("result = %#v", result)
				}
				messageFixtureJSON(w, 200, map[string]any{"content": finalizationContent("observed", "observed"), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			runtime := fixture.open(t)
			_, run := startMessageRun(t, runtime)
			if run.Status != "completed" {
				t.Fatalf("Run=%+v", run)
			}
			for _, field := range []string{"chat_muted_until", "chat_mute_reason", "chat_rate_minutes", "chat_rate_count"} {
				if strings.Contains(string(run.InitialContext), field) {
					t.Fatalf("wake-up added %s: %s", field, run.InitialContext)
				}
			}
		})
	}
}
