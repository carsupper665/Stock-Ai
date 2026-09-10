package auth

import (
	"context"
	"errors"
	"strings"

	"backend/internal/database"
)

// Kind 區分兩種身分。系統只有這兩種，沒有其他角色。
type Kind string

const (
	KindUser    Kind = "user"
	KindAccount Kind = "account"
)

// Identity 是一次請求的呼叫者身分。
type Identity struct {
	Kind      Kind
	AccountID string // 僅 KindAccount 有值
	UserName  string // 顯示名稱：USER 來自 .env，帳號來自 Account.user_name
}

// IsUser 回報這個身分是否為 USER。
func (i Identity) IsUser() bool { return i.Kind == KindUser }

// Owns 回報這個身分是否就是指定的帳號。USER 不「擁有」任何帳號，
// 但另有管理所有帳號的權限，由呼叫端自行判斷。
func (i Identity) Owns(accountID string) bool {
	return i.Kind == KindAccount && i.AccountID == accountID
}

var (
	// ErrNoToken 表示請求沒有帶 Authorization。
	ErrNoToken = errors.New("未提供 token")
	// ErrInvalidToken 表示 token 無法對應到任何身分，或帳號已停用。
	ErrInvalidToken = errors.New("token 無效")
)

// Authenticator 解析 token 並判斷身分。
type Authenticator struct {
	userToken string
	userName  string
	store     *database.Store
}

func New(userToken, userName string, store *database.Store) *Authenticator {
	return &Authenticator{userToken: userToken, userName: userName, store: store}
}

// Resolve 把 raw token 換成身分。
// 空字串回傳 ErrNoToken，對不上或帳號停用回傳 ErrInvalidToken。
func (a *Authenticator) Resolve(ctx context.Context, raw string) (Identity, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return Identity{}, ErrNoToken
	}

	if token == a.userToken {
		return Identity{Kind: KindUser, UserName: a.userName}, nil
	}

	acc, err := a.store.AccountByToken(ctx, token)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return Identity{}, ErrInvalidToken
		}
		return Identity{}, err
	}
	// 停用的帳號等同 token 失效。
	if !acc.Active() {
		return Identity{}, ErrInvalidToken
	}

	return Identity{Kind: KindAccount, AccountID: acc.ID, UserName: acc.UserName}, nil
}

// BearerToken 從 Authorization header 取出 token。
// 接受 "Bearer <token>"，也接受直接放 token。
func BearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	if len(header) >= 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return header
}
