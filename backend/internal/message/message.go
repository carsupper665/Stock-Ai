package message

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/auth"
	"backend/internal/database"
	"backend/internal/id"
)

const (
	MaxLimit      = 30
	MaxContentLen = 2000

	You         = "you"
	deletedName = "[deleted]"
)

var (
	ErrEmptyContent = errors.New("content 不可為空")
	ErrContentLong  = errors.New("content 超過 2000 字元")
	ErrInvalidOrder = errors.New("order 只能是 asc 或 desc")
)

type Service struct {
	store    *database.Store
	userName string
}

func New(store *database.Store, userName string) *Service {
	return &Service{store: store, userName: userName}
}

// Post 由 token 身分決定作者，不接受 client 指定（規格 §14）。
func (s *Service) Post(ctx context.Context, author auth.Identity, content string) (*database.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, ErrEmptyContent
	}
	if utf8.RuneCountInString(content) > MaxContentLen {
		return nil, ErrContentLong
	}

	messageID, err := id.New("msg_")
	if err != nil {
		return nil, err
	}
	message := &database.Message{
		ID:         messageID,
		AuthorType: string(author.Kind),
		AuthorID:   author.AccountID,
		Content:    content,
	}
	if err := s.store.CreateMessage(ctx, message); err != nil {
		return nil, err
	}
	return message, nil
}

type Query struct {
	After  *time.Time
	Before *time.Time
	Order  string
	Limit  int
}

// View 是對外的留言：只有誰說的、說了什麼、什麼時候（規格 §18）。
type View struct {
	ID        string
	UserName  string
	Content   string
	CreatedAt time.Time
}

// List 查詢留言。requester 為 nil 表示匿名；有身分時，
// 自己的留言 user_name 顯示為 "you"（規格 §16）。
func (s *Service) List(ctx context.Context, q Query, requester *auth.Identity) ([]View, error) {
	desc, err := parseOrder(q.Order)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > MaxLimit {
		limit = MaxLimit
	}

	messages, err := s.store.ListMessages(ctx, database.MessageQuery{
		After: q.After, Before: q.Before, Desc: desc, Limit: limit,
	})
	if err != nil {
		return nil, err
	}

	names, err := s.store.AccountNames(ctx, accountIDs(messages))
	if err != nil {
		return nil, err
	}

	views := make([]View, 0, len(messages))
	for _, m := range messages {
		views = append(views, View{
			ID:        m.ID,
			UserName:  s.displayName(m, names, requester),
			Content:   m.Content,
			CreatedAt: m.CreatedAt.UTC(),
		})
	}
	return views, nil
}

func (s *Service) displayName(m database.Message, names map[string]string, requester *auth.Identity) string {
	if requester != nil && string(requester.Kind) == m.AuthorType && requester.AccountID == m.AuthorID {
		return You
	}
	if m.AuthorType == database.AuthorUser {
		return s.userName
	}
	if name, ok := names[m.AuthorID]; ok {
		return name
	}
	return deletedName
}

func accountIDs(messages []database.Message) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, m := range messages {
		if m.AuthorType == database.AuthorAccount && !seen[m.AuthorID] {
			seen[m.AuthorID] = true
			ids = append(ids, m.AuthorID)
		}
	}
	return ids
}

func parseOrder(order string) (desc bool, err error) {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "", "desc":
		return true, nil
	case "asc":
		return false, nil
	default:
		return false, ErrInvalidOrder
	}
}
