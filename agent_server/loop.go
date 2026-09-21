package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"stock-ai/agent-server/internal/history"
	"stock-ai/agent-server/internal/memory"
	"stock-ai/agent-server/internal/tools"
)

func (r *AgentRuntime) runAgent(ctx context.Context, session Session, trigger string, runID int64, active *activeRun) {
	defer r.finishActive(session.ID, active)
	contextBody := map[string]any{
		"trigger": trigger, "session_name": session.Name, "account_id": session.AccountID,
		"model_name": session.ModelName, "model_level": session.ModelLevel,
		"max_loop": session.MaxLoop, "max_tool_call": session.MaxToolCall,
	}
	accountContext, cancelAccount := context.WithTimeout(ctx, r.config.RequestTimeout)
	account, accountErr := r.fetchAccount(accountContext, session.AccountID)
	cancelAccount()
	if ctx.Err() != nil {
		return
	}
	if accountErr != nil {
		contextBody["account_snapshot"] = map[string]any{"ok": false, "error": map[string]string{
			"code": dependencyReasonCode(accountErr), "message": dependencyReason(accountErr),
		}}
	} else {
		contextBody["account_snapshot"] = map[string]any{
			"ok": true, "id": account.ID, "user_name": account.UserName, "status": account.Status,
			"initial_balance": account.InitialBalance, "balance": account.Balance,
		}
	}
	if trigger == "session_restart" && len(session.RestartContext) != 0 && string(session.RestartContext) != "null" {
		restartContext, err := normalizeRestartContext(session.RestartContext)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["restart_context"] = restartContext
	}
	if trigger == "event" {
		triggers, err := r.runTriggers(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["triggers"] = triggers
	}
	if trigger == "server_recovery" {
		recovery, err := r.recoveryContext(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["recovery_context"] = recovery
	}
	if trigger == "manual_retry" {
		retryContext, err := r.manualRetryContext(ctx, session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["retry_context"] = retryContext
	}
	var recentSummary *memory.RecentRun
	var memoryIndex []memory.Metadata
	var err error
	if trigger == "server_recovery" {
		memoryIndex, err = memory.List(ctx, r.db, session.ID, "", tools.MemoryDefaultLimit)
	} else {
		recentSummary, memoryIndex, err = memory.ProgressiveContext(ctx, r.db, session.ID, runID)
	}
	if err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return
	}
	if recentSummary != nil {
		contextBody["recent_run_summary"] = recentSummary
	}
	if len(memoryIndex) != 0 {
		contextBody["memory_index"] = memoryIndex
	}
	contextBody["available_tools"] = runtimeToolNames()
	encodedContext, _ := json.Marshal(contextBody)
	systemPrompt := session.Prompt + toolDisclosureInstruction() + memory.Instruction
	userPrompt := string(encodedContext)
	messages := []gatewayMessage{{Role: "system", Content: &systemPrompt}, {Role: "user", Content: &userPrompt}}
	progress := make([]traceEntry, 0)
	modelCalls, toolCalls, decisions := 0, 0, 0
	finalizing, finalizationAttempts := false, 0
	continuation := &continuationState{}
	if updated, err := r.persistInitialContext(session.ID, runID, encodedContext); err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return
	} else if !updated {
		return
	}
	persistInstruction := func(instruction string, attempt int) bool {
		progress = append(progress, traceEntry{Type: "instruction", AttemptType: "terminal", Attempt: attempt, Content: &instruction})
		updated, err := r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return false
		}
		return updated
	}
	persistDeniedTool := func(call gatewayTool, result json.RawMessage) bool {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		progress = append(progress, traceEntry{Type: "tool_denied", AttemptType: "terminal", Iteration: decisions,
			ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, Result: result, StartedAt: now, EndedAt: &now})
		updated, err := r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return false
		}
		return updated
	}
	for {
		if ctx.Err() != nil {
			return
		}
		running, err := r.runIsRunning(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		if !running {
			return
		}
		if !finalizing && (decisions >= session.MaxLoop || toolCalls >= session.MaxToolCall) {
			finalizing = true
			reason := "max_loop reached"
			if toolCalls >= session.MaxToolCall {
				reason = "max_tool_call reached"
			}
			instruction := terminalFinalizationPrompt(reason)
			messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
			if !persistInstruction(instruction, finalizationAttempts+1) {
				return
			}
		}
		attemptLimit := maxModelGenerateRetries + 1
		iteration := decisions + 1
		toolsEnabled := !finalizing
		if finalizing {
			attemptLimit = maxFinalizationRetries + 1 - finalizationAttempts
			if attemptLimit <= 0 {
				r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: retry allowance exhausted")
				return
			}
		} else {
			decisions++
			iteration = decisions
		}
		attemptType := "decision"
		if finalizing {
			attemptType = "terminal"
		}
		attemptOffset := 0
		if finalizing {
			attemptOffset = finalizationAttempts
		}
		response, attempts, ok := r.callModel(ctx, session, runID, iteration, attemptType, messages, toolsEnabled,
			attemptLimit, attemptOffset, &progress, &modelCalls, toolCalls, continuation)
		if !ok {
			return
		}
		if finalizing {
			finalizationAttempts += attempts
		}
		if len(response.ToolCalls) == 0 {
			var final memory.Finalization
			var finalizationErr error
			if response.Content == nil || strings.TrimSpace(*response.Content) == "" || response.FinishReason != "stop" {
				finalizationErr = errors.New("final response requires nonempty content and stop finish_reason")
			} else {
				final, finalizationErr = memory.ParseFinalization(*response.Content)
			}
			if finalizationErr != nil {
				if !finalizing {
					finalizing = true
					finalizationAttempts = 1
					markLastModelCallTerminal(progress, iteration)
				}
				if finalizationAttempts >= maxFinalizationRetries+1 {
					r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: "+finalizationErr.Error())
					return
				}
				invalidContent := "[invalid empty final response]"
				if response.Content != nil && strings.TrimSpace(*response.Content) != "" {
					invalidContent = *response.Content
				}
				messages = append(messages, gatewayMessage{Role: "assistant", Content: &invalidContent})
				instruction := terminalFinalizationPrompt("the previous final response was invalid: " + finalizationErr.Error())
				messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
				if !persistInstruction(instruction, finalizationAttempts+1) {
					return
				}
				continue
			}
			completed, err := r.completeFinalizedRun(session.ID, runID, final)
			if err != nil {
				var referenceError *memory.ReferenceError
				if errors.As(err, &referenceError) {
					if !finalizing {
						finalizing = true
						finalizationAttempts = 1
						markLastModelCallTerminal(progress, iteration)
					}
					if finalizationAttempts >= maxFinalizationRetries+1 {
						r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: "+err.Error())
						return
					}
					messages = append(messages, gatewayMessage{Role: "assistant", Content: response.Content})
					instruction := terminalFinalizationPrompt("the previous memory references were invalid: " + referenceError.Error())
					messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
					if !persistInstruction(instruction, finalizationAttempts+1) {
						return
					}
					continue
				}
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !completed {
				return
			}
			return
		}
		messages = append(messages, gatewayMessage{Role: "assistant", Content: response.Content, ToolCalls: response.ToolCalls})
		if finalizing || response.FinishReason != "tool_call" {
			for _, call := range response.ToolCalls {
				result := tools.ErrorResult("TOOL_DISABLED", "tools are disabled during finalization", "not_executed")
				content := string(result)
				callID := call.ID
				messages = append(messages, gatewayMessage{Role: "tool", Content: &content, ToolCallID: &callID})
				if !persistDeniedTool(call, result) {
					return
				}
			}
			if !finalizing {
				finalizing = true
				finalizationAttempts = 1
				markLastModelCallTerminal(progress, iteration)
			}
			if finalizationAttempts >= maxFinalizationRetries+1 {
				r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: model returned tool calls during finalization")
				return
			}
			instruction := terminalFinalizationPrompt("tools are unavailable; return the final response now")
			messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
			if !persistInstruction(instruction, finalizationAttempts+1) {
				return
			}
			continue
		}
		for _, call := range response.ToolCalls {
			if ctx.Err() != nil {
				return
			}
			running, err = r.runIsRunning(session.ID, runID)
			if err != nil {
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !running {
				return
			}
			if toolCalls >= session.MaxToolCall {
				result := tools.ErrorResult("MAX_TOOL_CALL_REACHED", "tool-call budget is exhausted; finalize from existing evidence", "not_executed")
				content := string(result)
				callID := call.ID
				messages = append(messages, gatewayMessage{Role: "tool", Content: &content, ToolCallID: &callID})
				if !persistDeniedTool(call, result) {
					return
				}
				continue
			}
			toolCalls++
			toolStarted := time.Now()
			progress = append(progress, traceEntry{Type: "tool_call", Iteration: decisions,
				ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments,
				Result:    tools.ErrorResult("TOOL_INTERRUPTED", "tool result is not yet known", "unknown"),
				StartedAt: toolStarted.UTC().Format(time.RFC3339Nano)})
			updated, err := r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
			if err != nil {
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !updated {
				return
			}
			result := r.executeTool(ctx, session, runID, call)
			progress[len(progress)-1].Result = history.FilterToolResult(call.Name, result)
			finishTraceTiming(&progress[len(progress)-1], toolStarted, time.Now())
			updated, err = r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
			if err != nil {
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !updated {
				return
			}
			content := string(result)
			callID := call.ID
			messages = append(messages, gatewayMessage{Role: "tool", Content: &content, ToolCallID: &callID})
		}
	}
}

func terminalFinalizationPrompt(reason string) string {
	return "Terminal instruction (" + reason + "): do not call tools. Using only the evidence already in this Run, return the required finalization JSON now. Preserve uncertain tool outcomes as uncertain."
}

func (r *AgentRuntime) callModel(ctx context.Context, session Session, runID int64, iteration int, attemptType string,
	messages []gatewayMessage, toolsEnabled bool, attemptLimit, attemptOffset int, progress *[]traceEntry,
	modelCalls *int, toolCalls int, continuationState *continuationState,
) (gatewayResponse, int, bool) {
	if ctx.Err() != nil {
		return gatewayResponse{}, 0, false
	}
	running, err := r.runIsRunning(session.ID, runID)
	if err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return gatewayResponse{}, 0, false
	}
	if !running {
		return gatewayResponse{}, 0, false
	}
	reserved, err := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress)
	if err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return gatewayResponse{}, 0, false
	}
	if !reserved {
		return gatewayResponse{}, 0, false
	}
	modelContext, cancelModel := context.WithTimeout(ctx, r.config.RequestTimeout)
	model, err := r.resolveModel(modelContext, session.ModelName, session.ModelLevel)
	cancelModel()
	if err != nil {
		if ctx.Err() != nil {
			return gatewayResponse{}, 0, false
		}
		started := time.Now()
		ended := started.UTC().Format(time.RFC3339Nano)
		*progress = append(*progress, traceEntry{Type: "model_resolution", AttemptType: attemptType,
			Iteration: iteration, ModelName: session.ModelName, Error: dependencyReason(err),
			StartedAt: ended, EndedAt: &ended})
		if updated, updateErr := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress); updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
		} else if updated {
			r.persistRunFailure(session.ID, runID, dependencyReason(err))
		}
		return gatewayResponse{}, 0, false
	}
	projection, err := projectModelContext(messages)
	if err != nil {
		*progress = append(*progress, traceEntry{Type: "context_projection", AttemptType: attemptType,
			Iteration: iteration, Error: "CONTEXT_LIMIT_EXCEEDED", ContextOriginalBytes: projection.OriginalBytes,
			ContextProjectedBytes: projection.ProjectedBytes, ContextCompacted: projection.Compacted})
		if updated, updateErr := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress); updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
		} else if updated {
			r.persistRunFailure(session.ID, runID, "CONTEXT_LIMIT_EXCEEDED: mandatory and recent Run context exceeds the Gateway request bound")
		}
		return gatewayResponse{}, 0, false
	}
	for attempt := 1; attempt <= attemptLimit; attempt++ {
		if ctx.Err() != nil {
			return gatewayResponse{}, attempt - 1, false
		}
		running, err := r.runIsRunning(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return gatewayResponse{}, attempt - 1, false
		}
		if !running {
			return gatewayResponse{}, attempt - 1, false
		}
		(*modelCalls)++
		attemptNumber := attemptOffset + attempt
		started := time.Now()
		*progress = append(*progress, traceEntry{
			Type: "model_call", AttemptType: attemptType, Iteration: iteration, Attempt: attemptNumber, ModelName: session.ModelName,
			ProviderID: model.ProviderID, Model: model.Model,
			StartedAt: started.UTC().Format(time.RFC3339Nano), ConversationID: session.ID + "/" + strconv.FormatInt(runID, 10),
			ContextOriginalBytes: projection.OriginalBytes, ContextProjectedBytes: projection.ProjectedBytes,
			ContextCompacted: projection.Compacted,
		})
		updated, updateErr := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress)
		if updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
			return gatewayResponse{}, attempt, false
		}
		if !updated {
			return gatewayResponse{}, attempt, false
		}
		var continuation *gatewayContinuation
		if attempt == 1 {
			continuation = continuationFor(continuationState, projection.Messages)
		}
		response, requestBytes, dispatched, generateErr := r.generate(ctx, model, session.ModelLevel, session.ID+"/"+strconv.FormatInt(runID, 10), projection.Messages, toolsEnabled, continuation)
		entry := &(*progress)[len(*progress)-1]
		entry.RequestBytes = requestBytes
		if !dispatched {
			(*modelCalls)--
			entry.Type = "generate_dispatch"
			entry.Attempt = 0
		}
		finishTraceTiming(entry, started, time.Now())
		if generateErr != nil {
			entry.Error = generateErr.Error()
			entry.Content = response.Content
			entry.Reasoning = response.Reasoning
			entry.FinishReason = response.FinishReason
			entry.ToolCalls = response.ToolCalls
			if validGatewayDiagnostics(response.NumTurns, response.TotalCostUSD) {
				entry.NumTurns = response.NumTurns
				entry.TotalCostUSD = response.TotalCostUSD
			}
			if validContinuationDiagnostics(response.SessionMode, response.FallbackReason, response.InvocationCount) {
				entry.SessionMode = response.SessionMode
				entry.FallbackReason = response.FallbackReason
				entry.InvocationCount = response.InvocationCount
			}
			if validGatewayUsage(response.Usage) {
				entry.Usage = response.Usage
			}
		} else {
			entry.Content = response.Content
			entry.Reasoning = response.Reasoning
			entry.FinishReason = response.FinishReason
			entry.ToolCalls = response.ToolCalls
			entry.Usage = response.Usage
			entry.NumTurns = response.NumTurns
			entry.TotalCostUSD = response.TotalCostUSD
			entry.SessionMode = response.SessionMode
			entry.FallbackReason = response.FallbackReason
			entry.InvocationCount = response.InvocationCount
		}
		updated, updateErr = r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress)
		if updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
			return gatewayResponse{}, attempt, false
		}
		if !updated || ctx.Err() != nil {
			return gatewayResponse{}, attempt, false
		}
		if generateErr == nil {
			acknowledgeContinuation(continuationState, projection.Messages, response)
			if response.FallbackReason != nil {
				r.logOperational("gateway_fallback", session.ID, runID, *response.FallbackReason)
			}
			return response, attempt, true
		}
		if attempt == attemptLimit || !retryableGenerateError(generateErr) {
			r.logOperational("gateway_error", session.ID, runID, compactErrorCode(generateErr.Error()))
			r.persistRunFailure(session.ID, runID, "GATEWAY_ERROR: "+generateErr.Error())
			return gatewayResponse{}, attempt, false
		}
	}
	return gatewayResponse{}, attemptLimit, false
}

