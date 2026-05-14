package service

import (
	"context"
	"time"

	"server/domain"
	"server/model/repo"
	"server/model/store"
)

type MonitorService struct {
	repo      *repo.Repository
	sandboxes *SandboxService
	datasets  *DatasetService
	live      *LiveMarketProvider
	clock     domain.Clock
}

type SandboxSnapshot struct {
	Sandbox       *store.Sandbox       `json:"sandbox"`
	ReplayControl *ReplayControlStatus `json:"replay_control,omitempty"`
	Dataset       *DatasetSummary      `json:"dataset,omitempty"`
	Accounts      []store.Account      `json:"accounts"`
	Orders        []store.Order        `json:"orders"`
	Trades        []store.Trade        `json:"trades"`
	Positions     []store.Position     `json:"positions"`
	FreshnessAt   time.Time            `json:"freshness_at"`
}

type LiveSymbolsSnapshot struct {
	Summary LiveSymbolsSummary  `json:"summary"`
	Items   []LivePriceSnapshot `json:"items"`
}

func NewMonitorService(repo *repo.Repository, sandboxes *SandboxService, datasets *DatasetService, live *LiveMarketProvider, clock domain.Clock) *MonitorService {
	return &MonitorService{repo: repo, sandboxes: sandboxes, datasets: datasets, live: live, clock: clock}
}

func (s *MonitorService) Snapshot(ctx context.Context, sandboxID string) (*SandboxSnapshot, error) {
	sandbox, err := s.sandboxes.Get(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	accounts, err := s.repo.ListAccountsBySandbox(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	var orders []store.Order
	if err := s.repo.WithContext(ctx).Where("sandbox_id = ?", sandboxID).Order("created_at desc").Find(&orders).Error; err != nil {
		return nil, err
	}
	var trades []store.Trade
	if err := s.repo.WithContext(ctx).Where("sandbox_id = ?", sandboxID).Order("executed_at desc").Find(&trades).Error; err != nil {
		return nil, err
	}
	var positions []store.Position
	if err := s.repo.WithContext(ctx).Where("sandbox_id = ?", sandboxID).Order("updated_at desc").Find(&positions).Error; err != nil {
		return nil, err
	}

	var dataset *DatasetSummary
	if sandbox.DatasetID != "" && s.datasets != nil {
		dataset, err = s.datasets.Get(ctx, sandbox.DatasetID)
		if err != nil {
			return nil, err
		}
	}

	freshnessAt := time.Now().UTC()
	if s.clock != nil {
		freshnessAt = s.clock.Now()
	}

	return &SandboxSnapshot{
		Sandbox:       sandbox,
		ReplayControl: replayControlStatus(*sandbox, sandbox.ReplayCurrentTime),
		Dataset:       dataset,
		Accounts:      accounts,
		Orders:        orders,
		Trades:        trades,
		Positions:     positions,
		FreshnessAt:   freshnessAt,
	}, nil
}

func (s *MonitorService) LiveSymbols(ctx context.Context) (*LiveSymbolsSnapshot, error) {
	if s.live == nil {
		return &LiveSymbolsSnapshot{}, nil
	}
	items, summary := s.live.SnapshotList()
	return &LiveSymbolsSnapshot{Summary: summary, Items: items}, nil
}

func MapError(err error) *domain.AppError {
	if appErr, ok := err.(*domain.AppError); ok {
		return appErr
	}
	return domain.NewError(500, "INTERNAL_ERROR", err.Error(), nil)
}
