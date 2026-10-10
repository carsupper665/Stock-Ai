package database

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAccountTokensAreEncryptedMigratedAndKeyedForLookup(t *testing.T) {
	path := t.TempDir() + "/accounts.db"
	plaintext := "at_plaintext_token_must_not_remain_in_database"
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	account := &Account{ID: "acc_encrypted", UserName: "encrypted", Token: plaintext, InitialBalance: 100, Balance: 100, Status: AccountActive}
	if err := store.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	store.tokenKey = []byte("0123456789abcdef0123456789ABCDEF")
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate plaintext token: %v", err)
	}
	var persisted Account
	if err := store.db.First(&persisted, "id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Token == plaintext || strings.Contains(persisted.Token, plaintext) || !strings.HasPrefix(persisted.Token, encryptedTokenPrefix) {
		t.Fatalf("persisted token is not protected: %q", persisted.Token)
	}
	if persisted.TokenHash == nil || *persisted.TokenHash == "" || strings.Contains(*persisted.TokenHash, plaintext) {
		t.Fatal("keyed token lookup was not populated")
	}
	resolved, err := store.AccountByToken(context.Background(), plaintext)
	if err != nil || resolved.Token != plaintext {
		t.Fatalf("lookup/decrypt = %#v, %v", resolved, err)
	}
	if err := store.Tx(context.Background(), func(tx *Store) error {
		resolved, lookupErr := tx.AccountByToken(context.Background(), plaintext)
		if lookupErr != nil {
			return lookupErr
		}
		if resolved.Token != plaintext {
			return errors.New("transaction returned the wrong decrypted token")
		}
		return nil
	}); err != nil {
		t.Fatalf("transaction lost token key: %v", err)
	}

	duplicate := &Account{ID: "acc_duplicate", UserName: "duplicate", Token: plaintext, InitialBalance: 100, Balance: 100, Status: AccountActive}
	if err := store.CreateAccount(context.Background(), duplicate); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate production token create error = %v", err)
	}
	secondToken := "at_second_unique_token_for_rotation_checks"
	second := &Account{ID: "acc_second", UserName: "second", Token: secondToken, InitialBalance: 100, Balance: 100, Status: AccountActive}
	if err := store.CreateAccount(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccountToken(context.Background(), second.ID, plaintext); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate production token rotation error = %v", err)
	}
	if resolved, err := store.AccountByToken(context.Background(), secondToken); err != nil || resolved.ID != second.ID {
		t.Fatalf("failed duplicate rotation changed old credential: %#v, %v", resolved, err)
	}

	prefixedCreateToken := "enc:v1:plaintext_create_token_must_be_encrypted"
	prefixed := &Account{ID: "acc_prefixed", UserName: "prefixed", Token: prefixedCreateToken, InitialBalance: 100, Balance: 100, Status: AccountActive}
	if err := store.CreateAccount(context.Background(), prefixed); err != nil {
		t.Fatal(err)
	}
	var prefixedStored Account
	if err := store.db.First(&prefixedStored, "id = ?", prefixed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if prefixedStored.Token == prefixedCreateToken || prefixedStored.TokenHash == nil {
		t.Fatal("enc:v1-prefixed create token bypassed encryption")
	}
	if resolved, err := store.AccountByToken(context.Background(), prefixedCreateToken); err != nil || resolved.Token != prefixedCreateToken {
		t.Fatalf("prefixed create lookup = %#v, %v", resolved, err)
	}
	prefixedResetToken := "enc:v1:plaintext_reset_token_must_be_encrypted"
	if err := store.UpdateAccountToken(context.Background(), second.ID, prefixedResetToken); err != nil {
		t.Fatal(err)
	}
	if resolved, err := store.AccountByToken(context.Background(), prefixedResetToken); err != nil || resolved.ID != second.ID || resolved.Token != prefixedResetToken {
		t.Fatalf("prefixed reset lookup = %#v, %v", resolved, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	rawDatabase, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for index, secret := range []string{plaintext, prefixedCreateToken, prefixedResetToken} {
		if strings.Contains(string(rawDatabase), secret) {
			t.Fatalf("plaintext token remains in database pages for test credential %d", index)
		}
	}
	missing, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.Migrate(); err == nil || !strings.Contains(err.Error(), "ACCOUNT_TOKEN_KEY is required") {
		t.Fatalf("missing retained key error = %v", err)
	}
	if err := missing.Close(); err != nil {
		t.Fatal(err)
	}

	wrong, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	wrong.tokenKey = []byte("XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
	if err := wrong.Migrate(); err == nil || !strings.Contains(err.Error(), "cannot decrypt") {
		t.Fatalf("wrong retained key error = %v", err)
	}
	var afterWrongKey Account
	if err := wrong.db.First(&afterWrongKey, "id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterWrongKey.Token != persisted.Token {
		t.Fatal("wrong key modified retained ciphertext")
	}
}

func TestDevelopmentTokenUniquenessAndPrefixRemainCompatibleWithoutKey(t *testing.T) {
	store, err := OpenSQLite(t.TempDir() + "/development.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	token := "enc:v1:development_plaintext_token"
	first := &Account{ID: "acc_dev_first", UserName: "dev-first", Token: token, InitialBalance: 100, Balance: 100, Status: AccountActive}
	if err := store.CreateAccount(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.AccountByToken(context.Background(), token)
	if err != nil || resolved.Token != token {
		t.Fatalf("development prefixed token = %#v, %v", resolved, err)
	}
	var persisted Account
	if err := store.db.First(&persisted, "id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Token != token || persisted.TokenHash != nil {
		t.Fatal("development mode unexpectedly created a production envelope")
	}
	duplicate := &Account{ID: "acc_dev_duplicate", UserName: "dev-duplicate", Token: token, InitialBalance: 100, Balance: 100, Status: AccountActive}
	if err := store.CreateAccount(context.Background(), duplicate); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate development token create error = %v", err)
	}
}
