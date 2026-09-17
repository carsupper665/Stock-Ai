package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

const (
	codexHarnessVersion      = "codex-cli 0.145.0"
	claudeCodeHarnessVersion = "2.1.269 (Claude Code)"
	maxHarnessOutput         = 1 << 20
	maxHarnessErrorOutput    = 64 << 10
)

var (
	ErrWebHarnessUnavailable    = errors.New("web harness is unavailable")
	ErrWebHarnessVersion        = errors.New("web harness version does not match the pinned version")
	ErrWebHarnessAuthentication = errors.New("web harness authentication failed")
	ErrWebHarnessResponse       = errors.New("web harness returned an invalid response")
	errHarnessOutputLimit       = errors.New("web harness output limit exceeded")
)

type WebHarnessMessage struct {
	Role       string               `json:"role"`
	Content    *string              `json:"content"`
	ToolCalls  []WebHarnessToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string              `json:"tool_call_id,omitempty"`
}

type WebHarnessTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type WebHarnessToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type WebHarnessRequest struct {
	Model    string              `json:"model"`
	Tools    []WebHarnessTool    `json:"tools"`
	Messages []WebHarnessMessage `json:"messages"`
}

type WebHarnessUsage struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
	CachedTokens *int `json:"cached_tokens"`
	TotalTokens  *int `json:"total_tokens"`
}

type WebHarnessResponse struct {
	Content         *string              `json:"content"`
	Reasoning       *string              `json:"reasoning,omitempty"`
	ToolCalls       []WebHarnessToolCall `json:"tool_calls"`
	FinishReason    string               `json:"finish_reason"`
	Usage           *WebHarnessUsage     `json:"usage"`
	NumTurns        *int                 `json:"num_turns,omitempty"`
	TotalCostUSD    *float64             `json:"total_cost_usd,omitempty"`
	SessionMode     string               `json:"session_mode,omitempty"`
	FallbackReason  string               `json:"fallback_reason,omitempty"`
	InvocationCount int                  `json:"invocation_count,omitempty"`
}

type CodexWebHarness struct {
	Executable string
	AuthFile   string
	Effort     string
}

type ClaudeCodeWebHarness struct {
	Executable string
	AuthFile   string
	Effort     string
}

type claudeHarnessSession struct {
	key              string
	workingDirectory string
	sessionID        string
	delivered        []WebHarnessMessage
	generated        *WebHarnessMessage
	gate             chan struct{}
	lastUsed         time.Time
	active           int
	retired          bool
}

func (h CodexWebHarness) Generate(ctx context.Context, credential string, request WebHarnessRequest) (WebHarnessResponse, error) {
	workingDirectory, err := prepareHarness(h.Executable, credential, h.AuthFile, request)
	if err != nil {
		return WebHarnessResponse{}, err
	}
	defer os.RemoveAll(workingDirectory)
	if err := verifyHarnessVersion(ctx, h.Executable, codexHarnessVersion, workingDirectory, harnessEnvironment(workingDirectory, "", "")); err != nil {
		return WebHarnessResponse{}, err
	}
	if err := copyHarnessLogin(h.AuthFile, filepath.Join(workingDirectory, "auth.json")); err != nil {
		return WebHarnessResponse{}, err
	}
	environment := harnessEnvironment(workingDirectory, "CODEX_API_KEY", credential)
	schemaPath := filepath.Join(workingDirectory, "response-schema.json")
	if err := os.WriteFile(schemaPath, []byte(webHarnessResponseSchema), 0o600); err != nil {
		return WebHarnessResponse{}, ErrWebHarnessUnavailable
	}
	prompt, err := webHarnessPrompt(request)
	if err != nil {
		return WebHarnessResponse{}, err
	}
	arguments := []string{
		"exec", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "-c", `web_search="live"`,
	}
	if h.Effort != "" {
		arguments = append(arguments, "-c", `model_reasoning_effort="`+h.Effort+`"`)
	}
	if request.Model != "cli-default" {
		arguments = append(arguments, "-m", request.Model)
	}
	for _, feature := range []string{
		"shell_tool", "apps", "multi_agent", "goals", "hooks", "memories", "remote_plugin", "plugins",
		"browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "image_generation",
		"code_mode", "code_mode_host", "skill_search", "tool_suggest", "workspace_dependencies",
	} {
		arguments = append(arguments, "--disable", feature)
	}
	arguments = append(arguments, "--json", "--output-schema", schemaPath, "-")
	stdout, stderr, runErr := runHarnessCommand(ctx, h.Executable, arguments, workingDirectory, environment, prompt)
	if ctx.Err() != nil {
		return WebHarnessResponse{}, ctx.Err()
	}
	if runErr != nil {
		if errors.Is(runErr, errHarnessOutputLimit) {
			return WebHarnessResponse{}, ErrWebHarnessResponse
		}
		return WebHarnessResponse{}, classifyHarnessFailure(stdout, stderr)
	}
	return parseCodexHarnessResponse(stdout, request.Messages)
}

