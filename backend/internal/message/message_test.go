package message

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/database"
)

var (
	user  = auth.Identity{Kind: auth.KindUser, UserName: "Bless"}
	alpha = auth.Identity{Kind: auth.KindAccount, AccountID: "acc_alpha", UserName: "BTC-Agent-01"}
	beta  = auth.Identity{Kind: auth.KindAccount, AccountID: "acc_beta", UserName: "ETH-Agent-02"}
)

func newTestService(t *testing.T) (*Service, *database.Store) {
	t.Helper()
	store, err := database.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("開啟測試資料庫: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("建表: %v", err)
	}

	for _, acc := range []auth.Identity{alpha, beta} {
		err := store.CreateAccount(context.Background(), &database.Account{
			ID: acc.AccountID, UserName: acc.UserName, Token: "at_" + acc.AccountID,
			InitialBalance: 1, Balance: 1, Status: database.AccountActive,
		})
		if err != nil {
			t.Fatalf("建立帳號 %s: %v", acc.AccountID, err)
		}
	}
	return New(store, "Bless"), store
}

func mustPost(t *testing.T, s *Service, author auth.Identity, content string) *database.Message {
	t.Helper()
	m, err := s.Post(context.Background(), author, content)
	if err != nil {
		t.Fatalf("發布留言: %v", err)
	}
	return m
}

func names(views []View) []string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.UserName
	}
	return out
}

func TestPostRecordsRealAuthorNotYou(t *testing.T) {
	s, store := newTestService(t)

	m := mustPost(t, s, alpha, "  BTC breakout looks valid.  ")
	if m.AuthorType != database.AuthorAccount || m.AuthorID != "acc_alpha" {
		t.Fatalf("應存真實作者: %+v", m)
	}
	if m.Content != "BTC breakout looks valid." {
		t.Fatalf("內容應去掉前後空白: %q", m.Content)
	}

	m = mustPost(t, s, user, "hello")
	if m.AuthorType != database.AuthorUser || m.AuthorID != "" {
		t.Fatalf("USER 的留言應以 author_type 區分、author_id 留空: %+v", m)
	}

	stored, _ := store.ListMessages(context.Background(), database.MessageQuery{Limit: 10})
	for _, row := range stored {
		if strings.Contains(row.AuthorID, "you") || strings.Contains(row.AuthorType, "you") {
			t.Fatalf("資料庫裡不該出現 you: %+v", row)
		}
	}
}

func TestPostRejectsBadContent(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()

	if _, err := s.Post(ctx, alpha, "   "); !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("空白內容應被拒絕，得到 %v", err)
	}
	if _, err := s.Post(ctx, alpha, strings.Repeat("字", MaxContentLen+1)); !errors.Is(err, ErrContentLong) {
		t.Fatalf("過長內容應被拒絕，得到 %v", err)
	}
	if _, err := s.Post(ctx, alpha, strings.Repeat("字", MaxContentLen)); err != nil {
		t.Fatalf("剛好上限應可發布: %v", err)
	}
}

func TestYouDependsOnRequester(t *testing.T) {
	s, _ := newTestService(t)
	mustPost(t, s, user, "from user")
	mustPost(t, s, alpha, "from alpha")
	mustPost(t, s, beta, "from beta")
	q := Query{Order: "asc"}

	cases := []struct {
		name      string
		requester *auth.Identity
		want      []string
	}{
		{"匿名", nil, []string{"Bless", "BTC-Agent-01", "ETH-Agent-02"}},
		{"USER", &user, []string{"you", "BTC-Agent-01", "ETH-Agent-02"}},
		{"alpha", &alpha, []string{"Bless", "you", "ETH-Agent-02"}},
		{"beta", &beta, []string{"Bless", "BTC-Agent-01", "you"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			views, err := s.List(context.Background(), q, tc.requester)
			if err != nil {
				t.Fatalf("查詢: %v", err)
			}
			if got := names(views); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("顯示名稱不符: 得到 %v，預期 %v", got, tc.want)
			}
		})
	}
}

func TestDeletedAccountShowsPlaceholder(t *testing.T) {
	s, store := newTestService(t)
	mustPost(t, s, alpha, "before deletion")
	if err := store.DeleteAccount(context.Background(), alpha.AccountID); err != nil {
		t.Fatalf("刪除帳號: %v", err)
	}

	views, err := s.List(context.Background(), Query{}, nil)
	if err != nil {
		t.Fatalf("查詢: %v", err)
	}
	if len(views) != 1 || views[0].UserName != deletedName {
		t.Fatalf("已刪除帳號的留言應顯示 %s: %v", deletedName, names(views))
	}
}

func TestOrderAndLimit(t *testing.T) {
	s, _ := newTestService(t)
	for i := 0; i < 35; i++ {
		mustPost(t, s, alpha, fmt.Sprintf("msg %02d", i))
		time.Sleep(time.Millisecond)
	}
	ctx := context.Background()

	views, _ := s.List(ctx, Query{}, nil)
	if len(views) != MaxLimit {
		t.Fatalf("預設應回最多 %d 則，得到 %d", MaxLimit, len(views))
	}
	if views[0].Content != "msg 34" {
		t.Fatalf("預設 desc 應最新在前，得到 %q", views[0].Content)
	}

	views, _ = s.List(ctx, Query{Limit: 100}, nil)
	if len(views) != MaxLimit {
		t.Fatalf("limit 超過上限應被壓到 %d，得到 %d", MaxLimit, len(views))
	}

	views, _ = s.List(ctx, Query{Order: "asc", Limit: 3}, nil)
	if len(views) != 3 || views[0].Content != "msg 00" || views[2].Content != "msg 02" {
		t.Fatalf("asc 應由舊到新: %v", views)
	}

	if _, err := s.List(ctx, Query{Order: "sideways"}, nil); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("未知的 order 應被拒絕，得到 %v", err)
	}
}

func TestAfterAndBefore(t *testing.T) {
	s, _ := newTestService(t)
	first := mustPost(t, s, alpha, "first")
	time.Sleep(5 * time.Millisecond)
	second := mustPost(t, s, alpha, "second")
	time.Sleep(5 * time.Millisecond)
	mustPost(t, s, alpha, "third")
	ctx := context.Background()

	views, _ := s.List(ctx, Query{After: &first.CreatedAt, Order: "asc"}, nil)
	if len(views) != 2 || views[0].Content != "second" {
		t.Fatalf("after 應排除自己並回傳之後的: %v", views)
	}

	views, _ = s.List(ctx, Query{Before: &second.CreatedAt, Order: "asc"}, nil)
	if len(views) != 1 || views[0].Content != "first" {
		t.Fatalf("before 應只回傳之前的: %v", views)
	}

	views, _ = s.List(ctx, Query{After: &first.CreatedAt, Before: &second.CreatedAt}, nil)
	if len(views) != 0 {
		t.Fatalf("after 與 before 之間沒有留言時應為空: %v", views)
	}
}
