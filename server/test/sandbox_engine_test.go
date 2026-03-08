package test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"server/sandbox"
)

func TestReplayEngineAdvanceIsDeterministic(t *testing.T) {
	engineA := newReplayEngine(t)
	engineB := newReplayEngine(t)

	sequenceA := collectEngineSequence(t, engineA)
	sequenceB := collectEngineSequence(t, engineB)

	if !reflect.DeepEqual(sequenceA, sequenceB) {
		t.Fatalf("expected deterministic sequences, got %#v and %#v", sequenceA, sequenceB)
	}

	last := sequenceA[len(sequenceA)-1]
	if last.Cursor != 2 {
		t.Fatalf("expected last cursor 2, got %d", last.Cursor)
	}
	if last.RemainingBars != 0 {
		t.Fatalf("expected no remaining bars, got %d", last.RemainingBars)
	}
	if len(last.VisibleBars) != 3 {
		t.Fatalf("expected 3 visible bars at the end, got %d", len(last.VisibleBars))
	}
}

func TestReplayEngineResetReturnsBaseline(t *testing.T) {
	engine := newReplayEngine(t)

	if _, err := engine.Advance(); err != nil {
		t.Fatalf("first advance failed: %v", err)
	}
	if _, err := engine.Advance(); err != nil {
		t.Fatalf("second advance failed: %v", err)
	}

	resetState, err := engine.Reset()
	if err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	wantTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if resetState.Cursor != 0 {
		t.Fatalf("expected cursor 0 after reset, got %d", resetState.Cursor)
	}
	if resetState.CurrentBar.Close != 101.5 {
		t.Fatalf("expected first bar close 101.5 after reset, got %v", resetState.CurrentBar.Close)
	}
	if len(resetState.VisibleBars) != 1 {
		t.Fatalf("expected one visible bar after reset, got %d", len(resetState.VisibleBars))
	}
	if !resetState.Clock.CurrentTime.Equal(wantTime) {
		t.Fatalf("expected reset clock time %s, got %s", wantTime, resetState.Clock.CurrentTime)
	}
}

func TestClockRejectsNonSequentialAdvance(t *testing.T) {
	clock := sandbox.NewClock()
	first := sandbox.CursorState{
		Cursor:    0,
		TotalBars: 3,
		Bar: sandbox.Bar{
			Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	if _, err := clock.Reset(first); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	skipped := sandbox.CursorState{
		Cursor:    2,
		TotalBars: 3,
		Bar: sandbox.Bar{
			Time: time.Date(2025, 1, 1, 0, 2, 0, 0, time.UTC),
		},
	}
	if _, err := clock.Advance(skipped); !errors.Is(err, sandbox.ErrClockInvariant) {
		t.Fatalf("expected ErrClockInvariant, got %v", err)
	}
}

func newReplayEngine(t *testing.T) *sandbox.ReplayEngine {
	t.Helper()

	feed := sandbox.NewReplayFeed()
	scenarioPath := filepath.Join("..", "testdata", "replay", "btcusdt_1m.json")
	if err := feed.LoadScenarioFile(scenarioPath); err != nil {
		t.Fatalf("LoadScenarioFile failed: %v", err)
	}

	engine, err := sandbox.NewReplayEngine(feed)
	if err != nil {
		t.Fatalf("NewReplayEngine failed: %v", err)
	}
	return engine
}

func collectEngineSequence(t *testing.T, engine *sandbox.ReplayEngine) []sandbox.EngineState {
	t.Helper()

	state, err := engine.State()
	if err != nil {
		t.Fatalf("State failed: %v", err)
	}
	sequence := []sandbox.EngineState{state}

	for {
		next, err := engine.Advance()
		if errors.Is(err, sandbox.ErrNoMoreBars) {
			break
		}
		if err != nil {
			t.Fatalf("Advance failed: %v", err)
		}
		sequence = append(sequence, next)
	}

	return sequence
}
