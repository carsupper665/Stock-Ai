package sandbox

import (
	"fmt"
)

const DefaultMarketSlippageBps = 5.0

func ApplyDeterministicSlippage(side string, price, bps float64) (float64, error) {
	if price <= 0 {
		return 0, fmt.Errorf("price must be positive: %v", price)
	}
	if bps < 0 {
		return 0, fmt.Errorf("slippage bps must be non-negative: %v", bps)
	}

	multiplier := 1 + (bps / 10000)
	switch side {
	case OrderSideBuy:
		return price * multiplier, nil
	case OrderSideSell:
		return price * (2 - multiplier), nil
	default:
		return 0, fmt.Errorf("%w: %s", ErrUnsupportedOrderSide, side)
	}
}
