// Package history implements the Run History store: shard files that mirror
// terminal runs, the transactional staging that keeps shard files and the
// run_history_files index consistent, shard validation, and the filtering
// applied to tool results before they enter a Run's recorded context.
package history

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"stock-ai/agent-server/internal/events"
	"stock-ai/agent-server/internal/tools"
)

// ShardSize is the number of runs held by one history shard file.
const ShardSize int64 = 20

var Schema = []string{
	`CREATE TABLE IF NOT EXISTS run_history_files (
		session_id TEXT NOT NULL, start_run_id INTEGER NOT NULL, end_run_id INTEGER NOT NULL,
		file_path TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (session_id, start_run_id), UNIQUE (file_path)
	)`,
}

// Shard is the on-disk JSON document for one history shard file.
type Shard struct {
	SessionID  string `json:"session_id"`
	StartRunID int64  `json:"start_run_id"`
	EndRunID   int64  `json:"end_run_id"`
	Runs       []Run  `json:"runs"`
}

// Run is the recorded form of one terminal run inside a history shard.
type Run struct {
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
	EventLog       []Event         `json:"event_log"`
}

// Event is the recorded form of one event log row inside a history shard.
type Event struct {
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

// Range returns the shard start and end run ids for runID.
func Range(runID int64) (int64, int64) {
	start := ((runID - 1) / ShardSize * ShardSize) + 1
	return start, start + ShardSize - 1
}

// FileName returns the shard file name for the range.
func FileName(sessionID string, start, end int64) string {
	return fmt.Sprintf("%s_%d-%d.json", sessionID, start, end)
}

// ToolArgument decodes the get_run_history tool arguments.
func ToolArgument(arguments json.RawMessage) (int64, error) {
	var raw struct {
		RunID json.RawMessage `json:"run_id"`
	}
	if !tools.DecodeArguments(arguments, &raw) {
		return 0, errors.New("run_id is required and no other field is accepted")
	}
	var runID int64
	if len(raw.RunID) == 0 || bytes.Equal(bytes.TrimSpace(raw.RunID), []byte("null")) || json.Unmarshal(raw.RunID, &runID) != nil || runID < 1 {
		return 0, errors.New("run_id must be a positive integer")
	}
	return runID, nil
}

// ClearanceRunIDs returns the current run plus the runs referenced by an
// event clearance so their shards can be restaged.
func ClearanceRunIDs(currentRunID int64, clearance events.Clearance) []int64 {
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

const runSelect = `SELECT run_id, status, trigger, triggers_json, summary, output, error,
	progress_json, initial_context_json, model_call_count, tool_call_count, start_time, end_time FROM runs`

type scanner interface {
	Scan(dest ...any) error
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func scanRunRow(row scanner) (Run, error) {
	var run Run
	var triggers, summary, output, runError, initialContext, endedAt sql.NullString
	var progress string
	err := row.Scan(&run.RunID, &run.Status, &run.Trigger, &triggers, &summary, &output,
		&runError, &progress, &initialContext, &run.ModelCalls, &run.ToolCalls, &run.StartedAt, &endedAt)
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
	run.Iterations = json.RawMessage(progress)
	run.InitialContext = json.RawMessage("null")
	if initialContext.Valid {
		run.InitialContext = json.RawMessage(initialContext.String)
	}
	return run, nil
}

// RunsInTx returns the recorded runs in [start, end] that have ended.
func RunsInTx(tx *sql.Tx, sessionID string, start, end int64) ([]Run, error) {
	rows, err := tx.Query(runSelect+` WHERE session_id = ? AND run_id BETWEEN ? AND ?
		AND status IN ('completed', 'failed', 'interrupted') ORDER BY run_id`, sessionID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRunRow(rows)
		if err != nil {
			return nil, err
		}
		memoryIDs, err := runMemoryIDsTx(tx, sessionID, run.RunID)
		if err != nil {
			return nil, err
		}
		eventLog, err := runEventsTx(tx, sessionID, run.RunID)
		if err != nil {
			return nil, err
		}
		run.MemoryIDs = memoryIDs
		run.EventLog = eventLog
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func runMemoryIDsTx(tx *sql.Tx, sessionID string, runID int64) ([]string, error) {
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'run_memories'`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return []string{}, nil
	}
	rows, err := tx.Query(`SELECT memory_id FROM run_memories WHERE session_id = ? AND run_id = ? ORDER BY rowid`, sessionID, runID)
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

func runEventsTx(tx *sql.Tx, sessionID string, runID int64) ([]Event, error) {
	rows, err := tx.Query(`SELECT event_id, created_run_id, type, params_json, state_json,
		created_at, expires_at, outcome, outcome_run_id, detail_json, at FROM event_log
		WHERE session_id = ? AND (created_run_id = ? OR outcome_run_id = ?) ORDER BY id`, sessionID, runID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
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
