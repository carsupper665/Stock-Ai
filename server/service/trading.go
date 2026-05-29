package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"server/domain"
	"server/model/repo"
	"server/model/store"

	"gorm.io/gorm"
)

type TradingService struct {
	repo       *repo.Repository
	clock      domain.Clock
	market     domain.MarketDataProvider
	liveMarket *LiveMarketProvider
	exchange   ExchangeAdapter
	events     domain.EventPublisher
	risk       domain.RiskEngine
	fillPolicy domain.FillPolicy
	ledger     domain.AccountLedger
	config     AppConfig
}

type PlaceOrderInput struct {
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	PositionSide  string  `json:"position_side"`
	OrderType     string  `json:"order_type"`
	Type          string  `json:"type,omitempty"`
	Quantity      float64 `json:"qty"`
	QuantityAlt   float64 `json:"quantity,omitempty"`
	Price         float64 `json:"price,omitempty"`
	StopPrice     float64 `json:"stop_price,omitempty"`
	Leverage      float64 `json:"leverage,omitempty"`
	ReduceOnly    bool    `json:"reduce_only,omitempty"`
	ClientOrderID string  `json:"client_order_id,omitempty"`
}

type TradingMode string

const (
	TradingModeReplay  TradingMode = "replay"
	TradingModePaper   TradingMode = "paper"
	TradingModeTestnet TradingMode = "testnet"
	TradingModeMainnet TradingMode = "mainnet"
)

type AccountPerformance struct {
	AccountID      string  `json:"account_id"`
	Equity         float64 `json:"equity"`
	RealizedPnL    float64 `json:"realized_pnl"`
	UnrealizedPnL  float64 `json:"unrealized_pnl"`
	ReturnPct      float64 `json:"return_pct"`
	WalletBalance  float64 `json:"wallet_balance"`
	AvailableFunds float64 `json:"available_balance"`
}

func NewTradingService(repo *repo.Repository, clock domain.Clock, market domain.MarketDataProvider, liveMarket *LiveMarketProvider, exchange ExchangeAdapter, events domain.EventPublisher, risk domain.RiskEngine, fillPolicy domain.FillPolicy, ledger domain.AccountLedger, cfg AppConfig) *TradingService {
	return &TradingService{repo: repo, clock: clock, market: market, liveMarket: liveMarket, exchange: exchange, events: events, risk: risk, fillPolicy: fillPolicy, ledger: ledger, config: cfg}
}

