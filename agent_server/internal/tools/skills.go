package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// skillDefinitions are exposed only to Sessions created with skills_enabled.
func skillDefinitions() []map[string]any {
	positive := map[string]any{"type": "number", "exclusiveMinimum": 0}
	return []map[string]any{
		{
			"name": "skill_get", "description": "Read one skill listed in skill_index by name: the full playbook for the task it describes.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}},
				"required": []string{"name"}, "additionalProperties": false,
			},
		},
		{
			"name": "plan_trade", "description": "Compute an order's numbers from the bound Account and the symbol's rules without placing it: entry, stop, target, " +
				"risk and reward per unit, R multiple, quantity, notional, margin, and the loss at the stop with its percent of equity. " +
				"Pass exactly one of take_profit or target_r, and exactly one of risk_pct or quantity. A market order plans at the current price. " +
				"Returns problems that make the order invalid; when there are none it returns the exact place_order arguments as order. Fees are not included.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"market":      map[string]any{"type": "string", "enum": []string{"crypto", "stock"}, "default": "crypto", "description": marketDescription},
					"symbol":      map[string]any{"type": "string", "pattern": "^[A-Z0-9]{2,24}$", "description": "The symbol you would pass to place_order."},
					"product":     map[string]any{"type": "string", "enum": []string{"spot", "futures"}, "default": "futures"},
					"side":        map[string]any{"type": "string", "enum": []string{"buy", "sell"}},
					"type":        map[string]any{"type": "string", "enum": []string{"market", "limit"}, "default": "market"},
					"price":       map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "Limit price; required for limit orders only."},
					"leverage":    map[string]any{"type": "number", "minimum": 1, "maximum": 100, "default": 1},
					"stop_loss":   positive,
					"take_profit": positive,
					"target_r":    map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "Target as a multiple of the risk per unit, e.g. 2 for 2R."},
					"risk_pct":    map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 100, "description": "Percent of equity lost if the stop fills; sizes the quantity."},
					"quantity":    positive,
				}, "required": []string{"symbol", "side", "stop_loss"}, "additionalProperties": false,
			},
		},
	}
}

