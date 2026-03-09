package service

import (
    "context"
    "time"

    "server/domain"
    "server/model/repo"
    "server/model/store"

    "gorm.io/gorm"
)

type SandboxService struct {
    repo   *repo.Repository
    clock  domain.Clock
    events domain.EventPublisher
}

func NewSandboxService(repo *repo.Repository, clock domain.Clock, events domain.EventPublisher) *SandboxService {
    return &SandboxService{repo: repo, clock: clock, events: events}
}

type CreateSandboxInput struct {
    ID                string    `json:"id"`
    Name              string    `json:"name"`
    StartDatetime     time.Time `json:"start_datetime"`
    ReplayCurrentTime time.Time `json:"replay_current_time"`
    ReplaySpeed       float64   `json:"replay_speed"`
    DatasetID         string    `json:"dataset_id"`
}

type UpdateSandboxInput struct {
    Name              *string    `json:"name,omitempty"`
    ReplayCurrentTime *time.Time `json:"replay_current_time,omitempty"`
    ReplaySpeed       *float64   `json:"replay_speed,omitempty"`
    DatasetID         *string    `json:"dataset_id,omitempty"`
    Status            *string    `json:"status,omitempty"`
}

func (s *SandboxService) Create(ctx context.Context, input CreateSandboxInput) (*store.Sandbox, error) {
    if input.ID == "" {
        input.ID = newID("sbx")
    }
    if input.Name == "" {
        return nil, domain.ValidationError("INVALID_SANDBOX_NAME", "sandbox name is required")
    }
    if input.DatasetID == "" {
        return nil, domain.ValidationError("INVALID_DATASET_ID", "dataset_id is required")
    }
    if _, err := s.repo.FindDataset(ctx, input.DatasetID); err != nil {
        return nil, domain.NotFoundError("DATASET_NOT_FOUND", "replay dataset not found")
    }
    if input.ReplaySpeed <= 0 {
        input.ReplaySpeed = 1
    }
    if input.StartDatetime.IsZero() {
        input.StartDatetime = s.clock.Now()
    }
    if input.ReplayCurrentTime.IsZero() {
        input.ReplayCurrentTime = input.StartDatetime
    }

    sandbox := &store.Sandbox{
        ID:                input.ID,
        Name:              input.Name,
        Mode:              store.SandboxModeReplay,
        Status:            store.SandboxStatusReady,
        StartDatetime:     input.StartDatetime.UTC(),
        ReplayCurrentTime: input.ReplayCurrentTime.UTC(),
        ReplaySpeed:       input.ReplaySpeed,
        DatasetID:         input.DatasetID,
    }
    if err := s.repo.Create(ctx, sandbox); err != nil {
        return nil, err
    }
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.updated", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"status": sandbox.Status}})
    return sandbox, nil
}

func (s *SandboxService) Get(ctx context.Context, sandboxID string) (*store.Sandbox, error) {
    sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
    if err != nil {
        return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
    }
    copy := *sandbox
    copy.ReplayCurrentTime = currentReplayTime(copy, s.clock.Now())
    return &copy, nil
}

func (s *SandboxService) List(ctx context.Context) ([]store.Sandbox, error) {
    var sandboxes []store.Sandbox
    err := s.repo.WithContext(ctx).Order("created_at desc").Find(&sandboxes).Error
    if err != nil {
        return nil, err
    }
    now := s.clock.Now()
    for i := range sandboxes {
        sandboxes[i].ReplayCurrentTime = currentReplayTime(sandboxes[i], now)
    }
    return sandboxes, nil
}