func (s *TradingService) PlaceOrder(ctx context.Context, accountID string, input PlaceOrderInput) (*store.Order, error) {
	input = canonicalizePlaceOrderInput(input)
	account, err := s.repo.FindAccount(ctx, accountID)
	if err != nil {
		return nil, domain.NotFoundError("ACCOUNT_NOT_FOUND", "account not found")
	}
	input.Symbol = normalizeOrderSymbol(input.Symbol)
	mode, err := s.resolveTradingMode(*account)
	if err != nil {
		return nil, err
	}
	if err := validateSupportedSymbol(*account, input.Symbol); err != nil {
		return nil, err
	}
	if input.ReduceOnly && isOpeningAction(input.Side, input.PositionSide) {
		return nil, domain.ValidationError("REDUCE_ONLY_WOULD_OPEN", "reduce_only order would open or increase exposure")
	}
	if mode == TradingModeTestnet || mode == TradingModeMainnet {
		if err := s.liveExecutionGate(*account, mode); err != nil {
			s.publishAuditEvent(ctx, "live.order.rejected", "", accountID, "", map[string]any{"symbol": input.Symbol, "code": errorCode(err, "LIVE_ORDER_REJECTED"), "mode": mode})
			return nil, err
		}
	}
	sandboxID := ""
	sandboxStatus := store.SandboxStatusRunning
	marketPrice := input.Price
	tradable := false
	if mode == TradingModeReplay {
		sandboxID = *account.SandboxID
		sandbox, err := s.repo.FindSandbox(ctx, sandboxID)
		if err != nil {
			return nil, domain.NotFoundError("SANDBOX_NOT_FOUND", "sandbox not found")
		}
		sandboxStatus = sandbox.Status
		marketPrice, _, err = s.market.GetLastPrice(ctx, sandbox.ID, input.Symbol)
		tradable = err == nil
		if !tradable {
			marketPrice = input.Price
		}
	} else {
		if s.liveMarket == nil {
			return nil, domain.ValidationError("LIVE_MARKET_UNAVAILABLE", "live market provider is unavailable")
		}
		snapshot, err := s.liveMarket.GetSnapshot(ctx, input.Symbol)
		if err != nil {
			s.publishAuditEvent(ctx, "live.order.rejected", "", accountID, "", map[string]any{"symbol": input.Symbol, "code": errorCode(err, "LIVE_MARKET_UNAVAILABLE"), "mode": mode})
			return nil, err
		}
		marketPrice = snapshot.LastPrice
		tradable = snapshot.State == domain.MarketStateActive
	}
	position, err := s.repo.FindPosition(ctx, accountID, sandboxID, input.Symbol, input.PositionSide)
	if err != nil {
		return nil, err
	}
	currentQty := 0.0
	if position != nil {
		currentQty = position.Quantity
	}
	leverage := input.Leverage
	if leverage <= 0 {
		leverage = 1
	}
	input, err = normalizeOrderInput(*account, input, marketPrice, leverage)
	if err != nil {
		s.publishAuditEvent(ctx, "risk.order.rejected", sandboxID, accountID, "", map[string]any{"symbol": input.Symbol, "code": errorCode(err, "ORDER_NORMALIZATION_FAILED"), "mode": mode})
		return nil, err
	}
	leverage = input.Leverage
	if input.ClientOrderID != "" {
		existing, err := s.findOrderByClientID(ctx, accountID, input.ClientOrderID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if sameClientOrderPayload(*existing, input) {
				return existing, nil
			}
			return nil, domain.ConflictError("CLIENT_ORDER_ID_CONFLICT", "client_order_id already exists with different order payload")
		}
	}
	if err := s.risk.ValidateNewOrder(ctx, domain.RiskOrderRequest{
		WalletBalance:    account.WalletBalance,
		AvailableBalance: account.AvailableBalance,
		CurrentQuantity:  currentQty,
		MarketPrice:      marketPrice,
		OrderType:        input.OrderType,
		Side:             input.Side,
		PositionSide:     input.PositionSide,
		Quantity:         input.Quantity,
		LimitPrice:       input.Price,
		StopPrice:        input.StopPrice,
		Leverage:         leverage,
		SandboxStatus:    sandboxStatus,
		Tradable:         tradable,
	}); err != nil {
		s.publishAuditEvent(ctx, "risk.order.rejected", sandboxID, accountID, "", map[string]any{"symbol": input.Symbol, "code": errorCode(err, "RISK_ORDER_REJECTED"), "mode": mode})
		return nil, err
	}

	order := &store.Order{
		ID:            newID("ord"),
		SandboxID:     sandboxID,
		AccountID:     accountID,
		Symbol:        input.Symbol,
		Side:          input.Side,
		PositionSide:  input.PositionSide,
		OrderType:     input.OrderType,
		Quantity:      input.Quantity,
		Price:         input.Price,
		StopPrice:     input.StopPrice,
		Leverage:      leverage,
		Status:        store.OrderStatusNew,
		ReduceOnly:    input.ReduceOnly,
		ClientOrderID: input.ClientOrderID,
	}
	if err := s.repo.Create(ctx, order); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "order.created", SandboxID: sandboxID, AccountID: accountID, AggregateID: order.ID, Payload: map[string]any{"symbol": order.Symbol, "status": order.Status}})
	if mode == TradingModeTestnet || mode == TradingModeMainnet {
		if err := s.submitExchangeOrder(ctx, *account, order, marketPrice, mode); err != nil {
			s.publishAuditEvent(ctx, "live.order.rejected", sandboxID, accountID, order.ID, map[string]any{"symbol": order.Symbol, "code": errorCode(err, "LIVE_ORDER_REJECTED"), "mode": mode})
			return nil, err
		}
		return s.GetOrder(ctx, order.ID)
	}

	switch order.OrderType {
	case store.OrderTypeMarket:
		if err := s.fillOrder(ctx, order.ID, marketPrice, false); err != nil {
			return nil, err
		}
	case store.OrderTypeLimit:
		if s.fillPolicy.LimitShouldFill(order.Side, marketPrice, order.Price) {
			if err := s.fillOrder(ctx, order.ID, marketPrice, false); err != nil {
				return nil, err
			}
		}
	case store.OrderTypeStop:
		if s.fillPolicy.StopShouldTrigger(order.PositionSide, marketPrice, order.StopPrice) {
			if err := s.fillOrder(ctx, order.ID, marketPrice, true); err != nil {
				return nil, err
			}
		}
	default:
		return nil, domain.ValidationError("INVALID_ORDER_TYPE", "unsupported order type")
	}
	return s.GetOrder(ctx, order.ID)
}

