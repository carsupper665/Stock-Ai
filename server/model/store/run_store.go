package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type RunSession struct {
	RunID                 string `gorm:"primaryKey;size:64" json:"run_id"`
	ScenarioID            string `gorm:"size:128;index;not null" json:"scenario_id"`
	DatasetHash           string `gorm:"size:128;not null" json:"dataset_hash"`
	Stage                 string `gorm:"size:32;index;not null" json:"stage"`
	Cursor                int    `gorm:"not null" json:"cursor"`
	TrainStart            int    `gorm:"not null" json:"train_start"`
	TrainEnd              int    `gorm:"not null" json:"train_end"`
	ValidationStart       int    `gorm:"not null" json:"validation_start"`
	ValidationEnd         int    `gorm:"not null" json:"validation_end"`
	OOSStart              int    `gorm:"not null" json:"oos_start"`
	OOSEnd                int    `gorm:"not null" json:"oos_end"`
	PromotionStart        *int   `json:"promotion_start"`
	PromotionEnd          *int   `json:"promotion_end"`
	ExecutionModelVersion string `gorm:"size:64;not null" json:"execution_model_version"`
	FeeModelVersion       string `gorm:"size:64;not null" json:"fee_model_version"`
	SlippageModelVersion  string `gorm:"size:64;not null" json:"slippage_model_version"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type RunSessionStore struct {
	db *gorm.DB
}

func NewRunSessionStore(db *gorm.DB) *RunSessionStore {
	return &RunSessionStore{db: db}
}

func (s *RunSessionStore) Create(ctx context.Context, session *RunSession) error {
	return s.db.WithContext(ctx).Create(session).Error
}

func (s *RunSessionStore) Save(ctx context.Context, session *RunSession) error {
	return s.db.WithContext(ctx).Save(session).Error
}

func (s *RunSessionStore) GetByRunID(ctx context.Context, runID string) (*RunSession, error) {
	var session RunSession
	if err := s.db.WithContext(ctx).Where("run_id = ?", runID).First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}
