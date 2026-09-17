package common

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlite "github.com/glebarez/go-sqlite"
)

const (
	sessionPageSize         = 100
	maxRuntimeBody          = 1 << 20
	restartProgressEntries  = 5
	maxModelGenerateRetries = 3
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
	}, eventSchema...), historySchema...), memorySchema...) {
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

func (r *AgentRuntime) Handler() http.Handler { return r }

func (r *AgentRuntime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.serveHTTP(w, request)
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

func writeMutationPersistenceError(w http.ResponseWriter, requestCanceled bool) {
	if requestCanceled {
		writeHTTPError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before the mutation committed")
		return
	}
	writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
}

func (r *AgentRuntime) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if !r.authorized(request) {
		writeHTTPError(w, http.StatusUnauthorized, "UNAUTHORIZED", "valid Agent Server admin credentials are required")
		return
	}
	if request.Method != http.MethodGet && r.persistenceUnavailable() {
		writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	path := strings.TrimSuffix(request.URL.Path, "/")
	if path == "/api/v1/sessions" {
		switch request.Method {
		case http.MethodPost:
			r.createSession(w, request)
		case http.MethodGet:
			r.listSessions(w, request)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		}
		return
	}
	if !strings.HasPrefix(path, "/api/v1/sessions/") {
		writeHTTPError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/sessions/"), "/")
	if len(parts) == 1 {
		switch request.Method {
		case http.MethodGet:
			r.getSession(w, request, parts[0])
		case http.MethodDelete:
			r.deleteSession(w, request, parts[0])
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
			writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "session settings are immutable")
		}
		return
	}
	if len(parts) >= 2 && parts[1] == "events" {
		r.serveEvents(w, request, parts[0], parts[2:])
		return
	}
	if len(parts) >= 2 && parts[1] == "memories" {
		r.serveMemories(w, request, parts[0], parts[2:])
		return
	}
	if len(parts) == 2 && (parts[1] == "ledger" || parts[1] == "positions") {
		r.serveTradingMonitor(w, request, parts[0], parts[1])
		return
	}
	if len(parts) == 2 && parts[1] == "usage" {
		r.serveUsage(w, request, parts[0])
		return
	}
	if len(parts) == 3 && parts[1] == "tools" && parts[2] == "stats" {
		r.serveToolStats(w, request, parts[0])
		return
	}
	if len(parts) == 4 && parts[1] == "runs" && parts[3] == "history" {
		r.serveRunHistory(w, request, parts[0], parts[2])
		return
	}
	if len(parts) == 4 && parts[1] == "runs" && parts[3] == "retry" {
		if request.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		r.retryRun(w, request, parts[0], parts[2])
		return
	}
	if len(parts) == 2 && request.Method == http.MethodPost {
		switch parts[1] {
		case "start":
			r.startSession(w, request, parts[0])
		case "stop":
			r.stopSession(w, request, parts[0])
		default:
			writeHTTPError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		}
		return
	}
	if len(parts) >= 2 && parts[1] == "runs" && request.Method == http.MethodGet {
		switch len(parts) {
		case 2:
			r.listRuns(w, request, parts[0])
		case 3:
			if parts[2] == "current" {
				r.currentRun(w, request, parts[0])
				return
			}
			r.getRun(w, request, parts[0], parts[2])
		default:
			writeHTTPError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		}
		return
	}
	writeHTTPError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
}

func (r *AgentRuntime) authorized(request *http.Request) bool {
	provided := request.Header.Get("Authorization")
	expected := "Bearer " + r.config.AdminToken
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

type createSessionRequest struct {
	Name        string          `json:"name"`
	AccountID   string          `json:"account_id"`
	ModelName   string          `json:"model_name"`
	ModelLevel  json.RawMessage `json:"model_level"`
	Prompt      json.RawMessage `json:"prompt"`
	MaxLoop     json.RawMessage `json:"max_loop"`
	MaxToolCall json.RawMessage `json:"max_tool_call"`
}

func (r *AgentRuntime) createSession(w http.ResponseWriter, request *http.Request) {
	var input createSessionRequest
	if err := decodeRuntimeJSON(request, &input); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.ModelName = strings.TrimSpace(input.ModelName)
	if input.Name == "" || input.AccountID == "" || input.ModelName == "" {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "name, account_id, and model_name are required")
		return
	}
	maxLoop, maxTools := r.config.DefaultMaxLoop, r.config.DefaultMaxToolCall
	if len(input.MaxLoop) != 0 && (bytes.Equal(bytes.TrimSpace(input.MaxLoop), []byte("null")) || json.Unmarshal(input.MaxLoop, &maxLoop) != nil) {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "max_loop must be a positive integer when provided")
		return
	}
	if len(input.MaxToolCall) != 0 && (bytes.Equal(bytes.TrimSpace(input.MaxToolCall), []byte("null")) || json.Unmarshal(input.MaxToolCall, &maxTools) != nil) {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "max_tool_call must be a positive integer when provided")
		return
	}
	if maxLoop <= 0 || maxTools <= 0 {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "max_loop and max_tool_call must be positive integers")
		return
	}
	prompt := r.config.DefaultPrompt
	if len(input.Prompt) != 0 {
		var parsed string
		if bytes.Equal(bytes.TrimSpace(input.Prompt), []byte("null")) || json.Unmarshal(input.Prompt, &parsed) != nil {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "prompt must be a string when provided")
			return
		}
		if parsed != "" {
			prompt = parsed
		}
	}
	var level *string
	if len(input.ModelLevel) != 0 {
		var parsed string
		if bytes.Equal(bytes.TrimSpace(input.ModelLevel), []byte("null")) || json.Unmarshal(input.ModelLevel, &parsed) != nil || parsed == "" {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "model_level must be a nonempty supported string when provided")
			return
		}
		level = &parsed
	}
	modelContext, cancelModel := context.WithTimeout(request.Context(), r.config.RequestTimeout)
	err := r.validateModel(modelContext, input.ModelName, level)
	cancelModel()
	if err != nil {
		r.writeDependencyError(w, err)
		return
	}
	accountContext, cancelAccount := context.WithTimeout(request.Context(), r.config.RequestTimeout)
	_, err = r.fetchAccountToken(accountContext, input.AccountID)
	cancelAccount()
	if err != nil {
		r.writeDependencyError(w, err)
		return
	}
	id, err := newSessionID()
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to create session ID")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.mu.Lock()
	if r.closed || r.persistenceErr != nil {
		r.mu.Unlock()
		writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	_, err = r.db.ExecContext(request.Context(), `INSERT INTO sessions
		(id, name, account_id, model_name, model_level, prompt, max_loop, max_tool_call,
		 status, current_run_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'stopped', 0, ?)`,
		id, input.Name, input.AccountID, input.ModelName, level, prompt, maxLoop, maxTools, now)
	requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
	if err != nil {
		var sqliteError *sqlite.Error
		if !requestCanceled && (!errors.As(err, &sqliteError) || sqliteError.Code() != 2067) {
			r.latchPersistenceFaultLocked(err)
		}
	}
	r.mu.Unlock()
	if err != nil {
		var sqliteError *sqlite.Error
		if errors.As(err, &sqliteError) && sqliteError.Code() == 2067 {
			writeHTTPError(w, http.StatusConflict, "ACCOUNT_ALREADY_BOUND", "account_id is already bound to a Session")
			return
		}
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	session := Session{
		ID: id, Name: input.Name, AccountID: input.AccountID, ModelName: input.ModelName,
		ModelLevel: level, Prompt: prompt, MaxLoop: maxLoop, MaxToolCall: maxTools,
		Status: "stopped", RestartContext: json.RawMessage("null"), CreatedAt: now,
	}
	writeRuntimeJSON(w, http.StatusCreated, session)
}

type dependencyError struct {
	status int
	code   string
	msg    string
}

func (e *dependencyError) Error() string { return e.msg }

func (r *AgentRuntime) writeDependencyError(w http.ResponseWriter, err error) {
	var dependency *dependencyError
	if errors.As(err, &dependency) {
		writeHTTPError(w, dependency.status, dependency.code, dependency.msg)
		return
	}
	writeHTTPError(w, http.StatusBadGateway, "DEPENDENCY_UNAVAILABLE", "dependency request failed")
}

func (r *AgentRuntime) validateModel(ctx context.Context, modelName string, level *string) error {
	_, err := r.resolveModel(ctx, modelName, level)
	return err
}

func (r *AgentRuntime) resolveModel(ctx context.Context, modelName string, level *string) (modelMapping, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.config.GatewayURL, "/")+"/v1/models", nil)
	if err != nil {
		return modelMapping{}, err
	}
	request.Header.Set("Authorization", "Bearer "+r.config.GatewayRuntimeToken)
	response, err := r.httpClient.Do(request)
	if err != nil {
		return modelMapping{}, &dependencyError{http.StatusBadGateway, "GATEWAY_UNAVAILABLE", "Gateway model catalog is unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return modelMapping{}, &dependencyError{http.StatusBadGateway, "GATEWAY_AUTH_FAILED", "Gateway rejected its configured runtime credential"}
		}
		return modelMapping{}, &dependencyError{http.StatusBadGateway, "GATEWAY_UNAVAILABLE", "Gateway model catalog is unavailable"}
	}
	var body struct {
		Models []modelMapping `json:"models"`
	}
	if err := decodeDependencyJSON(response.Body, &body); err != nil {
		return modelMapping{}, &dependencyError{http.StatusBadGateway, "GATEWAY_RESPONSE_INVALID", "Gateway returned an invalid model catalog"}
	}
	for _, model := range body.Models {
		if model.ModelName != modelName {
			continue
		}
		if !model.Enabled || !model.Available {
			return modelMapping{}, &dependencyError{http.StatusConflict, "MODEL_UNAVAILABLE", "model_name is currently unavailable"}
		}
		if level != nil {
			if _, supported := model.Levels[*level]; !supported {
				return modelMapping{}, &dependencyError{http.StatusBadRequest, "INVALID_REQUEST", "model_level is not supported by model_name"}
			}
		}
		return model, nil
	}
	return modelMapping{}, &dependencyError{http.StatusBadRequest, "MODEL_NOT_FOUND", "unknown model_name"}
}

