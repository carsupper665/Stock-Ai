package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTicket36ProcessCrashAfterBackendCommitDoesNotReplayOrder(t *testing.T) {
	var mu sync.Mutex
	orderCalls := 0
	ledgerCalls := 0
	orderCommitted := make(chan struct{})
	orderResponse := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/accounts/"):
			if request.Header.Get("Authorization") != "Bearer "+backendUserToken {
				writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
				return
			}
			id := strings.TrimPrefix(request.URL.Path, "/v1/accounts/")
			writeFixtureJSON(w, http.StatusOK, map[string]any{"id": id, "status": "active", "token": "process-account-token"})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/orders":
			if request.Header.Get("Authorization") != "Bearer process-account-token" {
				writeFixtureJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
				return
			}
			mu.Lock()
			orderCalls++
			if orderCalls == 1 {
				close(orderCommitted)
			}
			mu.Unlock()
			<-orderResponse
			writeFixtureJSON(w, http.StatusOK, map[string]any{"id": "ord_committed", "status": "filled"})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/ledger":
			mu.Lock()
			ledgerCalls++
			mu.Unlock()
			writeFixtureJSON(w, http.StatusOK, map[string]any{"entries": []any{map[string]any{
				"seq": 1, "event": "order_filled", "order_id": "ord_committed",
				"source": map[string]any{"session_id": request.URL.Query().Get("session_id"), "run_id": 1},
			}}})
		default:
			writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		}
	}))
	defer backend.Close()

	var gatewayMu sync.Mutex
	gatewayCalls := 0
	recoveryContext := make(chan string, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/v1/models" {
			writeFixtureJSON(w, http.StatusOK, map[string]any{"models": []any{map[string]any{
				"model_name": "fixture-model", "provider_id": "fixture", "model": "provider-model",
				"levels": map[string]any{}, "enabled": true, "available": true,
			}}})
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/v1/generate" {
			writeFixtureJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "NOT_FOUND"}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode process Generate: %v", err)
			return
		}
		gatewayMu.Lock()
		gatewayCalls++
		call := gatewayCalls
		gatewayMu.Unlock()
		switch call {
		case 1:
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": nil, "finish_reason": "tool_call", "usage": nil,
				"tool_calls": []any{map[string]any{"id": "place-once", "name": "place_order", "arguments": map[string]any{
					"symbol": "BTCUSDT", "side": "buy", "quantity": 0.01,
				}}},
			})
		case 2:
			recoveryContext <- contextText(body)
			writeFixtureJSON(w, http.StatusOK, map[string]any{
				"content": nil, "finish_reason": "tool_call", "usage": nil,
				"tool_calls": []any{map[string]any{"id": "audit-ledger", "name": "get_ledger", "arguments": map[string]any{"run_id": 1}}},
			})
		default:
			writeFixtureJSON(w, http.StatusOK, finalResponse("recovered after ledger audit", "recovered", nil, nil))
		}
	}))
	defer gateway.Close()

	temp := t.TempDir()
	binary := filepath.Join(temp, "agent-server"+processExecutableSuffix())
	build := exec.Command("go", "build", "-o", binary, "../cmd/agent-server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Agent Server: %v\n%s", err, output)
	}
	databasePath := filepath.Join(temp, "agent.db")
	historyPath := filepath.Join(temp, "history")
	process := startTicket36Process(t, binary, databasePath, historyPath, backend.URL, gateway.URL)
	sessionBody := map[string]any{"name": "crash fixture", "account_id": testAccountID, "model_name": "fixture-model"}
	status, raw := ticket36ProcessRequest(t, process.baseURL, http.MethodPost, "/api/v1/sessions", sessionBody)
	if status != http.StatusCreated {
		t.Fatalf("create process Session = %d %s", status, raw)
	}
	session := decodeBody[struct {
		ID string `json:"id"`
	}](t, raw)
	status, raw = ticket36ProcessRequest(t, process.baseURL, http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", nil)
	if status != http.StatusAccepted {
		t.Fatalf("start process Run = %d %s", status, raw)
	}
	select {
	case <-orderCommitted:
	case <-time.After(5 * time.Second):
		t.Fatal("Backend order did not reach the committed-before-response boundary")
	}
	process.killAndWait(t)
	close(orderResponse)

	restarted := startTicket36Process(t, binary, databasePath, historyPath, backend.URL, gateway.URL)
	defer restarted.killAndWait(t)
	select {
	case context := <-recoveryContext:
		if !containsAll(context, `"trigger":"server_recovery"`, `"previous_run_id":1`, `"place_order"`, `"status":"unknown"`) {
			t.Fatalf("process recovery context = %s", context)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restarted process did not begin server_recovery")
	}
	waitTicket36ProcessRun(t, restarted.baseURL, session.ID, 1, "interrupted")
	waitTicket36ProcessRun(t, restarted.baseURL, session.ID, 2, "completed")
	mu.Lock()
	orders, ledgers := orderCalls, ledgerCalls
	mu.Unlock()
	if orders != 1 || ledgers != 1 {
		t.Fatalf("Backend calls after crash = orders %d ledger %d, want 1/1", orders, ledgers)
	}
}

type ticket36Process struct {
	cmd     *exec.Cmd
	baseURL string
	wait    chan error
	stopped bool
}

func startTicket36Process(t *testing.T, binary, databasePath, historyPath, backendURL, gatewayURL string) *ticket36Process {
	t.Helper()
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENT_SERVER_ADDR=127.0.0.1:0",
		"AGENT_SERVER_DB_PATH="+databasePath,
		"AGENT_SERVER_HISTORY_DIR="+historyPath,
		"AGENT_SERVER_ADMIN_TOKEN="+agentAdminToken,
		"AGENT_SERVER_BACKEND_URL="+backendURL,
		"AGENT_SERVER_BACKEND_USER_TOKEN="+backendUserToken,
		"AGENT_SERVER_GATEWAY_URL="+gatewayURL,
		"AGENT_SERVER_GATEWAY_RUNTIME_TOKEN="+gatewayToken,
		"AGENT_SERVER_REQUEST_TIMEOUT=30s",
		"AGENT_SERVER_TOOL_TIMEOUT=30s",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Agent Server stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Agent Server: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if index := strings.Index(line, "ready: listening on "); index >= 0 {
				ready <- strings.TrimSpace(line[index+len("ready: listening on "):])
			}
		}
	}()
	process := &ticket36Process{cmd: cmd, wait: wait}
	t.Cleanup(func() { process.killAndWait(t) })
	select {
	case address := <-ready:
		process.baseURL = "http://" + address
		return process
	case err := <-wait:
		process.stopped = true
		t.Fatalf("Agent Server exited before ready: %v; stderr=%s", err, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("Agent Server readiness timed out; stderr=%s", stderr.String())
	}
	return nil
}

func (p *ticket36Process) killAndWait(t *testing.T) {
	t.Helper()
	if p == nil || p.stopped {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.wait:
		p.stopped = true
	case <-time.After(5 * time.Second):
		t.Fatal("Agent Server process did not exit after hard kill")
	}
}

func ticket36ProcessRequest(t *testing.T, baseURL, method, path string, body any) (int, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode process request: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, baseURL+path, payload)
	if err != nil {
		t.Fatalf("create process request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("process request %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read process response: %v", err)
	}
	return response.StatusCode, raw
}

func waitTicket36ProcessRun(t *testing.T, baseURL, sessionID string, runID int64, wanted string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		status, raw := ticket36ProcessRequest(t, baseURL, http.MethodGet,
			fmt.Sprintf("/api/v1/sessions/%s/runs/%d", sessionID, runID), nil)
		if status == http.StatusOK {
			var run struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(raw, &run) == nil && run.Status == wanted {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("process Run %d did not become %s; last = %d %s", runID, wanted, status, raw)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
