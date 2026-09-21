package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"stock-ai/agent-server/internal/events"
	"stock-ai/agent-server/internal/history"
	"stock-ai/agent-server/internal/server"
)

const sessionSelect = `SELECT id, name, account_id, model_name, model_level, prompt,
	max_loop, max_tool_call, status, current_run_id, restart_context, created_at,
	started_at, stopped_at, deleted_at FROM sessions`

func scanSession(row scanner) (Session, error) {
	var session Session
	var modelLevel, restartContext, startedAt, stoppedAt, deletedAt sql.NullString
	err := row.Scan(&session.ID, &session.Name, &session.AccountID, &session.ModelName, &modelLevel,
		&session.Prompt, &session.MaxLoop, &session.MaxToolCall, &session.Status, &session.CurrentRunID,
		&restartContext, &session.CreatedAt, &startedAt, &stoppedAt, &deletedAt)
	if err != nil {
		return Session{}, err
	}
	if modelLevel.Valid {
		session.ModelLevel = &modelLevel.String
	}
	if restartContext.Valid {
		session.RestartContext = json.RawMessage(restartContext.String)
	} else {
		session.RestartContext = json.RawMessage("null")
	}
	session.StartedAt = nullStringPointer(startedAt)
	session.StoppedAt = nullStringPointer(stoppedAt)
	session.DeletedAt = nullStringPointer(deletedAt)
	return session, nil
}

func (r *AgentRuntime) sessionByID(ctx context.Context, sessionID string) (Session, error) {
	return scanSession(r.db.QueryRowContext(ctx, sessionSelect+` WHERE id = ?`, sessionID))
}

func (r *AgentRuntime) stopSession(w http.ResponseWriter, request *http.Request, sessionID string) {
	r.changeSessionState(w, request, sessionID, false)
}

func (r *AgentRuntime) deleteSession(w http.ResponseWriter, request *http.Request, sessionID string) {
	r.changeSessionState(w, request, sessionID, true)
}