func (s *TradingService) resolveTradingMode(account store.Account) (TradingMode, error) {
	switch account.Type {
	case store.AccountTypeVirtual:
		if account.PriceMode == "live" {
			return "", domain.ValidationError("INVALID_TRADING_MODE", "virtual accounts cannot use live price mode")
		}
		if account.SandboxID == nil || *account.SandboxID == "" {
			return "", domain.ValidationError("ACCOUNT_SANDBOX_REQUIRED", "virtual account is not bound to a sandbox")
		}
		return TradingModeReplay, nil
	case store.AccountTypeLive:
		if account.PriceMode != "live" {
			return "", domain.ValidationError("INVALID_TRADING_MODE", "live accounts require price_mode=live")
		}
		switch account.Environment {
		case "paper", "production":
			return TradingModePaper, nil
		case "testnet":
			if account.CredentialsStatus != "healthy" {
				return "", domain.ValidationError("LIVE_CREDENTIALS_REQUIRED", "healthy exchange credentials are required")
			}
			return TradingModeTestnet, nil
		case "mainnet":
			if account.CredentialsStatus != "healthy" {
				return "", domain.ValidationError("LIVE_CREDENTIALS_REQUIRED", "healthy exchange credentials are required")
			}
			return TradingModeMainnet, nil
		default:
			return "", domain.ValidationError("INVALID_TRADING_MODE", "live account environment must be paper, testnet, or mainnet")
		}
	default:
		return "", domain.ValidationError("INVALID_ACCOUNT_TYPE", "unsupported account type")
	}
}

func (s *TradingService) liveExecutionGate(account store.Account, mode TradingMode) error {
	if !s.config.AllowLiveExecution {
		return domain.ConflictError("LIVE_EXECUTION_DISABLED", "live exchange execution is disabled")
	}
	if mode == TradingModeMainnet && !s.config.AllowMainnetExecution {
		return domain.ConflictError("MAINNET_EXECUTION_DISABLED", "mainnet execution is disabled")
	}
	if !account.LiveTradingEnabled {
		return domain.ConflictError("ACCOUNT_LIVE_TRADING_DISABLED", "account live trading is disabled")
	}
	if s.exchange == nil {
		return domain.ConflictError("LIVE_EXCHANGE_EXECUTION_UNSUPPORTED", "exchange execution adapter is not configured")
	}
	return nil
}

type accountRiskProfile struct {
	SymbolRules       []domain.SymbolRule `json:"symbol_rules"`
	AllowedOrderTypes []string            `json:"allowed_order_types"`
}

