package service

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"server/model/store"

	"gorm.io/gorm"
)

// Test that Monitor.Snapshot includes indicators for a small replay dataset (minimal set)
func TestMonitorSnapshotIncludesIndicators(t *testing.T) {
	app := newTestApp(t)
	// create a tiny dataset and sandbox via the helper
	seedReplaySandbox(t, app.DB, "sandbox-ind", "account-ind")

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-ind"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}

	snap, err := app.Monitor.Snapshot(ctx, "sandbox-ind")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap == nil {
		t.Fatalf("expected snapshot, got nil")
	}
	// indicators map should exist (OBV at minimum for short series)
	if snap.Indicators == nil {
		t.Fatalf("expected indicators in snapshot")
	}
	obv, ok := snap.Indicators["obv"]
	if !ok || len(obv) == 0 {
		t.Fatalf("expected obv series, got: %+v", snap.Indicators)
	}

	// sanity: ensure accounts were included
	if len(snap.Accounts) == 0 {
		t.Fatalf("expected accounts in snapshot")
	}
	// ensure orders/trades arrays exist (may be empty)
	_ = snap.Orders
	_ = snap.Trades
	_ = snap.Positions

	// done
}

func TestMonitorSnapshotIndicatorsStopAtReplayCurrentTime(t *testing.T) {
	app := newTestApp(t)
	seedIndicatorDataset(t, app.DB, "dataset-snapshot-cursor", 40)

	replayCursor := time.Date(2025, 1, 1, 0, 20, 0, 0, time.UTC)
	app.Monitor.clock = funcClock{now: func() time.Time {
		return time.Date(2025, 1, 1, 0, 39, 0, 0, time.UTC)
	}}

	sandbox := store.Sandbox{
		ID:                "sandbox-snapshot-cursor",
		Name:              "sandbox cursor",
		Mode:              "replay",
		Status:            store.SandboxStatusPaused,
		StartDatetime:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ReplayCurrentTime: replayCursor,
		ReplaySpeed:       1,
		DatasetID:         "dataset-snapshot-cursor",
	}
	if err := app.DB.Create(&sandbox).Error; err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	snap, err := app.Monitor.Snapshot(context.Background(), sandbox.ID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Indicators == nil {
		t.Fatalf("expected indicators in snapshot")
	}

	for key, points := range snap.Indicators {
		for _, point := range points {
			if point.At.After(replayCursor) {
				t.Fatalf("indicator %q included future point at %s after replay cursor %s", key, point.At, replayCursor)
			}
		}
	}
}

func TestIndicatorContractIncludesRequestedKeys(t *testing.T) {
	app := newTestApp(t)
	seedIndicatorDataset(t, app.DB, "dataset-indicator-contract", 40)

	indicators, err := app.Monitor.indicators.ComputeForDataset(context.Background(), "dataset-indicator-contract", time.Date(2025, 1, 1, 0, 39, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("compute indicators: %v", err)
	}

	requiredKeys := []string{
		"sma_20",
		"macd_line",
		"macd_signal",
		"macd_hist",
		"rsi_14",
		"bb_upper_20_2",
		"bb_middle_20",
		"bb_lower_20_2",
		"obv",
		"kd_k",
		"kd_d",
		"kd_j",
		"dmi_plus_di",
		"dmi_minus_di",
		"dmi_adx",
		"ad",
		"bias_20",
	}
	for _, key := range requiredKeys {
		points, ok := indicators[key]
		if !ok {
			t.Fatalf("missing indicator key %q in keys %v", key, sortedIndicatorKeys(indicators))
		}
		if len(points) == 0 {
			t.Fatalf("expected indicator %q to have points", key)
		}
		for _, point := range points {
			if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
				t.Fatalf("indicator %q emitted non-finite value %v", key, point.Value)
			}
		}
	}
}

func TestIndicatorsDoNotEncodeNaN(t *testing.T) {
	app := newTestApp(t)
	seedIndicatorDataset(t, app.DB, "dataset-short-indicator-contract", 2)

	indicators, err := app.Monitor.indicators.ComputeForDataset(context.Background(), "dataset-short-indicator-contract", time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("compute indicators: %v", err)
	}

	payload, err := json.Marshal(indicators)
	if err != nil {
		t.Fatalf("marshal indicators: %v", err)
	}
	encoded := string(payload)
	for _, forbidden := range []string{"NaN", "Infinity", "+Inf", "-Inf"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("encoded indicators contain %s: %s", forbidden, encoded)
		}
	}
}

func seedIndicatorDataset(t *testing.T, db interface {
	Create(value any) *gorm.DB
}, datasetID string, count int) {
	t.Helper()

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	dataset := store.ReplayDataset{
		ID:       datasetID,
		Name:     "Indicator Contract",
		Symbol:   "BTCUSDT",
		Interval: "1m",
		StartAt:  start,
		EndAt:    start.Add(time.Duration(count-1) * time.Minute),
		Source:   "test",
	}
	if err := db.Create(&dataset).Error; err != nil {
		t.Fatalf("create indicator dataset: %v", err)
	}

	klines := make([]store.ReplayKline, 0, count)
	for i := range count {
		base := 100 + float64(i)
		if i%7 == 0 {
			base -= 4
		}
		open := base - 0.5
		close := base + float64((i%5)-2)*0.35
		low := math.Min(open, close) - 1 - float64(i%3)*0.1
		high := math.Max(open, close) + 1 + float64(i%4)*0.15
		klines = append(klines, store.ReplayKline{
			DatasetID: dataset.ID,
			Symbol:    dataset.Symbol,
			Ts:        start.Add(time.Duration(i) * time.Minute),
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close,
			Volume:    10 + float64(i%6),
		})
	}
	if err := db.Create(&klines).Error; err != nil {
		t.Fatalf("create indicator klines: %v", err)
	}
}

func sortedIndicatorKeys(indicators map[string][]IndicatorPoint) []string {
	keys := make([]string, 0, len(indicators))
	for key := range indicators {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
