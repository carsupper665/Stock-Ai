package store

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	ID                 uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	Username           string         `gorm:"size:12;not null;uniqueIndex" json:"username"`
	DisplayName        string         `json:"display_name" gorm:"index" validate:"max=20"`
	Role               int            `gorm:"default:1;not null" json:"role"`
	Email              string         `gorm:"size:255;uniqueIndex;not null" json:"email"`
	Password           string         `gorm:"size:255;not null" json:"-"`
	Salt               string         `gorm:"size:255;not null" json:"-"`
	VerificationCode   string         `gorm:"size:6" json:"verification_code"`
	VerificationSentAt time.Time      `gorm:"autoCreateTime" json:"verification_sent_at"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	DeletedAt          gorm.DeletedAt `gorm:"index" json:"-"`
}

type LLMUser struct {
	ID          uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string  `gorm:"size:255;not null" json:"name"`
	AccessToken *string `json:"access_token" gorm:"type:char(32);column:access_token;uniqueIndex"`
}

type Account struct {
	AccountID uint    `gorm:"primaryKey;autoIncrement" json:"account_id"`
	OwnerID   uint    `gorm:"primaryKey;not null" json:"owner_id"`
	Balance   float64 `gorm:"not null" json:"balance"`
	Available float64 `gorm:"not null" json:"available"`
	Frozen    float64 `gorm:"not null" json:"frozen"`
}