func normalizeOrderInput(account store.Account, input PlaceOrderInput, marketPrice, leverage float64) (PlaceOrderInput, error) {
	input.Symbol = normalizeOrderSymbol(input.Symbol)
	if input.Leverage <= 0 {
		input.Leverage = leverage
	}
	profile, err := parseAccountRiskProfile(account)
	if err != nil {
		return input, err
	}
	if len(profile.AllowedOrderTypes) > 0 && !stringInFoldedList(input.OrderType, profile.AllowedOrderTypes) {
		return input, domain.ValidationError("ORDER_TYPE_NOT_ALLOWED", "order type is not allowed by account risk profile")
	}
	rule, ok := findSymbolRule(profile, input.Symbol)
	if !ok {
		return input, nil
	}
	if rule.MaxLeverage > 0 && input.Leverage > rule.MaxLeverage {
		return input, domain.ValidationError("LEVERAGE_TOO_HIGH", "leverage exceeds symbol rule")
	}
	if rule.StepSize > 0 {
		input.Quantity = floorToStep(input.Quantity, rule.StepSize)
	}
	if rule.MinQty > 0 && input.Quantity+1e-12 < rule.MinQty {
		return input, domain.ValidationError("MIN_QTY_NOT_MET", "quantity is below symbol minimum")
	}
	priceForNotional := marketPrice
	if input.OrderType == store.OrderTypeLimit {
		if rule.TickSize > 0 {
			input.Price = normalizeLimitPrice(input.Side, input.Price, rule.TickSize)
		}
		priceForNotional = input.Price
	}
	if input.OrderType == store.OrderTypeStop && rule.TickSize > 0 {
		input.StopPrice = normalizeLimitPrice(input.Side, input.StopPrice, rule.TickSize)
	}
	notional := input.Quantity * priceForNotional
	if rule.MinNotional > 0 && notional+1e-9 < rule.MinNotional {
		return input, domain.ValidationError("MIN_NOTIONAL_NOT_MET", "order notional is below symbol minimum")
	}
	return input, nil
}

func parseAccountRiskProfile(account store.Account) (accountRiskProfile, error) {
	var profile accountRiskProfile
	if strings.TrimSpace(account.RiskProfileJSON) == "" {
		return profile, nil
	}
	if err := json.Unmarshal([]byte(account.RiskProfileJSON), &profile); err != nil {
		return profile, domain.ValidationError("INVALID_RISK_PROFILE", "account risk profile is invalid")
	}
	for i := range profile.SymbolRules {
		profile.SymbolRules[i].Symbol = normalizeOrderSymbol(profile.SymbolRules[i].Symbol)
	}
	return profile, nil
}

func findSymbolRule(profile accountRiskProfile, symbol string) (domain.SymbolRule, bool) {
	for _, rule := range profile.SymbolRules {
		if rule.Symbol == symbol {
			return rule, true
		}
	}
	return domain.SymbolRule{}, false
}

