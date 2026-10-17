package account

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"backend/internal/database"
)

// 業務規則違反時回傳的錯誤，handler 依此對應到 HTTP 狀態碼。
var (
	ErrInvalidName    = errors.New("user_name 不可為空，長度上限 64")
	ErrInvalidBalance = errors.New("initial_balance 必須大於 0")
	ErrInvalidStatus  = errors.New("status 只能是 active 或 disabled")
	ErrInvalidMaxDD   = errors.New("max_dd 必須是 0 與 100 之間（含 100）的有限數值")
	ErrNameTaken      = errors.New("user_name 已被使用")
	// ErrTradingNotStopped 是解除停止時帳號未處於 max_dd 停止狀態（未停止、
	// 未啟用 max_dd、或已經解除過）：重試不會再移動 baseline。
	ErrTradingNotStopped = errors.New("帳號未處於 max_dd 風控停止狀態，不需要解除")
	// ErrLiquidationPending 是已停止但仍有部位或掛單：強平未完成，不能解除。
	ErrLiquidationPending = errors.New("尚有部位或未成交掛單，強平未完成前不能解除停止")
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
	// MaxDD 是選填的百分比；nil 代表未設定，建立後維持未啟用。
	MaxDD *float64
}

// Create 建立虛擬帳號，同時產生專屬的 Account Token。
// 初始餘額即為當前餘額；設定 max_dd 時以 initial_balance 為第一輪 baseline。
func (s *Service) Create(ctx context.Context, in CreateInput) (*database.Account, error) {
	name, err := cleanName(in.UserName)
	if err != nil {
		return nil, err
	}
	if in.InitialBalance <= 0 {
		return nil, ErrInvalidBalance
	}
	var maxDD, baseline *float64
	if in.MaxDD != nil {
		if err := validateMaxDD(*in.MaxDD); err != nil {
			return nil, err
		}
		maxDD = in.MaxDD
		base := in.InitialBalance
		baseline = &base
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
		ID:                  id,
		UserName:            name,
		Token:               token,
		InitialBalance:      in.InitialBalance,
		Balance:             in.InitialBalance,
		Status:              database.AccountActive,
		MaxDD:               maxDD,
		MaxDDBaselineEquity: baseline,
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

// ResumeTrading 是 USER 明確解除 max_dd 風控停止（規格 §2）：帳號必須已停止、
// 且部位與掛單都已清完（強平完成）；row-lock 內重讀驗證後，把 baseline 重設為
// 全平後 equity（即當前 Balance）、清除停止標記、管理狀態設回 active，並寫一筆
// max_dd_resumed Ledger，同 transaction 提交或回滾。max_dd、initial_balance、
// Balance 與 Token 都不改。兩個並行解除只有第一個成功；第二個看到標記已清，
// 回 ErrTradingNotStopped，不會再次移動 baseline。
func (s *Service) ResumeTrading(ctx context.Context, id string) (*database.Account, error) {
	var acc *database.Account
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, id); err != nil {
			return err
		}
		account, err := tx.AccountByID(ctx, id)
		if err != nil {
			return err
		}
		if account.MaxDD == nil || account.MaxDDBaselineEquity == nil || account.MaxDDStoppedAt == nil {
			return ErrTradingNotStopped
		}
		positions, err := tx.ListPositions(ctx, id, "")
		if err != nil {
			return err
		}
		if len(positions) > 0 {
			return ErrLiquidationPending
		}
		orders, err := tx.OpenOrdersForAccount(ctx, id)
		if err != nil {
			return err
		}
		if len(orders) > 0 {
			return ErrLiquidationPending
		}
		base := account.Balance
		account.MaxDDBaselineEquity = &base
		account.MaxDDStoppedAt = nil
		account.Status = database.AccountActive
		if err := tx.SaveAccount(ctx, account); err != nil {
			return err
		}
		if err := tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
			AccountID:    id,
			Event:        database.LedgerMaxDDResumed,
			BalanceAfter: account.Balance,
		}); err != nil {
			return err
		}
		acc = account
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrTradingNotStopped) || errors.Is(err, ErrLiquidationPending) {
			return nil, err
		}
		return nil, fmt.Errorf("解除風控停止失敗: %w", err)
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

// validateMaxDD 只接受有限且落在 (0, 100] 的百分比。
// 0 不是「停用」、不是「未設定」，一樣拒絕。
func validateMaxDD(value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 100 {
		return ErrInvalidMaxDD
	}
	return nil
}