type backendAccount struct {
	ID             string  `json:"id"`
	UserName       string  `json:"user_name"`
	Status         string  `json:"status"`
	Token          string  `json:"token"`
	InitialBalance float64 `json:"initial_balance"`
	Balance        float64 `json:"balance"`
}

func (r *AgentRuntime) fetchAccount(ctx context.Context, accountID string) (backendAccount, error) {
	path := strings.TrimRight(r.config.BackendURL, "/") + "/v1/accounts/" + url.PathEscape(accountID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return backendAccount{}, err
	}
	request.Header.Set("Authorization", "Bearer "+r.config.BackendUserToken)
	response, err := r.httpClient.Do(request)
	if err != nil {
		return backendAccount{}, &dependencyError{http.StatusBadGateway, "BACKEND_UNAVAILABLE", "Backend account lookup is unavailable"}
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return backendAccount{}, &dependencyError{http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Backend Account does not exist"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return backendAccount{}, &dependencyError{http.StatusBadGateway, "BACKEND_AUTH_FAILED", "Backend rejected its configured USER credential"}
	default:
		return backendAccount{}, &dependencyError{http.StatusBadGateway, "BACKEND_UNAVAILABLE", "Backend account lookup is unavailable"}
	}
	var account backendAccount
	if err := decodeDependencyJSON(response.Body, &account); err != nil || account.ID != accountID || account.Token == "" {
		return backendAccount{}, &dependencyError{http.StatusBadGateway, "BACKEND_RESPONSE_INVALID", "Backend returned an invalid Account response"}
	}
	if account.Status != "active" {
		return backendAccount{}, &dependencyError{http.StatusConflict, "ACCOUNT_DISABLED", "Backend Account is not active"}
	}
	return account, nil
}

func (r *AgentRuntime) fetchAccountToken(ctx context.Context, accountID string) (string, error) {
	account, err := r.fetchAccount(ctx, accountID)
	if err != nil {
		return "", err
	}
	return account.Token, nil
}

func (r *AgentRuntime) listSessions(w http.ResponseWriter, request *http.Request) {
	page, ok := queryPage(request, "include_deleted")
	if !ok {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "page must be a positive integer")
		return
	}
	includeDeleted := request.URL.Query().Get("include_deleted")
	for key, values := range request.URL.Query() {
		if (key != "page" && key != "include_deleted") || len(values) != 1 {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "only page and include_deleted are accepted once")
			return
		}
	}
	if includeDeleted != "" && includeDeleted != "true" && includeDeleted != "false" {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "include_deleted must be true or false")
		return
	}
	rows, err := r.db.QueryContext(request.Context(), sessionSelect+` WHERE (? OR status != 'deleted') ORDER BY created_at, id LIMIT ? OFFSET ?`, includeDeleted == "true", sessionPageSize, (page-1)*sessionPageSize)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Sessions")
		return
	}
	defer rows.Close()
	sessions := make([]Session, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Sessions")
			return
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Sessions")
		return
	}
	if err := rows.Close(); err != nil {
		writeHTTPError(w, 500, "INTERNAL_ERROR", "unable to list Sessions")
		return
	}
	aggregates := make([]sessionAggregate, 0, len(sessions))
	for _, session := range sessions {
		aggregate, err := r.sessionAggregate(request.Context(), session)
		if err != nil {
			writeHTTPError(w, 500, "INTERNAL_ERROR", "unable to aggregate Session list")
			return
		}
		aggregates = append(aggregates, aggregate)
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"sessions": aggregates, "page": page, "page_size": sessionPageSize, "include_deleted": includeDeleted == "true"})
}

