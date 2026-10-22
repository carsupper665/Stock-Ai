package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"stock-ai/agent-server/internal/history"
	"stock-ai/agent-server/internal/tools"
)

const (
	maxGatewayRequestBytes = 1 << 20
	maxModelMessageBytes   = 768 << 10
	compactStringRunes     = 512

	defaultContextWindow = 128000
	// Compaction starts when the estimate reaches 80% of the window and aims for 50%: earlier
	// Runs fold first, the current Run's earlier steps only when that is not enough. The LLM
	// pass runs on a folded history that alone still takes 20%, since it cannot shrink the
	// system prompt or the wake-up message.
	compactTriggerRatio    = 0.80
	compactTargetRatio     = 0.50
	llmCompactRatio        = 0.20
	compactArgumentRunes   = 160
	compactAssistantRunes  = 1000
	compactedHistoryHeader = "[Context compacted] Earlier conversation of this Session. Tool outputs were removed; re-query tools if exact data is needed."
	compactionInstruction  = "Summarize the earlier conversation of a trading agent Session so the agent can continue it. " +
		"Keep decisions made, key numbers learned, every order/position/event id with its status, uncertain outcomes, and pending plans. " +
		"Drop tool output detail, retries, and errors. Reply with plain text under 1000 words."
)

type gatewayContinuation struct {
	AfterMessages int              `json:"after_messages"`
	Messages      []gatewayMessage `json:"messages"`
}

type continuationState struct {
	request   []gatewayMessage
	assistant gatewayMessage
}

type modelContextProjection struct {
	Messages       []gatewayMessage
	OriginalBytes  int
	ProjectedBytes int
	Compacted      int
}

func runtimeToolNames(skillsEnabled bool) []string {
	definitions := tools.Definitions(skillsEnabled)
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		name, _ := definition["name"].(string)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func toolDisclosureInstruction(skillsEnabled bool) string {
	return "\n\nThe available tool names are exactly: " + strings.Join(runtimeToolNames(skillsEnabled), ", ") + ". Use only these names through the structured tool channel."
}

// modelTools returns the tool definitions sent with a model call: none while finalizing.
func modelTools(session Session, toolsEnabled bool) []map[string]any {
	if !toolsEnabled {
		return []map[string]any{}
	}
	return tools.Definitions(session.SkillsEnabled)
}

func projectModelContext(messages []gatewayMessage) (modelContextProjection, error) {
	original, err := json.Marshal(messages)
	if err != nil {
		return modelContextProjection{}, err
	}
	projection := modelContextProjection{
		Messages: cloneGatewayMessages(messages), OriginalBytes: len(original), ProjectedBytes: len(original),
	}
	if projection.ProjectedBytes <= maxModelMessageBytes {
		return projection, nil
	}

	toolNames := make(map[string]string)
	readResults := make([]int, 0)
	mutationResults := make([]int, 0)
	assistantContent := make([]int, 0)
	for index, message := range projection.Messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				toolNames[call.ID] = call.Name
			}
			if index > 1 && message.Content != nil && len(*message.Content) > 4096 {
				assistantContent = append(assistantContent, index)
			}
			continue
		}
		if message.Role != "tool" || message.ToolCallID == nil {
			continue
		}
		if tools.IsMutation(toolNames[*message.ToolCallID]) {
			mutationResults = append(mutationResults, index)
		} else {
			readResults = append(readResults, index)
		}
	}

	for _, index := range readResults {
		if projection.ProjectedBytes <= maxModelMessageBytes {
			break
		}
		message := &projection.Messages[index]
		name := toolNames[*message.ToolCallID]
		if replaceMessageContent(message, compactToolContent(name, *message.Content, false)) {
			projection.Compacted++
			projection.ProjectedBytes = encodedMessageBytes(projection.Messages)
		}
	}
	for _, index := range assistantContent {
		if projection.ProjectedBytes <= maxModelMessageBytes {
			break
		}
		message := &projection.Messages[index]
		replacement := fmt.Sprintf("[older assistant content compacted; original_utf8_bytes=%d]", len(*message.Content))
		if replaceMessageContent(message, replacement) {
			projection.Compacted++
			projection.ProjectedBytes = encodedMessageBytes(projection.Messages)
		}
	}
	for _, index := range mutationResults {
		if projection.ProjectedBytes <= maxModelMessageBytes {
			break
		}
		message := &projection.Messages[index]
		name := toolNames[*message.ToolCallID]
		if replaceMessageContent(message, compactToolContent(name, *message.Content, true)) {
			projection.Compacted++
			projection.ProjectedBytes = encodedMessageBytes(projection.Messages)
		}
	}
	if projection.ProjectedBytes > maxModelMessageBytes {
		return projection, errors.New("mandatory and recent Run context exceeds the Gateway request bound")
	}
	return projection, nil
}

