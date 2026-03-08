package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"server/model/store"
	"server/sandbox"

	"gorm.io/gorm"
)

var (
	ErrOrderNotFound     = errors.New("order not found")
	ErrOrderNotOpen      = errors.New("order not open")
	ErrRunCursorMismatch = errors.New("run cursor mismatch")
)

type orderEngine interface {
	State() (sandbox.EngineState, error)
	Advance() (sandbox.EngineState, error)
	Seek(int) (sandbox.EngineState, error)
}

type OrderServiceOption func(*OrderService)

type SubmitOrderInput struct {
	RunID         string
	OwnerID       uint
	ClientOrderID string
	Symbol        string
	Side          string
	Type          string
	TimeInForce   string
	Quantity      float64
	Price         *float64
}

type OrderService struct {
	db               *gorm.DB
	accounts         *AccountService
	engine           orderEngine
	beforeStepCommit func(context.Context, *gorm.DB) error
}

func NewOrderService(db *gorm.DB, accounts *AccountService, engine orderEngine, opts ...OrderServiceOption) *OrderService {
	svc := &OrderService{
		db:       db,
		accounts: accounts,
		engine:   engine,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func WithBeforeStepCommitHook(hook func(context.Context, *gorm.DB) error) OrderServiceOption {
	return func(s *OrderService) {
		s.beforeStepCommit = hook
	}
}

func (s *OrderService) SubmitOrder(ctx context.Context, input SubmitOrderInput) (*store.Order, error) {
	if err := s.validateSubmitInput(input); err != nil {
		return nil, err
	}

	currentState, err := s.engine.State()
	if err != nil {
		return nil, err
	}
	runSession, err := s.loadRunSession(ctx, input.RunID)
	if err != nil {
		return nil, err
	}
	if runSession.Cursor != currentState.Cursor {
		return nil, fmt.Errorf("%w: run=%d engine=%d", ErrRunCursorMismatch, runSession.Cursor, currentState.Cursor)
	}

	symbol, err := sandbox.NormalizeSymbol(input.Symbol)
	if err != nil {
		return nil, err
	}
	config, err := s.loadActiveSymbolConfig(ctx, symbol)
	if err != nil {
		return nil, err
	}
	if err := validateOrderAgainstConfig(input, config, currentState); err != nil {
		return nil, err
	}

	reservedAsset, reservedAmount, err := reservationForOrder(input, config, currentState)
	if err != nil {
		return nil, err
	}

	order := &store.Order{
		RunID:             input.RunID,
		OwnerID:           input.OwnerID,
		ClientOrderID:     strings.TrimSpace(input.ClientOrderID),
		Symbol:            symbol,
		Side:              strings.ToUpper(strings.TrimSpace(input.Side)),
		Type:              strings.ToUpper(strings.TrimSpace(input.Type)),
		TimeInForce:       normalizeTimeInForce(input),
		Status:            sandbox.OrderStatusOpen,
		Quantity:          input.Quantity,
		Price:             input.Price,
		ReservedAsset:     reservedAsset,
		ReservedAmount:    reservedAmount,
		AverageFillPrice:  0,
		SubmittedBarIndex: currentState.Cursor,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.accounts.freezeReservation(tx, order); err != nil {
			return err
		}
		return tx.Create(order).Error
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

func (s *OrderService) CancelOrder(ctx context.Context, runID string, ownerID uint, orderID uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order store.Order
		if err := tx.Where("id = ? AND run_id = ? AND owner_id = ?", orderID, runID, ownerID).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrderNotFound
			}
			return err
		}
		if order.Status != sandbox.OrderStatusOpen {
			return ErrOrderNotOpen
		}
		if err := s.accounts.releaseReservation(tx, &order); err != nil {
			return err
		}
		now := time.Now()
		order.Status = sandbox.OrderStatusCanceled
		order.CanceledAt = &now
		return tx.Save(&order).Error
	})
}

func (s *OrderService) OpenOrders(ctx context.Context, runID string, ownerID uint) ([]store.Order, error) {
	var orders []store.Order
	err := s.db.WithContext(ctx).
		Where("run_id = ? AND owner_id = ? AND status = ?", runID, ownerID, sandbox.OrderStatusOpen).
		Order("id asc").
		Find(&orders).Error
	return orders, err
}

func (s *OrderService) Fills(ctx context.Context, runID string, ownerID uint) ([]store.Fill, error) {
	var fills []store.Fill
	err := s.db.WithContext(ctx).
		Where("run_id = ? AND owner_id = ?", runID, ownerID).
		Order("id asc").
		Find(&fills).Error
	return fills, err
}

