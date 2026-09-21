package events

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PositionEvidenceLimit bounds the ledger rows consulted for one close reason.
const PositionEvidenceLimit = 200

type PositionIdentity struct {
	ID      string `json:"id"`
	Market  string `json:"market"`
	Symbol  string `json:"symbol"`
	Product string `json:"product"`
	Side    string `json:"side"`
}

type positionCloseState struct {
	Position PositionIdentity `json:"position"`
}

func (s positionCloseState) encode() string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// DecodeCloseState parses the persisted first-observation state of a position_close Event.
// A nil result means the position has not yet been observed open.
func DecodeCloseState(raw json.RawMessage) (*positionCloseState, error) {
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

type PositionWatch struct {
	SessionID  string
	AccountID  string
	PositionID string
	Observed   bool
}

type PositionAccountSample struct {
	Positions map[string]PositionIdentity
	Reasons   map[string]string
}

// DecodePositionSnapshot projects the Backend /v1/positions list to position identity, rejecting
// malformed or duplicate entries.
func DecodePositionSnapshot(raw json.RawMessage) (map[string]PositionIdentity, bool) {
	var positions []PositionIdentity
	if len(raw) == 0 || json.Unmarshal(raw, &positions) != nil || positions == nil {
		return nil, false
	}
	result := make(map[string]PositionIdentity, len(positions))
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

// MatchPositionCloseTx claims position_close Events whose bound Account no longer reports the
// exact position ID. A position is closed only after a successful earlier poll observed it.
func MatchPositionCloseTx(tx *sql.Tx, now time.Time, samples map[string]PositionAccountSample) ([]string, error) {
	records, err := queryEventRecords(tx, Select+` WHERE type = 'position_close' AND expires_at > ? ORDER BY created_at, id`, FormatTime(now))
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
		state, err := DecodeCloseState(record.State)
		if err != nil {
			return nil, fmt.Errorf("event %s state: %w", record.ID, err)
		}
		position, present := sample.Positions[params.PositionID]
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
		if reason := sample.Reasons[params.PositionID]; reason != "" {
			matched["close_reason"] = reason
		}
		if err := fireEventTx(tx, record, matched, now); err != nil {
			return nil, err
		}
		sessions = appendUnique(sessions, record.SessionID)
	}
	return sessions, nil
}
