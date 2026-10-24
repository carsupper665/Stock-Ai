package database

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const encryptedTokenPrefix = "enc:v1:"

func (s *Store) CreateAccount(ctx context.Context, account *Account) error {
	stored, err := s.storedAccount(account)
	if err != nil {
		return err
	}
	return translate(s.db.WithContext(ctx).Create(stored).Error)
}

func (s *Store) AccountByID(ctx context.Context, id string) (*Account, error) {
	return s.accountWhere(ctx, "id = ?", id)
}

// AccountByToken 用於驗證 Account Token。
func (s *Store) AccountByToken(ctx context.Context, token string) (*Account, error) {
	if len(s.tokenKey) == 0 {
		return s.accountWhere(ctx, "token = ?", token)
	}
	return s.accountWhere(ctx, "token_hash = ?", s.hashToken(token))
}

func (s *Store) accountWhere(ctx context.Context, query string, args ...any) (*Account, error) {
	var account Account
	if err := s.db.WithContext(ctx).Where(query, args...).First(&account).Error; err != nil {
		return nil, translate(err)
	}
	if err := s.decryptAccount(&account); err != nil {
		return nil, err
	}
	return &account, nil
}

// ListAccounts 依建立時間由新到舊回傳所有帳號。
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	var accounts []Account
	if err := s.db.WithContext(ctx).Order("created_at desc").Find(&accounts).Error; err != nil {
		return nil, translate(err)
	}
	for index := range accounts {
		if err := s.decryptAccount(&accounts[index]); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

func (s *Store) SaveAccount(ctx context.Context, account *Account) error {
	stored, err := s.storedAccount(account)
	if err != nil {
		return err
	}
	return translate(s.db.WithContext(ctx).Save(stored).Error)
}

// ListMaxDDAccounts 回傳設定過 max_dd 的帳號（含已停止的），供撮合引擎做風控掃描。
// 未設定（null）的帳號不在其中，維持原有快速路徑。
func (s *Store) ListMaxDDAccounts(ctx context.Context) ([]Account, error) {
	var accounts []Account
	if err := s.db.WithContext(ctx).Where("max_dd IS NOT NULL").Order("created_at asc").Find(&accounts).Error; err != nil {
		return nil, translate(err)
	}
	for index := range accounts {
		if err := s.decryptAccount(&accounts[index]); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

// UpdateAccountFields only writes fields owned by Account administration. Trading balance and
// token rotation are separate mutations and must not be overwritten by a stale Account value.
// AccountFields 是 USER 可以修改的帳號欄位；nil 表示不改。
type AccountFields struct {
	UserName        *string
	Status          *string
	ChatRead        *bool
	ChatPost        *bool
	ChatReadSince   *time.Time
	ChatRateMinutes *int
	ChatRateCount   *int
}

func (s *Store) UpdateAccountFields(ctx context.Context, id string, in AccountFields) error {
	fields := make(map[string]any, 5)
	if in.UserName != nil {
		fields["user_name"] = *in.UserName
	}
	if in.Status != nil {
		fields["status"] = *in.Status
	}
	if in.ChatRead != nil {
		fields["chat_read"] = *in.ChatRead
	}
	if in.ChatPost != nil {
		fields["chat_post"] = *in.ChatPost
	}
	if in.ChatReadSince != nil {
		fields["chat_read_since"] = *in.ChatReadSince
	}
	if in.ChatRateMinutes != nil {
		fields["chat_rate_minutes"] = *in.ChatRateMinutes
	}
	if in.ChatRateCount != nil {
		fields["chat_rate_count"] = *in.ChatRateCount
	}
	if len(fields) == 0 {
		return nil
	}
	result := s.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(fields)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateAccountToken(ctx context.Context, id, token string) error {
	stored, err := s.protectToken(token)
	if err != nil {
		return err
	}
	fields := map[string]any{"token": stored, "token_hash": nil}
	if len(s.tokenKey) > 0 {
		hash := s.hashToken(token)
		fields["token_hash"] = &hash
	}
	result := s.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(fields)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateChatMute(ctx context.Context, id string, until *time.Time, reason string) error {
	result := s.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).
		Updates(map[string]any{"chat_muted_until": until, "chat_mute_reason": reason})
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) storedAccount(account *Account) (*Account, error) {
	copy := *account
	protected, err := s.protectToken(account.Token)
	if err != nil {
		return nil, err
	}
	copy.Token = protected
	if len(s.tokenKey) > 0 {
		hash := s.hashToken(account.Token)
		copy.TokenHash = &hash
	} else {
		copy.TokenHash = nil
	}
	return &copy, nil
}

func (s *Store) protectToken(token string) (string, error) {
	if len(s.tokenKey) == 0 {
		return token, nil
	}
	block, err := aes.NewCipher(s.tokenKey)
	if err != nil {
		return "", errors.New("invalid Account token encryption key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.New("initialize Account token encryption")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", errors.New("generate Account token nonce")
	}
	ciphertext := aead.Seal(nil, nonce, []byte(token), nil)
	return encryptedTokenPrefix + base64.RawStdEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

func (s *Store) decryptAccount(account *Account) error {
	if len(s.tokenKey) == 0 {
		// Development stores plaintext. A token beginning with the production
		// envelope prefix is still plaintext when no key/hash envelope exists.
		if account.TokenHash != nil {
			return errors.New("ACCOUNT_TOKEN_KEY is required for encrypted Account tokens")
		}
		return nil
	}
	if !strings.HasPrefix(account.Token, encryptedTokenPrefix) {
		return errors.New("unprotected Account token encountered after migration")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(account.Token, encryptedTokenPrefix))
	if err != nil {
		return errors.New("invalid encrypted Account token")
	}
	block, err := aes.NewCipher(s.tokenKey)
	if err != nil {
		return errors.New("invalid Account token encryption key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(raw) < aead.NonceSize() {
		return errors.New("invalid encrypted Account token")
	}
	plaintext, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return errors.New("ACCOUNT_TOKEN_KEY cannot decrypt retained Account database")
	}
	account.Token = string(plaintext)
	return nil
}

func (s *Store) hashToken(token string) string {
	if len(s.tokenKey) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, s.tokenKey)
	_, _ = mac.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Store) migrateAccountTokens() error {
	if len(s.tokenKey) == 0 {
		var protectedRows int64
		if err := s.db.Model(&Account{}).Where("token_hash IS NOT NULL").Count(&protectedRows).Error; err != nil {
			return err
		}
		if protectedRows > 0 {
			return errors.New("ACCOUNT_TOKEN_KEY is required for retained encrypted Account database")
		}
		return nil
	}
	var accounts []Account
	if err := s.db.Find(&accounts).Error; err != nil {
		return err
	}
	plaintextByID := make(map[string]string, len(accounts))
	storedByID := make(map[string]string, len(accounts))
	migratedPlaintext := false
	for index := range accounts {
		account := &accounts[index]
		storedByID[account.ID] = account.Token
		if account.TokenHash != nil {
			if !strings.HasPrefix(account.Token, encryptedTokenPrefix) {
				return fmt.Errorf("account %s: invalid encrypted Account token envelope", account.ID)
			}
			if err := s.decryptAccount(account); err != nil {
				return fmt.Errorf("account %s: %w", account.ID, err)
			}
		} else {
			migratedPlaintext = true
		}
		plaintextByID[account.ID] = account.Token
	}
	// Validate every existing ciphertext before writing anything. A wrong key
	// must leave even a mixed/partially migrated database unchanged.
	for index := range accounts {
		account := &accounts[index]
		plaintext := plaintextByID[account.ID]
		protected := storedByID[account.ID]
		if account.TokenHash == nil {
			var err error
			protected, err = s.protectToken(plaintext)
			if err != nil {
				return err
			}
		}
		hash := s.hashToken(plaintext)
		updates := map[string]any{"token": protected, "token_hash": &hash}
		if err := s.db.Model(&Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	if migratedPlaintext {
		statement := "VACUUM"
		if s.db.Dialector.Name() == "postgres" {
			statement = "VACUUM FULL accounts"
		}
		if err := s.db.Exec(statement).Error; err != nil {
			return fmt.Errorf("remove migrated plaintext pages: %w", err)
		}
	}
	return nil
}

// DeleteAccount 永久刪除帳號。找不到時回傳 ErrNotFound。
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Delete(&Account{}, "id = ?", id)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
