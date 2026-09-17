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
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestTicket26ActualBackendPublishesWithBoundAccountAndReadsRequesterIdentity(t *testing.T) {
	backendURL := startTicket26Backend(t)
	bound := createTicket26Account(t, backendURL, "Bound Message Agent")
	other := createTicket26Account(t, backendURL, "Other Message Agent")
	posted := make([]ticket26MessageView, 0, 35)
	for index := 0; index < 35; index++ {
		token := messageBackendToken
		switch index % 3 {
		case 1:
			token = other.Token
		case 2:
			token = bound.Token
		}
		raw := ticket26BackendRequest(t, http.MethodPost, backendURL+"/v1/messages", token, map[string]any{
			"content": fmt.Sprintf("seed %02d", index),
		}, http.StatusCreated)
		var message ticket26MessageView
		if err := json.Unmarshal(raw, &message); err != nil || message.ID == "" || message.CreatedAt == "" {
			t.Fatalf("decode seeded message %d: %+v (%v)", index, message, err)
		}
		posted = append(posted, message)
	}

	fixture := newMessageToolFixture()
	boundPosted := make(chan string, 1)
	fixture.generate = func(w http.ResponseWriter, request *http.Request, call int) {
		switch call {
		case 1:
			messageToolCall(w, "cross-post", "post_message", map[string]any{"content": "from bound account"})
		case 2:
			post := messageToolResult(t, request, "cross-post")
			postResult, _ := post["result"].(map[string]any)
			postedID, _ := postResult["id"].(string)
			if post["ok"] != true || postedID == "" || postResult["status"] != "posted" || postResult["content"] != nil {
				t.Errorf("actual Backend post result = %#v", post)
			}
			boundPosted <- postedID
			messageToolCall(w, "cross-default-desc", "get_messages", map[string]any{})
		case 3:
			latest := <-boundPosted
			boundPosted <- latest
			want := []ticket26ExpectedMessage{{ID: latest, UserName: "you"}}
			for index := 34; index >= 26; index-- {
				want = append(want, ticket26ExpectedMessage{ID: posted[index].ID, UserName: ticket26SeedDisplayName(index)})
			}
			ticket26AssertMessageList(t, messageToolResult(t, request, "cross-default-desc"), want)
			messageToolCall(w, "cross-page-2", "get_messages", map[string]any{"page": 2})
		case 4:
			want := make([]ticket26ExpectedMessage, 0, 10)
			for index := 25; index >= 16; index-- {
				want = append(want, ticket26ExpectedMessage{ID: posted[index].ID, UserName: ticket26SeedDisplayName(index)})
			}
			ticket26AssertMessageList(t, messageToolResult(t, request, "cross-page-2"), want)
			messageToolCall(w, "cross-first-asc", "get_messages", map[string]any{"sort": "asc"})
		default:
			want := make([]ticket26ExpectedMessage, 0, 10)
			for index := 0; index < 10; index++ {
				want = append(want, ticket26ExpectedMessage{ID: posted[index].ID, UserName: ticket26SeedDisplayName(index)})
			}
			ticket26AssertMessageList(t, messageToolResult(t, request, "cross-first-asc"), want)
			messageFixtureJSON(w, http.StatusOK, map[string]any{"content": finalizationContent("cross-service message pages verified", "cross-service message pages verified", nil, nil), "tool_calls": []any{}, "finish_reason": "stop"})
		}
	}
	gateway := httptest.NewServer(http.HandlerFunc(fixture.serveGateway))
	t.Cleanup(gateway.Close)
	runtime, err := common.OpenAgentRuntime(common.RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "agent.db"), AdminToken: messageAgentToken,
		BackendURL: backendURL, BackendUserToken: messageBackendToken,
		GatewayURL: gateway.URL, GatewayRuntimeToken: messageGatewayToken,
		DefaultPrompt: "actual message Backend", DefaultMaxLoop: 10, DefaultMaxToolCall: 10,
		RequestTimeout: 2 * time.Second, ToolTimeout: 2 * time.Second, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenAgentRuntime() against actual Backend: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	status, raw := messageRuntimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions", map[string]any{
		"name": "actual Backend message session", "account_id": bound.ID, "model_name": "message-model",
	})
	if status != http.StatusCreated {
		t.Fatalf("create actual Backend Session status=%d body=%s", status, raw)
	}
	var session common.Session
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatalf("decode actual Backend Session: %v", err)
	}
	resetRaw := ticket26BackendRequest(t, http.MethodPost, backendURL+"/v1/accounts/"+bound.ID+"/token/reset", messageBackendToken, nil, http.StatusOK)
	var reset ticket26Account
	if err := json.Unmarshal(resetRaw, &reset); err != nil || reset.Token == "" || reset.Token == bound.Token {
		t.Fatalf("actual Backend Token reset = %+v (%v)", reset, err)
	}
	ticket26BackendRequest(t, http.MethodPost, backendURL+"/v1/messages", bound.Token, map[string]any{"content": "stale token must fail"}, http.StatusUnauthorized)
	status, raw = messageRuntimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", nil)
	if status != http.StatusAccepted {
		t.Fatalf("start actual Backend Session status=%d body=%s", status, raw)
	}
	run := waitForMessageRun(t, runtime, session.ID)
	if run.Status != "completed" || run.ToolCalls != 4 || run.ModelCalls != 5 {
		t.Fatalf("actual Backend Run = %+v", run)
	}
}

