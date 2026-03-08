package store

import "time"

type MarketScenario struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ScenarioID  string    `gorm:"size:128;not null;uniqueIndex" json:"scenario_id"`
	Symbol      string    `gorm:"size:32;not null;index" json:"symbol"`
	Interval    string    `gorm:"size:16;not null" json:"interval"`
	SourceType  string    `gorm:"size:32;not null" json:"source_type"`
	DatasetHash string    `gorm:"size:128;not null;index" json:"dataset_hash"`
	BarCount    int       `gorm:"not null" json:"bar_count"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SymbolConfig struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Symbol       string    `gorm:"size:32;not null;uniqueIndex" json:"symbol"`
	BaseAsset    string    `gorm:"size:16;not null" json:"base_asset"`
	QuoteAsset   string    `gorm:"size:16;not null" json:"quote_asset"`
	PriceTick    float64   `gorm:"not null" json:"price_tick"`
	QuantityStep float64   `gorm:"not null" json:"quantity_step"`
	MinQuantity  float64   `gorm:"not null" json:"min_quantity"`
	MinNotional  float64   `gorm:"not null" json:"min_notional"`
	MakerFeeBps  float64   `gorm:"not null" json:"maker_fee_bps"`
	TakerFeeBps  float64   `gorm:"not null" json:"taker_fee_bps"`
	IsActive     bool      `gorm:"not null;default:true" json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Bar struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ScenarioID string    `gorm:"size:128;not null;index:idx_scenario_bar,priority:1;index" json:"scenario_id"`
	Symbol     string    `gorm:"size:32;not null;index" json:"symbol"`
	Interval   string    `gorm:"size:16;not null" json:"interval"`
	BarIndex   int       `gorm:"not null;uniqueIndex:idx_scenario_bar,priority:2" json:"bar_index"`
	OpenTime   time.Time `gorm:"not null;index" json:"open_time"`
	Open       float64   `gorm:"not null" json:"open"`
	High       float64   `gorm:"not null" json:"high"`
	Low        float64   `gorm:"not null" json:"low"`
	Close      float64   `gorm:"not null" json:"close"`
	Volume     float64   `gorm:"not null" json:"volume"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
