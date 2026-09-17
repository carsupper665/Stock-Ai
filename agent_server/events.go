package common

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	maxActiveEvents    = 10
	defaultTimerExpiry = time.Hour
	defaultEventExpiry = 24 * time.Hour
	// Fixed-width UTC layout: RFC3339Nano trims fractional zeros, which breaks SQLite
	// string ordering ("…00Z" sorts after "…00.5Z"), so every compared Event time uses this.
	eventTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"
)

var eventSchema = []string{
	`CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY, owner_session_id TEXT NOT NULL, created_run_id INTEGER NOT NULL,
		type TEXT NOT NULL, params_json TEXT NOT NULL, market TEXT, symbol TEXT,
		due_at TEXT, state_json TEXT, created_at TEXT NOT NULL, expires_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS events_owner ON events(owner_session_id, created_at, id)`,
	`CREATE TABLE IF NOT EXISTS pending_triggers (
		id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
		payload_json TEXT NOT NULL, created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS pending_triggers_session ON pending_triggers(session_id, id)`,
	`CREATE TABLE IF NOT EXISTS event_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, event_id TEXT NOT NULL,
		created_run_id INTEGER NOT NULL, type TEXT NOT NULL, params_json TEXT NOT NULL,
		state_json TEXT, created_at TEXT NOT NULL, expires_at TEXT NOT NULL,
		outcome TEXT NOT NULL, outcome_run_id INTEGER, detail_json TEXT, at TEXT NOT NULL
	)`,
}

type Event struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	CreatedRunID int64           `json:"created_run_id"`
	Type         string          `json:"type"`
	Params       json.RawMessage `json:"params"`
	State        json.RawMessage `json:"state"`
	CreatedAt    string          `json:"created_at"`
	ExpiresAt    string          `json:"expires_at"`
}

type eventRecord struct {
	Event
	market string
	symbol string
	dueAt  sql.NullString
}

type sessionEventClearance struct {
	Events  []Event           `json:"cleared_events"`
	Pending []json.RawMessage `json:"pending_triggers"`
}

type timerParams struct {
	At string `json:"at"`
}

type priceLevelParams struct {
	Market string  `json:"market"`
	Symbol string  `json:"symbol"`
	Price  float64 `json:"price"`
}

type priceChangeParams struct {
	Market    string  `json:"market"`
	Symbol    string  `json:"symbol"`
	BasePrice float64 `json:"base_price"`
	Direction string  `json:"direction"`
	Pct       float64 `json:"pct"`
}

type positionCloseParams struct {
	PositionID string `json:"position_id"`
}

func formatEventTime(value time.Time) string { return value.UTC().Format(eventTimeLayout) }

func parseEventTime(value string) (time.Time, error) { return time.Parse(eventTimeLayout, value) }

func eventToolSchemas() []map[string]any {
	return []map[string]any{
		{
			"name": "create_event",
			"description": "Create a one-shot wake-up condition for this Session. type=timer needs params.at (RFC3339, future); " +
				"price_above/price_below/price_cross_above/price_cross_below need params.symbol and params.price (market defaults to crypto); " +
				"price_change_pct needs params.symbol, params.base_price, params.direction (up|down) and params.pct; " +
				"position_close needs params.position_id and fires only after that bound-Account position has first been observed open and is later absent. " +
				"A position closed before its first successful observation does not fire this event. " +
				"expires_at is optional RFC3339 (timer default: at + 1h; other events default: now + 24h). At most 10 active events per Session.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"type": map[string]any{"type": "string", "enum": []string{
						"timer", "price_above", "price_below", "price_cross_above", "price_cross_below", "price_change_pct", "position_close",
					}},
					"params":     map[string]any{"type": "object"},
					"expires_at": map[string]any{"type": "string", "format": "date-time"},
				}, "required": []string{"type", "params"}, "additionalProperties": false,
			},
		},
		{
			"name": "delete_event", "description": "Delete one of this Session's active events by id.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"event_id": map[string]any{"type": "string"}},
				"required": []string{"event_id"}, "additionalProperties": false,
			},
		},
		{
			"name": "list_events", "description": "List this Session's active events.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
	}
}