func (r *AgentRuntime) getSession(w http.ResponseWriter, request *http.Request, sessionID string) {
	session, err := r.sessionByID(request.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	detail, err := r.sessionAggregate(request.Context(), session)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to aggregate Session")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, detail)
}

const sessionSelect = `SELECT id, name, account_id, model_name, model_level, prompt,
	max_loop, max_tool_call, status, current_run_id, restart_context, created_at,
	started_at, stopped_at, deleted_at FROM sessions`

type scanner interface{ Scan(...any) error }

func scanSession(row scanner) (Session, error) {
	var session Session
	var modelLevel, restartContext, startedAt, stoppedAt, deletedAt sql.NullString
	err := row.Scan(&session.ID, &session.Name, &session.AccountID, &session.ModelName, &modelLevel,
		&session.Prompt, &session.MaxLoop, &session.MaxToolCall, &session.Status, &session.CurrentRunID,
		&restartContext, &session.CreatedAt, &startedAt, &stoppedAt, &deletedAt)
	if err != nil {
		return Session{}, err
	}
	if modelLevel.Valid {
		session.ModelLevel = &modelLevel.String
	}
	if restartContext.Valid {
		session.RestartContext = json.RawMessage(restartContext.String)
	} else {
		session.RestartContext = json.RawMessage("null")
	}
	session.StartedAt = nullStringPointer(startedAt)
	session.StoppedAt = nullStringPointer(stoppedAt)
	session.DeletedAt = nullStringPointer(deletedAt)
	return session, nil
}

func (r *AgentRuntime) sessionByID(ctx context.Context, sessionID string) (Session, error) {
	return scanSession(r.db.QueryRowContext(ctx, sessionSelect+` WHERE id = ?`, sessionID))
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func queryPage(request *http.Request, additionalKeys ...string) (int, bool) {
	values := request.URL.Query()
	for key := range values {
		allowed := key == "page"
		for _, additional := range additionalKeys {
			allowed = allowed || key == additional
		}
		if !allowed {
			return 0, false
		}
	}
	raw := values.Get("page")
	if raw == "" {
		return 1, true
	}
	if len(values["page"]) != 1 {
		return 0, false
	}
	page, err := strconv.Atoi(raw)
	maxInt := int(^uint(0) >> 1)
	return page, err == nil && page > 0 && page <= maxInt/sessionPageSize
}

func newSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(value), nil
}

func decodeRuntimeJSON(request *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRuntimeBody+1))
	if err != nil || len(body) > maxRuntimeBody {
		return errors.New("request body must be at most 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request body must contain one valid JSON object with known fields")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func decodeDependencyJSON(reader io.Reader, target any) error {
	body, err := io.ReadAll(io.LimitReader(reader, maxRuntimeBody+1))
	if err != nil || len(body) > maxRuntimeBody {
		return errors.New("response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing data")
	}
	return nil
}

func writeRuntimeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (r *AgentRuntime) startSession(w http.ResponseWriter, request *http.Request, sessionID string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
	session, err := scanSession(tx.QueryRowContext(request.Context(), sessionSelect+` WHERE id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if session.Status == "deleted" {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, http.StatusConflict, "SESSION_DELETED", "deleted Session cannot be started")
		return
	}
	if session.Status != "stopped" {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, http.StatusConflict, "SESSION_ALREADY_RUNNING", "Session is already running")
		return
	}
	if _, exists := r.active[sessionID]; exists {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, http.StatusConflict, "SESSION_RUN_ACTIVE", "previous Run cancellation is still pending")
		return
	}
	runID := session.CurrentRunID + 1
	trigger := "session_start"
	if session.CurrentRunID != 0 {
		trigger = "session_restart"
	}
	if _, err := tx.ExecContext(request.Context(), `UPDATE sessions SET status = 'running', current_run_id = ?, started_at = ?, stopped_at = NULL WHERE id = ?`, runID, now, sessionID); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if _, err := tx.ExecContext(request.Context(), `INSERT INTO runs
		(session_id, run_id, status, trigger, progress_json, model_call_count, tool_call_count, start_time)
		VALUES (?, ?, 'running', ?, '[]', 0, 0, ?)`, sessionID, runID, trigger, now); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if err := tx.Commit(); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	runContext, cancel := context.WithCancel(context.Background())
	active := &activeRun{runID: runID, cancel: cancel, done: make(chan struct{})}
	r.active[sessionID] = active
	r.wg.Add(1)
	r.mu.Unlock()

	go r.runAgent(runContext, session, trigger, runID, active)
	run := Run{
		SessionID: sessionID, RunID: runID, Status: "running", Trigger: trigger,
		Progress: json.RawMessage("[]"), StartedAt: now,
	}
	writeRuntimeJSON(w, http.StatusAccepted, run)
}

func (r *AgentRuntime) retryRun(w http.ResponseWriter, request *http.Request, sessionID, rawRunID string) {
	runID, err := strconv.ParseInt(rawRunID, 10, 64)
	if err != nil || runID <= 0 {
		writeHTTPError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.mu.Lock()
	if r.closed || r.persistenceErr != nil {
		r.mu.Unlock()
		writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	if _, active := r.active[sessionID]; active {
		r.mu.Unlock()
		writeHTTPError(w, http.StatusConflict, "SESSION_RUN_ACTIVE", "a Run is already running")
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
	fail := func(status int, code, message string) {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, status, code, message)
	}
	session, err := scanSession(tx.QueryRowContext(request.Context(), sessionSelect+` WHERE id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		fail(http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
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
	if session.Status != "running" {
		fail(http.StatusConflict, "SESSION_NOT_RUNNING", "only a running Session waiting for work can retry a failed Run")
		return
	}
	failed, err := scanRun(tx.QueryRowContext(request.Context(), runSelect+` WHERE session_id = ? AND run_id = ?`, sessionID, runID))
	if errors.Is(err, sql.ErrNoRows) {
		fail(http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
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
	if failed.Status != "failed" {
		fail(http.StatusConflict, "RUN_NOT_RETRYABLE", "only a failed Run can be retried")
		return
	}
	if session.CurrentRunID != failed.RunID {
		fail(http.StatusConflict, "RUN_NOT_LATEST", "only the latest Run can be retried")
		return
	}
	if failed.RetryAttempt >= maxManualRunRetries {
		fail(http.StatusConflict, "RETRY_LIMIT_REACHED", "manual retry limit reached")
		return
	}
	newRunID := failed.RunID + 1
	retryOf := failed.RunID
	if failed.RetryOfRunID != nil {
		retryOf = *failed.RetryOfRunID
	}
	retryAttempt := failed.RetryAttempt + 1
	if _, err := tx.ExecContext(request.Context(), `INSERT INTO runs
		(session_id, run_id, status, trigger, progress_json, model_call_count, tool_call_count, start_time, retry_of_run_id, retry_attempt)
		VALUES (?, ?, 'running', 'manual_retry', '[]', 0, 0, ?, ?, ?)`, sessionID, newRunID, now, retryOf, retryAttempt); err != nil {
		_ = tx.Rollback()
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if _, err := tx.ExecContext(request.Context(), `UPDATE sessions SET current_run_id = ? WHERE id = ?`, newRunID, sessionID); err != nil {
		_ = tx.Rollback()
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if err := tx.Commit(); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	session.CurrentRunID = newRunID
	runContext, cancel := context.WithCancel(context.Background())
	active := &activeRun{runID: newRunID, cancel: cancel, done: make(chan struct{})}
	r.active[sessionID] = active
	r.wg.Add(1)
	r.mu.Unlock()
	go r.runAgent(runContext, session, "manual_retry", newRunID, active)
	writeRuntimeJSON(w, http.StatusAccepted, Run{
		SessionID: sessionID, RunID: newRunID, Status: "running", Trigger: "manual_retry",
		Progress: json.RawMessage("[]"), StartedAt: now, RetryOfRunID: &retryOf, RetryAttempt: retryAttempt,
	})
}

func (r *AgentRuntime) manualRetryContext(ctx context.Context, sessionID string, runID int64) (map[string]any, error) {
	previous, err := r.runByID(ctx, sessionID, runID-1)
	if err != nil {
		return nil, err
	}
	summary, recent, omitted, err := compactRestartProgress(previous)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"failed_run_id": previous.RunID, "status": previous.Status, "reason": previous.Error,
		"progress_summary": summary, "recent_progress": recent, "progress_omitted": omitted,
		"audit_reference": map[string]any{"session_id": sessionID, "run_id": previous.RunID},
		"safety": []string{
			"This is a new Run; no Provider or Tool call from the failed Run is replayed automatically.",
			"Do not repeat a successful trading mutation. Inspect current account state and Ledger before any new trading mutation.",
			"Treat failed or unknown Tool outcomes as unresolved until current state provides evidence.",
		},
	}, nil
}

func (r *AgentRuntime) runAgent(ctx context.Context, session Session, trigger string, runID int64, active *activeRun) {
	defer r.finishActive(session.ID, active)
	contextBody := map[string]any{
		"trigger": trigger, "session_name": session.Name, "account_id": session.AccountID,
		"model_name": session.ModelName, "model_level": session.ModelLevel,
		"max_loop": session.MaxLoop, "max_tool_call": session.MaxToolCall,
	}
	accountContext, cancelAccount := context.WithTimeout(ctx, r.config.RequestTimeout)
	account, accountErr := r.fetchAccount(accountContext, session.AccountID)
	cancelAccount()
	if ctx.Err() != nil {
		return
	}
	if accountErr != nil {
		contextBody["account_snapshot"] = map[string]any{"ok": false, "error": map[string]string{
			"code": dependencyReasonCode(accountErr), "message": dependencyReason(accountErr),
		}}
	} else {
		contextBody["account_snapshot"] = map[string]any{
			"ok": true, "id": account.ID, "user_name": account.UserName, "status": account.Status,
			"initial_balance": account.InitialBalance, "balance": account.Balance,
		}
	}
	if trigger == "session_restart" && len(session.RestartContext) != 0 && string(session.RestartContext) != "null" {
		restartContext, err := normalizeRestartContext(session.RestartContext)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["restart_context"] = restartContext
	}
	if trigger == "event" {
		triggers, err := r.runTriggers(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["triggers"] = triggers
	}
	if trigger == "server_recovery" {
		recovery, err := r.recoveryContext(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["recovery_context"] = recovery
	}
	if trigger == "manual_retry" {
		retryContext, err := r.manualRetryContext(ctx, session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		contextBody["retry_context"] = retryContext
	}
	var recentSummary *recentRunSummary
	var memoryIndex []memoryMetadata
	var err error
	if trigger == "server_recovery" {
		memoryIndex, err = r.memoryMetadata(ctx, session.ID, "", memoryToolDefault)
	} else {
		recentSummary, memoryIndex, err = r.progressiveRunContext(ctx, session.ID, runID)
	}
	if err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return
	}
	if recentSummary != nil {
		contextBody["recent_run_summary"] = recentSummary
	}
	if len(memoryIndex) != 0 {
		contextBody["memory_index"] = memoryIndex
	}
	contextBody["available_tools"] = runtimeToolNames()
	encodedContext, _ := json.Marshal(contextBody)
	systemPrompt := session.Prompt + toolDisclosureInstruction() + finalizationInstruction
	userPrompt := string(encodedContext)
	messages := []gatewayMessage{{Role: "system", Content: &systemPrompt}, {Role: "user", Content: &userPrompt}}
	progress := make([]traceEntry, 0)
	modelCalls, toolCalls, decisions := 0, 0, 0
	finalizing, finalizationAttempts := false, 0
	continuation := &continuationState{}
	if updated, err := r.persistInitialContext(session.ID, runID, encodedContext); err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return
	} else if !updated {
		return
	}
	persistInstruction := func(instruction string, attempt int) bool {
		progress = append(progress, traceEntry{Type: "instruction", AttemptType: "terminal", Attempt: attempt, Content: &instruction})
		updated, err := r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return false
		}
		return updated
	}
	persistDeniedTool := func(call gatewayTool, result json.RawMessage) bool {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		progress = append(progress, traceEntry{Type: "tool_denied", AttemptType: "terminal", Iteration: decisions,
			ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, Result: result, StartedAt: now, EndedAt: &now})
		updated, err := r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return false
		}
		return updated
	}
	for {
		if ctx.Err() != nil {
			return
		}
		running, err := r.runIsRunning(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return
		}
		if !running {
			return
		}
		if !finalizing && (decisions >= session.MaxLoop || toolCalls >= session.MaxToolCall) {
			finalizing = true
			reason := "max_loop reached"
			if toolCalls >= session.MaxToolCall {
				reason = "max_tool_call reached"
			}
			instruction := terminalFinalizationPrompt(reason)
			messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
			if !persistInstruction(instruction, finalizationAttempts+1) {
				return
			}
		}
		attemptLimit := maxModelGenerateRetries + 1
		iteration := decisions + 1
		toolsEnabled := !finalizing
		if finalizing {
			attemptLimit -= finalizationAttempts
			if attemptLimit <= 0 {
				r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: retry allowance exhausted")
				return
			}
		} else {
			decisions++
			iteration = decisions
		}
		attemptType := "decision"
		if finalizing {
			attemptType = "terminal"
		}
		attemptOffset := 0
		if finalizing {
			attemptOffset = finalizationAttempts
		}
		response, attempts, ok := r.callModel(ctx, session, runID, iteration, attemptType, messages, toolsEnabled,
			attemptLimit, attemptOffset, &progress, &modelCalls, toolCalls, continuation)
		if !ok {
			return
		}
		if finalizing {
			finalizationAttempts += attempts
		}
		if len(response.ToolCalls) == 0 {
			var final finalizationOutput
			var finalizationErr error
			if response.Content == nil || strings.TrimSpace(*response.Content) == "" || response.FinishReason != "stop" {
				finalizationErr = errors.New("final response requires nonempty content and stop finish_reason")
			} else {
				final, finalizationErr = parseFinalization(*response.Content)
			}
			if finalizationErr != nil {
				if !finalizing {
					finalizing = true
					finalizationAttempts = 1
					markLastModelCallTerminal(progress, iteration)
				}
				if finalizationAttempts >= maxModelGenerateRetries+1 {
					r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: "+finalizationErr.Error())
					return
				}
				invalidContent := "[invalid empty final response]"
				if response.Content != nil && strings.TrimSpace(*response.Content) != "" {
					invalidContent = *response.Content
				}
				messages = append(messages, gatewayMessage{Role: "assistant", Content: &invalidContent})
				instruction := terminalFinalizationPrompt("the previous final response was invalid: " + finalizationErr.Error())
				messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
				if !persistInstruction(instruction, finalizationAttempts+1) {
					return
				}
				continue
			}
			completed, err := r.completeFinalizedRun(session.ID, runID, final)
			if err != nil {
				var referenceError *finalizationReferenceError
				if errors.As(err, &referenceError) {
					if !finalizing {
						finalizing = true
						finalizationAttempts = 1
						markLastModelCallTerminal(progress, iteration)
					}
					if finalizationAttempts >= maxModelGenerateRetries+1 {
						r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: "+err.Error())
						return
					}
					messages = append(messages, gatewayMessage{Role: "assistant", Content: response.Content})
					instruction := terminalFinalizationPrompt("the previous memory references were invalid: " + referenceError.Error())
					messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
					if !persistInstruction(instruction, finalizationAttempts+1) {
						return
					}
					continue
				}
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !completed {
				return
			}
			return
		}
		messages = append(messages, gatewayMessage{Role: "assistant", Content: response.Content, ToolCalls: response.ToolCalls})
		if finalizing || response.FinishReason != "tool_call" {
			for _, call := range response.ToolCalls {
				result := toolErrorResult("TOOL_DISABLED", "tools are disabled during finalization", "not_executed")
				content := string(result)
				callID := call.ID
				messages = append(messages, gatewayMessage{Role: "tool", Content: &content, ToolCallID: &callID})
				if !persistDeniedTool(call, result) {
					return
				}
			}
			if !finalizing {
				finalizing = true
				finalizationAttempts = 1
				markLastModelCallTerminal(progress, iteration)
			}
			if finalizationAttempts >= maxModelGenerateRetries+1 {
				r.persistRunFailure(session.ID, runID, "FINALIZATION_INVALID: model returned tool calls during finalization")
				return
			}
			instruction := terminalFinalizationPrompt("tools are unavailable; return the final response now")
			messages = append(messages, gatewayMessage{Role: "user", Content: &instruction})
			if !persistInstruction(instruction, finalizationAttempts+1) {
				return
			}
			continue
		}
		for _, call := range response.ToolCalls {
			if ctx.Err() != nil {
				return
			}
			running, err = r.runIsRunning(session.ID, runID)
			if err != nil {
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !running {
				return
			}
			if toolCalls >= session.MaxToolCall {
				result := toolErrorResult("MAX_TOOL_CALL_REACHED", "tool-call budget is exhausted; finalize from existing evidence", "not_executed")
				content := string(result)
				callID := call.ID
				messages = append(messages, gatewayMessage{Role: "tool", Content: &content, ToolCallID: &callID})
				if !persistDeniedTool(call, result) {
					return
				}
				continue
			}
			toolCalls++
			toolStarted := time.Now()
			progress = append(progress, traceEntry{Type: "tool_call", Iteration: decisions,
				ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments,
				Result:    toolErrorResult("TOOL_INTERRUPTED", "tool result is not yet known", "unknown"),
				StartedAt: toolStarted.UTC().Format(time.RFC3339Nano)})
			updated, err := r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
			if err != nil {
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !updated {
				return
			}
			result := r.executeTool(ctx, session, runID, call)
			progress[len(progress)-1].Result = filterToolResultForHistory(call.Name, result)
			finishTraceTiming(&progress[len(progress)-1], toolStarted, time.Now())
			updated, err = r.updateRunProgress(session.ID, runID, modelCalls, toolCalls, progress)
			if err != nil {
				r.handleRunPersistenceError(session.ID, runID, err)
				return
			}
			if !updated {
				return
			}
			content := string(result)
			callID := call.ID
			messages = append(messages, gatewayMessage{Role: "tool", Content: &content, ToolCallID: &callID})
		}
	}
}

func terminalFinalizationPrompt(reason string) string {
	return "Terminal instruction (" + reason + "): do not call tools. Using only the evidence already in this Run, return the required finalization JSON now. Preserve uncertain tool outcomes as uncertain."
}

func (r *AgentRuntime) callModel(ctx context.Context, session Session, runID int64, iteration int, attemptType string,
	messages []gatewayMessage, toolsEnabled bool, attemptLimit, attemptOffset int, progress *[]traceEntry,
	modelCalls *int, toolCalls int, continuationState *continuationState,
) (gatewayResponse, int, bool) {
	if ctx.Err() != nil {
		return gatewayResponse{}, 0, false
	}
	running, err := r.runIsRunning(session.ID, runID)
	if err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return gatewayResponse{}, 0, false
	}
	if !running {
		return gatewayResponse{}, 0, false
	}
	reserved, err := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress)
	if err != nil {
		r.handleRunPersistenceError(session.ID, runID, err)
		return gatewayResponse{}, 0, false
	}
	if !reserved {
		return gatewayResponse{}, 0, false
	}
	modelContext, cancelModel := context.WithTimeout(ctx, r.config.RequestTimeout)
	model, err := r.resolveModel(modelContext, session.ModelName, session.ModelLevel)
	cancelModel()
	if err != nil {
		if ctx.Err() != nil {
			return gatewayResponse{}, 0, false
		}
		started := time.Now()
		ended := started.UTC().Format(time.RFC3339Nano)
		*progress = append(*progress, traceEntry{Type: "model_resolution", AttemptType: attemptType,
			Iteration: iteration, ModelName: session.ModelName, Error: dependencyReason(err),
			StartedAt: ended, EndedAt: &ended})
		if updated, updateErr := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress); updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
		} else if updated {
			r.persistRunFailure(session.ID, runID, dependencyReason(err))
		}
		return gatewayResponse{}, 0, false
	}
	projection, err := projectModelContext(messages)
	if err != nil {
		*progress = append(*progress, traceEntry{Type: "context_projection", AttemptType: attemptType,
			Iteration: iteration, Error: "CONTEXT_LIMIT_EXCEEDED", ContextOriginalBytes: projection.OriginalBytes,
			ContextProjectedBytes: projection.ProjectedBytes, ContextCompacted: projection.Compacted})
		if updated, updateErr := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress); updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
		} else if updated {
			r.persistRunFailure(session.ID, runID, "CONTEXT_LIMIT_EXCEEDED: mandatory and recent Run context exceeds the Gateway request bound")
		}
		return gatewayResponse{}, 0, false
	}
	for attempt := 1; attempt <= attemptLimit; attempt++ {
		if ctx.Err() != nil {
			return gatewayResponse{}, attempt - 1, false
		}
		running, err := r.runIsRunning(session.ID, runID)
		if err != nil {
			r.handleRunPersistenceError(session.ID, runID, err)
			return gatewayResponse{}, attempt - 1, false
		}
		if !running {
			return gatewayResponse{}, attempt - 1, false
		}
		(*modelCalls)++
		attemptNumber := attemptOffset + attempt
		started := time.Now()
		*progress = append(*progress, traceEntry{
			Type: "model_call", AttemptType: attemptType, Iteration: iteration, Attempt: attemptNumber, ModelName: session.ModelName,
			ProviderID: model.ProviderID, Model: model.Model,
			StartedAt: started.UTC().Format(time.RFC3339Nano), ConversationID: session.ID + "/" + strconv.FormatInt(runID, 10),
			ContextOriginalBytes: projection.OriginalBytes, ContextProjectedBytes: projection.ProjectedBytes,
			ContextCompacted: projection.Compacted,
		})
		updated, updateErr := r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress)
		if updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
			return gatewayResponse{}, attempt, false
		}
		if !updated {
			return gatewayResponse{}, attempt, false
		}
		var continuation *gatewayContinuation
		if attempt == 1 {
			continuation = continuationFor(continuationState, projection.Messages)
		}
		response, requestBytes, dispatched, generateErr := r.generate(ctx, model, session.ModelLevel, session.ID+"/"+strconv.FormatInt(runID, 10), projection.Messages, toolsEnabled, continuation)
		entry := &(*progress)[len(*progress)-1]
		entry.RequestBytes = requestBytes
		if !dispatched {
			(*modelCalls)--
			entry.Type = "generate_dispatch"
			entry.Attempt = 0
		}
		finishTraceTiming(entry, started, time.Now())
		if generateErr != nil {
			entry.Error = generateErr.Error()
			entry.Content = response.Content
			entry.Reasoning = response.Reasoning
			entry.FinishReason = response.FinishReason
			entry.ToolCalls = response.ToolCalls
			if validGatewayDiagnostics(response.NumTurns, response.TotalCostUSD) {
				entry.NumTurns = response.NumTurns
				entry.TotalCostUSD = response.TotalCostUSD
			}
			if validContinuationDiagnostics(response.SessionMode, response.FallbackReason, response.InvocationCount) {
				entry.SessionMode = response.SessionMode
				entry.FallbackReason = response.FallbackReason
				entry.InvocationCount = response.InvocationCount
			}
			if validGatewayUsage(response.Usage) {
				entry.Usage = response.Usage
			}
		} else {
			entry.Content = response.Content
			entry.Reasoning = response.Reasoning
			entry.FinishReason = response.FinishReason
			entry.ToolCalls = response.ToolCalls
			entry.Usage = response.Usage
			entry.NumTurns = response.NumTurns
			entry.TotalCostUSD = response.TotalCostUSD
			entry.SessionMode = response.SessionMode
			entry.FallbackReason = response.FallbackReason
			entry.InvocationCount = response.InvocationCount
		}
		updated, updateErr = r.updateRunProgress(session.ID, runID, *modelCalls, toolCalls, *progress)
		if updateErr != nil {
			r.handleRunPersistenceError(session.ID, runID, updateErr)
			return gatewayResponse{}, attempt, false
		}
		if !updated || ctx.Err() != nil {
			return gatewayResponse{}, attempt, false
		}
		if generateErr == nil {
			acknowledgeContinuation(continuationState, projection.Messages, response)
			if response.FallbackReason != nil {
				r.logOperational("gateway_fallback", session.ID, runID, *response.FallbackReason)
			}
			return response, attempt, true
		}
		if attempt == attemptLimit || !retryableGenerateError(generateErr) {
			r.logOperational("gateway_error", session.ID, runID, compactErrorCode(generateErr.Error()))
			r.persistRunFailure(session.ID, runID, "GATEWAY_ERROR: "+generateErr.Error())
			return gatewayResponse{}, attempt, false
		}
	}
	return gatewayResponse{}, attemptLimit, false
}

func dependencyReason(err error) string {
	var dependency *dependencyError
	if errors.As(err, &dependency) {
		return dependency.code + ": " + dependency.msg
	}
	return "DEPENDENCY_UNAVAILABLE: dependency request failed"
}

type gatewayGenerateError struct {
	message   string
	retryable bool
}

func (err *gatewayGenerateError) Error() string { return err.message }

func retryableGenerateError(err error) bool {
	var generateErr *gatewayGenerateError
	return errors.As(err, &generateErr) && generateErr.retryable
}

func (r *AgentRuntime) generate(ctx context.Context, model modelMapping, level *string, conversationID string, messages []gatewayMessage, toolsEnabled bool, continuation *gatewayContinuation) (gatewayResponse, int, bool, error) {
	options := map[string]any{}
	if level != nil {
		options["model_level"] = *level
	}
	tools := []map[string]any{}
	if toolsEnabled {
		tools = runtimeToolDefinitions()
	}
	body := map[string]any{
		"provider": model.ProviderID, "model": model.Model, "messages": messages,
		"tools": tools, "options": options, "conversation_id": conversationID,
	}
	if continuation != nil {
		body["continuation"] = continuation
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return gatewayResponse{}, 0, false, &gatewayGenerateError{message: "unable to encode Gateway request"}
	}
	if len(encoded) > maxGatewayRequestBytes && continuation != nil {
		delete(body, "continuation")
		encoded, err = json.Marshal(body)
		if err != nil {
			return gatewayResponse{}, 0, false, &gatewayGenerateError{message: "unable to encode Gateway request"}
		}
	}
	if len(encoded) > maxGatewayRequestBytes {
		return gatewayResponse{}, len(encoded), false, &gatewayGenerateError{message: "CONTEXT_LIMIT_EXCEEDED: Gateway request exceeds 1 MiB"}
	}
	callContext, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callContext, http.MethodPost, strings.TrimRight(r.config.GatewayURL, "/")+"/v1/generate", bytes.NewReader(encoded))
	if err != nil {
		return gatewayResponse{}, len(encoded), false, &gatewayGenerateError{message: "unable to create Gateway request"}
	}
	request.Header.Set("Authorization", "Bearer "+r.config.GatewayRuntimeToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.httpClient.Do(request)
	if err != nil {
		if callContext.Err() != nil {
			return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{
				message:   "request canceled or timed out: " + callContext.Err().Error(),
				retryable: ctx.Err() == nil && errors.Is(callContext.Err(), context.DeadlineExceeded),
			}
		}
		return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: "Gateway request failed", retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = decodeDependencyJSON(response.Body, &failure)
		retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		switch failure.Error.Code {
		case "AUTHENTICATION_FAILED", "REQUEST_INVALID", "PROVIDER_INVALID_CONFIG", "HARNESS_NOT_CONFIGURED", "HARNESS_LOGIN_REQUIRED", "HARNESS_VERSION_MISMATCH":
			retryable = false
		case "PROVIDER_UNAVAILABLE", "PROVIDER_TIMEOUT", "RATE_LIMITED":
			retryable = true
		}
		if failure.Error.Code == "" {
			return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: fmt.Sprintf("Gateway returned HTTP %d", response.StatusCode), retryable: retryable}
		}
		return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: fmt.Sprintf("%s: %s", failure.Error.Code, failure.Error.Message), retryable: retryable}
	}
	var result gatewayResponse
	if err := decodeDependencyJSON(response.Body, &result); err != nil {
		return gatewayResponse{}, len(encoded), true, &gatewayGenerateError{message: "Gateway returned an invalid response", retryable: true}
	}
	if result.FinishReason == "" || !validGatewayUsage(result.Usage) || !validGatewayDiagnostics(result.NumTurns, result.TotalCostUSD) ||
		!validContinuationDiagnostics(result.SessionMode, result.FallbackReason, result.InvocationCount) {
		return result, len(encoded), true, &gatewayGenerateError{message: "Gateway returned an invalid response", retryable: true}
	}
	return result, len(encoded), true, nil
}

