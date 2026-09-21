package common

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
	run, err := scanRun(tx.QueryRow(runSelect+` WHERE session_id = ? AND run_id = ?`, sessionID, runID))
	if err != nil || run.Status != "running" {
		_ = tx.Rollback()
		return false, err
	}
	endTime := time.Now().UTC().Format(time.RFC3339Nano)
	expiredReferences := make(map[string]bool, len(final.ExpireMemories))
	for _, expiration := range final.ExpireMemories {
		var expired bool
		err := tx.QueryRow(`SELECT is_expired FROM memories WHERE id = ? AND session_id = ?`, expiration.ID, sessionID).Scan(&expired)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return false, memory.NewReferenceError(expiration.ID)
		}
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
		expiredReferences[expiration.ID] = expired
	}
	for index, candidate := range final.CandidateMemories {
		id, err := memory.NewMemoryID()
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
		_, err = tx.Exec(`INSERT INTO memories (id, session_id, run_id, candidate_index, type, summary,
			content, importance, run_start_at, run_end_at, is_expired, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			id, sessionID, runID, index, candidate.Type, candidate.Summary, candidate.Content,
			candidate.Importance, run.StartedAt, endTime, endTime, endTime)
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
	}
	for _, expiration := range final.ExpireMemories {
		if expiredReferences[expiration.ID] {
			continue
		}
		if _, err := tx.Exec(`UPDATE memories SET is_expired = 1, expired_at = ?, expired_source = 'agent',
			expired_run_id = ?, expiration_reason = ?, updated_at = ? WHERE id = ? AND session_id = ? AND is_expired = 0`,
			endTime, runID, expiration.Reason, endTime, expiration.ID, sessionID); err != nil {
			_ = tx.Rollback()
			return false, err
		}
	}
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

func (r *AgentRuntime) executeMemoryTool(ctx context.Context, sessionID string, call gatewayTool) (json.RawMessage, bool) {
	switch call.Name {
	case "memory_list":
		limit, err := tools.MemoryLimitArguments(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		memories, err := memory.List(ctx, r.db, sessionID, "", limit)
		return r.memoryReadToolResult(memories, err), true
	case "memory_search":
		query, limit, err := tools.MemorySearchArguments(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		memories, err := memory.List(ctx, r.db, sessionID, query, limit)
		return r.memoryReadToolResult(memories, err), true
	case "memory_read":
		id, err := tools.SingleIDArgument(call.Arguments, "memory_id")
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		mem, err := memory.MemoryByID(ctx, r.db, sessionID, id, false)
		if errors.Is(err, sql.ErrNoRows) {
			return tools.ErrorResult("MEMORY_NOT_FOUND", "Memory does not exist or is expired in this Session", "not_executed"), true
		}
		if err != nil {
			return r.memoryPersistenceToolError(err), true
		}
		return tools.SuccessResult(map[string]any{
			"id": mem.ID, "run_id": mem.RunID, "type": mem.Type,
			"summary": mem.Summary, "content": mem.Content, "importance": mem.Importance,
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

func (r *AgentRuntime) memoryReadToolResult(memories []memory.Metadata, err error) json.RawMessage {
	if err != nil {
		return r.memoryPersistenceToolError(err)
	}
	return tools.SuccessResult(map[string]any{"memories": memories})
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
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	page, includeExpired, ok := memoryListQuery(request)
	if !ok {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "page must be positive and include_expired must be true or false")
		return
	}
	query := memory.ListSelect + ` WHERE session_id = ?`
	if !includeExpired {
		query += ` AND is_expired = 0`
	}
	query += ` ORDER BY importance DESC, run_id DESC, id LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(request.Context(), query, sessionID, memory.PageSize, (page-1)*memory.PageSize)
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Memories")
		return
	}
	defer rows.Close()
	items := make([]memory.ListItem, 0)
	for rows.Next() {
		item, err := memory.ScanListItem(rows)
		if err != nil {
			server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Memories")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Memories")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"memories": items, "page": page, "page_size": memory.PageSize, "include_expired": includeExpired})
}

func memoryListQuery(request *http.Request) (int, bool, bool) {
	values := request.URL.Query()
	for key := range values {
		if key != "page" && key != "include_expired" {
			return 0, false, false
		}
	}
	page := 1
	if rawValues, present := values["page"]; present {
		if len(rawValues) != 1 || rawValues[0] == "" {
			return 0, false, false
		}
		var err error
		page, err = strconv.Atoi(rawValues[0])
		if err != nil || page < 1 || page > int(^uint(0)>>1)/memory.PageSize {
			return 0, false, false
		}
	}
	includeExpired := false
	if rawValues, present := values["include_expired"]; present {
		if len(rawValues) != 1 || (rawValues[0] != "true" && rawValues[0] != "false") {
			return 0, false, false
		}
		includeExpired = rawValues[0] == "true"
	}
	return page, includeExpired, true
}

func (r *AgentRuntime) getMemoryHTTP(w http.ResponseWriter, request *http.Request, sessionID, memoryID string) {
	mem, err := memory.MemoryByID(request.Context(), r.db, sessionID, memoryID, true)
	if errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
		return
	}
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Memory")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, mem)
}

func (r *AgentRuntime) deleteMemoryHTTP(w http.ResponseWriter, request *http.Request, sessionID, memoryID string) {
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
	mem, err := memory.ScanMemory(tx.QueryRowContext(request.Context(), memory.Select+` WHERE session_id = ? AND id = ?`, sessionID, memoryID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
		return
	}
	if err != nil {
		_ = tx.Rollback()
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	var userExpiredAt *string
	if !mem.IsExpired {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		userExpiredAt = &now
		_, err = tx.ExecContext(request.Context(), `UPDATE memories SET is_expired = 1, expired_at = ?,
			expired_source = 'user', expired_run_id = NULL, expiration_reason = 'deleted by user', updated_at = ?
			WHERE session_id = ? AND id = ? AND is_expired = 0`, now, now, sessionID, memoryID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		if requestCancellationCausedPersistenceError(request.Context(), err) {
			r.mu.Unlock()
			server.WriteJSONError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before the mutation committed")
			return
		}
		r.latchPersistenceFaultLocked(err)
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	if userExpiredAt != nil {
		source, reason := "user", "deleted by user"
		mem.IsExpired = true
		mem.ExpiredAt = userExpiredAt
		mem.ExpiredSource = &source
		mem.ExpirationReason = &reason
		mem.UpdatedAt = *userExpiredAt
	}
	r.mu.Unlock()
	writeRuntimeJSON(w, http.StatusOK, mem)
}
