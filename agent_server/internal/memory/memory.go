// Package memory implements the Session memory store: Memory records,
// finalization parsing, and the read queries used by the Agent loop and the
// memory HTTP API.
package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"stock-ai/agent-server/internal/tools"
)

const (
	PageSize           = 100
	maxCandidates      = 20
	maxTypeRunes       = 64
	maxSummaryRunes    = 500
	maxContentBytes    = 64 << 10
	maxExpirationRunes = 500
)

var Schema = []string{
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

type Metadata struct {
	ID         string `json:"id"`
	RunID      int64  `json:"run_id"`
	Type       string `json:"type"`
	Summary    string `json:"summary"`
	Importance int    `json:"importance"`
	IsExpired  bool   `json:"is_expired"`
}

type ListItem struct {
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

type RecentRun struct {
	RunID   int64  `json:"run_id"`
	Summary string `json:"summary"`
}

type Candidate struct {
	Type       string `json:"type"`
	Summary    string `json:"summary"`
	Content    string `json:"content"`
	Importance int    `json:"importance"`
}

type Expiration struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type Finalization struct {
	Output            string       `json:"output"`
	Summary           string       `json:"summary"`
	CandidateMemories []Candidate  `json:"candidate_memories"`
	ExpireMemories    []Expiration `json:"expire_memories"`
}

type ReferenceError struct{ message string }

func (err *ReferenceError) Error() string { return err.message }

// NewReferenceError reports that a finalization references a Memory that
// does not exist in the Session.
func NewReferenceError(id string) *ReferenceError {
	return &ReferenceError{message: fmt.Sprintf("Memory %s does not exist in this Session", id)}
}

const Instruction = `

When your work is normally complete, finish with finish_reason "stop" and content containing exactly one JSON object:
{"output":"human-readable final decision","summary":"short summary (max 500 Unicode characters)","candidate_memories":[],"expire_memories":[]}
All four fields are required. candidate_memories may contain at most 20 objects with only type (max 64 Unicode characters), summary (max 500 Unicode characters), content (max 65536 UTF-8 bytes), and importance (1, 2, or 3). expire_memories may contain at most 20 objects with only a Server-issued memory id and a reason of at most 500 Unicode characters. Do not include session_id, run_id, timestamps, credentials, hidden reasoning, or extra fields. Tool calls continue to use the structured tool channel; do not put this final object in a tool call.`

func ParseFinalization(content string) (Finalization, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var envelope struct {
		Output            *string       `json:"output"`
		Summary           *string       `json:"summary"`
		CandidateMemories *[]Candidate  `json:"candidate_memories"`
		ExpireMemories    *[]Expiration `json:"expire_memories"`
	}
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		envelope.Output == nil || envelope.Summary == nil || envelope.CandidateMemories == nil || envelope.ExpireMemories == nil {
		return Finalization{}, errors.New("content must be exactly one finalization JSON object with known fields")
	}
	output := Finalization{
		Output: *envelope.Output, Summary: *envelope.Summary,
		CandidateMemories: *envelope.CandidateMemories, ExpireMemories: *envelope.ExpireMemories,
	}
	output.Output = strings.TrimSpace(output.Output)
	output.Summary = strings.TrimSpace(output.Summary)
	if output.Output == "" || output.Summary == "" {
		return Finalization{}, errors.New("output and summary must be nonempty strings")
	}
	if utf8.RuneCountInString(output.Summary) > maxSummaryRunes {
		return Finalization{}, fmt.Errorf("summary must not exceed %d Unicode characters", maxSummaryRunes)
	}
	if len(output.CandidateMemories) > maxCandidates || len(output.ExpireMemories) > maxCandidates {
		return Finalization{}, fmt.Errorf("candidate_memories and expire_memories may each contain at most %d items", maxCandidates)
	}
	for index := range output.CandidateMemories {
		candidate := &output.CandidateMemories[index]
		candidate.Type = strings.TrimSpace(candidate.Type)
		candidate.Summary = strings.TrimSpace(candidate.Summary)
		candidate.Content = strings.TrimSpace(candidate.Content)
		if candidate.Type == "" || utf8.RuneCountInString(candidate.Type) > maxTypeRunes {
			return Finalization{}, fmt.Errorf("candidate_memories[%d].type must contain 1 through %d Unicode characters", index, maxTypeRunes)
		}
		if candidate.Summary == "" || utf8.RuneCountInString(candidate.Summary) > maxSummaryRunes {
			return Finalization{}, fmt.Errorf("candidate_memories[%d].summary must contain 1 through %d Unicode characters", index, maxSummaryRunes)
		}
		if candidate.Content == "" || len(candidate.Content) > maxContentBytes {
			return Finalization{}, fmt.Errorf("candidate_memories[%d].content must contain 1 through %d UTF-8 bytes", index, maxContentBytes)
		}
		if candidate.Importance < 1 || candidate.Importance > 3 {
			return Finalization{}, fmt.Errorf("candidate_memories[%d].importance must be 1, 2, or 3", index)
		}
	}
	seen := make(map[string]bool, len(output.ExpireMemories))
	for index := range output.ExpireMemories {
		expiration := &output.ExpireMemories[index]
		expiration.ID = strings.TrimSpace(expiration.ID)
		expiration.Reason = strings.TrimSpace(expiration.Reason)
		if !ValidMemoryID(expiration.ID) {
			return Finalization{}, fmt.Errorf("expire_memories[%d].id must be a Server-issued Memory ID", index)
		}
		if expiration.Reason == "" || utf8.RuneCountInString(expiration.Reason) > maxExpirationRunes {
			return Finalization{}, fmt.Errorf("expire_memories[%d].reason must contain 1 through %d Unicode characters", index, maxExpirationRunes)
		}
		if seen[expiration.ID] {
			return Finalization{}, fmt.Errorf("expire_memories contains duplicate id %q", expiration.ID)
		}
		seen[expiration.ID] = true
	}
	return output, nil
}

func ValidMemoryID(id string) bool {
	if len(id) != 34 || !strings.HasPrefix(id, "m_") || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id[2:])
	return err == nil
}

func NewMemoryID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("unable to create Memory ID")
	}
	return "m_" + hex.EncodeToString(value), nil
}

