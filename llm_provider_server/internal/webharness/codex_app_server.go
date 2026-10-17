package webharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"stock-ai/llm-provider-server/internal/providers"
	"strings"
	"sync"
	"time"
)

const (
	codexAppServerIdle = 30 * time.Minute
	codexInstructions  = "You are a reasoning and web research provider. Use only native web search when current public information is needed. " +
		"The provided functions are executed by the caller: call one when it is needed instead of describing it."
)

// Code mode stays enabled: Codex 0.162 routes dynamic tools through its code-mode host.
var codexDisabledFeatures = []string{
	"shell_tool", "apps", "multi_agent", "goals", "hooks", "memories", "remote_plugin", "plugins",
	"browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "image_generation",
	"skill_search", "tool_suggest", "workspace_dependencies",
}

// CodexAppServer runs Codex through `codex app-server` (JSON-RPC over stdio), one long-lived
// process per credential. Every Complete runs on a fresh ephemeral thread seeded with the full
// history and stops at the first tool call, so the Gateway keeps no conversation state and a
// retried Generate is safe.
type CodexAppServer struct {
	mu        sync.Mutex
	processes map[string]*codexProcess
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewCodexAppServer() *CodexAppServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &CodexAppServer{processes: map[string]*codexProcess{}, ctx: ctx, cancel: cancel}
}

// Close kills every app-server process tree and waits until its private CODEX_HOME is removed.
func (c *CodexAppServer) Close() {
	c.cancel()
	c.mu.Lock()
	processes := c.processes
	c.processes = map[string]*codexProcess{}
	c.mu.Unlock()
	for _, process := range processes {
		<-process.exited
	}
}

func (c *CodexAppServer) Complete(ctx context.Context, model providers.Model, request providers.GenerateRequest, options providers.Options) (providers.GenerateResponse, error) {
	setup, err := ResolveHarness("codex", model.Config, options.APIKey, false, options.Options)
	if err != nil {
		return providers.GenerateResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, options.Client.Timeout)
	defer cancel()
	process, err := c.acquire(ctx, setup)
	if err != nil {
		return providers.GenerateResponse{}, HarnessProviderError("codex", err)
	}
	defer c.release(process)
	response, err := process.generate(ctx, model.ID, setup.Effort, request)
	if err != nil {
		return providers.GenerateResponse{}, HarnessProviderError("codex", err)
	}
	return response, nil
}