func (r *AgentRuntime) changeSessionState(w http.ResponseWriter, request *http.Request, sessionID string, deleting bool) {
	r.mu.Lock()
	if r.closed || r.persistenceErr != nil {
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	tx, err := r.db.BeginTx(request.Context(), nil)
	if err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	session, err := scanSession(tx.QueryRowContext(request.Context(), sessionSelect+` WHERE id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if session.Status == "deleted" && !deleting {
		_ = tx.Rollback()
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusConflict, "SESSION_DELETED", "deleted Session cannot be stopped")
		return
	}
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	targetStatus := "stopped"
	clearOutcome := "cleared_by_stop"
	if deleting {
		targetStatus = "deleted"
		clearOutcome = "cleared_by_delete"
	}
	clearance, err := events.ClearSessionTx(tx, sessionID, nowTime, clearOutcome)
	if err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	// A repeated stop on an already stopped Session recomputes nothing: the first stop's
	// cleared_events / pending_triggers exist only in the saved restart_context.
	var restartContext any
	if len(session.RestartContext) != 0 && string(session.RestartContext) != "null" {
		restartContext = string(session.RestartContext)
	}
	if session.Status == "running" {
		restartContext, err = restartContextFor(tx, sessionID, session.CurrentRunID, true, clearance)
		if err != nil {
			requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
			if !requestCanceled {
				r.latchPersistenceFaultLocked(err)
			}
			_ = tx.Rollback()
			r.mu.Unlock()
			writeMutationPersistenceError(w, requestCanceled)
			return
		}
	}
	if session.Status == "running" && session.CurrentRunID != 0 {
		if _, err := tx.ExecContext(request.Context(), `UPDATE runs SET status = 'interrupted', summary = NULL,
			output = NULL, error = 'HARD_STOP: interrupted by user', end_time = ? WHERE session_id = ? AND run_id = ? AND status = 'running'`,
			now, sessionID, session.CurrentRunID); err != nil {
			requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
			if !requestCanceled {
				r.latchPersistenceFaultLocked(err)
			}
			_ = tx.Rollback()
			r.mu.Unlock()
			writeMutationPersistenceError(w, requestCanceled)
			return
		}
	}
	deletedAt := any(nil)
	if deleting {
		deletedAt = now
	}
	if _, err := tx.ExecContext(request.Context(), `UPDATE sessions SET status = ?, stopped_at = ?, deleted_at = ?, restart_context = ? WHERE id = ?`,
		targetStatus, now, deletedAt, restartContext, sessionID); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	var historyStage *history.Stage
	if session.Status == "running" && session.CurrentRunID != 0 {
		historyStage, err = history.StageRunIDs(tx, &r.historyMu, r.config.HistoryDirectory, sessionID, history.ClearanceRunIDs(session.CurrentRunID, clearance)...)
		if err != nil {
			requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
			if !requestCanceled {
				r.latchPersistenceFaultLocked(err)
			}
			_ = tx.Rollback()
			r.mu.Unlock()
			writeMutationPersistenceError(w, requestCanceled)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		if historyStage != nil {
			if restoreErr := historyStage.Finish(false); restoreErr != nil {
				r.latchPersistenceFaultLocked(restoreErr)
			}
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if historyStage != nil {
		if err := historyStage.Finish(true); err != nil {
			r.latchPersistenceFaultLocked(err)
			r.mu.Unlock()
			server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
			return
		}
	}
	if session.Status == "running" && session.CurrentRunID != 0 {
		r.logOperational("run_terminal", sessionID, session.CurrentRunID, "interrupted.HARD_STOP")
	}
	active := r.active[sessionID]
	if active != nil {
		active.cancel()
	}
	session.Status = targetStatus
	session.StoppedAt = &now
	session.DeletedAt = nil
	if deleting {
		session.DeletedAt = &now
	}
	session.RestartContext = json.RawMessage("null")
	if encoded, ok := restartContext.(string); ok {
		session.RestartContext = json.RawMessage(encoded)
	}
	r.mu.Unlock()

	pending := false
	if active != nil {
		timer := time.NewTimer(r.config.StopTimeout)
		select {
		case <-active.done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			pending = true
		}
	}
	status := http.StatusOK
	if pending {
		status = http.StatusAccepted
	}
	writeRuntimeJSON(w, status, map[string]any{"session": session, "cancellation_pending": pending})
}

func restartContextFor(tx *sql.Tx, sessionID string, runID int64, interrupting bool, clearance events.Clearance) (any, error) {
	if runID == 0 {
		return nil, nil
	}
	run, err := scanRun(tx.QueryRow(runSelect+` WHERE session_id = ? AND run_id = ?`, sessionID, runID))
	if err != nil {
		return nil, err
	}
	status, reason := run.Status, run.Error
	if interrupting && run.Status == "running" {
		status = "interrupted"
		value := "HARD_STOP: interrupted by user"
		reason = &value
	}
	if status == "completed" && len(clearance.Events) == 0 && len(clearance.Pending) == 0 {
		return nil, nil
	}
	progressSummary, recentProgress, omitted, err := compactRestartProgress(run)
	if err != nil {
		return nil, err
	}
	context := map[string]any{
		"previous_run_id": run.RunID, "status": status, "reason": reason,
		"summary": run.Summary, "output": run.Output, "progress_summary": progressSummary,
		"recent_progress": recentProgress, "progress_omitted": omitted,
		"triggers": run.Triggers, "cleared_events": clearance.Events, "pending_triggers": clearance.Pending,
	}
	if status == "completed" {
		delete(context, "reason")
		delete(context, "summary")
		delete(context, "output")
		delete(context, "progress_summary")
		delete(context, "recent_progress")
		delete(context, "progress_omitted")
		delete(context, "triggers")
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

type restartProgressEntry struct {
	Type         string `json:"type"`
	Iteration    int    `json:"iteration,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Status       string `json:"status"`
}

func compactRestartProgress(run Run) (map[string]int, []restartProgressEntry, int, error) {
	var progress []traceEntry
	if err := json.Unmarshal(run.Progress, &progress); err != nil {
		return nil, nil, 0, err
	}
	start := len(progress) - restartProgressEntries
	if start < 0 {
		start = 0
	}
	recent := make([]restartProgressEntry, 0, len(progress)-start)
	for _, entry := range progress[start:] {
		status := "completed"
		errorCode := ""
		if entry.Error != "" {
			status = "error"
			errorCode = compactErrorCode(entry.Error)
		} else if entry.Type == "tool_call" {
			var result struct {
				OK    bool `json:"ok"`
				Error struct {
					Code    string `json:"code"`
					Outcome string `json:"outcome"`
				} `json:"error"`
			}
			if json.Unmarshal(entry.Result, &result) != nil {
				status = "unknown"
			} else if result.OK {
				status = "ok"
			} else if result.Error.Outcome != "" {
				status = result.Error.Outcome
				errorCode = result.Error.Code
			} else if entry.EndedAt == nil {
				status = "in_progress"
			} else {
				status = "error"
			}
		} else if entry.EndedAt == nil {
			status = "in_progress"
		}
		recent = append(recent, restartProgressEntry{
			Type: entry.Type, Iteration: entry.Iteration, ToolName: entry.ToolName,
			FinishReason: entry.FinishReason, ErrorCode: errorCode, Status: status,
		})
	}
	return map[string]int{
		"model_call_count": run.ModelCalls,
		"tool_call_count":  run.ToolCalls,
		"trace_count":      len(progress),
	}, recent, start, nil
}

func compactErrorCode(message string) string {
	code := strings.TrimSpace(strings.SplitN(message, ":", 2)[0])
	if code == "" {
		return ""
	}
	for index, char := range code {
		if index == 0 && (char < 'A' || char > 'Z') {
			return ""
		}
		if index > 0 && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return ""
		}
	}
	return code
}

func normalizeRestartContext(raw json.RawMessage) (json.RawMessage, error) {
	var context map[string]json.RawMessage
	if err := json.Unmarshal(raw, &context); err != nil {
		return nil, err
	}
	progress, legacy := context["progress"]
	if !legacy {
		return raw, nil
	}
	run := Run{Progress: progress}
	_ = json.Unmarshal(context["model_call_count"], &run.ModelCalls)
	_ = json.Unmarshal(context["tool_call_count"], &run.ToolCalls)
	modelCountMissing, toolCountMissing := run.ModelCalls == 0, run.ToolCalls == 0
	if modelCountMissing || toolCountMissing {
		var entries []traceEntry
		if err := json.Unmarshal(progress, &entries); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type == "model_call" && modelCountMissing {
				run.ModelCalls++
			}
			if entry.Type == "tool_call" && toolCountMissing {
				run.ToolCalls++
			}
		}
	}
	summary, recent, omitted, err := compactRestartProgress(run)
	if err != nil {
		return nil, err
	}
	for key, value := range map[string]any{
		"progress_summary": summary, "recent_progress": recent, "progress_omitted": omitted,
	} {
		context[key], _ = json.Marshal(value)
	}
	delete(context, "progress")
	return json.Marshal(context)
}

func deriveDecisionCount(progress json.RawMessage, modelCalls int) int {
	var entries []traceEntry
	if json.Unmarshal(progress, &entries) != nil {
		return modelCalls
	}
	iterations := make(map[int]bool)
	decisions, tracedCalls := 0, 0
	for _, entry := range entries {
		if entry.Type != "model_call" {
			continue
		}
		tracedCalls++
		if entry.AttemptType == "terminal" && !entry.CountsAsDecision {
			continue
		}
		if entry.Iteration > 0 {
			iterations[entry.Iteration] = true
		} else {
			decisions++
		}
	}
	decisions += len(iterations)
	if modelCalls > tracedCalls {
		decisions += modelCalls - tracedCalls
	}
	return decisions
}

func (r *AgentRuntime) interruptRunLocked(sessionID string, runID int64, reason string) (bool, error) {
	return r.persistTerminalRunLocked(sessionID, runID, "interrupted", nil, nil, &reason)
}

const runSelect = `SELECT session_id, run_id, status, trigger, triggers_json, summary, output, error,
	progress_json, initial_context_json, model_call_count, tool_call_count, start_time, end_time, retry_of_run_id, retry_attempt FROM runs`

func scanRun(row scanner) (Run, error) {
	var run Run
	var triggers, summary, output, runError, initialContext, endedAt sql.NullString
	var retryOf sql.NullInt64
	var progress string
	err := row.Scan(&run.SessionID, &run.RunID, &run.Status, &run.Trigger, &triggers, &summary, &output,
		&runError, &progress, &initialContext, &run.ModelCalls, &run.ToolCalls, &run.StartedAt, &endedAt, &retryOf, &run.RetryAttempt)
	if err != nil {
		return Run{}, err
	}
	run.Triggers = json.RawMessage("null")
	if triggers.Valid {
		run.Triggers = json.RawMessage(triggers.String)
	}
	run.Summary = nullStringPointer(summary)
	run.Output = nullStringPointer(output)
	run.Error = nullStringPointer(runError)
	run.EndedAt = nullStringPointer(endedAt)
	if retryOf.Valid {
		run.RetryOfRunID = &retryOf.Int64
	}
	run.Progress = json.RawMessage(progress)
	run.InitialContext = json.RawMessage("null")
	if initialContext.Valid {
		run.InitialContext = json.RawMessage(initialContext.String)
	}
	run.DecisionCount = deriveDecisionCount(run.Progress, run.ModelCalls)
	return run, nil
}

func (r *AgentRuntime) runByID(ctx context.Context, sessionID string, runID int64) (Run, error) {
	return scanRun(r.db.QueryRowContext(ctx, runSelect+` WHERE session_id = ? AND run_id = ?`, sessionID, runID))
}
