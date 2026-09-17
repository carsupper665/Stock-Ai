package common

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const maxTriggersPerRun = 10

type eventRunLaunch struct {
	session Session
	runID   int64
}

// enqueueTriggerTx appends one trigger to the Session's ordered list. The AUTOINCREMENT id is
// the Server-generated arrival sequence; it is never reused, so order survives crashes.
func enqueueTriggerTx(tx *sql.Tx, sessionID string, payload json.RawMessage, now time.Time) error {
	_, err := tx.Exec(`INSERT INTO pending_triggers (session_id, payload_json, created_at) VALUES (?, ?, ?)`,
		sessionID, string(payload), formatEventTime(now))
	return err
}

func pendingPayloadsTx(tx *sql.Tx, sessionID string, limit int) ([]json.RawMessage, error) {
	query := `SELECT payload_json FROM pending_triggers WHERE session_id = ? ORDER BY id`
	args := []any{sessionID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	payloads := make([]json.RawMessage, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if !json.Valid([]byte(payload)) {
			return nil, fmt.Errorf("invalid pending Trigger for Session %s", sessionID)
		}
		payloads = append(payloads, json.RawMessage(payload))
	}
	return payloads, rows.Err()
}

// startPendingRunTx consumes the next batch (at most maxTriggersPerRun, arrival order, no
// reordering) into a new running Run of a running Session. Run insert, current_run_id
// increment, and queue deletion share the caller's transaction so a crash can neither lose
// nor duplicate a trigger. It returns nil when the Session is not running or the queue is empty.
func (r *AgentRuntime) startPendingRunTx(tx *sql.Tx, sessionID string) (*eventRunLaunch, error) {
	session, err := scanSession(tx.QueryRow(sessionSelect+` WHERE id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && session.Status != "running") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	payloads, err := pendingPayloadsTx(tx, sessionID, maxTriggersPerRun)
	if err != nil || len(payloads) == 0 {
		return nil, err
	}
	encoded, err := json.Marshal(payloads)
	if err != nil {
		return nil, fmt.Errorf("encode pending Triggers for Session %s: %w", sessionID, err)
	}
	runID := session.CurrentRunID + 1
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO runs
		(session_id, run_id, status, trigger, triggers_json, progress_json, model_call_count, tool_call_count, start_time)
		VALUES (?, ?, 'running', 'event', ?, '[]', 0, 0, ?)`, sessionID, runID, string(encoded), now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE sessions SET current_run_id = ? WHERE id = ?`, runID, sessionID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM pending_triggers WHERE id IN (
		SELECT id FROM pending_triggers WHERE session_id = ? ORDER BY id LIMIT ?)`, sessionID, len(payloads)); err != nil {
		return nil, err
	}
	session.CurrentRunID = runID
	return &eventRunLaunch{session: session, runID: runID}, nil
}

// launchRunLocked registers the committed Run exactly as startSession does. Caller holds r.mu.
func (r *AgentRuntime) launchRunLocked(launch *eventRunLaunch) {
	runContext, cancel := context.WithCancel(context.Background())
	active := &activeRun{runID: launch.runID, cancel: cancel, done: make(chan struct{})}
	r.active[launch.session.ID] = active
	r.wg.Add(1)
	go r.runAgent(runContext, launch.session, "event", launch.runID, active)
}

// dispatchPendingLocked starts the next queued batch once a Run has left. Caller holds r.mu
// with the finished Run already removed from r.active. Stopped, deleted, closing, and faulted
// states dispatch nothing; queued rows then stay in SQLite for restart or recovery context.
func (r *AgentRuntime) dispatchPendingLocked(sessionID string) {
	if r.closed || r.persistenceErr != nil {
		return
	}
	if _, busy := r.active[sessionID]; busy {
		return
	}
	tx, err := r.db.Begin()
	if err != nil {
		r.latchPersistenceFaultLocked(err)
		return
	}
	launch, err := r.startPendingRunTx(tx, sessionID)
	if err == nil && launch == nil {
		_ = tx.Rollback()
		return
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		r.latchPersistenceFaultLocked(err)
		return
	}
	r.launchRunLocked(launch)
}

func (r *AgentRuntime) runTriggers(sessionID string, runID int64) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var triggers sql.NullString
	err := r.db.QueryRow(`SELECT triggers_json FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&triggers)
	if err != nil {
		return nil, err
	}
	if !triggers.Valid {
		return json.RawMessage("[]"), nil
	}
	return json.RawMessage(triggers.String), nil
}
