package sandbox

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrClockNotInitialized = errors.New("clock not initialized")
	ErrClockInvariant      = errors.New("clock invariant violated")
)

type ClockSnapshot struct {
	Cursor        int
	TotalBars     int
	CurrentTime   time.Time
	RemainingBars int
}

type Clock struct {
	mu          sync.RWMutex
	cursor      int
	totalBars   int
	currentTime time.Time
	initialized bool
}

func NewClock() *Clock {
	return &Clock{cursor: -1}
}

func (c *Clock) Reset(state CursorState) (ClockSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.setLocked(state)
}

func (c *Clock) Advance(state CursorState) (ClockSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.initialized {
		return ClockSnapshot{}, ErrClockNotInitialized
	}
	if state.Cursor != c.cursor+1 {
		return ClockSnapshot{}, fmt.Errorf("%w: next_cursor=%d current_cursor=%d", ErrClockInvariant, state.Cursor, c.cursor)
	}

	return c.setLocked(state)
}

func (c *Clock) Snapshot() (ClockSnapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.initialized {
		return ClockSnapshot{}, ErrClockNotInitialized
	}

	return c.snapshotLocked(), nil
}

func (c *Clock) setLocked(state CursorState) (ClockSnapshot, error) {
	if state.TotalBars <= 0 {
		return ClockSnapshot{}, fmt.Errorf("%w: total_bars=%d", ErrClockInvariant, state.TotalBars)
	}
	if state.Cursor < 0 || state.Cursor >= state.TotalBars {
		return ClockSnapshot{}, fmt.Errorf("%w: cursor=%d total_bars=%d", ErrClockInvariant, state.Cursor, state.TotalBars)
	}
	if state.Bar.Time.IsZero() {
		return ClockSnapshot{}, fmt.Errorf("%w: zero bar time", ErrClockInvariant)
	}

	c.cursor = state.Cursor
	c.totalBars = state.TotalBars
	c.currentTime = state.Bar.Time
	c.initialized = true
	return c.snapshotLocked(), nil
}

func (c *Clock) snapshotLocked() ClockSnapshot {
	remaining := c.totalBars - c.cursor - 1
	if remaining < 0 {
		remaining = 0
	}
	return ClockSnapshot{
		Cursor:        c.cursor,
		TotalBars:     c.totalBars,
		CurrentTime:   c.currentTime,
		RemainingBars: remaining,
	}
}