func validGatewayDiagnostics(numTurns *int, totalCostUSD *float64) bool {
	return (numTurns == nil || *numTurns >= 0) &&
		(totalCostUSD == nil || (*totalCostUSD >= 0 && !math.IsNaN(*totalCostUSD) && !math.IsInf(*totalCostUSD, 0)))
}

func validContinuationDiagnostics(sessionMode, fallbackReason *string, invocationCount *int) bool {
	return validDiagnosticLabel(sessionMode) && validDiagnosticLabel(fallbackReason) &&
		(invocationCount == nil || *invocationCount >= 1)
}

func validDiagnosticLabel(value *string) bool {
	if value == nil {
		return true
	}
	if len(*value) == 0 || len(*value) > 64 {
		return false
	}
	for _, char := range *value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func markLastModelCallTerminal(progress []traceEntry, iteration int) {
	for index := len(progress) - 1; index >= 0; index-- {
		if progress[index].Type != "model_call" {
			continue
		}
		progress[index].AttemptType = "terminal"
		progress[index].Attempt = 1
		progress[index].Iteration = iteration
		progress[index].CountsAsDecision = true
		return
	}
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

func runtimeToolDefinitions() []map[string]any {
	tools := []map[string]any{
		{
			"name": "get_market_snapshot", "description": "Get the latest market price snapshot.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market": map[string]any{"type": "string"}, "symbol": map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "Exchange symbol with no separator, uppercase, e.g. BTCUSDT or ETHUSDT. Never BTC/USDT or BTC-USDT."},
				}, "required": []string{"symbol"}, "additionalProperties": false,
			},
		},
		{
			"name": "get_ohlcv", "description": "Get completed OHLCV candles in an ascending UTC time range.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market":     map[string]any{"type": "string", "default": "crypto"},
					"symbol":     map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "Exchange symbol with no separator, uppercase, e.g. BTCUSDT or ETHUSDT. Never BTC/USDT or BTC-USDT."},
					"interval":   map[string]any{"type": "string", "enum": []string{"1m", "5m", "15m", "1h", "4h", "1d"}},
					"start_time": map[string]any{"type": "string", "format": "date-time"},
					"end_time":   map[string]any{"type": "string", "format": "date-time"},
					"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100},
				}, "required": []string{"symbol", "interval", "start_time", "end_time"}, "additionalProperties": false,
			},
		},
		{
			"name": "get_market_info", "description": "Get source market rules and their relationship to virtual Backend execution.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market": map[string]any{"type": "string", "default": "crypto"}, "symbol": map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "Exchange symbol with no separator, uppercase, e.g. BTCUSDT or ETHUSDT. Never BTC/USDT or BTC-USDT."},
				}, "required": []string{"symbol"}, "additionalProperties": false,
			},
		},
	}
	tools = append(tools, eventToolSchemas()...)
	tools = append(tools, tradingToolDefinitions()...)
	tools = append(tools, messageToolDefinitions()...)
	tools = append(tools, memoryToolDefinitions()...)
	return tools
}

