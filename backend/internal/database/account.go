package database

import "context"

func (s *Store) CreateAccount(ctx context.Context, account *Account) error {
	return translate(s.db.WithContext(ctx).Create(account).Error)
}

func (s *Store) AccountByID(ctx context.Context, id string) (*Account, error) {
	return s.accountWhere(ctx, "id = ?", id)
}

// AccountByToken 用於驗證 Account Token。
func (s *Store) AccountByToken(ctx context.Context, token string) (*Account, error) {
	return s.accountWhere(ctx, "token = ?", token)
}

func (s *Store) accountWhere(ctx context.Context, query string, args ...any) (*Account, error) {
	var account Account
	if err := s.db.WithContext(ctx).Where(query, args...).First(&account).Error; err != nil {
		return nil, translate(err)
	}
	return &account, nil
}

// ListAccounts 依建立時間由新到舊回傳所有帳號。
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	var accounts []Account
	if err := s.db.WithContext(ctx).Order("created_at desc").Find(&accounts).Error; err != nil {
		return nil, translate(err)
	}
	return accounts, nil
}

func (s *Store) SaveAccount(ctx context.Context, account *Account) error {
	return translate(s.db.WithContext(ctx).Save(account).Error)
}

// UpdateAccountFields only writes fields owned by Account administration. Trading balance and
// token rotation are separate mutations and must not be overwritten by a stale Account value.
func (s *Store) UpdateAccountFields(ctx context.Context, id string, userName, status *string) error {
	fields := make(map[string]any, 2)
	if userName != nil {
		fields["user_name"] = *userName
	}
	if status != nil {
		fields["status"] = *status
	}
	if len(fields) == 0 {
		return nil
	}
	result := s.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(fields)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateAccountToken(ctx context.Context, id, token string) error {
	result := s.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Update("token", token)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAccount 永久刪除帳號。找不到時回傳 ErrNotFound。
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Delete(&Account{}, "id = ?", id)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