type scanner interface {
	Scan(dest ...any) error
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func ScanMemory(row scanner) (Memory, error) {
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

const Select = `SELECT id, session_id, run_id, type, summary, content, importance,
	run_start_at, run_end_at, is_expired, expired_at, expired_source, expired_run_id,
	expiration_reason, created_at, updated_at FROM memories`

const ListSelect = `SELECT id, session_id, run_id, type, summary, importance,
	run_start_at, run_end_at, is_expired, expired_at, expired_source, expired_run_id,
	expiration_reason, created_at, updated_at FROM memories`

func ScanListItem(row scanner) (ListItem, error) {
	var memory ListItem
	var expiredAt, expiredSource, expirationReason sql.NullString
	var expiredRunID sql.NullInt64
	err := row.Scan(&memory.ID, &memory.SessionID, &memory.RunID, &memory.Type, &memory.Summary,
		&memory.Importance, &memory.RunStartAt, &memory.RunEndAt, &memory.IsExpired,
		&expiredAt, &expiredSource, &expiredRunID, &expirationReason, &memory.CreatedAt, &memory.UpdatedAt)
	if err != nil {
		return ListItem{}, err
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

func List(ctx context.Context, db *sql.DB, sessionID, query string, limit int) ([]Metadata, error) {
	statement := `SELECT id, run_id, type, summary, importance, is_expired FROM memories
		WHERE session_id = ? AND is_expired = 0`
	args := []any{sessionID}
	if query != "" {
		statement += ` AND (instr(lower(summary), lower(?)) > 0 OR instr(lower(type), lower(?)) > 0)`
		args = append(args, query, query)
	}
	statement += ` ORDER BY importance DESC, run_id DESC, id LIMIT ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memories := make([]Metadata, 0)
	for rows.Next() {
		var memory Metadata
		if err := rows.Scan(&memory.ID, &memory.RunID, &memory.Type, &memory.Summary, &memory.Importance, &memory.IsExpired); err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func MemoryByID(ctx context.Context, db *sql.DB, sessionID, memoryID string, includeExpired bool) (Memory, error) {
	query := Select + ` WHERE session_id = ? AND id = ?`
	if !includeExpired {
		query += ` AND is_expired = 0`
	}
	return ScanMemory(db.QueryRowContext(ctx, query, sessionID, memoryID))
}

func ProgressiveContext(ctx context.Context, db *sql.DB, sessionID string, currentRunID int64) (*RecentRun, []Metadata, error) {
	var recent RecentRun
	err := db.QueryRowContext(ctx, `SELECT run_id, summary FROM runs
		WHERE session_id = ? AND run_id < ? AND status = 'completed' AND summary IS NOT NULL
		ORDER BY run_id DESC LIMIT 1`, sessionID, currentRunID).Scan(&recent.RunID, &recent.Summary)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	var recentPointer *RecentRun
	if err == nil {
		recentPointer = &recent
	}
	memories, err := List(ctx, db, sessionID, "", tools.MemoryDefaultLimit)
	return recentPointer, memories, err
}
