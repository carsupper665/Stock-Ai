package common

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"stock-ai/agent-server/internal/eventloop"
	"stock-ai/agent-server/internal/events"
	"stock-ai/agent-server/internal/history"
	"stock-ai/agent-server/internal/server"
	"stock-ai/agent-server/internal/tools"
)

func (r *AgentRuntime) executeEventTool(session Session, runID int64, call gatewayTool, now time.Time) json.RawMessage {
	switch call.Name {
	case "create_event":
		record, err := events.ParseCreateEvent(call.Arguments, session.ID, runID, now)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.createEventForRun(record)
	case "delete_event":
		var rawArguments struct {
			EventID json.RawMessage `json:"event_id"`
		}
		if !tools.DecodeArguments(call.Arguments, &rawArguments) {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", "event_id is required and no other field is accepted", "not_executed")
		}
		eventID, err := tools.RequiredString(rawArguments.EventID, "event_id")
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.deleteEventForRun(session.ID, runID, eventID, now)
	default:
		if !tools.DecodeArguments(call.Arguments, &struct{}{}) {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", "list_events accepts no arguments", "not_executed")
		}
		return r.listEventsForRun(session.ID)
	}
}

func (r *AgentRuntime) createEventForRun(record events.Record) json.RawMessage {
	return r.eventToolTransaction(record.SessionID, record.CreatedRunID, events.CreateForRun(record))
}

func (r *AgentRuntime) deleteEventForRun(sessionID string, runID int64, eventID string, now time.Time) json.RawMessage {
	return r.eventToolTransaction(sessionID, runID, events.DeleteForRun(sessionID, runID, eventID, now))
}

// eventToolTransaction runs one Event Tool mutation under r.mu in one transaction. The Run
// must still be running inside that transaction, so a concurrent hard stop (which clears the
// Session's Events in its own transaction) can never be followed by a late Event write.
func (r *AgentRuntime) eventToolTransaction(sessionID string, runID int64, mutate func(tx *sql.Tx) (json.RawMessage, error)) json.RawMessage {
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
	running, err := runIsRunningTx(tx, sessionID, runID)
	if err == nil && !running {
		_ = tx.Rollback()
		return tools.ErrorResult("RUN_NOT_RUNNING", "the current Run is no longer running", "not_executed")
	}
	var result json.RawMessage
	var logID int64
	if err == nil {
		logID, err = history.LatestEventLogIDTx(tx)
	}
	if err == nil {
		result, err = mutate(tx)
	}
	var stage *history.Stage
	if err == nil {
		stage, err = history.StageEventSince(tx, &r.historyMu, r.config.HistoryDirectory, logID)
	}
	if err != nil {
		_ = tx.Rollback()
		err = errors.Join(err, stage.Finish(false))
		r.latchPersistenceFaultLocked(err)
		return unavailable("not_executed")
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		r.latchPersistenceFaultLocked(errors.Join(err, stage.Finish(false)))
		return unavailable("unknown")
	}
	if err := stage.Finish(true); err != nil {
		r.latchPersistenceFaultLocked(err)
		return unavailable("unknown")
	}
	return result
}

func runIsRunningTx(tx *sql.Tx, sessionID string, runID int64) (bool, error) {
	var status string
	err := tx.QueryRow("SELECT status FROM runs WHERE session_id = ? AND run_id = ?", sessionID, runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return status == "running", err
}

func (r *AgentRuntime) listEventsForRun(sessionID string) json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return tools.ErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", "not_executed")
	}
	list, err := events.SessionEvents(r.db, sessionID)
	if err != nil {
		r.latchPersistenceFaultLocked(err)
		return tools.ErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", "not_executed")
	}
	compact := make([]map[string]any, 0, len(list))
	for _, event := range list {
		compact = append(compact, events.CompactView(event))
	}
	return tools.SuccessResult(map[string]any{"events": compact})
}

