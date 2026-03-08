package sandbox

import (
	"errors"
	"fmt"
	"sync"
)

var ErrEngineNotReady = errors.New("replay engine not ready")

type replayEngineFeed interface {
	Current() (CursorState, bool)
	Advance() (CursorState, error)
	Reset() error
	Bars() []Bar
}

type EngineState struct {
	ScenarioID    string
	Symbol        string
	Interval      string
	Cursor        int
	TotalBars     int
	RemainingBars int
	CurrentBar    Bar
	VisibleBars   []Bar
	Clock         ClockSnapshot
}

type ReplayEngine struct {
	mu    sync.RWMutex
	feed  replayEngineFeed
	clock *Clock
}

func NewReplayEngine(feed replayEngineFeed) (*ReplayEngine, error) {
	if feed == nil {
		return nil, errors.New("replay feed is required")
	}

	engine := &ReplayEngine{
		feed:  feed,
		clock: NewClock(),
	}

	if _, err := engine.Reset(); err != nil {
		return nil, err
	}
	return engine, nil
}

func (e *ReplayEngine) State() (EngineState, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	state, ok := e.feed.Current()
	if !ok {
		return EngineState{}, ErrEngineNotReady
	}
	return e.stateLocked(state)
}

func (e *ReplayEngine) Advance() (EngineState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	nextState, err := e.feed.Advance()
	if err != nil {
		return EngineState{}, err
	}
	if _, err := e.clock.Advance(nextState); err != nil {
		return EngineState{}, err
	}
	return e.stateLocked(nextState)
}

func (e *ReplayEngine) Reset() (EngineState, error) {
	return e.Seek(0)
}

func (e *ReplayEngine) Seek(cursor int) (EngineState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if cursor < 0 {
		return EngineState{}, fmt.Errorf("cursor must be non-negative: %d", cursor)
	}
	if err := e.feed.Reset(); err != nil {
		return EngineState{}, err
	}
	state, ok := e.feed.Current()
	if !ok {
		return EngineState{}, ErrEngineNotReady
	}
	if _, err := e.clock.Reset(state); err != nil {
		return EngineState{}, err
	}
	if cursor > state.TotalBars-1 {
		return EngineState{}, fmt.Errorf("cursor exceeds total bars: cursor=%d total=%d", cursor, state.TotalBars)
	}

	for state.Cursor < cursor {
		nextState, err := e.feed.Advance()
		if err != nil {
			return EngineState{}, err
		}
		if _, err := e.clock.Advance(nextState); err != nil {
			return EngineState{}, err
		}
		state = nextState
	}

	return e.stateLocked(state)
}

func (e *ReplayEngine) stateLocked(state CursorState) (EngineState, error) {
	clock, err := e.clock.Snapshot()
	if err != nil {
		return EngineState{}, err
	}
	if clock.Cursor != state.Cursor || clock.TotalBars != state.TotalBars || !clock.CurrentTime.Equal(state.Bar.Time) {
		return EngineState{}, fmt.Errorf("%w: clock/feed mismatch", ErrClockInvariant)
	}

	bars := e.feed.Bars()
	limit := state.Cursor + 1
	if limit < 0 || limit > len(bars) {
		return EngineState{}, fmt.Errorf("%w: visible_limit=%d bars=%d", ErrClockInvariant, limit, len(bars))
	}

	visibleBars := make([]Bar, limit)
	copy(visibleBars, bars[:limit])

	return EngineState{
		ScenarioID:    state.ScenarioID,
		Symbol:        state.Symbol,
		Interval:      state.Interval,
		Cursor:        state.Cursor,
		TotalBars:     state.TotalBars,
		RemainingBars: clock.RemainingBars,
		CurrentBar:    state.Bar,
		VisibleBars:   visibleBars,
		Clock:         clock,
	}, nil
}
