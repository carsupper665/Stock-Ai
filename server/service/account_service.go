package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"server/model/store"
	"server/sandbox"

	"gorm.io/gorm"
)

var (
	ErrInvalidAmount       = errors.New("invalid amount")
	ErrInsufficientBalance = errors.New("insufficient balance")
)

const balanceEpsilon = 1e-9

type AccountService struct {
	db *gorm.DB
}

type ledgerRequest struct {
	RunID     string
	OwnerID   uint
	Asset     string
	EntryType string
	Amount    float64
	OrderID   *uint
	FillID    *uint
	BarIndex  *int
	Note      string
}

type fillSettlement struct {
	RunID      string
	OwnerID    uint
	Side       string
	BaseAsset  string
	QuoteAsset string
	Order      *store.Order
	Fill       *store.Fill
}

func NewAccountService(db *gorm.DB) *AccountService {
	return &AccountService{db: db}
}

func (s *AccountService) Deposit(ctx context.Context, runID string, ownerID uint, asset string, amount float64, note string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		wallet, err := s.mutateWallet(tx, runID, ownerID, asset, amount, amount, 0)
		if err != nil {
			return err
		}
		return s.createLedger(tx, wallet, ledgerRequest{
			RunID:     runID,
			OwnerID:   ownerID,
			Asset:     asset,
			EntryType: "deposit",
			Amount:    amount,
			Note:      note,
		})
	})
}