func stringInFoldedList(value string, allowed []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, item := range allowed {
		if strings.ToLower(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}

func floorToStep(value, step float64) float64 {
	if step <= 0 {
		return value
	}
	return math.Floor((value+1e-12)/step) * step
}

func normalizeLimitPrice(side string, price, tick float64) float64 {
	if tick <= 0 || price <= 0 {
		return price
	}
	switch side {
	case store.OrderSideSell:
		return math.Ceil((price-1e-12)/tick) * tick
	default:
		return math.Floor((price+1e-12)/tick) * tick
	}
}

func validateSupportedSymbol(account store.Account, symbol string) error {
	if len(account.SupportedSymbols) == 0 {
		return nil
	}
	for _, supported := range account.SupportedSymbols {
		if normalizeOrderSymbol(supported) == symbol {
			return nil
		}
	}
	return domain.ValidationError("UNSUPPORTED_SYMBOL", "symbol is not supported by account")
}

func normalizeOrderSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

func canonicalizePlaceOrderInput(input PlaceOrderInput) PlaceOrderInput {
	if strings.TrimSpace(input.OrderType) == "" {
		input.OrderType = input.Type
	}
	if input.Quantity <= 0 && input.QuantityAlt > 0 {
		input.Quantity = input.QuantityAlt
	}
	input.OrderType = strings.ToLower(strings.TrimSpace(input.OrderType))
	input.Side = strings.ToLower(strings.TrimSpace(input.Side))
	input.PositionSide = strings.ToLower(strings.TrimSpace(input.PositionSide))
	return input
}

func (s *TradingService) findOrderByClientID(ctx context.Context, accountID, clientOrderID string) (*store.Order, error) {
	var order store.Order
	err := s.repo.WithContext(ctx).Where("account_id = ? AND client_order_id = ?", accountID, clientOrderID).First(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func sameClientOrderPayload(order store.Order, input PlaceOrderInput) bool {
	return order.Symbol == input.Symbol &&
		order.Side == input.Side &&
		order.PositionSide == input.PositionSide &&
		order.OrderType == input.OrderType &&
		order.Quantity == input.Quantity &&
		order.Price == input.Price &&
		order.StopPrice == input.StopPrice &&
		order.ReduceOnly == input.ReduceOnly
}

func (s *TradingService) fillOrder(ctx context.Context, orderID string, fillPrice float64, triggered bool) error {
	var accountID string
	var sandboxID string
	err := s.repo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order store.Order
		if err := tx.First(&order, "id = ?", orderID).Error; err != nil {
			return err
		}
		if order.Status == store.OrderStatusFilled || order.Status == store.OrderStatusCanceled {
			return nil
		}
		var account store.Account
		if err := tx.First(&account, "id = ?", order.AccountID).Error; err != nil {
			return err
		}
		accountID = account.ID
		sandboxID = order.SandboxID

		var position store.Position
		positionErr := tx.Where("account_id = ? AND sandbox_id = ? AND symbol = ? AND position_side = ?", order.AccountID, order.SandboxID, order.Symbol, order.PositionSide).First(&position).Error
		if errors.Is(positionErr, gorm.ErrRecordNotFound) {
			positionErr = nil
		}
		if positionErr != nil {
			return positionErr
		}

		if triggered {
			now := s.clock.Now()
			order.Status = store.OrderStatusTriggered
			order.TriggeredAt = &now
			if err := tx.Save(&order).Error; err != nil {
				return err
			}
		}

		opening := isOpeningAction(order.Side, order.PositionSide)
		quantity := order.Quantity
		if opening {
			requiredMargin := quantity * fillPrice / order.Leverage
			if err := s.ledger.ReserveMargin(&account.WalletBalance, &account.LockedMargin, requiredMargin); err != nil {
				order.Status = store.OrderStatusRejected
				order.Rejection = err.Error()
				_ = tx.Save(&order).Error
				return err
			}
			if position.ID == "" {
				position = store.Position{
					ID:            newID("pos"),
					AccountID:     order.AccountID,
					SandboxID:     order.SandboxID,
					Symbol:        order.Symbol,
					PositionSide:  order.PositionSide,
					Quantity:      quantity,
					EntryPrice:    fillPrice,
					MarkPrice:     fillPrice,
					Leverage:      order.Leverage,
					MarginUsed:    requiredMargin,
					UnrealizedPnL: 0,
					UpdatedAt:     s.clock.Now(),
				}
				if err := tx.Create(&position).Error; err != nil {
					return err
				}
			} else {
				totalQty := position.Quantity + quantity
				if totalQty <= 0 {
					return domain.ValidationError("INVALID_POSITION_QTY", "position quantity must stay positive")
				}
				position.EntryPrice = ((position.EntryPrice * position.Quantity) + (fillPrice * quantity)) / totalQty
				position.Quantity = totalQty
				position.MarkPrice = fillPrice
				position.Leverage = order.Leverage
				position.MarginUsed += requiredMargin
				position.UpdatedAt = s.clock.Now()
				if err := tx.Save(&position).Error; err != nil {
					return err
				}
			}
		} else {
			if position.ID == "" || position.Quantity < quantity {
				return domain.ValidationError("POSITION_QTY_EXCEEDED", "order quantity exceeds current position size")
			}
			release := position.MarginUsed * (quantity / position.Quantity)
			pnl := realizedPnL(position.PositionSide, position.EntryPrice, fillPrice, quantity)
			s.ledger.ReleaseMargin(&account.WalletBalance, &account.LockedMargin, release)
			s.ledger.ApplyRealizedPnL(&account.WalletBalance, &account.RealizedPnL, pnl)
			position.Quantity -= quantity
			position.MarginUsed -= release
			position.MarkPrice = fillPrice
			position.UpdatedAt = s.clock.Now()
			if position.Quantity <= 0 {
				if err := tx.Delete(&position).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Save(&position).Error; err != nil {
					return err
				}
			}
		}

		trade := store.Trade{
			ID:           newID("trd"),
			OrderID:      order.ID,
			AccountID:    order.AccountID,
			SandboxID:    order.SandboxID,
			Symbol:       order.Symbol,
			Quantity:     quantity,
			Price:        fillPrice,
			Fee:          0,
			Side:         order.Side,
			PositionSide: order.PositionSide,
			ExecutedAt:   s.clock.Now(),
		}
		if err := tx.Create(&trade).Error; err != nil {
			return err
		}

		order.Status = store.OrderStatusFilled
		order.FilledQty = quantity
		order.AvgFillPrice = fillPrice
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if accountID != "" {
		if err := s.RefreshAccount(ctx, accountID); err != nil {
			return err
		}
	}
	if triggered {
		_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "stoploss.triggered", SandboxID: sandboxID, AccountID: accountID, AggregateID: orderID, Payload: map[string]any{"order_id": orderID}})
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "order.updated", SandboxID: sandboxID, AccountID: accountID, AggregateID: orderID, Payload: map[string]any{"status": store.OrderStatusFilled}})
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "trade.executed", SandboxID: sandboxID, AccountID: accountID, AggregateID: orderID, Payload: map[string]any{"order_id": orderID}})
	return nil
}

