package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"stock-ai/agent-server/internal/history"
	"stock-ai/agent-server/internal/server"
	"stock-ai/agent-server/internal/tools"
)

// This file keeps the AgentRuntime-bound Run History lifecycle: persisting
// terminal runs, repairing shards after a crash, serving shard files over
// HTTP, and shaping history tool errors. The shard storage domain lives in
// internal/history.

func (r *AgentRuntime) persistTerminalRun(sessionID string, runID int64, status string, summary, output, runError *string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persistTerminalRunLocked(sessionID, runID, status, summary, output, runError)
}

func (r *AgentRuntime) persistTerminalRunLocked(sessionID string, runID int64, status string, summary, output, runError *string) (bool, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE runs SET status = ?, summary = ?, output = ?, error = ?, end_time = ?
		WHERE session_id = ? AND run_id = ? AND status = 'running'`, status, summary, output, runError, now, sessionID, runID)
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
		restoreErr := stage.Finish(false)
		return false, errors.Join(err, restoreErr)
	}
	if err := stage.Finish(true); err != nil {
		return false, err
	}
	detail := status
	if runError != nil {
		if code := compactErrorCode(*runError); code != "" {
			detail += "." + code
		}
	}
	r.logOperational("run_terminal", sessionID, runID, detail)
	return true, nil
}

// repairRunHistory rewrites every shard that contains a terminal run so a
// crashed server cannot leave stale shard files behind.
func (r *AgentRuntime) repairRunHistory() error {
	rows, err := r.db.Query(`SELECT session_id, run_id FROM runs
		WHERE status IN ('completed', 'failed', 'interrupted') ORDER BY session_id, run_id`)
	if err != nil {
		return err
	}
	type shardKey struct {
		sessionID string
		start     int64
	}
	keys := make([]shardKey, 0)
	seen := make(map[shardKey]bool)
	for rows.Next() {
		var sessionID string
		var runID int64
		if err := rows.Scan(&sessionID, &runID); err != nil {
			_ = rows.Close()
			return err
		}
		start, _ := history.Range(runID)
		key := shardKey{sessionID: sessionID, start: start}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range keys {
		tx, err := r.db.Begin()
		if err != nil {
			return err
		}
		stage, err := history.StageRange(tx, &r.historyMu, r.config.HistoryDirectory, key.sessionID, key.start)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			_ = tx.Rollback()
			var restoreErr error
			if stage != nil {
				restoreErr = stage.Finish(false)
			}
			return errors.Join(err, restoreErr)
		}
		if err := stage.Finish(true); err != nil {
			return err
		}
	}
	return history.RemoveTemps(r.config.HistoryDirectory)
}

func (r *AgentRuntime) serveRunHistory(w http.ResponseWriter, request *http.Request, sessionID, rawRunID string) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	runID, err := strconv.ParseInt(rawRunID, 10, 64)
	if err != nil || runID <= 0 {
		server.WriteJSONError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	run, err := r.runByID(request.Context(), sessionID, runID)
	if requestCancellationCausedPersistenceError(request.Context(), err) {
		server.WriteJSONError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	if err != nil {
		r.writeHistoryUnavailable(w, fmt.Errorf("resolve Run for History: %w", err))
		return
	}
	if err := request.Context().Err(); err != nil {
		server.WriteJSONError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
		return
	}
	if run.Status == "running" {
		server.WriteJSONError(w, http.StatusConflict, "HISTORY_NOT_READY", "Run History is available after the Run ends")
		return
	}
	var start, end int64
	var name string
	err = r.db.QueryRowContext(request.Context(), `SELECT start_run_id, end_run_id, file_path FROM run_history_files
		WHERE session_id = ? AND start_run_id <= ? AND end_run_id >= ?`, sessionID, runID, runID).Scan(&start, &end, &name)
	if err != nil {
		if requestCancellationCausedPersistenceError(request.Context(), err) {
			server.WriteJSONError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
			return
		}
		r.writeHistoryUnavailable(w, fmt.Errorf("resolve History index: %w", err))
		return
	}
	if err := request.Context().Err(); err != nil {
		server.WriteJSONError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
		return
	}
	expectedStart, expectedEnd := history.Range(runID)
	if start != expectedStart || end != expectedEnd || name != history.FileName(sessionID, start, end) || filepath.Base(name) != name {
		r.writeHistoryUnavailable(w, errors.New("History index path is invalid"))
		return
	}
	r.historyMu.Lock()
	body, err := os.ReadFile(filepath.Join(r.config.HistoryDirectory, name))
	r.historyMu.Unlock()
	if err != nil {
		r.writeHistoryUnavailable(w, fmt.Errorf("read History shard: %w", err))
		return
	}
	if !history.ValidBody(body, sessionID, start, end, runID) {
		r.writeHistoryUnavailable(w, errors.New("Run History shard is invalid"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (r *AgentRuntime) writeHistoryUnavailable(w http.ResponseWriter, err error) {
	r.latchPersistenceFault(err)
	server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Run History is unavailable")
}

func (r *AgentRuntime) historyToolError(err error) json.RawMessage {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return tools.ErrorResult("TOOL_INTERRUPTED", "Run History read was interrupted", "not_executed")
	}
	code := "HISTORY_UNAVAILABLE"
	message := "Run History is unavailable"
	if errors.Is(err, sql.ErrNoRows) {
		code, message = "RUN_NOT_FOUND", "Run does not exist in this Session"
	} else {
		r.latchPersistenceFault(err)
	}
	return tools.ErrorResult(code, message, "not_executed")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
