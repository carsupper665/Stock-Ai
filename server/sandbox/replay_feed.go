package sandbox

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

type ReplayFeed struct {
	mu       sync.RWMutex
	scenario Scenario
	cursor   int
	loaded   bool
}

func NewReplayFeed() *ReplayFeed {
	return &ReplayFeed{cursor: -1}
}

func (f *ReplayFeed) LoadScenarioFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	return f.LoadScenarioReader(file)
}

func (f *ReplayFeed) LoadScenarioReader(r io.Reader) error {
	var scenario Scenario
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		return fmt.Errorf("decode scenario: %w", err)
	}
	return f.LoadScenario(scenario)
}

func (f *ReplayFeed) LoadScenario(s Scenario) error {
	scenario, err := validateScenario(s)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.scenario = scenario
	f.cursor = 0
	f.loaded = true
	return nil
}

func (f *ReplayFeed) ScenarioID() string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if !f.loaded {
		return ""
	}
	return f.scenario.ID
}

func (f *ReplayFeed) Symbol() string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if !f.loaded {
		return ""
	}
	return f.scenario.Symbol
}

func (f *ReplayFeed) Cursor() int {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if !f.loaded {
		return -1
	}
	return f.cursor
}

func (f *ReplayFeed) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if !f.loaded {
		return 0
	}
	return len(f.scenario.Bars)
}

func (f *ReplayFeed) Bars() []Bar {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if !f.loaded {
		return nil
	}

	bars := make([]Bar, len(f.scenario.Bars))
	copy(bars, f.scenario.Bars)
	return bars
}

func (f *ReplayFeed) Current() (CursorState, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if !f.loaded || f.cursor < 0 || f.cursor >= len(f.scenario.Bars) {
		return CursorState{}, false
	}

	return f.currentLocked(), true
}

func (f *ReplayFeed) Advance() (CursorState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.loaded {
		return CursorState{}, ErrScenarioNotLoaded
	}
	if f.cursor >= len(f.scenario.Bars)-1 {
		return CursorState{}, ErrNoMoreBars
	}

	f.cursor++
	return f.currentLocked(), nil
}

func (f *ReplayFeed) Reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.loaded {
		return ErrScenarioNotLoaded
	}

	f.cursor = 0
	return nil
}

func (f *ReplayFeed) currentLocked() CursorState {
	bar := f.scenario.Bars[f.cursor]
	return CursorState{
		ScenarioID: f.scenario.ID,
		Symbol:     f.scenario.Symbol,
		Interval:   f.scenario.Interval,
		Cursor:     f.cursor,
		TotalBars:  len(f.scenario.Bars),
		Bar:        bar,
	}
}