func (s *TradingService) submitExchangeOrder(ctx context.Context, account store.Account, order *store.Order, marketPrice float64, mode TradingMode) error {
	price := order.Price
	if price <= 0 {
		price = marketPrice
	}
	s.publishAuditEvent(ctx, "live.order.submitted", order.SandboxID, account.ID, order.ID, map[string]any{"symbol": order.Symbol, "mode": mode, "client_order_id": order.ClientOrderID})
	result, err := s.exchange.PlaceOrder(ctx, ExchangeOrderRequest{
		AccountID:     account.ID,
		OrderID:       order.ID,
		Symbol:        order.Symbol,
		Side:          order.Side,
		PositionSide:  order.PositionSide,
		OrderType:     order.OrderType,
		Quantity:      order.Quantity,
		Price:         price,
		StopPrice:     order.StopPrice,
		Leverage:      order.Leverage,
		ReduceOnly:    order.ReduceOnly,
		ClientOrderID: order.ClientOrderID,
		Mode:          mode,
	})
	if err != nil {
		order.Status = store.OrderStatusRejected
		order.Rejection = errorCode(err, "LIVE_EXCHANGE_ORDER_REJECTED")
		_ = s.repo.Save(ctx, order)
		return err
	}
	return s.ApplyExchangeOrderResult(ctx, account, order.ID, result)
}

func (s *TradingService) ReconcileExchangeOrder(ctx context.Context, accountID, orderID string) error {
	if s.exchange == nil {
		return domain.ConflictError("LIVE_EXCHANGE_EXECUTION_UNSUPPORTED", "exchange execution adapter is not configured")
	}
	account, err := s.repo.FindAccount(ctx, accountID)
	if err != nil {
		return domain.NotFoundError("ACCOUNT_NOT_FOUND", "account not found")
	}
	order, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if order.AccountID != accountID {
		return domain.ForbiddenError("order does not belong to account")
	}
	result, err := s.exchange.GetOrder(ctx, *account, *order)
	if err != nil {
		return err
	}
	return s.ApplyExchangeOrderResult(ctx, *account, orderID, result)
}

