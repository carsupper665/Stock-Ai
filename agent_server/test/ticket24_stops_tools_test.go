package test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTicket24SetPositionStopsInjectsRunSourceAndForwardsExactLevels(t *testing.T) {
	backend := newTradingBackend(t, func(w http.ResponseWriter, request *http.Request, _ string, body []byte) {
		if request.Method != http.MethodPatch || request.URL.Path != "/v1/positions/pos_1" {
			t.Errorf("unexpected Backend request %s %s", request.Method, request.URL.RequestURI())
			writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "missing"})
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"id": "pos_1", "stop_loss": 58000, "take_profit": 65000.5, "echo": string(body)})
	})
	backend.addAccount(testAccountID, "tok-24")
	gateway := newScriptedGateway()
	gateway.script(testAccountID,
		scriptedCall{ID: "both", Name: "set_position_stops", Arguments: map[string]any{"position_id": "pos_1", "stop_loss": 58000, "take_profit": 65000.5}},
		scriptedCall{ID: "only-tp", Name: "set_position_stops", Arguments: map[string]any{"position_id": "pos_1", "take_profit": 66000}},
		scriptedCall{ID: "remove-sl", Name: "set_position_stops", Arguments: map[string]any{"position_id": "pos_1", "stop_loss": 0}},
	)
	runtime := openTradingRuntime(t, backend, gateway, time.Second)
	session := createStoppedSession(t, runtime, map[string]any{"max_loop": 8, "max_tool_call": 3})
	startSessionRun(t, runtime, session)
	if run := waitForRunStatus(t, runtime, session.ID, 1, "completed"); run.ToolCalls != 3 {
		t.Fatalf("Run = %+v", run)
	}
	properties := gateway.schema(t, "set_position_stops")["properties"].(map[string]any)
	for _, forbidden := range []string{"account_id", "session_id", "run_id", "source"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("set_position_stops schema exposes %s", forbidden)
		}
	}
	source := `"source":{"run_id":1,"session_id":"` + session.ID + `"}`
	requests := backend.authenticatedRequests()
	if len(requests) != 3 || requests[0].Body != `{`+source+`,"stop_loss":58000,"take_profit":65000.5}` ||
		requests[1].Body != `{`+source+`,"take_profit":66000}` || requests[2].Body != `{`+source+`,"stop_loss":0}` {
		t.Fatalf("Backend requests = %+v", requests)
	}
	result := decodeToolResult(t, gateway.result(t, "both"))
	if !result.OK || !strings.Contains(string(result.Result), `"take_profit":65000.5`) {
		t.Fatalf("set_position_stops result = %s", gateway.result(t, "both"))
	}
}

func TestTicket24SetPositionStopsErrorsAreFixedAndRetriesCostBudget(t *testing.T) {
	valid := map[string]any{"position_id": "pos_1", "stop_loss": 58000}
	cases := []mutationErrorCase{
		{name: "identity in arguments", arguments: map[string]any{"position_id": "pos_1", "stop_loss": 58000, "session_id": "s_x"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "no level given", arguments: map[string]any{"position_id": "pos_1"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "null level", arguments: map[string]any{"position_id": "pos_1", "stop_loss": nil}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "negative level", arguments: map[string]any{"position_id": "pos_1", "take_profit": -5}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "string level", arguments: map[string]any{"position_id": "pos_1", "take_profit": "65000"}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "missing position", arguments: map[string]any{"stop_loss": 58000}, wantCode: "INVALID_TOOL_ARGUMENTS", wantOutcome: "not_executed"},
		{name: "wrong side rejected by Backend", arguments: map[string]any{"position_id": "pos_1", "stop_loss": 61000}, wantCode: "invalid_request", wantMessage: "stop_loss", wantOutcome: "not_executed", wantIO: 1,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "多單的 stop_loss 必須低於進場價、take_profit 必須高於進場價；空單相反"})
			}},
		{name: "closed or foreign position", arguments: valid, wantCode: "not_found", wantOutcome: "not_executed", wantIO: 1,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "部位已不存在"})
			}},
	}
	runMutationErrorCases(t, "set_position_stops", append(cases, backendFailureCases(valid)...))
}
