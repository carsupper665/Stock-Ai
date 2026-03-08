package sandbox

import (
	"errors"
	"fmt"
	"math"
)

const (
	OrderSideBuy  = "BUY"
	OrderSideSell = "SELL"

	OrderTypeMarket = "MARKET"
	OrderTypeLimit  = "LIMIT"

	OrderStatusOpen     = "OPEN"
	OrderStatusFilled   = "FILLED"
	OrderStatusCanceled = "CANCELED"

	TimeInForceGTC = "GTC"
)

var (
	ErrUnsupportedOrderSide = errors.New("unsupported order side")
	ErrUnsupportedOrderType = errors.New("unsupported order type")
	ErrLimitPriceRequired   = errors.New("limit price required")
)

type MatchInput struct {
	OrderType   string
	Side        string
	Quantity    float64
	LimitPrice  *float64
	Bar         Bar
	SlippageBps float64
}

type MatchDecision struct {
	ShouldFill  bool
	FillPrice   float64
	SlippageBps float64
	IsMaker     bool
}

func DecideFill(input MatchInput) (MatchDecision, error) {
	if input.Quantity <= 0 {
		return MatchDecision{}, fmt.Errorf("quantity must be positive: %v", input.Quantity)
	}

	switch input.Side {
	case OrderSideBuy, OrderSideSell:
	default:
		return MatchDecision{}, fmt.Errorf("%w: %s", ErrUnsupportedOrderSide, input.Side)
	}

	switch input.OrderType {
	case OrderTypeMarket:
		fillPrice, err := ApplyDeterministicSlippage(input.Side, input.Bar.Open, input.SlippageBps)
		if err != nil {
			return MatchDecision{}, err
		}
		return MatchDecision{
			ShouldFill:  true,
			FillPrice:   fillPrice,
			SlippageBps: input.SlippageBps,
			IsMaker:     false,
		}, nil
	case OrderTypeLimit:
		if input.LimitPrice == nil || *input.LimitPrice <= 0 {
			return MatchDecision{}, ErrLimitPriceRequired
		}
		limitPrice := *input.LimitPrice
		switch input.Side {
		case OrderSideBuy:
			if input.Bar.Low <= limitPrice || almostEqual(input.Bar.Low, limitPrice) {
				return MatchDecision{ShouldFill: true, FillPrice: limitPrice, IsMaker: true}, nil
			}
		case OrderSideSell:
			if input.Bar.High >= limitPrice || almostEqual(input.Bar.High, limitPrice) {
				return MatchDecision{ShouldFill: true, FillPrice: limitPrice, IsMaker: true}, nil
			}
		}
		return MatchDecision{}, nil
	default:
		return MatchDecision{}, fmt.Errorf("%w: %s", ErrUnsupportedOrderType, input.OrderType)
	}
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
