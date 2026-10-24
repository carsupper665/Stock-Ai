package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"stock-ai/agent-server/internal/server"
	"stock-ai/agent-server/internal/tools"
)

// The Session conversation is every message the model has seen in a Session, minus the system
// prompt (rebuilt on each Run). Each Run appends its wake-up message and continues where the
// previous Run ended or was interrupted. What is stored keeps meaning, not a transcript:
// read-tool outputs become short digests, repeated identical failures collapse into one, and
// rejected final responses with their repair instructions are dropped.

const (
	conversationSchema = `CREATE TABLE IF NOT EXISTS session_conversations (
		session_id TEXT PRIMARY KEY, messages_json TEXT NOT NULL, run_id INTEGER NOT NULL, updated_at TEXT NOT NULL
	)`
	terminalInstructionPrefix = "Terminal instruction ("
	digestRunes               = 300
	interruptedToolMessage    = "The Run was interrupted before this call's result was recorded; it may have executed. Check the current state with read tools before acting on it."
)

// wakeKeys are the parts of an earlier Run's wake-up message that still mean something later;
// tool lists, memory indexes and recalled memories are re-sent with every wake-up.
var wakeKeys = []string{"trigger", "triggers", "current_time", "account_snapshot", "inbox", "restart_context", "recovery_context", "retry_context"}

type sessionConversation struct {
	Messages []gatewayMessage `json:"messages"`
	RunID    int64            `json:"run_id"`
	Tokens   int              `json:"estimated_tokens"`
	Window   *int64           `json:"context_window"`
	Updated  string           `json:"updated_at"`
}

// serveConversation shows the Session conversation the next Run continues; context_window is
// null when the model catalog cannot be read.
func (r *AgentRuntime) serveConversation(w http.ResponseWriter, request *http.Request, sessionID string) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	session, err := r.sessionByID(request.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session conversation")
		return
	}
	view, err := r.conversation(sessionID)
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session conversation")
		return
	}
	if model, err := r.resolveModel(request.Context(), session.ModelName, session.ModelLevel); err == nil {
		window := model.ContextWindow
		if window <= 0 {
			window = defaultContextWindow
		}
		view.Window = &window
	}
	writeRuntimeJSON(w, http.StatusOK, view)
}

