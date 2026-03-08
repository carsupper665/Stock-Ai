package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"server/model/store"
	"server/sandbox"
)

type runSessionStore interface {
	Create(context.Context, *store.RunSession) error
	Save(context.Context, *store.RunSession) error
	GetByRunID(context.Context, string) (*store.RunSession, error)
}

type scenarioBarSource interface {
	ScenarioID() string
	Bars() []sandbox.Bar
}

type RunService struct {
	runs      runSessionStore
	scenarios map[string]scenarioBarSource
	mu        sync.RWMutex
}

func NewRunService(runs runSessionStore) *RunService {
	return &RunService{
		runs:      runs,
		scenarios: make(map[string]scenarioBarSource),
	}
}

func (s *RunService) RegisterScenario(feed scenarioBarSource) error {
	if feed == nil {
		return errors.New("scenario feed is required")
	}
	scenarioID := feed.ScenarioID()
	if scenarioID == "" {
		return errors.New("scenario feed has empty scenario id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.scenarios[scenarioID] = feed
	return nil
}

func (s *RunService) CreateRun(ctx context.Context, session sandbox.RunSession) error {
	if err := session.Validate(); err != nil {
		return err
	}

	bars, err := s.barsForScenario(session.ScenarioID)
	if err != nil {
		return err
	}
	if err := ensureRangeBounds(session, len(bars)); err != nil {
		return err
	}

	record := toStoreRunSession(session)
	return s.runs.Create(ctx, &record)
}

func (s *RunService) UpdateStage(ctx context.Context, runID string, stage sandbox.RunStage, cursor int) error {
	record, err := s.runs.GetByRunID(ctx, runID)
	if err != nil {
		return err
	}

	session := toDomainRunSession(record)
	session.Stage = stage
	session.Cursor = cursor
	if err := session.Validate(); err != nil {
		return err
	}

	bars, err := s.barsForScenario(session.ScenarioID)
	if err != nil {
		return err
	}
	if err := ensureRangeBounds(session, len(bars)); err != nil {
		return err
	}

	record.Stage = string(stage)
	record.Cursor = cursor
	return s.runs.Save(ctx, record)
}

func (s *RunService) GetRun(ctx context.Context, runID string) (sandbox.RunSession, error) {
	record, err := s.runs.GetByRunID(ctx, runID)
	if err != nil {
		return sandbox.RunSession{}, err
	}
	return toDomainRunSession(record), nil
}

func (s *RunService) GetVisibleBars(ctx context.Context, runID string, start, end int) ([]sandbox.Bar, error) {
	session, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	bars, err := s.barsForScenario(session.ScenarioID)
	if err != nil {
		return nil, err
	}
	if err := sandbox.GuardBarRange(session, start, end, len(bars)); err != nil {
		return nil, err
	}

	window := make([]sandbox.Bar, end-start)
	copy(window, bars[start:end])
	return window, nil
}

func (s *RunService) CanAccessSummary(ctx context.Context, runID string, summary sandbox.SummaryKind) error {
	session, err := s.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	return sandbox.CheckSummaryAccess(session, summary)
}

func (s *RunService) barsForScenario(scenarioID string) ([]sandbox.Bar, error) {
	s.mu.RLock()
	feed, ok := s.scenarios[scenarioID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("scenario feed not registered: %s", scenarioID)
	}
	return feed.Bars(), nil
}

func ensureRangeBounds(session sandbox.RunSession, totalBars int) error {
	ranges := []struct {
		name   string
		range_ sandbox.IndexRange
	}{
		{name: "train", range_: session.TrainRange},
		{name: "validation", range_: session.ValidationRange},
		{name: "oos", range_: session.OOSRange},
	}

	for _, item := range ranges {
		if item.range_.End >= totalBars {
			return fmt.Errorf("%s range exceeds total bars: end=%d total=%d", item.name, item.range_.End, totalBars)
		}
	}
	if session.PromotionRange != nil && session.PromotionRange.End >= totalBars {
		return fmt.Errorf("promotion range exceeds total bars: end=%d total=%d", session.PromotionRange.End, totalBars)
	}
	return nil
}

func toStoreRunSession(session sandbox.RunSession) store.RunSession {
	record := store.RunSession{
		RunID:                 session.RunID,
		ScenarioID:            session.ScenarioID,
		DatasetHash:           session.DatasetHash,
		Stage:                 string(session.Stage),
		Cursor:                session.Cursor,
		TrainStart:            session.TrainRange.Start,
		TrainEnd:              session.TrainRange.End,
		ValidationStart:       session.ValidationRange.Start,
		ValidationEnd:         session.ValidationRange.End,
		OOSStart:              session.OOSRange.Start,
		OOSEnd:                session.OOSRange.End,
		ExecutionModelVersion: session.ExecutionModelVersion,
		FeeModelVersion:       session.FeeModelVersion,
		SlippageModelVersion:  session.SlippageModelVersion,
	}
	if session.PromotionRange != nil {
		start := session.PromotionRange.Start
		end := session.PromotionRange.End
		record.PromotionStart = &start
		record.PromotionEnd = &end
	}
	return record
}

func toDomainRunSession(record *store.RunSession) sandbox.RunSession {
	session := sandbox.RunSession{
		RunID:       record.RunID,
		ScenarioID:  record.ScenarioID,
		DatasetHash: record.DatasetHash,
		Stage:       sandbox.RunStage(record.Stage),
		Cursor:      record.Cursor,
		TrainRange: sandbox.IndexRange{
			Start: record.TrainStart,
			End:   record.TrainEnd,
		},
		ValidationRange: sandbox.IndexRange{
			Start: record.ValidationStart,
			End:   record.ValidationEnd,
		},
		OOSRange: sandbox.IndexRange{
			Start: record.OOSStart,
			End:   record.OOSEnd,
		},
		ExecutionModelVersion: record.ExecutionModelVersion,
		FeeModelVersion:       record.FeeModelVersion,
		SlippageModelVersion:  record.SlippageModelVersion,
	}
	if record.PromotionStart != nil && record.PromotionEnd != nil {
		session.PromotionRange = &sandbox.IndexRange{
			Start: *record.PromotionStart,
			End:   *record.PromotionEnd,
		}
	}
	return session
}
