package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"stock-ai/agent-server/internal/skills"
	"stock-ai/agent-server/internal/tools"
)

const maxSkillRunes = 4000

func TestSkillsLibraryEntriesAreReadableAndBounded(t *testing.T) {
	index := skills.Index()
	if len(index) == 0 {
		t.Fatal("skill library is empty")
	}
	found := false
	for _, entry := range index {
		content, ok := skills.Get(entry.Name)
		if !ok || entry.Description == "" || content == "" {
			t.Fatalf("skill %+v unreadable", entry)
		}
		if runes := utf8.RuneCountInString(content); runes > maxSkillRunes {
			t.Fatalf("skill %s has %d runes; keep it under %d so it can stay in context", entry.Name, runes, maxSkillRunes)
		}
		found = found || entry.Name == "trade-plan"
	}
	if !found {
		t.Fatalf("trade-plan missing from %+v", index)
	}
	if _, ok := skills.Get("missing"); ok {
		t.Fatal("Get must reject unknown names")
	}
}

func generateRecorder(t *testing.T, steps func(call int) map[string]any) (func(http.ResponseWriter, *http.Request), func() []map[string]any) {
	var mu sync.Mutex
	requests := make([]map[string]any, 0)
	handler := func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode generate request: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		call := len(requests)
		mu.Unlock()
		writeFixtureJSON(w, http.StatusOK, steps(call))
	}
	recorded := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), requests...)
	}
	return handler, recorded
}

func requestToolNames(request map[string]any) map[string]bool {
	names := make(map[string]bool)
	definitions, _ := request["tools"].([]any)
	for _, raw := range definitions {
		definition, _ := raw.(map[string]any)
		name, _ := definition["name"].(string)
		names[name] = true
	}
	return names
}

func TestSkillsToggleControlsToolsContextAndDispatch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("skills_enabled=%v", enabled), func(t *testing.T) {
			fixture := newRuntimeFixture()
			var recorded func() []map[string]any
			fixture.generate, recorded = generateRecorder(t, func(call int) map[string]any {
				if call == 1 {
					return toolStep("", toolCall("skill-read", "skill_get", map[string]any{"name": "trade-plan"}))
				}
				return finalResponse("done", "done")
			})
			runtime, _, _ := fixture.open(t, "")
			session := createStoppedSession(t, runtime, map[string]any{"skills_enabled": enabled})
			if session.SkillsEnabled != enabled {
				t.Fatalf("created Session skills_enabled=%v", session.SkillsEnabled)
			}
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodGet, "/api/v1/sessions/"+session.ID, agentAdminToken, nil)
			if status != http.StatusOK || !strings.Contains(string(raw), fmt.Sprintf(`"skills_enabled":%v`, enabled)) {
				t.Fatalf("stored Session status=%d body=%s", status, raw)
			}
			startSessionForTest(t, runtime, session.ID)
			waitForRunStatus(t, runtime, session.ID, 1, "completed")

			requests := recorded()
			if len(requests) != 2 {
				t.Fatalf("generate calls=%d", len(requests))
			}
			names := requestToolNames(requests[0])
			if names["skill_get"] != enabled || names["plan_trade"] != enabled || !names["place_order"] {
				t.Fatalf("tool names=%v", names)
			}
			messages := requestMessages(requests[0])
			system, initial := messageText(messages[0]), messageText(messages[1])
			if strings.Contains(initial, `"skill_index"`) != enabled || strings.Contains(system, "skill_index") != enabled ||
				strings.Contains(system, "skill_get") != enabled {
				t.Fatalf("enabled=%v system=%q initial=%s", enabled, system, initial)
			}
			second := requestMessages(requests[1])
			result := messageText(second[len(second)-1])
			if enabled && !strings.Contains(result, "plan_trade does the arithmetic") {
				t.Fatalf("skill_get result=%s", result)
			}
			if !enabled && !strings.Contains(result, "UNKNOWN_TOOL") {
				t.Fatalf("disabled skill_get result=%s", result)
			}
		})
	}
}

// Tool names live in separate definition and dispatch switches; this catches a tool defined but never executed.
func TestEveryDefinedToolIsDispatched(t *testing.T) {
	definitions := tools.Definitions(true)
	calls := make([]map[string]any, 0, len(definitions))
	for index, definition := range definitions {
		calls = append(calls, toolCall(fmt.Sprintf("call-%d", index), definition["name"].(string), map[string]any{}))
	}
	fixture := newRuntimeFixture()
	var recorded func() []map[string]any
	fixture.generate, recorded = generateRecorder(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("", calls...)
		}
		return finalResponse("done", "done")
	})
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"skills_enabled": true, "max_tool_call": len(definitions)})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	requests := recorded()
	if len(requests) != 2 {
		t.Fatalf("generate calls=%d", len(requests))
	}
	results := 0
	for _, message := range requestMessages(requests[1]) {
		if message["role"] != "tool" {
			continue
		}
		results++
		if strings.Contains(messageText(message), "UNKNOWN_TOOL") {
			t.Errorf("tool call %v is defined but not dispatched: %s", message["tool_call_id"], messageText(message))
		}
	}
	if results != len(definitions) {
		t.Fatalf("tool results=%d, want %d", results, len(definitions))
	}
}

