package test

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type drainingRuntimeHandler struct {
	runtime *common.AgentRuntime
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *drainingRuntimeHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/api/slow" {
		h.runtime.ServeHTTP(w, request)
		return
	}
	h.once.Do(func() { close(h.started) })
	<-h.release
	w.WriteHeader(http.StatusNoContent)
}

func (h *drainingRuntimeHandler) HealthError() error { return h.runtime.HealthError() }

func TestSPEC05ServerQuiescesRuntimeBeforeWaitingForSlowHTTP(t *testing.T) {
	modelStarted := make(chan struct{})
	modelCanceled := make(chan struct{})
	modelRelease := make(chan struct{})
	defer close(modelRelease)
	var modelOnce sync.Once
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		modelOnce.Do(func() { close(modelStarted) })
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-request.Context().Done():
			close(modelCanceled)
		case <-modelRelease:
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start Run = %d %s", status, raw)
	}
	select {
	case <-modelStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("active Run did not reach the model")
	}

	application := &drainingRuntimeHandler{
		runtime: runtime, started: make(chan struct{}), release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-application.release:
		default:
			close(application.release)
		}
	}()
	server, err := common.NewServer(common.NewEventLoopWithClock(newManualClock()), nil,
		[]io.Closer{runtime}, nil, 2*time.Second, application)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := server.Start(listener); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	httpDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String() + "/api/slow")
		if requestErr == nil {
			requestErr = response.Body.Close()
		}
		httpDone <- requestErr
	}()
	select {
	case <-application.started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow HTTP request did not enter the handler")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown() }()
	deadline := time.Now().Add(time.Second)
	for {
		status, raw = runtimeRequest(t, runtime.Handler(), http.MethodGet,
			"/api/v1/sessions/"+session.ID+"/runs/1", agentAdminToken, nil)
		if status == http.StatusOK && strings.Contains(string(raw), `"status":"interrupted"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Runtime did not interrupt its active Run while HTTP drain was blocked: %d %s", status, raw)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-modelCanceled:
	case <-time.After(time.Second):
		t.Fatal("Runtime worker was not canceled while HTTP drain was blocked")
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
		t.Fatalf("mutation after shutdown admission closed = %d %s", status, raw)
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before slow HTTP request drained: %v", err)
	default:
	}

	close(application.release)
	if err := <-httpDone; err != nil {
		t.Fatalf("slow HTTP request error = %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	closeResults := make(chan error, 2)
	go func() { closeResults <- runtime.Close() }()
	go func() { closeResults <- runtime.Close() }()
	for range 2 {
		select {
		case err := <-closeResults:
			if err != nil {
				t.Fatalf("idempotent Runtime Close() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("idempotent Runtime Close() deadlocked")
		}
	}
}

func TestSPEC06ProcessRejectsBadStartupBeforeRuntimeRecoveryIO(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agent-server"+processExecutableSuffix())
	build := exec.Command("go", "build", "-o", binary, "../cmd/agent-server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent server: %v\n%s", err, output)
	}

	tests := []struct {
		name      string
		configure func(*testing.T, map[string]string) func()
	}{
		{
			name: "bad event interval",
			configure: func(_ *testing.T, environment map[string]string) func() {
				environment["AGENT_SERVER_ADDR"] = "127.0.0.1:0"
				environment["AGENT_SERVER_EVENT_SCAN_INTERVAL"] = "0"
				return func() {}
			},
		},
		{
			name: "occupied listener",
			configure: func(t *testing.T, environment map[string]string) func() {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("reserve occupied listener: %v", err)
				}
				environment["AGENT_SERVER_ADDR"] = listener.Addr().String()
				return func() { _ = listener.Close() }
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			temp := t.TempDir()
			databasePath := filepath.Join(temp, "must-not-open.db")
			historyPath := filepath.Join(temp, "must-not-create-history")
			environment := map[string]string{
				"AGENT_SERVER_DB_PATH":               databasePath,
				"AGENT_SERVER_HISTORY_DIR":           historyPath,
				"AGENT_SERVER_ADMIN_TOKEN":           "process-agent-admin",
				"AGENT_SERVER_BACKEND_URL":           "http://127.0.0.1:1",
				"AGENT_SERVER_BACKEND_USER_TOKEN":    "process-backend-user",
				"AGENT_SERVER_GATEWAY_URL":           "http://127.0.0.1:1",
				"AGENT_SERVER_GATEWAY_RUNTIME_TOKEN": "process-gateway-runtime",
				"AGENT_SERVER_DEFAULT_MAX_LOOP":      "20",
				"AGENT_SERVER_DEFAULT_MAX_TOOL_CALL": "40",
				"AGENT_SERVER_REQUEST_TIMEOUT":       "30s",
				"AGENT_SERVER_TOOL_TIMEOUT":          "10s",
				"AGENT_SERVER_STOP_TIMEOUT":          "5s",
				"AGENT_SERVER_EVENT_SCAN_INTERVAL":   "1s",
			}
			cleanup := testCase.configure(t, environment)
			defer cleanup()

			cmd := exec.Command(binary)
			cmd.Env = processEnvironment(environment)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("bad startup exited successfully: %s", output)
			}
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() == 0 {
				t.Fatalf("bad startup error = %v, output = %s", err, output)
			}
			if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bad startup opened Runtime database: %v", err)
			}
			if _, err := os.Stat(historyPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bad startup created Runtime history: %v", err)
			}
		})
	}
}

func processEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; !replaced {
			environment = append(environment, entry)
		}
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func TestSPEC08PricePercentageOverflowKeepsEventAndFiniteBoundaryFiresWithValidJSON(t *testing.T) {
	t.Run("overflow does not consume or poison", func(t *testing.T) {
		fixture := newRuntimeFixture()
		clock := newEventClock()
		book := newPriceBook()
		script := newLLMScript(
			respondToolCalls(changeEventCall("overflow", "BTCUSDT", math.SmallestNonzeroFloat64, "up", 1)),
			respondStop("armed"),
			respondStop("must not wake"),
		)
		fixture.generate = script.handler(t)
		runtime := openEventRuntime(t, fixture, book, "")
		tasks := newEventTasks(runtime, clock)
		session, events := armPriceEvents(t, script, runtime, nil)

		clock.Advance(time.Second)
		book.set("crypto", "BTCUSDT", math.MaxFloat64, clock.Now())
		if err := tasks.price.Run(context.Background()); err != nil {
			t.Fatalf("price round error = %v", err)
		}
		script.expectNoRequest(t, "wake-up with non-finite percentage facts")
		remaining := listEventsHTTP(t, runtime, session.ID)
		if len(remaining) != 1 || remaining[0].ID != events[0].ID {
			t.Fatalf("overflowing percentage silently consumed Event: %+v", remaining)
		}
		if err := runtime.HealthError(); err != nil {
			t.Fatalf("numeric overflow poisoned Runtime health: %v", err)
		}
		if ids := startedRunIDs(t, runtime, session.ID); len(ids) != 1 {
			t.Fatalf("numeric overflow created a Run with corrupt or empty triggers: %v", ids)
		}
	})

	t.Run("largest finite prices produce valid trigger JSON", func(t *testing.T) {
		fixture := newRuntimeFixture()
		clock := newEventClock()
		book := newPriceBook()
		basePrice := math.MaxFloat64 / 2
		script := newLLMScript(
			respondToolCalls(changeEventCall("finite", "BTCUSDT", basePrice, "up", 99)),
			respondStop("armed"),
			respondStop("handled"),
		)
		fixture.generate = script.handler(t)
		runtime := openEventRuntime(t, fixture, book, "")
		tasks := newEventTasks(runtime, clock)
		session, _ := armPriceEvents(t, script, runtime, nil)

		clock.Advance(time.Second)
		book.set("crypto", "BTCUSDT", math.MaxFloat64, clock.Now())
		if err := tasks.price.Run(context.Background()); err != nil {
			t.Fatalf("price round error = %v", err)
		}
		triggers := contextTriggers(t, script.next(t, "finite boundary wake-up"))
		if len(triggers) != 1 {
			t.Fatalf("finite boundary triggers = %v", triggers)
		}
		matched, _ := triggers[0]["matched"].(map[string]any)
		change, _ := matched["change_pct"].(float64)
		if math.IsNaN(change) || math.IsInf(change, 0) || change < 99 {
			t.Fatalf("change_pct = %v, want finite matched percentage", matched["change_pct"])
		}
		waitForRunStatus(t, runtime, session.ID, 2, "completed")
	})
}
