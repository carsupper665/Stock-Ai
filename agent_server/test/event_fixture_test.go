package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

// priceBook is the Backend GET /v1/market/price fixture for Server-side sampling, which
// authenticates with the Backend USER credential rather than a Session Account Token.
type priceBook struct {
	mu       sync.Mutex
	quotes   map[string]map[string]any
	statuses map[string]int
	handlers map[string]http.HandlerFunc
	requests []string
}

func newPriceBook() *priceBook {
	return &priceBook{
		quotes: make(map[string]map[string]any), statuses: make(map[string]int), handlers: make(map[string]http.HandlerFunc),
	}
}

func (b *priceBook) key(market, symbol string) string { return market + "/" + symbol }

func quoteBody(market, symbol string, price float64, updatedAt time.Time) map[string]any {
	return map[string]any{
		"market": market, "symbol": symbol, "price": price, "updated_at": updatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (b *priceBook) set(market, symbol string, price float64, updatedAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.statuses, b.key(market, symbol))
	b.quotes[b.key(market, symbol)] = quoteBody(market, symbol, price, updatedAt)
}

func (b *priceBook) setRaw(market, symbol string, body map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.statuses, b.key(market, symbol))
	b.quotes[b.key(market, symbol)] = body
}

func (b *priceBook) fail(market, symbol string, status int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.statuses[b.key(market, symbol)] = status
}

func (b *priceBook) handle(market, symbol string, handler http.HandlerFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[b.key(market, symbol)] = handler
}

func (b *priceBook) requestCount(market, symbol string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, request := range b.requests {
		if request == b.key(market, symbol) {
			count++
		}
	}
	return count
}

func (b *priceBook) serve(w http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer "+backendUserToken {
		writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "sampling must use the USER credential"})
		return
	}
	key := b.key(request.URL.Query().Get("market"), request.URL.Query().Get("symbol"))
	b.mu.Lock()
	b.requests = append(b.requests, key)
	status, failing := b.statuses[key]
	quote, known := b.quotes[key]
	handler := b.handlers[key]
	b.mu.Unlock()
	switch {
	case handler != nil:
		handler(w, request)
	case failing:
		writeFixtureJSON(w, status, map[string]any{"error": "market_unavailable", "message": "fixture failure"})
	case known:
		writeFixtureJSON(w, http.StatusOK, quote)
	default:
		writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "symbol_not_found", "message": "unknown symbol"})
	}
}

// openEventRuntime mirrors runtimeFixture.open but routes Server-side price sampling to book.
func openEventRuntime(t *testing.T, fixture *runtimeFixture, book *priceBook, databasePath string) *common.AgentRuntime {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/market/price" {
			book.serve(w, request)
			return
		}
		fixture.serveBackend(w, request)
	}))
	gateway := httptest.NewServer(http.HandlerFunc(fixture.serveGateway))
	t.Cleanup(backend.Close)
	t.Cleanup(gateway.Close)
	if databasePath == "" {
		databasePath = filepath.Join(t.TempDir(), "agent.db")
	}
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: databasePath, AdminToken: agentAdminToken,
		BackendURL: backend.URL, BackendUserToken: backendUserToken,
		GatewayURL: gateway.URL, GatewayRuntimeToken: gatewayToken,
		DefaultPrompt: "default mission", DefaultMaxLoop: 4, DefaultMaxToolCall: 3,
		RequestTimeout: time.Second, ToolTimeout: durationOr(fixture.toolTimeout, time.Second), StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// completeRecoveryRun consumes the server_recovery Run that Ticket 36 starts for every running
// Session when the Runtime reopens, so the next Gateway request is the wake-up under test. It
// returns the recovery Run's decoded Generate request for assertions on recovery_context.
func completeRecoveryRun(t *testing.T, script *llmScript, runtime *common.AgentRuntime, sessionID string, runID int64) map[string]any {
	t.Helper()
	request := script.next(t, "server_recovery Run")
	if runContext(t, request)["trigger"] != "server_recovery" {
		t.Fatalf("expected the server_recovery Run, got %s", contextText(request))
	}
	waitForRunStatus(t, runtime, sessionID, runID, "completed")
	return request
}

func recoveryContext(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	recovery, _ := runContext(t, request)["recovery_context"].(map[string]any)
	if recovery == nil {
		t.Fatalf("Run context has no recovery_context: %s", contextText(request))
	}
	return recovery
}