func (r *AgentRuntime) executeTool(ctx context.Context, session Session, runID int64, call gatewayTool) json.RawMessage {
	switch call.Name {
	case "create_event", "delete_event", "list_events":
		return r.executeEventTool(session, runID, call, time.Now())
	case "get_market_snapshot":
		query, err := snapshotToolQuery(call.Arguments)
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.backendToolRequest(ctx, session, http.MethodGet, "/v1/market/price", query, nil)
	case "get_ohlcv":
		query, err := ohlcvToolQuery(call.Arguments)
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.backendToolRequest(ctx, session, http.MethodGet, "/v1/market/ohlcv", query, nil)
	case "get_market_info":
		query, err := marketInfoToolQuery(call.Arguments)
		if err != nil {
			return toolErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.backendToolRequest(ctx, session, http.MethodGet, "/v1/market/info", query, nil)
	default:
		if result, handled := r.executeTradingTool(ctx, session, runID, call); handled {
			return result
		}
		if result, handled := r.executeMessageTool(ctx, session, call); handled {
			return result
		}
		if result, handled := r.executeMemoryTool(ctx, session.ID, call); handled {
			return result
		}
		return toolErrorResult("UNKNOWN_TOOL", "tool is not registered", "not_executed")
	}
}

type marketSymbolArguments struct {
	Market json.RawMessage `json:"market"`
	Symbol json.RawMessage `json:"symbol"`
}

func snapshotToolQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Market json.RawMessage `json:"market"`
		Symbol json.RawMessage `json:"symbol"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("symbol is required and only market and symbol are accepted")
	}
	market, symbol, err := parseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	return url.Values{"market": {market}, "symbol": {symbol}}, nil
}

func ohlcvToolQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments struct {
		Market    json.RawMessage `json:"market"`
		Symbol    json.RawMessage `json:"symbol"`
		Interval  json.RawMessage `json:"interval"`
		StartTime json.RawMessage `json:"start_time"`
		EndTime   json.RawMessage `json:"end_time"`
		Limit     json.RawMessage `json:"limit"`
	}
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("only market, symbol, interval, start_time, end_time, and limit are accepted")
	}
	market, symbol, err := parseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	interval, err := requiredToolString(rawArguments.Interval, "interval")
	if err != nil || !validOHLCVInterval(interval) {
		return nil, errors.New("interval must be one of 1m, 5m, 15m, 1h, 4h, or 1d")
	}
	startValue, err := requiredToolString(rawArguments.StartTime, "start_time")
	if err != nil {
		return nil, err
	}
	endValue, err := requiredToolString(rawArguments.EndTime, "end_time")
	if err != nil {
		return nil, err
	}
	start, startErr := time.Parse(time.RFC3339, startValue)
	end, endErr := time.Parse(time.RFC3339, endValue)
	if startErr != nil || endErr != nil || !start.Before(end) {
		return nil, errors.New("start_time and end_time must be RFC3339 timestamps with start_time before end_time")
	}
	limit := 100
	if len(rawArguments.Limit) != 0 {
		if bytes.Equal(bytes.TrimSpace(rawArguments.Limit), []byte("null")) || json.Unmarshal(rawArguments.Limit, &limit) != nil || limit < 1 || limit > 500 {
			return nil, errors.New("limit must be an integer from 1 through 500")
		}
	}
	return url.Values{
		"market": {market}, "symbol": {symbol}, "interval": {interval},
		"start_time": {startValue}, "end_time": {endValue},
		"limit": {strconv.Itoa(limit)},
	}, nil
}

func marketInfoToolQuery(arguments json.RawMessage) (url.Values, error) {
	var rawArguments marketSymbolArguments
	if !decodeToolArguments(arguments, &rawArguments) {
		return nil, errors.New("symbol is required and only market and symbol are accepted")
	}
	market, symbol, err := parseMarketSymbol(rawArguments.Market, rawArguments.Symbol)
	if err != nil {
		return nil, err
	}
	return url.Values{"market": {market}, "symbol": {symbol}}, nil
}

func decodeToolArguments(arguments json.RawMessage, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func parseMarketSymbol(rawMarket, rawSymbol json.RawMessage) (string, string, error) {
	symbol, err := requiredToolString(rawSymbol, "symbol")
	if err != nil {
		return "", "", err
	}
	market := "crypto"
	if len(rawMarket) != 0 {
		if bytes.Equal(bytes.TrimSpace(rawMarket), []byte("null")) || json.Unmarshal(rawMarket, &market) != nil {
			return "", "", errors.New("market must be a string")
		}
		market = strings.TrimSpace(market)
		if market == "" {
			market = "crypto"
		}
	}
	return market, strings.TrimSpace(symbol), nil
}

func requiredToolString(raw json.RawMessage, name string) (string, error) {
	var value string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a nonempty string", name)
	}
	return strings.TrimSpace(value), nil
}

func validOHLCVInterval(interval string) bool {
	switch interval {
	case "1m", "5m", "15m", "1h", "4h", "1d":
		return true
	default:
		return false
	}
}

func dependencyReasonCode(err error) string {
	var dependency *dependencyError
	if errors.As(err, &dependency) {
		return dependency.code
	}
	return "DEPENDENCY_UNAVAILABLE"
}

func toolErrorResult(code, message, outcome string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]string{
		"code": code, "message": message, "outcome": outcome,
	}})
	return encoded
}

func (r *AgentRuntime) runIsRunning(sessionID string, runID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var status string
	err := r.db.QueryRow(`SELECT status FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "running", nil
}