func (s *SandboxService) Update(ctx context.Context, sandboxID string, input UpdateSandboxInput) (*store.Sandbox, error) {
    sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
    if err != nil {
        return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
    }
    if input.Name != nil {
        sandbox.Name = *input.Name
    }
    if input.ReplaySpeed != nil && *input.ReplaySpeed > 0 {
        sandbox.ReplaySpeed = *input.ReplaySpeed
    }
    if input.DatasetID != nil && *input.DatasetID != "" {
        sandbox.DatasetID = *input.DatasetID
    }
    if input.ReplayCurrentTime != nil {
        materialized := input.ReplayCurrentTime.UTC()
        sandbox.ReplayCurrentTime = materialized
        now := s.clock.Now()
        sandbox.RuntimeAnchorAt = &now
    }
    if input.Status != nil {
        sandbox.Status = *input.Status
    }
    if err := s.repo.Save(ctx, sandbox); err != nil {
        return nil, err
    }
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.updated", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"status": sandbox.Status, "replay_current_time": sandbox.ReplayCurrentTime}})
    return sandbox, nil
}

func (s *SandboxService) Start(ctx context.Context, sandboxID string) (*store.Sandbox, error) {
    sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
    if err != nil {
        return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
    }
    current := currentReplayTime(*sandbox, s.clock.Now())
    now := s.clock.Now()
    sandbox.ReplayCurrentTime = current
    sandbox.RuntimeAnchorAt = &now
    sandbox.Status = store.SandboxStatusRunning
    if err := s.repo.Save(ctx, sandbox); err != nil {
        return nil, err
    }
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.updated", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"status": sandbox.Status}})
    return sandbox, nil
}

func (s *SandboxService) Pause(ctx context.Context, sandboxID string) (*store.Sandbox, error) {
    sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
    if err != nil {
        return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
    }
    current := currentReplayTime(*sandbox, s.clock.Now())
    sandbox.ReplayCurrentTime = current
    sandbox.RuntimeAnchorAt = nil
    sandbox.Status = store.SandboxStatusPaused
    if err := s.repo.Save(ctx, sandbox); err != nil {
        return nil, err
    }
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.updated", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"status": sandbox.Status}})
    return sandbox, nil
}

func (s *SandboxService) Stop(ctx context.Context, sandboxID string) (*store.Sandbox, error) {
    sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
    if err != nil {
        return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
    }
    current := currentReplayTime(*sandbox, s.clock.Now())
    sandbox.ReplayCurrentTime = current
    sandbox.RuntimeAnchorAt = nil
    sandbox.Status = store.SandboxStatusStopped
    if err := s.repo.Save(ctx, sandbox); err != nil {
        return nil, err
    }
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.updated", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"status": sandbox.Status}})
    return sandbox, nil
}

func (s *SandboxService) Delete(ctx context.Context, sandboxID string) error {
    return s.repo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        var sandbox store.Sandbox
        if err := tx.First(&sandbox, "id = ?", sandboxID).Error; err != nil {
            return domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
        }
        var positions int64
        if err := tx.Model(&store.Position{}).Where("sandbox_id = ?", sandboxID).Count(&positions).Error; err != nil {
            return err
        }
        if positions > 0 {
            return domain.ConflictError("SANDBOX_HAS_POSITIONS", "sandbox still has open positions")
        }
        var accountIDs []string
        if err := tx.Model(&store.Account{}).Where("sandbox_id = ?", sandboxID).Pluck("id", &accountIDs).Error; err != nil {
            return err
        }
        if len(accountIDs) > 0 {
            if err := tx.Where("account_id IN ?", accountIDs).Delete(&store.AccountToken{}).Error; err != nil {
                return err
            }
            if err := tx.Where("account_id IN ?", accountIDs).Delete(&store.Order{}).Error; err != nil {
                return err
            }
            if err := tx.Where("account_id IN ?", accountIDs).Delete(&store.Trade{}).Error; err != nil {
                return err
            }
            if err := tx.Where("id IN ?", accountIDs).Delete(&store.Account{}).Error; err != nil {
                return err
            }
        }
        if err := tx.Where("sandbox_id = ?", sandboxID).Delete(&store.Position{}).Error; err != nil {
            return err
        }
        if err := tx.Delete(&sandbox).Error; err != nil {
            return err
        }
        return nil
    })
}

func (s *SandboxService) UpdateReplayTime(ctx context.Context, sandboxID string, at time.Time) (*store.Sandbox, error) {
    return s.Update(ctx, sandboxID, UpdateSandboxInput{ReplayCurrentTime: &at})
}