func assertInitialContextBasics(t *testing.T, request map[string]any, maxLoop, maxToolCall int) {
	t.Helper()
	context := runContext(t, request)
	account, _ := context["account_snapshot"].(map[string]any)
	if context["max_loop"] != float64(maxLoop) || context["max_tool_call"] != float64(maxToolCall) ||
		account["ok"] != true || account["id"] != testAccountID || account["user_name"] != "fixture" ||
		account["status"] != "active" || account["initial_balance"] != float64(100000) || account["balance"] != float64(97500) {
		t.Fatalf("Generate initial context basics = %s", contextText(request))
	}
	text := contextText(request)
	if strings.Contains(text, backendUserToken) || strings.Contains(text, "account-token-v1") || strings.Contains(text, gatewayToken) {
		t.Fatalf("Generate initial context leaked credentials: %s", text)
	}
}

func waitForSampleCount(t *testing.T, book *priceBook, market, symbol string, atLeast int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for book.requestCount(market, symbol) < atLeast {
		select {
		case <-deadline:
			t.Fatalf("%s/%s was sampled %d times, want at least %d", market, symbol, book.requestCount(market, symbol), atLeast)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func priceEventCall(id, eventType, symbol string, price float64) map[string]any {
	return createEventCall(id, eventType, map[string]any{"symbol": symbol, "price": price})
}

// newEventClock starts the manual Event clock at the wall clock: create_event validates
// "at > now" against wall time while due/expiry use this clock, so Event times computed
// from clock.Now() stay in the future for both as long as the clock only moves forward.
func newEventClock() *manualClock {
	return &manualClock{now: time.Now().UTC(), armChanged: make(chan struct{})}
}

func (c *manualClock) AdvanceTo(t *testing.T, at time.Time) {
	t.Helper()
	if at.Before(c.Now()) {
		t.Fatalf("clock cannot move backwards to %s", at)
	}
	c.Advance(at.Sub(c.Now()))
}

// llmStep answers one Generate request; body is the decoded request and request carries the
// context so a held step can leave when the Runtime abandons the call (stop, close, failure).
type llmStep func(w http.ResponseWriter, request *http.Request, body map[string]any)

// llmScript is the Gateway fixture: each Generate call is decoded, published on requests as a
// synchronization point, and answered by the next scripted step (idle stop when exhausted).
type llmScript struct {
	mu       sync.Mutex
	steps    []llmStep
	requests chan map[string]any
}

func newLLMScript(steps ...llmStep) *llmScript {
	return &llmScript{steps: steps, requests: make(chan map[string]any, 256)}
}

func (s *llmScript) handler(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Generate request: %v", err)
		}
		s.mu.Lock()
		var step llmStep
		if len(s.steps) != 0 {
			step, s.steps = s.steps[0], s.steps[1:]
		}
		s.mu.Unlock()
		s.requests <- body
		if step == nil {
			step = respondStop("idle")
		}
		step(w, request, body)
	}
}

func (s *llmScript) append(steps ...llmStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, steps...)
}

func (s *llmScript) next(t *testing.T, name string) map[string]any {
	t.Helper()
	select {
	case request := <-s.requests:
		return request
	case <-time.After(3 * time.Second):
		t.Fatalf("Gateway request %q did not arrive", name)
		return nil
	}
}

func (s *llmScript) expectNoRequest(t *testing.T, name string) {
	t.Helper()
	select {
	case request := <-s.requests:
		t.Fatalf("unexpected Gateway request %q: %s", name, contextText(request))
	default:
	}
}

// respondStop completes the Run normally; since Ticket 33 that requires the finalization object.
func respondStop(content string) llmStep {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": finalizationContent(content, content, nil, nil), "tool_calls": []any{}, "finish_reason": "stop", "usage": nil,
		})
	}
}

func respondToolCalls(calls ...map[string]any) llmStep {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"content": nil, "finish_reason": "tool_call", "usage": nil, "tool_calls": calls,
		})
	}
}

func holdUntil(release <-chan struct{}, then llmStep) llmStep {
	return func(w http.ResponseWriter, request *http.Request, body map[string]any) {
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		then(w, request, body)
	}
}

func toolCall(id, name string, arguments map[string]any) map[string]any {
	return map[string]any{"id": id, "name": name, "arguments": arguments}
}

func createEventCall(id, eventType string, params map[string]any) map[string]any {
	return toolCall(id, "create_event", map[string]any{"type": eventType, "params": params})
}

func timerParams(at time.Time) map[string]any {
	return map[string]any{"at": at.UTC().Format(time.RFC3339Nano)}
}

