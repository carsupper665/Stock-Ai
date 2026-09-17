package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGatewayProcessStartsWithConfiguredDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts the gateway process")
	}
	tempDir := t.TempDir()
	binary := buildGatewayProcessBinary(t)
	address := availableTCPAddress(t)
	dbPath := filepath.Join(tempDir, "gateway.db")
	command, logs := startGatewayProcess(t, binary, address, dbPath, "")
	stopped := false
	defer func() {
		if !stopped {
			stopGatewayProcess(command)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	if err := waitForGatewayReady(ctx, client, address); err != nil {
		stopGatewayProcess(command)
		stopped = true
		t.Fatalf("gateway did not become ready: %v\n%s", err, logs.String())
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("configured database was not created: %v", err)
	}
}

func TestGatewayProcessModelCatalogContract(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts the gateway process")
	}
	binary := buildGatewayProcessBinary(t)

	t.Run("configured catalog is public and drives Generate", func(t *testing.T) {
		upstreamRequests := make(chan map[string]any, 1)
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chat/completions" {
				t.Errorf("upstream path = %q", r.URL.Path)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode upstream request: %v", err)
			}
			upstreamRequests <- body
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"process answer"},"finish_reason":"stop"}]}`))
		}))
		defer provider.Close()

		tempDir := t.TempDir()
		address := availableTCPAddress(t)
		dbPath := filepath.Join(tempDir, "gateway.db")
		catalog := `[{"model_name":"process-reasoning","provider_id":"fixture","model":"actual-process-model","options":{"temperature":0},"levels":{"high":{"reasoning_effort":"high"}}}]`
		command, logs := startGatewayProcess(t, binary, address, dbPath, catalog)
		stopped := false
		defer func() {
			if !stopped {
				stopGatewayProcess(command)
			}
		}()

		client := &http.Client{Timeout: 500 * time.Millisecond}
		readyContext, cancelReady := context.WithTimeout(context.Background(), 5*time.Second)
		if err := waitForGatewayReady(readyContext, client, address); err != nil {
			cancelReady()
			stopGatewayProcess(command)
			stopped = true
			t.Fatalf("catalog Gateway did not become ready: %v\n%s", err, logs.String())
		}
		cancelReady()
		baseURL := "http://" + address

		providerBody, err := json.Marshal(map[string]any{
			"id": "fixture", "name": "Fixture", "type": "openai_compatible",
			"base_url": provider.URL, "default_model": "different-default", "enabled": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		status, body := processHTTPRequest(t, client, http.MethodPost, baseURL+"/v1/providers", adminToken, providerBody)
		if status != http.StatusCreated {
			t.Fatalf("process create Provider status = %d, body = %s", status, body)
		}

		status, body = processHTTPRequest(t, client, http.MethodGet, baseURL+"/v1/models", runtimeToken, nil)
		if status != http.StatusOK {
			t.Fatalf("process models status = %d, body = %s", status, body)
		}
		var models struct {
			Models []struct {
				Name       string                    `json:"model_name"`
				ProviderID string                    `json:"provider_id"`
				Model      string                    `json:"model"`
				Options    map[string]any            `json:"options"`
				Levels     map[string]map[string]any `json:"levels"`
				Enabled    bool                      `json:"enabled"`
				Available  bool                      `json:"available"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &models); err != nil {
			t.Fatalf("decode process models: %v; body = %s", err, body)
		}
		if len(models.Models) != 1 || models.Models[0].Name != "process-reasoning" ||
			models.Models[0].ProviderID != "fixture" || models.Models[0].Model != "actual-process-model" ||
			models.Models[0].Options["temperature"] != float64(0) ||
			models.Models[0].Levels["high"]["reasoning_effort"] != "high" ||
			!models.Models[0].Enabled || !models.Models[0].Available {
			t.Fatalf("process model catalog = %+v", models.Models)
		}

		generateBody := []byte(`{"provider":"fixture","model":"actual-process-model","messages":[{"role":"user","content":"hello"}],"options":{"model_level":"high"}}`)
		status, body = processHTTPRequest(t, client, http.MethodPost, baseURL+"/v1/generate", runtimeToken, generateBody)
		if status != http.StatusOK {
			t.Fatalf("process Generate status = %d, body = %s", status, body)
		}
		var generated map[string]any
		if err := json.Unmarshal(body, &generated); err != nil || generated["content"] != "process answer" {
			t.Fatalf("process Generate response = %s, decode error = %v", body, err)
		}
		select {
		case upstream := <-upstreamRequests:
			if upstream["model"] != "actual-process-model" || upstream["temperature"] != float64(0) ||
				upstream["reasoning_effort"] != "high" {
				t.Fatalf("process mapped upstream request = %+v", upstream)
			}
			if _, exists := upstream["model_level"]; exists {
				t.Fatalf("process leaked model_level upstream: %+v", upstream)
			}
		case <-time.After(time.Second):
			t.Fatal("process Generate did not reach Provider fixture")
		}
		if _, err := os.Stat(dbPath); err != nil {
			t.Fatalf("catalog process database was not created: %v", err)
		}
	})

	t.Run("invalid catalog exits before readiness", func(t *testing.T) {
		tempDir := t.TempDir()
		address := availableTCPAddress(t)
		dbPath := filepath.Join(tempDir, "must-not-open.db")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary)
		command.Env = gatewayProcessEnvironment(address, dbPath,
			`[{"model_name":"invalid","provider_id":"fixture","model":"actual","levels":{"low":null}}]`)
		output, err := command.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("invalid-catalog process did not exit: %v\n%s", ctx.Err(), output)
		}
		if err == nil {
			t.Fatalf("invalid-catalog process exited successfully: %s", output)
		}
		if !strings.Contains(string(output), "LLM_SERVER_MODEL_CATALOG") || strings.Contains(string(output), "ready: listening") {
			t.Fatalf("invalid-catalog startup logs = %q", output)
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Fatalf("invalid catalog opened database: stat error = %v", statErr)
		}

		requestContext, cancelRequest := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancelRequest()
		request, requestErr := http.NewRequestWithContext(requestContext, http.MethodGet, "http://"+address+"/healthz", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response, requestErr := (&http.Client{Timeout: 250 * time.Millisecond}).Do(request)
		if requestErr == nil {
			response.Body.Close()
			t.Fatalf("invalid-catalog process exposed readiness with status %d", response.StatusCode)
		}
	})
}

func buildGatewayProcessBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "llm-provider-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/llm-provider-server")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gateway: %v\n%s", err, output)
	}
	return binary
}

func availableTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func gatewayProcessEnvironment(address, dbPath, catalog string) []string {
	return append(os.Environ(),
		"LLM_SERVER_ADDR="+address,
		"LLM_SERVER_DB_PATH="+dbPath,
		"LLM_SERVER_ADMIN_TOKEN="+adminToken,
		"LLM_SERVER_RUNTIME_TOKEN="+runtimeToken,
		"LLM_SERVER_MASTER_KEY="+masterKey,
		"LLM_SERVER_MODEL_CATALOG="+catalog,
	)
}

func startGatewayProcess(t *testing.T, binary, address, dbPath, catalog string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	command := exec.Command(binary)
	prepareGatewayCommand(command)
	command.Env = gatewayProcessEnvironment(address, dbPath, catalog)
	logs := &bytes.Buffer{}
	command.Stdout = logs
	command.Stderr = logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command, logs
}

func stopGatewayProcess(command *exec.Cmd) {
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}

func waitForGatewayReady(ctx context.Context, client *http.Client, address string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	url := fmt.Sprintf("http://%s/healthz", address)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("health status = %d", response.StatusCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: last health request: %v", ctx.Err(), requestErr)
		case <-ticker.C:
		}
	}
}

func processHTTPRequest(t *testing.T, client *http.Client, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("process HTTP %s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read process HTTP %s %s: %v", method, url, err)
	}
	return response.StatusCode, responseBody
}