func (h ClaudeCodeWebHarness) Generate(ctx context.Context, credential string, request WebHarnessRequest) (WebHarnessResponse, error) {
	workingDirectory, err := prepareHarness(h.Executable, credential, h.AuthFile, request)
	if err != nil {
		return WebHarnessResponse{}, err
	}
	defer os.RemoveAll(workingDirectory)
	if err := verifyHarnessVersion(ctx, h.Executable, claudeCodeHarnessVersion, workingDirectory, harnessEnvironment(workingDirectory, "", "")); err != nil {
		return WebHarnessResponse{}, err
	}
	if err := copyHarnessLogin(h.AuthFile, filepath.Join(workingDirectory, ".credentials.json")); err != nil {
		return WebHarnessResponse{}, err
	}
	environment := harnessEnvironment(workingDirectory, "ANTHROPIC_API_KEY", credential)
	prompt, err := webHarnessPrompt(request)
	if err != nil {
		return WebHarnessResponse{}, err
	}
	arguments := []string{
		"--restricted", "--safe-mode", "--setting-sources", "", "--strict-mcp-config",
		"--tools", "WebSearch,WebFetch", "--allowedTools", "WebSearch,WebFetch", "--disallowedTools", "mcp__*",
		"--permission-mode", "dontAsk", "--permission-prompts", "none", "--no-session-persistence",
		"--disable-slash-commands", "--no-chrome", "--model", request.Model, "-p", "--output-format", "json",
		"--json-schema", webHarnessResponseSchema,
	}
	if h.Effort != "" {
		arguments = append(arguments, "--effort", h.Effort)
	}
	stdout, stderr, runErr := runHarnessCommand(ctx, h.Executable, arguments, workingDirectory, environment, prompt)
	if ctx.Err() != nil {
		return WebHarnessResponse{}, ctx.Err()
	}
	if runErr != nil {
		if errors.Is(runErr, errHarnessOutputLimit) {
			return WebHarnessResponse{}, ErrWebHarnessResponse
		}
		return WebHarnessResponse{}, classifyHarnessFailure(stdout, stderr)
	}
	return parseClaudeHarnessResponse(stdout, request.Messages)
}