// contextText returns the factual Run context the Runtime sends as the user message.
func contextText(request map[string]any) string {
	messages, _ := request["messages"].([]any)
	if len(messages) < 2 {
		return ""
	}
	text, _ := messages[1].(map[string]any)["content"].(string)
	return text
}

func runContext(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(contextText(request)), &decoded); err != nil {
		t.Fatalf("decode Run context %q: %v", contextText(request), err)
	}
	return decoded
}

func contextTriggers(t *testing.T, request map[string]any) []map[string]any {
	t.Helper()
	raw, _ := runContext(t, request)["triggers"].([]any)
	triggers := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		trigger, _ := item.(map[string]any)
		triggers = append(triggers, trigger)
	}
	return triggers
}

// lastToolResult decodes the tool message that answers the previous scripted tool call.
func lastToolResult(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	messages, _ := request["messages"].([]any)
	var last map[string]any
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		if message["role"] == "tool" {
			last = message
			break
		}
	}
	if last == nil {
		t.Fatalf("request has no tool result: %v", messages)
	}
	content, _ := last["content"].(string)
	var result map[string]any
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		t.Fatalf("decode tool result %q: %v", content, err)
	}
	return result
}

func toolResults(t *testing.T, request map[string]any) []map[string]any {
	t.Helper()
	messages, _ := request["messages"].([]any)
	results := make([]map[string]any, 0)
	for _, item := range messages {
		message, _ := item.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		content, _ := message["content"].(string)
		var result map[string]any
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatalf("decode tool result %q: %v", content, err)
		}
		results = append(results, result)
	}
	return results
}

func toolErrorCode(result map[string]any) string {
	toolError, _ := result["error"].(map[string]any)
	code, _ := toolError["code"].(string)
	return code
}

func resultEvent(t *testing.T, result map[string]any) common.Event {
	t.Helper()
	if result["ok"] != true {
		t.Fatalf("tool result is not ok: %v", result)
	}
	raw, _ := json.Marshal(result["result"])
	return decodeBody[common.Event](t, raw)
}

func listEventsHTTP(t *testing.T, runtime *common.AgentRuntime, sessionID string) []common.Event {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+sessionID+"/events", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("GET events status = %d body = %s", status, raw)
	}
	return decodeBody[struct {
		Events []common.Event `json:"events"`
	}](t, raw).Events
}

// eventTasks holds the two resident tasks exactly as cmd/agent-server registers them. One value
// must be reused across rounds because the price task keeps the previous round's samples.
type eventTasks struct {
	local    common.BackgroundTask
	price    common.BackgroundTask
	position common.BackgroundTask
}

func newEventTasks(runtime *common.AgentRuntime, clock common.Clock) eventTasks {
	return eventTasks{
		local: runtime.LocalEventTask(clock, time.Second), price: runtime.PriceEventTask(clock, time.Second),
		position: runtime.PositionEventTask(clock, time.Second),
	}
}

func (tasks eventTasks) all() []common.BackgroundTask {
	return []common.BackgroundTask{tasks.local, tasks.price, tasks.position}
}

// runEventRound drives one local claim and then one sampling+claim, the deterministic order
// the ordering assertions rely on; the real EventLoop runs the two callbacks concurrently.
func runEventRound(t *testing.T, tasks eventTasks) {
	t.Helper()
	for _, task := range tasks.all() {
		if err := task.Run(context.Background()); err != nil {
			t.Fatalf("%s round error = %v", task.Name, err)
		}
	}
}

func waitForEventCount(t *testing.T, runtime *common.AgentRuntime, sessionID string, wanted int) []common.Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		events := listEventsHTTP(t, runtime, sessionID)
		if len(events) == wanted {
			return events
		}
		select {
		case <-deadline:
			t.Fatalf("Session %s has %d events, want %d", sessionID, len(events), wanted)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func eventTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse event time %q: %v", value, err)
	}
	return parsed
}

func startedRunIDs(t *testing.T, runtime *common.AgentRuntime, sessionID string) []int64 {
	t.Helper()
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet,
		"/api/v1/sessions/"+sessionID+"/runs", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("list Runs status = %d body = %s", status, raw)
	}
	runs := decodeBody[struct {
		Runs []common.Run `json:"runs"`
	}](t, raw).Runs
	ids := make([]int64, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.RunID)
	}
	return ids
}

func containsAll(text string, wanted ...string) bool {
	for _, item := range wanted {
		if !strings.Contains(text, item) {
			return false
		}
	}
	return true
}
