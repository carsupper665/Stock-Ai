package repo

import (
	"context"
	"errors"
	"time"

	"server/model/store"

	"gorm.io/gorm"
)

type Repository struct {
	DB *gorm.DB
}

func New(db *gorm.DB) *Repository {
	return &Repository{DB: db}
}

func (r *Repository) WithContext(ctx context.Context) *gorm.DB {
	return r.DB.WithContext(ctx)
}

func (r *Repository) FindSandbox(ctx context.Context, id string) (*store.Sandbox, error) {
	var sandbox store.Sandbox
	if err := r.WithContext(ctx).First(&sandbox, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &sandbox, nil
}

func (r *Repository) FindAccount(ctx context.Context, id string) (*store.Account, error) {
	var account store.Account
	if err := r.WithContext(ctx).First(&account, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *Repository) FindToken(ctx context.Context, id string) (*store.AccountToken, error) {
	var token store.AccountToken
	if err := r.WithContext(ctx).First(&token, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *Repository) FindUserByUsername(ctx context.Context, username string) (*store.User, error) {
	var user store.User
	if err := r.WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) FindUserByID(ctx context.Context, id uint) (*store.User, error) {
	var user store.User
	if err := r.WithContext(ctx).First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) FindDataset(ctx context.Context, id string) (*store.ReplayDataset, error) {
	var dataset store.ReplayDataset
	if err := r.WithContext(ctx).First(&dataset, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &dataset, nil
}

func (r *Repository) FindPosition(ctx context.Context, accountID, sandboxID, symbol, positionSide string) (*store.Position, error) {
	var position store.Position
	err := r.WithContext(ctx).Where("account_id = ? AND sandbox_id = ? AND symbol = ? AND position_side = ?", accountID, sandboxID, symbol, positionSide).First(&position).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &position, nil
}

func (r *Repository) ListPositionsByAccount(ctx context.Context, accountID string) ([]store.Position, error) {
	var positions []store.Position
	err := r.WithContext(ctx).Where("account_id = ?", accountID).Order("updated_at desc").Find(&positions).Error
	return positions, err
}

func (r *Repository) ListOrdersByAccount(ctx context.Context, accountID string) ([]store.Order, error) {
	var orders []store.Order
	err := r.WithContext(ctx).Where("account_id = ?", accountID).Order("created_at desc").Find(&orders).Error
	return orders, err
}

func (r *Repository) ListTradesByAccount(ctx context.Context, accountID string) ([]store.Trade, error) {
	var trades []store.Trade
	err := r.WithContext(ctx).Where("account_id = ?", accountID).Order("executed_at desc").Find(&trades).Error
	return trades, err
}

func (r *Repository) ListAccountsBySandbox(ctx context.Context, sandboxID string) ([]store.Account, error) {
	var accounts []store.Account
	err := r.WithContext(ctx).Where("sandbox_id = ?", sandboxID).Order("created_at asc").Find(&accounts).Error
	return accounts, err
}

func (r *Repository) ListPendingOrdersBySandbox(ctx context.Context, sandboxID string) ([]store.Order, error) {
	var orders []store.Order
	err := r.WithContext(ctx).Where("sandbox_id = ? AND status IN ?", sandboxID, []string{store.OrderStatusNew, store.OrderStatusTriggered}).Order("created_at asc").Find(&orders).Error
	return orders, err
}

func (r *Repository) FindReplayPrice(ctx context.Context, datasetID, symbol string, at time.Time) (*store.ReplayKline, error) {
	var kline store.ReplayKline
	err := r.WithContext(ctx).
		Where("dataset_id = ? AND symbol = ? AND ts <= ?", datasetID, symbol, at).
		Order("ts desc").
		First(&kline).Error
	if err != nil {
		return nil, err
	}
	return &kline, nil
}

func (r *Repository) Create(ctx context.Context, value any) error {
	return r.WithContext(ctx).Create(value).Error
}

func (r *Repository) Save(ctx context.Context, value any) error {
	return r.WithContext(ctx).Save(value).Error
}

func (r *Repository) Delete(ctx context.Context, value any, conds ...any) error {
	return r.WithContext(ctx).Delete(value, conds...).Error
}