func (s *TradingService) ApplyExchangeOrderResult(ctx context.Context, account store.Account, orderID string, result *ExchangeOrderResult) error {
	if result == nil {
		return domain.ValidationError("INVALID_EXCHANGE_RESULT", "exchange order result is required")
	}
	order, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return err
	}
	order.ExchangeOrderID = result.ExchangeOrderID
	order.ExchangeStatus = result.Status
	if result.RejectionCode != "" || result.Status == store.OrderStatusRejected {
		order.Status = store.OrderStatusRejected
		order.Rejection = result.RejectionCode
		if err := s.repo.Save(ctx, order); err != nil {
			return err
		}
		s.publishAuditEvent(ctx, "live.order.rejected", order.SandboxID, account.ID, order.ID, map[string]any{"symbol": order.Symbol, "code": result.RejectionCode})
		return nil
	}
	switch result.Status {
	case store.OrderStatusFilled:
		fillPrice := result.AvgFillPrice
		if fillPrice <= 0 {
			fillPrice = order.Price
		}
		if fillPrice <= 0 {
			return domain.ValidationError("INVALID_EXCHANGE_FILL_PRICE", "filled exchange order requires a positive fill price")
		}
		if order.Status != store.OrderStatusFilled {
			if err := s.fillOrder(ctx, order.ID, fillPrice, false); err != nil {
				return err
			}
		}
		updated, err := s.GetOrder(ctx, order.ID)
		if err != nil {
			return err
		}
		updated.ExchangeOrderID = result.ExchangeOrderID
		updated.ExchangeStatus = result.Status
		if err := s.repo.Save(ctx, updated); err != nil {
			return err
		}
		s.publishAuditEvent(ctx, "live.order.filled", updated.SandboxID, account.ID, updated.ID, map[string]any{"symbol": updated.Symbol, "exchange_order_id": updated.ExchangeOrderID})
	case store.OrderStatusPartiallyFilled:
		order.Status = store.OrderStatusPartiallyFilled
		order.FilledQty = result.FilledQty
		order.AvgFillPrice = result.AvgFillPrice
		if err := s.repo.Save(ctx, order); err != nil {
			return err
		}
	default:
		if result.Status != "" {
			order.Status = result.Status
		}
		if err := s.repo.Save(ctx, order); err != nil {
			return err
		}
	}
	s.publishAuditEvent(ctx, "live.order.reconciled", order.SandboxID, account.ID, order.ID, map[string]any{"symbol": order.Symbol, "exchange_order_id": result.ExchangeOrderID, "status": result.Status})
	return nil
}

func (s *TradingService) publishAuditEvent(ctx context.Context, topic, sandboxID, accountID, aggregateID string, payload map[string]any) {
	if s.events == nil {
		return
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: topic, SandboxID: sandboxID, AccountID: accountID, AggregateID: aggregateID, Payload: payload})
}

func (s *TradingService) RefreshAccount(ctx context.Context, accountID string) error {
	account, err := s.repo.FindAccount(ctx, accountID)
	if err != nil {
		return err
	}
	positions, err := s.repo.ListPositionsByAccount(ctx, accountID)
	if err != nil {
		return err
	}
	totalUnrealized := 0.0
	for i := range positions {
		var price float64
		var err error
		if account.Type == store.AccountTypeLive && s.liveMarket != nil {
			var snapshot domain.MarketSnapshot
			snapshot, err = s.liveMarket.GetSnapshot(ctx, positions[i].Symbol)
			price = snapshot.LastPrice
		} else {
			price, _, err = s.market.GetLastPrice(ctx, positions[i].SandboxID, positions[i].Symbol)
		}
		if err != nil {
			price = positions[i].MarkPrice
		}
		positions[i].MarkPrice = price
		positions[i].UnrealizedPnL = realizedPnL(positions[i].PositionSide, positions[i].EntryPrice, price, positions[i].Quantity)
		totalUnrealized += positions[i].UnrealizedPnL
		if err := s.repo.Save(ctx, &positions[i]); err != nil {
			return err
		}
	}
	s.ledger.Reprice(&account.UnrealizedPnL, &account.Equity, &account.AvailableBalance, account.WalletBalance, account.LockedMargin, totalUnrealized)
	if err := s.repo.Save(ctx, account); err != nil {
		return err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "account.balance.updated", AccountID: accountID, AggregateID: accountID, Payload: map[string]any{"available_balance": account.AvailableBalance}})
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "account.equity.updated", AccountID: accountID, AggregateID: accountID, Payload: map[string]any{"equity": account.Equity}})
	return nil
}

