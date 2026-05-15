package domain

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type AppError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Status  int            `json:"-"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *AppError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func NewError(status int, code, message string, details map[string]any) *AppError {
	return &AppError{Status: status, Code: code, Message: message, Details: details}
}

func ValidationError(code, message string) *AppError {
	return NewError(http.StatusBadRequest, code, message, nil)
}

func UnauthorizedError(message string) *AppError {
	return NewError(http.StatusUnauthorized, "UNAUTHORIZED", message, nil)
}

func ForbiddenError(message string) *AppError {
	return NewError(http.StatusForbidden, "FORBIDDEN", message, nil)
}

func NotFoundError(code, message string) *AppError {
	return NewError(http.StatusNotFound, code, message, nil)
}

func ConflictError(code, message string) *AppError {
	return NewError(http.StatusConflict, code, message, nil)
}

type MarketTicker struct {
	Symbol string    `json:"symbol"`
	Price  float64   `json:"price"`
	At     time.Time `json:"at"`
}

type MarketState string

const (
	MarketStateActive   MarketState = "active"
	MarketStateStale    MarketState = "stale"
	MarketStateDegraded MarketState = "degraded"
)

type MarketSnapshot struct {
	Symbol      string      `json:"symbol"`
	Price       float64     `json:"price"`
	LastPrice   float64     `json:"last_price"`
	MarkPrice   *float64    `json:"mark_price,omitempty"`
	BidPrice    *float64    `json:"bid_price,omitempty"`
	BidQty      *float64    `json:"bid_qty,omitempty"`
	AskPrice    *float64    `json:"ask_price,omitempty"`
	AskQty      *float64    `json:"ask_qty,omitempty"`
	Spread      *float64    `json:"spread,omitempty"`
	At          time.Time   `json:"at"`
	ReceivedAt  time.Time   `json:"received_at"`
	FreshnessMs int64       `json:"freshness_ms"`
	Provider    string      `json:"provider"`
	State       MarketState `json:"state"`
	ErrorCode   string      `json:"error_code,omitempty"`
}

type SymbolRule struct {
	Symbol      string  `json:"symbol"`
	TickSize    float64 `json:"tick_size"`
	StepSize    float64 `json:"step_size"`
	MinQty      float64 `json:"min_qty"`
	MinNotional float64 `json:"min_notional"`
	MaxLeverage float64 `json:"max_leverage"`
}

type Kline struct {
	Symbol string    `json:"symbol"`
	At     time.Time `json:"at"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

type DomainEvent struct {
	Topic       string         `json:"topic"`
	SandboxID   string         `json:"sandbox_id,omitempty"`
	AccountID   string         `json:"account_id,omitempty"`
	AggregateID string         `json:"aggregate_id,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type MarketDataProvider interface {
	GetTicker(ctx context.Context, sandboxID, symbol string) (MarketTicker, error)
	GetLastPrice(ctx context.Context, sandboxID, symbol string) (float64, time.Time, error)
	GetKlines(ctx context.Context, sandboxID, symbol, interval string, from, to time.Time) ([]Kline, error)
}

type ExecutionEngine interface {
	ExecuteOrder(ctx context.Context, req ExecuteOrderRequest) (*ExecutionResult, error)
	CancelOrder(ctx context.Context, orderID string) error
}

type ExecuteOrderRequest struct {
	OrderID string
}

type ExecutionResult struct {
	Filled bool
}

type RiskEngine interface {
	ValidateNewOrder(ctx context.Context, req RiskOrderRequest) error
}

type RiskOrderRequest struct {
	WalletBalance    float64
	AvailableBalance float64
	CurrentQuantity  float64
	MarketPrice      float64
	OrderType        string
	Side             string
	PositionSide     string
	Quantity         float64
	LimitPrice       float64
	StopPrice        float64
	Leverage         float64
	SandboxStatus    string
	Tradable         bool
}

type AccountLedger interface {
	ReserveMargin(wallet, locked *float64, amount float64) error
	ReleaseMargin(wallet, locked *float64, amount float64)
	ApplyRealizedPnL(wallet, realized *float64, pnl float64)
	Reprice(unrealized, equity, available *float64, walletBalance, lockedMargin, totalUnrealized float64)
}

type EventPublisher interface {
	Publish(ctx context.Context, event DomainEvent) error
}

type EventSubscriber interface {
	Subscribe(buffer int, fn func(DomainEvent) bool) (<-chan DomainEvent, func())
}

type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string, requiredScopes []string) (accountID string, err error)
}

type Clock interface {
	Now() time.Time
}

type FillPolicy interface {
	LimitShouldFill(side string, marketPrice, limitPrice float64) bool
	StopShouldTrigger(positionSide string, marketPrice, stopPrice float64) bool
}
