package events

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// priceSampleMaxAge bounds how far a Backend sample time may lie from the Event clock, in either
// direction. Backend serves prices fresher than its 1.5s TTL or waits for a new one, so this only
// rejects broken sources or gross clock skew; a future-dated sample would otherwise be remembered
// and silence every genuine later sample until the clock caught up.
const priceSampleMaxAge = 30 * time.Second

type PriceSample struct {
	Price     float64
	UpdatedAt time.Time
}

func (s PriceSample) encode() string {
	encoded, _ := json.Marshal(map[string]any{"price": s.Price, "updated_at": FormatTime(s.UpdatedAt)})
	return string(encoded)
}

func decodePriceSample(state json.RawMessage) (*PriceSample, error) {
	if len(state) == 0 || string(state) == "null" {
		return nil, nil
	}
	var stored struct {
		Price     float64 `json:"price"`
		UpdatedAt string  `json:"updated_at"`
	}
	if err := json.Unmarshal(state, &stored); err != nil {
		return nil, err
	}
	updatedAt, err := parseEventTime(stored.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &PriceSample{Price: stored.Price, UpdatedAt: updatedAt}, nil
}

type Instrument struct {
	Market string
	Symbol string
}

func (i Instrument) Key() string { return i.Market + "/" + i.Symbol }

func evaluatePriceEvent(record Record, sample PriceSample) (map[string]any, *PriceSample, error) {
	sampleFacts := func(facts map[string]any) map[string]any {
		facts["price"] = sample.Price
		facts["updated_at"] = FormatTime(sample.UpdatedAt)
		return facts
	}
	switch record.Type {
	case "price_above", "price_below":
		var params priceLevelParams
		if err := json.Unmarshal(record.Params, &params); err != nil {
			return nil, nil, fmt.Errorf("event %s params: %w", record.ID, err)
		}
		if record.Type == "price_above" && sample.Price >= params.Price {
			return sampleFacts(map[string]any{"condition": "price >= " + formatPrice(params.Price)}), nil, nil
		}
		if record.Type == "price_below" && sample.Price <= params.Price {
			return sampleFacts(map[string]any{"condition": "price <= " + formatPrice(params.Price)}), nil, nil
		}
		return nil, nil, nil
	case "price_cross_above", "price_cross_below":
		var params priceLevelParams
		if err := json.Unmarshal(record.Params, &params); err != nil {
			return nil, nil, fmt.Errorf("event %s params: %w", record.ID, err)
		}
		previous, err := decodePriceSample(record.State)
		if err != nil {
			return nil, nil, fmt.Errorf("event %s state: %w", record.ID, err)
		}
		if previous == nil {
			return nil, &sample, nil
		}
		if !sample.UpdatedAt.After(previous.UpdatedAt) {
			return nil, nil, nil
		}
		crossed := previous.Price < params.Price && sample.Price >= params.Price
		condition := "previous_price < " + formatPrice(params.Price) + " <= price"
		if record.Type == "price_cross_below" {
			crossed = previous.Price > params.Price && sample.Price <= params.Price
			condition = "previous_price > " + formatPrice(params.Price) + " >= price"
		}
		if !crossed {
			return nil, &sample, nil
		}
		return sampleFacts(map[string]any{
			"condition": condition, "previous_price": previous.Price, "previous_updated_at": FormatTime(previous.UpdatedAt),
		}), nil, nil
	case "price_change_pct":
		var params priceChangeParams
		if err := json.Unmarshal(record.Params, &params); err != nil {
			return nil, nil, fmt.Errorf("event %s params: %w", record.ID, err)
		}
		changePct := (sample.Price - params.BasePrice) / params.BasePrice * 100
		if math.IsNaN(changePct) || math.IsInf(changePct, 0) {
			return nil, nil, nil
		}
		condition := "change_pct >= " + formatPrice(params.Pct)
		matched := changePct >= params.Pct
		if params.Direction == "down" {
			condition = "change_pct <= -" + formatPrice(params.Pct)
			matched = changePct <= -params.Pct
		}
		if !matched {
			return nil, nil, nil
		}
		return sampleFacts(map[string]any{"condition": condition, "base_price": params.BasePrice, "change_pct": changePct}), nil, nil
	default:
		return nil, nil, fmt.Errorf("event %s has unknown type %q", record.ID, record.Type)
	}
}

func formatPrice(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