func cloneGatewayMessages(messages []gatewayMessage) []gatewayMessage {
	cloned := make([]gatewayMessage, len(messages))
	copy(cloned, messages)
	for index := range cloned {
		cloned[index].ToolCalls = append([]gatewayTool(nil), messages[index].ToolCalls...)
	}
	return cloned
}

func encodedMessageBytes(messages []gatewayMessage) int {
	encoded, _ := json.Marshal(messages)
	return len(encoded)
}

func replaceMessageContent(message *gatewayMessage, replacement string) bool {
	if message.Content == nil || replacement == *message.Content || len(replacement) >= len(*message.Content) {
		return false
	}
	message.Content = &replacement
	return true
}

func compactToolContent(name, content string, mutation bool) string {
	raw := json.RawMessage(content)
	if name == "skill_get" {
		return content
	}
	if name == "get_ohlcv" {
		return string(history.FilterToolResult(name, raw))
	}
	if name == "get_run_history" {
		return string(history.FilterToolResult(name, raw))
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return fmt.Sprintf(`{"context_compacted":true,"tool":%q,"original_utf8_bytes":%d}`, name, len(content))
	}
	if envelope, ok := value.(map[string]any); ok && envelope["ok"] == false {
		encoded, _ := json.Marshal(compactJSONValue(envelope, 0))
		return string(encoded)
	}
	projected := map[string]any{
		"context_compacted":   true,
		"tool":                name,
		"original_utf8_bytes": len(content),
		"evidence":            compactJSONValue(value, 0),
	}
	if mutation {
		projected["mutation_evidence_preserved"] = true
	}
	encoded, _ := json.Marshal(projected)
	return string(encoded)
}

