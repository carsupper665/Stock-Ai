package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"stock-ai/agent-server/internal/tools"
)

func (r *AgentRuntime) executeTool(ctx context.Context, session Session, runID int64, call gatewayTool) json.RawMessage {
	switch call.Name {
	case "create_event", "delete_event", "list_events":
		return r.executeEventTool(session, runID, call, time.Now())
	case "get_market_snapshot":
		query, err := tools.SnapshotQuery(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.backendToolRequest(ctx, session, http.MethodGet, "/v1/market/price", query, nil)
	case "get_ohlcv":
		query, err := tools.OHLCVQuery(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.backendToolRequest(ctx, session, http.MethodGet, "/v1/market/ohlcv", query, nil)
	case "get_market_info":
		query, err := tools.MarketInfoQuery(call.Arguments)
		if err != nil {
			return tools.ErrorResult("INVALID_TOOL_ARGUMENTS", err.Error(), "not_executed")
		}
		return r.backendToolRequest(ctx, session, http.MethodGet, "/v1/market/info", query, nil)
	default:
		if result, handled := r.executeTradingTool(ctx, session, runID, call); handled {
			return result
		}
		if result, handled := r.executeMessageTool(ctx, session, call); handled {
			return result
		}
		if result, handled := r.executeMemoryTool(ctx, session.ID, runID, call); handled {
			return result
		}
		if result, handled := r.executeSkillTool(ctx, session, call); handled {
			return result
		}
		return tools.ErrorResult("UNKNOWN_TOOL", "tool is not registered", "not_executed")
	}
}

func dependencyReasonCode(err error) string {
	var dependency *dependencyError
	if errors.As(err, &dependency) {
		return dependency.code
	}
	return "DEPENDENCY_UNAVAILABLE"
}

// toolErrorCode returns the code of a failed Tool result, or "" when the Tool succeeded.
func toolErrorCode(result json.RawMessage) string {
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(result, &envelope) != nil || envelope.OK {
		return ""
	}
	if envelope.Error.Code == "" {
		return "UNKNOWN"
	}
	return envelope.Error.Code
}
