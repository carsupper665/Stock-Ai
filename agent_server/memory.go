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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	memoryPageSize        = 100
	memoryToolDefault     = 20
	memoryToolMax         = 100
	maxCandidateMemories  = 20
	maxMemoryTypeRunes    = 64
	maxMemorySummaryRunes = 500
	maxMemoryContentBytes = 64 << 10
	maxExpirationRunes    = 500
)

var memorySchema = []string{
	`CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY, session_id TEXT NOT NULL, run_id INTEGER NOT NULL,
		candidate_index INTEGER NOT NULL, type TEXT NOT NULL, summary TEXT NOT NULL,
		content TEXT NOT NULL, importance INTEGER NOT NULL CHECK (importance BETWEEN 1 AND 3),
		run_start_at TEXT NOT NULL, run_end_at TEXT NOT NULL,
		is_expired INTEGER NOT NULL DEFAULT 0 CHECK (is_expired IN (0, 1)),
		expired_at TEXT, expired_source TEXT, expired_run_id INTEGER, expiration_reason TEXT,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		UNIQUE (session_id, run_id, candidate_index)
	)`,
	`CREATE INDEX IF NOT EXISTS memories_session_listing
		ON memories(session_id, is_expired, importance DESC, run_id DESC, id)`,
}

type Memory struct {
	ID               string  `json:"id"`
	SessionID        string  `json:"session_id"`
	RunID            int64   `json:"run_id"`
	Type             string  `json:"type"`
	Summary          string  `json:"summary"`
	Content          string  `json:"content"`
	Importance       int     `json:"importance"`
	RunStartAt       string  `json:"run_start_at"`
	RunEndAt         string  `json:"run_end_at"`
	IsExpired        bool    `json:"is_expired"`
	ExpiredAt        *string `json:"expired_at"`
	ExpiredSource    *string `json:"expired_source"`
	ExpiredRunID     *int64  `json:"expired_run_id"`
	ExpirationReason *string `json:"expiration_reason"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type memoryMetadata struct {
	ID         string `json:"id"`
	RunID      int64  `json:"run_id"`
	Type       string `json:"type"`
	Summary    string `json:"summary"`
	Importance int    `json:"importance"`
	IsExpired  bool   `json:"is_expired"`
}

type memoryListItem struct {
	ID               string  `json:"id"`
	SessionID        string  `json:"session_id"`
	RunID            int64   `json:"run_id"`
	Type             string  `json:"type"`
	Summary          string  `json:"summary"`
	Importance       int     `json:"importance"`
	RunStartAt       string  `json:"run_start_at"`
	RunEndAt         string  `json:"run_end_at"`
	IsExpired        bool    `json:"is_expired"`
	ExpiredAt        *string `json:"expired_at"`
	ExpiredSource    *string `json:"expired_source"`
	ExpiredRunID     *int64  `json:"expired_run_id"`
	ExpirationReason *string `json:"expiration_reason"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type recentRunSummary struct {
	RunID   int64  `json:"run_id"`
	Summary string `json:"summary"`
}

type candidateMemory struct {
	Type       string `json:"type"`
	Summary    string `json:"summary"`
	Content    string `json:"content"`
	Importance int    `json:"importance"`
}

type memoryExpiration struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type finalizationOutput struct {
	Output            string             `json:"output"`
	Summary           string             `json:"summary"`
	CandidateMemories []candidateMemory  `json:"candidate_memories"`
	ExpireMemories    []memoryExpiration `json:"expire_memories"`
}

type finalizationReferenceError struct{ message string }

func (err *finalizationReferenceError) Error() string { return err.message }

const finalizationInstruction = `

When your work is normally complete, finish with finish_reason "stop" and content containing exactly one JSON object:
{"output":"human-readable final decision","summary":"short summary (max 500 Unicode characters)","candidate_memories":[],"expire_memories":[]}
All four fields are required. candidate_memories may contain at most 20 objects with only type (max 64 Unicode characters), summary (max 500 Unicode characters), content (max 65536 UTF-8 bytes), and importance (1, 2, or 3). expire_memories may contain at most 20 objects with only a Server-issued memory id and a reason of at most 500 Unicode characters. Do not include session_id, run_id, timestamps, credentials, hidden reasoning, or extra fields. Tool calls continue to use the structured tool channel; do not put this final object in a tool call.`

func parseFinalization(content string) (finalizationOutput, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var envelope struct {
		Output            *string             `json:"output"`
		Summary           *string             `json:"summary"`
		CandidateMemories *[]candidateMemory  `json:"candidate_memories"`
		ExpireMemories    *[]memoryExpiration `json:"expire_memories"`
	}
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		envelope.Output == nil || envelope.Summary == nil || envelope.CandidateMemories == nil || envelope.ExpireMemories == nil {
		return finalizationOutput{}, errors.New("content must be exactly one finalization JSON object with known fields")
	}
	output := finalizationOutput{
		Output: *envelope.Output, Summary: *envelope.Summary,
		CandidateMemories: *envelope.CandidateMemories, ExpireMemories: *envelope.ExpireMemories,
	}
	output.Output = strings.TrimSpace(output.Output)
	output.Summary = strings.TrimSpace(output.Summary)
	if output.Output == "" || output.Summary == "" {
		return finalizationOutput{}, errors.New("output and summary must be nonempty strings")
	}
	if utf8.RuneCountInString(output.Summary) > maxMemorySummaryRunes {
		return finalizationOutput{}, fmt.Errorf("summary must not exceed %d Unicode characters", maxMemorySummaryRunes)
	}
	if len(output.CandidateMemories) > maxCandidateMemories || len(output.ExpireMemories) > maxCandidateMemories {
		return finalizationOutput{}, fmt.Errorf("candidate_memories and expire_memories may each contain at most %d items", maxCandidateMemories)
	}
	for index := range output.CandidateMemories {
		candidate := &output.CandidateMemories[index]
		candidate.Type = strings.TrimSpace(candidate.Type)
		candidate.Summary = strings.TrimSpace(candidate.Summary)
		candidate.Content = strings.TrimSpace(candidate.Content)
		if candidate.Type == "" || utf8.RuneCountInString(candidate.Type) > maxMemoryTypeRunes {
			return finalizationOutput{}, fmt.Errorf("candidate_memories[%d].type must contain 1 through %d Unicode characters", index, maxMemoryTypeRunes)
		}
		if candidate.Summary == "" || utf8.RuneCountInString(candidate.Summary) > maxMemorySummaryRunes {
			return finalizationOutput{}, fmt.Errorf("candidate_memories[%d].summary must contain 1 through %d Unicode characters", index, maxMemorySummaryRunes)
		}
		if candidate.Content == "" || len(candidate.Content) > maxMemoryContentBytes {
			return finalizationOutput{}, fmt.Errorf("candidate_memories[%d].content must contain 1 through %d UTF-8 bytes", index, maxMemoryContentBytes)
		}
		if candidate.Importance < 1 || candidate.Importance > 3 {
			return finalizationOutput{}, fmt.Errorf("candidate_memories[%d].importance must be 1, 2, or 3", index)
		}
	}
	seen := make(map[string]bool, len(output.ExpireMemories))
	for index := range output.ExpireMemories {
		expiration := &output.ExpireMemories[index]
		expiration.ID = strings.TrimSpace(expiration.ID)
		expiration.Reason = strings.TrimSpace(expiration.Reason)
		if !validMemoryID(expiration.ID) {
			return finalizationOutput{}, fmt.Errorf("expire_memories[%d].id must be a Server-issued Memory ID", index)
		}
		if expiration.Reason == "" || utf8.RuneCountInString(expiration.Reason) > maxExpirationRunes {
			return finalizationOutput{}, fmt.Errorf("expire_memories[%d].reason must contain 1 through %d Unicode characters", index, maxExpirationRunes)
		}
		if seen[expiration.ID] {
			return finalizationOutput{}, fmt.Errorf("expire_memories contains duplicate id %q", expiration.ID)
		}
		seen[expiration.ID] = true
	}
	return output, nil
}

func validMemoryID(id string) bool {
	if len(id) != 34 || !strings.HasPrefix(id, "m_") || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id[2:])
	return err == nil
}