func (s *Server) generateClaudeSession(ctx context.Context, key, credential string, loginSnapshot []byte, effort string, request WebHarnessRequest, continuation *webHarnessContinuation) (WebHarnessResponse, error) {
	session, err := s.acquireClaudeSession(ctx, key, credential, loginSnapshot, request)
	if err != nil {
		return WebHarnessResponse{}, err
	}
	fallbackReason := ""
	resumeMessages := []WebHarnessMessage(nil)
	_, directoryErr := os.Stat(session.workingDirectory)
	replace := false
	if directoryErr != nil {
		fallbackReason = "session_directory_missing"
		replace = true
	} else if len(session.delivered) == 0 {
		if continuation != nil {
			fallbackReason = "continuation_unavailable"
		}
	} else if continuation != nil {
		if validClaudeContinuation(session, request.Messages, continuation) {
			resumeMessages = continuation.messages
		} else {
			fallbackReason = "continuation_invalid"
			replace = true
		}
	} else {
		validPrefix := messagePrefix(request.Messages, session.delivered)
		switch {
		case !validPrefix:
			fallbackReason = "history_prefix_changed"
			replace = true
		case len(request.Messages) == len(session.delivered):
			fallbackReason = "no_new_messages"
			replace = true
		default:
			resumeMessages = request.Messages[len(session.delivered):]
		}
	}
	if fallbackReason != "" {
		log.Printf("web_harness event=fallback harness=claude_code reason=%s", fallbackReason)
	}
	if replace {
		fresh, replaceErr := s.reserveClaudeReplacement(ctx, session, credential, loginSnapshot, request)
		s.releaseClaudeSession(session)
		if replaceErr != nil {
			return WebHarnessResponse{}, replaceErr
		}
		session = fresh
	}
	resume := len(resumeMessages) != 0
	response, invokeErr := (ClaudeCodeWebHarness{Executable: s.claudeExecutable, Effort: effort}).generateInDirectory(
		ctx, credential, request, session.workingDirectory, session.sessionID, resume, resumeMessages)
	if invokeErr == nil {
		acknowledgeClaudeSession(session, request.Messages, response)
		response.SessionMode = "cold"
		if resume {
			response.SessionMode = "resume"
		}
		response.FallbackReason = fallbackReason
		response.InvocationCount = 1
		s.releaseClaudeSession(session)
		return response, nil
	}
	if !resume || ctx.Err() != nil {
		s.retireClaudeSession(session)
		s.releaseClaudeSession(session)
		if ctx.Err() != nil {
			return WebHarnessResponse{}, ctx.Err()
		}
		return WebHarnessResponse{}, invokeErr
	}
	// A persisted CLI session can become unusable independently of the request.
	// Reserve its one cold retry before releasing waiters so the UUID cannot be initialized twice.
	log.Printf("web_harness event=fallback harness=claude_code reason=resume_failed category=%s", harnessErrorCategory(invokeErr))
	cold, replaceErr := s.reserveClaudeReplacement(ctx, session, credential, loginSnapshot, request)
	s.releaseClaudeSession(session)
	if replaceErr != nil {
		return WebHarnessResponse{}, replaceErr
	}
	response, coldErr := (ClaudeCodeWebHarness{Executable: s.claudeExecutable, Effort: effort}).generateInDirectory(
		ctx, credential, request, cold.workingDirectory, cold.sessionID, false, nil)
	if coldErr == nil {
		acknowledgeClaudeSession(cold, request.Messages, response)
		response.SessionMode = "cold"
		response.FallbackReason = "resume_failed"
		response.InvocationCount = 2
		s.releaseClaudeSession(cold)
		return response, nil
	}
	s.retireClaudeSession(cold)
	s.releaseClaudeSession(cold)
	if ctx.Err() != nil {
		return WebHarnessResponse{}, ctx.Err()
	}
	return WebHarnessResponse{}, coldErr
}

func validClaudeContinuation(session *claudeHarnessSession, messages []WebHarnessMessage, continuation *webHarnessContinuation) bool {
	if continuation.afterMessages != len(session.delivered) || len(continuation.messages) == 0 || session.generated == nil {
		return false
	}
	if len(messages) != continuation.afterMessages+1+len(continuation.messages) || !messagePrefix(messages, session.delivered) {
		return false
	}
	if !reflect.DeepEqual(messages[continuation.afterMessages], *session.generated) {
		return false
	}
	return reflect.DeepEqual(messages[continuation.afterMessages+1:], continuation.messages)
}

func acknowledgeClaudeSession(session *claudeHarnessSession, messages []WebHarnessMessage, response WebHarnessResponse) {
	session.delivered = cloneHarnessMessages(messages)
	generated := WebHarnessMessage{Role: "assistant", Content: response.Content}
	if len(response.ToolCalls) != 0 {
		generated.ToolCalls = append([]WebHarnessToolCall(nil), response.ToolCalls...)
	}
	cloned := cloneHarnessMessages([]WebHarnessMessage{generated})
	session.generated = &cloned[0]
}

