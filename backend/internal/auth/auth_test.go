package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const userToken = "user-token-secret"

func newTestAuth(t *testing.T) (*Authenticator, *database.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{
		TranslateError: true,
		Logger:         gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("開啟測試資料庫: %v", err)
	}
	store := database.NewStore(db)
	// Windows 上檔案沒關就刪不掉 TempDir。
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("建表: %v", err)
	}
	return New(userToken, "Bless", store), store
}

func seedAccount(t *testing.T, store *database.Store, id, name, token, status string) {
	t.Helper()
	acc := &database.Account{
		ID: id, UserName: name, Token: token,
		InitialBalance: 100, Balance: 100, Status: status,
	}
	if err := store.CreateAccount(context.Background(), acc); err != nil {
		t.Fatalf("建立測試帳號: %v", err)
	}
}

func TestResolveIdentifiesUserToken(t *testing.T) {
	a, _ := newTestAuth(t)

	id, err := a.Resolve(context.Background(), userToken)
	if err != nil {
		t.Fatalf("解析 USER token: %v", err)
	}
	if !id.IsUser() || id.UserName != "Bless" || id.AccountID != "" {
		t.Fatalf("USER 身分不正確: %+v", id)
	}
}

func TestResolveIdentifiesAccountToken(t *testing.T) {
	a, store := newTestAuth(t)
	seedAccount(t, store, "acc_1", "BTC-Agent-01", "at_abc", database.AccountActive)

	id, err := a.Resolve(context.Background(), "at_abc")
	if err != nil {
		t.Fatalf("解析 Account token: %v", err)
	}
	if id.Kind != KindAccount || id.AccountID != "acc_1" || id.UserName != "BTC-Agent-01" {
		t.Fatalf("帳號身分不正確: %+v", id)
	}
	if id.IsUser() {
		t.Fatal("Account token 不該被認成 USER")
	}
}

func TestResolveRejectsDisabledAccount(t *testing.T) {
	a, store := newTestAuth(t)
	seedAccount(t, store, "acc_off", "Disabled-Agent", "at_off", database.AccountDisabled)

	if _, err := a.Resolve(context.Background(), "at_off"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("停用帳號的 token 應失效，得到 %v", err)
	}
}

func TestResolveDistinguishesMissingFromInvalid(t *testing.T) {
	a, _ := newTestAuth(t)
	ctx := context.Background()

	// 沒帶 token 與帶錯 token 必須是不同的錯誤：
	// 前者在公開端點可以放行，後者一定要回 401。
	if _, err := a.Resolve(ctx, ""); !errors.Is(err, ErrNoToken) {
		t.Fatalf("空 token 應回 ErrNoToken，得到 %v", err)
	}
	if _, err := a.Resolve(ctx, "   "); !errors.Is(err, ErrNoToken) {
		t.Fatalf("只有空白的 token 應回 ErrNoToken，得到 %v", err)
	}
	if _, err := a.Resolve(ctx, "at_does_not_exist"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("不存在的 token 應回 ErrInvalidToken，得到 %v", err)
	}
}

func TestOwns(t *testing.T) {
	account := Identity{Kind: KindAccount, AccountID: "acc_1"}
	user := Identity{Kind: KindUser}

	if !account.Owns("acc_1") {
		t.Fatal("帳號應該擁有自己")
	}
	if account.Owns("acc_2") {
		t.Fatal("帳號不該擁有別人")
	}
	if user.Owns("acc_1") {
		t.Fatal("USER 不透過 Owns 取得帳號權限")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct{ header, want string }{
		{"Bearer at_abc", "at_abc"},
		{"bearer at_abc", "at_abc"},
		{"BEARER at_abc", "at_abc"},
		{"  Bearer   at_abc  ", "at_abc"},
		{"at_abc", "at_abc"},
		{"", ""},
		{"   ", ""},
		{"Bearer", "Bearer"}, // 沒有空白就不是 bearer 格式，整串當 token
	}
	for _, tc := range cases {
		if got := BearerToken(tc.header); got != tc.want {
			t.Fatalf("BearerToken(%q) = %q，預期 %q", tc.header, got, tc.want)
		}
	}
}