type ticket26MessageView struct {
	ID        string `json:"id"`
	UserName  string `json:"user_name"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type ticket26ExpectedMessage struct {
	ID       string
	UserName string
}

func ticket26DecodeMessageResult(t *testing.T, result map[string]any) ticket26MessageView {
	t.Helper()
	raw, err := json.Marshal(result["result"])
	if err != nil {
		t.Fatalf("encode message Tool Result: %v", err)
	}
	var message ticket26MessageView
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("decode message Tool Result: %v", err)
	}
	return message
}

func ticket26AssertMessageList(t *testing.T, result map[string]any, want []ticket26ExpectedMessage) {
	t.Helper()
	raw, err := json.Marshal(result["result"])
	if err != nil {
		t.Fatalf("encode message-list Tool Result: %v", err)
	}
	var envelope struct {
		Messages []ticket26MessageView `json:"messages"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode message-list Tool Result: %v", err)
	}
	if len(envelope.Messages) != len(want) {
		t.Fatalf("message-list length=%d, want %d: %s", len(envelope.Messages), len(want), raw)
	}
	for index, expected := range want {
		message := envelope.Messages[index]
		if message.ID != expected.ID || message.UserName != expected.UserName {
			t.Errorf("message %d=%+v, want id=%s user_name=%s", index, message, expected.ID, expected.UserName)
		}
	}
	if strings.Contains(string(raw), "author_id") || strings.Contains(string(raw), "author_type") || strings.Contains(string(raw), "token") {
		t.Errorf("message-list Tool Result leaked private identity: %s", raw)
	}
}

func ticket26SeedDisplayName(index int) string {
	switch index % 3 {
	case 0:
		return "Ticket 26 User"
	case 1:
		return "Other Message Agent"
	default:
		return "you"
	}
}

type ticket26Account struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

func createTicket26Account(t *testing.T, backendURL, name string) ticket26Account {
	t.Helper()
	body := ticket26BackendRequest(t, http.MethodPost, backendURL+"/v1/accounts", messageBackendToken, map[string]any{
		"user_name": name, "initial_balance": 1000,
	}, http.StatusCreated)
	var account ticket26Account
	if err := json.Unmarshal(body, &account); err != nil || account.ID == "" || account.Token == "" {
		t.Fatalf("decode actual Backend Account %s: %+v (%v)", body, account, err)
	}
	return account
}

func ticket26BackendRequest(t *testing.T, method, endpoint, token string, body any, wantStatus int) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal actual Backend request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		t.Fatalf("create actual Backend request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("actual Backend request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read actual Backend response: %v", err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("actual Backend status=%d body=%s, want %d", response.StatusCode, raw, wantStatus)
	}
	return raw
}

func startTicket26Backend(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve actual Backend address: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	_, thisFile, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("locate Ticket 26 test source")
	}
	backendRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "backend")
	binaryName := "ticket26-backend"
	if goruntime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildContext, "go", "build", "-o", binaryPath, ".")
	build.Dir = backendRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual Backend: %v\n%s", err, output)
	}

	command := exec.Command(binaryPath)
	command.Dir = backendRoot
	command.Env = ticket26Environment(map[string]string{
		"USER_TOKEN": messageBackendToken, "USER_NAME": "Ticket 26 User", "PORT": fmt.Sprint(port),
		"DB_DRIVER": "sqlite", "DB_DSN": filepath.Join(t.TempDir(), "backend.db"), "LOG_DIR": filepath.Join(t.TempDir(), "logs"),
		"LOG_MAX_LINES": "1000", "DEBUG": "false", "MATCHING_INTERVAL": "1s",
	})
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("start actual Backend: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("actual Backend process did not exit")
		}
	})

	backendURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequest(http.MethodGet, backendURL+"/v1/health", nil)
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return backendURL
			}
		}
		select {
		case err := <-done:
			t.Fatalf("actual Backend exited before readiness: %v", err)
		case <-deadline.C:
			t.Fatal("actual Backend readiness timeout")
		case <-ticker.C:
		}
	}
}

func ticket26Environment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(name)]; !replaced {
			environment = append(environment, entry)
		}
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}
