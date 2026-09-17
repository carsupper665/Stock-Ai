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

func mustPost(t *testing.T, s *Service, author auth.Identity, content string, tags ...string) *database.Message {
	t.Helper()
	m, err := s.Post(context.Background(), author, content, tags)
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

	if _, err := s.Post(ctx, alpha, "   ", nil); !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("空白內容應被拒絕，得到 %v", err)
	}
	if _, err := s.Post(ctx, alpha, strings.Repeat("字", MaxContentLen+1), nil); !errors.Is(err, ErrContentLong) {
		t.Fatalf("過長內容應被拒絕，得到 %v", err)
	}
	if _, err := s.Post(ctx, alpha, strings.Repeat("字", MaxContentLen), nil); err != nil {
		t.Fatalf("剛好上限應可發布: %v", err)
	}
	if _, err := s.Post(ctx, alpha, "tagged", []string{"beta", " beta "}); !errors.Is(err, ErrInvalidTags) {
		t.Fatalf("trim 後重複的 tags 應被拒絕，得到 %v", err)
	}
	if posted, err := s.Post(ctx, alpha, strings.Repeat("😀", MaxContentLen), []string{" ETH-Agent-02 "}); err != nil || fmt.Sprint(posted.Tags) != "[ETH-Agent-02]" {
		t.Fatalf("上限按 Unicode 字元且 tags 應 trim: %+v %v", posted, err)
	}
}

func TestYouDependsOnRequester(t *testing.T) {
	s, _ := newTestService(t)
	mustPost(t, s, user, "from user")
	mustPost(t, s, alpha, "from alpha")
	mustPost(t, s, beta, "from beta")
	q := Query{Sort: "asc"}

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
			views, _, err := s.List(context.Background(), q, tc.requester)
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

	views, _, err := s.List(context.Background(), Query{}, nil)
	if err != nil {
		t.Fatalf("查詢: %v", err)
	}
	if len(views) != 1 || views[0].UserName != deletedName {
		t.Fatalf("已刪除帳號的留言應顯示 %s: %v", deletedName, names(views))
	}
}

func TestFixedPaginationSortAndTagFilter(t *testing.T) {
	s, _ := newTestService(t)
	for i := 0; i < 25; i++ {
		tags := []string{}
		if i%2 == 0 {
			tags = []string{beta.UserName}
		}
		mustPost(t, s, alpha, fmt.Sprintf("msg %02d", i), tags...)
		time.Sleep(time.Millisecond)
	}
	ctx := context.Background()

	views, more, _ := s.List(ctx, Query{}, nil)
	if len(views) != PageSize || !more {
		t.Fatalf("預設應回 %d 則並有下一頁，得到 %d more=%v", PageSize, len(views), more)
	}
	if views[0].Content != "msg 24" {
		t.Fatalf("預設 desc 應最新在前，得到 %q", views[0].Content)
	}

	views, more, _ = s.List(ctx, Query{Page: 3}, nil)
	if len(views) != 5 || more || views[0].Content != "msg 04" {
		t.Fatalf("第三頁應有最後 5 則: %v more=%v", views, more)
	}

	views, _, _ = s.List(ctx, Query{Sort: "asc"}, nil)
	if len(views) != PageSize || views[0].Content != "msg 00" || views[9].Content != "msg 09" {
		t.Fatalf("asc 應由舊到新: %v", views)
	}

	views, more, _ = s.List(ctx, Query{Sort: "asc", Page: 2, Tag: beta.UserName}, nil)
	if len(views) != 3 || more || views[0].Content != "msg 20" || fmt.Sprint(views[0].Tags) != "[ETH-Agent-02]" {
		t.Fatalf("tag 應先過濾再分頁: %v more=%v", views, more)
	}

	if _, _, err := s.List(ctx, Query{Sort: "sideways"}, nil); !errors.Is(err, ErrInvalidSort) {
		t.Fatalf("未知的 sort 應被拒絕，得到 %v", err)
	}
}

func TestDeleteAllowsUserAndOwnerOnly(t *testing.T) {
	s, _ := newTestService(t)
	owned := mustPost(t, s, alpha, "owned", beta.UserName)
	if err := s.Delete(context.Background(), owned.ID, beta); !errors.Is(err, ErrForbidden) {
		t.Fatalf("其他帳號不應可刪除，得到 %v", err)
	}
	if err := s.Delete(context.Background(), owned.ID, alpha); err != nil {
		t.Fatalf("作者應可刪除: %v", err)
	}
	admin := mustPost(t, s, beta, "admin delete")
	if err := s.Delete(context.Background(), admin.ID, user); err != nil {
		t.Fatalf("USER 應可管理刪除: %v", err)
	}
}
