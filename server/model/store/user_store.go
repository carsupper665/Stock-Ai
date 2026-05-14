package store

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	RoleRootUser = 6
)

const (
	SandboxModeReplay = "replay"
)

const (
	SandboxStatusDraft     = "draft"
	SandboxStatusReady     = "ready"
	SandboxStatusRunning   = "running"
	SandboxStatusPaused    = "paused"
	SandboxStatusStopped   = "stopped"
	SandboxStatusCompleted = "completed"
)

const (
	AccountTypeVirtual = "virtual"
	AccountTypeLive    = "live"
)

const (
	AccountStatusActive   = "active"
	AccountStatusDisabled = "disabled"
)

const (
	OrderSideBuy  = "buy"
	OrderSideSell = "sell"
)

const (
	PositionSideLong  = "long"
	PositionSideShort = "short"
)

const (
	OrderTypeMarket = "market"
	OrderTypeLimit  = "limit"
	OrderTypeStop   = "stop"
)

const (
	OrderStatusNew             = "NEW"
	OrderStatusPartiallyFilled = "PARTIALLY_FILLED"
	OrderStatusFilled          = "FILLED"
	OrderStatusCanceled        = "CANCELED"
	OrderStatusRejected        = "REJECTED"
	OrderStatusExpired         = "EXPIRED"
	OrderStatusTriggered       = "TRIGGERED"
)

const (
	DatasetImportStatusPending   = "pending"
	DatasetImportStatusRunning   = "running"
	DatasetImportStatusCompleted = "completed"
	DatasetImportStatusFailed    = "failed"
)

type StringList []string

func (s StringList) Value() (driver.Value, error) {
	cleaned := normalizeStringList([]string(s))
	if len(cleaned) == 0 {
		return "", nil
	}
	return strings.Join(cleaned, ","), nil
}

func (s *StringList) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*s = nil
		return nil
	case string:
		*s = StringList(normalizeStringList(strings.Split(v, ",")))
		return nil
	case []byte:
		*s = StringList(normalizeStringList(strings.Split(string(v), ",")))
		return nil
	default:
		return fmt.Errorf("unsupported StringList scan type %T", value)
	}
}

type User struct {
	ID                 uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	Username           string         `gorm:"size:64;not null;uniqueIndex" json:"username"`
	DisplayName        string         `gorm:"size:64" json:"display_name"`
	Role               int            `gorm:"default:1;not null" json:"role"`
	Email              string         `gorm:"size:255;uniqueIndex;not null" json:"email"`
	Password           string         `gorm:"size:255;not null" json:"-"`
	Salt               string         `gorm:"size:255;not null" json:"-"`
	VerificationCode   string         `gorm:"size:6" json:"verification_code,omitempty"`
	VerificationSentAt time.Time      `gorm:"autoCreateTime" json:"verification_sent_at,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	DeletedAt          gorm.DeletedAt `gorm:"index" json:"-"`
}

type Sandbox struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	Name              string     `gorm:"size:255;not null" json:"name"`
	Mode              string     `gorm:"size:32;not null;default:replay" json:"mode"`
	Status            string     `gorm:"size:32;not null;default:draft;index" json:"status"`
	StartDatetime     time.Time  `json:"start_datetime"`
	ReplayCurrentTime time.Time  `gorm:"index" json:"replay_current_time"`
	ReplaySpeed       float64    `gorm:"not null;default:1" json:"replay_speed"`
	DatasetID         string     `gorm:"size:64;index" json:"dataset_id"`
	RuntimeAnchorAt   *time.Time `json:"runtime_anchor_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Account struct {
	ID                string         `gorm:"primaryKey;size:64" json:"id"`
	SandboxID         *string        `gorm:"size:64;index" json:"sandbox_id,omitempty"`
	Name              string         `gorm:"size:255;not null" json:"name"`
	Type              string         `gorm:"size:32;not null;default:virtual" json:"type"`
	Provider          string         `gorm:"size:64" json:"provider,omitempty"`
	Environment       string         `gorm:"size:32" json:"environment,omitempty"`
	PriceMode         string         `gorm:"size:32" json:"price_mode,omitempty"`
	CredentialsStatus string         `gorm:"size:32" json:"credentials_status,omitempty"`
	SupportedSymbols  StringList     `gorm:"type:text" json:"supported_symbols,omitempty"`
	LastHealthCheckAt *time.Time     `json:"last_health_check_at,omitempty"`
	BaseCurrency      string         `gorm:"size:16;not null;default:USD" json:"base_currency"`
	InitialBalance    float64        `gorm:"not null" json:"initial_balance"`
	WalletBalance     float64        `gorm:"not null" json:"wallet_balance"`
	AvailableBalance  float64        `gorm:"not null" json:"available_balance"`
	LockedMargin      float64        `gorm:"not null" json:"locked_margin"`
	RealizedPnL       float64        `gorm:"not null" json:"realized_pnl"`
	UnrealizedPnL     float64        `gorm:"not null" json:"unrealized_pnl"`
	Equity            float64        `gorm:"not null" json:"equity"`
	Status            string         `gorm:"size:32;not null;default:active;index" json:"status"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`
}

