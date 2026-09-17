package common

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"time"
)

const unknownToolStatsName = "_unknown"

type usageUnknownAttempts struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CachedTokens int `json:"cached_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type usageSummary struct {
	InputTokens     *int64               `json:"input_tokens"`
	OutputTokens    *int64               `json:"output_tokens"`
	CachedTokens    *int64               `json:"cached_tokens"`
	TotalTokens     *int64               `json:"total_tokens"`
	ModelCalls      int                  `json:"model_calls"`
	UnknownAttempts usageUnknownAttempts `json:"unknown_attempts"`
}

type runUsageSummary struct {
	RunID  int64  `json:"run_id"`
	Status string `json:"status"`
	usageSummary
}

type modelUsageSummary struct {
	ModelName  string  `json:"model_name"`
	ProviderID *string `json:"provider_id"`
	Model      *string `json:"model"`
	usageSummary
}

type sessionUsageResponse struct {
	SessionID string `json:"session_id"`
	usageSummary
	ByModel []modelUsageSummary `json:"by_model"`
	ByRun   []runUsageSummary   `json:"by_run"`
}

type monitoringRun struct {
	RunID      int64
	Status     string
	Progress   []traceEntry
	ModelCalls int
	ToolCalls  int
}

type modelUsageKey struct {
	modelName  string
	providerID string
	model      string
}

func (r *AgentRuntime) serveUsage(w http.ResponseWriter, request *http.Request, sessionID string) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if request.URL.RawQuery != "" {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Usage does not accept query parameters")
		return
	}
	session, err := r.sessionByID(request.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session Usage")
		return
	}
	runs, err := r.monitoringRuns(request.Context(), sessionID)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Session Usage")
		return
	}
	response, err := aggregateUsage(session, runs)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to aggregate Session Usage")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, response)
}

func (r *AgentRuntime) monitoringRuns(ctx context.Context, sessionID string) ([]monitoringRun, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT run_id, status, progress_json, model_call_count, tool_call_count
		FROM runs WHERE session_id = ? ORDER BY run_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]monitoringRun, 0)
	for rows.Next() {
		var run monitoringRun
		var progress string
		if err := rows.Scan(&run.RunID, &run.Status, &progress, &run.ModelCalls, &run.ToolCalls); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(progress), &run.Progress); err != nil || run.Progress == nil || run.ModelCalls < 0 || run.ToolCalls < 0 {
			return nil, errors.New("invalid persisted Run monitoring data")
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func aggregateUsage(session Session, runs []monitoringRun) (sessionUsageResponse, error) {
	response := sessionUsageResponse{SessionID: session.ID, ByModel: make([]modelUsageSummary, 0), ByRun: make([]runUsageSummary, 0, len(runs))}
	byModel := make(map[modelUsageKey]*usageSummary)
	for _, run := range runs {
		runSummary := runUsageSummary{RunID: run.RunID, Status: run.Status}
		tracedCalls := 0
		for _, entry := range run.Progress {
			if entry.Type != "model_call" {
				continue
			}
			tracedCalls++
			key := modelKey(entry, session.ModelName)
			modelSummary := byModel[key]
			if modelSummary == nil {
				modelSummary = &usageSummary{}
				byModel[key] = modelSummary
			}
			if err := addUsageAttempt(&response.usageSummary, entry.Usage); err != nil {
				return sessionUsageResponse{}, err
			}
			if err := addUsageAttempt(&runSummary.usageSummary, entry.Usage); err != nil {
				return sessionUsageResponse{}, err
			}
			if err := addUsageAttempt(modelSummary, entry.Usage); err != nil {
				return sessionUsageResponse{}, err
			}
		}
		if tracedCalls > run.ModelCalls {
			return sessionUsageResponse{}, errors.New("Run model trace exceeds persisted model-call count")
		}
		for missing := tracedCalls; missing < run.ModelCalls; missing++ {
			key := modelUsageKey{modelName: session.ModelName}
			modelSummary := byModel[key]
			if modelSummary == nil {
				modelSummary = &usageSummary{}
				byModel[key] = modelSummary
			}
			_ = addUsageAttempt(&response.usageSummary, nil)
			_ = addUsageAttempt(&runSummary.usageSummary, nil)
			_ = addUsageAttempt(modelSummary, nil)
		}
		response.ByRun = append(response.ByRun, runSummary)
	}
	keys := make([]modelUsageKey, 0, len(byModel))
	for key := range byModel {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].modelName != keys[j].modelName {
			return keys[i].modelName < keys[j].modelName
		}
		if (keys[i].providerID == "") != (keys[j].providerID == "") {
			return keys[i].providerID != ""
		}
		if keys[i].providerID != keys[j].providerID {
			return keys[i].providerID < keys[j].providerID
		}
		return keys[i].model < keys[j].model
	})
	for _, key := range keys {
		response.ByModel = append(response.ByModel, modelUsageSummary{
			ModelName: key.modelName, ProviderID: nullablePointer(key.providerID), Model: nullablePointer(key.model),
			usageSummary: *byModel[key],
		})
	}
	return response, nil
}

func modelKey(entry traceEntry, fallback string) modelUsageKey {
	name := entry.ModelName
	if name == "" {
		name = fallback
	}
	return modelUsageKey{modelName: name, providerID: entry.ProviderID, model: entry.Model}
}

func nullablePointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func addUsageAttempt(summary *usageSummary, usage *gatewayUsage) error {
	summary.ModelCalls++
	var input, output, cached, total *int64
	if usage != nil {
		input, output, cached, total = usage.InputTokens, usage.OutputTokens, usage.CachedTokens, usage.TotalTokens
	}
	if err := addUsageMetric(&summary.InputTokens, input, &summary.UnknownAttempts.InputTokens); err != nil {
		return err
	}
	if err := addUsageMetric(&summary.OutputTokens, output, &summary.UnknownAttempts.OutputTokens); err != nil {
		return err
	}
	if err := addUsageMetric(&summary.CachedTokens, cached, &summary.UnknownAttempts.CachedTokens); err != nil {
		return err
	}
	return addUsageMetric(&summary.TotalTokens, total, &summary.UnknownAttempts.TotalTokens)
}

func addUsageMetric(total **int64, value *int64, unknown *int) error {
	if value == nil {
		*unknown++
		return nil
	}
	if *value < 0 {
		return errors.New("negative persisted Usage")
	}
	if *total == nil {
		copy := *value
		*total = &copy
		return nil
	}
	if *value > math.MaxInt64-**total {
		return errors.New("persisted Usage total overflow")
	}
	**total += *value
	return nil
}

type currentRunView struct {
	Run
	ModelName         string  `json:"model_name"`
	ProviderID        *string `json:"provider_id"`
	Model             *string `json:"model"`
	MaxLoop           int     `json:"max_loop"`
	MaxToolCall       int     `json:"max_tool_call"`
	LoopRemaining     int     `json:"loop_remaining"`
	ToolCallRemaining int     `json:"tool_call_remaining"`
	ElapsedMS         int64   `json:"elapsed_ms"`
	CurrentActivity   string  `json:"current_activity"`
	LastActivityAt    string  `json:"last_activity_at"`
	usageSummary
}

func currentRunMonitoring(run Run, session Session, now time.Time) (currentRunView, error) {
	var entries []traceEntry
	if err := json.Unmarshal(run.Progress, &entries); err != nil || entries == nil {
		return currentRunView{}, errors.New("invalid persisted Run progress")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, run.StartedAt)
	if err != nil {
		return currentRunView{}, err
	}
	view := currentRunView{
		Run: run, ModelName: session.ModelName, MaxLoop: session.MaxLoop, MaxToolCall: session.MaxToolCall,
		LoopRemaining: maxInt(0, session.MaxLoop-run.DecisionCount), ToolCallRemaining: maxInt(0, session.MaxToolCall-run.ToolCalls),
		ElapsedMS: maxInt64(0, now.Sub(startedAt).Milliseconds()), CurrentActivity: "starting", LastActivityAt: run.StartedAt,
	}
	tracedCalls := 0
	for _, entry := range entries {
		if entry.Type == "model_call" {
			tracedCalls++
			if err := addUsageAttempt(&view.usageSummary, entry.Usage); err != nil {
				return currentRunView{}, err
			}
			if entry.ProviderID != "" {
				view.ProviderID = nullablePointer(entry.ProviderID)
				view.Model = nullablePointer(entry.Model)
			}
		}
	}
	for missing := tracedCalls; missing < run.ModelCalls; missing++ {
		_ = addUsageAttempt(&view.usageSummary, nil)
	}
	if tracedCalls > run.ModelCalls {
		return currentRunView{}, errors.New("Run model trace exceeds persisted model-call count")
	}
	if len(entries) != 0 {
		last := entries[len(entries)-1]
		view.CurrentActivity, view.LastActivityAt = traceActivity(last, run.StartedAt)
	}
	return view, nil
}

func traceActivity(entry traceEntry, fallback string) (string, string) {
	lastAt := entry.StartedAt
	if entry.EndedAt != nil {
		lastAt = *entry.EndedAt
	}
	if lastAt == "" {
		lastAt = fallback
	}
	switch entry.Type {
	case "model_call":
		if entry.EndedAt == nil {
			if entry.ProviderID == "" {
				return "resolving_model", lastAt
			}
			return "waiting_for_model", lastAt
		}
		if len(entry.ToolCalls) != 0 {
			return "preparing_tool_call", lastAt
		}
		return "finalizing", lastAt
	case "tool_call":
		if entry.EndedAt == nil {
			return "tool:" + entry.ToolName, lastAt
		}
		return "between_iterations", lastAt
	default:
		return "running", lastAt
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

type toolStats struct {
	ToolName           string   `json:"tool_name"`
	CallCount          int      `json:"call_count"`
	SuccessCount       int      `json:"success_count"`
	ErrorCount         int      `json:"error_count"`
	InFlightCount      int      `json:"in_flight_count"`
	LatencySampleCount int      `json:"latency_sample_count"`
	AverageLatencyMS   *float64 `json:"avg_latency_ms"`
	MaxLatencyMS       *int64   `json:"max_latency_ms"`
	LastLatencyMS      *int64   `json:"last_latency_ms"`
	latencyTotal       int64
}

func (r *AgentRuntime) serveToolStats(w http.ResponseWriter, request *http.Request, sessionID string) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if request.URL.RawQuery != "" {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Tool statistics do not accept query parameters")
		return
	}
	if _, err := r.sessionByID(request.Context(), sessionID); errors.Is(err, sql.ErrNoRows) {
		writeHTTPError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "Session does not exist")
		return
	} else if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Tool statistics")
		return
	}
	runs, err := r.monitoringRuns(request.Context(), sessionID)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to read Tool statistics")
		return
	}
	stats, err := aggregateToolStats(runs)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unable to aggregate Tool statistics")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "tools": stats})
}

func aggregateToolStats(runs []monitoringRun) ([]toolStats, error) {
	known := make(map[string]bool)
	statsByName := make(map[string]*toolStats)
	for _, definition := range runtimeToolDefinitions() {
		name, _ := definition["name"].(string)
		if name == "" {
			continue
		}
		known[name] = true
		statsByName[name] = &toolStats{ToolName: name}
	}
	statsByName[unknownToolStatsName] = &toolStats{ToolName: unknownToolStatsName}
	for _, run := range runs {
		tracedCalls := 0
		for _, entry := range run.Progress {
			if entry.Type != "tool_call" {
				continue
			}
			tracedCalls++
			name := entry.ToolName
			if !known[name] {
				name = unknownToolStatsName
			}
			stats := statsByName[name]
			stats.CallCount++
			if entry.EndedAt == nil && entry.StartedAt != "" {
				if run.Status == "running" {
					stats.InFlightCount++
				} else {
					stats.ErrorCount++
				}
			} else if toolResultSucceeded(entry.Result) {
				stats.SuccessCount++
			} else {
				stats.ErrorCount++
			}
			if entry.DurationMS != nil {
				if *entry.DurationMS < 0 || *entry.DurationMS > math.MaxInt64-stats.latencyTotal {
					return nil, errors.New("invalid persisted Tool latency")
				}
				stats.latencyTotal += *entry.DurationMS
				stats.LatencySampleCount++
				value := *entry.DurationMS
				stats.LastLatencyMS = &value
				if stats.MaxLatencyMS == nil || value > *stats.MaxLatencyMS {
					stats.MaxLatencyMS = &value
				}
			}
		}
		if tracedCalls > run.ToolCalls {
			return nil, errors.New("Run Tool trace exceeds persisted Tool-call count")
		}
		missing := run.ToolCalls - tracedCalls
		statsByName[unknownToolStatsName].CallCount += missing
		if run.Status == "running" {
			statsByName[unknownToolStatsName].InFlightCount += missing
		} else {
			statsByName[unknownToolStatsName].ErrorCount += missing
		}
	}
	names := make([]string, 0, len(statsByName))
	for name := range statsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]toolStats, 0, len(names))
	for _, name := range names {
		stats := statsByName[name]
		if stats.LatencySampleCount != 0 {
			average := float64(stats.latencyTotal) / float64(stats.LatencySampleCount)
			stats.AverageLatencyMS = &average
		}
		result = append(result, *stats)
	}
	return result, nil
}

func toolResultSucceeded(result json.RawMessage) bool {
	var envelope struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal(result, &envelope) == nil && envelope.OK
}

func finishTraceTiming(entry *traceEntry, started, ended time.Time) {
	endedAt := ended.UTC().Format(time.RFC3339Nano)
	duration := ended.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	entry.EndedAt = &endedAt
	entry.DurationMS = &duration
}

func validGatewayUsage(usage *gatewayUsage) bool {
	if usage == nil {
		return true
	}
	for _, value := range []*int64{usage.InputTokens, usage.OutputTokens, usage.CachedTokens, usage.TotalTokens} {
		if value != nil && *value < 0 {
			return false
		}
	}
	return usage.InputTokens == nil || usage.CachedTokens == nil || *usage.CachedTokens <= *usage.InputTokens
}
