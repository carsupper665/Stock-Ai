package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"stock-ai/agent-server/internal/events"
	"stock-ai/agent-server/internal/history"
	"stock-ai/agent-server/internal/memory"
)

const (
	sessionPageSize         = 100
	maxRuntimeBody          = 1 << 20
	restartProgressEntries  = 5
	maxModelGenerateRetries = 3
	maxFinalizationRetries  = 5
	maxManualRunRetries     = 3
)

type RuntimeConfig struct {
	DatabasePath        string
	HistoryDirectory    string
	AdminToken          string
	BackendURL          string
	BackendUserToken    string
	GatewayURL          string
	GatewayRuntimeToken string
	DefaultPrompt       string
	DefaultMaxLoop      int
	DefaultMaxToolCall  int
	RequestTimeout      time.Duration
	ToolTimeout         time.Duration
	StopTimeout         time.Duration
	Logger              *log.Logger
}

type AgentRuntime struct {
	db         *sql.DB
	config     RuntimeConfig
	httpClient *http.Client
	logger     *log.Logger

	mu        sync.Mutex
	historyMu sync.Mutex
	active    map[string]*activeRun
	wg        sync.WaitGroup
	closed    bool

	persistenceErr error
	quiesceOnce    sync.Once
	quiesceErr     error
	closeOnce      sync.Once
	closeErr       error
}

type activeRun struct {
	runID  int64
	cancel context.CancelFunc
	done   chan struct{}
}

type Session struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	AccountID      string          `json:"account_id"`
	ModelName      string          `json:"model_name"`
	ModelLevel     *string         `json:"model_level"`
	Prompt         string          `json:"prompt"`
	MaxLoop        int             `json:"max_loop"`
	MaxToolCall    int             `json:"max_tool_call"`
	Status         string          `json:"status"`
	CurrentRunID   int64           `json:"current_run_id"`
	RestartContext json.RawMessage `json:"restart_context"`
	CreatedAt      string          `json:"created_at"`
	StartedAt      *string         `json:"started_at"`
	StoppedAt      *string         `json:"stopped_at"`
	DeletedAt      *string         `json:"deleted_at"`
}

type Run struct {
	SessionID      string          `json:"session_id"`
	RunID          int64           `json:"run_id"`
	Status         string          `json:"status"`
	Trigger        string          `json:"trigger"`
	Triggers       json.RawMessage `json:"triggers"`
	Summary        *string         `json:"summary"`
	Output         *string         `json:"output"`
	Error          *string         `json:"error"`
	Progress       json.RawMessage `json:"progress"`
	InitialContext json.RawMessage `json:"initial_context"`
	ModelCalls     int             `json:"model_call_count"`
	DecisionCount  int             `json:"decision_count"`
	ToolCalls      int             `json:"tool_call_count"`
	StartedAt      string          `json:"start_time"`
	EndedAt        *string         `json:"end_time"`
	RetryOfRunID   *int64          `json:"retry_of_run_id,omitempty"`
	RetryAttempt   int             `json:"retry_attempt,omitempty"`
}

type modelMapping struct {
	ModelName  string                                `json:"model_name"`
	ProviderID string                                `json:"provider_id"`
	Model      string                                `json:"model"`
	Levels     map[string]map[string]json.RawMessage `json:"levels"`
	Enabled    bool                                  `json:"enabled"`
	Available  bool                                  `json:"available"`
}

type gatewayMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content"`
	ToolCalls  []gatewayTool `json:"tool_calls,omitempty"`
	ToolCallID *string       `json:"tool_call_id,omitempty"`
}

type gatewayTool struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type gatewayResponse struct {
	Content         *string       `json:"content"`
	Reasoning       *string       `json:"reasoning,omitempty"`
	ToolCalls       []gatewayTool `json:"tool_calls"`
	FinishReason    string        `json:"finish_reason"`
	Usage           *gatewayUsage `json:"usage"`
	NumTurns        *int          `json:"num_turns,omitempty"`
	TotalCostUSD    *float64      `json:"total_cost_usd,omitempty"`
	SessionMode     *string       `json:"session_mode,omitempty"`
	FallbackReason  *string       `json:"fallback_reason,omitempty"`
	InvocationCount *int          `json:"invocation_count,omitempty"`
}

