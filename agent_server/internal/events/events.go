// Package events owns the one-shot wake-up Event domain: its types, argument parsing,
// SQLite schema and claim transactions. Claim functions are pure transaction logic; the
// composition root wraps them with locking, history staging, and Run launch.
package events

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"stock-ai/agent-server/internal/tools"
)

const (
	maxActiveEvents    = 10
	defaultTimerExpiry = time.Hour
	defaultEventExpiry = 24 * time.Hour
	// Fixed-width UTC layout: RFC3339Nano trims fractional zeros, which breaks SQLite
	// string ordering ("…00Z" sorts after "…00.5Z"), so every compared Event time uses this.
	eventTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"
)

// Schema is the Event domain's DDL, run by the composition root at startup.
var Schema = []string{
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

// Record is an active Event row plus the market/symbol projection and due time used by
// the price and position engines.
type Record struct {
	Event
	market string
	symbol string
	dueAt  sql.NullString
}

type Clearance struct {
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

func FormatTime(value time.Time) string { return value.UTC().Format(eventTimeLayout) }

func parseEventTime(value string) (time.Time, error) { return time.Parse(eventTimeLayout, value) }

// ParseCreateEvent validates and normalizes one create_event Tool call into a ready Record.
func ParseCreateEvent(arguments json.RawMessage, sessionID string, runID int64, now time.Time) (Record, error) {
	var rawArguments struct {
		Type      json.RawMessage `json:"type"`
		Params    json.RawMessage `json:"params"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if !tools.DecodeArguments(arguments, &rawArguments) {
		return Record{}, errors.New("only type, params, and expires_at are accepted")
	}
	eventType, err := tools.RequiredString(rawArguments.Type, "type")
	if err != nil {
		return Record{}, err
	}
	if len(rawArguments.Params) == 0 || bytes.Equal(bytes.TrimSpace(rawArguments.Params), []byte("null")) {
		return Record{}, errors.New("params must be an object")
	}
	id, err := newEventID()
	if err != nil {
		return Record{}, err
	}
	record := Record{Event: Event{
		ID: id, SessionID: sessionID, CreatedRunID: runID, Type: eventType,
		State: json.RawMessage("null"), CreatedAt: FormatTime(now),
	}}
	defaultExpiry := now.Add(defaultEventExpiry)
	earliestExpiry := now
	switch eventType {
	case "timer":
		var raw struct {
			At json.RawMessage `json:"at"`
		}
		if !tools.DecodeArguments(rawArguments.Params, &raw) {
			return Record{}, errors.New("timer params accept only at")
		}
		at, err := requiredRFC3339(raw.At, "at")
		if err != nil {
			return Record{}, err
		}
		if !at.After(now) {
			return Record{}, errors.New("at must be later than the current time")
		}
		record.Params, _ = json.Marshal(timerParams{At: FormatTime(at)})
		record.dueAt = sql.NullString{String: FormatTime(at), Valid: true}
		defaultExpiry, earliestExpiry = at.Add(defaultTimerExpiry), at
	case "price_above", "price_below", "price_cross_above", "price_cross_below":
		var raw struct {
			Market json.RawMessage `json:"market"`
			Symbol json.RawMessage `json:"symbol"`
			Price  json.RawMessage `json:"price"`
		}
		if !tools.DecodeArguments(rawArguments.Params, &raw) {
			return Record{}, errors.New("price params accept only market, symbol, and price")
		}
		market, symbol, err := parseEventMarketSymbol(raw.Market, raw.Symbol)
		if err != nil {
			return Record{}, err
		}
		price, err := requiredPositiveNumber(raw.Price, "price")
		if err != nil {
			return Record{}, err
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
		if !tools.DecodeArguments(rawArguments.Params, &raw) {
			return Record{}, errors.New("price_change_pct params accept only market, symbol, base_price, direction, and pct")
		}
		market, symbol, err := parseEventMarketSymbol(raw.Market, raw.Symbol)
		if err != nil {
			return Record{}, err
		}
		basePrice, err := requiredPositiveNumber(raw.BasePrice, "base_price")
		if err != nil {
			return Record{}, err
		}
		direction, err := tools.RequiredString(raw.Direction, "direction")
		if err != nil || (direction != "up" && direction != "down") {
			return Record{}, errors.New("direction must be up or down")
		}
		pct, err := requiredPositiveNumber(raw.Pct, "pct")
		if err != nil {
			return Record{}, err
		}
		record.Params, _ = json.Marshal(priceChangeParams{Market: market, Symbol: symbol, BasePrice: basePrice, Direction: direction, Pct: pct})
		record.market, record.symbol = market, symbol
	case "position_close":
		var raw struct {
			PositionID json.RawMessage `json:"position_id"`
		}
		if !tools.DecodeArguments(rawArguments.Params, &raw) {
			return Record{}, errors.New("position_close params accept only position_id")
		}
		positionID, err := tools.RequiredString(raw.PositionID, "position_id")
		if err != nil {
			return Record{}, err
		}
		record.Params, _ = json.Marshal(positionCloseParams{PositionID: positionID})
	default:
		return Record{}, errors.New("type must be timer, price_above, price_below, price_cross_above, price_cross_below, price_change_pct, or position_close")
	}
	expiresAt := defaultExpiry
	if len(rawArguments.ExpiresAt) != 0 {
		if expiresAt, err = requiredRFC3339(rawArguments.ExpiresAt, "expires_at"); err != nil {
			return Record{}, err
		}
	}
	if !expiresAt.After(earliestExpiry) {
		return Record{}, errors.New("expires_at must be later than the current time and, for timers, later than at")
	}
	record.ExpiresAt = FormatTime(expiresAt)
	return record, nil
}

func parseEventMarketSymbol(rawMarket, rawSymbol json.RawMessage) (string, string, error) {
	market, symbol, err := tools.ParseMarketSymbol(rawMarket, rawSymbol)
	if err != nil {
		return "", "", err
	}
	// Backend lowercases market and uppercases symbol; matching that keeps one sample per instrument.
	return strings.ToLower(market), strings.ToUpper(symbol), nil
}

func requiredRFC3339(raw json.RawMessage, name string) (time.Time, error) {
	value, err := tools.RequiredString(raw, name)
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

// CreateForRun returns the transaction mutation that inserts one parsed Event, enforcing the
// per-Session active limit. The caller runs it inside its locked persistence transaction.
func CreateForRun(record Record) func(tx *sql.Tx) (json.RawMessage, error) {
	return func(tx *sql.Tx) (json.RawMessage, error) {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE owner_session_id = ?`, record.SessionID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= maxActiveEvents {
			return tools.ErrorResult("EVENT_LIMIT_EXCEEDED", fmt.Sprintf("Session already has %d active events", maxActiveEvents), "not_executed"), nil
		}
		_, err := tx.Exec(`INSERT INTO events (id, owner_session_id, created_run_id, type, params_json, market, symbol, due_at, state_json, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`, record.ID, record.SessionID, record.CreatedRunID, record.Type,
			string(record.Params), nullableString(record.market), nullableString(record.symbol), record.dueAt, record.CreatedAt, record.ExpiresAt)
		if err != nil {
			return nil, err
		}
		return tools.SuccessResult(map[string]any{"id": record.ID, "type": record.Type, "status": "created"}), nil
	}
}

// DeleteForRun returns the transaction mutation that deletes one active Event of this Session.
func DeleteForRun(sessionID string, runID int64, eventID string, now time.Time) func(tx *sql.Tx) (json.RawMessage, error) {
	return func(tx *sql.Tx) (json.RawMessage, error) {
		record, err := ScanRecord(tx.QueryRow(Select+` WHERE id = ? AND owner_session_id = ?`, eventID, sessionID))
		if errors.Is(err, sql.ErrNoRows) {
			return tools.ErrorResult("EVENT_NOT_FOUND", "event does not exist in this Session", "not_executed"), nil
		}
		if err != nil {
			return nil, err
		}
		if err := RemoveRecordTx(tx, record, "deleted_by_agent", &runID, nil, now); err != nil {
			return nil, err
		}
		return tools.SuccessResult(map[string]any{"id": record.ID, "type": record.Type, "status": "deleted"}), nil
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// CompactView is the per-Event projection the model sees from list_events and fired triggers.
func CompactView(event Event) map[string]any {
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

// ClosePositionID extracts the position id from position_close params, for the position poller.
func ClosePositionID(params json.RawMessage) (string, error) {
	var decoded positionCloseParams
	if err := json.Unmarshal(params, &decoded); err != nil || strings.TrimSpace(decoded.PositionID) == "" {
		return "", errors.New("invalid position_close params")
	}
	return decoded.PositionID, nil
}

const Select = `SELECT id, owner_session_id, created_run_id, type, params_json, market, symbol,
	due_at, state_json, created_at, expires_at FROM events`

type scanner interface {
	Scan(dest ...any) error
}

func scanEventRecord(row scanner) (Record, error) {
	var record Record
	var params string
	var market, symbol, state sql.NullString
	err := row.Scan(&record.ID, &record.SessionID, &record.CreatedRunID, &record.Type, &params,
		&market, &symbol, &record.dueAt, &state, &record.CreatedAt, &record.ExpiresAt)
	if err != nil {
		return Record{}, err
	}
	record.Params = json.RawMessage(params)
	record.State = json.RawMessage("null")
	if state.Valid {
		record.State = json.RawMessage(state.String)
	}
	record.market, record.symbol = market.String, symbol.String
	return record, nil
}

// ScanRecord scans one active Event row (any *sql.Row or *sql.Rows cursor).
func ScanRecord(row any) (Record, error) {
	return scanEventRecord(row.(scanner))
}

type eventQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func queryEventRecords(querier eventQuerier, query string, args ...any) ([]Record, error) {
	rows, err := querier.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanEventRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// SessionEvents lists every active Event of one Session in creation order.
func SessionEvents(querier eventQuerier, sessionID string) ([]Event, error) {
	records, err := queryEventRecords(querier, Select+` WHERE owner_session_id = ? ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(records))
	for _, record := range records {
		events = append(events, record.Event)
	}
	return events, nil
}

// RemoveRecordTx is the single one-shot terminal path for every Event outcome: RowsAffected proves
// the claim, and the log row preserves the snapshot after the active row is gone.
func RemoveRecordTx(tx *sql.Tx, record Record, outcome string, outcomeRunID *int64, detail json.RawMessage, at time.Time) error {
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
		record.CreatedAt, record.ExpiresAt, outcome, outcomeRunID, detailValue, FormatTime(at))
	return err
}

// ClearSessionTx removes every active Event of a Session and drains its trigger queue, returning
// the cleared snapshot for the caller's response or restart context.
func ClearSessionTx(tx *sql.Tx, sessionID string, now time.Time, outcome string) (Clearance, error) {
	clearance := Clearance{Events: make([]Event, 0), Pending: make([]json.RawMessage, 0)}
	records, err := queryEventRecords(tx, Select+` WHERE owner_session_id = ? ORDER BY created_at, id`, sessionID)
	if err != nil {
		return clearance, err
	}
	for _, record := range records {
		if err := RemoveRecordTx(tx, record, outcome, nil, nil, now); err != nil {
			return clearance, err
		}
		clearance.Events = append(clearance.Events, record.Event)
	}
	clearance.Pending, err = PendingPayloadsTx(tx, sessionID, 0)
	if err != nil {
		return clearance, err
	}
	_, err = tx.Exec(`DELETE FROM pending_triggers WHERE session_id = ?`, sessionID)
	return clearance, err
}

// ExpireTx terminates every Event whose expiry has arrived.
func ExpireTx(tx *sql.Tx, now time.Time) error {
	expired, err := queryEventRecords(tx, Select+` WHERE expires_at <= ? ORDER BY created_at, id`, FormatTime(now))
	if err != nil {
		return err
	}
	for _, record := range expired {
		if err := RemoveRecordTx(tx, record, "expired", nil, nil, now); err != nil {
			return err
		}
	}
	return nil
}

// FireTimersTx claims every due timer Event and enqueues its trigger.
func FireTimersTx(tx *sql.Tx, now time.Time) ([]string, error) {
	nowValue := FormatTime(now)
	due, err := queryEventRecords(tx, Select+` WHERE type = 'timer' AND due_at <= ? AND expires_at > ? ORDER BY created_at, id`, nowValue, nowValue)
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

// MatchPriceTx applies the round's samples to every unexpired price Event. The expiry
// filter uses the claim clock, so an Event whose expiry has arrived is never matched.
func MatchPriceTx(tx *sql.Tx, now time.Time, samples map[string]PriceSample) ([]string, error) {
	sessions := make([]string, 0)
	watched, err := queryEventRecords(tx, Select+` WHERE market IS NOT NULL AND symbol IS NOT NULL AND expires_at > ? ORDER BY created_at, id`, FormatTime(now))
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

func fireEventTx(tx *sql.Tx, record Record, matched map[string]any, now time.Time) error {
	detail, err := json.Marshal(matched)
	if err != nil {
		return fmt.Errorf("encode Event %s match facts: %w", record.ID, err)
	}
	payload, err := json.Marshal(map[string]any{
		"type": record.Type, "event_id": record.ID, "created_run_id": record.CreatedRunID,
		"params": record.Params, "expires_at": record.ExpiresAt, "fired_at": FormatTime(now), "matched": matched,
	})
	if err != nil {
		return fmt.Errorf("encode Event %s Trigger: %w", record.ID, err)
	}
	if err := RemoveRecordTx(tx, record, "fired", nil, detail, now); err != nil {
		return err
	}
	return enqueueTriggerTx(tx, record.SessionID, payload, now)
}

func appendUnique(values []string, wanted string) []string {
	for _, value := range values {
		if value == wanted {
			return values
		}
	}
	return append(values, wanted)
}

// MaxTriggersPerRun bounds one Run's trigger batch.
const MaxTriggersPerRun = 10

// enqueueTriggerTx appends one trigger to the Session's ordered list. The AUTOINCREMENT id is
// the Server-generated arrival sequence; it is never reused, so order survives crashes.
func enqueueTriggerTx(tx *sql.Tx, sessionID string, payload json.RawMessage, now time.Time) error {
	_, err := tx.Exec(`INSERT INTO pending_triggers (session_id, payload_json, created_at) VALUES (?, ?, ?)`,
		sessionID, string(payload), FormatTime(now))
	return err
}

// PendingPayloadsTx reads the Session's queued triggers in arrival order (limit 0 = all).
func PendingPayloadsTx(tx *sql.Tx, sessionID string, limit int) ([]json.RawMessage, error) {
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