func (s *Server) acquireClaudeSession(ctx context.Context, key, credential string, loginSnapshot []byte, request WebHarnessRequest) (*claudeHarnessSession, error) {
	for {
		s.harnessSessionMu.Lock()
		session := s.harnessSessions[key]
		if session == nil {
			var err error
			session, err = s.newClaudeHarnessSession(key, credential, loginSnapshot, request)
			if err != nil {
				s.harnessSessionMu.Unlock()
				return nil, err
			}
			s.harnessSessions[key] = session
		}
		session.active++
		s.harnessSessionMu.Unlock()
		select {
		case <-ctx.Done():
			s.harnessSessionMu.Lock()
			session.active--
			remove := session.retired && session.active == 0
			s.harnessSessionMu.Unlock()
			if remove {
				_ = os.RemoveAll(session.workingDirectory)
			}
			return nil, ctx.Err()
		case <-session.gate:
		}
		s.harnessSessionMu.Lock()
		current := s.harnessSessions[key] == session && !session.retired
		s.harnessSessionMu.Unlock()
		if current {
			return session, nil
		}
		s.releaseClaudeSession(session)
	}
}

func (s *Server) reserveClaudeReplacement(ctx context.Context, current *claudeHarnessSession, credential string, loginSnapshot []byte, request WebHarnessRequest) (*claudeHarnessSession, error) {
	if err := ctx.Err(); err != nil {
		s.retireClaudeSession(current)
		return nil, err
	}
	s.harnessSessionMu.Lock()
	if s.harnessSessions[current.key] != current {
		s.harnessSessionMu.Unlock()
		return nil, ErrWebHarnessUnavailable
	}
	replacement, err := s.newClaudeHarnessSession(current.key, credential, loginSnapshot, request)
	if err != nil {
		delete(s.harnessSessions, current.key)
		current.retired = true
		s.harnessSessionMu.Unlock()
		return nil, err
	}
	<-replacement.gate
	replacement.active = 1
	current.retired = true
	s.harnessSessions[current.key] = replacement
	s.harnessSessionMu.Unlock()
	return replacement, nil
}

func (s *Server) newClaudeHarnessSession(key, credential string, loginSnapshot []byte, request WebHarnessRequest) (*claudeHarnessSession, error) {
	workingDirectory, err := prepareHarnessWithLogin(s.claudeExecutable, credential, len(loginSnapshot) != 0, request)
	if err != nil {
		return nil, err
	}
	if len(loginSnapshot) != 0 {
		if err := os.WriteFile(filepath.Join(workingDirectory, ".credentials.json"), loginSnapshot, 0o600); err != nil {
			_ = os.RemoveAll(workingDirectory)
			return nil, ErrWebHarnessUnavailable
		}
	}
	sessionID, err := newHarnessSessionID()
	if err != nil {
		_ = os.RemoveAll(workingDirectory)
		return nil, ErrWebHarnessUnavailable
	}
	session := &claudeHarnessSession{key: key, workingDirectory: workingDirectory, sessionID: sessionID, gate: make(chan struct{}, 1), lastUsed: time.Now()}
	session.gate <- struct{}{}
	return session, nil
}

func (s *Server) releaseClaudeSession(session *claudeHarnessSession) {
	s.harnessSessionMu.Lock()
	session.active--
	session.lastUsed = time.Now()
	remove := session.retired && session.active == 0
	s.harnessSessionMu.Unlock()
	session.gate <- struct{}{}
	if remove {
		_ = os.RemoveAll(session.workingDirectory)
	}
}

func (s *Server) retireClaudeSession(session *claudeHarnessSession) {
	s.harnessSessionMu.Lock()
	if s.harnessSessions[session.key] == session {
		delete(s.harnessSessions, session.key)
	}
	session.retired = true
	s.harnessSessionMu.Unlock()
}

