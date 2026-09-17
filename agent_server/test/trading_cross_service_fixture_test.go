package test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

// settablePriceSource answers the Binance ticker endpoint the actual Backend polls, so
// tests can move the market that fills market orders, limit orders and stops.
type settablePriceSource struct {
	mu     sync.Mutex
	price  string
	server *httptest.Server
}

func newSettablePriceSource(t *testing.T, price string) *settablePriceSource {
	t.Helper()
	source := &settablePriceSource{price: price}
	source.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v3/ticker/price" {
			t.Errorf("unexpected source request %s", request.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		source.mu.Lock()
		current := source.price
		source.mu.Unlock()
		writeFixtureJSON(w, http.StatusOK, map[string]any{"symbol": request.URL.Query().Get("symbol"), "price": current})
	}))
	t.Cleanup(source.server.Close)
	return source
}

func (s *settablePriceSource) set(price string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.price = price
}

// startTradingBackend builds and runs backend/test/market_cross_service_host, which is the
// market host plus the matching engine, and returns its base URL.
func startTradingBackend(t *testing.T, sourceURL string) string {
	t.Helper()
	_, thisFile, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("locate trading cross-service test source")
	}
	backendRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "backend")
	binaryPath := filepath.Join(t.TempDir(), "trading-cross-service-host")
	if goruntime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildContext, "go", "build", "-o", binaryPath, "./test/market_cross_service_host")
	build.Dir = backendRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build trading Backend test host: %v\n%s", err, output)
	}
	command := exec.Command(binaryPath)
	command.Dir = backendRoot
	command.Env = crossBackendEnvironment(map[string]string{
		"USER_TOKEN": crossBackendUserToken, "USER_NAME": "ticket-trading", "DB_DRIVER": "sqlite",
		"DB_DSN": filepath.Join(t.TempDir(), "backend.db"), "LOG_DIR": filepath.Join(t.TempDir(), "logs"),
		"PORT": "0", "LOG_MAX_LINES": "1000", "DEBUG": "false", "FEE_RATE_MAKER": "0.0002", "FEE_RATE_TAKER": "0.0004",
		"MARKET_FRESH_TTL": "100ms", "MARKET_IDLE_TIMEOUT": "1s", "MARKET_WAIT_TIMEOUT": "1s",
		"MATCHING_INTERVAL": "50ms", "MARKET_CROSS_SERVICE_SOURCE_URL": sourceURL,
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
		t.Fatalf("start trading Backend test host: %v", err)
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
			<-process.done
		}
		if err := process.waitError(); err != nil {
			t.Errorf("Backend host exit: %v\n%s", err, stderrBuffer.String())
		}
	})
	readyLines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "READY ") {
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

// waitForBackend polls an authenticated Backend GET until check accepts the decoded body.
func waitForBackend(t *testing.T, endpoint, token, what string, check func(body []byte) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		status, raw := crossHTTPRequest(t, http.MethodGet, endpoint, token, nil)
		last = raw
		if status == http.StatusOK && check(raw) {
			return raw
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never observed; last body = %s", what, last)
	return nil
}

func backendLedger(t *testing.T, backendURL, token, query string) []ledgerEntryResult {
	t.Helper()
	status, raw := crossHTTPRequest(t, http.MethodGet, backendURL+"/v1/ledger"+query, token, nil)
	if status != http.StatusOK {
		t.Fatalf("Backend ledger status=%d body=%s", status, raw)
	}
	var body struct {
		Entries []ledgerEntryResult `json:"entries"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode Backend ledger: %v", err)
	}
	return body.Entries
}

type ledgerEntryResult struct {
	Seq          int64          `json:"seq"`
	Event        string         `json:"event"`
	OrderID      string         `json:"order_id"`
	PositionID   string         `json:"position_id"`
	Trigger      string         `json:"trigger"`
	Quantity     float64        `json:"quantity"`
	Price        float64        `json:"price"`
	Fee          float64        `json:"fee"`
	RealizedPnL  float64        `json:"realized_pnl"`
	BalanceDelta float64        `json:"balance_delta"`
	BalanceAfter float64        `json:"balance_after"`
	StopLoss     float64        `json:"stop_loss"`
	TakeProfit   float64        `json:"take_profit"`
	Source       *backendSource `json:"source"`
}

// openTradingCrossRuntime opens an Agent runtime on dbPath against the actual Backend so a
// test can close it and reopen the same persisted Sessions, as a Server restart would.
func openTradingCrossRuntime(t *testing.T, backendURL string, gateway *scriptedGateway, dbPath string) *common.AgentRuntime {
	t.Helper()
	fixture := newRuntimeFixture()
	fixture.generate = gateway.generate
	gatewayServer := httptest.NewServer(http.HandlerFunc(fixture.serveGateway))
	t.Cleanup(gatewayServer.Close)
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: dbPath, AdminToken: agentAdminToken,
		BackendURL: backendURL, BackendUserToken: crossBackendUserToken, GatewayURL: gatewayServer.URL, GatewayRuntimeToken: gatewayToken,
		DefaultPrompt: "trading mission", DefaultMaxLoop: 8, DefaultMaxToolCall: 4,
		RequestTimeout: 2 * time.Second, ToolTimeout: 2 * time.Second, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}
