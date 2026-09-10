package database

import "time"

const (
	AccountActive   = "active"
	AccountDisabled = "disabled"

	ProductSpot    = "spot"
	ProductFutures = "futures"

	SideBuy  = "buy"
	SideSell = "sell"

	Long  = "long"
	Short = "short"

	OrderMarket = "market"
	OrderLimit  = "limit"

	OrderOpen     = "open"
	OrderFilled   = "filled"
	OrderCanceled = "canceled"

	RoleMaker = "maker"
	RoleTaker = "taker"
)

// Account 是一個虛擬交易帳號。
//
// Token 以明文保存：規格 §1 要求 USER 能「查看 / Reset Account Token」，
// 雜湊後就只能重設、無法查看。
type Account struct {
	ID             string  `gorm:"primaryKey;size:64"`
	UserName       string  `gorm:"size:64;not null;uniqueIndex"`
	Token          string  `gorm:"size:128;not null;uniqueIndex"`
	InitialBalance float64 `gorm:"not null"`
	Balance        float64 `gorm:"not null"`
	Status         string  `gorm:"size:16;not null;index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (a *Account) Active() bool {
	return a.Status == AccountActive
}

type Order struct {
	ID        string `gorm:"primaryKey;size:64"`
	AccountID string `gorm:"size:64;not null;index"`
	Market    string `gorm:"size:16;not null"`
	Symbol    string `gorm:"size:32;not null"`
	Product   string `gorm:"size:16;not null"`
	Side      string `gorm:"size:8;not null"`
	Type      string `gorm:"size:8;not null"`

	Quantity   float64 `gorm:"not null"`
	Price      float64
	Leverage   float64 `gorm:"not null"`
	StopLoss   float64
	TakeProfit float64

	Status         string `gorm:"size:16;not null;index"`
	FilledQuantity float64
	AvgFillPrice   float64
	Fee            float64
	RealizedPnL    float64
	RejectReason   string `gorm:"size:128"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Position 的唯一鍵是 帳號+市場+標的+產品：同一個標的只有一個淨部位，
// 反向下單先減倉，歸零後才反向開倉。
type Position struct {
	ID        string `gorm:"primaryKey;size:64"`
	AccountID string `gorm:"size:64;not null;index:idx_position_key,unique,priority:1"`
	Market    string `gorm:"size:16;not null;index:idx_position_key,unique,priority:2"`
	Symbol    string `gorm:"size:32;not null;index:idx_position_key,unique,priority:3"`
	Product   string `gorm:"size:16;not null;index:idx_position_key,unique,priority:4"`

	Side       string  `gorm:"size:8;not null"`
	Quantity   float64 `gorm:"not null"`
	EntryPrice float64 `gorm:"not null"`
	Leverage   float64 `gorm:"not null"`
	Margin     float64 `gorm:"not null"`
	StopLoss   float64
	TakeProfit float64

	CreatedAt time.Time
	UpdatedAt time.Time
}

type Trade struct {
	ID        string `gorm:"primaryKey;size:64"`
	AccountID string `gorm:"size:64;not null;index"`
	OrderID   string `gorm:"size:64;not null;index"`
	Market    string `gorm:"size:16;not null"`
	Symbol    string `gorm:"size:32;not null"`
	Product   string `gorm:"size:16;not null"`
	Side      string `gorm:"size:8;not null"`
	Role      string `gorm:"size:8;not null"`

	Quantity    float64 `gorm:"not null"`
	Price       float64 `gorm:"not null"`
	Fee         float64
	RealizedPnL float64

	CreatedAt time.Time
}
