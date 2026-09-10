package database

import (
	"context"

	"gorm.io/gorm"
)

// Tx 在一個交易中執行 fn，傳進去的 Store 綁在該交易上。
// 下單會同時動到餘額、部位、訂單與成交，必須一起成功或一起失敗。
func (s *Store) Tx(ctx context.Context, fn func(*Store) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&Store{db: tx})
	})
}

func (s *Store) CreateOrder(ctx context.Context, order *Order) error {
	return translate(s.db.WithContext(ctx).Create(order).Error)
}

func (s *Store) SaveOrder(ctx context.Context, order *Order) error {
	return translate(s.db.WithContext(ctx).Save(order).Error)
}

func (s *Store) OrderByID(ctx context.Context, accountID, orderID string) (*Order, error) {
	var order Order
	err := s.db.WithContext(ctx).Where("account_id = ? AND id = ?", accountID, orderID).First(&order).Error
	if err != nil {
		return nil, translate(err)
	}
	return &order, nil
}

// ListOrders 依建立時間由新到舊回傳訂單，status 為空時不過濾。
func (s *Store) ListOrders(ctx context.Context, accountID, status string, limit int) ([]Order, error) {
	query := s.db.WithContext(ctx).Where("account_id = ?", accountID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var orders []Order
	err := query.Order("created_at desc, id desc").Limit(limit).Find(&orders).Error
	return orders, translate(err)
}

func (s *Store) PositionFor(ctx context.Context, accountID, market, symbol, product string) (*Position, error) {
	var position Position
	err := s.db.WithContext(ctx).
		Where("account_id = ? AND market = ? AND symbol = ? AND product = ?", accountID, market, symbol, product).
		First(&position).Error
	if err != nil {
		return nil, translate(err)
	}
	return &position, nil
}

func (s *Store) PositionByID(ctx context.Context, accountID, positionID string) (*Position, error) {
	var position Position
	err := s.db.WithContext(ctx).Where("account_id = ? AND id = ?", accountID, positionID).First(&position).Error
	if err != nil {
		return nil, translate(err)
	}
	return &position, nil
}

// ListPositions 回傳未平倉部位，product 為空時不過濾。
func (s *Store) ListPositions(ctx context.Context, accountID, product string) ([]Position, error) {
	query := s.db.WithContext(ctx).Where("account_id = ?", accountID)
	if product != "" {
		query = query.Where("product = ?", product)
	}
	var positions []Position
	err := query.Order("created_at asc").Find(&positions).Error
	return positions, translate(err)
}

func (s *Store) CreatePosition(ctx context.Context, position *Position) error {
	return translate(s.db.WithContext(ctx).Create(position).Error)
}

func (s *Store) SavePosition(ctx context.Context, position *Position) error {
	return translate(s.db.WithContext(ctx).Save(position).Error)
}

func (s *Store) DeletePosition(ctx context.Context, id string) error {
	return translate(s.db.WithContext(ctx).Delete(&Position{}, "id = ?", id).Error)
}

// LockedMargin 是帳號目前被未平倉部位佔用的保證金總額。
func (s *Store) LockedMargin(ctx context.Context, accountID string) (float64, error) {
	var total *float64
	err := s.db.WithContext(ctx).Model(&Position{}).
		Where("account_id = ?", accountID).
		Select("sum(margin)").Scan(&total).Error
	if err != nil {
		return 0, translate(err)
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}

func (s *Store) CreateTrade(ctx context.Context, trade *Trade) error {
	return translate(s.db.WithContext(ctx).Create(trade).Error)
}

func (s *Store) ListTrades(ctx context.Context, accountID string, limit int) ([]Trade, error) {
	var trades []Trade
	err := s.db.WithContext(ctx).Where("account_id = ?", accountID).
		Order("created_at desc, id desc").Limit(limit).Find(&trades).Error
	return trades, translate(err)
}