func (s *AccountService) Reset(ctx context.Context, runID string, ownerID uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("run_id = ? AND owner_id = ?", runID, ownerID).Delete(&store.LedgerEntry{}).Error; err != nil {
			return err
		}
		if err := tx.Where("run_id = ? AND owner_id = ?", runID, ownerID).Delete(&store.Wallet{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (s *AccountService) Balances(ctx context.Context, runID string, ownerID uint) ([]store.Wallet, error) {
	var wallets []store.Wallet
	err := s.db.WithContext(ctx).
		Where("run_id = ? AND owner_id = ?", runID, ownerID).
		Order("asset asc").
		Find(&wallets).Error
	return wallets, err
}

func (s *AccountService) Ledger(ctx context.Context, runID string, ownerID uint) ([]store.LedgerEntry, error) {
	var ledger []store.LedgerEntry
	err := s.db.WithContext(ctx).
		Where("run_id = ? AND owner_id = ?", runID, ownerID).
		Order("id asc").
		Find(&ledger).Error
	return ledger, err
}

func (s *AccountService) freezeReservation(tx *gorm.DB, order *store.Order) error {
	if order.ReservedAmount <= 0 {
		return ErrInvalidAmount
	}
	_, err := s.mutateWallet(tx, order.RunID, order.OwnerID, order.ReservedAsset, 0, -order.ReservedAmount, order.ReservedAmount)
	return err
}

func (s *AccountService) releaseReservation(tx *gorm.DB, order *store.Order) error {
	if order.ReservedAmount <= 0 {
		return nil
	}
	_, err := s.mutateWallet(tx, order.RunID, order.OwnerID, order.ReservedAsset, 0, order.ReservedAmount, -order.ReservedAmount)
	return err
}

func (s *AccountService) applyFill(tx *gorm.DB, settlement fillSettlement) error {
	order := settlement.Order
	fill := settlement.Fill
	barIndex := fill.ExecutionBarIndex

	switch settlement.Side {
	case sandbox.OrderSideBuy:
		quoteCostWallet, err := s.mutateWallet(tx, order.RunID, order.OwnerID, settlement.QuoteAsset, -fill.QuoteQuantity, 0, -fill.QuoteQuantity)
		if err != nil {
			return err
		}
		if err := s.createLedger(tx, quoteCostWallet, ledgerRequest{RunID: order.RunID, OwnerID: order.OwnerID, Asset: settlement.QuoteAsset, EntryType: "trade_buy_quote_out", Amount: -fill.QuoteQuantity, OrderID: &order.ID, FillID: &fill.ID, BarIndex: &barIndex}); err != nil {
			return err
		}

		remainingFrozen := order.ReservedAmount - fill.QuoteQuantity
		quoteFeeWallet, err := s.mutateWallet(tx, order.RunID, order.OwnerID, settlement.QuoteAsset, -fill.FeeAmount, remainingFrozen-fill.FeeAmount, -remainingFrozen)
		if err != nil {
			return err
		}
		if err := s.createLedger(tx, quoteFeeWallet, ledgerRequest{RunID: order.RunID, OwnerID: order.OwnerID, Asset: settlement.QuoteAsset, EntryType: "trade_fee", Amount: -fill.FeeAmount, OrderID: &order.ID, FillID: &fill.ID, BarIndex: &barIndex}); err != nil {
			return err
		}

		baseWallet, err := s.mutateWallet(tx, order.RunID, order.OwnerID, settlement.BaseAsset, fill.Quantity, fill.Quantity, 0)
		if err != nil {
			return err
		}
		if err := s.createLedger(tx, baseWallet, ledgerRequest{RunID: order.RunID, OwnerID: order.OwnerID, Asset: settlement.BaseAsset, EntryType: "fill_base_in", Amount: fill.Quantity, OrderID: &order.ID, FillID: &fill.ID, BarIndex: &barIndex}); err != nil {
			return err
		}
	case sandbox.OrderSideSell:
		baseWallet, err := s.mutateWallet(tx, order.RunID, order.OwnerID, settlement.BaseAsset, -fill.Quantity, 0, -order.ReservedAmount)
		if err != nil {
			return err
		}
		if err := s.createLedger(tx, baseWallet, ledgerRequest{RunID: order.RunID, OwnerID: order.OwnerID, Asset: settlement.BaseAsset, EntryType: "trade_sell_base_out", Amount: -fill.Quantity, OrderID: &order.ID, FillID: &fill.ID, BarIndex: &barIndex}); err != nil {
			return err
		}

		quoteInWallet, err := s.mutateWallet(tx, order.RunID, order.OwnerID, settlement.QuoteAsset, fill.QuoteQuantity, fill.QuoteQuantity, 0)
		if err != nil {
			return err
		}
		if err := s.createLedger(tx, quoteInWallet, ledgerRequest{RunID: order.RunID, OwnerID: order.OwnerID, Asset: settlement.QuoteAsset, EntryType: "trade_sell_quote_in", Amount: fill.QuoteQuantity, OrderID: &order.ID, FillID: &fill.ID, BarIndex: &barIndex}); err != nil {
			return err
		}

		quoteFeeWallet, err := s.mutateWallet(tx, order.RunID, order.OwnerID, settlement.QuoteAsset, -fill.FeeAmount, -fill.FeeAmount, 0)
		if err != nil {
			return err
		}
		if err := s.createLedger(tx, quoteFeeWallet, ledgerRequest{RunID: order.RunID, OwnerID: order.OwnerID, Asset: settlement.QuoteAsset, EntryType: "trade_fee", Amount: -fill.FeeAmount, OrderID: &order.ID, FillID: &fill.ID, BarIndex: &barIndex}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported settlement side: %s", settlement.Side)
	}

	return nil
}

func (s *AccountService) mutateWallet(tx *gorm.DB, runID string, ownerID uint, asset string, deltaBalance, deltaAvailable, deltaFrozen float64) (*store.Wallet, error) {
	asset = normalizeAsset(asset)
	if asset == "" {
		return nil, errors.New("asset is required")
	}

	var wallet store.Wallet
	err := tx.Where("run_id = ? AND owner_id = ? AND asset = ?", runID, ownerID, asset).First(&wallet).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		wallet = store.Wallet{RunID: runID, OwnerID: ownerID, Asset: asset}
	}

	balance := wallet.Balance + deltaBalance
	available := wallet.Available + deltaAvailable
	frozen := wallet.Frozen + deltaFrozen
	if balance < -balanceEpsilon || available < -balanceEpsilon || frozen < -balanceEpsilon {
		return nil, ErrInsufficientBalance
	}

	wallet.Balance = clampZero(balance)
	wallet.Available = clampZero(available)
	wallet.Frozen = clampZero(frozen)

	if wallet.ID == 0 {
		if err := tx.Create(&wallet).Error; err != nil {
			return nil, err
		}
	} else {
		if err := tx.Save(&wallet).Error; err != nil {
			return nil, err
		}
	}

	return &wallet, nil
}

func (s *AccountService) createLedger(tx *gorm.DB, wallet *store.Wallet, req ledgerRequest) error {
	entry := store.LedgerEntry{
		RunID:        req.RunID,
		OwnerID:      req.OwnerID,
		Asset:        normalizeAsset(req.Asset),
		EntryType:    req.EntryType,
		Amount:       req.Amount,
		BalanceAfter: wallet.Balance,
		OrderID:      req.OrderID,
		FillID:       req.FillID,
		BarIndex:     req.BarIndex,
		Note:         req.Note,
	}
	return tx.Create(&entry).Error
}

func normalizeAsset(asset string) string {
	return strings.ToUpper(strings.TrimSpace(asset))
}

func clampZero(value float64) float64 {
	if math.Abs(value) < balanceEpsilon {
		return 0
	}
	return value
}