func (r *AgentRuntime) serveEvents(w http.ResponseWriter, request *http.Request, sessionID string, rest []string) {
	switch {
	case len(rest) == 0 && request.Method == http.MethodGet:
		r.listEventsHTTP(w, request, sessionID)
	case len(rest) == 0:
		w.Header().Set("Allow", http.MethodGet)
		server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only the Agent can create events")
	case len(rest) == 1 && request.Method == http.MethodDelete:
		r.deleteEventHTTP(w, request, sessionID, rest[0])
	case len(rest) == 1:
		w.Header().Set("Allow", http.MethodDelete)
		server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	default:
		server.WriteJSONError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (r *AgentRuntime) listEventsHTTP(w http.ResponseWriter, request *http.Request, sessionID string) {
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	list, err := events.SessionEvents(r.db, sessionID)
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list events")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"events": list})
}

func (r *AgentRuntime) deleteEventHTTP(w http.ResponseWriter, request *http.Request, sessionID, eventID string) {
	status, code, msg, event := r.deleteEventByUser(request.Context(), sessionID, eventID)
	if code != "" {
		server.WriteJSONError(w, status, code, msg)
		return
	}
	writeRuntimeJSON(w, http.StatusOK, event)
}

// deleteEventByUser returns either an error triple (code != "") or the removed Event; the lock
// is released before any response is written so a slow client cannot stall the Runtime.
func (r *AgentRuntime) deleteEventByUser(ctx context.Context, sessionID, eventID string) (int, string, string, events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", events.Event{}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return r.mutationFailureLocked(ctx, nil, err)
	}
	if _, err := scanSession(tx.QueryRowContext(ctx, sessionSelect+" WHERE id = ?", sessionID)); errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist", events.Event{}
	} else if err != nil {
		return r.mutationFailureLocked(ctx, tx, err)
	}
	record, err := events.ScanRecord(tx.QueryRowContext(ctx, events.Select+" WHERE id = ? AND owner_session_id = ?", eventID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return http.StatusNotFound, "EVENT_NOT_FOUND", "event does not exist in this Session", events.Event{}
	}
	var logID int64
	if err == nil {
		logID, err = history.LatestEventLogIDTx(tx)
	}
	if err == nil {
		err = events.RemoveRecordTx(tx, record, "deleted_by_user", nil, nil, time.Now())
	}
	var stage *history.Stage
	if err == nil {
		stage, err = history.StageEventSince(tx, &r.historyMu, r.config.HistoryDirectory, logID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		return r.mutationFailureLocked(ctx, nil, errors.Join(err, stage.Finish(false)))
	}
	if err := stage.Finish(true); err != nil {
		return r.mutationFailureLocked(ctx, nil, err)
	}
	return http.StatusOK, "", "", record.Event
}

// mutationFailureLocked mirrors the Session mutation rule: a failure caused by the caller's
// own request context is a local 408, anything else latches the persistence fault (503).
// Caller holds r.mu.
func (r *AgentRuntime) mutationFailureLocked(ctx context.Context, tx *sql.Tx, err error) (int, string, string, events.Event) {
	if tx != nil {
		_ = tx.Rollback()
	}
	if requestCancellationCausedPersistenceError(ctx, err) {
		return http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before the mutation committed", events.Event{}
	}
	r.latchPersistenceFaultLocked(err)
	return http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", events.Event{}
}

func eventClock(clock eventloop.Clock) eventloop.Clock {
	if clock == nil {
		return eventloop.RealClock()
	}
	return clock
}

// LocalEventTask owns expiry and timer claims. It performs no remote I/O, so slow price
// sampling can never delay it; expiry runs first so an Event expired at this instant never fires.
func (r *AgentRuntime) LocalEventTask(clock eventloop.Clock, interval time.Duration) server.BackgroundTask {
	clock = eventClock(clock)
	return server.BackgroundTask{Name: "agent-events-local", Interval: interval, Run: func(ctx context.Context) error {
		now := clock.Now()
		return r.dispatchEventRound(ctx, func(tx *sql.Tx) ([]string, error) {
			if err := events.ExpireTx(tx, now); err != nil {
				return nil, err
			}
			return events.FireTimersTx(tx, now)
		})
	}}
}

// PriceEventTask samples watched instruments outside r.mu, then claims matching price Events.
// The claim clock is read after sampling: an Event whose expiry arrived while Backend was
// answering is filtered out (its expired trace belongs to the local task), and fired_at is
// never earlier than the sample it reports. Without a new valid sample there is nothing to claim.
func (r *AgentRuntime) PriceEventTask(clock eventloop.Clock, interval time.Duration) server.BackgroundTask {
	clock = eventClock(clock)
	lastSamples := make(map[string]events.PriceSample)
	return server.BackgroundTask{Name: "agent-events-price", Interval: interval, Run: func(ctx context.Context) error {
		watched, err := r.watchedInstruments(ctx, clock.Now())
		if err != nil {
			return err
		}
		samples := r.samplePrices(ctx, clock, watched, lastSamples)
		if len(samples) == 0 {
			return nil
		}
		now := clock.Now()
		return r.dispatchEventRound(ctx, func(tx *sql.Tx) ([]string, error) {
			return events.MatchPriceTx(tx, now, samples)
		})
	}}
}

// dispatchEventRound runs one claim under r.mu in one transaction: claim removes fired Events
// and enqueues their triggers, then every fired idle Session gets its next Run from the queue.
// Commit precedes Run goroutine start; any failure rolls the whole round back.
func (r *AgentRuntime) dispatchEventRound(ctx context.Context, claim func(*sql.Tx) ([]string, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return r.roundFailureLocked(ctx, nil, err)
	}
	logID, err := history.LatestEventLogIDTx(tx)
	var launches []*eventRunLaunch
	if err == nil {
		launches, err = r.admitFiredSessionsTx(tx, claim)
	}
	var stage *history.Stage
	if err == nil {
		stage, err = history.StageEventSince(tx, &r.historyMu, r.config.HistoryDirectory, logID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		return r.roundFailureLocked(ctx, nil, errors.Join(err, stage.Finish(false)))
	}
	if err := stage.Finish(true); err != nil {
		return r.roundFailureLocked(ctx, nil, err)
	}
	for _, launch := range launches {
		r.launchRunLocked(launch)
	}
	return nil
}

func (r *AgentRuntime) admitFiredSessionsTx(tx *sql.Tx, claim func(*sql.Tx) ([]string, error)) ([]*eventRunLaunch, error) {
	firedSessions, err := claim(tx)
	if err != nil {
		return nil, err
	}
	launches := make([]*eventRunLaunch, 0)
	for _, sessionID := range firedSessions {
		if _, busy := r.active[sessionID]; busy {
			continue
		}
		launch, err := r.startPendingRunTx(tx, sessionID)
		if err != nil {
			return nil, err
		}
		if launch != nil {
			launches = append(launches, launch)
		}
	}
	return launches, nil
}

func (r *AgentRuntime) roundFailureLocked(ctx context.Context, tx *sql.Tx, err error) error {
	if tx != nil {
		_ = tx.Rollback()
	}
	if !requestCancellationCausedPersistenceError(ctx, err) {
		r.latchPersistenceFaultLocked(err)
	}
	return err
}