func (s *TradingService) ProcessSandbox(ctx context.Context, sandboxID string) error {
	orders, err := s.repo.ListPendingOrdersBySandbox(ctx, sandboxID)
	if err != nil {
		return err
	}
	sort.SliceStable(orders, func(i, j int) bool {
		return orders[i].CreatedAt.Before(orders[j].CreatedAt)
	})
	for _, order := range orders {
		marketPrice, _, err := s.market.GetLastPrice(ctx, sandboxID, order.Symbol)
		if err != nil {
			continue
		}
		switch order.OrderType {
		case store.OrderTypeLimit:
			if order.Status == store.OrderStatusNew && s.fillPolicy.LimitShouldFill(order.Side, marketPrice, order.Price) {
				if err := s.fillOrder(ctx, order.ID, marketPrice, false); err != nil {
					return err
				}
			}
		case store.OrderTypeStop:
			if order.Status == store.OrderStatusNew && s.fillPolicy.StopShouldTrigger(order.PositionSide, marketPrice, order.StopPrice) {
				if err := s.fillOrder(ctx, order.ID, marketPrice, true); err != nil {
					return err
				}
			}
		}
	}
	accountRows, err := s.repo.ListAccountsBySandbox(ctx, sandboxID)
	if err != nil {
		return err
	}
	for _, account := range accountRows {
		if err := s.RefreshAccount(ctx, account.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *TradingService) CancelOrder(ctx context.Context, accountID, orderID string) (*store.Order, error) {
	order, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.AccountID != accountID {
		return nil, domain.ForbiddenError("order does not belong to account")
	}
	if order.Status != store.OrderStatusNew && order.Status != store.OrderStatusTriggered {
		return nil, domain.ConflictError("ORDER_NOT_CANCELABLE", "order cannot be canceled")
	}
	order.Status = store.OrderStatusCanceled
	if err := s.repo.Save(ctx, order); err != nil {
		return nil, err
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "order.updated", SandboxID: order.SandboxID, AccountID: order.AccountID, AggregateID: order.ID, Payload: map[string]any{"status": order.Status}})
	return order, nil
}

func (s *TradingService) GetOrder(ctx context.Context, orderID string) (*store.Order, error) {
	var order store.Order
	if err := s.repo.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return nil, domain.NotFoundError("ORDER_NOT_FOUND", "order not found")
	}
	return &order, nil
}

func (s *TradingService) ListOrders(ctx context.Context, accountID string) ([]store.Order, error) {
	return s.repo.ListOrdersByAccount(ctx, accountID)
}

func (s *TradingService) ListTrades(ctx context.Context, accountID string) ([]store.Trade, error) {
	return s.repo.ListTradesByAccount(ctx, accountID)
}

func (s *TradingService) ListPositions(ctx context.Context, accountID string) ([]store.Position, error) {
	positions, err := s.repo.ListPositionsByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	filtered := positions[:0]
	for _, position := range positions {
		if position.Quantity > 0 {
			filtered = append(filtered, position)
		}
	}
	return filtered, nil
}

func (s *TradingService) GetAccountSummary(ctx context.Context, accountID string) (*store.Account, error) {
	if err := s.RefreshAccount(ctx, accountID); err != nil {
		return nil, err
	}
	return s.repo.FindAccount(ctx, accountID)
}

func (s *TradingService) GetPerformance(ctx context.Context, accountID string) (*AccountPerformance, error) {
	account, err := s.GetAccountSummary(ctx, accountID)
	if err != nil {
		return nil, err
	}
	perf := &AccountPerformance{
		AccountID:      account.ID,
		Equity:         account.Equity,
		RealizedPnL:    account.RealizedPnL,
		UnrealizedPnL:  account.UnrealizedPnL,
		WalletBalance:  account.WalletBalance,
		AvailableFunds: account.AvailableBalance,
	}
	if account.InitialBalance > 0 {
		perf.ReturnPct = (account.Equity - account.InitialBalance) / account.InitialBalance * 100
	}
	return perf, nil
}

func isOpeningAction(side, positionSide string) bool {
	return (positionSide == store.PositionSideLong && side == store.OrderSideBuy) ||
		(positionSide == store.PositionSideShort && side == store.OrderSideSell)
}

func realizedPnL(positionSide string, entryPrice, exitPrice, quantity float64) float64 {
	switch positionSide {
	case store.PositionSideLong:
		return (exitPrice - entryPrice) * quantity
	case store.PositionSideShort:
		return (entryPrice - exitPrice) * quantity
	default:
		return 0
	}
}

func mustTimePtr(t time.Time) *time.Time {
	value := t.UTC()
	return &value
}
