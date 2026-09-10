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