func (c *CodexAppServer) acquire(ctx context.Context, setup HarnessSetup) (*codexProcess, error) {
	var login []byte
	if setup.AuthFile != "" {
		var err error
		if login, err = ReadHarnessLogin(setup.AuthFile); err != nil {
			return nil, err
		}
	}
	digest := sha256.Sum256([]byte(setup.Executable + "\x00" + setup.Credential + "\x00" + string(login)))
	key := hex.EncodeToString(digest[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return nil, ErrWebHarnessUnavailable
	}
	for existing, process := range c.processes {
		if process.closed() || (process.active == 0 && time.Since(process.lastUsed) > codexAppServerIdle) {
			process.stop()
			delete(c.processes, existing)
		}
	}
	if process := c.processes[key]; process != nil {
		process.active++
		return process, nil
	}
	process, err := startCodexProcess(ctx, c.ctx, setup, login)
	if err != nil {
		return nil, err
	}
	process.active++
	c.processes[key] = process
	return process, nil
}

func (c *CodexAppServer) release(process *codexProcess) {
	c.mu.Lock()
	process.active--
	process.lastUsed = time.Now()
	c.mu.Unlock()
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type codexProcess struct {
	home     string
	stdin    io.WriteCloser
	stop     context.CancelFunc
	exited   chan struct{}
	writeMu  sync.Mutex
	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan rpcMessage
	threads  map[string]chan rpcMessage
	failure  error
	active   int
	lastUsed time.Time
}

func startCodexProcess(ctx, base context.Context, setup HarnessSetup, login []byte) (*codexProcess, error) {
	home, err := os.MkdirTemp("", "stock-ai-codex-app-server-")
	if err != nil {
		return nil, ErrWebHarnessUnavailable
	}
	environment := harnessEnvironment(home, "", "")
	if err := verifyHarnessVersion(ctx, setup.Executable, CodexHarnessVersion, home, environment); err != nil {
		os.RemoveAll(home)
		return nil, err
	}
	if login != nil && os.WriteFile(filepath.Join(home, "auth.json"), login, 0o600) != nil {
		os.RemoveAll(home)
		return nil, ErrWebHarnessUnavailable
	}
	arguments := []string{"app-server", "-c", `web_search="live"`}
	for _, feature := range codexDisabledFeatures {
		arguments = append(arguments, "--disable", feature)
	}
	command := exec.Command(setup.Executable, arguments...)
	command.Dir = home
	command.Env = environment
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		os.RemoveAll(home)
		return nil, ErrWebHarnessUnavailable
	}
	processContext, stop := context.WithCancel(base)
	p := &codexProcess{home: home, stdin: stdin, stop: stop, exited: make(chan struct{}), pending: map[int64]chan rpcMessage{},
		threads: map[string]chan rpcMessage{}, lastUsed: time.Now()}
	command.Stdout = &rpcLineWriter{process: p}
	command.Stderr = io.Discard
	go func() {
		err := runHarnessProcess(processContext, command)
		if err == nil || errors.Is(err, context.Canceled) {
			err = ErrWebHarnessUnavailable
		}
		p.fail(err)
		os.RemoveAll(home)
		close(p.exited)
	}()
	if err := p.handshake(ctx, setup.Credential, login != nil); err != nil {
		stop()
		<-p.exited
		return nil, err
	}
	return p, nil
}

func (p *codexProcess) handshake(ctx context.Context, credential string, localLogin bool) error {
	initialize := map[string]any{"clientInfo": map[string]string{"name": "stock-ai-llm-provider-server", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": true}}
	if err := p.call(ctx, "initialize", initialize, nil); err != nil {
		return err
	}
	if err := p.write(rpcMessage{Method: "initialized"}); err != nil {
		return err
	}
	if !localLogin {
		return p.call(ctx, "account/login/start", map[string]string{"type": "apiKey", "apiKey": credential}, nil)
	}
	var account struct {
		Account json.RawMessage `json:"account"`
	}
	if err := p.call(ctx, "account/read", map[string]bool{"refreshToken": false}, &account); err != nil {
		return err
	}
	if len(account.Account) == 0 || providers.IsJSONNull(account.Account) {
		return harnessFailure("not logged in")
	}
	return nil
}

func (p *codexProcess) closed() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

func (p *codexProcess) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == nil {
		p.failure = err
	}
}

func (p *codexProcess) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

func (p *codexProcess) write(message rpcMessage) error {
	line, err := json.Marshal(message)
	if err != nil {
		return ErrWebHarnessResponse
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return ErrWebHarnessUnavailable
	}
	return nil
}

func (p *codexProcess) send(method string, params any) (int64, chan rpcMessage, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return 0, nil, ErrWebHarnessResponse
	}
	reply := make(chan rpcMessage, 1)
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.pending[id] = reply
	p.mu.Unlock()
	if err := p.write(rpcMessage{ID: json.RawMessage(jsonInt(id)), Method: method, Params: encoded}); err != nil {
		p.forget(id)
		return 0, nil, err
	}
	return id, reply, nil
}

func (p *codexProcess) forget(id int64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

func (p *codexProcess) call(ctx context.Context, method string, params, result any) error {
	id, reply, err := p.send(method, params)
	if err != nil {
		return err
	}
	select {
	case message := <-reply:
		if message.Error != nil {
			return harnessFailure(message.Error.Message)
		}
		if result != nil && json.Unmarshal(message.Result, result) != nil {
			return ErrWebHarnessResponse
		}
		return nil
	case <-ctx.Done():
		p.forget(id)
		return ctx.Err()
	case <-p.exited:
		return p.err()
	}
}

// notify sends a request whose reply nobody waits for.
func (p *codexProcess) notify(method string, params any) {
	if id, _, err := p.send(method, params); err == nil {
		p.forget(id)
	}
}

func jsonInt(value int64) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

// Only events that decide a Generate are routed to its thread; deltas and status chatter are dropped.
var codexThreadEvents = map[string]bool{
	"item/tool/call": true, "item/completed": true, "thread/tokenUsage/updated": true, "turn/completed": true, "error": true,
}

func (p *codexProcess) dispatch(line []byte) {
	var message rpcMessage
	if json.Unmarshal(line, &message) != nil {
		return
	}
	if message.Method == "" {
		var id int64
		if json.Unmarshal(message.ID, &id) != nil {
			return
		}
		p.mu.Lock()
		reply := p.pending[id]
		delete(p.pending, id)
		p.mu.Unlock()
		if reply != nil {
			reply <- message
		}
		return
	}
	var scope struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(message.Params, &scope)
	p.mu.Lock()
	events := p.threads[scope.ThreadID]
	p.mu.Unlock()
	if events != nil && codexThreadEvents[message.Method] {
		select {
		case events <- message:
		default: // A Generate that already returned stops draining; never block the reader for it.
		}
		return
	}
	if len(message.ID) != 0 {
		// approvalPolicy=never and the disabled features leave no other Server request the Gateway can answer.
		_ = p.write(rpcMessage{ID: message.ID, Error: &rpcError{Code: -32601, Message: "unsupported by the Stock AI Gateway"}})
	}
}

// rpcLineWriter receives app-server stdout and dispatches each JSONL message as it completes.
type rpcLineWriter struct {
	process *codexProcess
	line    bytes.Buffer
}

func (w *rpcLineWriter) Write(value []byte) (int, error) {
	written := len(value)
	for len(value) != 0 {
		index := bytes.IndexByte(value, '\n')
		if index < 0 {
			w.line.Write(value)
			break
		}
		w.line.Write(value[:index])
		w.process.dispatch(w.line.Bytes())
		w.line.Reset()
		value = value[index+1:]
	}
	if w.line.Len() > maxHarnessOutput {
		w.process.fail(ErrWebHarnessResponse)
		w.process.stop()
		w.line.Reset()
	}
	return written, nil
}

func (p *codexProcess) generate(ctx context.Context, model, effort string, request providers.GenerateRequest) (providers.GenerateResponse, error) {
	aliases := providers.NewToolAliases(request)
	instructions, history, input := codexHistory(request.Messages, aliases.WireByRuntime)
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": aliases.WireByRuntime[tool.Name], "description": tool.Description, "inputSchema": tool.InputSchema})
	}
	thread := map[string]any{"baseInstructions": instructions, "dynamicTools": tools, "ephemeral": true,
		"sandbox": "read-only", "approvalPolicy": "never", "cwd": p.home}
	if model != "cli-default" {
		thread["model"] = model
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := p.call(ctx, "thread/start", thread, &started); err != nil {
		return providers.GenerateResponse{}, err
	}
	threadID := started.Thread.ID
	events := make(chan rpcMessage, 256)
	p.mu.Lock()
	p.threads[threadID] = events
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.threads, threadID)
		p.mu.Unlock()
		p.notify("thread/unsubscribe", map[string]string{"threadId": threadID})
	}()
	if len(history) != 0 {
		if err := p.call(ctx, "thread/inject_items", map[string]any{"threadId": threadID, "items": history}, nil); err != nil {
			return providers.GenerateResponse{}, err
		}
	}
	turn := map[string]any{"threadId": threadID, "input": input}
	if effort != "" {
		turn["effort"] = effort
	}
	var startedTurn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := p.call(ctx, "turn/start", turn, &startedTurn); err != nil {
		return providers.GenerateResponse{}, err
	}
	return p.collect(ctx, threadID, startedTurn.Turn.ID, events, aliases.RuntimeByWire, providers.HistoricalToolCallIDs(request.Messages))
}

