package test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type positionCloseBackendState struct {
	mu         sync.Mutex
	present    bool
	fail       bool
	trigger    string
	reduceOnly bool
}

func (s *positionCloseBackendState) set(present, fail bool) {
	s.mu.Lock()
	s.present, s.fail = present, fail
	s.mu.Unlock()
}

func (s *positionCloseBackendState) serve(w http.ResponseWriter, request *http.Request, _ string, _ []byte) {
	s.mu.Lock()
	present, fail, trigger, reduceOnly := s.present, s.fail, s.trigger, s.reduceOnly
	s.mu.Unlock()
	if request.URL.Path == "/v1/positions" {
		if fail {
			writeFixtureJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
			return
		}
		positions := []any{}
		if present {
			positions = append(positions, map[string]any{
				"id": "pos_watch", "market": "crypto", "symbol": "BTCUSDT", "product": "futures", "side": "long",
			})
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"positions": positions})
		return
	}
	if request.URL.Path == "/v1/ledger" {
		writeFixtureJSON(w, http.StatusOK, map[string]any{"entries": []any{map[string]any{
			"seq": 2, "event": "fill", "order_id": "ord_close", "position_id": "pos_watch", "trigger": trigger,
		}}})
		return
	}
	if request.URL.Path == "/v1/orders/ord_close" {
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"id": "ord_close", "status": "filled", "reduce_only": reduceOnly, "trigger": trigger,
		})
		return
	}
	writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
}

func createPositionCloseEvent(t *testing.T, state *positionCloseBackendState) (*common.AgentRuntime, *scriptedGateway, common.Session, common.Event) {
	t.Helper()
	backend := newTradingBackend(t, state.serve)
	backend.addAccount(testAccountID, "position-token")
	gateway := newScriptedGateway()
	gateway.script(testAccountID, scriptedCall{
		ID: "watch-close", Name: "create_event", Arguments: map[string]any{
			"type": "position_close", "params": map[string]any{"position_id": "pos_watch"},
		},
	})
	runtime := openTradingRuntime(t, backend, gateway, time.Second)
	session := createStoppedSession(t, runtime, nil)
	startSessionRun(t, runtime, session)
	waitForRunStatus(t, runtime, session.ID, 1, "completed")
	events := listEventsHTTP(t, runtime, session.ID)
	if len(events) != 1 || events[0].Type != "position_close" || string(events[0].State) != "null" ||
		!strings.Contains(string(events[0].Params), `"position_id":"pos_watch"`) {
		t.Fatalf("created position event = %+v", events)
	}
	return runtime, gateway, session, events[0]
}

func positionCloseTrigger(t *testing.T, runtime *common.AgentRuntime, sessionID string) map[string]any {
	t.Helper()
	run := waitForRunStatus(t, runtime, sessionID, 2, "completed")
	var triggers []map[string]any
	if err := json.Unmarshal(run.Triggers, &triggers); err != nil || len(triggers) != 1 {
		t.Fatalf("position close triggers = %s, error = %v", run.Triggers, err)
	}
	return triggers[0]
}

func TestPositionCloseEventRequiresObservedPositionAndIgnoresBackendFailure(t *testing.T) {
	state := &positionCloseBackendState{trigger: "stop_loss"}
	runtime, gateway, session, event := createPositionCloseEvent(t, state)
	clock := newEventClock()
	task := runtime.PositionEventTask(clock, time.Second)

	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("initial absent poll: %v", err)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || string(events[0].State) != "null" {
		t.Fatalf("initial absence consumed or initialized event: %+v", events)
	}
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("initial absence started a Run: %v", ids)
	}

	state.set(false, true)
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("failed Backend poll: %v", err)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("Backend failure consumed event: %+v", events)
	}

	state.set(true, false)
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("position observation poll: %v", err)
	}
	observed := listEventsHTTP(t, runtime, session.ID)
	if len(observed) != 1 || !strings.Contains(string(observed[0].State), `"id":"pos_watch"`) {
		t.Fatalf("position observation was not persisted: %+v", observed)
	}

	state.set(false, true)
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("failed poll after observation: %v", err)
	}
	if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
		t.Fatalf("temporary failure looked like a close: %v", ids)
	}

	state.set(false, false)
	clock.Advance(time.Second)
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("close poll: %v", err)
	}
	trigger := positionCloseTrigger(t, runtime, session.ID)
	matched, _ := trigger["matched"].(map[string]any)
	params, _ := trigger["params"].(map[string]any)
	if trigger["type"] != "position_close" || trigger["event_id"] != event.ID || params["position_id"] != "pos_watch" ||
		matched["position_id"] != "pos_watch" || matched["symbol"] != "BTCUSDT" || matched["product"] != "futures" ||
		matched["side"] != "long" || matched["close_reason"] != "stop_loss" {
		t.Fatalf("position close trigger = %v", trigger)
	}
	if events := listEventsHTTP(t, runtime, session.ID); len(events) != 0 {
		t.Fatalf("fired position event remains active: %+v", events)
	}
	if enum := gateway.schema(t, "create_event")["properties"].(map[string]any)["type"].(map[string]any)["enum"].([]any); !containsJSONValue(enum, "position_close") || containsJSONValue(enum, "stop_loss") || containsJSONValue(enum, "take_profit") {
		t.Fatalf("create_event type enum = %v", enum)
	}
}

func TestPositionCloseEventReportsOnlyBackendSupportedCloseReason(t *testing.T) {
	tests := []struct {
		name       string
		trigger    string
		reduceOnly bool
		wantReason string
	}{
		{name: "take profit", trigger: "take_profit", wantReason: "take_profit"},
		{name: "liquidation evidence", trigger: "liquidation", wantReason: "liquidation"},
		{name: "manual reduce-only close", reduceOnly: true, wantReason: "manual"},
		{name: "ordinary fill has no fabricated reason"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &positionCloseBackendState{present: true, trigger: test.trigger, reduceOnly: test.reduceOnly}
			runtime, _, session, _ := createPositionCloseEvent(t, state)
			task := runtime.PositionEventTask(newEventClock(), time.Second)
			if err := task.Run(context.Background()); err != nil {
				t.Fatalf("observe position: %v", err)
			}
			state.set(false, false)
			if err := task.Run(context.Background()); err != nil {
				t.Fatalf("detect close: %v", err)
			}
			matched := positionCloseTrigger(t, runtime, session.ID)["matched"].(map[string]any)
			if reason, present := matched["close_reason"]; test.wantReason == "" {
				if present {
					t.Fatalf("fabricated close reason = %v", reason)
				}
			} else if !present || reason != test.wantReason {
				t.Fatalf("close_reason = %v, want %q", reason, test.wantReason)
			}
		})
	}
}

func containsJSONValue(values []any, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
