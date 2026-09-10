// Package stock 是美股行情來源的位置。
//
// 規格 §4 要求支援美股，但還沒選定 provider，所以這裡先放一個
// 會明確報錯的實作。Market Runtime 的快取與訂閱邏輯跟 provider 無關，
// 之後接上真正的來源時不需要改動 Runtime。
package stock

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotImplemented 表示美股行情尚未接上。
var ErrNotImplemented = errors.New("美股行情來源尚未實作")

// Unavailable 是佔位用的來源：任何訂閱都立刻失敗。
// 這樣行為是明確的錯誤，而不是無聲地等到逾時。
type Unavailable struct{}

func New() *Unavailable { return &Unavailable{} }

func (Unavailable) Name() string { return "unavailable" }

func (Unavailable) Stream(_ context.Context, symbol string, _ chan<- float64) error {
	return fmt.Errorf("%w（symbol=%s）", ErrNotImplemented, symbol)
}
