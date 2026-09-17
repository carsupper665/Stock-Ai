package common

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const positionEvidenceLimit = 200

type positionIdentity struct {
	ID      string `json:"id"`
	Market  string `json:"market"`
	Symbol  string `json:"symbol"`
	Product string `json:"product"`
	Side    string `json:"side"`
}

type positionCloseState struct {
	Position positionIdentity `json:"position"`
}

func (s positionCloseState) encode() string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func decodePositionCloseState(raw json.RawMessage) (*positionCloseState, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var state positionCloseState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.Position.ID) == "" {
		return nil, fmt.Errorf("observed position id is missing")
	}
	return &state, nil
}

type positionWatch struct {
	sessionID  string
	accountID  string
	positionID string
	observed   bool
}

type positionAccountSample struct {
	positions map[string]positionIdentity
	reasons   map[string]string
}

// PositionEventTask polls the bound Account's authoritative open-position view. A missing
// position is a close only after a successful earlier poll observed that exact position ID.
func (r *AgentRuntime) PositionEventTask(clock Clock, interval time.Duration) BackgroundTask {
	clock = eventClock(clock)
	return BackgroundTask{Name: "agent-events-position", Interval: interval, Run: func(ctx context.Context) error {
		watched, err := r.watchedPositions(ctx, clock.Now())
		if err != nil {
			return err
		}
		samples := r.samplePositions(ctx, watched)
		if len(samples) == 0 {
			return nil
		}
		now := clock.Now()
		return r.dispatchEventRound(ctx, func(tx *sql.Tx) ([]string, error) {
			return matchPositionCloseEventsTx(tx, now, samples)
		})
	}}
}

