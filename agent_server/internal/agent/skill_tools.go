package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"stock-ai/agent-server/internal/skills"
	"stock-ai/agent-server/internal/tools"
)

func (r *AgentRuntime) executeSkillTool(ctx context.Context, session Session, call gatewayTool) (json.RawMessage, bool) {
	if call.Name != "skill_get" && call.Name != "plan_trade" {
		return nil, false
	}
	if !session.SkillsEnabled {
		return tools.ErrorResult("UNKNOWN_TOOL", "tool is not registered", "not_executed"), true
	}
	if call.Name == "skill_get" {
		name, err := tools.SkillName(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed"), true
		}
		content, ok := skills.Get(name)
		if !ok {
			return tools.ErrorResult("SKILL_NOT_FOUND", "no skill named "+name+"; use a name from skill_index", "not_executed"), true
		}
		return tools.SuccessResult(map[string]any{"name": name, "content": content}), true
	}
	return r.planTrade(ctx, session, call.Arguments), true
}

func (r *AgentRuntime) planTrade(ctx context.Context, session Session, arguments json.RawMessage) json.RawMessage {
	in, err := tools.PlanTradeInputs(arguments)
	if err != nil {
		return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
	}
	symbol := url.Values{"market": {in.Market}, "symbol": {in.Symbol}}
	var account struct {
		Equity    float64 `json:"equity"`
		Available float64 `json:"available"`
	}
	if failed := r.backendRead(ctx, session, "/v1/account", nil, &account); failed != nil {
		return failed
	}
	var rules struct {
		PriceFilter *struct {
			TickSize string `json:"tick_size"`
		} `json:"price_filter"`
		QuantityFilter *struct {
			StepSize string `json:"step_size"`
		} `json:"quantity_filter"`
	}
	if failed := r.backendRead(ctx, session, "/v1/market/info", symbol, &rules); failed != nil {
		return failed
	}
	entry := in.Price
	if in.Type == "market" {
		var snapshot struct {
			Price float64 `json:"price"`
		}
		if failed := r.backendRead(ctx, session, "/v1/market/price", symbol, &snapshot); failed != nil {
			return failed
		}
		entry = snapshot.Price
	}
	tickSize, stepSize := "", ""
	if rules.PriceFilter != nil {
		tickSize = rules.PriceFilter.TickSize
	}
	if rules.QuantityFilter != nil {
		stepSize = rules.QuantityFilter.StepSize
	}
	return tools.SuccessResult(tools.PlanTrade(in, entry, account.Equity, account.Available, tickSize, stepSize))
}

// backendRead decodes a successful Backend read into target, or returns the failed Tool result unchanged.
func (r *AgentRuntime) backendRead(ctx context.Context, session Session, path string, query url.Values, target any) json.RawMessage {
	result := r.backendToolRequest(ctx, session, http.MethodGet, path, query, nil)
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(result, &envelope) != nil || !envelope.OK {
		return result
	}
	if json.Unmarshal(envelope.Result, target) != nil {
		return tools.ErrorResult("BACKEND_RESPONSE_INVALID", "Backend returned an invalid response", "not_executed")
	}
	return nil
}
