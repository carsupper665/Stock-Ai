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
	OrderRejected = "rejected"

	RoleMaker = "maker"
	RoleTaker = "taker"

	AuthorUser    = "user"
	AuthorAccount = "account"

	LedgerOrderPlaced   = "order_placed"
	LedgerFill          = "fill"
	LedgerOrderRejected = "order_rejected"
	LedgerOrderCanceled = "order_canceled"
	LedgerStopsUpdated  = "stops_updated"
)

// Source 是造成帳戶變動的 Agent Session／Run。非 Agent 操作與舊資料為零值，
// 對外顯示為 null，不為舊資料虛構 run_id。
type Source struct {
	SessionID string `gorm:"size:64"`
	RunID     int64
}

func (s Source) Known() bool {
	return s.SessionID != "" && s.RunID > 0
}

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
	// ReduceOnly 的單只能減倉，部位不夠就拒絕，不會反向開倉。
	// 平倉與停損停利觸發都是這種單。
	ReduceOnly bool

	Status         string `gorm:"size:16;not null;index"`
	FilledQuantity float64
	AvgFillPrice   float64
	Fee            float64
	RealizedPnL    float64
	RejectReason   string `gorm:"size:128"`

	// Source 是建立這張單的 Run；Trigger 標記由停損停利自動產生的平倉單。
	Source  Source `gorm:"embedded;embeddedPrefix:source_"`
	Trigger string `gorm:"size:16"`

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

	// 停損與停利各自記住設定它的 Run，改其中一個不覆寫另一個。
	StopLossSource   Source `gorm:"embedded;embeddedPrefix:stop_loss_source_"`
	TakeProfitSource Source `gorm:"embedded;embeddedPrefix:take_profit_source_"`

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
	Source      Source `gorm:"embedded;embeddedPrefix:source_"`

	CreatedAt time.Time
}

// LedgerEntry 是一筆可追溯的帳戶事件。只有 fill 會改變餘額（BalanceDelta = 已實現損益 − 手續費）；
// 其餘事件記錄掛單、拒絕、撤單與停損停利變更的來源。Seq 遞增，作為分頁游標。
type LedgerEntry struct {
	Seq        int64  `gorm:"primaryKey;autoIncrement"`
	AccountID  string `gorm:"size:64;not null;index"`
	Event      string `gorm:"size:32;not null"`
	OrderID    string `gorm:"size:64"`
	TradeID    string `gorm:"size:64"`
	PositionID string `gorm:"size:64"`
	Trigger    string `gorm:"size:16"`

	Quantity     float64
	Price        float64
	Fee          float64
	RealizedPnL  float64
	BalanceDelta float64
	BalanceAfter float64
	StopLoss     float64
	TakeProfit   float64

	Source    Source `gorm:"embedded;embeddedPrefix:source_"`
	CreatedAt time.Time
}

// Message 只存真實作者，"you" 是查詢時依請求者身分算出來的（規格 §13）。
// USER 沒有 id，AuthorID 留空、以 AuthorType 區分。
type Message struct {
	ID         string    `gorm:"primaryKey;size:64"`
	AuthorType string    `gorm:"size:16;not null"`
	AuthorID   string    `gorm:"size:64;not null;index"`
	Content    string    `gorm:"size:2000;not null"`
	Tags       []string  `gorm:"-"`
	CreatedAt  time.Time `gorm:"index"`
}

type MessageTag struct {
	MessageID string `gorm:"primaryKey;size:64"`
	Position  int    `gorm:"primaryKey;autoIncrement:false"`
	Tag       string `gorm:"not null"`
}
