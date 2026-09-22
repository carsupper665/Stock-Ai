package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"stock-ai/agent-server/internal/server"

	sqlite "github.com/glebarez/go-sqlite"
)

func (r *AgentRuntime) Handler() http.Handler { return r }

func (r *AgentRuntime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.serveHTTP(w, request)
}

func writeMutationPersistenceError(w http.ResponseWriter, requestCanceled bool) {
	if requestCanceled {
		server.WriteJSONError(w, http.StatusRequestTimeout, "REQUEST_CANCELED", "request was canceled before the mutation committed")
		return
	}
	server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
}

func (r *AgentRuntime) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if !r.authorized(request) {
		server.WriteJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "valid Agent Server admin credentials are required")
		return
	}
	if request.Method != http.MethodGet && r.persistenceUnavailable() {
		server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
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
			server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		}
		return
	}
	if !strings.HasPrefix(path, "/api/v1/sessions/") {
		server.WriteJSONError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
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
			server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "session settings are immutable")
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
			server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
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
			server.WriteJSONError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
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
			server.WriteJSONError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		}
		return
	}
	server.WriteJSONError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
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
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.ModelName = strings.TrimSpace(input.ModelName)
	if input.Name == "" || input.AccountID == "" || input.ModelName == "" {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "name, account_id, and model_name are required")
		return
	}
	maxLoop, maxTools := r.config.DefaultMaxLoop, r.config.DefaultMaxToolCall
	if len(input.MaxLoop) != 0 && (bytes.Equal(bytes.TrimSpace(input.MaxLoop), []byte("null")) || json.Unmarshal(input.MaxLoop, &maxLoop) != nil) {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "max_loop must be a positive integer when provided")
		return
	}
	if len(input.MaxToolCall) != 0 && (bytes.Equal(bytes.TrimSpace(input.MaxToolCall), []byte("null")) || json.Unmarshal(input.MaxToolCall, &maxTools) != nil) {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "max_tool_call must be a positive integer when provided")
		return
	}
	if maxLoop <= 0 || maxTools <= 0 {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "max_loop and max_tool_call must be positive integers")
		return
	}
	prompt := r.config.DefaultPrompt
	if len(input.Prompt) != 0 {
		var parsed string
		if bytes.Equal(bytes.TrimSpace(input.Prompt), []byte("null")) || json.Unmarshal(input.Prompt, &parsed) != nil {
			server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "prompt must be a string when provided")
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
			server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "model_level must be a nonempty supported string when provided")
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
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to create session ID")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.mu.Lock()
	if r.closed || r.persistenceErr != nil {
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
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
			server.WriteJSONError(w, http.StatusConflict, "ACCOUNT_ALREADY_BOUND", "account_id is already bound to a Session")
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
		server.WriteJSONError(w, dependency.status, dependency.code, dependency.msg)
		return
	}
	server.WriteJSONError(w, http.StatusBadGateway, "DEPENDENCY_UNAVAILABLE", "dependency request failed")
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
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "page must be a positive integer")
		return
	}
	includeDeleted := request.URL.Query().Get("include_deleted")
	for key, values := range request.URL.Query() {
		if (key != "page" && key != "include_deleted") || len(values) != 1 {
			server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "only page and include_deleted are accepted once")
			return
		}
	}
	if includeDeleted != "" && includeDeleted != "true" && includeDeleted != "false" {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "include_deleted must be true or false")
		return
	}
	rows, err := r.db.QueryContext(request.Context(), sessionSelect+` WHERE (? OR status != 'deleted') ORDER BY created_at, id LIMIT ? OFFSET ?`, includeDeleted == "true", sessionPageSize, (page-1)*sessionPageSize)
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Sessions")
		return
	}
	defer rows.Close()
	sessions := make([]Session, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Sessions")
			return
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Sessions")
		return
	}
	if err := rows.Close(); err != nil {
		server.WriteJSONError(w, 500, "INTERNAL_ERROR", "unable to list Sessions")
		return
	}
	aggregates := make([]sessionAggregate, 0, len(sessions))
	for _, session := range sessions {
		aggregate, err := r.sessionAggregate(request.Context(), session)
		if err != nil {
			server.WriteJSONError(w, 500, "INTERNAL_ERROR", "unable to aggregate Session list")
			return
		}
		aggregates = append(aggregates, aggregate)
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"sessions": aggregates, "page": page, "page_size": sessionPageSize, "include_deleted": includeDeleted == "true"})
}

func (r *AgentRuntime) getSession(w http.ResponseWriter, request *http.Request, sessionID string) {
	session, err := r.sessionByID(request.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	detail, err := r.sessionAggregate(request.Context(), session)
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to aggregate Session")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, detail)
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
		server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
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
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
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
		server.WriteJSONError(w, http.StatusConflict, "SESSION_DELETED", "deleted Session cannot be started")
		return
	}
	if session.Status != "stopped" {
		_ = tx.Rollback()
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusConflict, "SESSION_ALREADY_RUNNING", "Session is already running")
		return
	}
	if _, exists := r.active[sessionID]; exists {
		_ = tx.Rollback()
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusConflict, "SESSION_RUN_ACTIVE", "previous Run cancellation is still pending")
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
		server.WriteJSONError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.mu.Lock()
	if r.closed || r.persistenceErr != nil {
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
		return
	}
	if _, active := r.active[sessionID]; active {
		r.mu.Unlock()
		server.WriteJSONError(w, http.StatusConflict, "SESSION_RUN_ACTIVE", "a Run is already running")
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
		server.WriteJSONError(w, status, code, message)
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

func (r *AgentRuntime) listRuns(w http.ResponseWriter, request *http.Request, sessionID string) {
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	page, ok := queryPage(request)
	if !ok {
		server.WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "page must be a positive integer")
		return
	}
	rows, err := r.db.QueryContext(request.Context(), runSelect+` WHERE session_id = ? ORDER BY run_id LIMIT ? OFFSET ?`, sessionID, sessionPageSize, (page-1)*sessionPageSize)
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Runs")
		return
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Runs")
			return
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to list Runs")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"runs": runs, "page": page, "page_size": sessionPageSize})
}

func (r *AgentRuntime) currentRun(w http.ResponseWriter, request *http.Request, sessionID string) {
	session, err := r.sessionByID(request.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
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
		server.WriteJSONError(w, http.StatusNotFound, "CURRENT_RUN_NOT_FOUND", message)
		return
	}
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read current Run")
		return
	}
	view, err := currentRunMonitoring(run, session, time.Now())
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read current Run monitoring")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, view)
}

func (r *AgentRuntime) getRun(w http.ResponseWriter, request *http.Request, sessionID, rawRunID string) {
	runID, err := strconv.ParseInt(rawRunID, 10, 64)
	if err != nil || runID <= 0 {
		server.WriteJSONError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	run, err := r.runByID(request.Context(), sessionID, runID)
	if errors.Is(err, sql.ErrNoRows) {
		server.WriteJSONError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	if err != nil {
		server.WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Run")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, run)
}
