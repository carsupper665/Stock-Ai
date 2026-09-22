package agent

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"stock-ai/agent-server/internal/eventloop"
	"stock-ai/agent-server/internal/events"
)

// priceSampleMaxAge bounds how far a Backend sample time may lie from the Event clock, in either
// direction. Backend serves prices fresher than its 1.5s TTL or waits for a new one, so this only
// rejects broken sources or gross clock skew; a future-dated sample would otherwise be remembered
// and silence every genuine later sample until the clock caught up.
const priceSampleMaxAge = 30 * time.Second

func (r *AgentRuntime) watchedInstruments(ctx context.Context, now time.Time) ([]events.Instrument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.persistenceErr != nil {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT market, symbol FROM events
		WHERE market IS NOT NULL AND symbol IS NOT NULL AND expires_at > ? ORDER BY market, symbol`, events.FormatTime(now))
	if err != nil {
		return nil, r.roundFailureLocked(ctx, nil, err)
	}
	defer rows.Close()
	watched := make([]events.Instrument, 0)
	for rows.Next() {
		var item events.Instrument
		if err := rows.Scan(&item.Market, &item.Symbol); err != nil {
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
func (r *AgentRuntime) samplePrices(ctx context.Context, clock eventloop.Clock, watched []events.Instrument, last map[string]events.PriceSample) map[string]events.PriceSample {
	fresh := make(map[string]events.PriceSample, len(watched))
	keep := make(map[string]bool, len(watched))
	for _, item := range watched {
		keep[item.Key()] = true
		sample, ok := r.fetchPriceSample(ctx, item)
		if !ok {
			continue
		}
		if age := clock.Now().Sub(sample.UpdatedAt); age > priceSampleMaxAge || age < -priceSampleMaxAge {
			continue
		}
		if previous, seen := last[item.Key()]; seen && !sample.UpdatedAt.After(previous.UpdatedAt) {
			continue
		}
		last[item.Key()] = sample
		fresh[item.Key()] = sample
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
func (r *AgentRuntime) fetchPriceSample(ctx context.Context, item events.Instrument) (events.PriceSample, bool) {
	requestContext, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
	defer cancel()
	endpoint, err := url.Parse(strings.TrimRight(r.config.BackendURL, "/") + "/v1/market/price")
	if err != nil {
		return events.PriceSample{}, false
	}
	endpoint.RawQuery = url.Values{"market": {item.Market}, "symbol": {item.Symbol}}.Encode()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return events.PriceSample{}, false
	}
	request.Header.Set("Authorization", "Bearer "+r.config.BackendUserToken)
	response, err := r.httpClient.Do(request)
	if err != nil {
		return events.PriceSample{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return events.PriceSample{}, false
	}
	var body struct {
		Price     *float64 `json:"price"`
		UpdatedAt string   `json:"updated_at"`
	}
	if err := decodeDependencyJSON(response.Body, &body); err != nil || body.Price == nil ||
		math.IsNaN(*body.Price) || math.IsInf(*body.Price, 0) || *body.Price <= 0 {
		return events.PriceSample{}, false
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, body.UpdatedAt)
	if err != nil {
		return events.PriceSample{}, false
	}
	return events.PriceSample{Price: *body.Price, UpdatedAt: updatedAt}, true
}