func (s *Server) collectHarnessSessions() {
	interval := time.Minute
	if s.harnessSessionTTL < interval {
		interval = s.harnessSessionTTL
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(s.harnessGCDone)
	for {
		select {
		case <-s.harnessGCStop:
			return
		case now := <-ticker.C:
			var directories []string
			s.harnessSessionMu.Lock()
			for key, session := range s.harnessSessions {
				if session.active == 0 && now.Sub(session.lastUsed) >= s.harnessSessionTTL {
					delete(s.harnessSessions, key)
					session.retired = true
					directories = append(directories, session.workingDirectory)
				}
			}
			s.harnessSessionMu.Unlock()
			for _, directory := range directories {
				_ = os.RemoveAll(directory)
			}
		}
	}
}

func (s *Server) removeAllHarnessSessions() {
	s.harnessSessionMu.Lock()
	directories := make([]string, 0, len(s.harnessSessions))
	for key, session := range s.harnessSessions {
		delete(s.harnessSessions, key)
		session.retired = true
		directories = append(directories, session.workingDirectory)
	}
	s.harnessSessionMu.Unlock()
	for _, directory := range directories {
		_ = os.RemoveAll(directory)
	}
}

func (h ClaudeCodeWebHarness) generateInDirectory(ctx context.Context, credential string, request WebHarnessRequest, workingDirectory, sessionID string, resume bool, resumeMessages []WebHarnessMessage) (WebHarnessResponse, error) {
	if err := verifyHarnessVersion(ctx, h.Executable, claudeCodeHarnessVersion, workingDirectory, harnessEnvironment(workingDirectory, "", "")); err != nil {
		return WebHarnessResponse{}, err
	}
	environment := harnessEnvironment(workingDirectory, "ANTHROPIC_API_KEY", credential)
	var prompt []byte
	var err error
	if resume {
		prompt, err = claudeResumePrompt(resumeMessages)
	} else {
		prompt, err = webHarnessPrompt(request)
	}
	if err != nil {
		return WebHarnessResponse{}, err
	}
	arguments := []string{
		"--restricted", "--safe-mode", "--setting-sources", "", "--strict-mcp-config",
		"--tools", "WebSearch,WebFetch", "--allowedTools", "WebSearch,WebFetch", "--disallowedTools", "mcp__*",
		"--permission-mode", "dontAsk", "--permission-prompts", "none", "--disable-slash-commands", "--no-chrome",
	}
	if resume {
		arguments = append(arguments, "--resume", sessionID)
	} else {
		arguments = append(arguments, "--session-id", sessionID, "--model", request.Model)
		if h.Effort != "" {
			arguments = append(arguments, "--effort", h.Effort)
		}
	}
	arguments = append(arguments, "-p", "--output-format", "json", "--json-schema", webHarnessResponseSchema)
	stdout, stderr, runErr := runHarnessCommand(ctx, h.Executable, arguments, workingDirectory, environment, prompt)
	if ctx.Err() != nil {
		return WebHarnessResponse{}, ctx.Err()
	}
	if runErr != nil {
		if errors.Is(runErr, errHarnessOutputLimit) {
			return WebHarnessResponse{}, ErrWebHarnessResponse
		}
		return WebHarnessResponse{}, classifyHarnessFailure(stdout, stderr)
	}
	return parseClaudeHarnessResponse(stdout, request.Messages)
}

func claudeResumePrompt(messages []WebHarnessMessage) ([]byte, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, ErrWebHarnessResponse
	}
	return []byte("Continue with these new normalized messages. Return only the JSON object required by the output schema:\n" + string(encoded)), nil
}

func messagePrefix(messages, delivered []WebHarnessMessage) bool {
	return len(delivered) == 0 || (len(messages) >= len(delivered) && reflect.DeepEqual(messages[:len(delivered)], delivered))
}

func cloneHarnessMessages(messages []WebHarnessMessage) []WebHarnessMessage {
	encoded, _ := json.Marshal(messages)
	var cloned []WebHarnessMessage
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}

func newHarnessSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func prepareHarness(executable, credential, authFile string, request WebHarnessRequest) (string, error) {
	return prepareHarnessWithLogin(executable, credential, harnessFileAvailable(authFile), request)
}

func prepareHarnessWithLogin(executable, credential string, loginAvailable bool, request WebHarnessRequest) (string, error) {
	if (credential == "" && !loginAvailable) || !filepath.IsAbs(executable) || (runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(executable), ".exe")) {
		return "", ErrWebHarnessUnavailable
	}
	if strings.TrimSpace(request.Model) == "" || len(request.Messages) == 0 {
		return "", ErrWebHarnessResponse
	}
	workingDirectory, err := os.MkdirTemp("", "stock-ai-web-harness-")
	if err != nil {
		return "", ErrWebHarnessUnavailable
	}
	return workingDirectory, nil
}

