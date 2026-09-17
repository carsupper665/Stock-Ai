package test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

const crossBackendUserToken = "ticket-19-20-backend-user"

type crossBackendProcess struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	done    chan struct{}
	waitMu  sync.Mutex
	waitErr error
}

type crossProcessOutput struct {
	mu sync.Mutex
	bytes.Buffer
}

func (o *crossProcessOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Buffer.Write(value)
}

func (o *crossProcessOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Buffer.String()
}

type crossGatewayRequest struct {
	Messages []struct {
		Role       string  `json:"role"`
		Content    *string `json:"content"`
		ToolCallID string  `json:"tool_call_id"`
	} `json:"messages"`
}

type crossAccount struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Token  string `json:"token"`
}

func TestTicket19CrossServiceOHLCVUsesActualBackendAndPropagatesCancellation(t *testing.T) {
	const injectedSymbol = "btcusdt&limit=500&symbol=ethusdt"
	const normalizedSymbol = "BTCUSDT&LIMIT=500&SYMBOL=ETHUSDT"

	var sourceCalls atomic.Int32
	sourceStarted := make(chan struct{})
	sourceCanceled := make(chan struct{})
	releaseSource := make(chan struct{})
	var startedOnce, canceledOnce sync.Once
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		sourceCalls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v3/klines" {
			t.Errorf("source request = %s %s", request.Method, request.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		query := request.URL.Query()
		switch query.Get("symbol") {
		case normalizedSymbol:
			if len(query) != 5 || len(query["symbol"]) != 1 || query.Get("interval") != "1m" ||
				query.Get("startTime") != "1577959200000" || query.Get("endTime") != "1577959499999" || query.Get("limit") != "2" {
				t.Errorf("Binance OHLCV query = %#v", query)
			}
			writeFixtureJSON(w, http.StatusOK, []any{
				[]any{int64(1577959380000), "13.00", "14.00", "12.00", "13.50", "6.00", int64(1577959439999), "0", 1, "0", "0", "0"},
				[]any{int64(1577959320000), "12.00", "13.00", "11.00", "12.50", "5.00", int64(1577959379999), "0", 1, "0", "0", "0"},
				[]any{int64(1577959140000), "9.00", "10.00", "8.00", "9.50", "2.00", int64(1577959199999), "0", 1, "0", "0", "0"},
				[]any{int64(1577959260000), "11.00", "12.00", "10.00", "11.50", "4.00", int64(32503680000000), "0", 1, "0", "0", "0"},
				[]any{int64(1577959200000), "10.00", "11.00", "9.00", "10.50", "3.00", int64(1577959259999), "0", 1, "0", "0", "0"},
			})
		case "CANCELUSDT":
			startedOnce.Do(func() { close(sourceStarted) })
			select {
			case <-request.Context().Done():
				canceledOnce.Do(func() { close(sourceCanceled) })
			case <-releaseSource:
			}
		default:
			t.Errorf("unexpected source symbol/query = %#v", query)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(source.Close)
	t.Cleanup(func() { close(releaseSource) })

	backendURL := startCrossBackend(t, source.URL)
	account := createCrossAccount(t, backendURL)
	if account.ID == "" || account.Token == "" || account.Status != "active" {
		t.Fatalf("created Backend Account = %+v", account)
	}

	t.Run("completed candles are capped ordered and UTC", func(t *testing.T) {
		fixture := newRuntimeFixture()
		secondTurn := make(chan crossGatewayRequest, 1)
		var round atomic.Int32
		fixture.generate = func(w http.ResponseWriter, request *http.Request) {
			if round.Add(1) == 1 {
				writeToolCall(w, "cross-ohlcv", "get_ohlcv", map[string]any{
					"symbol": injectedSymbol, "interval": "1m", "start_time": "2020-01-02T12:00:00+02:00",
					"end_time": "2020-01-02T12:05:00+02:00", "limit": 2,
				})
				return
			}
			var body crossGatewayRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode second Gateway turn: %v", err)
			}
			secondTurn <- body
			writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("OHLCV accepted", "OHLCV accepted", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
		}
		runtime := openCrossServiceRuntime(t, backendURL, crossBackendUserToken, fixture, 2*time.Second)
		session := createStoppedSession(t, runtime, map[string]any{"account_id": account.ID})
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
		if status != http.StatusAccepted {
			t.Fatalf("start status=%d body=%s", status, raw)
		}
		run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
		if run.ModelCalls != 2 || run.ToolCalls != 1 {
			t.Fatalf("Run counters = %+v", run)
		}

		turn := receiveGatewayTurn(t, secondTurn)
		message := lastCrossToolMessage(t, turn, "cross-ohlcv")
		var result struct {
			OK     bool `json:"ok"`
			Result struct {
				Market   string      `json:"market"`
				Symbol   string      `json:"symbol"`
				Interval string      `json:"interval"`
				TimeZone string      `json:"time_zone"`
				Columns  []string    `json:"columns"`
				Rows     [][]float64 `json:"rows"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(message), &result); err != nil {
			t.Fatalf("decode OHLCV Tool Result %q: %v", message, err)
		}
		if !result.OK || result.Result.Market != "crypto" || result.Result.Symbol != normalizedSymbol ||
			result.Result.Interval != "1m" || result.Result.TimeZone != "UTC" || len(result.Result.Columns) != 6 ||
			result.Result.Columns[0] != "open_time_unix_ms" {
			t.Fatalf("OHLCV metadata/result = %+v", result)
		}
		rows := result.Result.Rows
		firstAt, _ := time.Parse(time.RFC3339, "2020-01-02T10:00:00Z")
		secondAt, _ := time.Parse(time.RFC3339, "2020-01-02T10:02:00Z")
		if len(rows) != 2 || rows[0][0] != float64(firstAt.UnixMilli()) || rows[0][4] != 10.5 ||
			rows[1][0] != float64(secondAt.UnixMilli()) || rows[1][4] != 12.5 {
			t.Fatalf("completed capped candle rows = %+v", rows)
		}
	})

	t.Run("Tool timeout cancels the source request", func(t *testing.T) {
		fixture := newRuntimeFixture()
		secondTurn := make(chan crossGatewayRequest, 1)
		var round atomic.Int32
		fixture.generate = func(w http.ResponseWriter, request *http.Request) {
			if round.Add(1) == 1 {
				writeToolCall(w, "cross-cancel", "get_ohlcv", map[string]any{
					"symbol": "CANCELUSDT", "interval": "1m", "start_time": "2020-01-02T10:00:00Z",
					"end_time": "2020-01-02T10:05:00Z", "limit": 1,
				})
				return
			}
			var body crossGatewayRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode timeout Gateway turn: %v", err)
			}
			secondTurn <- body
			writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("timeout accepted", "timeout accepted", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
		}
		runtime := openCrossServiceRuntime(t, backendURL, crossBackendUserToken, fixture, 500*time.Millisecond)
		session := createStoppedSession(t, runtime, map[string]any{"account_id": account.ID})
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
		if status != http.StatusAccepted {
			t.Fatalf("start status=%d body=%s", status, raw)
		}
		select {
		case <-sourceStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("actual Binance adapter never reached the cancellation fixture")
		}
		run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
		if run.ModelCalls != 2 || run.ToolCalls != 1 {
			t.Fatalf("timeout Run counters = %+v", run)
		}
		turn := receiveGatewayTurn(t, secondTurn)
		message := lastCrossToolMessage(t, turn, "cross-cancel")
		if !strings.Contains(message, `"ok":false`) || !strings.Contains(message, `"code":"TOOL_TIMEOUT"`) ||
			!strings.Contains(message, `"outcome":"unknown"`) {
			t.Fatalf("timeout Tool Result = %s", message)
		}
		select {
		case <-sourceCanceled:
		case <-time.After(2 * time.Second):
			t.Fatal("Agent cancellation did not reach the source request")
		}
	})

	if sourceCalls.Load() != 2 {
		t.Fatalf("source calls = %d, want one successful request and one canceled request", sourceCalls.Load())
	}
}

func TestTicket20CrossServiceMarketInfoPreservesFactsAndRejectsBadBackendAuth(t *testing.T) {
	const injectedSymbol = "btcusdt&limit=500&symbol=ethusdt"
	const normalizedSymbol = "BTCUSDT&LIMIT=500&SYMBOL=ETHUSDT"
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		sourceCalls.Add(1)
		query := request.URL.Query()
		if request.Method != http.MethodGet || request.URL.Path != "/api/v3/exchangeInfo" || len(query) != 1 ||
			len(query["symbol"]) != 1 || query.Get("symbol") != normalizedSymbol {
			t.Errorf("Binance Market Info request = %s %s query=%#v", request.Method, request.URL.RequestURI(), query)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeFixtureJSON(w, http.StatusOK, map[string]any{"symbols": []any{map[string]any{
			"symbol": normalizedSymbol, "status": "BREAK", "baseAsset": "BTC", "quoteAsset": "USDT",
			"orderTypes": []string{"LIMIT", "MARKET"}, "isSpotTradingAllowed": true, "isMarginTradingAllowed": false,
			"filters": []any{
				map[string]any{"filterType": "PRICE_FILTER", "minPrice": "0.01000000", "maxPrice": "1000000.00000000", "tickSize": "0.01000000"},
				map[string]any{"filterType": "LOT_SIZE", "minQty": "0.00001000", "maxQty": "9000.00000000", "stepSize": "0.00001000"},
			},
		}}})
	}))
	t.Cleanup(source.Close)

	backendURL := startCrossBackend(t, source.URL)
	account := createCrossAccount(t, backendURL)
	fixture := newRuntimeFixture()
	secondTurn := make(chan crossGatewayRequest, 1)
	var round atomic.Int32
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		if round.Add(1) == 1 {
			writeToolCall(w, "cross-info", "get_market_info", map[string]any{"market": "crypto", "symbol": injectedSymbol})
			return
		}
		var body crossGatewayRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Market Info Gateway turn: %v", err)
		}
		secondTurn <- body
		writeFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("market info accepted", "market info accepted", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
	}
	runtime := openCrossServiceRuntime(t, backendURL, crossBackendUserToken, fixture, 2*time.Second)
	session := createStoppedSession(t, runtime, map[string]any{"account_id": account.ID})
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", status, raw)
	}
	run := waitForRunStatus(t, runtime, session.ID, 1, "completed")
	if run.ModelCalls != 2 || run.ToolCalls != 1 {
		t.Fatalf("Run counters = %+v", run)
	}
	turn := receiveGatewayTurn(t, secondTurn)
	message := lastCrossToolMessage(t, turn, "cross-info")
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			Symbol              string            `json:"symbol"`
			Source              string            `json:"source"`
			Status              string            `json:"status"`
			BaseAsset           string            `json:"base_asset"`
			QuoteAsset          string            `json:"quote_asset"`
			OrderTypes          []string          `json:"order_types"`
			PriceFilter         map[string]string `json:"price_filter"`
			QuantityFilter      map[string]string `json:"quantity_filter"`
			MinNotional         json.RawMessage   `json:"min_notional"`
			SourceRulesEnforced bool              `json:"source_rules_enforced"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(message), &result); err != nil {
		t.Fatalf("decode Market Info Tool Result %q: %v", message, err)
	}
	rules := result.Result
	if !result.OK || rules.Symbol != normalizedSymbol || rules.Source != "binance" ||
		rules.Status != "BREAK" || rules.BaseAsset != "BTC" || rules.QuoteAsset != "USDT" ||
		len(rules.OrderTypes) != 2 || rules.OrderTypes[0] != "LIMIT" || rules.OrderTypes[1] != "MARKET" {
		t.Fatalf("Market Info source facts = %+v", result)
	}
	if rules.PriceFilter["min_price"] != "0.01000000" || rules.PriceFilter["max_price"] != "1000000.00000000" ||
		rules.PriceFilter["tick_size"] != "0.01000000" || rules.QuantityFilter["min_quantity"] != "0.00001000" ||
		rules.QuantityFilter["max_quantity"] != "9000.00000000" || rules.QuantityFilter["step_size"] != "0.00001000" ||
		string(rules.MinNotional) != "null" {
		t.Fatalf("Market Info decimal/null rules = %+v", rules)
	}
	if result.Result.SourceRulesEnforced {
		t.Fatalf("source rules unexpectedly enforced = %+v", result.Result)
	}

	badAuthFixture := newRuntimeFixture()
	badAuthRuntime := openCrossServiceRuntime(t, backendURL, "wrong-backend-user-token", badAuthFixture, time.Second)
	status, raw = runtimeRequest(t, badAuthRuntime.Handler(), http.MethodPost, "/api/v1/sessions", agentAdminToken, map[string]any{
		"name": "bad auth", "account_id": account.ID, "model_name": "fixture-model",
	})
	if status != http.StatusBadGateway || !bytes.Contains(raw, []byte(`"error":"BACKEND_AUTH_FAILED"`)) {
		t.Fatalf("bad Backend auth status=%d body=%s", status, raw)
	}
	if sourceCalls.Load() != 1 {
		t.Fatalf("authentication failure reached market source: calls=%d", sourceCalls.Load())
	}
}

func openCrossServiceRuntime(t *testing.T, backendURL, backendToken string, fixture *runtimeFixture, toolTimeout time.Duration) *common.AgentRuntime {
	t.Helper()
	gateway := httptest.NewServer(http.HandlerFunc(fixture.serveGateway))
	t.Cleanup(gateway.Close)
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: agentAdminToken,
		BackendURL: backendURL, BackendUserToken: backendToken, GatewayURL: gateway.URL, GatewayRuntimeToken: gatewayToken,
		DefaultPrompt: "cross-service mission", DefaultMaxLoop: 4, DefaultMaxToolCall: 3,
		RequestTimeout: 2 * time.Second, ToolTimeout: toolTimeout, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func createCrossAccount(t *testing.T, backendURL string) crossAccount {
	t.Helper()
	status, raw := crossHTTPRequest(t, http.MethodPost, backendURL+"/v1/accounts", crossBackendUserToken, map[string]any{
		"user_name": "ticket-19-20-cross-service", "initial_balance": 100000,
	})
	if status != http.StatusCreated {
		t.Fatalf("create actual Backend Account status=%d body=%s", status, raw)
	}
	var account crossAccount
	if err := json.Unmarshal(raw, &account); err != nil {
		t.Fatalf("decode actual Backend Account: %v", err)
	}
	return account
}

func crossHTTPRequest(t *testing.T, method, endpoint, token string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal cross-service request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		t.Fatalf("create cross-service request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("cross-service request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read cross-service response: %v", err)
	}
	return response.StatusCode, raw
}

func receiveGatewayTurn(t *testing.T, turns <-chan crossGatewayRequest) crossGatewayRequest {
	t.Helper()
	select {
	case turn := <-turns:
		return turn
	case <-time.After(2 * time.Second):
		t.Fatal("Gateway did not receive the Tool Result turn")
		return crossGatewayRequest{}
	}
}

func lastCrossToolMessage(t *testing.T, turn crossGatewayRequest, callID string) string {
	t.Helper()
	if len(turn.Messages) == 0 {
		t.Fatal("next Gateway turn has no messages")
	}
	message := turn.Messages[len(turn.Messages)-1]
	if message.Role != "tool" || message.ToolCallID != callID || message.Content == nil {
		t.Fatalf("correlated Tool Result message = %+v", message)
	}
	return *message.Content
}

func startCrossBackend(t *testing.T, sourceURL string) string {
	t.Helper()
	_, thisFile, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("locate cross-service test source")
	}
	backendRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "backend")
	binaryName := "market-cross-service-host"
	if goruntime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildContext, "go", "build", "-o", binaryPath, "./test/market_cross_service_host")
	build.Dir = backendRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual Backend test host: %v\n%s", err, output)
	}

	command := exec.Command(binaryPath)
	command.Dir = backendRoot
	command.Env = crossBackendEnvironment(map[string]string{
		"USER_TOKEN": crossBackendUserToken, "USER_NAME": "ticket-cross-service", "DB_DRIVER": "sqlite",
		"DB_DSN": filepath.Join(t.TempDir(), "backend.db"), "LOG_DIR": filepath.Join(t.TempDir(), "logs"),
		"PORT": "0", "LOG_MAX_LINES": "1000", "DEBUG": "false", "FEE_RATE_MAKER": "0.0002", "FEE_RATE_TAKER": "0.0004",
		"MARKET_FRESH_TTL": "100ms", "MARKET_IDLE_TIMEOUT": "1s", "MARKET_WAIT_TIMEOUT": "1s",
		"MATCHING_INTERVAL": "1s", "MARKET_CROSS_SERVICE_SOURCE_URL": sourceURL,
	})
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("Backend host stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("Backend host stdout: %v", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatalf("Backend host stderr: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start actual Backend test host: %v", err)
	}
	process := &crossBackendProcess{cmd: command, stdin: stdin, done: make(chan struct{})}
	var stderrBuffer crossProcessOutput
	go func() { _, _ = io.Copy(&stderrBuffer, stderr) }()
	go func() {
		err := command.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.done)
	}()
	t.Cleanup(func() {
		_ = process.stdin.Close()
		select {
		case <-process.done:
		case <-time.After(3 * time.Second):
			_ = process.cmd.Process.Kill()
			select {
			case <-process.done:
			case <-time.After(2 * time.Second):
				t.Errorf("Backend host process did not stop after kill")
			}
		}
		process.waitMu.Lock()
		waitErr := process.waitErr
		process.waitMu.Unlock()
		if waitErr != nil {
			t.Errorf("Backend host exit: %v\n%s", waitErr, stderrBuffer.String())
		}
	})

	readyLines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "READY ") {
				select {
				case readyLines <- strings.TrimSpace(strings.TrimPrefix(line, "READY ")):
				default:
				}
			}
		}
	}()
	var backendURL string
	select {
	case backendURL = <-readyLines:
	case <-process.done:
		t.Fatalf("Backend host exited before readiness: %v\n%s", process.waitError(), stderrBuffer.String())
	case <-time.After(5 * time.Second):
		t.Fatalf("Backend host readiness timed out\n%s", stderrBuffer.String())
	}
	waitForCrossBackendHealth(t, backendURL, process, &stderrBuffer)
	return backendURL
}

func (p *crossBackendProcess) waitError() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func waitForCrossBackendHealth(t *testing.T, backendURL string, process *crossBackendProcess, stderr *crossProcessOutput) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, backendURL+"/v1/health", nil)
		response, err := http.DefaultClient.Do(request)
		cancel()
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-process.done:
			t.Fatalf("Backend host exited during readiness: %v\n%s", process.waitError(), stderr.String())
		case <-deadline.C:
			t.Fatalf("Backend health readiness timed out\n%s", stderr.String())
		case <-ticker.C:
		}
	}
}

func crossBackendEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(name)]; !replaced {
			environment = append(environment, entry)
		}
	}
	for name, value := range overrides {
		environment = append(environment, fmt.Sprintf("%s=%s", name, value))
	}
	return environment
}
