package store

import "time"

type Wallet struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	RunID     string    `gorm:"size:64;not null;uniqueIndex:idx_wallet_run_owner_asset,priority:1;index" json:"run_id"`
	OwnerID   uint      `gorm:"not null;uniqueIndex:idx_wallet_run_owner_asset,priority:2;index" json:"owner_id"`
	Asset     string    `gorm:"size:16;not null;uniqueIndex:idx_wallet_run_owner_asset,priority:3;index" json:"asset"`
	Balance   float64   `gorm:"not null" json:"balance"`
	Available float64   `gorm:"not null" json:"available"`
	Frozen    float64   `gorm:"not null" json:"frozen"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type LedgerEntry struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	RunID        string    `gorm:"size:64;not null;index" json:"run_id"`
	OwnerID      uint      `gorm:"not null;index" json:"owner_id"`
	Asset        string    `gorm:"size:16;not null;index" json:"asset"`
	EntryType    string    `gorm:"size:32;not null;index" json:"entry_type"`
	Amount       float64   `gorm:"not null" json:"amount"`
	BalanceAfter float64   `gorm:"not null" json:"balance_after"`
	OrderID      *uint     `gorm:"index" json:"order_id"`
	FillID       *uint     `gorm:"index" json:"fill_id"`
	BarIndex     *int      `json:"bar_index"`
	Note         string    `gorm:"size:255" json:"note"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Order struct {
	ID                uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	RunID             string     `gorm:"size:64;not null;uniqueIndex:idx_order_run_client,priority:1;index" json:"run_id"`
	OwnerID           uint       `gorm:"not null;index" json:"owner_id"`
	ClientOrderID     string     `gorm:"size:64;not null;uniqueIndex:idx_order_run_client,priority:2" json:"client_order_id"`
	Symbol            string     `gorm:"size:32;not null;index" json:"symbol"`
	Side              string     `gorm:"size:8;not null;index" json:"side"`
	Type              string     `gorm:"size:16;not null;index" json:"type"`
	TimeInForce       string     `gorm:"size:16" json:"time_in_force"`
	Status            string     `gorm:"size:16;not null;index" json:"status"`
	Quantity          float64    `gorm:"not null" json:"quantity"`
	FilledQuantity    float64    `gorm:"not null" json:"filled_quantity"`
	Price             *float64   `json:"price"`
	ReservedAsset     string     `gorm:"size:16;not null" json:"reserved_asset"`
	ReservedAmount    float64    `gorm:"not null" json:"reserved_amount"`
	AverageFillPrice  float64    `gorm:"not null" json:"average_fill_price"`
	SubmittedBarIndex int        `gorm:"not null" json:"submitted_bar_index"`
	EligibleBarIndex  *int       `json:"eligible_bar_index"`
	CanceledAt        *time.Time `json:"canceled_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Fill struct {
	ID                uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	RunID             string    `gorm:"size:64;not null;index" json:"run_id"`
	OrderID           uint      `gorm:"not null;index" json:"order_id"`
	OwnerID           uint      `gorm:"not null;index" json:"owner_id"`
	Symbol            string    `gorm:"size:32;not null;index" json:"symbol"`
	Side              string    `gorm:"size:8;not null;index" json:"side"`
	Price             float64   `gorm:"not null" json:"price"`
	Quantity          float64   `gorm:"not null" json:"quantity"`
	QuoteQuantity     float64   `gorm:"not null" json:"quote_quantity"`
	FeeAsset          string    `gorm:"size:16;not null" json:"fee_asset"`
	FeeAmount         float64   `gorm:"not null" json:"fee_amount"`
	SlippageBps       float64   `gorm:"not null" json:"slippage_bps"`
	ExecutionBarIndex int       `gorm:"not null;index" json:"execution_bar_index"`
	ExecutionTime     time.Time `gorm:"not null;index" json:"execution_time"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
