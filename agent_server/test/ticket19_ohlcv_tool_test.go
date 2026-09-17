package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func openMarketReadRuntime(t *testing.T, fixture *runtimeFixture, marketHandler http.HandlerFunc) *common.AgentRuntime {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		fixture.backendRequests = append(fixture.backendRequests, request.Method+" "+request.URL.RequestURI())
		fixture.backendAuth = append(fixture.backendAuth, request.Header.Get("Authorization"))
		accountToken, accountStatus := fixture.accountToken, fixture.accountStatus
		fixture.mu.Unlock()
		if strings.HasPrefix(request.URL.Path, "/v1/accounts/") {
			if request.Header.Get("Authorization") != "Bearer "+backendUserToken {
				writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "bad token"})
				return
			}
			accountID := strings.TrimPrefix(request.URL.Path, "/v1/accounts/")
			writeFixtureJSON(w, http.StatusOK, map[string]any{"id": accountID, "status": accountStatus, "token": accountToken})
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+accountToken {
			writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "bad token"})
			return
		}
		marketHandler(w, request)
	}))
	gateway := httptest.NewServer(http.HandlerFunc(fixture.serveGateway))
	t.Cleanup(backend.Close)
	t.Cleanup(gateway.Close)
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: agentAdminToken,
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

func TestTicket19OHLCVToolSchemaBackendMappingAndCorrelatedResult(t *testing.T) {
	fixture := newRuntimeFixture()
	start := "2026-09-13T10:00:00Z"
	end := "2026-09-13T11:00:00Z"
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []struct {
				Role       string  `json:"role"`
				Content    *string `json:"content"`
				ToolCallID string  `json:"tool_call_id"`
			} `json:"messages"`
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"input_schema"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode generate request: %v", err)
		}
		if round.Add(1) == 1 {
			var schema map[string]any
			for _, tool := range body.Tools {
				if tool.Name == "get_ohlcv" {
					schema = tool.InputSchema
				}
			}
			if schema == nil || schema["additionalProperties"] != false {
				t.Errorf("get_ohlcv schema = %#v", schema)
			}
			fixture.mu.Lock()
			fixture.accountToken = "account-token-after-reset"
			fixture.mu.Unlock()
			writeToolCall(w, "call-ohlcv", "get_ohlcv", map[string]any{
				"market": "crypto", "symbol": "BTCUSDT", "interval": "1m",
				"start_time": start, "end_time": end, "limit": 2,
			})
			return
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role != "tool" || last.ToolCallID != "call-ohlcv" || last.Content == nil ||
			!strings.Contains(*last.Content, `"columns":["open_time_unix_ms","open","high","low","close","volume"]`) ||
			!strings.Contains(*last.Content, `"rows":[[`) || strings.Contains(*last.Content, `"candles"`) ||
			strings.Contains(*last.Content, `"close_time"`) || !strings.Contains(*last.Content, `"ok":true`) {
			t.Errorf("correlated OHLCV result = %+v", last)
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("candles checked", "candles checked", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	marketCalls := atomic.Int32{}
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, request *http.Request) {
		marketCalls.Add(1)
		if request.URL.Path != "/v1/market/ohlcv" || request.URL.Query().Get("market") != "crypto" ||
			request.URL.Query().Get("symbol") != "BTCUSDT" || request.URL.Query().Get("interval") != "1m" ||
			request.URL.Query().Get("start_time") != start || request.URL.Query().Get("end_time") != end || request.URL.Query().Get("limit") != "2" {
			t.Errorf("Backend OHLCV request = %s", request.URL.RequestURI())
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"market": "crypto", "symbol": "BTCUSDT", "interval": "1m", "start_time": start, "end_time": end,
			"time_zone": "UTC", "includes_incomplete": false,
			"candles": []any{map[string]any{"open_time": start, "close_time": "2026-09-13T10:00:59.999Z", "open": 100, "high": 102, "low": 99, "close": 101, "volume": 5}},
		})
	})
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", status, raw)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ModelCalls != 2 || run.ToolCalls != 1 || marketCalls.Load() != 1 {
		t.Fatalf("Run/calls = %+v market_calls=%d", run, marketCalls.Load())
	}
	fixture.mu.Lock()
	auth := append([]string(nil), fixture.backendAuth...)
	fixture.mu.Unlock()
	if len(auth) != 4 || auth[3] != "Bearer account-token-after-reset" {
		t.Fatalf("Backend credentials = %v", auth)
	}
}