func (r *AgentRuntime) watchedPositions(ctx context.Context, now time.Time) ([]positionWatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT e.owner_session_id, s.account_id, e.params_json, e.state_json
		FROM events e JOIN sessions s ON s.id = e.owner_session_id
		WHERE e.type = 'position_close' AND e.expires_at > ? ORDER BY e.created_at, e.id`, formatEventTime(now))
	if err != nil {
		return nil, r.roundFailureLocked(ctx, nil, err)
	}
	defer rows.Close()
	watched := make([]positionWatch, 0)
	for rows.Next() {
		var watch positionWatch
		var params string
		var state sql.NullString
		if err := rows.Scan(&watch.sessionID, &watch.accountID, &params, &state); err != nil {
			return nil, r.roundFailureLocked(ctx, nil, err)
		}
		var decodedParams positionCloseParams
		if err := json.Unmarshal([]byte(params), &decodedParams); err != nil || strings.TrimSpace(decodedParams.PositionID) == "" {
			return nil, r.roundFailureLocked(ctx, nil, fmt.Errorf("invalid position_close params for Session %s", watch.sessionID))
		}
		watch.positionID = decodedParams.PositionID
		if state.Valid {
			decodedState, err := decodePositionCloseState(json.RawMessage(state.String))
			if err != nil {
				return nil, r.roundFailureLocked(ctx, nil, fmt.Errorf("invalid position_close state for Session %s: %w", watch.sessionID, err))
			}
			watch.observed = decodedState != nil
		}
		watched = append(watched, watch)
	}
	if err := rows.Err(); err != nil {
		return nil, r.roundFailureLocked(ctx, nil, err)
	}
	return watched, nil
}

func (r *AgentRuntime) samplePositions(ctx context.Context, watched []positionWatch) map[string]positionAccountSample {
	bySession := make(map[string][]positionWatch)
	for _, watch := range watched {
		bySession[watch.sessionID] = append(bySession[watch.sessionID], watch)
	}
	samples := make(map[string]positionAccountSample, len(bySession))
	for sessionID, watches := range bySession {
		requestContext, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
		token, err := r.fetchAccountToken(requestContext, watches[0].accountID)
		if err != nil {
			cancel()
			continue
		}
		body, err := r.readMonitorBackend(requestContext, token, "/v1/positions")
		if err != nil {
			cancel()
			continue
		}
		positions, ok := decodePositionSnapshot(body["positions"])
		if !ok {
			cancel()
			continue
		}
		missing := make(map[string]bool)
		for _, watch := range watches {
			if _, present := positions[watch.positionID]; watch.observed && !present {
				missing[watch.positionID] = true
			}
		}
		reasons := map[string]string{}
		if len(missing) != 0 {
			reasons = r.fetchPositionCloseReasons(requestContext, token, missing)
		}
		cancel()
		samples[sessionID] = positionAccountSample{positions: positions, reasons: reasons}
	}
	return samples
}

func decodePositionSnapshot(raw json.RawMessage) (map[string]positionIdentity, bool) {
	var positions []positionIdentity
	if len(raw) == 0 || json.Unmarshal(raw, &positions) != nil || positions == nil {
		return nil, false
	}
	result := make(map[string]positionIdentity, len(positions))
	for _, position := range positions {
		if strings.TrimSpace(position.ID) == "" || strings.TrimSpace(position.Market) == "" ||
			strings.TrimSpace(position.Symbol) == "" || strings.TrimSpace(position.Product) == "" || strings.TrimSpace(position.Side) == "" {
			return nil, false
		}
		if _, duplicate := result[position.ID]; duplicate {
			return nil, false
		}
		result[position.ID] = position
	}
	return result, true
}

type positionLedgerEvidence struct {
	Event      string `json:"event"`
	OrderID    string `json:"order_id"`
	PositionID string `json:"position_id"`
	Trigger    string `json:"trigger"`
}

func (r *AgentRuntime) fetchPositionCloseReasons(ctx context.Context, token string, wanted map[string]bool) map[string]string {
	query := url.Values{"limit": {fmt.Sprint(positionEvidenceLimit)}}
	body, err := r.readMonitorBackend(ctx, token, "/v1/ledger?"+query.Encode())
	if err != nil {
		return map[string]string{}
	}
	var entries []positionLedgerEvidence
	if json.Unmarshal(body["entries"], &entries) != nil || entries == nil {
		return map[string]string{}
	}
	reasons := make(map[string]string)
	manualCandidates := make(map[string]string)
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.Event != "fill" || !wanted[entry.PositionID] || seen[entry.PositionID] {
			continue
		}
		seen[entry.PositionID] = true
		if strings.TrimSpace(entry.Trigger) != "" {
			reasons[entry.PositionID] = entry.Trigger
		} else if strings.TrimSpace(entry.OrderID) != "" {
			manualCandidates[entry.PositionID] = entry.OrderID
		}
	}
	for positionID, orderID := range manualCandidates {
		order, err := r.readMonitorBackend(ctx, token, "/v1/orders/"+url.PathEscape(orderID))
		if err != nil {
			continue
		}
		var reduceOnly bool
		if raw, present := order["reduce_only"]; !present || json.Unmarshal(raw, &reduceOnly) != nil || !reduceOnly {
			continue
		}
		var trigger string
		_ = json.Unmarshal(order["trigger"], &trigger)
		if strings.TrimSpace(trigger) != "" {
			reasons[positionID] = trigger
		} else {
			reasons[positionID] = "manual"
		}
	}
	return reasons
}

func matchPositionCloseEventsTx(tx *sql.Tx, now time.Time, samples map[string]positionAccountSample) ([]string, error) {
	records, err := queryEventRecords(tx, eventSelect+` WHERE type = 'position_close' AND expires_at > ? ORDER BY created_at, id`, formatEventTime(now))
	if err != nil {
		return nil, err
	}
	sessions := make([]string, 0)
	for _, record := range records {
		sample, ok := samples[record.SessionID]
		if !ok {
			continue
		}
		var params positionCloseParams
		if err := json.Unmarshal(record.Params, &params); err != nil {
			return nil, fmt.Errorf("event %s params: %w", record.ID, err)
		}
		state, err := decodePositionCloseState(record.State)
		if err != nil {
			return nil, fmt.Errorf("event %s state: %w", record.ID, err)
		}
		position, present := sample.positions[params.PositionID]
		if present {
			if state == nil || state.Position != position {
				next := positionCloseState{Position: position}
				if _, err := tx.Exec(`UPDATE events SET state_json = ? WHERE id = ?`, next.encode(), record.ID); err != nil {
					return nil, err
				}
			}
			continue
		}
		if state == nil {
			continue
		}
		matched := map[string]any{
			"position_id": state.Position.ID, "market": state.Position.Market, "symbol": state.Position.Symbol,
			"product": state.Position.Product, "side": state.Position.Side,
		}
		if reason := sample.reasons[params.PositionID]; reason != "" {
			matched["close_reason"] = reason
		}
		if err := fireEventTx(tx, record, matched, now); err != nil {
			return nil, err
		}
		sessions = appendUnique(sessions, record.SessionID)
	}
	return sessions, nil
}