func (r *AgentRuntime) updateRunProgress(sessionID string, runID int64, modelCalls, toolCalls int, progress []traceEntry) (bool, error) {
	encoded, _ := json.Marshal(progress)
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.db.Exec(`UPDATE runs SET model_call_count = ?, tool_call_count = ?, progress_json = ?
		WHERE session_id = ? AND run_id = ? AND status = 'running'`, modelCalls, toolCalls, string(encoded), sessionID, runID)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return updated == 1, nil
}

func (r *AgentRuntime) persistInitialContext(sessionID string, runID int64, context json.RawMessage) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.db.Exec(`UPDATE runs SET initial_context_json = ?
		WHERE session_id = ? AND run_id = ? AND status = 'running' AND initial_context_json IS NULL`, string(context), sessionID, runID)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

func (r *AgentRuntime) failRun(sessionID string, runID int64, reason string) (bool, error) {
	return r.persistTerminalRun(sessionID, runID, "failed", nil, nil, &reason)
}

func (r *AgentRuntime) persistRunFailure(sessionID string, runID int64, reason string) {
	_, err := r.failRun(sessionID, runID, reason)
	if err != nil {
		r.latchPersistenceFault(fmt.Errorf("persist failed Run %s/%d: %w", sessionID, runID, err))
	}
}

