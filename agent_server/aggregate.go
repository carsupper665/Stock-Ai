package common

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type sessionAggregate struct {
	Session
	CurrentRun          *Run              `json:"current_run"`
	LastRun             any               `json:"last_run"`
	ActiveEvents        []Event           `json:"active_events"`
	ActiveEventCount    int               `json:"active_event_count"`
	PendingTriggerCount int               `json:"pending_trigger_count"`
	TotalRuns           int               `json:"total_runs"`
	TotalToolCalls      int               `json:"total_tool_calls"`
	MemorySummary       map[string]int    `json:"memory_summary"`
	Usage               usageSummary      `json:"usage"`
	Trading             map[string]any    `json:"trading"`
	RuntimeHealth       map[string]string `json:"runtime_health"`
}

func (r *AgentRuntime) sessionAggregate(ctx context.Context, session Session) (sessionAggregate, error) {
	result := sessionAggregate{Session: session, MemorySummary: map[string]int{"active_count": 0, "expired_count": 0}, RuntimeHealth: map[string]string{"status": "ok"}}
	runs, err := r.monitoringRuns(ctx, session.ID)
	if err != nil {
		return result, err
	}
	usage, err := aggregateUsage(session, runs)
	if err != nil {
		return result, err
	}
	result.Usage = usage.usageSummary
	result.TotalRuns = len(runs)
	for _, run := range runs {
		result.TotalToolCalls += run.ToolCalls
	}
	if len(runs) > 0 {
		last, err := r.runByID(ctx, session.ID, runs[len(runs)-1].RunID)
		if err != nil {
			return result, err
		}
		result.LastRun = struct {
			Run
			Reason *string `json:"reason"`
		}{last, last.Error}
		if last.Status == "running" {
			result.CurrentRun = &last
		}
	}
	result.ActiveEvents, err = sessionEvents(r.db, session.ID)
	if err != nil {
		return result, err
	}
	result.ActiveEventCount = len(result.ActiveEvents)
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_triggers WHERE session_id=?`, session.ID).Scan(&result.PendingTriggerCount); err != nil {
		return result, err
	}
	var active, expired int
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN is_expired=0 THEN 1 END), COUNT(CASE WHEN is_expired=1 THEN 1 END) FROM memories WHERE session_id=?`, session.ID).Scan(&active, &expired)
	if err != nil {
		return result, err
	}
	result.MemorySummary["active_count"], result.MemorySummary["expired_count"] = active, expired
	if r.persistenceUnavailable() {
		result.RuntimeHealth["status"] = "unavailable"
	}
	result.Trading = r.tradingAggregate(ctx, session)
	return result, nil
}

func monitoringFailure(err error) any {
	if err == nil {
		return nil
	}
	var failure *dependencyError
	if errors.As(err, &failure) {
		return map[string]string{"code": failure.code, "message": failure.msg}
	}
	return map[string]string{"code": "BACKEND_UNAVAILABLE", "message": "Backend monitoring is unavailable"}
}

func (r *AgentRuntime) readMonitorBackend(ctx context.Context, token, path string) (map[string]json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.config.BackendURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := r.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &dependencyError{http.StatusBadGateway, "BACKEND_UNAVAILABLE", "Backend monitoring is unavailable"}
	}
	var body map[string]json.RawMessage
	if err := decodeDependencyJSON(response.Body, &body); err != nil || body == nil {
		return nil, &dependencyError{http.StatusBadGateway, "BACKEND_RESPONSE_INVALID", "Backend monitoring response is invalid"}
	}
	return body, nil
}

func (r *AgentRuntime) tradingAggregate(ctx context.Context, session Session) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	result := map[string]any{"scope": "account", "account": nil, "positions": nil, "position_count": nil, "pnl": nil, "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}
	failures := map[string]any{"account": nil, "positions": nil}
	result["errors"] = failures
	token, err := r.fetchAccountToken(ctx, session.AccountID)
	if err != nil {
		failures["account"], failures["positions"] = monitoringFailure(err), monitoringFailure(err)
		return result
	}
	account, accountErr := r.readMonitorBackend(ctx, token, "/v1/account")
	positions, positionErr := r.readMonitorBackend(ctx, token, "/v1/positions")
	failures["account"], failures["positions"] = monitoringFailure(accountErr), monitoringFailure(positionErr)
	if accountErr == nil {
		public := make(map[string]json.RawMessage)
		for _, key := range []string{"id", "user_name", "initial_balance", "balance", "status", "created_at", "locked_margin", "available", "unrealized_pnl", "equity"} {
			if value, ok := account[key]; ok {
				public[key] = value
			}
		}
		result["account"] = public
		var balance, initial, unrealized *float64
		if json.Unmarshal(account["balance"], &balance) == nil && json.Unmarshal(account["initial_balance"], &initial) == nil && json.Unmarshal(account["unrealized_pnl"], &unrealized) == nil && balance != nil && initial != nil && unrealized != nil {
			result["pnl"] = map[string]any{"scope": "account", "net_realized": *balance - *initial, "unrealized": *unrealized, "total": *balance - *initial + *unrealized}
		}
	}
	if positionErr == nil {
		var items []json.RawMessage
		if json.Unmarshal(positions["positions"], &items) != nil || items == nil {
			failures["positions"] = monitoringFailure(&dependencyError{http.StatusBadGateway, "BACKEND_RESPONSE_INVALID", "Backend positions response is invalid"})
		} else {
			result["positions"], result["position_count"] = items, len(items)
		}
	}
	return result
}

func (r *AgentRuntime) serveTradingMonitor(w http.ResponseWriter, request *http.Request, sessionID, resource string) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	query := request.URL.Query()
	for key, values := range query {
		if resource != "ledger" || key != "before_seq" || len(values) != 1 {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "only Ledger before_seq is supported")
			return
		}
	}
	var before int64
	if query.Has("before_seq") {
		var err error
		before, err = strconv.ParseInt(query.Get("before_seq"), 10, 64)
		if err != nil || before < 1 {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "before_seq must be positive")
			return
		}
	}
	session, err := r.sessionByID(request.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), r.config.RequestTimeout)
	defer cancel()
	token, err := r.fetchAccountToken(ctx, session.AccountID)
	if err != nil {
		r.writeDependencyError(w, err)
		return
	}
	path := "/v1/" + resource
	if resource == "ledger" {
		q := url.Values{"limit": {"100"}}
		if before > 0 {
			q.Set("before_seq", strconv.FormatInt(before, 10))
		}
		path += "?" + q.Encode()
	}
	body, err := r.readMonitorBackend(ctx, token, path)
	if err != nil {
		r.writeDependencyError(w, err)
		return
	}
	key := "positions"
	if resource == "ledger" {
		key = "entries"
	}
	var items []json.RawMessage
	if json.Unmarshal(body[key], &items) != nil || items == nil {
		writeHTTPError(w, http.StatusBadGateway, "BACKEND_RESPONSE_INVALID", "Backend monitoring response is invalid")
		return
	}
	result := map[string]any{key: items, "scope": "account", "session_id": sessionID, "account_id": session.AccountID, "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if resource == "ledger" {
		result["page_size"], result["before_seq"] = 100, before
	}
	writeRuntimeJSON(w, http.StatusOK, result)
}
