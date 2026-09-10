package account

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newTestService(t *testing.T) *Service {
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
	return New(store)
}

func mustCreate(t *testing.T, s *Service, name string, balance float64) *database.Account {
	t.Helper()
	acc, err := s.Create(context.Background(), CreateInput{UserName: name, InitialBalance: balance})
	if err != nil {
		t.Fatalf("建立帳號 %s: %v", name, err)
	}
	return acc
}

func TestCreateSetsBalanceAndIssuesToken(t *testing.T) {
	s := newTestService(t)
	acc := mustCreate(t, s, "BTC-Agent-01", 10000)

	if acc.Balance != 10000 || acc.InitialBalance != 10000 {
		t.Fatalf("餘額應等於初始餘額: %+v", acc)
	}
	if acc.Status != database.AccountActive {
		t.Fatalf("新帳號應為 active，實際 %q", acc.Status)
	}
	if !strings.HasPrefix(acc.ID, "acc_") {
		t.Fatalf("帳號 id 前綴錯誤: %q", acc.ID)
	}
	if !strings.HasPrefix(acc.Token, "at_") || len(acc.Token) < 40 {
		t.Fatalf("token 格式或長度不足: %q", acc.Token)
	}
}

func TestCreateTrimsNameAndRejectsBadInput(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	acc := mustCreate(t, s, "  Spaced Name  ", 100)
	if acc.UserName != "Spaced Name" {
		t.Fatalf("名稱前後空白應去掉，得到 %q", acc.UserName)
	}

	cases := []struct {
		name    string
		input   CreateInput
		wantErr error
	}{
		{"空名稱", CreateInput{UserName: "   ", InitialBalance: 100}, ErrInvalidName},
		{"名稱過長", CreateInput{UserName: strings.Repeat("x", 65), InitialBalance: 100}, ErrInvalidName},
		{"餘額為零", CreateInput{UserName: "zero", InitialBalance: 0}, ErrInvalidBalance},
		{"餘額為負", CreateInput{UserName: "negative", InitialBalance: -1}, ErrInvalidBalance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Create(ctx, tc.input); !errors.Is(err, tc.wantErr) {
				t.Fatalf("預期 %v，得到 %v", tc.wantErr, err)
			}
		})
	}
}

func TestTokensAndIDsAreUnique(t *testing.T) {
	s := newTestService(t)
	tokens := make(map[string]bool, 50)
	ids := make(map[string]bool, 50)

	for i := 0; i < 50; i++ {
		acc := mustCreate(t, s, strings.Repeat("a", i+1), 100)
		if tokens[acc.Token] {
			t.Fatalf("token 重複: %s", acc.Token)
		}
		if ids[acc.ID] {
			t.Fatalf("帳號 id 重複: %s", acc.ID)
		}
		tokens[acc.Token], ids[acc.ID] = true, true
	}
}

func TestDuplicateNameIsRejected(t *testing.T) {
	s := newTestService(t)
	mustCreate(t, s, "BTC-Agent-01", 100)

	_, err := s.Create(context.Background(), CreateInput{UserName: "BTC-Agent-01", InitialBalance: 200})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("重複名稱應被拒絕，得到 %v", err)
	}
}

func TestResetTokenInvalidatesOldToken(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	acc := mustCreate(t, s, "agent", 100)
	old := acc.Token

	updated, err := s.ResetToken(ctx, acc.ID)
	if err != nil {
		t.Fatalf("換發 token: %v", err)
	}
	if updated.Token == old {
		t.Fatal("換發後 token 沒有改變")
	}
	if updated.ID != acc.ID {
		t.Fatal("換發 token 不應更動帳號 id")
	}

	// 舊 token 不該再對應到任何帳號。
	if _, err := s.store.AccountByToken(ctx, old); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("舊 token 仍然有效: %v", err)
	}
}

func TestUpdateChangesNameAndStatus(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	acc := mustCreate(t, s, "before", 100)

	name, status := "after", database.AccountDisabled
	updated, err := s.Update(ctx, acc.ID, UpdateInput{UserName: &name, Status: &status})
	if err != nil {
		t.Fatalf("更新帳號: %v", err)
	}
	if updated.UserName != "after" || updated.Status != database.AccountDisabled {
		t.Fatalf("更新結果不符: %+v", updated)
	}
	if updated.Token != acc.Token {
		t.Fatal("更新名稱或狀態不應更動 token")
	}
}

func TestUpdateRejectsUnknownStatus(t *testing.T) {
	s := newTestService(t)
	acc := mustCreate(t, s, "agent", 100)

	bad := "paused"
	if _, err := s.Update(context.Background(), acc.ID, UpdateInput{Status: &bad}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("未知狀態應被拒絕，得到 %v", err)
	}
}

func TestGetUpdateDeleteOnMissingAccount(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	name := "x"

	if _, err := s.Get(ctx, "acc_missing"); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("Get 應回 ErrNotFound，得到 %v", err)
	}
	if _, err := s.Update(ctx, "acc_missing", UpdateInput{UserName: &name}); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("Update 應回 ErrNotFound，得到 %v", err)
	}
	if err := s.Delete(ctx, "acc_missing"); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("Delete 應回 ErrNotFound，得到 %v", err)
	}
	if _, err := s.ResetToken(ctx, "acc_missing"); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("ResetToken 應回 ErrNotFound，得到 %v", err)
	}
}

func TestListReturnsAllAccounts(t *testing.T) {
	s := newTestService(t)
	mustCreate(t, s, "one", 100)
	mustCreate(t, s, "two", 200)

	accounts, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("列出帳號: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("預期 2 個帳號，得到 %d", len(accounts))
	}
}
