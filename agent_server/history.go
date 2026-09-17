package common

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const historyShardSize int64 = 20

var historySchema = []string{
	`CREATE TABLE IF NOT EXISTS run_history_files (
		session_id TEXT NOT NULL, start_run_id INTEGER NOT NULL, end_run_id INTEGER NOT NULL,
		file_path TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (session_id, start_run_id), UNIQUE (file_path)
	)`,
}

type gatewayUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	CachedTokens *int64 `json:"cached_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
}

type historyShard struct {
	SessionID  string       `json:"session_id"`
	StartRunID int64        `json:"start_run_id"`
	EndRunID   int64        `json:"end_run_id"`
	Runs       []historyRun `json:"runs"`
}

type historyRun struct {
	RunID          int64           `json:"run_id"`
	Status         string          `json:"status"`
	Trigger        string          `json:"trigger"`
	Triggers       json.RawMessage `json:"triggers"`
	Summary        *string         `json:"summary"`
	Output         *string         `json:"output"`
	Error          *string         `json:"error"`
	Iterations     json.RawMessage `json:"iterations"`
	InitialContext json.RawMessage `json:"initial_context"`
	ModelCalls     int             `json:"model_call_count"`
	ToolCalls      int             `json:"tool_call_count"`
	StartedAt      string          `json:"start_time"`
	EndedAt        *string         `json:"end_time"`
	MemoryIDs      []string        `json:"memory_ids"`
	EventLog       []historyEvent  `json:"event_log"`
}

type historyEvent struct {
	ID           string          `json:"id"`
	CreatedRunID int64           `json:"created_run_id"`
	Type         string          `json:"type"`
	Params       json.RawMessage `json:"params"`
	State        json.RawMessage `json:"state"`
	CreatedAt    string          `json:"created_at"`
	ExpiresAt    string          `json:"expires_at"`
	Outcome      string          `json:"outcome"`
	OutcomeRunID *int64          `json:"outcome_run_id"`
	Detail       json.RawMessage `json:"detail"`
	At           string          `json:"at"`
}

type historyStage struct {
	runtime *AgentRuntime
	files   []historyStagedFile
	closed  bool
}

type historyStagedFile struct {
	path     string
	previous []byte
	existed  bool
}

type historyTarget struct {
	sessionID string
	start     int64
}

func historyRange(runID int64) (int64, int64) {
	start := ((runID - 1) / historyShardSize * historyShardSize) + 1
	return start, start + historyShardSize - 1
}

func historyFileName(sessionID string, start, end int64) string {
	return fmt.Sprintf("%s_%d-%d.json", sessionID, start, end)
}

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
		start, _ := historyRange(runID)
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
		stage, err := r.stageHistoryRangeTx(tx, key.sessionID, key.start)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			_ = tx.Rollback()
			var restoreErr error
			if stage != nil {
				restoreErr = stage.finish(false)
			}
			return errors.Join(err, restoreErr)
		}
		if err := stage.finish(true); err != nil {
			return err
		}
	}
	return removeHistoryTemps(r.config.HistoryDirectory)
}

func removeHistoryTemps(directory string) error {
	matches, err := filepath.Glob(filepath.Join(directory, ".history-*.tmp"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (r *AgentRuntime) stageRunHistoryTx(tx *sql.Tx, sessionID string, runID int64) (*historyStage, error) {
	runIDs, err := relatedHistoryRunIDsTx(tx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	return r.stageHistoryRunIDsTx(tx, sessionID, runIDs...)
}

func (r *AgentRuntime) stageHistoryRangeTx(tx *sql.Tx, sessionID string, start int64) (*historyStage, error) {
	return r.stageHistoryStartsTx(tx, sessionID, []int64{start})
}

func relatedHistoryRunIDsTx(tx *sql.Tx, sessionID string, runID int64) ([]int64, error) {
	runIDs := []int64{runID}
	rows, err := tx.Query(`SELECT DISTINCT created_run_id FROM event_log
		WHERE session_id = ? AND outcome_run_id = ?`, sessionID, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var related int64
		if err := rows.Scan(&related); err != nil {
			_ = rows.Close()
			return nil, err
		}
		runIDs = append(runIDs, related)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var triggers sql.NullString
	if err := tx.QueryRow(`SELECT triggers_json FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&triggers); err != nil {
		return nil, err
	}
	if triggers.Valid {
		var values []struct {
			CreatedRunID int64 `json:"created_run_id"`
		}
		if json.Unmarshal([]byte(triggers.String), &values) == nil {
			for _, value := range values {
				runIDs = append(runIDs, value.CreatedRunID)
			}
		}
	}
	return runIDs, nil
}

