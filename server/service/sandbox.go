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
	repo      *repo.Repository
	clock     domain.Clock
	events    domain.EventPublisher
	processor func(context.Context, string) error
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

type ReplayControlStatus struct {
	SandboxID   string    `json:"sandbox_id"`
	CurrentTime time.Time `json:"current_time"`
	Speed       float64   `json:"speed"`
	Status      string    `json:"status"`
}

func (s *SandboxService) SetProcessor(fn func(context.Context, string) error) {
	s.processor = fn
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
	if input.ReplayCurrentTime != nil || input.ReplaySpeed != nil {
		return nil, domain.ValidationError("REPLAY_CONTROL_REQUIRES_DEDICATED_ENDPOINT", "use replay control endpoints for time and speed changes")
	}
	shouldProcess := false
	if input.Name != nil {
		sandbox.Name = *input.Name
	}
	if input.DatasetID != nil && *input.DatasetID != "" {
		if _, err := s.repo.FindDataset(ctx, *input.DatasetID); err != nil {
			return nil, domain.NotFoundError("DATASET_NOT_FOUND", "replay dataset not found")
		}
		sandbox.DatasetID = *input.DatasetID
		shouldProcess = true
	}
	if input.Status != nil {
		sandbox.Status = *input.Status
	}
	if err := s.repo.Save(ctx, sandbox); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.updated", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"status": sandbox.Status, "dataset_id": sandbox.DatasetID}})
	if shouldProcess {
		if err := s.process(ctx, sandbox.ID); err != nil {
			return nil, err
		}
	}
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
	if err := s.process(ctx, sandbox.ID); err != nil {
		return nil, err
	}
	return sandbox, nil
}

func (s *SandboxService) Resume(ctx context.Context, sandboxID string) (*ReplayControlStatus, error) {
	sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
	if err != nil {
		return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
	}
	if sandbox.Status != store.SandboxStatusPaused && sandbox.Status != store.SandboxStatusReady {
		return nil, domain.ConflictError("SANDBOX_NOT_RESUMABLE", "sandbox is not paused")
	}
	current := currentReplayTime(*sandbox, s.clock.Now())
	now := s.clock.Now()
	sandbox.ReplayCurrentTime = current
	sandbox.RuntimeAnchorAt = &now
	sandbox.Status = store.SandboxStatusRunning
	if err := s.repo.Save(ctx, sandbox); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.replay.resume", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"status": sandbox.Status}})
	if err := s.process(ctx, sandbox.ID); err != nil {
		return nil, err
	}
	return replayControlStatus(*sandbox, current), nil
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

func (s *SandboxService) Seek(ctx context.Context, sandboxID string, at time.Time) (*ReplayControlStatus, error) {
	sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
	if err != nil {
		return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
	}
	if at.IsZero() {
		return nil, domain.ValidationError("INVALID_REPLAY_TIME", "replay_current_time is required")
	}
	materialized := at.UTC()
	sandbox.ReplayCurrentTime = materialized
	if sandbox.Status == store.SandboxStatusRunning {
		now := s.clock.Now()
		sandbox.RuntimeAnchorAt = &now
	} else {
		sandbox.RuntimeAnchorAt = nil
	}
	if err := s.repo.Save(ctx, sandbox); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.replay.seek", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"replay_current_time": materialized}})
	if err := s.process(ctx, sandbox.ID); err != nil {
		return nil, err
	}
	return replayControlStatus(*sandbox, materialized), nil
}

func (s *SandboxService) SetReplaySpeed(ctx context.Context, sandboxID string, speed float64) (*ReplayControlStatus, error) {
	sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
	if err != nil {
		return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
	}
	if speed <= 0 {
		return nil, domain.ValidationError("INVALID_REPLAY_SPEED", "replay speed must be positive")
	}
	current := currentReplayTime(*sandbox, s.clock.Now())
	sandbox.ReplayCurrentTime = current
	sandbox.ReplaySpeed = speed
	if sandbox.Status == store.SandboxStatusRunning {
		now := s.clock.Now()
		sandbox.RuntimeAnchorAt = &now
	} else {
		sandbox.RuntimeAnchorAt = nil
	}
	if err := s.repo.Save(ctx, sandbox); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "sandbox.replay.speed", SandboxID: sandbox.ID, AggregateID: sandbox.ID, Payload: map[string]any{"replay_speed": speed}})
	if err := s.process(ctx, sandbox.ID); err != nil {
		return nil, err
	}
	return replayControlStatus(*sandbox, current), nil
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
	if _, err := s.Seek(ctx, sandboxID, at); err != nil {
		return nil, err
	}
	return s.Get(ctx, sandboxID)
}

func (s *SandboxService) process(ctx context.Context, sandboxID string) error {
	if s.processor == nil {
		return nil
	}
	return s.processor(ctx, sandboxID)
}

func replayControlStatus(sandbox store.Sandbox, current time.Time) *ReplayControlStatus {
	return &ReplayControlStatus{
		SandboxID:   sandbox.ID,
		CurrentTime: current.UTC(),
		Speed:       sandbox.ReplaySpeed,
		Status:      sandbox.Status,
	}
}
