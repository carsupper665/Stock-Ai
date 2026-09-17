package common

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	crashRecoveryReason  = "CRASH_RECOVERY: Agent Server exited before the Run reached a terminal state"
	serverShutdownReason = "SERVER_SHUTDOWN: interrupted by graceful Agent Server shutdown"
)

type recoveryLaunch struct {
	session Session
	runID   int64
}

func (r *AgentRuntime) recoverPersistedRuntime() error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	interrupted, err := runningRunTargetsTx(tx)
	if err == nil {
		_, err = tx.Exec(`UPDATE runs SET status='interrupted', summary=NULL, output=NULL,
			error=?, end_time=? WHERE status='running'`, crashRecoveryReason, now)
	}
	var launches []recoveryLaunch
	if err == nil {
		launches, err = createRecoveryRunsTx(tx, now)
	}
	var stage *historyStage
	if err == nil {
		stage, err = r.stageHistoryTargetsTx(tx, interrupted)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		if stage != nil {
			err = errors.Join(err, stage.finish(false))
		}
		return err
	}
	if err := stage.finish(true); err != nil {
		return err
	}
	r.mu.Lock()
	for i := range launches {
		r.launchRecovery(&launches[i])
	}
	r.mu.Unlock()
	return nil
}

func runningRunTargetsTx(tx *sql.Tx) ([]historyTarget, error) {
	rows, err := tx.Query(`SELECT session_id, run_id FROM runs WHERE status='running' ORDER BY session_id, run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]historyTarget, 0)
	for rows.Next() {
		var sessionID string
		var runID int64
		if err := rows.Scan(&sessionID, &runID); err != nil {
			return nil, err
		}
		start, _ := historyRange(runID)
		targets = append(targets, historyTarget{sessionID: sessionID, start: start})
	}
	return targets, rows.Err()
}

func createRecoveryRunsTx(tx *sql.Tx, now string) ([]recoveryLaunch, error) {
	rows, err := tx.Query(sessionSelect + ` WHERE status='running' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	launches := make([]recoveryLaunch, 0, len(sessions))
	for _, session := range sessions {
		runID := session.CurrentRunID + 1
		if _, err := tx.Exec(`INSERT INTO runs
			(session_id,run_id,status,trigger,progress_json,model_call_count,tool_call_count,start_time)
			VALUES (?,?,'running','server_recovery','[]',0,0,?)`, session.ID, runID, now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE sessions SET current_run_id=? WHERE id=? AND status='running'`, runID, session.ID); err != nil {
			return nil, err
		}
		session.CurrentRunID = runID
		launches = append(launches, recoveryLaunch{session: session, runID: runID})
	}
	return launches, nil
}

func (r *AgentRuntime) launchRecovery(launch *recoveryLaunch) {
	runContext, cancel := context.WithCancel(context.Background())
	active := &activeRun{runID: launch.runID, cancel: cancel, done: make(chan struct{})}
	r.active[launch.session.ID] = active
	r.wg.Add(1)
	go r.runAgent(runContext, launch.session, "server_recovery", launch.runID, active)
}

func (r *AgentRuntime) recoveryContext(sessionID string, runID int64) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	contextBody := map[string]any{
		"previous_run_id":  runID - 1,
		"status":           "unknown",
		"reason":           nil,
		"progress_summary": map[string]int{"model_call_count": 0, "tool_call_count": 0, "trace_count": 0},
		"recent_progress":  []restartProgressEntry{},
		"progress_omitted": 0,
		"triggers":         json.RawMessage("null"),
		"active_events":    []Event{},
		"pending_triggers": []json.RawMessage{},
		"data_gaps": []string{
			"Provider and Tool calls are never replayed after restart.",
			"An unfinished Tool result may be unknown; query current Backend state and Ledger before taking further trading action.",
		},
	}
	if runID > 1 {
		previous, previousErr := scanRun(tx.QueryRow(runSelect+` WHERE session_id=? AND run_id=?`, sessionID, runID-1))
		if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
			return nil, previousErr
		}
		if previousErr == nil {
			progressSummary, recentProgress, omitted, compactErr := compactRestartProgress(previous)
			if compactErr != nil {
				return nil, compactErr
			}
			contextBody["status"] = previous.Status
			contextBody["reason"] = previous.Error
			contextBody["progress_summary"] = progressSummary
			contextBody["recent_progress"] = recentProgress
			contextBody["progress_omitted"] = omitted
			contextBody["triggers"] = previous.Triggers
		}
	}
	events, err := sessionEvents(tx, sessionID)
	if err != nil {
		return nil, err
	}
	contextBody["active_events"] = events
	rows, err := tx.Query(`SELECT payload_json FROM pending_triggers WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	pending := make([]json.RawMessage, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !json.Valid([]byte(payload)) {
			_ = rows.Close()
			return nil, fmt.Errorf("invalid pending Trigger for Session %s", sessionID)
		}
		pending = append(pending, json.RawMessage(payload))
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	contextBody["pending_triggers"] = pending
	return contextBody, nil
}