func dependencyReason(err error) string {
	var dependency *dependencyError
	if errors.As(err, &dependency) {
		return dependency.code + ": " + dependency.msg
	}
	return "DEPENDENCY_UNAVAILABLE: dependency request failed"
}

type gatewayGenerateError struct {
	message   string
	retryable bool
}

func (err *gatewayGenerateError) Error() string { return err.message }

func retryableGenerateError(err error) bool {
	var generateErr *gatewayGenerateError
	return errors.As(err, &generateErr) && generateErr.retryable
}

func (r *AgentRuntime) generate(ctx context.Context, model modelMapping, level *string, conversationID string, messages []gatewayMessage, toolsEnabled bool, continuation *gatewayContinuation) (gatewayResponse, int, bool, error) {
	options := map[string]any{}
	if level != nil {
		options["model_level"] = *level
	}
	toolDefinitions := []map[string]any{}
	if toolsEnabled {
		toolDefinitions = tools.Definitions()
	}
	body := map[string]any{
		"provider": model.ProviderID, "model": model.Model, "messages": messages,
		"tools": toolDefinitions, "options": options, "conversation_id": conversationID,
	}
	if continuation != nil {
		body["continuation"] = continuation
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return gatewayResponse{}, 0, false, &gatewayGenerateError{message: "unable to encode Gateway request"}
	}
	if len(encoded) > maxGatewayRequestBytes && continuation != nil {
		delete(body, "continuation")
		encoded, err = json.Marshal(body)
		if err != nil {
			return gatewayResponse{}, 0, false, &gatewayGenerateError{message: "unable to encode Gateway request"}
		}
	}
	if len(encoded) > maxGatewayRequestBytes {
		return gatewayResponse{}, len(encoded), false, &gatewayGenerateError{message: "CONTEXT_LIMIT_EXCEEDED: Gateway request exceeds 1 MiB"}
	}
	callContext, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callContext, http.MethodPost, strings.TrimRight(r.config.GatewayURL, "/")+"/v1/generate", bytes.NewReader(encoded))
	if err != nil {
		return gatewayResponse{}, len(encoded), false, &gatewayGenerateError{message: "unable to create Gateway request"}
	}
	request.Header.Set("Authorization", "Bearer "+r.config.GatewayRuntimeToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.httpClient.Do(request)
	if err != nil {
		if callContext.Err() != nil {
			return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{
				message:   "request canceled or timed out: " + callContext.Err().Error(),
				retryable: ctx.Err() == nil && errors.Is(callContext.Err(), context.DeadlineExceeded),
			}
		}
		return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: "Gateway request failed", retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = decodeDependencyJSON(response.Body, &failure)
		retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		switch failure.Error.Code {
		case "AUTHENTICATION_FAILED", "REQUEST_INVALID", "PROVIDER_INVALID_CONFIG", "HARNESS_NOT_CONFIGURED", "HARNESS_LOGIN_REQUIRED", "HARNESS_VERSION_MISMATCH":
			retryable = false
		case "PROVIDER_UNAVAILABLE", "PROVIDER_TIMEOUT", "RATE_LIMITED":
			retryable = true
		}
		if failure.Error.Code == "" {
			return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: fmt.Sprintf("Gateway returned HTTP %d", response.StatusCode), retryable: retryable}
		}
		return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: fmt.Sprintf("%s: %s", failure.Error.Code, failure.Error.Message), retryable: retryable}
	}
	var result gatewayResponse
	if err := decodeDependencyJSON(response.Body, &result); err != nil {
		return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: "Gateway returned an invalid response", retryable: true}
	}
	if result.FinishReason == "" || !validGatewayUsage(result.Usage) || !validGatewayDiagnostics(result.NumTurns, result.TotalCostUSD) ||
		!validContinuationDiagnostics(result.SessionMode, result.FallbackReason, result.InvocationCount) {
		return result, len(encoded), true, &gatewayGenerateError{message: "Gateway returned an invalid response", retryable: true}
	}
	return result, len(encoded), true, nil
}