type AccountToken struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"`
	AccountID  string     `gorm:"size:64;not null;index" json:"account_id"`
	TokenName  string     `gorm:"size:255;not null" json:"token_name"`
	TokenHash  string     `gorm:"size:255;not null" json:"-"`
	Scope      string     `gorm:"type:text;not null" json:"scope"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (t AccountToken) Scopes() []string {
	if t.Scope == "" {
		return nil
	}
	parts := strings.Split(t.Scope, ",")
	scopes := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			scopes = append(scopes, trimmed)
		}
	}
	return scopes
}

type Order struct {
	ID           string     `gorm:"primaryKey;size:64" json:"id"`
	SandboxID    string     `gorm:"size:64;not null;index" json:"sandbox_id"`
	AccountID    string     `gorm:"size:64;not null;index" json:"account_id"`
	Symbol       string     `gorm:"size:32;not null;index" json:"symbol"`
	Side         string     `gorm:"size:16;not null" json:"side"`
	PositionSide string     `gorm:"size:16;not null" json:"position_side"`
	OrderType    string     `gorm:"size:16;not null" json:"order_type"`
	Quantity     float64    `gorm:"not null" json:"qty"`
	Price        float64    `json:"price,omitempty"`
	StopPrice    float64    `json:"stop_price,omitempty"`
	Leverage     float64    `gorm:"not null;default:1" json:"leverage"`
	Status       string     `gorm:"size:32;not null;index" json:"status"`
	FilledQty    float64    `gorm:"not null" json:"filled_qty"`
	AvgFillPrice float64    `gorm:"not null" json:"avg_fill_price"`
	ReduceOnly   bool       `gorm:"not null;default:false" json:"reduce_only"`
	TriggeredAt  *time.Time `json:"triggered_at,omitempty"`
	Rejection    string     `gorm:"size:255" json:"rejection,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type Trade struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	OrderID      string    `gorm:"size:64;not null;index" json:"order_id"`
	AccountID    string    `gorm:"size:64;not null;index" json:"account_id"`
	SandboxID    string    `gorm:"size:64;not null;index" json:"sandbox_id"`
	Symbol       string    `gorm:"size:32;not null;index" json:"symbol"`
	Quantity     float64   `gorm:"not null" json:"qty"`
	Price        float64   `gorm:"not null" json:"price"`
	Fee          float64   `gorm:"not null" json:"fee"`
	Side         string    `gorm:"size:16;not null" json:"side"`
	PositionSide string    `gorm:"size:16;not null" json:"position_side"`
	ExecutedAt   time.Time `gorm:"index" json:"executed_at"`
}

type Position struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	AccountID     string    `gorm:"size:64;not null;index:idx_position_key,priority:1" json:"account_id"`
	SandboxID     string    `gorm:"size:64;not null;index:idx_position_key,priority:2" json:"sandbox_id"`
	Symbol        string    `gorm:"size:32;not null;index:idx_position_key,priority:3" json:"symbol"`
	PositionSide  string    `gorm:"size:16;not null;index:idx_position_key,priority:4" json:"position_side"`
	Quantity      float64   `gorm:"not null" json:"qty"`
	EntryPrice    float64   `gorm:"not null" json:"entry_price"`
	MarkPrice     float64   `gorm:"not null" json:"mark_price"`
	Leverage      float64   `gorm:"not null" json:"leverage"`
	MarginUsed    float64   `gorm:"not null" json:"margin_used"`
	UnrealizedPnL float64   `gorm:"not null" json:"unrealized_pnl"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ReplayDataset struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	Name      string    `gorm:"size:255;not null" json:"name"`
	Symbol    string    `gorm:"size:32;not null;index" json:"symbol"`
	Interval  string    `gorm:"size:16;not null" json:"interval"`
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Source    string    `gorm:"size:255" json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type ReplayKline struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	DatasetID string    `gorm:"size:64;not null;index:idx_dataset_symbol_ts,priority:1" json:"dataset_id"`
	Symbol    string    `gorm:"size:32;not null;index:idx_dataset_symbol_ts,priority:2" json:"symbol"`
	Ts        time.Time `gorm:"index:idx_dataset_symbol_ts,priority:3" json:"ts"`
	Open      float64   `gorm:"not null" json:"open"`
	High      float64   `gorm:"not null" json:"high"`
	Low       float64   `gorm:"not null" json:"low"`
	Close     float64   `gorm:"not null" json:"close"`
	Volume    float64   `gorm:"not null" json:"volume"`
}

type DatasetImportJob struct {
	ID           string     `gorm:"primaryKey;size:64" json:"id"`
	DatasetID    string     `gorm:"size:64;not null;index" json:"dataset_id"`
	FileName     string     `gorm:"size:255;not null" json:"file_name"`
	Status       string     `gorm:"size:32;not null;index" json:"status"`
	ErrorSummary string     `gorm:"size:255" json:"error_summary,omitempty"`
	RowsTotal    int        `gorm:"not null;default:0" json:"rows_total"`
	RowsImported int        `gorm:"not null;default:0" json:"rows_imported"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type EventLog struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	EventType     string    `gorm:"size:64;not null;index" json:"event_type"`
	AggregateType string    `gorm:"size:64;not null" json:"aggregate_type"`
	AggregateID   string    `gorm:"size:64;not null;index" json:"aggregate_id"`
	PayloadJSON   string    `gorm:"type:text;not null" json:"payload_json"`
	CreatedAt     time.Time `gorm:"index" json:"created_at"`
}

func normalizeStringList(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.ToUpper(strings.TrimSpace(item))
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
