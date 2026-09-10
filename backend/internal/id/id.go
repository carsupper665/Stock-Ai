package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func New(prefix string) (string, error) {
	return generate(prefix, 8)
}

// Secret 產生足以抵抗猜測的憑證，用於 Account Token。
func Secret(prefix string) (string, error) {
	return generate(prefix, 32)
}

func generate(prefix string, size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("產生 %s 識別碼失敗: %w", prefix, err)
	}
	return prefix + hex.EncodeToString(b), nil
}
