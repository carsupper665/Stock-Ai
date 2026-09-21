package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"stock-ai/agent-server/internal/eventloop"
	"stock-ai/agent-server/internal/events"
	"stock-ai/agent-server/internal/server"
)

// PositionEventTask polls the bound Account's authoritative open-position view. A missing
// position is a close only after a successful earlier poll observed that exact position ID.
func (r *AgentRuntime) PositionEventTask(clock eventloop.Clock, interval time.Duration) server.BackgroundTask {
	clock = eventClock(clock)
	return server.BackgroundTask{Name: "agent-events-position", Interval: interval, Run: func(ctx context.Context) error {
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
			return events.MatchPositionCloseTx(tx, now, samples)
		})
	}}
}

func (r *AgentRuntime) watchedPositions(ctx context.Context, now time.Time) ([]events.PositionWatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT e.owner_session_id, s.account_id, e.params_json, e.state_json
		FROM events e JOIN sessions s ON s.id = e.owner_session_id
		WHERE e.type = 'position_close' AND e.expires_at > ? ORDER BY e.created_at, e.id`, events.FormatTime(now))
	if err != nil {
		return nil, r.roundFailureLocked(ctx, nil, err)
	}
	defer rows.Close()
	watched := make([]events.PositionWatch, 0)
	for rows.Next() {
		var watch events.PositionWatch
		var params string
		var state sql.NullString
		if err := rows.Scan(&watch.SessionID, &watch.AccountID, &params, &state); err != nil {
			return nil, r.roundFailureLocked(ctx, nil, err)
		}
		positionID, err := events.ClosePositionID(json.RawMessage(params))
		if err != nil {
			return nil, r.roundFailureLocked(ctx, nil, fmt.Errorf("invalid position_close params for Session %s", watch.SessionID))
		}
		watch.PositionID = positionID
		if state.Valid {
			decodedState, err := events.DecodeCloseState(json.RawMessage(state.String))
			if err != nil {
				return nil, r.roundFailureLocked(ctx, nil, fmt.Errorf("invalid position_close state for Session %s: %w", watch.SessionID, err))
			}
			watch.Observed = decodedState != nil
		}
		watched = append(watched, watch)
	}
	if err := rows.Err(); err != nil {
		return nil, r.roundFailureLocked(ctx, nil, err)
	}
	return watched, nil
}

func (r *AgentRuntime) samplePositions(ctx context.Context, watched []events.PositionWatch) map[string]events.PositionAccountSample {
	bySession := make(map[string][]events.PositionWatch)
	for _, watch := range watched {
		bySession[watch.SessionID] = append(bySession[watch.SessionID], watch)
	}
	samples := make(map[string]events.PositionAccountSample, len(bySession))
	for sessionID, watches := range bySession {
		requestContext, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
		token, err := r.fetchAccountToken(requestContext, watches[0].AccountID)
		if err != nil {
			cancel()
			continue
		}
		body, err := r.readMonitorBackend(requestContext, token, "/v1/positions")
		if err != nil {
			cancel()
			continue
		}
		positions, ok := events.DecodePositionSnapshot(body["positions"])
		if !ok {
			cancel()
			continue
		}
		missing := make(map[string]bool)
		for _, watch := range watches {
			if _, present := positions[watch.PositionID]; watch.Observed && !present {
				missing[watch.PositionID] = true
			}
		}
		reasons := map[string]string{}
		if len(missing) != 0 {
			reasons = r.fetchPositionCloseReasons(requestContext, token, missing)
		}
		cancel()
		samples[sessionID] = events.PositionAccountSample{Positions: positions, Reasons: reasons}
	}
	return samples
}

type positionLedgerEvidence struct {
	Event      string `json:"event"`
	OrderID    string `json:"order_id"`
	PositionID string `json:"position_id"`
	Trigger    string `json:"trigger"`
}

func (r *AgentRuntime) fetchPositionCloseReasons(ctx context.Context, token string, wanted map[string]bool) map[string]string {
	query := url.Values{"limit": {fmt.Sprint(events.PositionEvidenceLimit)}}
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