// collect reads the turn until it completes. The first tool call interrupts it: the caller runs
// the tool and sends a new Generate with the result.
func (p *codexProcess) collect(ctx context.Context, threadID, turnID string, events chan rpcMessage, runtimeByWire map[string]string, historyIDs map[string]bool) (providers.GenerateResponse, error) {
	var content *string
	var usage *providers.Usage
	calls := []providers.ToolCall{}
	failure := ""
	fatalError := false
	interrupt := map[string]string{"threadId": threadID, "turnId": turnID}
	for {
		var event rpcMessage
		select {
		case event = <-events:
		case <-ctx.Done():
			p.notify("turn/interrupt", interrupt)
			return providers.GenerateResponse{}, ctx.Err()
		case <-p.exited:
			return providers.GenerateResponse{}, p.err()
		}
		switch event.Method {
		case "item/tool/call":
			var call struct {
				CallID    string          `json:"callId"`
				Tool      string          `json:"tool"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(event.Params, &call) != nil {
				return providers.GenerateResponse{}, ErrWebHarnessResponse
			}
			if len(calls) == 0 {
				p.notify("turn/interrupt", interrupt)
			}
			_ = p.write(rpcMessage{ID: event.ID, Result: json.RawMessage(`{"contentItems":[{"type":"inputText","text":"The caller runs this function and continues in a new request."}],"success":false}`)})
			calls = append(calls, providers.ToolCall{ID: call.CallID, Name: runtimeByWire[call.Tool], Arguments: call.Arguments})
		case "item/completed":
			var completed struct {
				Item struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"item"`
			}
			if json.Unmarshal(event.Params, &completed) == nil && completed.Item.Type == "agentMessage" {
				text := completed.Item.Text
				content = &text
			}
		case "thread/tokenUsage/updated":
			usage = codexUsage(event.Params)
		case "error":
			var reported struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
				WillRetry bool `json:"willRetry"`
			}
			if json.Unmarshal(event.Params, &reported) != nil {
				return providers.GenerateResponse{}, ErrWebHarnessResponse
			}
			if !reported.WillRetry {
				fatalError = true
				failure = reported.Error.Message
			}
		case "turn/completed":
			var completed struct {
				Turn struct {
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
			}
			if json.Unmarshal(event.Params, &completed) != nil {
				return providers.GenerateResponse{}, ErrWebHarnessResponse
			}
			if completed.Turn.Error != nil {
				fatalError = true
				failure = completed.Turn.Error.Message
			}
			if fatalError || completed.Turn.Status == "failed" {
				return providers.GenerateResponse{}, harnessFailure(failure)
			}
			if completed.Turn.Status != "completed" && !(completed.Turn.Status == "interrupted" && len(calls) > 0) {
				return providers.GenerateResponse{}, ErrWebHarnessResponse
			}
			return codexResponse(content, calls, usage, historyIDs)
		}
	}
}