func copyHarnessLogin(source, destination string) error {
	if source == "" {
		return nil
	}
	data, err := readHarnessLogin(source)
	if err != nil {
		return err
	}
	if os.WriteFile(destination, data, 0o600) != nil {
		return ErrWebHarnessUnavailable
	}
	return nil
}

func readHarnessLogin(source string) ([]byte, error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, ErrWebHarnessAuthentication
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(data) > 1<<20 || !validJSONObject(data) {
		return nil, ErrWebHarnessAuthentication
	}
	return data, nil
}

func verifyHarnessVersion(ctx context.Context, executable, expected, workingDirectory string, environment []string) error {
	stdout, _, err := runHarnessCommand(ctx, executable, []string{"--version"}, workingDirectory, environment, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return ErrWebHarnessUnavailable
	}
	if strings.TrimSpace(string(stdout)) != expected {
		return ErrWebHarnessVersion
	}
	return nil
}

func webHarnessPrompt(request WebHarnessRequest) ([]byte, error) {
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, ErrWebHarnessResponse
	}
	// Keep the stable instructions and tool schemas before growing conversation history so CLI prompt caching can reuse the prefix.
	prompt := "You are a reasoning and web research provider. Use only native Web Search and Web Fetch when current public information is needed. " +
		"The listed domain tools are return-only schemas: never execute them. If one is needed, return its structured call for the external Go runtime. " +
		"Encode each tool call's arguments object as a JSON string in the schema's arguments field. " +
		"Return only the JSON object required by the output schema. The normalized request follows:\n" + string(requestJSON)
	return []byte(prompt), nil
}