func (r *AgentRuntime) stageHistoryRunIDsTx(tx *sql.Tx, sessionID string, runIDs ...int64) (*historyStage, error) {
	starts := make([]int64, 0, len(runIDs))
	seen := make(map[int64]bool, len(runIDs))
	for _, runID := range runIDs {
		if runID <= 0 {
			continue
		}
		start, _ := historyRange(runID)
		if !seen[start] {
			seen[start] = true
			starts = append(starts, start)
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	return r.stageHistoryStartsTx(tx, sessionID, starts)
}

func clearanceHistoryRunIDs(currentRunID int64, clearance sessionEventClearance) []int64 {
	runIDs := []int64{currentRunID}
	for _, event := range clearance.Events {
		runIDs = append(runIDs, event.CreatedRunID)
	}
	for _, payload := range clearance.Pending {
		var trigger struct {
			CreatedRunID int64 `json:"created_run_id"`
		}
		if json.Unmarshal(payload, &trigger) == nil {
			runIDs = append(runIDs, trigger.CreatedRunID)
		}
	}
	return runIDs
}

func (r *AgentRuntime) stageHistoryStartsTx(tx *sql.Tx, sessionID string, starts []int64) (*historyStage, error) {
	targets := make([]historyTarget, 0, len(starts))
	for _, start := range starts {
		targets = append(targets, historyTarget{sessionID: sessionID, start: start})
	}
	return r.stageHistoryTargetsTx(tx, targets)
}

func (r *AgentRuntime) stageHistoryTargetsTx(tx *sql.Tx, targets []historyTarget) (*historyStage, error) {
	type projection struct {
		sessionID string
		start     int64
		end       int64
		name      string
		path      string
		body      []byte
	}
	deduplicated := make([]historyTarget, 0, len(targets))
	seen := make(map[historyTarget]bool, len(targets))
	for _, target := range targets {
		if target.sessionID == "" || target.start <= 0 || seen[target] {
			continue
		}
		seen[target] = true
		deduplicated = append(deduplicated, target)
	}
	sort.Slice(deduplicated, func(i, j int) bool {
		if deduplicated[i].sessionID == deduplicated[j].sessionID {
			return deduplicated[i].start < deduplicated[j].start
		}
		return deduplicated[i].sessionID < deduplicated[j].sessionID
	})
	projections := make([]projection, 0, len(deduplicated))
	for _, target := range deduplicated {
		end := target.start + historyShardSize - 1
		runs, err := historyRunsTx(tx, target.sessionID, target.start, end)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			continue
		}
		encoded, err := json.Marshal(historyShard{SessionID: target.sessionID, StartRunID: target.start, EndRunID: end, Runs: runs})
		if err != nil {
			return nil, err
		}
		name := historyFileName(target.sessionID, target.start, end)
		projections = append(projections, projection{
			sessionID: target.sessionID, start: target.start, end: end, name: name,
			path: filepath.Join(r.config.HistoryDirectory, name), body: append(encoded, '\n'),
		})
	}
	stage := &historyStage{runtime: r, files: make([]historyStagedFile, 0, len(projections))}
	if len(projections) == 0 {
		stage.closed = true
		return stage, nil
	}
	r.historyMu.Lock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, projection := range projections {
		previous, readErr := os.ReadFile(projection.path)
		existed := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, errors.Join(readErr, stage.finish(false))
		}
		if err := atomicWriteHistory(projection.path, projection.body); err != nil {
			return nil, errors.Join(err, stage.finish(false))
		}
		stage.files = append(stage.files, historyStagedFile{path: projection.path, previous: previous, existed: existed})
		if _, err := tx.Exec(`INSERT INTO run_history_files
			(session_id, start_run_id, end_run_id, file_path, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id, start_run_id) DO UPDATE SET
			end_run_id = excluded.end_run_id, file_path = excluded.file_path, updated_at = excluded.updated_at`,
			projection.sessionID, projection.start, projection.end, projection.name, now, now); err != nil {
			return nil, errors.Join(err, stage.finish(false))
		}
	}
	return stage, nil
}