func (r *AgentRuntime) executeEventTool(session Session, runID int64, call gatewayTool, now time.Time) json.RawMessage {
	switch call.Name {
	case "create_event":
		record, err := parseCreateEvent(call.Arguments, session.ID, runID, now)
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.createEventForRun(record)
	case "delete_event":
		var rawArguments struct {
			EventID json.RawMessage `json:"event_id"`
		}
		if !decodeToolArguments(call.Arguments, &rawArguments) {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", "event_id is required and no other field is accepted", "not_executed")
		}
		eventID, err := requiredToolString(rawArguments.EventID, "event_id")
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.deleteEventForRun(session.ID, runID, eventID, now)
	default:
		if !decodeToolArguments(call.Arguments, &struct{}{}) {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", "list_events accepts no arguments", "not_executed")
		}
		return r.listEventsForRun(session.ID)
	}
}

func parseCreateEvent(arguments json.RawMessage, sessionID string, runID int64, now time.Time) (eventRecord, error) {
	var rawArguments struct {
		Type      json.RawMessage `json:"type"`
		Params    json.RawMessage `json:"params"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return eventRecord{}, errors.New("only type, params, and expires_at are accepted")
	}
	eventType, err := requiredToolString(rawArguments.Type, "type")
	if err != nil {
		return eventRecord{}, err
	}
	if len(rawArguments.Params) == 0 || bytes.Equal(bytes.TrimSpace(rawArguments.Params), []byte("null")) {
		return eventRecord{}, errors.New("params must be an object")
	}
	id, err := newEventID()
	if err != nil {
		return eventRecord{}, err
	}
	record := eventRecord{Event: Event{
		ID: id, SessionID: sessionID, CreatedRunID: runID, Type: eventType,
		State: json.RawMessage("null"), CreatedAt: formatEventTime(now),
	}}
	defaultExpiry := now.Add(defaultEventExpiry)
	earliestExpiry := now
	switch eventType {
	case "timer":
		var raw struct {
			At json.RawMessage `json:"at"`
		}
		if !decodeToolArguments(rawArguments.Params, &raw) {
			return eventRecord{}, errors.New("timer params accept only at")
		}
		at, err := requiredRFC3339(raw.At, "at")
		if err != nil {
			return eventRecord{}, err
		}
		if !at.After(now) {
			return eventRecord{}, errors.New("at must be later than the current time")
		}
		record.Params, _ = json.Marshal(timerParams{At: formatEventTime(at)})
		record.dueAt = sql.NullString{String: formatEventTime(at), Valid: true}
		defaultExpiry, earliestExpiry = at.Add(defaultTimerExpiry), at
	case "price_above", "price_below", "price_cross_above", "price_cross_below":
		var raw struct {
			Market json.RawMessage `json:"market"`
			Symbol json.RawMessage `json:"symbol"`
			Price  json.RawMessage `json:"price"`
		}
		if !decodeToolArguments(rawArguments.Params, &raw) {
			return eventRecord{}, errors.New("price params accept only market, symbol, and price")
		}
		market, symbol, err := parseEventMarketSymbol(raw.Market, raw.Symbol)
		if err != nil {
			return eventRecord{}, err
		}
		price, err := requiredPositiveNumber(raw.Price, "price")
		if err != nil {
			return eventRecord{}, err
		}
		record.Params, _ = json.Marshal(priceLevelParams{Market: market, Symbol: symbol, Price: price})
		record.market, record.symbol = market, symbol
	case "price_change_pct":
		var raw struct {
			Market    json.RawMessage `json:"market"`
			Symbol    json.RawMessage `json:"symbol"`
			BasePrice json.RawMessage `json:"base_price"`
			Direction json.RawMessage `json:"direction"`
			Pct       json.RawMessage `json:"pct"`
		}
		if !decodeToolArguments(rawArguments.Params, &raw) {
			return eventRecord{}, errors.New("price_change_pct params accept only market, symbol, base_price, direction, and pct")
		}
		market, symbol, err := parseEventMarketSymbol(raw.Market, raw.Symbol)
		if err != nil {
			return eventRecord{}, err
		}
		basePrice, err := requiredPositiveNumber(raw.BasePrice, "base_price")
		if err != nil {
			return eventRecord{}, err
		}
		direction, err := requiredToolString(raw.Direction, "direction")
		if err != nil || (direction != "up" && direction != "down") {
			return eventRecord{}, errors.New("direction must be up or down")
		}
		pct, err := requiredPositiveNumber(raw.Pct, "pct")
		if err != nil {
			return eventRecord{}, err
		}
		record.Params, _ = json.Marshal(priceChangeParams{Market: market, Symbol: symbol, BasePrice: basePrice, Direction: direction, Pct: pct})
		record.market, record.symbol = market, symbol
	case "position_close":
		var raw struct {
			PositionID json.RawMessage `json:"position_id"`
		}
		if !decodeToolArguments(rawArguments.Params, &raw) {
			return eventRecord{}, errors.New("position_close params accept only position_id")
		}
		positionID, err := requiredToolString(raw.PositionID, "position_id")
		if err != nil {
			return eventRecord{}, err
		}
		record.Params, _ = json.Marshal(positionCloseParams{PositionID: positionID})
	default:
		return eventRecord{}, errors.New("type must be timer, price_above, price_below, price_cross_above, price_cross_below, price_change_pct, or position_close")
	}
	expiresAt := defaultExpiry
	if len(rawArguments.ExpiresAt) != 0 {
		if expiresAt, err = requiredRFC3339(rawArguments.ExpiresAt, "expires_at"); err != nil {
			return eventRecord{}, err
		}
	}
	if !expiresAt.After(earliestExpiry) {
		return eventRecord{}, errors.New("expires_at must be later than the current time and, for timers, later than at")
	}
	record.ExpiresAt = formatEventTime(expiresAt)
	return record, nil
}

func parseEventMarketSymbol(rawMarket, rawSymbol json.RawMessage) (string, string, error) {
	market, symbol, err := parseMarketSymbol(rawMarket, rawSymbol)
	if err != nil {
		return "", "", err
	}
	// Backend lowercases market and uppercases symbol; matching that keeps one sample per instrument.
	return strings.ToLower(market), strings.ToUpper(symbol), nil
}

func requiredRFC3339(raw json.RawMessage, name string) (time.Time, error) {
	value, err := requiredToolString(raw, name)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp", name)
	}
	return parsed.UTC(), nil
}

func requiredPositiveNumber(raw json.RawMessage, name string) (float64, error) {
	var value float64
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil ||
		math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive finite number", name)
	}
	return value, nil
}

func newEventID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("unable to create event ID")
	}
	return "ev_" + hex.EncodeToString(value), nil
}

func (r *AgentRuntime) createEventForRun(record eventRecord) json.RawMessage {
	return r.eventToolTransaction(record.SessionID, record.CreatedRunID, func(tx *sql.Tx) (json.RawMessage, error) {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE owner_session_id = ?`, record.SessionID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= maxActiveEvents {
			return toolErrorResult("EVENT_LIMIT_EXCEEDED", fmt.Sprintf("Session already has %d active events", maxActiveEvents), "not_executed"), nil
		}
		_, err := tx.Exec(`INSERT INTO events (id, owner_session_id, created_run_id, type, params_json, market, symbol, due_at, state_json, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`, record.ID, record.SessionID, record.CreatedRunID, record.Type,
			string(record.Params), nullableString(record.market), nullableString(record.symbol), record.dueAt, record.CreatedAt, record.ExpiresAt)
		if err != nil {
			return nil, err
		}
		return toolSuccessResult(map[string]any{"id": record.ID, "type": record.Type, "status": "created"}), nil
	})
}