func harnessEnvironment(workingDirectory, credentialName, credential string) []string {
	environment := []string{
		"HOME=" + workingDirectory,
		"USERPROFILE=" + workingDirectory,
		"CODEX_HOME=" + workingDirectory,
		"CLAUDE_CONFIG_DIR=" + workingDirectory,
	}
	if credentialName != "" && credential != "" {
		environment = append(environment, credentialName+"="+credential)
	}
	for _, name := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, exists := os.LookupEnv(name); exists {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func runHarnessCommand(ctx context.Context, executable string, arguments []string, workingDirectory string, environment []string, stdin []byte) ([]byte, []byte, error) {
	command := exec.Command(executable, arguments...)
	command.Dir = workingDirectory
	command.Env = environment
	command.Stdin = bytes.NewReader(stdin)
	command.WaitDelay = time.Second
	var stdout, stderr limitedHarnessBuffer
	stdout.remaining = maxHarnessOutput
	stderr.remaining = maxHarnessErrorOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := runHarnessProcess(ctx, command)
	if stdout.overflow || stderr.overflow {
		err = errHarnessOutputLimit
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

type limitedHarnessBuffer struct {
	buffer    bytes.Buffer
	remaining int
	overflow  bool
}

func (w *limitedHarnessBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if len(value) > w.remaining {
		if w.remaining > 0 {
			_, _ = w.buffer.Write(value[:w.remaining])
			w.remaining = 0
		}
		w.overflow = true
		return written, nil
	}
	w.remaining -= len(value)
	_, _ = w.buffer.Write(value)
	return written, nil
}

func (w *limitedHarnessBuffer) Bytes() []byte { return w.buffer.Bytes() }

func classifyHarnessFailure(stdout, stderr []byte) error {
	reason := harnessReason(stdout)
	return harnessFailure(string(stdout) + "\n" + string(stderr) + "\n" + reason)
}

// Both CLIs report failures as JSON on stdout: Claude Code as one result object with
// is_error/result, Codex as a stream of turn.failed / error events. The last such message wins.
func harnessReason(stdout []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	reason := ""
	for {
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			IsError *bool  `json:"is_error"`
			Result  string `json:"result"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if decoder.Decode(&event) != nil {
			return reason
		}
		switch {
		case event.IsError != nil && *event.IsError && event.Result != "":
			reason = event.Result
		case event.Type == "turn.failed" && event.Error.Message != "":
			reason = event.Error.Message
		case event.Type == "error" && event.Message != "":
			reason = event.Message
		}
	}
}

func harnessFailure(message string) error {
	kind := ErrWebHarnessUnavailable
	lowered := strings.ToLower(message)
	for _, marker := range []string{"authentication", "authenticate", "unauthorized", "not logged in", "login expired", "token expired", "oauth expired", "invalid api key", "api_key", "401", "403"} {
		if strings.Contains(lowered, marker) {
			kind = ErrWebHarnessAuthentication
			break
		}
	}
	detail := ""
	switch {
	case strings.Contains(lowered, "usage limit"), strings.Contains(lowered, "quota"),
		strings.Contains(lowered, "credit balance"), strings.Contains(lowered, "rate limit"):
		detail = "CLI quota or usage limit was reached."
	case strings.Contains(lowered, "unknown model"), strings.Contains(lowered, "model_not_found"):
		detail = "CLI does not provide the requested model."
	case strings.Contains(lowered, "login expired"), strings.Contains(lowered, "token expired"),
		strings.Contains(lowered, "oauth expired"), strings.Contains(lowered, "not logged in"):
		detail = "CLI login is missing or expired."
	case kind == ErrWebHarnessAuthentication:
		detail = "CLI authentication was rejected."
	}
	if detail == "" {
		return kind
	}
	return fmt.Errorf("%w: %s", kind, detail)
}

// HarnessDetail returns a fixed safe diagnostic category attached by harnessFailure, or "".
func HarnessDetail(err error) string {
	for _, kind := range []error{ErrWebHarnessAuthentication, ErrWebHarnessUnavailable, ErrWebHarnessResponse} {
		if errors.Is(err, kind) && strings.HasPrefix(err.Error(), kind.Error()+": ") {
			return strings.TrimPrefix(err.Error(), kind.Error()+": ")
		}
	}
	return ""
}

func parseCodexHarnessResponse(output []byte, history []WebHarnessMessage) (WebHarnessResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var structured []byte
	var normalizedUsage *WebHarnessUsage
	completed := false
	failure := ""
	for {
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage *struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
				CachedTokens *int `json:"cached_input_tokens"`
			} `json:"usage"`
		}
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return WebHarnessResponse{}, ErrWebHarnessResponse
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" {
			structured = []byte(event.Item.Text)
		}
		if event.Type == "turn.completed" {
			completed = true
			if event.Usage != nil {
				normalizedUsage = &WebHarnessUsage{InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens, CachedTokens: event.Usage.CachedTokens}
			}
		}
		if event.Type == "turn.failed" && event.Error.Message != "" {
			failure = event.Error.Message
		} else if event.Type == "error" && event.Message != "" {
			failure = event.Message
		}
	}
	if failure != "" && !completed {
		return WebHarnessResponse{}, harnessFailure(failure)
	}
	if !completed || len(structured) == 0 {
		return WebHarnessResponse{}, ErrWebHarnessResponse
	}
	return decodeWebHarnessResponse(structured, normalizedUsage, history)
}

func parseClaudeHarnessResponse(output []byte, history []WebHarnessMessage) (WebHarnessResponse, error) {
	var result struct {
		Type             string          `json:"type"`
		Subtype          string          `json:"subtype"`
		IsError          *bool           `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		Usage            *struct {
			InputTokens              *int `json:"input_tokens"`
			OutputTokens             *int `json:"output_tokens"`
			CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		NumTurns     *int     `json:"num_turns"`
		TotalCostUSD *float64 `json:"total_cost_usd"`
	}
	if json.Unmarshal(output, &result) != nil || result.Type != "result" || result.IsError == nil {
		return WebHarnessResponse{}, ErrWebHarnessResponse
	}
	if *result.IsError && result.Result != "" {
		return WebHarnessResponse{}, harnessFailure(result.Result)
	}
	if *result.IsError || result.Subtype != "success" || len(result.StructuredOutput) == 0 {
		return WebHarnessResponse{}, ErrWebHarnessResponse
	}
	if (result.NumTurns != nil && *result.NumTurns < 0) || (result.TotalCostUSD != nil && (*result.TotalCostUSD < 0 || math.IsNaN(*result.TotalCostUSD) || math.IsInf(*result.TotalCostUSD, 0))) {
		return WebHarnessResponse{}, ErrWebHarnessResponse
	}
	var normalizedUsage *WebHarnessUsage
	if result.Usage != nil {
		// Claude Code reports Anthropic-shaped Usage, where input_tokens excludes both cache
		// components; §32.4 requires cached_tokens to be a subset of input_tokens.
		normalizedUsage = &WebHarnessUsage{
			InputTokens:  sumTokens(result.Usage.InputTokens, result.Usage.CacheCreationInputTokens, result.Usage.CacheReadInputTokens),
			OutputTokens: result.Usage.OutputTokens,
			CachedTokens: result.Usage.CacheReadInputTokens,
		}
	}
	response, err := decodeWebHarnessResponse(result.StructuredOutput, normalizedUsage, history)
	if err == nil {
		response.NumTurns = result.NumTurns
		response.TotalCostUSD = result.TotalCostUSD
	}
	return response, err
}

func decodeWebHarnessResponse(value []byte, normalizedUsage *WebHarnessUsage, history []WebHarnessMessage) (WebHarnessResponse, error) {
	var structured struct {
		Content   *string `json:"content"`
		Reasoning *string `json:"reasoning"`
		ToolCalls []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"tool_calls"`
		FinishReason string `json:"finish_reason"`
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&structured) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return WebHarnessResponse{}, ErrWebHarnessResponse
	}
	response := WebHarnessResponse{Content: structured.Content, Reasoning: structured.Reasoning, FinishReason: structured.FinishReason, Usage: normalizedUsage}
	response.ToolCalls = make([]WebHarnessToolCall, 0, len(structured.ToolCalls))
	for _, call := range structured.ToolCalls {
		response.ToolCalls = append(response.ToolCalls, WebHarnessToolCall{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	}
	if err := validateWebHarnessResponse(&response, history); err != nil {
		return WebHarnessResponse{}, ErrWebHarnessResponse
	}
	return response, nil
}

func validateWebHarnessResponse(response *WebHarnessResponse, history []WebHarnessMessage) error {
	if response.Usage != nil {
		for _, count := range []*int{response.Usage.InputTokens, response.Usage.OutputTokens, response.Usage.CachedTokens, response.Usage.TotalTokens} {
			if count != nil && *count < 0 {
				return ErrWebHarnessResponse
			}
		}
		// Fail here rather than let a Runtime reject the whole model attempt with no diagnostic.
		if response.Usage.InputTokens != nil && response.Usage.CachedTokens != nil && *response.Usage.CachedTokens > *response.Usage.InputTokens {
			return ErrWebHarnessResponse
		}
	}
	seenIDs := map[string]bool{}
	for _, message := range history {
		for _, call := range message.ToolCalls {
			seenIDs[call.ID] = true
		}
	}
	if len(response.ToolCalls) == 0 {
		if response.Content == nil || (response.FinishReason != "stop" && response.FinishReason != "length" && response.FinishReason != "content_filter") {
			return ErrWebHarnessResponse
		}
		response.ToolCalls = []WebHarnessToolCall{}
		return nil
	}
	if response.FinishReason != "tool_call" {
		return ErrWebHarnessResponse
	}
	for index := range response.ToolCalls {
		call := &response.ToolCalls[index]
		if strings.TrimSpace(call.ID) == "" || !modelNamePattern.MatchString(call.Name) || !validJSONObject(call.Arguments) || seenIDs[call.ID] {
			return ErrWebHarnessResponse
		}
		seenIDs[call.ID] = true
		var object map[string]json.RawMessage
		_ = json.Unmarshal(call.Arguments, &object)
		call.Arguments, _ = json.Marshal(object)
	}
	return nil
}

const webHarnessResponseSchema = `{
  "type":"object",
  "properties":{
    "content":{"type":["string","null"]},
    "reasoning":{"type":["string","null"]},
    "tool_calls":{"type":"array","items":{"type":"object","properties":{
      "id":{"type":"string"},"name":{"type":"string"},"arguments":{"type":"string"}
    },"required":["id","name","arguments"],"additionalProperties":false}},
    "finish_reason":{"type":"string","enum":["stop","length","content_filter","tool_call"]}
  },
  "required":["content","tool_calls","finish_reason"],
  "additionalProperties":false
}`
