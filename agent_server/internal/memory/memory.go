// Package memory implements the Session memory cabinet: labelled memories the Agent saves,
// edits and reads during any Run, finalization parsing, and the queries used by the Agent
// loop and the memory HTTP API.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"stock-ai/agent-server/internal/tools"
)

const (
	Capacity          = 60
	MaxTitleRunes     = 100
	MaxMsgRunes       = 800
	MaxBringBackRunes = 3000
	MaxListItems      = 20
	RecentIndexItems  = 10
	maxSummaryRunes   = 500
	// A fixed-width UTC layout keeps created_at string comparisons chronological.
	TimeLayout = "2006-01-02T15:04:05.000Z07:00"
)

// ErrNotFound reports a memory id that does not exist in the Session.
var ErrNotFound = errors.New("memory does not exist in this Session")

// LimitError is a write rejected by a cabinet limit; Code is the Tool error code.
type LimitError struct{ Code, Message string }

func (err *LimitError) Error() string { return err.Message }

type Memory struct {
	ID           int64  `json:"id"`
	SessionID    string `json:"session_id"`
	Title        string `json:"title"`
	Msg          string `json:"msg,omitempty"`
	BringBack    bool   `json:"bring_back"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	CreatedRunID *int64 `json:"created_run_id"`
	UpdatedRunID *int64 `json:"updated_run_id"`
}

// Label is what the Agent sees on the outside of a cabinet drawer.
type Label struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

// Recalled is a bring_back memory injected into every Run's starting context.
type Recalled struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Msg   string `json:"msg"`
}

type RecentRun struct {
	RunID   int64  `json:"run_id"`
	Summary string `json:"summary"`
}

type Filter struct {
	Query    string
	Since    *time.Time
	Until    *time.Time
	BeforeID int64
	AfterID  int64
	Limit    int
}

type Edit struct {
	Title     *string
	Msg       *string
	BringBack *bool
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

const schema = `CREATE TABLE IF NOT EXISTS memories (
	id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
	title TEXT NOT NULL, msg TEXT NOT NULL,
	bring_back INTEGER NOT NULL DEFAULT 0 CHECK (bring_back IN (0, 1)),
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
	created_run_id INTEGER, updated_run_id INTEGER
)`

// runMemories records which memory ids each Run created, independently of the cabinet, so
// History keeps a Run's creation record after a memory is deleted and keeps legacy m_ ids.
const runMemoriesSchema = `CREATE TABLE IF NOT EXISTS run_memories (
	session_id TEXT NOT NULL, run_id INTEGER NOT NULL, memory_id TEXT NOT NULL,
	PRIMARY KEY (session_id, run_id, memory_id)
)`

// EnsureSchema creates the cabinet table. A legacy candidate-memory table is renamed to
// memories_legacy and kept for audit; its unexpired rows are copied in as cabinet memories.
func EnsureSchema(db *sql.DB) error {
	var legacy int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'candidate_index'`).Scan(&legacy); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if legacy != 0 {
		if _, err := tx.Exec(`ALTER TABLE memories RENAME TO memories_legacy`); err != nil {
			return err
		}
	}
	for _, statement := range []string{schema, runMemoriesSchema, `CREATE INDEX IF NOT EXISTS memories_cabinet ON memories(session_id, id)`} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if legacy != 0 {
		if err := copyLegacyMemories(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func copyLegacyMemories(tx *sql.Tx) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO run_memories (session_id, run_id, memory_id)
		SELECT session_id, run_id, id FROM memories_legacy ORDER BY session_id, run_id, candidate_index`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT session_id, summary, content, run_id, created_at, updated_at
		FROM memories_legacy WHERE is_expired = 0 ORDER BY created_at, candidate_index`)
	if err != nil {
		return err
	}
	type legacyRow struct {
		sessionID, title, msg, createdAt, updatedAt string
		runID                                       int64
	}
	var legacy []legacyRow
	for rows.Next() {
		var row legacyRow
		if err := rows.Scan(&row.sessionID, &row.title, &row.msg, &row.runID, &row.createdAt, &row.updatedAt); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, row := range legacy {
		if _, err := tx.Exec(`INSERT INTO memories (session_id, title, msg, bring_back, created_at, updated_at, created_run_id, updated_run_id)
			VALUES (?, ?, ?, 0, ?, ?, ?, ?)`, row.sessionID, truncateRunes(row.title, MaxTitleRunes), truncateRunes(row.msg, MaxMsgRunes),
			normalizeTime(row.createdAt), normalizeTime(row.updatedAt), row.runID, row.runID); err != nil {
			return err
		}
	}
	return nil
}

func normalizeTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format(TimeLayout)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

const memorySelect = `SELECT id, session_id, title, msg, bring_back, created_at, updated_at, created_run_id, updated_run_id FROM memories`

func scanMemory(row interface{ Scan(...any) error }) (Memory, error) {
	var memory Memory
	var createdRunID, updatedRunID sql.NullInt64
	err := row.Scan(&memory.ID, &memory.SessionID, &memory.Title, &memory.Msg, &memory.BringBack,
		&memory.CreatedAt, &memory.UpdatedAt, &createdRunID, &updatedRunID)
	if createdRunID.Valid {
		memory.CreatedRunID = &createdRunID.Int64
	}
	if updatedRunID.Valid {
		memory.UpdatedRunID = &updatedRunID.Int64
	}
	return memory, err
}

func Get(ctx context.Context, db querier, sessionID string, id int64) (Memory, error) {
	memory, err := scanMemory(db.QueryRowContext(ctx, memorySelect+` WHERE session_id = ? AND id = ?`, sessionID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, ErrNotFound
	}
	return memory, err
}

// All returns every memory of the Session without msg, for the user's cabinet view.
func All(ctx context.Context, db querier, sessionID string) ([]Memory, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, title, '', bring_back, created_at, updated_at,
		created_run_id, updated_run_id FROM memories WHERE session_id = ? ORDER BY id DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memories := make([]Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func List(ctx context.Context, db querier, sessionID string, filter Filter) ([]Label, error) {
	statement := `SELECT id, title, created_at FROM memories WHERE session_id = ?`
	args := []any{sessionID}
	if filter.Query != "" {
		statement += ` AND instr(lower(title), lower(?)) > 0`
		args = append(args, filter.Query)
	}
	if filter.Since != nil {
		statement += ` AND created_at >= ?`
		args = append(args, filter.Since.UTC().Format(TimeLayout))
	}
	if filter.Until != nil {
		statement += ` AND created_at < ?`
		args = append(args, filter.Until.UTC().Format(TimeLayout))
	}
	if filter.BeforeID > 0 {
		statement += ` AND id < ?`
		args = append(args, filter.BeforeID)
	}
	if filter.AfterID > 0 {
		statement += ` AND id > ?`
		args = append(args, filter.AfterID)
	}
	limit := filter.Limit
	if limit <= 0 || limit > MaxListItems {
		limit = MaxListItems
	}
	rows, err := db.QueryContext(ctx, statement+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := make([]Label, 0)
	for rows.Next() {
		var label Label
		if err := rows.Scan(&label.ID, &label.Title, &label.CreatedAt); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// Count returns how many memories the Session stores and how many are bring_back.
func Count(ctx context.Context, db querier, sessionID string) (int, int, error) {
	var stored, bringBack int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(bring_back), 0) FROM memories WHERE session_id = ?`,
		sessionID).Scan(&stored, &bringBack)
	return stored, bringBack, err
}

func Save(ctx context.Context, tx querier, sessionID string, runID int64, title, msg string, bringBack bool, now time.Time) (Memory, error) {
	stored, _, err := Count(ctx, tx, sessionID)
	if err != nil {
		return Memory{}, err
	}
	if stored >= Capacity {
		return Memory{}, &LimitError{"MEMORY_FULL", fmt.Sprintf(
			"the cabinet already holds %d memories (max %d); memory_edit an existing memory instead", stored, Capacity)}
	}
	if bringBack {
		if err := checkBringBackBudget(ctx, tx, sessionID, 0, msg); err != nil {
			return Memory{}, err
		}
	}
	at := now.UTC().Format(TimeLayout)
	result, err := tx.ExecContext(ctx, `INSERT INTO memories (session_id, title, msg, bring_back, created_at, updated_at, created_run_id, updated_run_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sessionID, title, msg, bringBack, at, at, runID, runID)
	if err != nil {
		return Memory{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Memory{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_memories (session_id, run_id, memory_id) VALUES (?, ?, ?)`,
		sessionID, runID, strconv.FormatInt(id, 10)); err != nil {
		return Memory{}, err
	}
	return Get(ctx, tx, sessionID, id)
}

func Update(ctx context.Context, tx querier, sessionID string, runID, id int64, edit Edit, now time.Time) (Memory, error) {
	memory, err := Get(ctx, tx, sessionID, id)
	if err != nil {
		return Memory{}, err
	}
	if edit.Title != nil {
		memory.Title = *edit.Title
	}
	if edit.Msg != nil {
		memory.Msg = *edit.Msg
	}
	if edit.BringBack != nil {
		memory.BringBack = *edit.BringBack
	}
	if memory.BringBack {
		if err := checkBringBackBudget(ctx, tx, sessionID, id, memory.Msg); err != nil {
			return Memory{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET title = ?, msg = ?, bring_back = ?, updated_at = ?, updated_run_id = ?
		WHERE session_id = ? AND id = ?`, memory.Title, memory.Msg, memory.BringBack, now.UTC().Format(TimeLayout), runID, sessionID, id); err != nil {
		return Memory{}, err
	}
	return Get(ctx, tx, sessionID, id)
}

func checkBringBackBudget(ctx context.Context, tx querier, sessionID string, excludeID int64, msg string) error {
	// Counted in Go like validateText: SQLite length() stops at the first NUL character.
	rows, err := tx.QueryContext(ctx, `SELECT msg FROM memories WHERE session_id = ? AND bring_back = 1 AND id != ?`, sessionID, excludeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	used := 0
	for rows.Next() {
		var pinned string
		if err := rows.Scan(&pinned); err != nil {
			return err
		}
		used += utf8.RuneCountInString(pinned)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if used+utf8.RuneCountInString(msg) > MaxBringBackRunes {
		return &LimitError{"BRING_BACK_LIMIT", fmt.Sprintf(
			"bring_back memories may total at most %d characters of msg and %d are already used; shorten one or set bring_back to false",
			MaxBringBackRunes, used)}
	}
	return nil
}

func Delete(ctx context.Context, tx querier, sessionID string, id int64) (Memory, error) {
	memory, err := Get(ctx, tx, sessionID, id)
	if err != nil {
		return Memory{}, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM memories WHERE session_id = ? AND id = ?`, sessionID, id)
	return memory, err
}

// ProgressiveContext returns what a Run starts with: the latest earlier completed Run's
// summary, the newest memory labels, and every bring_back memory in full.
func ProgressiveContext(ctx context.Context, db querier, sessionID string, currentRunID int64) (*RecentRun, []Label, []Recalled, error) {
	var recent *RecentRun
	var value RecentRun
	err := db.QueryRowContext(ctx, `SELECT run_id, summary FROM runs
		WHERE session_id = ? AND run_id < ? AND status = 'completed' AND summary IS NOT NULL
		ORDER BY run_id DESC LIMIT 1`, sessionID, currentRunID).Scan(&value.RunID, &value.Summary)
	if err == nil {
		recent = &value
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, err
	}
	labels, err := List(ctx, db, sessionID, Filter{Limit: RecentIndexItems})
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, title, msg FROM memories WHERE session_id = ? AND bring_back = 1 ORDER BY id`, sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	recalled := make([]Recalled, 0)
	for rows.Next() {
		var memory Recalled
		if err := rows.Scan(&memory.ID, &memory.Title, &memory.Msg); err != nil {
			return nil, nil, nil, err
		}
		recalled = append(recalled, memory)
	}
	return recent, labels, recalled, rows.Err()
}

// ParseListArguments parses memory_list arguments; every field is optional.
func ParseListArguments(arguments json.RawMessage, now time.Time) (Filter, error) {
	var raw struct {
		Query       *string  `json:"query"`
		WithinHours *float64 `json:"within_hours"`
		From        *string  `json:"from"`
		To          *string  `json:"to"`
		BeforeID    *int64   `json:"before_id"`
		AfterID     *int64   `json:"after_id"`
		Limit       *int     `json:"limit"`
	}
	if !tools.DecodeArguments(arguments, &raw) {
		return Filter{}, errors.New("only query, within_hours, from, to, before_id, after_id and limit are accepted")
	}
	filter := Filter{Limit: MaxListItems}
	if raw.Query != nil {
		filter.Query = strings.TrimSpace(*raw.Query)
	}
	if raw.WithinHours != nil {
		if *raw.WithinHours <= 0 || *raw.WithinHours > 24*366 {
			return Filter{}, errors.New("within_hours must be greater than 0 and at most 8784")
		}
		since := now.Add(-time.Duration(*raw.WithinHours * float64(time.Hour)))
		filter.Since = &since
	}
	if raw.From != nil {
		from, _, err := parseBound(*raw.From)
		if err != nil {
			return Filter{}, fmt.Errorf("from %w", err)
		}
		if filter.Since == nil || from.After(*filter.Since) {
			filter.Since = &from
		}
	}
	if raw.To != nil {
		to, dateOnly, err := parseBound(*raw.To)
		if err != nil {
			return Filter{}, fmt.Errorf("to %w", err)
		}
		if dateOnly {
			to = to.AddDate(0, 0, 1)
		}
		filter.Until = &to
	}
	if raw.BeforeID != nil {
		if *raw.BeforeID < 1 {
			return Filter{}, errors.New("before_id must be a positive integer")
		}
		filter.BeforeID = *raw.BeforeID
	}
	if raw.AfterID != nil {
		if *raw.AfterID < 1 {
			return Filter{}, errors.New("after_id must be a positive integer")
		}
		filter.AfterID = *raw.AfterID
	}
	if raw.Limit != nil {
		if *raw.Limit < 1 || *raw.Limit > MaxListItems {
			return Filter{}, fmt.Errorf("limit must be an integer from 1 through %d", MaxListItems)
		}
		filter.Limit = *raw.Limit
	}
	return filter, nil
}

// parseBound accepts YYYY-MM-DD (a whole UTC day) or an RFC3339 timestamp.
func parseBound(value string) (time.Time, bool, error) {
	value = strings.TrimSpace(value)
	if day, err := time.Parse("2006-01-02", value); err == nil {
		return day, true, nil
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, false, nil
	}
	return time.Time{}, false, errors.New("must be YYYY-MM-DD or an RFC3339 timestamp")
}

func ParseIDArgument(arguments json.RawMessage) (int64, error) {
	var raw struct {
		ID *int64 `json:"id"`
	}
	if !tools.DecodeArguments(arguments, &raw) || raw.ID == nil || *raw.ID < 1 {
		return 0, errors.New("id must be a positive integer and is the only accepted field")
	}
	return *raw.ID, nil
}

func ParseSaveArguments(arguments json.RawMessage) (string, string, bool, error) {
	var raw struct {
		Title     *string `json:"title"`
		Msg       *string `json:"msg"`
		BringBack *bool   `json:"bring_back"`
	}
	if !tools.DecodeArguments(arguments, &raw) || raw.Title == nil || raw.Msg == nil {
		return "", "", false, errors.New("title and msg are required; bring_back is optional")
	}
	title, msg := strings.TrimSpace(*raw.Title), strings.TrimSpace(*raw.Msg)
	if err := validateText(title, msg); err != nil {
		return "", "", false, err
	}
	return title, msg, raw.BringBack != nil && *raw.BringBack, nil
}

func ParseEditArguments(arguments json.RawMessage) (int64, Edit, error) {
	var raw struct {
		ID        *int64  `json:"id"`
		Title     *string `json:"title"`
		Msg       *string `json:"msg"`
		BringBack *bool   `json:"bring_back"`
	}
	if !tools.DecodeArguments(arguments, &raw) || raw.ID == nil || *raw.ID < 1 {
		return 0, Edit{}, errors.New("id must be a positive integer; title, msg and bring_back are optional")
	}
	if raw.Title == nil && raw.Msg == nil && raw.BringBack == nil {
		return 0, Edit{}, errors.New("provide at least one of title, msg or bring_back")
	}
	edit := Edit{BringBack: raw.BringBack}
	if raw.Title != nil {
		title := strings.TrimSpace(*raw.Title)
		if err := validateText(title, "-"); err != nil {
			return 0, Edit{}, err
		}
		edit.Title = &title
	}
	if raw.Msg != nil {
		msg := strings.TrimSpace(*raw.Msg)
		if err := validateText("-", msg); err != nil {
			return 0, Edit{}, err
		}
		edit.Msg = &msg
	}
	return *raw.ID, edit, nil
}

func validateText(title, msg string) error {
	if title == "" || utf8.RuneCountInString(title) > MaxTitleRunes {
		return fmt.Errorf("title must contain 1 through %d characters", MaxTitleRunes)
	}
	if msg == "" || utf8.RuneCountInString(msg) > MaxMsgRunes {
		return fmt.Errorf("msg must contain 1 through %d characters", MaxMsgRunes)
	}
	return nil
}

const Instruction = `

Memory cabinet: this Session keeps a private cabinet of at most 60 memories that lasts across Runs; no other Account can see it. Each memory has an id, a title (max 100 characters), a msg (max 800 characters) and bring_back. Every Run starts with current_time, memory_index (labels of the newest memories) and bring_back_memories (the full text of pinned memories) in its context.
Before deciding, read memory_index and bring_back_memories. Use memory_list to find older labels by title keyword (query), within_hours, from/to (YYYY-MM-DD or RFC3339, UTC) or before_id/after_id, then memory_get only the memories that matter.
Save as soon as you learn something worth keeping, not only at the end: lessons from outcomes, strategy rules that worked or failed, account constraints, your plan for the next Run, and instructions from the user. Prefer memory_edit to correct or extend an existing memory over saving a duplicate; when the cabinet is full, edit the least useful one. Set bring_back=true only for rules you must recall at the start of every Run (all bring_back msgs together may hold at most 3000 characters).
Do not store trades, orders, positions, prices or routine status: your trades are already recorded in the ledger (get_trades, get_ledger) and live state comes from tools.

When your work is normally complete, finish with finish_reason "stop" and content containing exactly one JSON object:
{"status":"completed","output":"human-readable final decision","summary":"short summary for the next Run (max 500 Unicode characters)"}
output and summary are required; summary is given to the next Run as recent_run_summary. Use status "completed" when the requested work succeeded or a deliberate no-action decision was reached. If a required tool or dependency is unavailable and prevents the work, use status "failed" and explain the blocker in output. Never claim completion merely because you produced a final response. Do not include extra fields, credentials or hidden reasoning. Tool calls continue to use the structured tool channel; do not put this final object in a tool call.`

type Finalization struct {
	Status  string `json:"status,omitempty"`
	Output  string `json:"output"`
	Summary string `json:"summary"`
}

func ParseFinalization(content string) (Finalization, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var envelope struct {
		Status  string  `json:"status"`
		Output  *string `json:"output"`
		Summary *string `json:"summary"`
	}
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		envelope.Output == nil || envelope.Summary == nil {
		return Finalization{}, errors.New("content must be exactly one finalization JSON object with status, output and summary only")
	}
	if envelope.Status != "" && envelope.Status != "completed" && envelope.Status != "failed" {
		return Finalization{}, errors.New("status must be completed or failed")
	}
	output := Finalization{Status: envelope.Status, Output: strings.TrimSpace(*envelope.Output), Summary: strings.TrimSpace(*envelope.Summary)}
	if output.Output == "" || output.Summary == "" {
		return Finalization{}, errors.New("output and summary must be nonempty strings")
	}
	if utf8.RuneCountInString(output.Summary) > maxSummaryRunes {
		return Finalization{}, fmt.Errorf("summary must not exceed %d Unicode characters", maxSummaryRunes)
	}
	return output, nil
}
