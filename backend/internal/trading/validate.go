package trading

import (
	"errors"
	"fmt"
	"strings"

	"backend/internal/database"
	"backend/internal/market"
)

var (
	ErrInvalidProduct  = errors.New("product 只能是 spot 或 futures")
	ErrInvalidSide     = errors.New("side 只能是 buy 或 sell")
	ErrInvalidType     = errors.New("type 只能是 market 或 limit")
	ErrInvalidQuantity = errors.New("quantity 必須大於 0")
	ErrInvalidPrice    = errors.New("限價單必須指定大於 0 的 price")
	ErrInvalidLeverage = errors.New("leverage 必須介於 1 到 100")
	ErrSpotLeverage    = errors.New("spot 不支援槓桿，leverage 只能是 1")
	ErrSpotShort       = errors.New("spot 不支援放空，賣出數量不可超過持倉")
	ErrInvalidStopLoss = errors.New("多單的 stop_loss 必須低於進場價、take_profit 必須高於進場價；空單相反")
	ErrAccountDisabled = errors.New("帳號已停用")
	ErrInsufficient    = errors.New("可用餘額不足")
	ErrPositionClosed  = errors.New("部位已不存在")
	ErrCloseTooMuch    = errors.New("平倉數量超過持倉")
	ErrLeverageLocked  = errors.New("已有持倉時不能改槓桿，請先平倉")
	ErrNoStopChange    = errors.New("沒有指定要修改的 stop_loss 或 take_profit")
)

const maxLeverage = 100

type PlaceOrderInput struct {
	Market     string
	Symbol     string
	Product    string
	Side       string
	Type       string
	Quantity   float64
	Price      float64
	Leverage   float64
	StopLoss   float64
	TakeProfit float64
}

func (in *PlaceOrderInput) normalize() error {
	in.Market = strings.ToLower(strings.TrimSpace(in.Market))
	if in.Market == "" {
		in.Market = market.Crypto
	}
	in.Symbol = strings.ToUpper(strings.TrimSpace(in.Symbol))
	in.Product = strings.ToLower(strings.TrimSpace(in.Product))
	if in.Product == "" {
		in.Product = database.ProductFutures
	}
	in.Side = strings.ToLower(strings.TrimSpace(in.Side))
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	if in.Type == "" {
		in.Type = database.OrderMarket
	}
	if in.Leverage == 0 {
		in.Leverage = 1
	}

	if in.Symbol == "" {
		return market.ErrEmptySymbol
	}
	if in.Product != database.ProductSpot && in.Product != database.ProductFutures {
		return ErrInvalidProduct
	}
	if in.Side != database.SideBuy && in.Side != database.SideSell {
		return ErrInvalidSide
	}
	if in.Type != database.OrderMarket && in.Type != database.OrderLimit {
		return ErrInvalidType
	}
	if in.Quantity <= 0 {
		return ErrInvalidQuantity
	}
	if in.Type == database.OrderLimit && in.Price <= 0 {
		return ErrInvalidPrice
	}
	if in.Leverage < 1 || in.Leverage > maxLeverage {
		return ErrInvalidLeverage
	}
	if in.Product == database.ProductSpot && in.Leverage != 1 {
		return ErrSpotLeverage
	}
	if in.StopLoss < 0 || in.TakeProfit < 0 {
		return fmt.Errorf("%w: stop_loss 與 take_profit 不可為負數", ErrInvalidStopLoss)
	}
	return nil
}

// checkStopLevels 確認停損停利落在正確的一側，避免掛上去就立刻觸發。
func checkStopLevels(side string, reference, stopLoss, takeProfit float64) error {
	long := side == database.SideBuy
	switch {
	case long && stopLoss > 0 && stopLoss >= reference:
		return ErrInvalidStopLoss
	case long && takeProfit > 0 && takeProfit <= reference:
		return ErrInvalidStopLoss
	case !long && stopLoss > 0 && stopLoss <= reference:
		return ErrInvalidStopLoss
	case !long && takeProfit > 0 && takeProfit >= reference:
		return ErrInvalidStopLoss
	}
	return nil
}

// checkSpotShort 擋下會讓 spot 變成負部位的賣單。
func checkSpotShort(product, side string, pos *database.Position, qty float64) error {
	if product != database.ProductSpot || side != database.SideSell {
		return nil
	}
	held := 0.0
	if pos != nil && pos.Side == database.Long {
		held = pos.Quantity
	}
	if qty > held+qtyEpsilon {
		return ErrSpotShort
	}
	return nil
}