// loadConversation returns the stored conversation and the Run that last wrote it.
func (r *AgentRuntime) loadConversation(sessionID string) ([]gatewayMessage, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var encoded string
	var runID int64
	err := r.db.QueryRow(`SELECT messages_json, run_id FROM session_conversations WHERE session_id = ?`, sessionID).Scan(&encoded, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var messages []gatewayMessage
	if err := json.Unmarshal([]byte(encoded), &messages); err != nil {
		return nil, 0, err
	}
	return completeToolResults(messages), runID, nil
}

// saveConversation stores the semantic form of messages (without the system prompt) while the
// Run is still running, so a stopped or superseded Run cannot overwrite a newer conversation.
func (r *AgentRuntime) saveConversation(sessionID string, runID int64, messages []gatewayMessage) (bool, error) {
	encoded, err := json.Marshal(semanticConversation(messages))
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.db.Exec(`INSERT INTO session_conversations (session_id, messages_json, run_id, updated_at)
		SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM runs WHERE session_id = ? AND run_id = ? AND status = 'running')
		ON CONFLICT(session_id) DO UPDATE SET messages_json = excluded.messages_json, run_id = excluded.run_id, updated_at = excluded.updated_at`,
		sessionID, string(encoded), runID, time.Now().UTC().Format(time.RFC3339Nano), sessionID, runID)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

func (r *AgentRuntime) conversation(sessionID string) (sessionConversation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var encoded string
	view := sessionConversation{Messages: []gatewayMessage{}}
	err := r.db.QueryRow(`SELECT messages_json, run_id, updated_at FROM session_conversations WHERE session_id = ?`, sessionID).
		Scan(&encoded, &view.RunID, &view.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	if err := json.Unmarshal([]byte(encoded), &view.Messages); err != nil {
		return view, err
	}
	view.Tokens = estimateTokens(view.Messages)
	return view, nil
}

// completeToolResults gives every tool call without a recorded result an explicit unknown
// outcome, so a conversation cut by a restart is valid and the model re-checks state instead of
// assuming an order was or was not placed.
func completeToolResults(messages []gatewayMessage) []gatewayMessage {
	completed := make([]gatewayMessage, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		completed = append(completed, message)
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		answered := map[string]bool{}
		for index+1 < len(messages) && messages[index+1].Role == "tool" {
			index++
			completed = append(completed, messages[index])
			if messages[index].ToolCallID != nil {
				answered[*messages[index].ToolCallID] = true
			}
		}
		for _, call := range message.ToolCalls {
			if answered[call.ID] {
				continue
			}
			content := string(tools.ErrorResult("RUN_INTERRUPTED", interruptedToolMessage, "unknown"))
			callID := call.ID
			completed = append(completed, gatewayMessage{Role: "tool", Content: &content, ToolCallID: &callID})
		}
	}
	return completed
}

func isTerminalInstruction(message gatewayMessage) bool {
	return message.Role == "user" && message.Content != nil && strings.HasPrefix(*message.Content, terminalInstructionPrefix)
}

// isWakeMessage recognizes a Run's wake-up message: the JSON object every Run starts with.
func isWakeMessage(message gatewayMessage) bool {
	if message.Role != "user" || message.Content == nil || !strings.HasPrefix(*message.Content, "{") {
		return false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal([]byte(*message.Content), &body) != nil {
		return false
	}
	_, hasTrigger := body["trigger"]
	_, hasTime := body["current_time"]
	return hasTrigger && hasTime
}

// isRejectedFinal is an assistant answer without tool calls that a repair instruction followed.
func isRejectedFinal(messages []gatewayMessage, index int) bool {
	return messages[index].Role == "assistant" && len(messages[index].ToolCalls) == 0 &&
		index+1 < len(messages) && isTerminalInstruction(messages[index+1])
}

// semanticConversation keeps what each step meant. It is idempotent, so stored conversations
// can pass through it again with the next Run appended.
func semanticConversation(messages []gatewayMessage) []gatewayMessage {
	names := map[string]string{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			names[call.ID] = call.Name
		}
	}
	kept := make([]gatewayMessage, 0, len(messages))
	var lastFailure string
	lastFailureResult := -1
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		switch {
		case isTerminalInstruction(message) || isRejectedFinal(messages, index):
			continue
		case isWakeMessage(message):
			message.Content = shrinkWake(*message.Content)
		case message.Role == "tool" && message.ToolCallID != nil && message.Content != nil:
			digest := digestToolResult(names[*message.ToolCallID], *message.Content)
			message.Content = &digest
		}
		// A lone tool call failing exactly like the previous one is folded into that one.
		if message.Role == "assistant" && len(message.ToolCalls) == 1 && index+1 < len(messages) && messages[index+1].Role == "tool" {
			key := failureKey(message.ToolCalls[0].Name, messages[index+1])
			if key != "" && key == lastFailure && lastFailureResult >= 0 {
				kept[lastFailureResult].Content = addRepeat(*kept[lastFailureResult].Content, *messages[index+1].Content)
				index++
				continue
			}
			kept = append(kept, message)
			result := messages[index+1]
			digest := digestToolResult(message.ToolCalls[0].Name, *result.Content)
			result.Content = &digest
			kept = append(kept, result)
			lastFailure, lastFailureResult = key, len(kept)-1
			if key == "" {
				lastFailureResult = -1
			}
			index++
			continue
		}
		lastFailure, lastFailureResult = "", -1
		kept = append(kept, message)
	}
	return kept
}

// failureKey identifies an error result by tool and error; successes have no key.
func failureKey(name string, result gatewayMessage) string {
	if result.Content == nil {
		return ""
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(*result.Content), &envelope) != nil || envelope.OK || envelope.Error == nil {
		return ""
	}
	return name + "\x00" + envelope.Error.Code + "\x00" + envelope.Error.Message
}

// digestToolResult keeps mutation and skill results whole, errors as code and message, and
// reduces read outputs to a short digest the model can re-query for exact numbers.
func digestToolResult(name, content string) string {
	if tools.IsMutation(name) || name == "skill_get" {
		return content
	}
	var value map[string]any
	if json.Unmarshal([]byte(content), &value) != nil {
		encoded, _ := json.Marshal(map[string]any{"digest": truncateRunes(content, digestRunes)})
		return string(encoded)
	}
	if _, done := value["digest"]; done {
		return content
	}
	if _, repeated := value["repeated"]; repeated {
		return content
	}
	if value["ok"] == false {
		envelope := map[string]any{"ok": false}
		if detail, ok := value["error"].(map[string]any); ok {
			message, _ := detail["message"].(string)
			envelope["error"] = map[string]any{"code": detail["code"], "message": truncateRunes(message, digestRunes)}
		}
		encoded, _ := json.Marshal(envelope)
		return string(encoded)
	}
	evidence, _ := json.Marshal(compactJSONValue(value["result"], 2))
	encoded, _ := json.Marshal(map[string]any{"ok": true, "digest": truncateRunes(string(evidence), digestRunes)})
	return string(encoded)
}

func shrinkWake(content string) *string {
	var body map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &body) != nil {
		return &content
	}
	kept := map[string]json.RawMessage{}
	for _, key := range wakeKeys {
		if value, ok := body[key]; ok {
			kept[key] = value
		}
	}
	encoded, _ := json.Marshal(kept)
	shrunk := string(encoded)
	return &shrunk
}

// addRepeat counts a folded failure on the kept result; either side may already carry a count.
func addRepeat(kept, folded string) *string {
	count := func(content string) float64 {
		var value map[string]any
		_ = json.Unmarshal([]byte(content), &value)
		if repeated, ok := value["repeated"].(float64); ok {
			return repeated
		}
		return 1
	}
	var value map[string]any
	_ = json.Unmarshal([]byte(kept), &value)
	value["repeated"] = count(kept) + count(folded)
	encoded, _ := json.Marshal(value)
	noted := string(encoded)
	return &noted
}
