package test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The legacy tables mirror the pre-Ticket-22 schema: no source columns and no ledger.
type legacyOrder struct {
	ID             string `gorm:"primaryKey;size:64"`
	AccountID      string `gorm:"size:64;not null;index"`
	Market         string `gorm:"size:16;not null"`
	Symbol         string `gorm:"size:32;not null"`
	Product        string `gorm:"size:16;not null"`
	Side           string `gorm:"size:8;not null"`
	Type           string `gorm:"size:8;not null"`
	Quantity       float64
	Price          float64
	Leverage       float64
	StopLoss       float64
	TakeProfit     float64
	ReduceOnly     bool
	Status         string `gorm:"size:16;not null;index"`
	FilledQuantity float64
	AvgFillPrice   float64
	Fee            float64
	RealizedPnL    float64
	RejectReason   string `gorm:"size:128"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (legacyOrder) TableName() string { return "orders" }

type legacyPosition struct {
	ID         string `gorm:"primaryKey;size:64"`
	AccountID  string `gorm:"size:64;not null"`
	Market     string `gorm:"size:16;not null"`
	Symbol     string `gorm:"size:32;not null"`
	Product    string `gorm:"size:16;not null"`
	Side       string `gorm:"size:8;not null"`
	Quantity   float64
	EntryPrice float64
	Leverage   float64
	Margin     float64
	StopLoss   float64
	TakeProfit float64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (legacyPosition) TableName() string { return "positions" }

type legacyTrade struct {
	ID          string `gorm:"primaryKey;size:64"`
	AccountID   string `gorm:"size:64;not null;index"`
	OrderID     string `gorm:"size:64;not null;index"`
	Market      string `gorm:"size:16;not null"`
	Symbol      string `gorm:"size:32;not null"`
	Product     string `gorm:"size:16;not null"`
	Side        string `gorm:"size:8;not null"`
	Role        string `gorm:"size:8;not null"`
	Quantity    float64
	Price       float64
	Fee         float64
	RealizedPnL float64
	CreatedAt   time.Time
}

func (legacyTrade) TableName() string { return "trades" }

func TestTicket22MigrationKeepsLegacyRowsWithoutInventedSource(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if err := legacy.AutoMigrate(&database.Account{}, &legacyOrder{}, &legacyPosition{}, &legacyTrade{}); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	opened := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []any{
		&database.Account{ID: "acc_legacy", UserName: "legacy", Token: "at_legacy", InitialBalance: 10000, Balance: 9997.6, Status: database.AccountActive},
		&legacyOrder{ID: "ord_legacy", AccountID: "acc_legacy", Market: "crypto", Symbol: "BTCUSDT", Product: "futures", Side: "buy", Type: "market",
			Quantity: 0.1, Leverage: 10, Status: "filled", FilledQuantity: 0.1, AvgFillPrice: 60000, Fee: 2.4, CreatedAt: opened, UpdatedAt: opened},
		&legacyTrade{ID: "trd_legacy", AccountID: "acc_legacy", OrderID: "ord_legacy", Market: "crypto", Symbol: "BTCUSDT", Product: "futures",
			Side: "buy", Role: "taker", Quantity: 0.1, Price: 60000, Fee: 2.4, CreatedAt: opened},
		&legacyPosition{ID: "pos_legacy", AccountID: "acc_legacy", Market: "crypto", Symbol: "BTCUSDT", Product: "futures", Side: "long",
			Quantity: 0.1, EntryPrice: 60000, Leverage: 10, Margin: 600, StopLoss: 55000, CreatedAt: opened, UpdatedAt: opened},
	}
	for _, row := range rows {
		if err := legacy.Create(row).Error; err != nil {
			t.Fatalf("insert legacy row %T: %v", row, err)
		}
	}
	if sqlDB, err := legacy.DB(); err == nil {
		_ = sqlDB.Close()
	}

	backend := newTradingAPI(t, dbPath)
	status, raw := backend.request(t, http.MethodGet, "/v1/orders", "at_legacy", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"id":"ord_legacy"`) || !strings.Contains(string(raw), `"source":null`) || strings.Contains(string(raw), `"trigger"`) {
		t.Fatalf("legacy orders = %d %s", status, raw)
	}
	status, raw = backend.request(t, http.MethodGet, "/v1/trades", "at_legacy", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"id":"trd_legacy"`) || !strings.Contains(string(raw), `"source":null`) {
		t.Fatalf("legacy trades = %d %s", status, raw)
	}
	status, raw = backend.request(t, http.MethodGet, "/v1/positions", "at_legacy", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"id":"pos_legacy"`) || !strings.Contains(string(raw), `"stop_loss":55000`) ||
		!strings.Contains(string(raw), `"stop_loss_source":null`) || !strings.Contains(string(raw), `"take_profit_source":null`) {
		t.Fatalf("legacy positions = %d %s", status, raw)
	}
	if entries := backend.ledger(t, "at_legacy", ""); len(entries) != 0 {
		t.Fatalf("legacy data must not gain invented ledger entries: %+v", entries)
	}

	backend.setPrice(t, 61000)
	status, raw = backend.request(t, http.MethodPost, "/v1/positions/pos_legacy/close", "at_legacy", map[string]any{"source": agentSource("s_new", 1)})
	closed := decodeJSON[orderView](t, raw)
	if status != http.StatusOK || closed.Status != "filled" || closed.RealizedPnL != 100 || closed.Source == nil || closed.Source.RunID != 1 {
		t.Fatalf("close legacy position = %d %s", status, raw)
	}
	entries := backend.ledger(t, "at_legacy", "")
	if len(entries) != 1 || entries[0].Event != "fill" || entries[0].PositionID != "pos_legacy" || entries[0].RealizedPnL != 100 ||
		entries[0].Fee != 2.44 || entries[0].BalanceDelta != 97.56 || entries[0].BalanceAfter != 10095.16 || entries[0].Source == nil {
		t.Fatalf("ledger after closing legacy position = %+v", entries)
	}
}
