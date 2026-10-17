package history

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ValidBody reports that body is a well-formed shard document for the
// range that contains runID.
func ValidBody(body []byte, sessionID string, start, end, runID int64) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	var shardSessionID string
	var shardStart, shardEnd int64
	var runs []json.RawMessage
	expectedStart, expectedEnd := Range(runID)
	if !decodeField(fields, "session_id", &shardSessionID) ||
		!decodeField(fields, "start_run_id", &shardStart) ||
		!decodeField(fields, "end_run_id", &shardEnd) ||
		!decodeArray(fields, "runs", &runs) ||
		shardSessionID != sessionID || shardStart != start || shardEnd != end ||
		start != expectedStart || end != expectedEnd {
		return false
	}
	found := false
	previous := int64(0)
	for _, rawRun := range runs {
		run, valid := validRun(rawRun, start, end)
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

func validRun(raw json.RawMessage, start, end int64) (Run, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return Run{}, false
	}
	required := []string{
		"run_id", "status", "trigger", "triggers", "summary", "output", "error", "iterations", "initial_context",
		"model_call_count", "tool_call_count", "start_time", "end_time", "memory_ids", "event_log",
	}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return Run{}, false
		}
	}
	var run Run
	if json.Unmarshal(raw, &run) != nil || run.RunID < start || run.RunID > end ||
		(run.Status != "completed" && run.Status != "failed" && run.Status != "interrupted") ||
		strings.TrimSpace(run.Trigger) == "" || run.ModelCalls < 0 || run.ToolCalls < 0 ||
		run.EndedAt == nil || !validTime(run.StartedAt) || !validTime(*run.EndedAt) ||
		!validNullableString(fields["summary"]) || !validNullableString(fields["output"]) ||
		!validNullableString(fields["error"]) || !validNullableArray(fields["triggers"]) {
		return Run{}, false
	}
	var iterations, memoryIDs, events []json.RawMessage
	if !decodeRawArray(fields["iterations"], &iterations) ||
		!decodeRawArray(fields["memory_ids"], &memoryIDs) ||
		!decodeRawArray(fields["event_log"], &events) {
		return Run{}, false
	}
	if !json.Valid(fields["initial_context"]) {
		return Run{}, false
	}
	if run.Status == "completed" {
		if run.Summary == nil || run.Output == nil || run.Error != nil {
			return Run{}, false
		}
	} else if run.Summary != nil || run.Output != nil || run.Error == nil {
		return Run{}, false
	}
	for _, rawID := range memoryIDs {
		var id string
		if json.Unmarshal(rawID, &id) != nil || !validMemoryID(id) {
			return Run{}, false
		}
	}
	for _, event := range events {
		if !validEvent(event) {
			return Run{}, false
		}
	}
	return run, true
}

func validEvent(raw json.RawMessage) bool {
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
	var event Event
	if json.Unmarshal(raw, &event) != nil || strings.TrimSpace(event.ID) == "" || event.CreatedRunID <= 0 ||
		strings.TrimSpace(event.Type) == "" || strings.TrimSpace(event.Outcome) == "" ||
		!json.Valid(fields["params"]) || !json.Valid(fields["state"]) || !json.Valid(fields["detail"]) ||
		!validTime(event.CreatedAt) || !validTime(event.ExpiresAt) || !validTime(event.At) {
		return false
	}
	if event.OutcomeRunID != nil && *event.OutcomeRunID <= 0 {
		return false
	}
	return true
}

func decodeField(fields map[string]json.RawMessage, name string, target any) bool {
	raw, exists := fields[name]
	return exists && json.Unmarshal(raw, target) == nil
}

func decodeArray(fields map[string]json.RawMessage, name string, target *[]json.RawMessage) bool {
	raw, exists := fields[name]
	return exists && decodeRawArray(raw, target)
}

func decodeRawArray(raw json.RawMessage, target *[]json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && trimmed[0] == '[' && json.Unmarshal(trimmed, target) == nil
}

func validNullableArray(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var values []json.RawMessage
	return decodeRawArray(raw, &values)
}

func validNullableString(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func validTime(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

// ParseRun returns the recorded run from a validated shard body.
func ParseRun(body []byte, runID int64) (Run, error) {
	var shard Shard
	if err := json.Unmarshal(body, &shard); err != nil {
		return Run{}, err
	}
	for _, run := range shard.Runs {
		if run.RunID == runID {
			return run, nil
		}
	}
	return Run{}, sql.ErrNoRows
}

// ReadRun resolves runID through the run status, the history index, and the
// shard file, returning the recorded run.
func ReadRun(ctx context.Context, db *sql.DB, mu sync.Locker, directory, sessionID string, runID int64) (Run, error) {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&status)
	if err != nil {
		return Run{}, err
	}
	if status != "completed" && status != "failed" && status != "interrupted" {
		return Run{}, sql.ErrNoRows
	}
	var name string
	var start, end int64
	err = db.QueryRowContext(ctx, `SELECT start_run_id, end_run_id, file_path FROM run_history_files
		WHERE session_id = ? AND start_run_id <= ? AND end_run_id >= ?`, sessionID, runID, runID).Scan(&start, &end, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, errors.New("History index is unavailable")
	}
	if err != nil {
		return Run{}, err
	}
	expectedStart, expectedEnd := Range(runID)
	if start != expectedStart || end != expectedEnd || name != FileName(sessionID, start, end) || filepath.Base(name) != name {
		return Run{}, errors.New("History index is invalid")
	}
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	mu.Lock()
	body, err := os.ReadFile(filepath.Join(directory, name))
	mu.Unlock()
	if err != nil || !ValidBody(body, sessionID, start, end, runID) {
		return Run{}, errors.New("History index is invalid")
	}
	return ParseRun(body, runID)
}

// validMemoryID accepts cabinet ids and the m_<32 hex> ids kept in shards written before the
// memory cabinet replaced finalization candidates.
func validMemoryID(id string) bool {
	if number, err := strconv.ParseInt(id, 10, 64); err == nil {
		return number > 0 && strconv.FormatInt(number, 10) == id
	}
	if len(id) != 34 || !strings.HasPrefix(id, "m_") || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id[2:])
	return err == nil
}