func planFor(t *testing.T, arguments string, entry, equity, available float64, tickSize, stepSize string) map[string]any {
	t.Helper()
	in, err := tools.PlanTradeInputs(json.RawMessage(arguments))
	if err != nil {
		t.Fatalf("PlanTradeInputs(%s) error = %v", arguments, err)
	}
	return tools.PlanTrade(in, entry, equity, available, tickSize, stepSize)
}

func TestPlanTradeComputesTheTradeNumbers(t *testing.T) {
	buy := planFor(t, `{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"target_r":2,"risk_pct":1,"leverage":5}`, 100, 10000, 10000, "0.01", "0.001")
	for field, want := range map[string]float64{
		"entry": 100, "stop_loss": 98, "take_profit": 104, "risk_per_unit": 2, "reward_per_unit": 4, "r_multiple": 2,
		"quantity": 50, "notional": 5000, "margin_required": 1000, "max_loss": 100, "max_gain": 200, "max_loss_pct_of_equity": 1,
	} {
		if buy[field] != want {
			t.Errorf("buy %s=%v want %v", field, buy[field], want)
		}
	}
	order, _ := buy["order"].(map[string]any)
	if order["type"] != "market" || order["quantity"] != 50.0 || order["stop_loss"] != 98.0 || order["take_profit"] != 104.0 ||
		order["leverage"] != 5.0 || order["product"] != "futures" || order["market"] != "crypto" {
		t.Fatalf("buy order=%v", buy["order"])
	}
	if _, hasPrice := order["price"]; hasPrice {
		t.Fatalf("market order must not carry a price: %v", order)
	}

	sell := planFor(t, `{"symbol":"ETHUSDT","side":"sell","type":"limit","price":100,"stop_loss":103,"take_profit":94,"quantity":2}`, 100, 1000, 1000, "", "")
	if sell["risk_per_unit"] != 3.0 || sell["reward_per_unit"] != 6.0 || sell["r_multiple"] != 2.0 || sell["max_loss"] != 6.0 {
		t.Fatalf("sell plan=%v", sell)
	}
	if order, _ := sell["order"].(map[string]any); order["type"] != "limit" || order["price"] != 100.0 {
		t.Fatalf("limit order=%v", sell["order"])
	}

	rounded := planFor(t, `{"symbol":"BTCUSDT","side":"buy","stop_loss":97.987,"take_profit":110,"risk_pct":1}`, 100, 10000, 100000, "0.01", "0.01")
	if rounded["stop_loss"] != 97.99 || rounded["quantity"] != 49.75 {
		t.Fatalf("tick and step rounding=%v", rounded)
	}
	onStep := planFor(t, `{"symbol":"BTCUSDT","side":"buy","stop_loss":90,"take_profit":110,"quantity":0.3}`, 100, 10000, 10000, "", "0.1")
	if onStep["quantity"] != 0.3 {
		t.Fatalf("a quantity already on the step must not floor down: %v", onStep["quantity"])
	}
}

func TestPlanTradeReportsProblemsWithoutAnOrder(t *testing.T) {
	for _, test := range []struct {
		name, arguments string
		available       float64
		want            string
	}{
		{"buy stop above entry", `{"symbol":"BTCUSDT","side":"buy","stop_loss":101,"target_r":2,"risk_pct":1}`, 10000, "must be below the entry"},
		{"sell stop below entry", `{"symbol":"BTCUSDT","side":"sell","stop_loss":99,"target_r":2,"risk_pct":1}`, 10000, "must be above the entry"},
		{"target on the wrong side", `{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"take_profit":99,"risk_pct":1}`, 10000, "take_profit"},
		{"margin over available", `{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"target_r":2,"quantity":10}`, 500, "exceeds the available balance"},
		{"quantity rounds to zero", `{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"target_r":2,"quantity":0.4}`, 10000, "rounds to 0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := planFor(t, test.arguments, 100, 10000, test.available, "0.01", "1")
			problems, _ := plan["problems"].([]string)
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), test.want) {
				t.Fatalf("problems=%v", plan["problems"])
			}
			if _, hasOrder := plan["order"]; hasOrder {
				t.Fatalf("a plan with problems must not offer an order: %v", plan)
			}
		})
	}
}