func validGatewayDiagnostics(numTurns *int, totalCostUSD *float64) bool {
	return (numTurns == nil || *numTurns >= 0) &&
		(totalCostUSD == nil || (*totalCostUSD >= 0 && !math.IsNaN(*totalCostUSD) && !math.IsInf(*totalCostUSD, 0)))
}

func validContinuationDiagnostics(sessionMode, fallbackReason *string, invocationCount *int) bool {
	return validDiagnosticLabel(sessionMode) && validDiagnosticLabel(fallbackReason) &&
		(invocationCount == nil || *invocationCount >= 1)
}

func validDiagnosticLabel(value *string) bool {
	if value == nil {
		return true
	}
	if len(*value) == 0 || len(*value) > 64 {
		return false
	}
	for _, char := range *value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func markLastModelCallTerminal(progress []traceEntry, iteration int) {
	for index := len(progress) - 1; index >= 0; index-- {
		if progress[index].Type != "model_call" {
			continue
		}
		progress[index].AttemptType = "terminal"
		progress[index].Attempt = 1
		progress[index].Iteration = iteration
		progress[index].CountsAsDecision = true
		return
	}
}

func (r *AgentRuntime) runIsRunning(sessionID string, runID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var status string
	err := r.db.QueryRow(`SELECT status FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "running", nil
}

func (r *AgentRuntime) updateRunProgress(sessionID string, runID int64, modelCalls, toolCalls int, progress []traceEntry) (bool, error) {
	encoded, _ := json.Marshal(progress)
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.db.Exec(`UPDATE runs SET model_call_count = ?, tool_call_count = ?, progress_json = ?
		WHERE session_id = ? AND run_id = ? AND status = 'running'`, modelCalls, toolCalls, string(encoded), sessionID, runID)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return updated == 1, nil
}

func (r *AgentRuntime) persistInitialContext(sessionID string, runID int64, context json.RawMessage) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.db.Exec(`UPDATE runs SET initial_context_json = ?
		WHERE session_id = ? AND run_id = ? AND status = 'running' AND initial_context_json IS NULL`, string(context), sessionID, runID)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

func (r *AgentRuntime) failRun(sessionID string, runID int64, reason string) (bool, error) {
	return r.persistTerminalRun(sessionID, runID, "failed", nil, nil, &reason)
}

func (r *AgentRuntime) persistRunFailure(sessionID string, runID int64, reason string) {
	_, err := r.failRun(sessionID, runID, reason)
	if err != nil {
		r.latchPersistenceFault(fmt.Errorf("persist failed Run %s/%d: %w", sessionID, runID, err))
	}
}

func (r *AgentRuntime) handleRunPersistenceError(sessionID string, runID int64, operationErr error) {
	_, terminalErr := r.failRun(sessionID, runID, "PERSISTENCE_ERROR: unable to persist Run progress")
	if terminalErr != nil {
		r.latchPersistenceFault(errors.Join(
			fmt.Errorf("persist Run %s/%d progress: %w", sessionID, runID, operationErr),
			fmt.Errorf("persist Run %s/%d failed state: %w", sessionID, runID, terminalErr),
		))
	}
}

func (r *AgentRuntime) finishActive(sessionID string, active *activeRun) {
	r.mu.Lock()
	if r.active[sessionID] == active {
		delete(r.active, sessionID)
	}
	close(active.done)
	r.dispatchPendingLocked(sessionID)
	r.mu.Unlock()
	r.wg.Done()
}