func TestTicket19OHLCVInvalidArgumentsBackendErrorsAndTimeoutUseCommonToolFlow(t *testing.T) {
	start := "2026-09-13T10:00:00Z"
	end := "2026-09-13T11:00:00Z"
	for _, test := range []struct {
		name         string
		arguments    map[string]any
		backend      func(http.ResponseWriter, *http.Request)
		wantCode     string
		wantMarketIO bool
		toolTimeout  time.Duration
	}{
		{name: "invalid range", arguments: map[string]any{"symbol": "BTCUSDT", "interval": "1m", "start_time": end, "end_time": start}, wantCode: "INVALID_TOOL_ARGUMENTS"},
		{name: "limit exceeds cap", arguments: map[string]any{"symbol": "BTCUSDT", "interval": "1m", "start_time": start, "end_time": end, "limit": 501}, wantCode: "INVALID_TOOL_ARGUMENTS"},
		{name: "Backend error preserved", arguments: map[string]any{"symbol": "UNKNOWN", "interval": "1m", "start_time": start, "end_time": end}, wantCode: "symbol_not_found", wantMarketIO: true,
			backend: func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "symbol_not_found", "message": "missing"})
			}},
		{name: "timeout", arguments: map[string]any{"symbol": "BTCUSDT", "interval": "1m", "start_time": start, "end_time": end}, wantCode: "TOOL_TIMEOUT", wantMarketIO: true, toolTimeout: 20 * time.Millisecond,
			backend: func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.toolTimeout = test.toolTimeout
			var round atomic.Int32
			fixture.generate = func(w http.ResponseWriter, request *http.Request) {
				current := round.Add(1)
				if current == 1 {
					writeToolCall(w, "ohlcv-error", "get_ohlcv", test.arguments)
					return
				}
				var body map[string]any
				_ = json.NewDecoder(request.Body).Decode(&body)
				messages := body["messages"].([]any)
				last := messages[len(messages)-1].(map[string]any)
				if last["tool_call_id"] != "ohlcv-error" || !strings.Contains(last["content"].(string), test.wantCode) {
					t.Errorf("Tool Error = %v", last)
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

func TestTicket19OHLCVPreservesFractionalOffsetRangeInBackendQuery(t *testing.T) {
	fixture := newRuntimeFixture()
	start := "2026-09-13T18:00:00.0004+08:00"
	end := "2026-09-13T19:00:00.0016+08:00"
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if round.Add(1) == 1 {
			writeToolCall(w, "fractional-range", "get_ohlcv", map[string]any{
				"symbol": "BTCUSDT", "interval": "1m", "start_time": start, "end_time": end,
			})
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("range checked", "range checked", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	var marketCalls atomic.Int32
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, request *http.Request) {
		marketCalls.Add(1)
		if request.URL.Query().Get("start_time") != start || request.URL.Query().Get("end_time") != end {
			t.Errorf("Backend range query = %s", request.URL.RawQuery)
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"candles": []any{}})
	})
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", status, raw)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ToolCalls != 1 || marketCalls.Load() != 1 {
		t.Fatalf("Run=%+v market_calls=%d", run, marketCalls.Load())
	}
}

func TestTicket19OHLCVBodyReadTimeoutAfterHeadersIsCorrelatedUnknownAndNotRetried(t *testing.T) {
	for _, statusCode := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			fixture := newRuntimeFixture()
			fixture.toolTimeout = 30 * time.Millisecond
			var rounds atomic.Int32
			fixture.generate = func(w http.ResponseWriter, request *http.Request) {
				if rounds.Add(1) == 1 {
					writeToolCall(w, "ohlcv-partial-body", "get_ohlcv", map[string]any{
						"symbol": "BTCUSDT", "interval": "1m",
						"start_time": "2026-09-13T10:00:00Z", "end_time": "2026-09-13T11:00:00Z",
					})
					return
				}
				assertCorrelatedToolError(t, request, "ohlcv-partial-body", "TOOL_TIMEOUT", "unknown")
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

func TestTicket19OHLCVFullyDeliveredMalformedJSONRemainsInvalid(t *testing.T) {
	fixture := newRuntimeFixture()
	var rounds atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if rounds.Add(1) == 1 {
			writeToolCall(w, "ohlcv-malformed", "get_ohlcv", map[string]any{
				"symbol": "BTCUSDT", "interval": "1m",
				"start_time": "2026-09-13T10:00:00Z", "end_time": "2026-09-13T11:00:00Z",
			})
			return
		}
		assertCorrelatedToolError(t, request, "ohlcv-malformed", "BACKEND_RESPONSE_INVALID", "unknown")
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("invalid response observed", "invalid response observed", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	var marketCalls atomic.Int32
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, _ *http.Request) {
		marketCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candles":`))
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

func TestTicket19HardStopDuringMarketBodyReadDoesNotDispatchOrOverwrite(t *testing.T) {
	fixture := newRuntimeFixture()
	bodyStarted := make(chan struct{})
	releaseBody := make(chan struct{})
	bodyFinished := make(chan struct{})
	nextModelStarted := make(chan struct{}, 1)
	var rounds atomic.Int32
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if rounds.Add(1) == 1 {
			writeToolCall(w, "ohlcv-hard-stop", "get_ohlcv", map[string]any{
				"symbol": "BTCUSDT", "interval": "1m",
				"start_time": "2026-09-13T10:00:00Z", "end_time": "2026-09-13T11:00:00Z",
			})
			return
		}
		nextModelStarted <- struct{}{}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("too late", "too late", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	var marketCalls atomic.Int32
	runtime := openMarketReadRuntime(t, fixture, func(w http.ResponseWriter, _ *http.Request) {
		marketCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candles":[`))
		w.(http.Flusher).Flush()
		close(bodyStarted)
		<-releaseBody
		_, _ = w.Write([]byte(`]}`))
		close(bodyFinished)
	})
	session := createStoppedSession(t, runtime, nil)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status=%d", status)
	}
	waitSignal(t, bodyStarted, "partial Backend body")
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/stop", agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", status, raw)
	}
	close(releaseBody)
	waitSignal(t, bodyFinished, "late Backend body")
	select {
	case <-nextModelStarted:
		t.Fatal("hard stop dispatched a next model request")
	default:
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "interrupted")
	if run.Status != "interrupted" || run.Output != nil || run.Summary != nil || run.ToolCalls != 1 || marketCalls.Load() != 1 || rounds.Load() != 1 {
		t.Fatalf("late body changed interrupted Run=%+v market_calls=%d model_rounds=%d", run, marketCalls.Load(), rounds.Load())
	}
}

func assertCorrelatedToolError(t *testing.T, request *http.Request, callID, code, outcome string) {
	t.Helper()
	var body struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Errorf("decode generate request: %v", err)
		return
	}
	last := body.Messages[len(body.Messages)-1]
	var result struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Outcome string `json:"outcome"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(last.Content), &result); err != nil {
		t.Errorf("decode Tool result %q: %v", last.Content, err)
		return
	}
	if last.Role != "tool" || last.ToolCallID != callID || result.OK || result.Error.Code != code || result.Error.Outcome != outcome {
		t.Errorf("correlated Tool result = %+v decoded=%+v", last, result)
	}
}