func codexResponse(content *string, calls []providers.ToolCall, usage *providers.Usage, historyIDs map[string]bool) (providers.GenerateResponse, error) {
	if usage != nil && (usage.InputTokens != nil && usage.CachedTokens != nil && *usage.CachedTokens > *usage.InputTokens) {
		return providers.GenerateResponse{}, ErrWebHarnessResponse
	}
	if len(calls) == 0 {
		if content == nil {
			return providers.GenerateResponse{}, ErrWebHarnessResponse
		}
		return providers.GenerateResponse{Content: content, ToolCalls: calls, FinishReason: "stop", Usage: usage}, nil
	}
	seen := make(map[string]bool, len(historyIDs)+len(calls))
	for id := range historyIDs {
		seen[id] = true
	}
	for index := range calls {
		call := &calls[index]
		if providers.ValidateToolCall(*call) != nil || seen[call.ID] {
			return providers.GenerateResponse{}, ErrWebHarnessResponse
		}
		seen[call.ID] = true
		var object map[string]json.RawMessage
		_ = json.Unmarshal(call.Arguments, &object)
		call.Arguments, _ = json.Marshal(object)
	}
	return providers.GenerateResponse{ToolCalls: calls, FinishReason: "tool_call", Usage: usage}, nil
}

// codexUsage reads the thread total: the thread is fresh, so it covers every model call of this
// Generate, internal web-search steps included.
func codexUsage(params json.RawMessage) *providers.Usage {
	var update struct {
		TokenUsage struct {
			Total struct {
				TotalTokens       *int `json:"totalTokens"`
				InputTokens       *int `json:"inputTokens"`
				CachedInputTokens *int `json:"cachedInputTokens"`
				OutputTokens      *int `json:"outputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(params, &update) != nil {
		return nil
	}
	total := update.TokenUsage.Total
	for _, count := range []*int{total.TotalTokens, total.InputTokens, total.CachedInputTokens, total.OutputTokens} {
		if count != nil && *count < 0 {
			return nil
		}
	}
	return &providers.Usage{InputTokens: total.InputTokens, OutputTokens: total.OutputTokens, CachedTokens: total.CachedInputTokens, TotalTokens: total.TotalTokens}
}

// codexHistory splits a Generate into the thread's base instructions, the Responses API items
// injected as prior history, and the new turn input: the trailing user messages, or nothing
// when the request ends with tool results.
func codexHistory(messages []providers.Message, wireByRuntime map[string]string) (string, []map[string]any, []map[string]any) {
	instructions := []string{codexInstructions}
	end := len(messages)
	for end > 0 && messages[end-1].Role == "user" {
		end--
	}
	history := make([]map[string]any, 0, end)
	text := func(kind, value string) []map[string]any { return []map[string]any{{"type": kind, "text": value}} }
	for _, message := range messages[:end] {
		content := ""
		if message.Content != nil {
			content = *message.Content
		}
		switch message.Role {
		case "system":
			if len(history) == 0 {
				instructions = append(instructions, content)
				continue
			}
			history = append(history, map[string]any{"type": "message", "role": "developer", "content": text("input_text", content)})
		case "user":
			history = append(history, map[string]any{"type": "message", "role": "user", "content": text("input_text", content)})
		case "assistant":
			if message.Content != nil {
				history = append(history, map[string]any{"type": "message", "role": "assistant", "content": text("output_text", content)})
			}
			for _, call := range message.ToolCalls {
				history = append(history, map[string]any{"type": "function_call", "call_id": call.ID, "name": wireByRuntime[call.Name], "arguments": string(call.Arguments)})
			}
		case "tool":
			history = append(history, map[string]any{"type": "function_call_output", "call_id": *message.ToolCallID, "output": content})
		}
	}
	input := make([]map[string]any, 0, len(messages)-end)
	for _, message := range messages[end:] {
		input = append(input, map[string]any{"type": "text", "text": *message.Content})
	}
	return strings.Join(instructions, "\n\n"), history, input
}
