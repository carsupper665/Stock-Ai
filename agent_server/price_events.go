package common

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// priceSampleMaxAge bounds how far a Backend sample time may lie from the Event clock, in either
// direction. Backend serves prices fresher than its 1.5s TTL or waits for a new one, so this only
// rejects broken sources or gross clock skew; a future-dated sample would otherwise be remembered
// and silence every genuine later sample until the clock caught up.
const priceSampleMaxAge = 30 * time.Second

type priceSample struct {
	Price     float64
	UpdatedAt time.Time
}

func (s priceSample) encode() string {
	encoded, _ := json.Marshal(map[string]any{"price": s.Price, "updated_at": formatEventTime(s.UpdatedAt)})
	return string(encoded)
}

func decodePriceSample(state json.RawMessage) (*priceSample, error) {
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
	return &priceSample{Price: stored.Price, UpdatedAt: updatedAt}, nil
}

type instrument struct {
	market string
	symbol string
}

func (i instrument) key() string { return i.market + "/" + i.symbol }

func (r *AgentRuntime) watchedInstruments(ctx context.Context, now time.Time) ([]instrument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT market, symbol FROM events
		WHERE market IS NOT NULL AND symbol IS NOT NULL AND expires_at > ? ORDER BY market, symbol`, formatEventTime(now))
	if err != nil {
		return nil, r.roundFailureLocked(ctx, nil, err)
	}
	defer rows.Close()
	watched := make([]instrument, 0)
	for rows.Next() {
		var item instrument
		if err := rows.Scan(&item.market, &item.symbol); err != nil {
			return nil, r.roundFailureLocked(ctx, nil, err)
		}
		watched = append(watched, item)
	}
	if err := rows.Err(); err != nil {
		return nil, r.roundFailureLocked(ctx, nil, err)
	}
	return watched, nil
}

// samplePrices queries Backend once per watched instrument, outside r.mu, and returns only
// samples that are well-formed, fresh when their answer arrives, and strictly newer than the
// previous round's sample. Duplicate or out-of-order samples carry no new information and never
// update state. Freshness is judged against the clock at arrival, not at the round start, so a
// slow earlier request in the same round cannot make a later answer look stale.
func (r *AgentRuntime) samplePrices(ctx context.Context, clock Clock, watched []instrument, last map[string]priceSample) map[string]priceSample {
	fresh := make(map[string]priceSample, len(watched))
	keep := make(map[string]bool, len(watched))
	for _, item := range watched {
		keep[item.key()] = true
		sample, ok := r.fetchPriceSample(ctx, item)
		if !ok {
			continue
		}
		if age := clock.Now().Sub(sample.UpdatedAt); age > priceSampleMaxAge || age < -priceSampleMaxAge {
			continue
		}
		if previous, seen := last[item.key()]; seen && !sample.UpdatedAt.After(previous.UpdatedAt) {
			continue
		}
		last[item.key()] = sample
		fresh[item.key()] = sample
	}
	for key := range last {
		if !keep[key] {
			delete(last, key)
		}
	}
	return fresh
}

// fetchPriceSample uses the Server's Backend USER credential: sampling is Server work shared by
// every Session watching the instrument, not a Session Tool call, so no Account Token is involved.
func (r *AgentRuntime) fetchPriceSample(ctx context.Context, item instrument) (priceSample, bool) {
	requestContext, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
	defer cancel()
	endpoint, err := url.Parse(strings.TrimRight(r.config.BackendURL, "/") + "/v1/market/price")
	if err != nil {
		return priceSample{}, false
	}
	endpoint.RawQuery = url.Values{"market": {item.market}, "symbol": {item.symbol}}.Encode()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return priceSample{}, false
	}
	request.Header.Set("Authorization", "Bearer "+r.config.BackendUserToken)
	response, err := r.httpClient.Do(request)
	if err != nil {
		return priceSample{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return priceSample{}, false
	}
	var body struct {
		Price     *float64 `json:"price"`
		UpdatedAt string   `json:"updated_at"`
	}
	if err := decodeDependencyJSON(response.Body, &body); err != nil || body.Price == nil ||
		math.IsNaN(*body.Price) || math.IsInf(*body.Price, 0) || *body.Price <= 0 {
		return priceSample{}, false
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, body.UpdatedAt)
	if err != nil {
		return priceSample{}, false
	}
	return priceSample{Price: *body.Price, UpdatedAt: updatedAt}, true
}

// evaluatePriceEvent applies one new valid sample to one Event. It returns the match facts
// when the Event fires, or the state to persist when a cross Event only advances its baseline.
func evaluatePriceEvent(record eventRecord, sample priceSample) (map[string]any, *priceSample, error) {
	sampleFacts := func(facts map[string]any) map[string]any {
		facts["price"] = sample.Price
		facts["updated_at"] = formatEventTime(sample.UpdatedAt)
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
			"condition": condition, "previous_price": previous.Price, "previous_updated_at": formatEventTime(previous.UpdatedAt),
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