func (r *AgentRuntime) handleRunPersistenceError(sessionID string, runID int64, operationErr error) {
	_, terminalErr := r.failRun(sessionID, runID, "PERSISTENCE_ERROR: unable to persist Run progress")
	if terminalErr != nil {
		r.latchPersistenceFault(errors.Join(
			fmt.Errorf("persist Run %s/%d progress: %w", sessionID, runID, operationErr),
			fmt.Errorf("persist Run %s/%d failed state: %w", sessionID, runID, terminalErr),
		))
	}
}

func (r *AgentRuntime) finishActive(sessionID string, active *activeRun) {
	r.mu.Lock()
	if r.active[sessionID] == active {
		delete(r.active, sessionID)
	}
	close(active.done)
	r.dispatchPendingLocked(sessionID)
	r.mu.Unlock()
	r.wg.Done()
}

func (r *AgentRuntime) stopSession(w http.ResponseWriter, request *http.Request, sessionID string) {
	r.changeSessionState(w, request, sessionID, false)
}

func (r *AgentRuntime) deleteSession(w http.ResponseWriter, request *http.Request, sessionID string) {
	r.changeSessionState(w, request, sessionID, true)
}

func (r *AgentRuntime) changeSessionState(w http.ResponseWriter, request *http.Request, sessionID string, deleting bool) {
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
	session, err := scanSession(tx.QueryRowContext(request.Context(), sessionSelect+` WHERE id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if session.Status == "deleted" && !deleting {
		_ = tx.Rollback()
		r.mu.Unlock()
		writeHTTPError(w, http.StatusConflict, "SESSION_DELETED", "deleted Session cannot be stopped")
		return
	}
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	targetStatus := "stopped"
	clearOutcome := "cleared_by_stop"
	if deleting {
		targetStatus = "deleted"
		clearOutcome = "cleared_by_delete"
	}
	clearance, err := r.clearSessionEventsTx(tx, sessionID, nowTime, clearOutcome)
	if err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	// A repeated stop on an already stopped Session recomputes nothing: the first stop's
	// cleared_events / pending_triggers exist only in the saved restart_context.
	var restartContext any
	if len(session.RestartContext) != 0 && string(session.RestartContext) != "null" {
		restartContext = string(session.RestartContext)
	}
	if session.Status == "running" {
		restartContext, err = restartContextFor(tx, sessionID, session.CurrentRunID, true, clearance)
		if err != nil {
			requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
			if !requestCanceled {
				r.latchPersistenceFaultLocked(err)
			}
			_ = tx.Rollback()
			r.mu.Unlock()
			writeMutationPersistenceError(w, requestCanceled)
			return
		}
	}
	if session.Status == "running" && session.CurrentRunID != 0 {
		if _, err := tx.ExecContext(request.Context(), `UPDATE runs SET status = 'interrupted', summary = NULL,
			output = NULL, error = 'HARD_STOP: interrupted by user', end_time = ? WHERE session_id = ? AND run_id = ? AND status = 'running'`,
			now, sessionID, session.CurrentRunID); err != nil {
			requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
			if !requestCanceled {
				r.latchPersistenceFaultLocked(err)
			}
			_ = tx.Rollback()
			r.mu.Unlock()
			writeMutationPersistenceError(w, requestCanceled)
			return
		}
	}
	deletedAt := any(nil)
	if deleting {
		deletedAt = now
	}
	if _, err := tx.ExecContext(request.Context(), `UPDATE sessions SET status = ?, stopped_at = ?, deleted_at = ?, restart_context = ? WHERE id = ?`,
		targetStatus, now, deletedAt, restartContext, sessionID); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	var historyStage *historyStage
	if session.Status == "running" && session.CurrentRunID != 0 {
		historyStage, err = r.stageHistoryRunIDsTx(tx, sessionID, clearanceHistoryRunIDs(session.CurrentRunID, clearance)...)
		if err != nil {
			requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
			if !requestCanceled {
				r.latchPersistenceFaultLocked(err)
			}
			_ = tx.Rollback()
			r.mu.Unlock()
			writeMutationPersistenceError(w, requestCanceled)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		requestCanceled := requestCancellationCausedPersistenceError(request.Context(), err)
		if !requestCanceled {
			r.latchPersistenceFaultLocked(err)
		}
		_ = tx.Rollback()
		if historyStage != nil {
			if restoreErr := historyStage.finish(false); restoreErr != nil {
				r.latchPersistenceFaultLocked(restoreErr)
			}
		}
		r.mu.Unlock()
		writeMutationPersistenceError(w, requestCanceled)
		return
	}
	if historyStage != nil {
		if err := historyStage.finish(true); err != nil {
			r.latchPersistenceFaultLocked(err)
			r.mu.Unlock()
			writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
			return
		}
	}
	if session.Status == "running" && session.CurrentRunID != 0 {
		r.logOperational("run_terminal", sessionID, session.CurrentRunID, "interrupted.HARD_STOP")
	}
	active := r.active[sessionID]
	if active != nil {
		active.cancel()
	}
	session.Status = targetStatus
	session.StoppedAt = &now
	session.DeletedAt = nil
	if deleting {
		session.DeletedAt = &now
	}
	session.RestartContext = json.RawMessage("null")
	if encoded, ok := restartContext.(string); ok {
		session.RestartContext = json.RawMessage(encoded)
	}
	r.mu.Unlock()

	pending := false
	if active != nil {
		timer := time.NewTimer(r.config.StopTimeout)
		select {
		case <-active.done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			pending = true
		}
	}
	status := http.StatusOK
	if pending {
		status = http.StatusAccepted
	}
	writeRuntimeJSON(w, status, map[string]any{"session": session, "cancellation_pending": pending})
}

func restartContextFor(tx *sql.Tx, sessionID string, runID int64, interrupting bool, clearance sessionEventClearance) (any, error) {
	if runID == 0 {
		return nil, nil
	}
	run, err := scanRun(tx.QueryRow(runSelect+` WHERE session_id = ? AND run_id = ?`, sessionID, runID))
	if err != nil {
		return nil, err
	}
	status, reason := run.Status, run.Error
	if interrupting && run.Status == "running" {
		status = "interrupted"
		value := "HARD_STOP: interrupted by user"
		reason = &value
	}
	if status == "completed" && len(clearance.Events) == 0 && len(clearance.Pending) == 0 {
		return nil, nil
	}
	progressSummary, recentProgress, omitted, err := compactRestartProgress(run)
	if err != nil {
		return nil, err
	}
	context := map[string]any{
		"previous_run_id": run.RunID, "status": status, "reason": reason,
		"summary": run.Summary, "output": run.Output, "progress_summary": progressSummary,
		"recent_progress": recentProgress, "progress_omitted": omitted,
		"triggers": run.Triggers, "cleared_events": clearance.Events, "pending_triggers": clearance.Pending,
	}
	if status == "completed" {
		delete(context, "reason")
		delete(context, "summary")
		delete(context, "output")
		delete(context, "progress_summary")
		delete(context, "recent_progress")
		delete(context, "progress_omitted")
		delete(context, "triggers")
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

type restartProgressEntry struct {
	Type         string `json:"type"`
	Iteration    int    `json:"iteration,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Status       string `json:"status"`
}

func compactRestartProgress(run Run) (map[string]int, []restartProgressEntry, int, error) {
	var progress []traceEntry
	if err := json.Unmarshal(run.Progress, &progress); err != nil {
		return nil, nil, 0, err
	}
	start := len(progress) - restartProgressEntries
	if start < 0 {
		start = 0
	}
	recent := make([]restartProgressEntry, 0, len(progress)-start)
	for _, entry := range progress[start:] {
		status := "completed"
		errorCode := ""
		if entry.Error != "" {
			status = "error"
			errorCode = compactErrorCode(entry.Error)
		} else if entry.Type == "tool_call" {
			var result struct {
				OK    bool `json:"ok"`
				Error struct {
					Code    string `json:"code"`
					Outcome string `json:"outcome"`
				} `json:"error"`
			}
			if json.Unmarshal(entry.Result, &result) != nil {
				status = "unknown"
			} else if result.OK {
				status = "ok"
			} else if result.Error.Outcome != "" {
				status = result.Error.Outcome
				errorCode = result.Error.Code
			} else if entry.EndedAt == nil {
				status = "in_progress"
			} else {
				status = "error"
			}
		} else if entry.EndedAt == nil {
			status = "in_progress"
		}
		recent = append(recent, restartProgressEntry{
			Type: entry.Type, Iteration: entry.Iteration, ToolName: entry.ToolName,
			FinishReason: entry.FinishReason, ErrorCode: errorCode, Status: status,
		})
	}
	return map[string]int{
		"model_call_count": run.ModelCalls,
		"tool_call_count":  run.ToolCalls,
		"trace_count":      len(progress),
	}, recent, start, nil
}

func compactErrorCode(message string) string {
	code := strings.TrimSpace(strings.SplitN(message, ":", 2)[0])
	if code == "" {
		return ""
	}
	for index, char := range code {
		if index == 0 && (char < 'A' || char > 'Z') {
			return ""
		}
		if index > 0 && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return ""
		}
	}
	return code
}

func normalizeRestartContext(raw json.RawMessage) (json.RawMessage, error) {
	var context map[string]json.RawMessage
	if err := json.Unmarshal(raw, &context); err != nil {
		return nil, err
	}
	progress, legacy := context["progress"]
	if !legacy {
		return raw, nil
	}
	run := Run{Progress: progress}
	_ = json.Unmarshal(context["model_call_count"], &run.ModelCalls)
	_ = json.Unmarshal(context["tool_call_count"], &run.ToolCalls)
	modelCountMissing, toolCountMissing := run.ModelCalls == 0, run.ToolCalls == 0
	if modelCountMissing || toolCountMissing {
		var entries []traceEntry
		if err := json.Unmarshal(progress, &entries); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type == "model_call" && modelCountMissing {
				run.ModelCalls++
			}
			if entry.Type == "tool_call" && toolCountMissing {
				run.ToolCalls++
			}
		}
	}
	summary, recent, omitted, err := compactRestartProgress(run)
	if err != nil {
		return nil, err
	}
	for key, value := range map[string]any{
		"progress_summary": summary, "recent_progress": recent, "progress_omitted": omitted,
	} {
		context[key], _ = json.Marshal(value)
	}
	delete(context, "progress")
	return json.Marshal(context)
}

