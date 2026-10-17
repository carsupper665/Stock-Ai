package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"stock-ai/agent-server/internal/history"
	"stock-ai/agent-server/internal/memory"
	"stock-ai/agent-server/internal/server"
	"stock-ai/agent-server/internal/tools"
)

func (r *AgentRuntime) completeFinalizedRun(sessionID string, runID int64, final memory.Finalization) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return false, errors.New("Agent persistence is unavailable")
	}
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	endTime := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE runs SET status = 'completed', summary = ?, output = ?, error = NULL, end_time = ?
		WHERE session_id = ? AND run_id = ? AND status = 'running'`, final.Summary, final.Output, endTime, sessionID, runID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		_ = tx.Rollback()
		return false, err
	}
	stage, err := history.StageRun(tx, &r.historyMu, r.config.HistoryDirectory, sessionID, runID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return false, errors.Join(err, stage.Finish(false))
	}
	if err := stage.Finish(true); err != nil {
		return false, err
	}
	r.logOperational("run_terminal", sessionID, runID, "completed")
	return true, nil
}

func (r *AgentRuntime) executeMemoryTool(ctx context.Context, sessionID string, runID int64, call gatewayTool) (json.RawMessage, bool) {
	switch call.Name {
	case "memory_list":
		filter, err := memory.ParseListArguments(call.Arguments, time.Now())
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		labels, err := memory.List(ctx, r.db, sessionID, filter)
		if err != nil {
			return r.memoryPersistenceToolError(err), true
		}
		stored, _, err := memory.Count(ctx, r.db, sessionID)
		if err != nil {
			return r.memoryPersistenceToolError(err), true
		}
		return tools.SuccessResult(map[string]any{"memories": labels, "stored": stored, "capacity": memory.Capacity}), true
	case "memory_get":
		id, err := memory.ParseIDArgument(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		mem, err := memory.Get(ctx, r.db, sessionID, id)
		if errors.Is(err, memory.ErrNotFound) {
			return tools.ErrorResult("MEMORY_NOT_FOUND", err.Error(), "not_executed"), true
		}
		if err != nil {
			return r.memoryPersistenceToolError(err), true
		}
		return tools.SuccessResult(map[string]any{"id": mem.ID, "title": mem.Title, "msg": mem.Msg,
			"bring_back": mem.BringBack, "created_at": mem.CreatedAt}), true
	case "memory_save":
		title, msg, bringBack, err := memory.ParseSaveArguments(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		return r.memoryWrite(sessionID, runID, func(tx *sql.Tx) (memory.Memory, error) {
			return memory.Save(context.Background(), tx, sessionID, runID, title, msg, bringBack, time.Now())
		}), true
	case "memory_edit":
		id, edit, err := memory.ParseEditArguments(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		return r.memoryWrite(sessionID, runID, func(tx *sql.Tx) (memory.Memory, error) {
			return memory.Update(context.Background(), tx, sessionID, runID, id, edit, time.Now())
		}), true
	case "get_run_history":
		runID, err := history.ToolArgument(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		run, err := history.ReadRun(ctx, r.db, &r.historyMu, r.config.HistoryDirectory, sessionID, runID)
		if err != nil {
			return r.historyToolError(err), true
		}
		return tools.SuccessResult(run), true
	default:
		return nil, false
	}
}

// memoryWrite runs one cabinet write under r.mu in one transaction, only while the Run is
// still running, so a hard stop can never be followed by a late memory write.
func (r *AgentRuntime) memoryWrite(sessionID string, runID int64, write func(tx *sql.Tx) (memory.Memory, error)) json.RawMessage {
	unavailable := func(outcome string) json.RawMessage {
		return tools.ErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", outcome)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return unavailable("not_executed")
	}
	tx, err := r.db.Begin()
	if err != nil {
		r.latchPersistenceFaultLocked(err)
		return unavailable("not_executed")
	}
	defer tx.Rollback()
	running, err := runIsRunningTx(tx, sessionID, runID)
	if err == nil && !running {
		return tools.ErrorResult("RUN_NOT_RUNNING", "the current Run is no longer running", "not_executed")
	}
	var saved memory.Memory
	if err == nil {
		saved, err = write(tx)
	}
	var limit *memory.LimitError
	if errors.As(err, &limit) {
		return tools.ErrorResult(limit.Code, limit.Message, "not_executed")
	}
	if errors.Is(err, memory.ErrNotFound) {
		return tools.ErrorResult("MEMORY_NOT_FOUND", err.Error(), "not_executed")
	}
	if err != nil {
		r.latchPersistenceFaultLocked(err)
		return unavailable("not_executed")
	}
	if err := tx.Commit(); err != nil {
		r.latchPersistenceFaultLocked(err)
		return unavailable("unknown")
	}
	return tools.SuccessResult(map[string]any{"id": saved.ID, "title": saved.Title, "bring_back": saved.BringBack, "created_at": saved.CreatedAt})
}

func (r *AgentRuntime) memoryPersistenceToolError(err error) json.RawMessage {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return tools.ErrorResult("TOOL_INTERRUPTED", "Memory read was interrupted", "not_executed")
	}
	r.latchPersistenceFault(err)
	return tools.ErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", "not_executed")
}

func (r *AgentRuntime) serveMemories(w http.ResponseWriter, request *http.Request, sessionID string, rest []string) {
	switch {
	case len(rest) == 0 && request.Method == http.MethodGet:
		r.listMemoriesHTTP(w, request, sessionID)
	case len(rest) == 0:
		w.Header().Set("Allow", http.MethodGet)
		server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "users cannot create or modify Memories")
	case len(rest) == 1 && request.Method == http.MethodGet:
		r.getMemoryHTTP(w, request, sessionID, rest[0])
	case len(rest) == 1 && request.Method == http.MethodDelete:
		r.deleteMemoryHTTP(w, request, sessionID, rest[0])
	case len(rest) == 1:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
		server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "users cannot create or modify Memories")
	default:
		server.WriteJSONError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (r *AgentRuntime) listMemoriesHTTP(w http.ResponseWriter, request *http.Request, sessionID string) {
	if request.URL.RawQuery != "" {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "the memory cabinet list takes no query parameters")
		return
	}
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	memories, err := memory.All(request.Context(), r.db, sessionID)
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Memories")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"memories": memories, "capacity": memory.Capacity})
}

func memoryIDFromPath(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func (r *AgentRuntime) getMemoryHTTP(w http.ResponseWriter, request *http.Request, sessionID, rawID string) {
	id, ok := memoryIDFromPath(rawID)
	if !ok {
		server.WriteJSONError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
		return
	}
	mem, err := memory.Get(request.Context(), r.db, sessionID, id)
	if errors.Is(err, memory.ErrNotFound) {
		server.WriteJSONError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
		return
	}
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Memory")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, mem)
}

func (r *AgentRuntime) deleteMemoryHTTP(w http.ResponseWriter, request *http.Request, sessionID, rawID string) {
	id, ok := memoryIDFromPath(rawID)
	if !ok {
		server.WriteJSONError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
		return
	}
	deleted, err := r.deleteMemory(request.Context(), sessionID, id)
	switch {
	case err == nil:
		writeRuntimeJSON(w, http.StatusOK, deleted)
	case errors.Is(err, memory.ErrNotFound):
		server.WriteJSONError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
	case errors.Is(err, errPersistenceUnavailable):
		server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
	default:
		writeMutationPersistenceError(w, requestCancellationCausedPersistenceError(request.Context(), err))
	}
}

var errPersistenceUnavailable = errors.New("Agent persistence is unavailable")

// deleteMemory holds the Runtime mutex only for the transaction, never while a response is
// written, so a slow client cannot block other Sessions.
func (r *AgentRuntime) deleteMemory(ctx context.Context, sessionID string, id int64) (memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return memory.Memory{}, errPersistenceUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err == nil {
		defer tx.Rollback()
		var deleted memory.Memory
		deleted, err = memory.Delete(ctx, tx, sessionID, id)
		if errors.Is(err, memory.ErrNotFound) {
			return memory.Memory{}, err
		}
		if err == nil {
			err = tx.Commit()
		}
		if err == nil {
			return deleted, nil
		}
	}
	if !requestCancellationCausedPersistenceError(ctx, err) {
		r.latchPersistenceFaultLocked(err)
	}
	return memory.Memory{}, err
}
