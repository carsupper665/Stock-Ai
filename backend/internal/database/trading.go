package database

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Tx 在一個交易中執行 fn，傳進去的 Store 綁在該交易上。
// 下單會同時動到餘額、部位、訂單與成交，必須一起成功或一起失敗。
func (s *Store) Tx(ctx context.Context, fn func(*Store) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&Store{db: tx})
	})
}

// LockAccount 是每個交易 mutation 交易的第一個敘述：鎖住帳號列，讓同一帳號的
// 成交、撤單、平倉與 stops 變更序列化，之後在交易內讀到的訂單／部位／餘額才是最新的
// （先鎖再讀）。postgres 用 SELECT ... FOR UPDATE；sqlite 不支援該語法，而 Open／OpenSQLite
// 都只開一條連線，交易本來就序列化，因此不發出任何 SQL。
func (s *Store) LockAccount(ctx context.Context, accountID string) error {
	if s.db.Dialector.Name() != "postgres" {
		return nil
	}
	var account Account
	err := s.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ?", accountID).First(&account).Error
	return translate(err)
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

// OpenLimitOrders 回傳所有帳號的未成交限價單，供撮合引擎掃描。
func (s *Store) OpenLimitOrders(ctx context.Context) ([]Order, error) {
	var orders []Order
	err := s.db.WithContext(ctx).
		Where("status = ? AND type = ?", OrderOpen, OrderLimit).
		Order("created_at asc").Find(&orders).Error
	return orders, translate(err)
}

// PositionsWithStops 回傳所有設定了停損或停利的部位。
func (s *Store) PositionsWithStops(ctx context.Context) ([]Position, error) {
	var positions []Position
	err := s.db.WithContext(ctx).
		Where("stop_loss > 0 OR take_profit > 0").
		Find(&positions).Error
	return positions, translate(err)
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

// UpdatePositionStops 只改停損停利及其來源；部位已不存在時回傳 ErrNotFound 而不新建。
func (s *Store) UpdatePositionStops(ctx context.Context, position *Position) error {
	result := s.db.WithContext(ctx).Model(&Position{}).Where("id = ?", position.ID).Updates(map[string]any{
		"stop_loss": position.StopLoss, "take_profit": position.TakeProfit,
		"stop_loss_source_session_id": position.StopLossSource.SessionID, "stop_loss_source_run_id": position.StopLossSource.RunID,
		"take_profit_source_session_id": position.TakeProfitSource.SessionID, "take_profit_source_run_id": position.TakeProfitSource.RunID,
	})
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeletePosition(ctx context.Context, id string) error {
	return translate(s.db.WithContext(ctx).Delete(&Position{}, "id = ?", id).Error)
}

// DeleteAccountTradingState 刪除帳號還活著的交易狀態：部位與未成交掛單。已成交訂單、
// 成交紀錄與 Ledger 留著當審計紀錄；它們不會被撮合引擎掃到，不影響後續運作。
func (s *Store) DeleteAccountTradingState(ctx context.Context, accountID string) error {
	if err := s.db.WithContext(ctx).Where("account_id = ?", accountID).Delete(&Position{}).Error; err != nil {
		return translate(err)
	}
	return translate(s.db.WithContext(ctx).
		Where("account_id = ? AND status = ?", accountID, OrderOpen).Delete(&Order{}).Error)
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

func (s *Store) CreateLedgerEntry(ctx context.Context, entry *LedgerEntry) error {
	return translate(s.db.WithContext(ctx).Create(entry).Error)
}

// LedgerFilter 的零值表示不過濾；BeforeSeq 是「只取 seq 更小」的分頁游標。
type LedgerFilter struct {
	SessionID string
	RunID     int64
	BeforeSeq int64
	Limit     int
}

// ListLedger 依 seq 由新到舊回傳帳戶事件。
func (s *Store) ListLedger(ctx context.Context, accountID string, filter LedgerFilter) ([]LedgerEntry, error) {
	query := s.db.WithContext(ctx).Where("account_id = ?", accountID)
	if filter.SessionID != "" {
		query = query.Where("source_session_id = ?", filter.SessionID)
	}
	if filter.RunID > 0 {
		query = query.Where("source_run_id = ?", filter.RunID)
	}
	if filter.BeforeSeq > 0 {
		query = query.Where("seq < ?", filter.BeforeSeq)
	}
	var entries []LedgerEntry
	err := query.Order("seq desc").Limit(filter.Limit).Find(&entries).Error
	return entries, translate(err)
}
