package service

import (
    "context"

    "server/domain"
    "server/model/repo"
    "server/model/store"
)

type MonitorService struct {
    repo      *repo.Repository
    sandboxes *SandboxService
}

type SandboxSnapshot struct {
    Sandbox   *store.Sandbox  `json:"sandbox"`
    Accounts  []store.Account `json:"accounts"`
    Orders    []store.Order   `json:"orders"`
    Trades    []store.Trade   `json:"trades"`
    Positions []store.Position `json:"positions"`
}

func NewMonitorService(repo *repo.Repository, sandboxes *SandboxService) *MonitorService {
    return &MonitorService{repo: repo, sandboxes: sandboxes}
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
    return &SandboxSnapshot{Sandbox: sandbox, Accounts: accounts, Orders: orders, Trades: trades, Positions: positions}, nil
}

func MapError(err error) *domain.AppError {
    if appErr, ok := err.(*domain.AppError); ok {
        return appErr
    }
    return domain.NewError(500, "INTERNAL_ERROR", err.Error(), nil)
}