func deriveDecisionCount(progress json.RawMessage, modelCalls int) int {
	var entries []traceEntry
	if json.Unmarshal(progress, &entries) != nil {
		return modelCalls
	}
	iterations := make(map[int]bool)
	decisions, tracedCalls := 0, 0
	for _, entry := range entries {
		if entry.Type != "model_call" {
			continue
		}
		tracedCalls++
		if entry.AttemptType == "terminal" && !entry.CountsAsDecision {
			continue
		}
		if entry.Iteration > 0 {
			iterations[entry.Iteration] = true
		} else {
			decisions++
		}
	}
	decisions += len(iterations)
	if modelCalls > tracedCalls {
		decisions += modelCalls - tracedCalls
	}
	return decisions
}

func (r *AgentRuntime) interruptRunLocked(sessionID string, runID int64, reason string) (bool, error) {
	return r.persistTerminalRunLocked(sessionID, runID, "interrupted", nil, nil, &reason)
}

const runSelect = `SELECT session_id, run_id, status, trigger, triggers_json, summary, output, error,
	progress_json, initial_context_json, model_call_count, tool_call_count, start_time, end_time, retry_of_run_id, retry_attempt FROM runs`

func scanRun(row scanner) (Run, error) {
	var run Run
	var triggers, summary, output, runError, initialContext, endedAt sql.NullString
	var retryOf sql.NullInt64
	var progress string
	err := row.Scan(&run.SessionID, &run.RunID, &run.Status, &run.Trigger, &triggers, &summary, &output,
		&runError, &progress, &initialContext, &run.ModelCalls, &run.ToolCalls, &run.StartedAt, &endedAt, &retryOf, &run.RetryAttempt)
	if err != nil {
		return Run{}, err
	}
	run.Triggers = json.RawMessage("null")
	if triggers.Valid {
		run.Triggers = json.RawMessage(triggers.String)
	}
	run.Summary = nullStringPointer(summary)
	run.Output = nullStringPointer(output)
	run.Error = nullStringPointer(runError)
	run.EndedAt = nullStringPointer(endedAt)
	if retryOf.Valid {
		run.RetryOfRunID = &retryOf.Int64
	}
	run.Progress = json.RawMessage(progress)
	run.InitialContext = json.RawMessage("null")
	if initialContext.Valid {
		run.InitialContext = json.RawMessage(initialContext.String)
	}
	run.DecisionCount = deriveDecisionCount(run.Progress, run.ModelCalls)
	return run, nil
}

func (r *AgentRuntime) runByID(ctx context.Context, sessionID string, runID int64) (Run, error) {
	return scanRun(r.db.QueryRowContext(ctx, runSelect+` WHERE session_id = ? AND run_id = ?`, sessionID, runID))
}

func (r *AgentRuntime) listRuns(w http.ResponseWriter, request *http.Request, sessionID string) {
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	page, ok := queryPage(request)
	if !ok {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "page must be a positive integer")
		return
	}
	rows, err := r.db.QueryContext(request.Context(), runSelect+` WHERE session_id = ? ORDER BY run_id LIMIT ? OFFSET ?`, sessionID, sessionPageSize, (page-1)*sessionPageSize)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Runs")
		return
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Runs")
			return
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Runs")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"runs": runs, "page": page, "page_size": sessionPageSize})
}

func (r *AgentRuntime) currentRun(w http.ResponseWriter, request *http.Request, sessionID string) {
	session, err := r.sessionByID(request.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	run, err := scanRun(r.db.QueryRowContext(request.Context(), runSelect+` WHERE session_id = ? AND status = 'running' ORDER BY run_id DESC LIMIT 1`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		message := "Session is waiting for an Event and has no running Run"
		if session.Status == "stopped" {
			message = "Stopped Session has no running Run"
		} else if session.Status == "deleted" {
			message = "Deleted Session has no running Run"
		}
		writeHTTPError(w, http.StatusNotFound, "CURRENT_RUN_NOT_FOUND", message)
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read current Run")
		return
	}
	view, err := currentRunMonitoring(run, session, time.Now())
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read current Run monitoring")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, view)
}

func (r *AgentRuntime) getRun(w http.ResponseWriter, request *http.Request, sessionID, rawRunID string) {
	runID, err := strconv.ParseInt(rawRunID, 10, 64)
	if err != nil || runID <= 0 {
		writeHTTPError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	run, err := r.runByID(request.Context(), sessionID, runID)
	if errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Run")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, run)
}
