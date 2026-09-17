package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"backend/internal/database"
)

// 業務規則違反時回傳的錯誤，handler 依此對應到 HTTP 狀態碼。
var (
	ErrInvalidName    = errors.New("user_name 不可為空，長度上限 64")
	ErrInvalidBalance = errors.New("initial_balance 必須大於 0")
	ErrInvalidStatus  = errors.New("status 只能是 active 或 disabled")
	ErrNameTaken      = errors.New("user_name 已被使用")
)

const maxNameLength = 64

// Service 負責虛擬帳號的業務規則。資料存取一律經由 database.Store。
type Service struct {
	store *database.Store
}

func New(store *database.Store) *Service {
	return &Service{store: store}
}

type CreateInput struct {
	UserName       string
	InitialBalance float64
}

// Create 建立虛擬帳號，同時產生專屬的 Account Token。
// 初始餘額即為當前餘額。
func (s *Service) Create(ctx context.Context, in CreateInput) (*database.Account, error) {
	name, err := cleanName(in.UserName)
	if err != nil {
		return nil, err
	}
	if in.InitialBalance <= 0 {
		return nil, ErrInvalidBalance
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}

	acc := &database.Account{
		ID:             id,
		UserName:       name,
		Token:          token,
		InitialBalance: in.InitialBalance,
		Balance:        in.InitialBalance,
		Status:         database.AccountActive,
	}
	if err := s.store.CreateAccount(ctx, acc); err != nil {
		if errors.Is(err, database.ErrDuplicate) {
			return nil, ErrNameTaken
		}
		return nil, fmt.Errorf("建立帳號失敗: %w", err)
	}
	return acc, nil
}

func (s *Service) List(ctx context.Context) ([]database.Account, error) {
	return s.store.ListAccounts(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (*database.Account, error) {
	return s.store.AccountByID(ctx, id)
}

// UpdateInput 只帶要修改的欄位，nil 表示不動。
type UpdateInput struct {
	UserName *string
	Status   *string
}

func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*database.Account, error) {
	var userName *string
	if in.UserName != nil {
		name, err := cleanName(*in.UserName)
		if err != nil {
			return nil, err
		}
		userName = &name
	}
	var status *string
	if in.Status != nil {
		value := strings.TrimSpace(*in.Status)
		if value != database.AccountActive && value != database.AccountDisabled {
			return nil, ErrInvalidStatus
		}
		status = &value
	}

	var acc *database.Account
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, id); err != nil {
			return err
		}
		if _, err := tx.AccountByID(ctx, id); err != nil {
			return err
		}
		if err := tx.UpdateAccountFields(ctx, id, userName, status); err != nil {
			return err
		}
		var err error
		acc, err = tx.AccountByID(ctx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, database.ErrDuplicate) {
			return nil, ErrNameTaken
		}
		return nil, fmt.Errorf("更新帳號失敗: %w", err)
	}
	return acc, nil
}

// Delete 連帶刪除帳號的部位與未成交掛單。留著的話撮合引擎仍會掃到它們，每輪嘗試平倉
// 卻找不到帳號；成交紀錄與 Ledger 保留作為審計。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, id); err != nil {
			return err
		}
		if err := tx.DeleteAccountTradingState(ctx, id); err != nil {
			return err
		}
		return tx.DeleteAccount(ctx, id)
	})
}

// ResetToken 換發 Account Token，舊 token 立即失效。
func (s *Service) ResetToken(ctx context.Context, id string) (*database.Account, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	var acc *database.Account
	err = s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, id); err != nil {
			return err
		}
		if _, err := tx.AccountByID(ctx, id); err != nil {
			return err
		}
		if err := tx.UpdateAccountToken(ctx, id, token); err != nil {
			return err
		}
		var err error
		acc, err = tx.AccountByID(ctx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("換發 token 失敗: %w", err)
	}
	return acc, nil
}

func cleanName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > maxNameLength {
		return "", ErrInvalidName
	}
	return name, nil
}