func compactJSONValue(value any, depth int) any {
	if depth >= 4 {
		return "[nested value compacted]"
	}
	switch typed := value.(type) {
	case string:
		if utf8.RuneCountInString(typed) <= compactStringRunes {
			return typed
		}
		runes := []rune(typed)
		return string(runes[:compactStringRunes]) + "…"
	case []any:
		if len(typed) <= 2 {
			items := make([]any, 0, len(typed))
			for _, item := range typed {
				items = append(items, compactJSONValue(item, depth+1))
			}
			return items
		}
		return map[string]any{
			"item_count": len(typed), "items_omitted": len(typed) - 2,
			"items_sample": []any{compactJSONValue(typed[0], depth+1), compactJSONValue(typed[len(typed)-1], depth+1)},
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 32 {
			keys = keys[:32]
		}
		result := make(map[string]any, len(keys)+1)
		for _, key := range keys {
			result[key] = compactJSONValue(typed[key], depth+1)
		}
		if len(keys) < len(typed) {
			result["fields_omitted"] = len(typed) - len(keys)
		}
		return result
	default:
		return value
	}
}

func continuationFor(state *continuationState, messages []gatewayMessage) *gatewayContinuation {
	if state == nil || len(state.request) == 0 || len(messages) <= len(state.request) ||
		!reflect.DeepEqual(messages[:len(state.request)], state.request) ||
		!reflect.DeepEqual(messages[len(state.request)], state.assistant) {
		return nil
	}
	delta := messages[len(state.request)+1:]
	if len(delta) == 0 {
		return nil
	}
	return &gatewayContinuation{AfterMessages: len(state.request), Messages: cloneGatewayMessages(delta)}
}

func acknowledgeContinuation(state *continuationState, request []gatewayMessage, response gatewayResponse) {
	if state == nil {
		return
	}
	state.request = cloneGatewayMessages(request)
	state.assistant = gatewayMessage{Role: "assistant", Content: response.Content, ToolCalls: append([]gatewayTool(nil), response.ToolCalls...)}
}

// compactContext applies the two-stage context compaction when the estimated request reaches
// compactTriggerRatio of the model's window. It returns nil when nothing was compacted.
func (r *AgentRuntime) compactContext(ctx context.Context, session Session, runID int64, iteration int,
	model modelMapping, messages []gatewayMessage, toolDefinitions []map[string]any,
) ([]gatewayMessage, *traceEntry) {
	window := model.ContextWindow
	if window <= 0 {
		window = defaultContextWindow
	}
	before := estimateContextTokens(messages, toolDefinitions)
	if float64(before) < compactTriggerRatio*float64(window) {
		return messages, nil
	}
	compacted, blocks := compactConversation(messages, func(candidate []gatewayMessage) bool {
		return float64(estimateContextTokens(candidate, toolDefinitions)) < compactTargetRatio*float64(window)
	})
	if len(blocks) == 0 {
		return messages, nil
	}
	started := time.Now()
	entry := traceEntry{Type: "context_compaction", ToolName: "context_compaction", Iteration: iteration,
		StartedAt: started.UTC().Format(time.RFC3339Nano)}
	stage := "rules"
	for _, index := range blocks {
		if float64(estimateTokens(compacted[index])) < llmCompactRatio*float64(window) {
			continue
		}
		summary, response, err := r.summarizeHistory(ctx, session, runID, model, *compacted[index].Content)
		if validGatewayUsage(response.Usage) {
			entry.Usage = response.Usage
		}
		if err != nil {
			entry.Error = err.Error()
		} else {
			stage = "llm"
			compacted[index].Content = &summary
			entry.Content = &summary
		}
	}
	entry.Result = tools.SuccessResult(map[string]any{"stage": stage, "context_window": window,
		"tokens_before": before, "tokens_after": estimateContextTokens(compacted, toolDefinitions)})
	finishTraceTiming(&entry, started, time.Now())
	return compacted, &entry
}

// summarizeHistory is the one-shot LLM pass. A failed, empty, or longer summary is rejected so
// the caller keeps the rule-compacted history.
func (r *AgentRuntime) summarizeHistory(ctx context.Context, session Session, runID int64, model modelMapping, history string) (string, gatewayResponse, error) {
	instruction := compactionInstruction
	messages := []gatewayMessage{{Role: "system", Content: &instruction}, {Role: "user", Content: &history}}
	conversationID := session.ID + "/" + strconv.FormatInt(runID, 10) + "/compaction"
	response, _, _, err := r.generate(ctx, model, session.ModelLevel, conversationID, messages, []map[string]any{}, nil, nil)
	if err != nil {
		return "", response, err
	}
	if response.Content == nil || strings.TrimSpace(*response.Content) == "" {
		return "", response, errors.New("compaction summary is empty")
	}
	summary := compactedHistoryHeader + "\n" + strings.TrimSpace(*response.Content)
	if len(summary) >= len(history) {
		return "", response, errors.New("compaction summary is not shorter than the compacted history")
	}
	return summary, response, nil
}

func estimateContextTokens(messages []gatewayMessage, toolDefinitions []map[string]any) int {
	return estimateTokens(messages) + estimateTokens(toolDefinitions)
}

// estimateTokens approximates tokens without a tokenizer: ASCII JSON averages about four bytes
// per token, while CJK text is closer to one token per character.
func estimateTokens(value any) int {
	encoded, _ := json.Marshal(value)
	ascii, other := 0, 0
	for _, char := range string(encoded) {
		if char < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return ascii/4 + other
}

// compactConversation keeps the system prompt, the current Run's wake-up message, and the tail
// from the last assistant message verbatim. Everything before the wake-up message folds into one
// compacted message first; the current Run's steps before the tail fold too only when enough
// reports that is still over budget. Tool/assistant pairs in the tail stay intact, so no result
// is orphaned. It returns the indexes of the compacted messages it wrote.
func compactConversation(messages []gatewayMessage, enough func([]gatewayMessage) bool) ([]gatewayMessage, []int) {
	wake := 1
	for index := len(messages) - 1; index > 0; index-- {
		if isWakeMessage(messages[index]) {
			wake = index
			break
		}
	}
	compacted := messages
	blocks := make([]int, 0, 2)
	if wake > 1 && !(wake == 2 && isCompactedHistory(messages[1])) {
		compacted = append([]gatewayMessage{messages[0], foldMessages(messages[1:wake])}, messages[wake:]...)
		blocks = append(blocks, 1)
		wake = 2
		if enough(compacted) {
			return compacted, blocks
		}
	}
	tail := 0
	for index := len(compacted) - 1; index > wake+1; index-- {
		if compacted[index].Role == "assistant" {
			tail = index
			break
		}
	}
	if tail == 0 || (tail == wake+2 && isCompactedHistory(compacted[wake+1])) {
		return compacted, blocks
	}
	folded := foldMessages(compacted[wake+1 : tail])
	result := append(append(append([]gatewayMessage{}, compacted[:wake+1]...), folded), compacted[tail:]...)
	return result, append(blocks, wake+1)
}

// foldMessages renders messages as one compacted history message with a semantic line per step.
func foldMessages(messages []gatewayMessage) gatewayMessage {
	results := make(map[string]string)
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID != nil && message.Content != nil {
			results[*message.ToolCallID] = *message.Content
		}
	}
	lines := make([]string, 0)
	seen := make(map[string]bool)
	for index, message := range messages {
		if isCompactedHistory(message) {
			if earlier := strings.TrimSpace(strings.TrimPrefix(*message.Content, compactedHistoryHeader)); earlier != "" {
				lines = append(lines, earlier)
				for _, line := range strings.Split(earlier, "\n") {
					seen[line] = true
				}
			}
			continue
		}
		if isWakeMessage(message) {
			lines = append(lines, "wake: "+truncateRunes(*shrinkWake(*message.Content), compactAssistantRunes))
			continue
		}
		if isChatMention(message) {
			lines = append(lines, "chat: "+truncateRunes(strings.TrimPrefix(*message.Content, chatMentionHeader+"\n"), compactAssistantRunes))
			continue
		}
		// Rejected final responses and their repair instructions are retries and are dropped; an
		// accepted final response is how an earlier Run ended and is kept.
		if isRejectedFinal(messages, index) || message.Role != "assistant" {
			continue
		}
		if message.Content != nil && strings.TrimSpace(*message.Content) != "" {
			lines = append(lines, "assistant: "+truncateRunes(strings.TrimSpace(*message.Content), compactAssistantRunes))
		}
		for _, call := range message.ToolCalls {
			if line, keep := compactToolCallLine(call, results[call.ID], seen); keep {
				lines = append(lines, line)
			}
		}
	}
	history := compactedHistoryHeader
	if len(lines) != 0 {
		history += "\n" + strings.Join(lines, "\n")
	}
	return gatewayMessage{Role: "user", Content: &history}
}

func isCompactedHistory(message gatewayMessage) bool {
	return message.Role == "user" && message.Content != nil && strings.HasPrefix(*message.Content, compactedHistoryHeader)
}

// compactToolCallLine drops read outputs, errors, and repeated reads. Mutation results and
// mutations with an unknown outcome keep a compact result: losing an order or position id
// could make the model repeat the order.
func compactToolCallLine(call gatewayTool, result string, seen map[string]bool) (string, bool) {
	line := strings.TrimSpace("- " + call.Name + " " + truncateRunes(string(call.Arguments), compactArgumentRunes))
	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Outcome string `json:"outcome"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(result), &envelope)
	// A skill is the playbook the rest of the Run follows, so its text survives compaction.
	if call.Name == "skill_get" && envelope.OK && !seen[line] {
		seen[line] = true
		return line + " -> " + result, true
	}
	if !tools.IsMutation(call.Name) {
		if !envelope.OK || seen[line] {
			return "", false
		}
		seen[line] = true
		return line, true
	}
	if !envelope.OK && (envelope.Error == nil || envelope.Error.Outcome != "unknown") {
		return "", false
	}
	var value any
	_ = json.Unmarshal([]byte(result), &value)
	encoded, _ := json.Marshal(compactJSONValue(value, 0))
	return line + " -> " + string(encoded), true
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}
