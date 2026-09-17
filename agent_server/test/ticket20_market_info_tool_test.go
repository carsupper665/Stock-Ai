package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTicket20MarketInfoToolSchemaBackendMappingAndCorrelatedResult(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode generate request: %v", err)
		}
		if round.Add(1) == 1 {
			found := false
			for _, raw := range body["tools"].([]any) {
				tool := raw.(map[string]any)
				if tool["name"] == "get_market_info" {
					found = true
					schema := tool["input_schema"].(map[string]any)
					if schema["additionalProperties"] != false {
						t.Errorf("get_market_info schema=%v", schema)
					}
				}
			}
			if !found {
				t.Errorf("get_market_info schema absent: %v", body["tools"])
			}
			writeToolCall(w, "call-info", "get_market_info", map[string]any{"market": "crypto", "symbol": "BTCUSDT"})
			return
		}
		messages := body["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		content := last["content"].(string)
		if last["tool_call_id"] != "call-info" || !strings.Contains(content, `"source":"binance"`) ||
			!strings.Contains(content, `"source_rules_enforced":false`) || !strings.Contains(content, `"min_notional":null`) {
			t.Errorf("Market Info Tool result=%v", last)
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("market rules checked", "market rules checked", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	var marketCalls atomic.Int32
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, request *http.Request) {
		marketCalls.Add(1)
		if request.URL.Path != "/v1/market/info" || request.URL.Query().Get("market") != "crypto" || request.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Errorf("Backend Market Info request=%s", request.URL.RequestURI())
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"market": "crypto", "symbol": "BTCUSDT", "source": "binance", "updated_at": "2026-09-13T12:00:00Z",
			"source_rules": map[string]any{"status": "TRADING", "base_asset": "BTC", "quote_asset": "USDT", "order_types": []string{"LIMIT", "MARKET"},
				"spot_trading_allowed": true, "margin_trading_allowed": false, "price_filter": nil, "quantity_filter": nil, "min_notional": nil},
			"backend_execution": map[string]any{"mode": "virtual", "source_rules_enforced": false},
		})
	})
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status=%d", status)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ModelCalls != 2 || run.ToolCalls != 1 || marketCalls.Load() != 1 {
		t.Fatalf("Run=%+v market_calls=%d", run, marketCalls.Load())
	}
}

func TestTicket20MarketInfoInvalidBackendErrorAndTimeoutUseCommonToolFlow(t *testing.T) {
	for _, test := range []struct {
		name         string
		arguments    map[string]any
		backend      func(http.ResponseWriter, *http.Request)
		wantCode     string
		wantMarketIO bool
		toolTimeout  time.Duration
	}{
		{name: "invalid symbol type", arguments: map[string]any{"symbol": 7}, wantCode: "INVALID_TOOL_ARGUMENTS"},
		{name: "unknown symbol", arguments: map[string]any{"symbol": "UNKNOWN"}, wantCode: "symbol_not_found", wantMarketIO: true,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "symbol_not_found", "message": "missing"})
			}},
		{name: "timeout", arguments: map[string]any{"symbol": "BTCUSDT"}, wantCode: "TOOL_TIMEOUT", wantMarketIO: true, toolTimeout: 20 * time.Millisecond,
			backend: func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.toolTimeout = test.toolTimeout
			var round atomic.Int32
			fixture.generate = func(w http.ResponseWriter, request *http.Request) {
				if round.Add(1) == 1 {
					writeToolCall(w, "info-error", "get_market_info", test.arguments)
					return
				}
				var body map[string]any
				_ = json.NewDecoder(request.Body).Decode(&body)
				messages := body["messages"].([]any)
				last := messages[len(messages)-1].(map[string]any)
				if last["tool_call_id"] != "info-error" || !strings.Contains(last["content"].(string), test.wantCode) {
					t.Errorf("Tool Error=%v", last)
				}
				writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("error handled", "error handled", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			var marketCalls atomic.Int32
			runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, request *http.Request) {
				marketCalls.Add(1)
				if test.backend == nil {
					t.Errorf("unexpected Backend request %s", request.URL.RequestURI())
					return
				}
				test.backend(w, request)
			})
			session := createStoppedSession(t, runtime, nil)
			status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			if status != http.StatusAccepted {
				t.Fatalf("start status=%d", status)
			}
			run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
			if run.ToolCalls != 1 || (marketCalls.Load() == 1) != test.wantMarketIO {
				t.Fatalf("Run=%+v market_calls=%d", run, marketCalls.Load())
			}
		})
	}
}

func TestTicket20MarketInfoBodyReadTimeoutAfterHeadersIsCorrelatedUnknownAndNotRetried(t *testing.T) {
	for _, statusCode := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.toolTimeout = 30 * time.Millisecond
			var rounds atomic.Int32
			fixture.generate = func(w http.ResponseWriter, request *http.Request) {
				if rounds.Add(1) == 1 {
					writeToolCall(w, "info-partial-body", "get_market_info", map[string]any{"symbol": "BTCUSDT"})
					return
				}
				assertCorrelatedToolError(t, request, "info-partial-body", "TOOL_TIMEOUT", "unknown")
				writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("timeout observed", "timeout observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
			}
			var marketCalls atomic.Int32
			runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, request *http.Request) {
				marketCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(statusCode)
				_, _ = w.Write([]byte(`{"error":"market_timeout"`))
				w.(http.Flusher).Flush()
				<-request.Context().Done()
			})
			session := createStoppedSession(t, runtime, nil)
			status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
			if status != http.StatusAccepted {
				t.Fatalf("start status=%d body=%s", status, raw)
			}
			run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
			if run.ToolCalls != 1 || run.ModelCalls != 2 || marketCalls.Load() != 1 || rounds.Load() != 2 {
				t.Fatalf("Run=%+v market_calls=%d model_rounds=%d", run, marketCalls.Load(), rounds.Load())
			}
		})
	}
}

func TestTicket20MarketInfoFullyDeliveredMalformedJSONRemainsInvalid(t *testing.T) {
	fixture := newRuntimeFixture()
	var rounds atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if rounds.Add(1) == 1 {
			writeToolCall(w, "info-malformed", "get_market_info", map[string]any{"symbol": "BTCUSDT"})
			return
		}
		assertCorrelatedToolError(t, request, "info-malformed", "BACKEND_RESPONSE_INVALID", "unknown")
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("invalid response observed", "invalid response observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	var marketCalls atomic.Int32
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, _ *http.Request) {
		marketCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"source_rules":`))
	})
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status=%d", status)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ToolCalls != 1 || marketCalls.Load() != 1 {
		t.Fatalf("Run=%+v market_calls=%d", run, marketCalls.Load())
	}
}
