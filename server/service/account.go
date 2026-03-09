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
    SandboxID      string  `json:"sandbox_id"`
    Name           string  `json:"name"`
    InitialBalance float64 `json:"initial_balance"`
    Type           string  `json:"type"`
}

type UpdateAccountInput struct {
    Name   *string `json:"name,omitempty"`
    Status *string `json:"status,omitempty"`
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
    sandboxID := input.SandboxID
    account := &store.Account{
        ID:               newID("acct"),
        SandboxID:        &sandboxID,
        Name:             input.Name,
        Type:             accountType,
        BaseCurrency:     "USD",
        InitialBalance:   input.InitialBalance,
        WalletBalance:    input.InitialBalance,
        AvailableBalance: input.InitialBalance,
        Equity:           input.InitialBalance,
        Status:           store.AccountStatusActive,
    }
    if input.SandboxID == "" {
        account.SandboxID = nil
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
