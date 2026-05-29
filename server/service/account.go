package service

import (
	"context"

	"server/domain"
	"server/model/repo"
	"server/model/store"

	"gorm.io/gorm"
)

type AccountService struct {
	repo *repo.Repository
}

func NewAccountService(repo *repo.Repository) *AccountService {
	return &AccountService{repo: repo}
}

type CreateAccountInput struct {
	SandboxID         string   `json:"sandbox_id"`
	Name              string   `json:"name"`
	InitialBalance    float64  `json:"initial_balance"`
	Type              string   `json:"type"`
	Provider          string   `json:"provider,omitempty"`
	Environment       string   `json:"environment,omitempty"`
	PriceMode         string   `json:"price_mode,omitempty"`
	CredentialsStatus string   `json:"credentials_status,omitempty"`
	SupportedSymbols  []string `json:"supported_symbols,omitempty"`
}

type UpdateAccountInput struct {
	Name               *string   `json:"name,omitempty"`
	Status             *string   `json:"status,omitempty"`
	Provider           *string   `json:"provider,omitempty"`
	Environment        *string   `json:"environment,omitempty"`
	PriceMode          *string   `json:"price_mode,omitempty"`
	CredentialsStatus  *string   `json:"credentials_status,omitempty"`
	SupportedSymbols   *[]string `json:"supported_symbols,omitempty"`
	LiveTradingEnabled *bool     `json:"live_trading_enabled,omitempty"`
}

func (s *AccountService) Create(ctx context.Context, input CreateAccountInput) (*store.Account, error) {
	if input.Name == "" {
		return nil, domain.ValidationError("INVALID_ACCOUNT_NAME", "account name is required")
	}
	if input.InitialBalance <= 0 {
		return nil, domain.ValidationError("INVALID_INITIAL_BALANCE", "initial balance must be positive")
	}
	accountType := input.Type
	if accountType == "" {
		accountType = store.AccountTypeVirtual
	}
	account := &store.Account{
		ID:                newID("acct"),
		Name:              input.Name,
		Type:              accountType,
		Provider:          input.Provider,
		Environment:       input.Environment,
		PriceMode:         input.PriceMode,
		CredentialsStatus: input.CredentialsStatus,
		SupportedSymbols:  store.StringList(input.SupportedSymbols),
		BaseCurrency:      "USD",
		InitialBalance:    input.InitialBalance,
		WalletBalance:     input.InitialBalance,
		AvailableBalance:  input.InitialBalance,
		Equity:            input.InitialBalance,
		Status:            store.AccountStatusActive,
	}
	if accountType == store.AccountTypeLive {
		if account.PriceMode == "" {
			account.PriceMode = "live"
		}
	} else {
		sandboxID := input.SandboxID
		account.SandboxID = &sandboxID
		if input.SandboxID == "" {
			account.SandboxID = nil
		}
	}
	if err := s.repo.Create(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *AccountService) Get(ctx context.Context, accountID string) (*store.Account, error) {
	account, err := s.repo.FindAccount(ctx, accountID)
	if err != nil {
		return nil, domain.NotFoundError("ACCOUNT_NOT_FOUND", "account not found")
	}
	return account, nil
}

func (s *AccountService) ListBySandbox(ctx context.Context, sandboxID string) ([]store.Account, error) {
	return s.repo.ListAccountsBySandbox(ctx, sandboxID)
}

func (s *AccountService) ListLive(ctx context.Context) ([]store.Account, error) {
	var accounts []store.Account
	err := s.repo.WithContext(ctx).Where("type = ?", store.AccountTypeLive).Order("created_at desc").Find(&accounts).Error
	return accounts, err
}

// ListAll returns all accounts (live + virtual). Pass typeFilter "live" or "virtual" to narrow; empty string = all.
func (s *AccountService) ListAll(ctx context.Context, typeFilter string) ([]store.Account, error) {
	var accounts []store.Account
	q := s.repo.WithContext(ctx)
	if typeFilter == store.AccountTypeLive || typeFilter == store.AccountTypeVirtual {
		q = q.Where("type = ?", typeFilter)
	}
	err := q.Order("created_at desc").Find(&accounts).Error
	return accounts, err
}

func (s *AccountService) Update(ctx context.Context, accountID string, input UpdateAccountInput) (*store.Account, error) {
	account, err := s.repo.FindAccount(ctx, accountID)
	if err != nil {
		return nil, domain.NotFoundError("ACCOUNT_NOT_FOUND", "account not found")
	}
	if input.Name != nil && *input.Name != "" {
		account.Name = *input.Name
	}
	if input.Status != nil && *input.Status != "" {
		account.Status = *input.Status
	}
	if input.Provider != nil {
		account.Provider = *input.Provider
	}
	if input.Environment != nil {
		account.Environment = *input.Environment
	}
	if input.PriceMode != nil {
		account.PriceMode = *input.PriceMode
	}
	if input.CredentialsStatus != nil {
		account.CredentialsStatus = *input.CredentialsStatus
	}
	if input.SupportedSymbols != nil {
		account.SupportedSymbols = store.StringList(*input.SupportedSymbols)
	}
	if input.LiveTradingEnabled != nil {
		account.LiveTradingEnabled = *input.LiveTradingEnabled
	}
	if err := s.repo.Save(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *AccountService) Delete(ctx context.Context, accountID string) error {
	return s.repo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account store.Account
		if err := tx.First(&account, "id = ?", accountID).Error; err != nil {
			return domain.NotFoundError("ACCOUNT_NOT_FOUND", "account not found")
		}
		var positions int64
		if err := tx.Model(&store.Position{}).Where("account_id = ?", accountID).Count(&positions).Error; err != nil {
			return err
		}
		if positions > 0 {
			return domain.ConflictError("ACCOUNT_HAS_POSITIONS", "account still has open positions")
		}
		if err := tx.Where("account_id = ?", accountID).Delete(&store.AccountToken{}).Error; err != nil {
			return err
		}
		if err := tx.Where("account_id = ?", accountID).Delete(&store.Order{}).Error; err != nil {
			return err
		}
		if err := tx.Where("account_id = ?", accountID).Delete(&store.Trade{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&account).Error; err != nil {
			return err
		}
		return nil
	})
}