func latestEventLogIDTx(tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM event_log`).Scan(&id)
	return id, err
}

func (r *AgentRuntime) stageEventHistorySinceTx(tx *sql.Tx, afterID int64) (*historyStage, error) {
	rows, err := tx.Query(`SELECT session_id, created_run_id, outcome_run_id FROM event_log WHERE id > ? ORDER BY id`, afterID)
	if err != nil {
		return nil, err
	}
	targets := make([]historyTarget, 0)
	for rows.Next() {
		var sessionID string
		var createdRunID int64
		var outcomeRunID sql.NullInt64
		if err := rows.Scan(&sessionID, &createdRunID, &outcomeRunID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		start, _ := historyRange(createdRunID)
		targets = append(targets, historyTarget{sessionID: sessionID, start: start})
		if outcomeRunID.Valid {
			start, _ = historyRange(outcomeRunID.Int64)
			targets = append(targets, historyTarget{sessionID: sessionID, start: start})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return r.stageHistoryTargetsTx(tx, targets)
}

func (s *historyStage) finish(committed bool) error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	defer s.runtime.historyMu.Unlock()
	if committed {
		return nil
	}
	var restoreErr error
	for index := len(s.files) - 1; index >= 0; index-- {
		file := s.files[index]
		if !file.existed {
			if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				restoreErr = errors.Join(restoreErr, err)
			}
			continue
		}
		restoreErr = errors.Join(restoreErr, atomicWriteHistory(file.path, file.previous))
	}
	return restoreErr
}

func atomicWriteHistory(path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".history-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return nil
}

func historyRunsTx(tx *sql.Tx, sessionID string, start, end int64) ([]historyRun, error) {
	rows, err := tx.Query(runSelect+` WHERE session_id = ? AND run_id BETWEEN ? AND ?
		AND status IN ('completed', 'failed', 'interrupted') ORDER BY run_id`, sessionID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]historyRun, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		memoryIDs, err := runMemoryIDsTx(tx, sessionID, run.RunID)
		if err != nil {
			return nil, err
		}
		events, err := runEventsTx(tx, sessionID, run.RunID)
		if err != nil {
			return nil, err
		}
		runs = append(runs, historyRun{
			RunID: run.RunID, Status: run.Status, Trigger: run.Trigger, Triggers: run.Triggers,
			Summary: run.Summary, Output: run.Output, Error: run.Error, Iterations: run.Progress, InitialContext: run.InitialContext,
			ModelCalls: run.ModelCalls, ToolCalls: run.ToolCalls, StartedAt: run.StartedAt,
			EndedAt: run.EndedAt, MemoryIDs: memoryIDs, EventLog: events,
		})
	}
	return runs, rows.Err()
}

func runMemoryIDsTx(tx *sql.Tx, sessionID string, runID int64) ([]string, error) {
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'memories'`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return []string{}, nil
	}
	rows, err := tx.Query(`SELECT id FROM memories WHERE session_id = ? AND run_id = ? ORDER BY candidate_index`, sessionID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func runEventsTx(tx *sql.Tx, sessionID string, runID int64) ([]historyEvent, error) {
	rows, err := tx.Query(`SELECT event_id, created_run_id, type, params_json, state_json,
		created_at, expires_at, outcome, outcome_run_id, detail_json, at FROM event_log
		WHERE session_id = ? AND (created_run_id = ? OR outcome_run_id = ?) ORDER BY id`, sessionID, runID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]historyEvent, 0)
	for rows.Next() {
		var event historyEvent
		var params string
		var state, detail sql.NullString
		var outcomeRunID sql.NullInt64
		if err := rows.Scan(&event.ID, &event.CreatedRunID, &event.Type, &params, &state, &event.CreatedAt, &event.ExpiresAt,
			&event.Outcome, &outcomeRunID, &detail, &event.At); err != nil {
			return nil, err
		}
		event.Params = json.RawMessage(params)
		event.State = json.RawMessage("null")
		event.Detail = json.RawMessage("null")
		if state.Valid {
			event.State = json.RawMessage(state.String)
		}
		if detail.Valid {
			event.Detail = json.RawMessage(detail.String)
		}
		if outcomeRunID.Valid {
			value := outcomeRunID.Int64
			event.OutcomeRunID = &value
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

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
	stage, err := r.stageRunHistoryTx(tx, sessionID, runID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		restoreErr := stage.finish(false)
		return false, errors.Join(err, restoreErr)
	}
	if err := stage.finish(true); err != nil {
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

func (r *AgentRuntime) serveRunHistory(w http.ResponseWriter, request *http.Request, sessionID, rawRunID string) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	runID, err := strconv.ParseInt(rawRunID, 10, 64)
	if err != nil || runID <= 0 {
		writeHTTPError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	run, err := r.runByID(request.Context(), sessionID, runID)
	if requestCancellationCausedPersistenceError(request.Context(), err) {
		writeHTTPError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	if err != nil {
		r.writeHistoryUnavailable(w, fmt.Errorf("resolve Run for History: %w", err))
		return
	}
	if err := request.Context().Err(); err != nil {
		writeHTTPError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
		return
	}
	if run.Status == "running" {
		writeHTTPError(w, http.StatusConflict, "HISTORY_NOT_READY", "Run History is available after the Run ends")
		return
	}
	var start, end int64
	var name string
	err = r.db.QueryRowContext(request.Context(), `SELECT start_run_id, end_run_id, file_path FROM run_history_files
		WHERE session_id = ? AND start_run_id <= ? AND end_run_id >= ?`, sessionID, runID, runID).Scan(&start, &end, &name)
	if err != nil {
		if requestCancellationCausedPersistenceError(request.Context(), err) {
			writeHTTPError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
			return
		}
		r.writeHistoryUnavailable(w, fmt.Errorf("resolve History index: %w", err))
		return
	}
	if err := request.Context().Err(); err != nil {
		writeHTTPError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before Run History was read")
		return
	}
	expectedStart, expectedEnd := historyRange(runID)
	if start != expectedStart || end != expectedEnd || name != historyFileName(sessionID, start, end) || filepath.Base(name) != name {
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
	if !validHistoryBody(body, sessionID, start, end, runID) {
		r.writeHistoryUnavailable(w, errors.New("Run History shard is invalid"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func validHistoryBody(body []byte, sessionID string, start, end, runID int64) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	var shardSessionID string
	var shardStart, shardEnd int64
	var runs []json.RawMessage
	expectedStart, expectedEnd := historyRange(runID)
	if !decodeHistoryField(fields, "session_id", &shardSessionID) ||
		!decodeHistoryField(fields, "start_run_id", &shardStart) ||
		!decodeHistoryField(fields, "end_run_id", &shardEnd) ||
		!decodeHistoryArray(fields, "runs", &runs) ||
		shardSessionID != sessionID || shardStart != start || shardEnd != end ||
		start != expectedStart || end != expectedEnd {
		return false
	}
	found := false
	previous := int64(0)
	for _, rawRun := range runs {
		run, valid := validHistoryRun(rawRun, start, end)
		if !valid {
			return false
		}
		if run.RunID <= previous || run.RunID < start || run.RunID > end {
			return false
		}
		previous = run.RunID
		if run.RunID == runID {
			found = true
		}
	}
	return found
}

func validHistoryRun(raw json.RawMessage, start, end int64) (historyRun, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return historyRun{}, false
	}
	required := []string{
		"run_id", "status", "trigger", "triggers", "summary", "output", "error", "iterations", "initial_context",
		"model_call_count", "tool_call_count", "start_time", "end_time", "memory_ids", "event_log",
	}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return historyRun{}, false
		}
	}
	var run historyRun
	if json.Unmarshal(raw, &run) != nil || run.RunID < start || run.RunID > end ||
		(run.Status != "completed" && run.Status != "failed" && run.Status != "interrupted") ||
		strings.TrimSpace(run.Trigger) == "" || run.ModelCalls < 0 || run.ToolCalls < 0 ||
		run.EndedAt == nil || !validHistoryTime(run.StartedAt) || !validHistoryTime(*run.EndedAt) ||
		!validNullableHistoryString(fields["summary"]) || !validNullableHistoryString(fields["output"]) ||
		!validNullableHistoryString(fields["error"]) || !validNullableHistoryArray(fields["triggers"]) {
		return historyRun{}, false
	}
	var iterations, memoryIDs, events []json.RawMessage
	if !decodeRawHistoryArray(fields["iterations"], &iterations) ||
		!decodeRawHistoryArray(fields["memory_ids"], &memoryIDs) ||
		!decodeRawHistoryArray(fields["event_log"], &events) {
		return historyRun{}, false
	}
	if !json.Valid(fields["initial_context"]) {
		return historyRun{}, false
	}
	if run.Status == "completed" {
		if run.Summary == nil || run.Output == nil || run.Error != nil {
			return historyRun{}, false
		}
	} else if run.Summary != nil || run.Output != nil || run.Error == nil {
		return historyRun{}, false
	}
	for _, rawID := range memoryIDs {
		var id string
		if json.Unmarshal(rawID, &id) != nil || !validMemoryID(id) {
			return historyRun{}, false
		}
	}
	for _, event := range events {
		if !validHistoryEvent(event) {
			return historyRun{}, false
		}
	}
	return run, true
}

func validHistoryEvent(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	required := []string{
		"id", "created_run_id", "type", "params", "state", "created_at", "expires_at",
		"outcome", "outcome_run_id", "detail", "at",
	}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return false
		}
	}
	var event historyEvent
	if json.Unmarshal(raw, &event) != nil || strings.TrimSpace(event.ID) == "" || event.CreatedRunID <= 0 ||
		strings.TrimSpace(event.Type) == "" || strings.TrimSpace(event.Outcome) == "" ||
		!json.Valid(fields["params"]) || !json.Valid(fields["state"]) || !json.Valid(fields["detail"]) ||
		!validHistoryTime(event.CreatedAt) || !validHistoryTime(event.ExpiresAt) || !validHistoryTime(event.At) {
		return false
	}
	if event.OutcomeRunID != nil && *event.OutcomeRunID <= 0 {
		return false
	}
	return true
}

func decodeHistoryField(fields map[string]json.RawMessage, name string, target any) bool {
	raw, exists := fields[name]
	return exists && json.Unmarshal(raw, target) == nil
}

func decodeHistoryArray(fields map[string]json.RawMessage, name string, target *[]json.RawMessage) bool {
	raw, exists := fields[name]
	return exists && decodeRawHistoryArray(raw, target)
}

func decodeRawHistoryArray(raw json.RawMessage, target *[]json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && trimmed[0] == '[' && json.Unmarshal(trimmed, target) == nil
}

func validNullableHistoryArray(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var values []json.RawMessage
	return decodeRawHistoryArray(raw, &values)
}

func validNullableHistoryString(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func validHistoryTime(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func (r *AgentRuntime) writeHistoryUnavailable(w http.ResponseWriter, err error) {
	r.latchPersistenceFault(err)
	writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Run History is unavailable")
}

func filterToolResultForHistory(toolName string, result json.RawMessage) json.RawMessage {
	if toolName == "get_run_history" {
		return filterRunHistoryToolResult(result)
	}
	if toolName != "get_ohlcv" {
		return result
	}
	var wrapper struct {
		OK     bool                       `json:"ok"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(result, &wrapper) != nil || !wrapper.OK || wrapper.Result == nil {
		return result
	}
	field := "rows"
	var candles []json.RawMessage
	if json.Unmarshal(wrapper.Result[field], &candles) != nil {
		field = "candles"
	}
	if json.Unmarshal(wrapper.Result[field], &candles) != nil {
		return result
	}
	delete(wrapper.Result, field)
	sample := make([]json.RawMessage, 0, 2)
	if len(candles) != 0 {
		sample = append(sample, candles[0])
		if len(candles) > 1 {
			sample = append(sample, candles[len(candles)-1])
		}
	}
	wrapper.Result["candle_count"], _ = json.Marshal(len(candles))
	wrapper.Result[field+"_sample"], _ = json.Marshal(sample)
	wrapper.Result["candles_omitted"], _ = json.Marshal(maxInt(0, len(candles)-len(sample)))
	filtered, err := json.Marshal(wrapper)
	if err != nil {
		return result
	}
	return filtered
}

func filterRunHistoryToolResult(result json.RawMessage) json.RawMessage {
	var wrapper struct {
		OK     bool                       `json:"ok"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(result, &wrapper) != nil || !wrapper.OK || wrapper.Result == nil {
		return result
	}
	if iterations, exists := wrapper.Result["iterations"]; exists {
		var entries []json.RawMessage
		if json.Unmarshal(iterations, &entries) == nil {
			wrapper.Result["iteration_count"], _ = json.Marshal(len(entries))
		}
		delete(wrapper.Result, "iterations")
	}
	if events, exists := wrapper.Result["event_log"]; exists {
		var entries []json.RawMessage
		if json.Unmarshal(events, &entries) == nil {
			wrapper.Result["event_count"], _ = json.Marshal(len(entries))
		}
		delete(wrapper.Result, "event_log")
	}
	if output, exists := wrapper.Result["output"]; exists {
		var text *string
		if json.Unmarshal(output, &text) == nil && text != nil {
			wrapper.Result["output_bytes"], _ = json.Marshal(len(*text))
			preview := *text
			if len(preview) > 2048 {
				preview = preview[:2048]
			}
			wrapper.Result["output_preview"], _ = json.Marshal(preview)
		}
		delete(wrapper.Result, "output")
	}
	delete(wrapper.Result, "initial_context")
	filtered, err := json.Marshal(wrapper)
	if err != nil {
		return result
	}
	return filtered
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func parseHistoryRun(body []byte, runID int64) (historyRun, error) {
	var shard historyShard
	if err := json.Unmarshal(body, &shard); err != nil {
		return historyRun{}, err
	}
	for _, run := range shard.Runs {
		if run.RunID == runID {
			return run, nil
		}
	}
	return historyRun{}, sql.ErrNoRows
}

func (r *AgentRuntime) historyToolError(err error) json.RawMessage {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return toolErrorResult("TOOL_INTERRUPTED", "Run History read was interrupted", "not_executed")
	}
	code := "HISTORY_UNAVAILABLE"
	message := "Run History is unavailable"
	if errors.Is(err, sql.ErrNoRows) {
		code, message = "RUN_NOT_FOUND", "Run does not exist in this Session"
	} else {
		r.latchPersistenceFault(err)
	}
	return toolErrorResult(code, message, "not_executed")
}