func (r *AgentRuntime) completeFinalizedRun(sessionID string, runID int64, final finalizationOutput) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return false, errors.New("Agent persistence is unavailable")
	}
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	run, err := scanRun(tx.QueryRow(runSelect+` WHERE session_id = ? AND run_id = ?`, sessionID, runID))
	if err != nil || run.Status != "running" {
		_ = tx.Rollback()
		return false, err
	}
	endTime := time.Now().UTC().Format(time.RFC3339Nano)
	expiredReferences := make(map[string]bool, len(final.ExpireMemories))
	for _, expiration := range final.ExpireMemories {
		var expired bool
		err := tx.QueryRow(`SELECT is_expired FROM memories WHERE id = ? AND session_id = ?`, expiration.ID, sessionID).Scan(&expired)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return false, &finalizationReferenceError{message: fmt.Sprintf("Memory %s does not exist in this Session", expiration.ID)}
		}
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
		expiredReferences[expiration.ID] = expired
	}
	for index, candidate := range final.CandidateMemories {
		id, err := newMemoryID()
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
		_, err = tx.Exec(`INSERT INTO memories
			(id, session_id, run_id, candidate_index, type, summary, content, importance,
			run_start_at, run_end_at, is_expired, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			id, sessionID, runID, index, candidate.Type, candidate.Summary, candidate.Content,
			candidate.Importance, run.StartedAt, endTime, endTime, endTime)
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
	}
	for _, expiration := range final.ExpireMemories {
		if expiredReferences[expiration.ID] {
			continue
		}
		if _, err := tx.Exec(`UPDATE memories SET is_expired = 1, expired_at = ?, expired_source = 'agent',
			expired_run_id = ?, expiration_reason = ?, updated_at = ? WHERE id = ? AND session_id = ? AND is_expired = 0`,
			endTime, runID, expiration.Reason, endTime, expiration.ID, sessionID); err != nil {
			_ = tx.Rollback()
			return false, err
		}
	}
	result, err := tx.Exec(`UPDATE runs SET status = 'completed', summary = ?, output = ?, error = NULL, end_time = ?
		WHERE session_id = ? AND run_id = ? AND status = 'running'`, final.Summary, final.Output, endTime, sessionID, runID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		_ = tx.Rollback()
		return false, err
	}
	stage, err := r.stageRunHistoryTx(tx, sessionID, runID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return false, errors.Join(err, stage.finish(false))
	}
	if err := stage.finish(true); err != nil {
		return false, err
	}
	r.logOperational("run_terminal", sessionID, runID, "completed")
	return true, nil
}

func newMemoryID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("unable to create Memory ID")
	}
	return "m_" + hex.EncodeToString(value), nil
}

func memoryToolDefinitions() []map[string]any {
	limit := map[string]any{"type": "integer", "minimum": 1, "maximum": memoryToolMax, "default": memoryToolDefault}
	return []map[string]any{
		{"name": "memory_list", "description": "List unexpired Memory metadata for this Session.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"limit": limit}, "additionalProperties": false,
		}},
		{"name": "memory_search", "description": "Search unexpired Memory summary and type text for this Session.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "limit": limit},
			"required": []string{"query"}, "additionalProperties": false,
		}},
		{"name": "memory_read", "description": "Read one unexpired Memory from this Session by id.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"memory_id": map[string]any{"type": "string"}},
			"required": []string{"memory_id"}, "additionalProperties": false,
		}},
		{"name": "get_run_history", "description": "Read one completed, failed, or interrupted Run from this Session's History.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"run_id": map[string]any{"type": "integer", "minimum": 1}},
			"required": []string{"run_id"}, "additionalProperties": false,
		}},
	}
}

func (r *AgentRuntime) executeMemoryTool(ctx context.Context, sessionID string, call gatewayTool) (json.RawMessage, bool) {
	switch call.Name {
	case "memory_list":
		limit, err := memoryLimitArguments(call.Arguments)
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		memories, err := r.memoryMetadata(ctx, sessionID, "", limit)
		return r.memoryReadToolResult(memories, err), true
	case "memory_search":
		query, limit, err := memorySearchArguments(call.Arguments)
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		memories, err := r.memoryMetadata(ctx, sessionID, query, limit)
		return r.memoryReadToolResult(memories, err), true
	case "memory_read":
		id, err := singleIDToolArgument(call.Arguments, "memory_id")
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		memory, err := r.memoryByID(ctx, sessionID, id, false)
		if errors.Is(err, sql.ErrNoRows) {
			return toolErrorResult("MEMORY_NOT_FOUND", "Memory does not exist or is expired in this Session", "not_executed"), true
		}
		if err != nil {
			return r.memoryPersistenceToolError(err), true
		}
		return toolSuccessResult(map[string]any{
			"id": memory.ID, "run_id": memory.RunID, "type": memory.Type,
			"summary": memory.Summary, "content": memory.Content, "importance": memory.Importance,
		}), true
	case "get_run_history":
		runID, err := runHistoryToolArgument(call.Arguments)
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		run, err := r.historyRunByID(ctx, sessionID, runID)
		if err != nil {
			return r.historyToolError(err), true
		}
		return toolSuccessResult(run), true
	default:
		return nil, false
	}
}

func memoryLimitArguments(arguments json.RawMessage) (int, error) {
	var raw struct {
		Limit json.RawMessage `json:"limit"`
	}
	if !decodeToolArguments(arguments, &raw) {
		return 0, errors.New("only limit is accepted")
	}
	limit := memoryToolDefault
	if len(raw.Limit) == 0 {
		return limit, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw.Limit), []byte("null")) || json.Unmarshal(raw.Limit, &limit) != nil || limit < 1 || limit > memoryToolMax {
		return 0, fmt.Errorf("limit must be an integer from 1 through %d", memoryToolMax)
	}
	return limit, nil
}

func memorySearchArguments(arguments json.RawMessage) (string, int, error) {
	var raw struct {
		Query json.RawMessage `json:"query"`
		Limit json.RawMessage `json:"limit"`
	}
	if !decodeToolArguments(arguments, &raw) {
		return "", 0, errors.New("only query and limit are accepted")
	}
	query, err := requiredToolString(raw.Query, "query")
	if err != nil {
		return "", 0, err
	}
	limit := memoryToolDefault
	if len(raw.Limit) != 0 && (bytes.Equal(bytes.TrimSpace(raw.Limit), []byte("null")) || json.Unmarshal(raw.Limit, &limit) != nil || limit < 1 || limit > memoryToolMax) {
		return "", 0, fmt.Errorf("limit must be an integer from 1 through %d", memoryToolMax)
	}
	return query, limit, nil
}

func runHistoryToolArgument(arguments json.RawMessage) (int64, error) {
	var raw struct {
		RunID json.RawMessage `json:"run_id"`
	}
	if !decodeToolArguments(arguments, &raw) {
		return 0, errors.New("run_id is required and no other field is accepted")
	}
	var runID int64
	if len(raw.RunID) == 0 || bytes.Equal(bytes.TrimSpace(raw.RunID), []byte("null")) || json.Unmarshal(raw.RunID, &runID) != nil || runID < 1 {
		return 0, errors.New("run_id must be a positive integer")
	}
	return runID, nil
}

func (r *AgentRuntime) memoryMetadata(ctx context.Context, sessionID, query string, limit int) ([]memoryMetadata, error) {
	statement := `SELECT id, run_id, type, summary, importance, is_expired FROM memories
		WHERE session_id = ? AND is_expired = 0`
	args := []any{sessionID}
	if query != "" {
		statement += ` AND (instr(lower(summary), lower(?)) > 0 OR instr(lower(type), lower(?)) > 0)`
		args = append(args, query, query)
	}
	statement += ` ORDER BY importance DESC, run_id DESC, id LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memories := make([]memoryMetadata, 0)
	for rows.Next() {
		var memory memoryMetadata
		if err := rows.Scan(&memory.ID, &memory.RunID, &memory.Type, &memory.Summary, &memory.Importance, &memory.IsExpired); err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func (r *AgentRuntime) progressiveRunContext(ctx context.Context, sessionID string, currentRunID int64) (*recentRunSummary, []memoryMetadata, error) {
	var recent recentRunSummary
	err := r.db.QueryRowContext(ctx, `SELECT run_id, summary FROM runs
		WHERE session_id = ? AND run_id < ? AND status = 'completed' AND summary IS NOT NULL
		ORDER BY run_id DESC LIMIT 1`, sessionID, currentRunID).Scan(&recent.RunID, &recent.Summary)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	var recentPointer *recentRunSummary
	if err == nil {
		recentPointer = &recent
	}
	memories, err := r.memoryMetadata(ctx, sessionID, "", memoryToolDefault)
	return recentPointer, memories, err
}

func (r *AgentRuntime) memoryReadToolResult(memories []memoryMetadata, err error) json.RawMessage {
	if err != nil {
		return r.memoryPersistenceToolError(err)
	}
	return toolSuccessResult(map[string]any{"memories": memories})
}

func (r *AgentRuntime) memoryPersistenceToolError(err error) json.RawMessage {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return toolErrorResult("TOOL_INTERRUPTED", "Memory read was interrupted", "not_executed")
	}
	r.latchPersistenceFault(err)
	return toolErrorResult("PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable", "not_executed")
}

func (r *AgentRuntime) historyRunByID(ctx context.Context, sessionID string, runID int64) (historyRun, error) {
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&status)
	if err != nil {
		return historyRun{}, err
	}
	if status != "completed" && status != "failed" && status != "interrupted" {
		return historyRun{}, sql.ErrNoRows
	}
	var name string
	var start, end int64
	err = r.db.QueryRowContext(ctx, `SELECT start_run_id, end_run_id, file_path FROM run_history_files
		WHERE session_id = ? AND start_run_id <= ? AND end_run_id >= ?`, sessionID, runID, runID).Scan(&start, &end, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return historyRun{}, errors.New("History index is unavailable")
	}
	if err != nil {
		return historyRun{}, err
	}
	expectedStart, expectedEnd := historyRange(runID)
	if start != expectedStart || end != expectedEnd || name != historyFileName(sessionID, start, end) || filepath.Base(name) != name {
		return historyRun{}, errors.New("History index is invalid")
	}
	if err := ctx.Err(); err != nil {
		return historyRun{}, err
	}
	r.historyMu.Lock()
	body, err := os.ReadFile(filepath.Join(r.config.HistoryDirectory, name))
	r.historyMu.Unlock()
	if err != nil || !validHistoryBody(body, sessionID, start, end, runID) {
		return historyRun{}, errors.New("History shard is unavailable")
	}
	return parseHistoryRun(body, runID)
}

func scanMemory(row scanner) (Memory, error) {
	var memory Memory
	var expiredAt, expiredSource, expirationReason sql.NullString
	var expiredRunID sql.NullInt64
	err := row.Scan(&memory.ID, &memory.SessionID, &memory.RunID, &memory.Type, &memory.Summary,
		&memory.Content, &memory.Importance, &memory.RunStartAt, &memory.RunEndAt, &memory.IsExpired,
		&expiredAt, &expiredSource, &expiredRunID, &expirationReason, &memory.CreatedAt, &memory.UpdatedAt)
	if err != nil {
		return Memory{}, err
	}
	memory.ExpiredAt = nullStringPointer(expiredAt)
	memory.ExpiredSource = nullStringPointer(expiredSource)
	memory.ExpirationReason = nullStringPointer(expirationReason)
	if expiredRunID.Valid {
		value := expiredRunID.Int64
		memory.ExpiredRunID = &value
	}
	return memory, nil
}

const memorySelect = `SELECT id, session_id, run_id, type, summary, content, importance,
	run_start_at, run_end_at, is_expired, expired_at, expired_source, expired_run_id,
	expiration_reason, created_at, updated_at FROM memories`

const memoryListSelect = `SELECT id, session_id, run_id, type, summary, importance,
	run_start_at, run_end_at, is_expired, expired_at, expired_source, expired_run_id,
	expiration_reason, created_at, updated_at FROM memories`

func scanMemoryListItem(row scanner) (memoryListItem, error) {
	var memory memoryListItem
	var expiredAt, expiredSource, expirationReason sql.NullString
	var expiredRunID sql.NullInt64
	err := row.Scan(&memory.ID, &memory.SessionID, &memory.RunID, &memory.Type, &memory.Summary,
		&memory.Importance, &memory.RunStartAt, &memory.RunEndAt, &memory.IsExpired,
		&expiredAt, &expiredSource, &expiredRunID, &expirationReason, &memory.CreatedAt, &memory.UpdatedAt)
	if err != nil {
		return memoryListItem{}, err
	}
	memory.ExpiredAt = nullStringPointer(expiredAt)
	memory.ExpiredSource = nullStringPointer(expiredSource)
	memory.ExpirationReason = nullStringPointer(expirationReason)
	if expiredRunID.Valid {
		value := expiredRunID.Int64
		memory.ExpiredRunID = &value
	}
	return memory, nil
}

func (r *AgentRuntime) memoryByID(ctx context.Context, sessionID, memoryID string, includeExpired bool) (Memory, error) {
	query := memorySelect + ` WHERE session_id = ? AND id = ?`
	if !includeExpired {
		query += ` AND is_expired = 0`
	}
	return scanMemory(r.db.QueryRowContext(ctx, query, sessionID, memoryID))
}

func (r *AgentRuntime) serveMemories(w http.ResponseWriter, request *http.Request, sessionID string, rest []string) {
	switch {
	case len(rest) == 0 && request.Method == http.MethodGet:
		r.listMemoriesHTTP(w, request, sessionID)
	case len(rest) == 0:
		w.Header().Set("Allow", http.MethodGet)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "users cannot create or modify Memories")
	case len(rest) == 1 && request.Method == http.MethodGet:
		r.getMemoryHTTP(w, request, sessionID, rest[0])
	case len(rest) == 1 && request.Method == http.MethodDelete:
		r.deleteMemoryHTTP(w, request, sessionID, rest[0])
	case len(rest) == 1:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "users cannot create or modify Memories")
	default:
		writeHTTPError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (r *AgentRuntime) listMemoriesHTTP(w http.ResponseWriter, request *http.Request, sessionID string) {
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	page, includeExpired, ok := memoryListQuery(request)
	if !ok {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "page must be positive and include_expired must be true or false")
		return
	}
	query := memoryListSelect + ` WHERE session_id = ?`
	if !includeExpired {
		query += ` AND is_expired = 0`
	}
	query += ` ORDER BY importance DESC, run_id DESC, id LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(request.Context(), query, sessionID, memoryPageSize, (page-1)*memoryPageSize)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Memories")
		return
	}
	defer rows.Close()
	memories := make([]memoryListItem, 0)
	for rows.Next() {
		memory, err := scanMemoryListItem(rows)
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Memories")
			return
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Memories")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"memories": memories, "page": page, "page_size": memoryPageSize, "include_expired": includeExpired})
}

func memoryListQuery(request *http.Request) (int, bool, bool) {
	values := request.URL.Query()
	for key := range values {
		if key != "page" && key != "include_expired" {
			return 0, false, false
		}
	}
	page := 1
	if rawValues, present := values["page"]; present {
		if len(rawValues) != 1 || rawValues[0] == "" {
			return 0, false, false
		}
		var err error
		page, err = strconv.Atoi(rawValues[0])
		if err != nil || page < 1 || page > int(^uint(0)>>1)/memoryPageSize {
			return 0, false, false
		}
	}
	includeExpired := false
	if rawValues, present := values["include_expired"]; present {
		if len(rawValues) != 1 || (rawValues[0] != "true" && rawValues[0] != "false") {
			return 0, false, false
		}
		includeExpired = rawValues[0] == "true"
	}
	return page, includeExpired, true
}

func (r *AgentRuntime) getMemoryHTTP(w http.ResponseWriter, request *http.Request, sessionID, memoryID string) {
	memory, err := r.memoryByID(request.Context(), sessionID, memoryID, true)
	if errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Memory")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, memory)
}

func (r *AgentRuntime) deleteMemoryHTTP(w http.ResponseWriter, request *http.Request, sessionID, memoryID string) {
	r.mu.Lock()
	if r.closed || r.persistenceErr != nil {
		r.mu.Unlock()
		writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	tx, err := r.db.BeginTx(request.Context(), nil)
	if err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	memory, err := scanMemory(tx.QueryRowContext(request.Context(), memorySelect+` WHERE session_id = ? AND id = ?`, sessionID, memoryID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "Memory does not exist in this Session")
		return
	}
	if err != nil {
		_ = tx.Rollback()
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	var userExpiredAt *string
	if !memory.IsExpired {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		userExpiredAt = &now
		_, err = tx.ExecContext(request.Context(), `UPDATE memories SET is_expired = 1, expired_at = ?,
			expired_source = 'user', expired_run_id = NULL, expiration_reason = 'deleted by user', updated_at = ?
			WHERE session_id = ? AND id = ? AND is_expired = 0`, now, now, sessionID, memoryID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		if requestCancellationCausedPersistenceError(request.Context(), err) {
			r.mu.Unlock()
			writeHTTPError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before the mutation committed")
			return
		}
		r.latchPersistenceFaultLocked(err)
		r.mu.Unlock()
		writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	if userExpiredAt != nil {
		source, reason := "user", "deleted by user"
		memory.IsExpired = true
		memory.ExpiredAt = userExpiredAt
		memory.ExpiredSource = &source
		memory.ExpirationReason = &reason
		memory.UpdatedAt = *userExpiredAt
	}
	r.mu.Unlock()
	writeRuntimeJSON(w, http.StatusOK, memory)
}
