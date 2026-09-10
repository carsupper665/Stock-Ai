package market

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 支援的市場。
const (
	Crypto = "crypto"
	Stock  = "stock"
)

var (
	// ErrUnknownMarket 表示沒有註冊這個市場的行情來源。
	ErrUnknownMarket = errors.New("未知的市場")
	// ErrEmptySymbol 表示沒有指定標的。
	ErrEmptySymbol = errors.New("symbol 不可為空")
	// ErrTimeout 表示等不到新報價。
	ErrTimeout = errors.New("等待報價逾時")
	// ErrClosed 表示 Runtime 已經關閉。
	ErrClosed = errors.New("market runtime 已關閉")
)

// Price 是一筆報價快照。
type Price struct {
	Market    string    `json:"market"`
	Symbol    string    `json:"symbol"`
	Price     float64   `json:"price"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Source 是單一市場的行情來源。
//
// Stream 必須持續把 symbol 的最新價送進 out，直到 ctx 結束或發生錯誤才回傳。
// ctx 被取消時回傳 ctx.Err() 即可，Runtime 會照常收尾。
//
// Runtime 會一直讀 out 直到 Stream 回傳，所以送值不會卡住；
// 但 Stream 回傳之後就不可以再送。
type Source interface {
	// Name 用於日誌，例如 "binance"。
	Name() string
	Stream(ctx context.Context, symbol string, out chan<- float64) error
}

// State 是一個標的的訂閱狀態，對應規格 §10。
type State string

const (
	StateInactive   State = "inactive"
	StateActivating State = "activating"
	StateActive     State = "active"
)

// normalize 統一市場與標的的寫法：市場小寫、標的大寫，兩者都去空白。
func normalize(market, symbol string) (string, string, error) {
	market = strings.ToLower(strings.TrimSpace(market))
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return "", "", ErrEmptySymbol
	}
	if market == "" {
		return "", "", fmt.Errorf("%w: market 不可為空", ErrUnknownMarket)
	}
	return market, symbol, nil
}

func key(market, symbol string) string {
	return market + ":" + symbol
}
