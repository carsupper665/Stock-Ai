package database

import "time"

// 帳號狀態。
const (
	AccountActive   = "active"
	AccountDisabled = "disabled"
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

// Active 回報這個帳號是否可以使用。
func (a *Account) Active() bool {
	return a.Status == AccountActive
}
