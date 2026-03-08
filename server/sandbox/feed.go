package sandbox

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidSymbol     = errors.New("invalid symbol")
	ErrEmptyScenario     = errors.New("scenario has no bars")
	ErrScenarioNotLoaded = errors.New("scenario not loaded")
	ErrNoMoreBars        = errors.New("no more bars")
)

var symbolPattern = regexp.MustCompile(`^[A-Z0-9]{2,20}$`)

type Bar struct {
	Time   time.Time `json:"time"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

type Scenario struct {
	ID       string `json:"scenario_id"`
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Bars     []Bar  `json:"bars"`
}

type CursorState struct {
	ScenarioID string
	Symbol     string
	Interval   string
	Cursor     int
	TotalBars  int
	Bar        Bar
}

type Feed interface {
	LoadScenario(Scenario) error
	Symbol() string
	Cursor() int
	Len() int
	Current() (CursorState, bool)
	Advance() (CursorState, error)
	Reset() error
}

func NormalizeSymbol(symbol string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return "", fmt.Errorf("%w: symbol is empty", ErrInvalidSymbol)
	}
	if !symbolPattern.MatchString(normalized) {
		return "", fmt.Errorf("%w: %s", ErrInvalidSymbol, symbol)
	}
	return normalized, nil
}

func NormalizeLiveSymbol(symbol string) (string, error) {
	normalized, err := NormalizeSymbol(symbol)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(normalized, "USDT") {
		normalized += "USDT"
	}
	return normalized, nil
}

func validateScenario(s Scenario) (Scenario, error) {
	s.ID = strings.TrimSpace(s.ID)
	if s.ID == "" {
		return Scenario{}, errors.New("scenario_id is required")
	}

	symbol, err := NormalizeSymbol(s.Symbol)
	if err != nil {
		return Scenario{}, err
	}
	s.Symbol = symbol

	if len(s.Bars) == 0 {
		return Scenario{}, ErrEmptyScenario
	}

	prevTime := time.Time{}
	bars := make([]Bar, len(s.Bars))
	for i, bar := range s.Bars {
		if bar.Time.IsZero() {
			return Scenario{}, fmt.Errorf("bar %d has zero time", i)
		}
		if !prevTime.IsZero() && !bar.Time.After(prevTime) {
			return Scenario{}, fmt.Errorf("bar %d time must be strictly increasing", i)
		}
		if bar.High < bar.Low {
			return Scenario{}, fmt.Errorf("bar %d high is below low", i)
		}
		if bar.Open < bar.Low || bar.Open > bar.High {
			return Scenario{}, fmt.Errorf("bar %d open is outside range", i)
		}
		if bar.Close < bar.Low || bar.Close > bar.High {
			return Scenario{}, fmt.Errorf("bar %d close is outside range", i)
		}
		bars[i] = bar
		prevTime = bar.Time
	}
	s.Bars = bars

	return s, nil
}
