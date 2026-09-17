package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type shutdownHarnessState struct {
	CLIPID           int    `json:"cli_pid"`
	DescendantPID    int    `json:"descendant_pid"`
	WorkingDirectory string `json:"working_directory"`
}

func TestGatewayShutdownCancelsHarnessAndRemovesTemporaryLogin(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and signals a test-owned Gateway process")
	}
	if !gatewaySignalTestSupported() {
		t.Skip("process signaling is unavailable on this platform")
	}
	binary := buildGatewayProcessBinary(t)
	directory := t.TempDir()
	authFile := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(authFile, []byte(`{"fixture_login":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	harnessExecutable := copyHarnessFixture(t, "codex-fixture-local-shutdown.exe")
	address := availableTCPAddress(t)
	dbPath := filepath.Join(directory, "gateway.db")
	statePath := filepath.Join(directory, "shutdown-state.json")
	command := exec.Command(binary)
	prepareGatewayCommand(command)
	command.Env = append(gatewayProcessEnvironment(address, dbPath, ""),
		"LLM_SERVER_CODEX_EXECUTABLE="+harnessExecutable,
		"LLM_SERVER_CODEX_AUTH_FILE="+authFile,
		"LLM_SERVER_PROVIDER_TIMEOUT=1m",
	)
	logs := &bytes.Buffer{}
	command.Stdout = logs
	command.Stderr = logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	processResult := make(chan error, 1)
	go func() { processResult <- command.Wait() }()
	stopped := false
	var state shutdownHarnessState
	defer func() {
		if !stopped {
			_ = command.Process.Kill()
			<-processResult
		}
		for _, pid := range []int{state.CLIPID, state.DescendantPID} {
			if pid == 0 {
				continue
			}
			if alive, _ := processIsAlive(pid); alive {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
				}
			}
		}
		if state.WorkingDirectory != "" {
			_ = os.RemoveAll(state.WorkingDirectory)
		}
	}()

	client := &http.Client{Timeout: 20 * time.Second}
	readyContext, cancelReady := context.WithTimeout(context.Background(), 5*time.Second)
	if err := waitForGatewayReady(readyContext, client, address); err != nil {
		cancelReady()
		t.Fatalf("Gateway did not become ready: %v\n%s", err, logs)
	}
	cancelReady()
	baseURL := "http://" + address
	providerBody := []byte(`{"id":"cli","name":"Codex","type":"codex","base_url":"","default_model":"cli-default","config":{"auth_mode":"local_login"}}`)
	if status, body := processHTTPRequest(t, client, http.MethodPost, baseURL+"/v1/providers", adminToken, providerBody); status != http.StatusCreated {
		t.Fatalf("create shutdown Provider = %d %s", status, body)
	}

	requestResult := make(chan error, 1)
	go func() {
		body, _ := json.Marshal(map[string]any{
			"provider": "cli", "messages": []any{map[string]string{"role": "user", "content": statePath}},
		})
		request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/generate", bytes.NewReader(body))
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+runtimeToken)
			request.Header.Set("Content-Type", "application/json")
			var response *http.Response
			response, err = client.Do(request)
			if response != nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
		}
		requestResult <- err
	}()
	state = waitForShutdownHarnessState(t, statePath)
	for _, pid := range []int{state.CLIPID, state.DescendantPID} {
		if alive, err := processIsAlive(pid); err != nil || !alive {
			t.Fatalf("fixture process %d was not alive before Gateway shutdown: alive=%t error=%v", pid, alive, err)
		}
	}
	if _, err := os.Stat(filepath.Join(state.WorkingDirectory, "auth.json")); err != nil {
		t.Fatalf("temporary login copy was unavailable before shutdown: %v", err)
	}

	if err := signalGatewayProcess(command); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-processResult:
		stopped = true
		if err != nil {
			t.Fatalf("Gateway shutdown failed: %v\n%s", err, logs)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("Gateway did not cancel and join the Harness during shutdown\n%s", logs)
	}
	select {
	case <-requestResult:
	case <-time.After(time.Second):
		t.Fatal("Generate client did not return after Gateway shutdown")
	}
	for _, pid := range []int{state.CLIPID, state.DescendantPID} {
		waitForProcessExit(t, pid)
	}
	if _, err := os.Stat(state.WorkingDirectory); !os.IsNotExist(err) {
		t.Fatalf("temporary Harness directory remains after successful shutdown: %s (stat error %v)", state.WorkingDirectory, err)
	}
}

func waitForShutdownHarnessState(t *testing.T, path string) shutdownHarnessState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			var state shutdownHarnessState
			if json.Unmarshal(body, &state) == nil && state.CLIPID != 0 && state.DescendantPID != 0 && state.WorkingDirectory != "" {
				return state
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Harness did not publish shutdown state at %s", path)
	return shutdownHarnessState{}
}
