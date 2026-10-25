package test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agent "stock-ai/agent-server/internal/agent"
)

type wakeChat struct {
	mu       sync.Mutex
	messages []map[string]any
	reads    int
}

func (c *wakeChat) add(id, author string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := chatMessage(id, "text-"+id, at)
	m["user_name"] = author
	c.messages = append(c.messages, m)
}

func (c *wakeChat) serve(t *testing.T, w http.ResponseWriter, request *http.Request, _ int) {
	t.Helper()
	if request.Header.Get("Authorization") != "Bearer ticket-25-account-v1" || request.URL.Query().Get("tag") != "Message Agent" {
		t.Errorf("wake query or credential invalid: %s", request.URL.RequestURI())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	messages := slices.Clone(c.messages)
	if after := request.URL.Query().Get("after"); after != "" {
		index := slices.IndexFunc(messages, func(m map[string]any) bool { return m["id"] == after })
		if index < 0 {
			messageFixtureJSON(w, 400, map[string]any{"error": "invalid_request", "message": "cursor removed"})
			return
		}
		messages = messages[index+1:]
	}
	if request.URL.Query().Get("sort") == "desc" {
		slices.Reverse(messages)
	}
	more := len(messages) > 10
	if more {
		messages = messages[:10]
	}
	if messages == nil {
		messages = []map[string]any{}
	}
	messageFixtureJSON(w, 200, map[string]any{"messages": messages, "page": 1, "has_more": more})
}

func openWakeRuntime(t *testing.T, fixture *messageToolFixture, path string) *agent.AgentRuntime {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(fixture.serveBackend))
	gateway := httptest.NewServer(http.HandlerFunc(fixture.serveGateway))
	t.Cleanup(backend.Close)
	t.Cleanup(gateway.Close)
	runtime, err := agent.OpenAgentRuntime(agent.RuntimeConfig{
		DatabasePath: path, AdminToken: messageAgentToken, BackendURL: backend.URL, BackendUserToken: messageBackendToken,
		GatewayURL: gateway.URL, GatewayRuntimeToken: messageGatewayToken, DefaultPrompt: "wake test", DefaultMaxLoop: 6,
		DefaultMaxToolCall: 6, RequestTimeout: time.Second, ToolTimeout: time.Second, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func TestChatWakeUserBurstAndRestart(t *testing.T) {
	fixture := newMessageToolFixture()
	chat := &wakeChat{}
	fixture.inbox = func(w http.ResponseWriter, r *http.Request, n int) { chat.serve(t, w, r, n) }
	script := newLLMScript(respondStop("initial"), respondStop("USER burst"), respondStop("recovery"))
	fixture.generate = func(w http.ResponseWriter, r *http.Request, _ int) { script.handler(t)(w, r) }
	path := filepath.Join(t.TempDir(), "agent.db")
	runtime := openWakeRuntime(t, fixture, path)
	session, _ := startMessageRun(t, runtime)
	script.next(t, "initial Run")
	clock := newEventClock()
	task := runtime.ChatWakeTask(clock)
	if task.Interval != 5*time.Second {
		t.Fatalf("interval = %s", task.Interval)
	}
	poll := func() {
		t.Helper()
		if err := task.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		chat.add("m"+jsonNumber(int64(i)), "USER", clock.Now())
		poll()
		clock.Advance(3 * time.Second)
	}
	poll()
	if session := getWakeSession(t, runtime, session.ID); session.CurrentRunID != 1 {
		t.Fatalf("woke before quiet time: %+v", session)
	}
	clock.Advance(6 * time.Second)
	poll()
	script.expectNoRequest(t, "9 seconds after last tag")
	clock.Advance(time.Second)
	poll()
	wake := script.next(t, "USER wake")
	context := runContext(t, wake)
	if context["trigger"] != "event" {
		t.Fatalf("wake = %v", context)
	}
	triggers := context["triggers"].([]any)
	if len(triggers) != 1 || triggers[0].(map[string]any)["type"] != "chat_mention" {
		t.Fatalf("triggers = %v", triggers)
	}
	ids := triggers[0].(map[string]any)["message_ids"].([]any)
	if len(ids) != 3 || ids[0] != "m1" || ids[2] != "m3" {
		t.Fatalf("message ids = %v", ids)
	}
	run := waitForMessageRunID(t, runtime, session.ID, 2)
	if ids, _ := inboxOf(t, run); ids != "m1,m2,m3" {
		t.Fatalf("burst inbox = %s", ids)
	}
	var trace []map[string]any
	_ = json.Unmarshal(run.Progress, &trace)
	for _, entry := range trace {
		if entry["type"] == "chat_mention" {
			t.Fatalf("inbox duplicated during Run: %s", run.Progress)
		}
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	runtime = openWakeRuntime(t, fixture, path)
	script.next(t, "recovery")
	waitForMessageRunID(t, runtime, session.ID, 3)
	if err := runtime.ChatWakeTask(clock).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := getWakeSession(t, runtime, session.ID).CurrentRunID; got != 3 {
		t.Fatalf("duplicate wake after restart: %d", got)
	}
	script.expectNoRequest(t, "duplicate USER tag after restart")
}

func TestChatWakeIgnoresAgentsDisabledAndStopped(t *testing.T) {
	fixture := newMessageToolFixture()
	chat := &wakeChat{}
	fixture.inbox = func(w http.ResponseWriter, r *http.Request, n int) { chat.serve(t, w, r, n) }
	fixture.generate = func(w http.ResponseWriter, r *http.Request, _ int) {
		writeFixtureJSON(w, 200, finalResponse("done", "done"))
	}
	runtime := openWakeRuntime(t, fixture, filepath.Join(t.TempDir(), "agent.db"))
	session, _ := startMessageRun(t, runtime)
	clock := newEventClock()
	poll := func() {
		t.Helper()
		if err := runtime.ChatWakeTask(clock).Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	chat.add("agent", "Other Agent", clock.Now())
	clock.Advance(20 * time.Second)
	poll()
	if getWakeSession(t, runtime, session.ID).CurrentRunID != 1 {
		t.Fatal("Agent tag woke idle Session")
	}
	fixture.mu.Lock()
	fixture.accountExtra = map[string]any{"chat_read": false}
	fixture.mu.Unlock()
	chat.add("disabled", "USER", clock.Now())
	chat.mu.Lock()
	reads := chat.reads
	chat.mu.Unlock()
	poll()
	chat.mu.Lock()
	unchanged := chat.reads == reads
	chat.mu.Unlock()
	if !unchanged {
		t.Fatal("polled chat while chat_read=false")
	}
	clock.Advance(time.Second)
	fixture.mu.Lock()
	fixture.accountExtra = map[string]any{"chat_read": true, "chat_read_since": clock.Now().Format(time.RFC3339Nano)}
	fixture.mu.Unlock()
	clock.Advance(20 * time.Second)
	poll()
	if getWakeSession(t, runtime, session.ID).CurrentRunID != 1 {
		t.Fatal("disabled-period tag woke after enabling")
	}
	for _, action := range []string{"stop", "delete"} {
		method, path := http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop"
		if action == "delete" {
			method, path = http.MethodDelete, "/api/v1/sessions/"+session.ID
		}
		if status, raw := messageRuntimeRequest(t, runtime.Handler(), method, path, nil); status != 200 {
			t.Fatalf("%s = %d %s", action, status, raw)
		}
		chat.add(action, "USER", clock.Now())
		clock.Advance(20 * time.Second)
		poll()
		if getWakeSession(t, runtime, session.ID).CurrentRunID != 1 {
			t.Fatalf("woke %s Session", action)
		}
	}
}

func TestChatWakeDuringRunAndFinalization(t *testing.T) {
	fixture := newMessageToolFixture()
	chat := &wakeChat{}
	fixture.inbox = func(w http.ResponseWriter, r *http.Request, n int) { chat.serve(t, w, r, n) }
	release, finish := make(chan struct{}), make(chan struct{})
	script := newLLMScript(
		holdUntil(release, respondToolCalls(toolCall("noop", "list_events", map[string]any{}))),
		holdUntil(finish, respondStop("finished")), respondStop("late tag"),
	)
	fixture.generate = func(w http.ResponseWriter, r *http.Request, _ int) { script.handler(t)(w, r) }
	runtime := openWakeRuntime(t, fixture, filepath.Join(t.TempDir(), "agent.db"))
	status, raw := messageRuntimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", map[string]any{"name": "wake", "account_id": messageAccountID, "model_name": "message-model"})
	if status != 201 {
		t.Fatalf("create = %d %s", status, raw)
	}
	session := decodeBody[agent.Session](t, raw)
	if status, _ := messageRuntimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", nil); status != 202 {
		t.Fatal(status)
	}
	script.next(t, "busy Run")
	clock := newEventClock()
	chat.add("during", "USER", clock.Now())
	poll := func() {
		t.Helper()
		if err := runtime.ChatWakeTask(clock).Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	close(release)
	request := script.next(t, "next decision")
	encoded, _ := json.Marshal(request["messages"])
	if strings.Count(string(encoded), "text-during") != 1 {
		t.Fatalf("steer = %s", encoded)
	}
	poll()
	chat.add("late", "USER", clock.Now())
	clock.Advance(10 * time.Second)
	poll()
	if getWakeSession(t, runtime, session.ID).CurrentRunID != 1 {
		t.Fatal("parallel Run opened")
	}
	close(finish)
	waitForMessageRunID(t, runtime, session.ID, 1)
	poll()
	wake := script.next(t, "late tag wakes idle Run")
	if contextText(wake) == "" {
		t.Fatal("missing wake context")
	}
	run := waitForMessageRunID(t, runtime, session.ID, 2)
	if ids, _ := inboxOf(t, run); ids != "late" {
		t.Fatalf("late inbox = %s", ids)
	}
}

func getWakeSession(t *testing.T, runtime *agent.AgentRuntime, id string) agent.Session {
	t.Helper()
	status, raw := messageRuntimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+id, nil)
	if status != 200 {
		t.Fatalf("get Session = %d %s", status, raw)
	}
	return decodeBody[agent.Session](t, raw)
}

func TestChatWakeMigrationAndAtomicClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE session_chat_cursors (session_id TEXT PRIMARY KEY, read_after TEXT NOT NULL DEFAULT '', mention_after TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_chat_cursors VALUES ('legacy', 'read', 'delivered')`); err != nil {
		t.Fatal(err)
	}
	fixture := newMessageToolFixture()
	chat := &wakeChat{}
	fixture.inbox = func(w http.ResponseWriter, r *http.Request, n int) { chat.serve(t, w, r, n) }
	fixture.generate = func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeFixtureJSON(w, 200, finalResponse("done", "done"))
	}
	runtime := openWakeRuntime(t, fixture, path)
	var read, mention, wake string
	if err := db.QueryRow(`SELECT read_after, mention_after, wake_after FROM session_chat_cursors WHERE session_id = 'legacy'`).Scan(&read, &mention, &wake); err != nil || read != "read" || mention != "delivered" || wake != "delivered" {
		t.Fatalf("migration = %q/%q/%q err=%v", read, mention, wake, err)
	}
	session, _ := startMessageRun(t, runtime)
	clock := newEventClock()
	chat.add("atomic", "USER", clock.Now())
	clock.Advance(10 * time.Second)
	installRejectingTrigger(t, path, "reject_wake_run", `BEFORE INSERT ON runs WHEN NEW.trigger = 'event' BEGIN SELECT RAISE(FAIL, 'reject wake run'); END`)
	if err := runtime.ChatWakeTask(clock).Run(t.Context()); err == nil || !strings.Contains(err.Error(), "reject wake run") {
		t.Fatalf("claim error = %v", err)
	}
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM pending_triggers WHERE session_id = ?`, session.ID); count != 0 {
		t.Fatalf("rollback pending = %d", count)
	}
	if count := databaseCount(t, db, `SELECT COUNT(*) FROM session_chat_cursors WHERE session_id = ? AND wake_after = 'atomic'`, session.ID); count != 0 {
		t.Fatal("failed claim advanced wake cursor")
	}
	if getWakeSession(t, runtime, session.ID).CurrentRunID != 1 {
		t.Fatal("failed claim incremented Run id")
	}
	if err := runtime.Close(); err == nil {
		t.Fatal("claim persistence error not latched")
	}
}

func TestChatWakeFindsAndDeliversUserBeyondOnePage(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(fmt.Sprint("seeded=", seeded), func(t *testing.T) {
			fixture := newMessageToolFixture()
			chat := &wakeChat{}
			if seeded {
				chat.add("seed", "Other Agent", time.Now().Add(-time.Minute))
			}
			fixture.inbox = func(w http.ResponseWriter, r *http.Request, n int) { chat.serve(t, w, r, n) }
			script := newLLMScript(respondStop("initial"), respondStop("USER read"))
			fixture.generate = func(w http.ResponseWriter, r *http.Request, _ int) { script.handler(t)(w, r) }
			runtime := openWakeRuntime(t, fixture, filepath.Join(t.TempDir(), "agent.db"))
			session, _ := startMessageRun(t, runtime)
			script.next(t, "initial")
			clock := newEventClock()
			if !seeded {
				chat.add("user", "USER", clock.Now())
			}
			for i := 0; i < 11; i++ {
				chat.add("agent-"+jsonNumber(int64(i)), "Other Agent", clock.Now())
				clock.Advance(time.Millisecond)
			}
			if seeded {
				if err := runtime.ChatWakeTask(clock).Run(t.Context()); err != nil {
					t.Fatal(err)
				}
				chat.add("user", "USER", clock.Now())
			}
			clock.Advance(10 * time.Second)
			if err := runtime.ChatWakeTask(clock).Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			wake := script.next(t, "USER beyond one page")
			if !strings.Contains(contextText(wake), "text-user") {
				t.Fatalf("trigger woke without USER content: %s", contextText(wake))
			}
			waitForMessageRunID(t, runtime, session.ID, 2)
		})
	}
}

func TestChatWakeDeletedCursorDoesNotRewake(t *testing.T) {
	fixture := newMessageToolFixture()
	chat := &wakeChat{}
	fixture.inbox = func(w http.ResponseWriter, r *http.Request, n int) { chat.serve(t, w, r, n) }
	script := newLLMScript(respondStop("initial"), respondStop("wake"))
	fixture.generate = func(w http.ResponseWriter, r *http.Request, _ int) { script.handler(t)(w, r) }
	runtime := openWakeRuntime(t, fixture, filepath.Join(t.TempDir(), "agent.db"))
	session, _ := startMessageRun(t, runtime)
	script.next(t, "initial")
	waitForMessageRunID(t, runtime, session.ID, 1)
	clock := newEventClock()
	chat.add("m1", "USER", clock.Now())
	chat.add("m2", "USER", clock.Now())
	clock.Advance(10 * time.Second)
	if err := runtime.ChatWakeTask(clock).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	script.next(t, "wake")
	waitForMessageRunID(t, runtime, session.ID, 2)
	chat.mu.Lock()
	chat.messages = slices.DeleteFunc(chat.messages, func(m map[string]any) bool { return m["id"] == "m2" })
	chat.mu.Unlock()
	clock.Advance(10 * time.Second)
	if err := runtime.ChatWakeTask(clock).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := getWakeSession(t, runtime, session.ID).CurrentRunID; got != 2 {
		t.Fatalf("delivered tag woke Run %d after its cursor message was deleted", got)
	}
}