func SkillName(arguments json.RawMessage) (string, error) {
	var rawArguments struct {
		Name json.RawMessage `json:"name"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return "", errors.New("only name is accepted")
	}
	return RequiredString(rawArguments.Name, "name")
}

type TradeInputs struct {
	Market, Symbol, Product, Side, Type string
	Price, Leverage, StopLoss           float64
	TakeProfit, TargetR                 float64
	RiskPct, Quantity                   float64
}

func PlanTradeInputs(arguments json.RawMessage) (TradeInputs, error) {
	var rawArguments struct {
		Market     json.RawMessage `json:"market"`
		Symbol     json.RawMessage `json:"symbol"`
		Product    json.RawMessage `json:"product"`
		Side       json.RawMessage `json:"side"`
		Type       json.RawMessage `json:"type"`
		Price      json.RawMessage `json:"price"`
		Leverage   json.RawMessage `json:"leverage"`
		StopLoss   json.RawMessage `json:"stop_loss"`
		TakeProfit json.RawMessage `json:"take_profit"`
		TargetR    json.RawMessage `json:"target_r"`
		RiskPct    json.RawMessage `json:"risk_pct"`
		Quantity   json.RawMessage `json:"quantity"`
	}
	if !DecodeArguments(arguments, &rawArguments) {
		return TradeInputs{}, errors.New("only market, symbol, product, side, type, price, leverage, stop_loss, take_profit, target_r, risk_pct, and quantity are accepted")
	}
	var in TradeInputs
	var err error
	if in.Market, in.Symbol, err = ParseMarketSymbol(rawArguments.Market, rawArguments.Symbol); err != nil {
		return TradeInputs{}, err
	}
	if in.Market = strings.ToLower(in.Market); in.Market != "crypto" && in.Market != "stock" {
		return TradeInputs{}, errors.New("market must be one of crypto, stock")
	}
	if in.Side, err = enumString(rawArguments.Side, "side", "buy", "sell"); err != nil {
		return TradeInputs{}, err
	}
	if in.Product, err = optionalEnumString(rawArguments.Product, "product", "futures", "spot", "futures"); err != nil {
		return TradeInputs{}, err
	}
	if in.Type, err = optionalEnumString(rawArguments.Type, "type", "market", "market", "limit"); err != nil {
		return TradeInputs{}, err
	}
	numbers := []struct {
		raw    json.RawMessage
		name   string
		target *float64
	}{
		{rawArguments.Price, "price", &in.Price}, {rawArguments.Leverage, "leverage", &in.Leverage},
		{rawArguments.StopLoss, "stop_loss", &in.StopLoss}, {rawArguments.TakeProfit, "take_profit", &in.TakeProfit},
		{rawArguments.TargetR, "target_r", &in.TargetR}, {rawArguments.RiskPct, "risk_pct", &in.RiskPct},
		{rawArguments.Quantity, "quantity", &in.Quantity},
	}
	for _, field := range numbers {
		if len(field.raw) == 0 {
			continue
		}
		validated, err := number(field.raw, field.name, true)
		if err != nil {
			return TradeInputs{}, err
		}
		_ = json.Unmarshal(validated, field.target)
	}
	switch {
	case in.StopLoss == 0:
		return TradeInputs{}, errors.New("stop_loss is required")
	case in.Type == "limit" && in.Price == 0:
		return TradeInputs{}, errors.New("price is required for limit orders")
	case in.Type == "market" && in.Price != 0:
		return TradeInputs{}, errors.New("price is only for limit orders; a market order plans at the current price")
	case (in.TakeProfit == 0) == (in.TargetR == 0):
		return TradeInputs{}, errors.New("pass exactly one of take_profit or target_r")
	case (in.RiskPct == 0) == (in.Quantity == 0):
		return TradeInputs{}, errors.New("pass exactly one of risk_pct or quantity")
	case in.RiskPct > 100:
		return TradeInputs{}, errors.New("risk_pct must be at most 100")
	}
	if in.Leverage == 0 {
		in.Leverage = 1
	}
	if in.Leverage > 100 {
		return TradeInputs{}, errors.New("leverage must be between 1 and 100")
	}
	if in.Product == "spot" && in.Leverage != 1 {
		return TradeInputs{}, errors.New("spot does not support leverage; leverage must be 1")
	}
	return in, nil
}

// PlanTrade computes the plan at entry. tickSize and stepSize are the symbol's source rules and may be empty.
func PlanTrade(in TradeInputs, entry, equity, available float64, tickSize, stepSize string) map[string]any {
	direction := 1.0
	if in.Side == "sell" {
		direction = -1
	}
	stop := roundToStep(in.StopLoss, tickSize, math.Round)
	riskPerUnit := (entry - stop) * direction
	plan := map[string]any{"entry": entry, "stop_loss": stop, "equity": equity, "available": available}
	problems := make([]string, 0)
	if riskPerUnit <= 0 {
		problems = append(problems, fmt.Sprintf("stop_loss %v must be %s the entry %v for a %s", stop, map[string]string{"buy": "below", "sell": "above"}[in.Side], entry, in.Side))
		plan["problems"] = problems
		return plan
	}
	takeProfit := in.TakeProfit
	if in.TargetR != 0 {
		takeProfit = entry + direction*in.TargetR*riskPerUnit
	}
	takeProfit = roundToStep(takeProfit, tickSize, math.Round)
	rewardPerUnit := (takeProfit - entry) * direction
	if rewardPerUnit <= 0 {
		problems = append(problems, fmt.Sprintf("take_profit %v must be %s the entry %v for a %s", takeProfit, map[string]string{"buy": "above", "sell": "below"}[in.Side], entry, in.Side))
	}
	quantity := in.Quantity
	if in.RiskPct != 0 {
		quantity = equity * in.RiskPct / 100 / riskPerUnit
	}
	// The epsilon keeps a value that is already on the step, like 0.3 at step 0.1, from flooring one step down.
	quantity = roundToStep(quantity, stepSize, func(value float64) float64 { return math.Floor(value + 1e-9) })
	if quantity <= 0 {
		problems = append(problems, fmt.Sprintf("quantity rounds to 0 at step size %s; raise risk_pct or quantity", stepSize))
	}
	notional := quantity * entry
	margin := notional / in.Leverage
	if margin > available {
		problems = append(problems, fmt.Sprintf("margin_required %v exceeds the available balance %v", roundDecimals(margin, 8), available))
	}
	maxLoss := quantity * riskPerUnit
	plan["take_profit"] = takeProfit
	plan["risk_per_unit"] = roundDecimals(riskPerUnit, 8)
	plan["reward_per_unit"] = roundDecimals(rewardPerUnit, 8)
	plan["r_multiple"] = roundDecimals(rewardPerUnit/riskPerUnit, 2)
	plan["quantity"] = quantity
	plan["leverage"] = in.Leverage
	plan["notional"] = roundDecimals(notional, 8)
	plan["margin_required"] = roundDecimals(margin, 8)
	plan["max_loss"] = roundDecimals(maxLoss, 8)
	plan["max_gain"] = roundDecimals(quantity*rewardPerUnit, 8)
	if equity > 0 {
		plan["max_loss_pct_of_equity"] = roundDecimals(maxLoss/equity*100, 2)
	}
	plan["problems"] = problems
	if len(problems) != 0 {
		return plan
	}
	order := map[string]any{
		"market": in.Market, "symbol": in.Symbol, "product": in.Product, "side": in.Side, "type": in.Type,
		"quantity": quantity, "leverage": in.Leverage, "stop_loss": stop, "take_profit": takeProfit,
	}
	if in.Type == "limit" {
		order["price"] = entry
	}
	plan["order"] = order
	return plan
}

// roundToStep rounds value to a multiple of step with round, keeping the step's decimals; an unusable step leaves value as is.
func roundToStep(value float64, step string, round func(float64) float64) float64 {
	size, err := strconv.ParseFloat(step, 64)
	if err != nil || size <= 0 {
		return value
	}
	decimals := 0
	if _, fraction, found := strings.Cut(strings.TrimRight(step, "0"), "."); found {
		decimals = len(fraction)
	}
	return roundDecimals(round(value/size)*size, decimals)
}

func roundDecimals(value float64, decimals int) float64 {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'f', decimals, 64), 64)
	return rounded
}