func (r *AgentRuntime) deleteEventForRun(sessionID string, runID int64, eventID string, now time.Time) json.RawMessage {
	return r.eventToolTransaction(sessionID, runID, func(tx *sql.Tx) (json.RawMessage, error) {
		record, err := scanEventRecord(tx.QueryRow(eventSelect+` WHERE id = ? AND owner_session_id = ?`, eventID, sessionID))
		if errors.Is(err, sql.ErrNoRows) {
			return toolErrorResult("EVENT_NOT_FOUND", "event does not exist in this Session", "not_executed"), nil
		}
		if err != nil {
			return nil, err
		}
		if err := removeEventTx(tx, record, "deleted_by_agent", &runID, nil, now); err != nil {
			return nil, err
		}
		return toolSuccessResult(map[string]any{"id": record.ID, "type": record.Type, "status": "deleted"}), nil
	})
}

// eventToolTransaction runs one Event Tool mutation under r.mu in one transaction. The Run
// must still be running inside that transaction, so a concurrent hard stop (which clears the
// Session's Events in its own transaction) can never be followed by a late Event write.
func (r *AgentRuntime) eventToolTransaction(sessionID string, runID int64, mutate func(tx *sql.Tx) (json.RawMessage, error)) json.RawMessage {
	unavailable := func(outcome string) json.RawMessage {
		return toolErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", outcome)
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
		return toolErrorResult("RUN_NOT_RUNNING", "the current Run is no longer running", "not_executed")
	}
	var result json.RawMessage
	var logID int64
	if err == nil {
		logID, err = latestEventLogIDTx(tx)
	}
	if err == nil {
		result, err = mutate(tx)
	}
	var stage *historyStage
	if err == nil {
		stage, err = r.stageEventHistorySinceTx(tx, logID)
	}
	if err != nil {
		_ = tx.Rollback()
		err = errors.Join(err, stage.finish(false))
		r.latchPersistenceFaultLocked(err)
		return unavailable("not_executed")
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		r.latchPersistenceFaultLocked(errors.Join(err, stage.finish(false)))
		return unavailable("unknown")
	}
	if err := stage.finish(true); err != nil {
		r.latchPersistenceFaultLocked(err)
		return unavailable("unknown")
	}
	return result
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func runIsRunningTx(tx *sql.Tx, sessionID string, runID int64) (bool, error) {
	var status string
	err := tx.QueryRow(`SELECT status FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return status == "running", err
}

func toolSuccessResult(result any) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": result})
	return encoded
}

func (r *AgentRuntime) listEventsForRun(sessionID string) json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return toolErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", "not_executed")
	}
	events, err := sessionEvents(r.db, sessionID)
	if err != nil {
		r.latchPersistenceFaultLocked(err)
		return toolErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", "not_executed")
	}
	compact := make([]map[string]any, 0, len(events))
	for _, event := range events {
		compact = append(compact, compactEventToolView(event))
	}
	return toolSuccessResult(map[string]any{"events": compact})
}

func compactEventToolView(event Event) map[string]any {
	view := map[string]any{"id": event.ID, "type": event.Type}
	switch event.Type {
	case "timer":
		var params timerParams
		if json.Unmarshal(event.Params, &params) == nil {
			view["threshold"] = params.At
		}
	case "price_above", "price_below", "price_cross_above", "price_cross_below":
		var params priceLevelParams
		if json.Unmarshal(event.Params, &params) == nil {
			view["symbol"] = params.Symbol
			view["threshold"] = params.Price
			if params.Market != "crypto" {
				view["market"] = params.Market
			}
		}
		view["expires_at"] = event.ExpiresAt
	case "price_change_pct":
		var params priceChangeParams
		if json.Unmarshal(event.Params, &params) == nil {
			view["symbol"] = params.Symbol
			view["threshold"] = map[string]any{"base_price": params.BasePrice, "direction": params.Direction, "pct": params.Pct}
			if params.Market != "crypto" {
				view["market"] = params.Market
			}
		}
		view["expires_at"] = event.ExpiresAt
	case "position_close":
		var params positionCloseParams
		if json.Unmarshal(event.Params, &params) == nil {
			view["position_id"] = params.PositionID
		}
		view["expires_at"] = event.ExpiresAt
	}
	return view
}

const eventSelect = `SELECT id, owner_session_id, created_run_id, type, params_json, market, symbol,
	due_at, state_json, created_at, expires_at FROM events`

func scanEventRecord(row scanner) (eventRecord, error) {
	var record eventRecord
	var params string
	var market, symbol, state sql.NullString
	err := row.Scan(&record.ID, &record.SessionID, &record.CreatedRunID, &record.Type, &params,
		&market, &symbol, &record.dueAt, &state, &record.CreatedAt, &record.ExpiresAt)
	if err != nil {
		return eventRecord{}, err
	}
	record.Params = json.RawMessage(params)
	record.State = json.RawMessage("null")
	if state.Valid {
		record.State = json.RawMessage(state.String)
	}
	record.market, record.symbol = market.String, symbol.String
	return record, nil
}

type eventQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func queryEventRecords(querier eventQuerier, query string, args ...any) ([]eventRecord, error) {
	rows, err := querier.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]eventRecord, 0)
	for rows.Next() {
		record, err := scanEventRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func sessionEvents(querier eventQuerier, sessionID string) ([]Event, error) {
	records, err := queryEventRecords(querier, eventSelect+` WHERE owner_session_id = ? ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(records))
	for _, record := range records {
		events = append(events, record.Event)
	}
	return events, nil
}

// removeEventTx is the single one-shot terminal path for every Event outcome: RowsAffected proves
// the claim, and the log row preserves the snapshot after the active row is gone.
func removeEventTx(tx *sql.Tx, record eventRecord, outcome string, outcomeRunID *int64, detail json.RawMessage, at time.Time) error {
	result, err := tx.Exec(`DELETE FROM events WHERE id = ?`, record.ID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted != 1 {
		return fmt.Errorf("event %s was already consumed", record.ID)
	}
	var detailValue any
	if len(detail) != 0 {
		detailValue = string(detail)
	}
	_, err = tx.Exec(`INSERT INTO event_log (session_id, event_id, created_run_id, type, params_json, state_json,
		created_at, expires_at, outcome, outcome_run_id, detail_json, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SessionID, record.ID, record.CreatedRunID, record.Type, string(record.Params), string(record.State),
		record.CreatedAt, record.ExpiresAt, outcome, outcomeRunID, detailValue, formatEventTime(at))
	return err
}

func (r *AgentRuntime) clearSessionEventsTx(tx *sql.Tx, sessionID string, now time.Time, outcome string) (sessionEventClearance, error) {
	clearance := sessionEventClearance{Events: make([]Event, 0), Pending: make([]json.RawMessage, 0)}
	records, err := queryEventRecords(tx, eventSelect+` WHERE owner_session_id = ? ORDER BY created_at, id`, sessionID)
	if err != nil {
		return clearance, err
	}
	for _, record := range records {
		if err := removeEventTx(tx, record, outcome, nil, nil, now); err != nil {
			return clearance, err
		}
		clearance.Events = append(clearance.Events, record.Event)
	}
	clearance.Pending, err = pendingPayloadsTx(tx, sessionID, 0)
	if err != nil {
		return clearance, err
	}
	_, err = tx.Exec(`DELETE FROM pending_triggers WHERE session_id = ?`, sessionID)
	return clearance, err
}

func (r *AgentRuntime) serveEvents(w http.ResponseWriter, request *http.Request, sessionID string, rest []string) {
	switch {
	case len(rest) == 0 && request.Method == http.MethodGet:
		r.listEventsHTTP(w, request, sessionID)
	case len(rest) == 0:
		w.Header().Set("Allow", http.MethodGet)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only the Agent can create events")
	case len(rest) == 1 && request.Method == http.MethodDelete:
		r.deleteEventHTTP(w, request, sessionID, rest[0])
	case len(rest) == 1:
		w.Header().Set("Allow", http.MethodDelete)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	default:
		writeHTTPError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (r *AgentRuntime) listEventsHTTP(w http.ResponseWriter, request *http.Request, sessionID string) {
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	events, err := sessionEvents(r.db, sessionID)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list events")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (r *AgentRuntime) deleteEventHTTP(w http.ResponseWriter, request *http.Request, sessionID, eventID string) {
	status, code, msg, event := r.deleteEventByUser(request.Context(), sessionID, eventID)
	if code != "" {
		writeHTTPError(w, status, code, msg)
		return
	}
	writeRuntimeJSON(w, http.StatusOK, event)
}

// deleteEventByUser returns either an error triple (code != "") or the removed Event; the lock
// is released before any response is written so a slow client cannot stall the Runtime.
func (r *AgentRuntime) deleteEventByUser(ctx context.Context, sessionID, eventID string) (int, string, string, Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", Event{}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return r.mutationFailureLocked(ctx, nil, err)
	}
	if _, err := scanSession(tx.QueryRowContext(ctx, sessionSelect+` WHERE id = ?`, sessionID)); errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist", Event{}
	} else if err != nil {
		return r.mutationFailureLocked(ctx, tx, err)
	}
	record, err := scanEventRecord(tx.QueryRowContext(ctx, eventSelect+` WHERE id = ? AND owner_session_id = ?`, eventID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return http.StatusNotFound, "EVENT_NOT_FOUND", "event does not exist in this Session", Event{}
	}
	var logID int64
	if err == nil {
		logID, err = latestEventLogIDTx(tx)
	}
	if err == nil {
		err = removeEventTx(tx, record, "deleted_by_user", nil, nil, time.Now())
	}
	var stage *historyStage
	if err == nil {
		stage, err = r.stageEventHistorySinceTx(tx, logID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		return r.mutationFailureLocked(ctx, nil, errors.Join(err, stage.finish(false)))
	}
	if err := stage.finish(true); err != nil {
		return r.mutationFailureLocked(ctx, nil, err)
	}
	return http.StatusOK, "", "", record.Event
}

// mutationFailureLocked mirrors the Session mutation rule: a failure caused by the caller's
// own request context is a local 408, anything else latches the persistence fault (503).
// Caller holds r.mu.
func (r *AgentRuntime) mutationFailureLocked(ctx context.Context, tx *sql.Tx, err error) (int, string, string, Event) {
	if tx != nil {
		_ = tx.Rollback()
	}
	if requestCancellationCausedPersistenceError(ctx, err) {
		return http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before the mutation committed", Event{}
	}
	r.latchPersistenceFaultLocked(err)
	return http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", Event{}
}

func eventClock(clock Clock) Clock {
	if clock == nil {
		return realClock{}
	}
	return clock
}

// LocalEventTask owns expiry and timer claims. It performs no remote I/O, so slow price
// sampling can never delay it; expiry runs first so an Event expired at this instant never fires.
func (r *AgentRuntime) LocalEventTask(clock Clock, interval time.Duration) BackgroundTask {
	clock = eventClock(clock)
	return BackgroundTask{Name: "agent-events-local", Interval: interval, Run: func(ctx context.Context) error {
		now := clock.Now()
		return r.dispatchEventRound(ctx, func(tx *sql.Tx) ([]string, error) {
			if err := expireEventsTx(tx, now); err != nil {
				return nil, err
			}
			return fireDueTimersTx(tx, now)
		})
	}}
}

// PriceEventTask samples watched instruments outside r.mu, then claims matching price Events.
// The claim clock is read after sampling: an Event whose expiry arrived while Backend was
// answering is filtered out (its expired trace belongs to the local task), and fired_at is
// never earlier than the sample it reports. Without a new valid sample there is nothing to claim.
func (r *AgentRuntime) PriceEventTask(clock Clock, interval time.Duration) BackgroundTask {
	clock = eventClock(clock)
	lastSamples := make(map[string]priceSample)
	return BackgroundTask{Name: "agent-events-price", Interval: interval, Run: func(ctx context.Context) error {
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
			return matchPriceEventsTx(tx, now, samples)
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
	logID, err := latestEventLogIDTx(tx)
	var launches []*eventRunLaunch
	if err == nil {
		launches, err = r.admitFiredSessionsTx(tx, claim)
	}
	var stage *historyStage
	if err == nil {
		stage, err = r.stageEventHistorySinceTx(tx, logID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		return r.roundFailureLocked(ctx, nil, errors.Join(err, stage.finish(false)))
	}
	if err := stage.finish(true); err != nil {
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

func expireEventsTx(tx *sql.Tx, now time.Time) error {
	expired, err := queryEventRecords(tx, eventSelect+` WHERE expires_at <= ? ORDER BY created_at, id`, formatEventTime(now))
	if err != nil {
		return err
	}
	for _, record := range expired {
		if err := removeEventTx(tx, record, "expired", nil, nil, now); err != nil {
			return err
		}
	}
	return nil
}

func fireDueTimersTx(tx *sql.Tx, now time.Time) ([]string, error) {
	nowValue := formatEventTime(now)
	due, err := queryEventRecords(tx, eventSelect+` WHERE type = 'timer' AND due_at <= ? AND expires_at > ? ORDER BY created_at, id`, nowValue, nowValue)
	if err != nil {
		return nil, err
	}
	sessions := make([]string, 0)
	for _, record := range due {
		if err := fireEventTx(tx, record, map[string]any{"at": record.dueAt.String}, now); err != nil {
			return nil, err
		}
		sessions = appendUnique(sessions, record.SessionID)
	}
	return sessions, nil
}

// matchPriceEventsTx applies the round's samples to every unexpired price Event. The expiry
// filter uses the claim clock, so an Event whose expiry has arrived is never matched.
func matchPriceEventsTx(tx *sql.Tx, now time.Time, samples map[string]priceSample) ([]string, error) {
	sessions := make([]string, 0)
	watched, err := queryEventRecords(tx, eventSelect+` WHERE market IS NOT NULL AND symbol IS NOT NULL AND expires_at > ? ORDER BY created_at, id`, formatEventTime(now))
	if err != nil {
		return nil, err
	}
	for _, record := range watched {
		sample, ok := samples[record.market+"/"+record.symbol]
		if !ok {
			continue
		}
		matched, newState, err := evaluatePriceEvent(record, sample)
		if err != nil {
			return nil, err
		}
		if matched != nil {
			if err := fireEventTx(tx, record, matched, now); err != nil {
				return nil, err
			}
			sessions = appendUnique(sessions, record.SessionID)
			continue
		}
		if newState != nil {
			if _, err := tx.Exec(`UPDATE events SET state_json = ? WHERE id = ?`, newState.encode(), record.ID); err != nil {
				return nil, err
			}
		}
	}
	return sessions, nil
}

func fireEventTx(tx *sql.Tx, record eventRecord, matched map[string]any, now time.Time) error {
	detail, err := json.Marshal(matched)
	if err != nil {
		return fmt.Errorf("encode Event %s match facts: %w", record.ID, err)
	}
	payload, err := json.Marshal(map[string]any{
		"type": record.Type, "event_id": record.ID, "created_run_id": record.CreatedRunID,
		"params": record.Params, "expires_at": record.ExpiresAt, "fired_at": formatEventTime(now), "matched": matched,
	})
	if err != nil {
		return fmt.Errorf("encode Event %s Trigger: %w", record.ID, err)
	}
	if err := removeEventTx(tx, record, "fired", nil, detail, now); err != nil {
		return err
	}
	return enqueueTriggerTx(tx, record.SessionID, payload, now)
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

func appendUnique(values []string, wanted string) []string {
	for _, value := range values {
		if value == wanted {
			return values
		}
	}
	return append(values, wanted)
}