func TestPlanTradeRejectsAmbiguousArguments(t *testing.T) {
	for _, arguments := range []string{
		`{"symbol":"BTCUSDT","side":"buy","target_r":2,"risk_pct":1}`,
		`{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"take_profit":104,"target_r":2,"risk_pct":1}`,
		`{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"risk_pct":1}`,
		`{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"target_r":2}`,
		`{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"target_r":2,"risk_pct":1,"quantity":1}`,
		`{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"target_r":2,"risk_pct":101}`,
		`{"symbol":"BTCUSDT","side":"buy","price":100,"stop_loss":98,"target_r":2,"risk_pct":1}`,
		`{"symbol":"BTCUSDT","side":"buy","type":"limit","stop_loss":98,"target_r":2,"risk_pct":1}`,
		`{"symbol":"BTCUSDT","side":"buy","product":"spot","leverage":2,"stop_loss":98,"target_r":2,"risk_pct":1}`,
		`{"symbol":"BTCUSDT","side":"hold","stop_loss":98,"target_r":2,"risk_pct":1}`,
		`{"symbol":"BTCUSDT","side":"buy","stop_loss":98,"target_r":2,"risk_pct":1,"extra":true}`,
	} {
		if _, err := tools.PlanTradeInputs(json.RawMessage(arguments)); err == nil {
			t.Errorf("PlanTradeInputs(%s) accepted ambiguous arguments", arguments)
		}
	}
}

func TestPlanTradeReadsAccountRulesAndPriceFromBackend(t *testing.T) {
	fixture := newRuntimeFixture()
	var recorded func() []map[string]any
	fixture.generate, recorded = generateRecorder(t, func(call int) map[string]any {
		if call == 1 {
			return toolStep("", toolCall("plan", "plan_trade", map[string]any{
				"symbol": "TSLAUSDT", "side": "buy", "stop_loss": 375, "target_r": 2, "risk_pct": 1, "leverage": 2,
			}))
		}
		return finalResponse("planned", "planned")
	})
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/account":
			writeFixtureJSON(w, http.StatusOK, map[string]any{"id": testAccountID, "balance": 10000, "locked_margin": 0,
				"available": 10000, "unrealized_pnl": 0, "equity": 10000})
		case "/v1/market/info":
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"market": "crypto", "symbol": "TSLAUSDT", "source": "binance", "updated_at": "2026-10-21T12:00:00Z",
				"source_rules": map[string]any{"status": "TRADING", "base_asset": "TSLA", "quote_asset": "USDT", "order_types": []string{"LIMIT", "MARKET"},
					"price_filter":    map[string]any{"min_price": "0.01", "max_price": "100000", "tick_size": "0.01"},
					"quantity_filter": map[string]any{"min_quantity": "0.01", "max_quantity": "20000", "step_size": "0.01"},
					"min_notional":    "5"},
				"backend_execution": map[string]any{"mode": "virtual", "source_rules_enforced": false},
			})
		case "/v1/market/price":
			writeFixtureJSON(w, http.StatusOK, map[string]any{"market": "crypto", "symbol": "TSLAUSDT", "price": 380.23,
				"updated_at": time.Now().UTC().Format(time.RFC3339Nano)})
		default:
			t.Errorf("unexpected Backend request %s", request.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	})
	session := createStoppedSession(t, runtime, map[string]any{"skills_enabled": true})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	requests := recorded()
	messages := requestMessages(requests[len(requests)-1])
	result := messageText(messages[len(messages)-1])
	for _, wanted := range []string{`"ok":true`, `"entry":380.23`, `"risk_per_unit":5.23`, `"quantity":19.12`, `"take_profit":390.69`,
		`"max_loss_pct_of_equity":1`, `"problems":[]`, `"order":{`, `"symbol":"TSLAUSDT"`} {
		if !strings.Contains(result, wanted) {
			t.Fatalf("plan_trade result misses %s: %s", wanted, result)
		}
	}
}

func TestSkillTextSurvivesContextCompaction(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.models = compactionModels(20000)
	fixture.market = largeSnapshots(32000)
	script := &compactionScript{}
	fixture.generate = script.handler(t, func(call int) map[string]any {
		switch call {
		case 1:
			return toolStep("step one",
				toolCall("c1-skill", "skill_get", map[string]any{"name": "trade-plan"}),
				snapshotCall("c1-read", "BTCUSDT"),
				snapshotCall("c1-read-again", "ETHUSDT"))
		case 2:
			return toolStep("step two", snapshotCall("c2-read", "BTCUSDT"))
		default:
			return finalResponse("done", "done")
		}
	}, failCompaction(t))
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 6, "max_tool_call": 10, "skills_enabled": true})
	startSessionForTest(t, runtime, session.ID)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.main) != 3 {
		t.Fatalf("main generate calls = %d", len(script.main))
	}
	messages := requestMessages(script.main[2])
	history := messageText(messages[2])
	if !strings.HasPrefix(history, "[Context compacted]") || !strings.Contains(history, "plan_trade does the arithmetic") ||
		strings.Contains(history, "~~~~") {
		t.Fatalf("compacted history must keep the skill text and drop read outputs: %s", history)
	}
}
