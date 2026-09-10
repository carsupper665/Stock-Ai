package account

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	idPrefix    = "acc_"
	tokenPrefix = "at_"
)

// newID 產生帳號識別碼，例如 acc_9f3c1a2b4d5e6f70。
func newID() (string, error) {
	suffix, err := randomHex(8)
	if err != nil {
		return "", fmt.Errorf("產生帳號 id 失敗: %w", err)
	}
	return idPrefix + suffix, nil
}

// newToken 產生 Account Token。使用密碼學安全亂數，長度足以抵抗猜測。
func newToken() (string, error) {
	suffix, err := randomHex(32)
	if err != nil {
		return "", fmt.Errorf("產生帳號 token 失敗: %w", err)
	}
	return tokenPrefix + suffix, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