func (s *OrderService) AdvanceAndFill(ctx context.Context, runID string) ([]store.Fill, sandbox.EngineState, error) {
	preState, err := s.engine.State()
	if err != nil {
		return nil, sandbox.EngineState{}, err
	}
	runSession, err := s.loadRunSession(ctx, runID)
	if err != nil {
		return nil, sandbox.EngineState{}, err
	}
	if runSession.Cursor != preState.Cursor {
		return nil, sandbox.EngineState{}, fmt.Errorf("%w: run=%d engine=%d", ErrRunCursorMismatch, runSession.Cursor, preState.Cursor)
	}

	nextState, err := s.engine.Advance()
	if err != nil {
		return nil, sandbox.EngineState{}, err
	}

	var fills []store.Fill
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var txRun store.RunSession
		if err := tx.Where("run_id = ?", runID).First(&txRun).Error; err != nil {
			return err
		}
		if txRun.Cursor != preState.Cursor {
			return fmt.Errorf("%w: run=%d engine=%d", ErrRunCursorMismatch, txRun.Cursor, preState.Cursor)
		}

		config, err := s.loadActiveSymbolConfigTx(tx, nextState.Symbol)
		if err != nil {
			return err
		}

		var openOrders []store.Order
		if err := tx.Where("run_id = ? AND status = ? AND symbol = ?", runID, sandbox.OrderStatusOpen, nextState.Symbol).Order("id asc").Find(&openOrders).Error; err != nil {
			return err
		}

		for i := range openOrders {
			order := &openOrders[i]
			decision, err := sandbox.DecideFill(sandbox.MatchInput{
				OrderType:   order.Type,
				Side:        order.Side,
				Quantity:    order.Quantity,
				LimitPrice:  order.Price,
				Bar:         nextState.CurrentBar,
				SlippageBps: sandbox.DefaultMarketSlippageBps,
			})
			if err != nil {
				return err
			}
			if !decision.ShouldFill {
				continue
			}

			feeBps := config.MakerFeeBps
			if !decision.IsMaker {
				feeBps = config.TakerFeeBps
			}
			quoteQuantity := decision.FillPrice * order.Quantity
			feeAmount := quoteQuantity * feeBps / 10000
			fill := store.Fill{
				RunID:             order.RunID,
				OrderID:           order.ID,
				OwnerID:           order.OwnerID,
				Symbol:            order.Symbol,
				Side:              order.Side,
				Price:             decision.FillPrice,
				Quantity:          order.Quantity,
				QuoteQuantity:     quoteQuantity,
				FeeAsset:          config.QuoteAsset,
				FeeAmount:         feeAmount,
				SlippageBps:       decision.SlippageBps,
				ExecutionBarIndex: nextState.Cursor,
				ExecutionTime:     nextState.CurrentBar.Time,
			}
			if err := tx.Create(&fill).Error; err != nil {
				return err
			}

			if err := s.accounts.applyFill(tx, fillSettlement{
				RunID:      order.RunID,
				OwnerID:    order.OwnerID,
				Side:       order.Side,
				BaseAsset:  config.BaseAsset,
				QuoteAsset: config.QuoteAsset,
				Order:      order,
				Fill:       &fill,
			}); err != nil {
				return err
			}

			eligibleBarIndex := nextState.Cursor
			order.Status = sandbox.OrderStatusFilled
			order.FilledQuantity = order.Quantity
			order.AverageFillPrice = fill.Price
			order.EligibleBarIndex = &eligibleBarIndex
			if err := tx.Save(order).Error; err != nil {
				return err
			}
			fills = append(fills, fill)
		}

		txRun.Cursor = nextState.Cursor
		if err := tx.Save(&txRun).Error; err != nil {
			return err
		}
		if s.beforeStepCommit != nil {
			if err := s.beforeStepCommit(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		if _, seekErr := s.engine.Seek(preState.Cursor); seekErr != nil {
			return nil, sandbox.EngineState{}, fmt.Errorf("advance tx failed: %w; engine restore failed: %v", txErr, seekErr)
		}
		return nil, sandbox.EngineState{}, txErr
	}

	return fills, nextState, nil
}

func (s *OrderService) loadRunSession(ctx context.Context, runID string) (*store.RunSession, error) {
	var runSession store.RunSession
	if err := s.db.WithContext(ctx).Where("run_id = ?", runID).First(&runSession).Error; err != nil {
		return nil, err
	}
	return &runSession, nil
}

func (s *OrderService) loadActiveSymbolConfig(ctx context.Context, symbol string) (*store.SymbolConfig, error) {
	return s.loadActiveSymbolConfigTx(s.db.WithContext(ctx), symbol)
}

func (s *OrderService) loadActiveSymbolConfigTx(tx *gorm.DB, symbol string) (*store.SymbolConfig, error) {
	var config store.SymbolConfig
	if err := tx.Where("symbol = ? AND is_active = ?", symbol, true).First(&config).Error; err != nil {
		return nil, err
	}
	return &config, nil
}

func validateOrderAgainstConfig(input SubmitOrderInput, config *store.SymbolConfig, state sandbox.EngineState) error {
	side := strings.ToUpper(strings.TrimSpace(input.Side))
	orderType := strings.ToUpper(strings.TrimSpace(input.Type))
	if input.Quantity < config.MinQuantity {
		return fmt.Errorf("quantity below minimum: %v < %v", input.Quantity, config.MinQuantity)
	}
	if !isStepAligned(input.Quantity, config.QuantityStep) {
		return fmt.Errorf("quantity is not aligned to step: %v", input.Quantity)
	}
	if orderType == sandbox.OrderTypeLimit {
		if input.Price == nil || *input.Price <= 0 {
			return sandbox.ErrLimitPriceRequired
		}
		if !isStepAligned(*input.Price, config.PriceTick) {
			return fmt.Errorf("price is not aligned to tick: %v", *input.Price)
		}
		if (*input.Price * input.Quantity) < config.MinNotional {
			return fmt.Errorf("notional below minimum: %v < %v", *input.Price*input.Quantity, config.MinNotional)
		}
		return nil
	}

	if orderType != sandbox.OrderTypeMarket {
		return fmt.Errorf("%w: %s", sandbox.ErrUnsupportedOrderType, orderType)
	}
	refPrice := state.CurrentBar.Close
	if side == sandbox.OrderSideSell {
		refPrice = state.CurrentBar.Close
	}
	if refPrice*input.Quantity < config.MinNotional {
		return fmt.Errorf("estimated notional below minimum: %v < %v", refPrice*input.Quantity, config.MinNotional)
	}
	return nil
}

func reservationForOrder(input SubmitOrderInput, config *store.SymbolConfig, state sandbox.EngineState) (string, float64, error) {
	side := strings.ToUpper(strings.TrimSpace(input.Side))
	orderType := strings.ToUpper(strings.TrimSpace(input.Type))

	switch side {
	case sandbox.OrderSideBuy:
		if orderType == sandbox.OrderTypeMarket {
			estimatedFillPrice, err := sandbox.ApplyDeterministicSlippage(side, state.CurrentBar.Close, sandbox.DefaultMarketSlippageBps)
			if err != nil {
				return "", 0, err
			}
			quote := estimatedFillPrice * input.Quantity
			fee := quote * config.TakerFeeBps / 10000
			return config.QuoteAsset, quote + fee, nil
		}
		if input.Price == nil {
			return "", 0, sandbox.ErrLimitPriceRequired
		}
		quote := *input.Price * input.Quantity
		fee := quote * config.MakerFeeBps / 10000
		return config.QuoteAsset, quote + fee, nil
	case sandbox.OrderSideSell:
		return config.BaseAsset, input.Quantity, nil
	default:
		return "", 0, fmt.Errorf("%w: %s", sandbox.ErrUnsupportedOrderSide, side)
	}
}

func (s *OrderService) validateSubmitInput(input SubmitOrderInput) error {
	if strings.TrimSpace(input.RunID) == "" {
		return errors.New("run_id is required")
	}
	if input.OwnerID == 0 {
		return errors.New("owner_id is required")
	}
	if strings.TrimSpace(input.ClientOrderID) == "" {
		return errors.New("client_order_id is required")
	}
	if input.Quantity <= 0 {
		return ErrInvalidAmount
	}
	side := strings.ToUpper(strings.TrimSpace(input.Side))
	if side != sandbox.OrderSideBuy && side != sandbox.OrderSideSell {
		return fmt.Errorf("%w: %s", sandbox.ErrUnsupportedOrderSide, input.Side)
	}
	orderType := strings.ToUpper(strings.TrimSpace(input.Type))
	if orderType != sandbox.OrderTypeMarket && orderType != sandbox.OrderTypeLimit {
		return fmt.Errorf("%w: %s", sandbox.ErrUnsupportedOrderType, input.Type)
	}
	if orderType == sandbox.OrderTypeLimit && (input.Price == nil || *input.Price <= 0) {
		return sandbox.ErrLimitPriceRequired
	}
	return nil
}

func normalizeTimeInForce(input SubmitOrderInput) string {
	if strings.ToUpper(strings.TrimSpace(input.Type)) == sandbox.OrderTypeLimit {
		if tif := strings.ToUpper(strings.TrimSpace(input.TimeInForce)); tif != "" {
			return tif
		}
		return sandbox.TimeInForceGTC
	}
	return ""
}

func isStepAligned(value, step float64) bool {
	if step <= 0 {
		return true
	}
	ratio := value / step
	return math.Abs(ratio-math.Round(ratio)) < 1e-9
}