type traceEntry struct {
	Type                  string          `json:"type"`
	AttemptType           string          `json:"attempt_type,omitempty"`
	Iteration             int             `json:"iteration,omitempty"`
	Attempt               int             `json:"attempt,omitempty"`
	ModelName             string          `json:"model_name,omitempty"`
	ProviderID            string          `json:"provider_id,omitempty"`
	Model                 string          `json:"model,omitempty"`
	Content               *string         `json:"content,omitempty"`
	Reasoning             *string         `json:"reasoning,omitempty"`
	FinishReason          string          `json:"finish_reason,omitempty"`
	ToolCalls             []gatewayTool   `json:"tool_calls,omitempty"`
	ToolCallID            string          `json:"tool_call_id,omitempty"`
	ToolName              string          `json:"tool_name,omitempty"`
	Arguments             json.RawMessage `json:"arguments,omitempty"`
	Result                json.RawMessage `json:"result,omitempty"`
	Usage                 *gatewayUsage   `json:"usage,omitempty"`
	StartedAt             string          `json:"started_at,omitempty"`
	EndedAt               *string         `json:"ended_at,omitempty"`
	DurationMS            *int64          `json:"duration_ms,omitempty"`
	Error                 string          `json:"error,omitempty"`
	ConversationID        string          `json:"conversation_id,omitempty"`
	NumTurns              *int            `json:"num_turns,omitempty"`
	TotalCostUSD          *float64        `json:"total_cost_usd,omitempty"`
	SessionMode           *string         `json:"session_mode,omitempty"`
	FallbackReason        *string         `json:"fallback_reason,omitempty"`
	InvocationCount       *int            `json:"invocation_count,omitempty"`
	RequestBytes          int             `json:"request_bytes,omitempty"`
	ContextOriginalBytes  int             `json:"context_original_bytes,omitempty"`
	ContextProjectedBytes int             `json:"context_projected_bytes,omitempty"`
	ContextCompacted      int             `json:"context_compacted_messages,omitempty"`
	CountsAsDecision      bool            `json:"counts_as_decision,omitempty"`
}

func OpenAgentRuntime(config RuntimeConfig) (*AgentRuntime, error) {
	if strings.TrimSpace(config.HistoryDirectory) == "" && strings.TrimSpace(config.DatabasePath) != "" {
		config.HistoryDirectory = config.DatabasePath + ".history"
	}
	if err := validateRuntimeConfig(config); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", config.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("open agent database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range append(append(append([]string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, account_id TEXT NOT NULL UNIQUE,
			model_name TEXT NOT NULL, model_level TEXT, prompt TEXT NOT NULL,
			max_loop INTEGER NOT NULL, max_tool_call INTEGER NOT NULL,
			status TEXT NOT NULL, current_run_id INTEGER NOT NULL,
			restart_context TEXT, created_at TEXT NOT NULL, started_at TEXT,
			stopped_at TEXT, deleted_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			session_id TEXT NOT NULL, run_id INTEGER NOT NULL, status TEXT NOT NULL,
			trigger TEXT NOT NULL, triggers_json TEXT, summary TEXT, output TEXT, error TEXT,
			progress_json TEXT NOT NULL, initial_context_json TEXT, model_call_count INTEGER NOT NULL,
			tool_call_count INTEGER NOT NULL, start_time TEXT NOT NULL, end_time TEXT,
			retry_of_run_id INTEGER, retry_attempt INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, run_id)
		)`,
	}, events.Schema...), history.Schema...), memory.Schema...) {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize agent database: %w", err)
		}
	}
	if err := ensureRunTriggersColumn(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate agent database: %w", err)
	}
	if err := ensureRunRetryColumns(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate agent database: %w", err)
	}
	if err := ensureRunInitialContextColumn(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate agent database: %w", err)
	}
	if err := os.MkdirAll(config.HistoryDirectory, 0o700); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize Run History directory: %w", err)
	}
	runtime := &AgentRuntime{
		db: db, config: config, active: make(map[string]*activeRun), logger: config.Logger,
		httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	if err := runtime.repairRunHistory(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repair Run History: %w", err)
	}
	if err := runtime.recoverPersistedRuntime(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recover Agent Runtime: %w", err)
	}
	return runtime, nil
}

func ensureRunTriggersColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "triggers_json" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE runs ADD COLUMN triggers_json TEXT`)
	return err
}

func ensureRunRetryColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	foundRetryOf, foundAttempt := false, false
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		foundRetryOf = foundRetryOf || name == "retry_of_run_id"
		foundAttempt = foundAttempt || name == "retry_attempt"
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !foundRetryOf {
		if _, err := db.Exec(`ALTER TABLE runs ADD COLUMN retry_of_run_id INTEGER`); err != nil {
			return err
		}
	}
	if !foundAttempt {
		_, err = db.Exec(`ALTER TABLE runs ADD COLUMN retry_attempt INTEGER NOT NULL DEFAULT 0`)
	}
	return err
}

func ensureRunInitialContextColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		found = found || name == "initial_context_json"
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE runs ADD COLUMN initial_context_json TEXT`)
	return err
}

func validateRuntimeConfig(config RuntimeConfig) error {
	if strings.TrimSpace(config.DatabasePath) == "" {
		return errors.New("agent database path is required")
	}
	if strings.TrimSpace(config.HistoryDirectory) == "" {
		return errors.New("Run History directory is required")
	}
	for name, value := range map[string]string{
		"agent admin token": config.AdminToken, "Backend USER token": config.BackendUserToken,
		"Gateway runtime token": config.GatewayRuntimeToken,
	} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{"Backend URL": config.BackendURL, "Gateway URL": config.GatewayURL} {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s must be an HTTP(S) origin or base path without credentials, query, or fragment", name)
		}
	}
	if config.DefaultMaxLoop <= 0 || config.DefaultMaxToolCall <= 0 {
		return errors.New("default loop and tool-call limits must be positive")
	}
	if config.RequestTimeout <= 0 || config.ToolTimeout <= 0 || config.StopTimeout <= 0 {
		return errors.New("request, tool, and stop timeouts must be positive")
	}
	return nil
}

func (r *AgentRuntime) Close() error {
	r.closeOnce.Do(func() {
		quiesceErr := r.Quiesce()
		r.wg.Wait()
		r.mu.Lock()
		persistenceErr := r.persistenceErr
		r.mu.Unlock()
		r.closeErr = errors.Join(quiesceErr, persistenceErr, r.db.Close())
	})
	return r.closeErr
}

// Quiesce closes Runtime admission and cancels autonomous Runs without closing persistence.
// Server calls it before draining HTTP so in-flight requests can finish against the same DB.
func (r *AgentRuntime) Quiesce() error {
	r.quiesceOnce.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		errorsDuringQuiesce := []error{r.persistenceErr}
		for sessionID, active := range r.active {
			_, err := r.interruptRunLocked(sessionID, active.runID, serverShutdownReason)
			if err != nil {
				wrapped := fmt.Errorf("persist Run %s/%d interruption: %w", sessionID, active.runID, err)
				errorsDuringQuiesce = append(errorsDuringQuiesce, wrapped)
				r.latchPersistenceFaultLocked(wrapped)
			}
			active.cancel()
		}
		r.quiesceErr = errors.Join(errorsDuringQuiesce...)
	})
	return r.quiesceErr
}

func (r *AgentRuntime) HealthError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persistenceErr
}

func (r *AgentRuntime) latchPersistenceFault(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latchPersistenceFaultLocked(err)
}

func (r *AgentRuntime) latchPersistenceFaultLocked(err error) {
	if err == nil {
		return
	}
	r.persistenceErr = errors.Join(r.persistenceErr, fmt.Errorf("agent persistence unavailable: %w", err))
	for _, active := range r.active {
		active.cancel()
	}
}

func (r *AgentRuntime) persistenceUnavailable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persistenceErr != nil || r.closed
}

func requestCancellationCausedPersistenceError(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil
}

type scanner interface{ Scan(...any) error }

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func (r *AgentRuntime) logOperational(event, sessionID string, runID int64, detail string) {
	if r.logger == nil {
		return
	}
	if detail == "" {
		detail = "unknown"
	}
	r.logger.Printf("event=%s session_id=%s run_id=%d detail=%s", event, sessionID, runID, detail)
}
