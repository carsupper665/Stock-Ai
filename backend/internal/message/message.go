package message

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/auth"
	"backend/internal/database"
	"backend/internal/id"
)

const (
	PageSize      = 10
	MaxContentLen = 500

	You         = "you"
	deletedName = "[deleted]"
)

var (
	ErrEmptyContent = errors.New("content 不可為空")
	ErrContentLong  = errors.New("content 超過 500 個 Unicode 字元")
	ErrInvalidSort  = errors.New("sort 只能是 asc 或 desc")
	ErrInvalidPage  = errors.New("page 必須是正整數")
	ErrInvalidTag   = errors.New("tag 不可為空")
	ErrInvalidTags  = errors.New("tags 必須是唯一的非空字串")
	ErrForbidden    = errors.New("只能刪除自己的留言")
)

type Service struct {
	store    *database.Store
	userName string
}

func New(store *database.Store, userName string) *Service {
	return &Service{store: store, userName: userName}
}

// Post 由 token 身分決定作者，不接受 client 指定（規格 §14）。
func (s *Service) Post(ctx context.Context, author auth.Identity, content string, tags []string) (*database.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, ErrEmptyContent
	}
	if utf8.RuneCountInString(content) > MaxContentLen {
		return nil, ErrContentLong
	}
	tags, err := normalizeTags(tags)
	if err != nil {
		return nil, err
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
		Tags:       tags,
	}
	if err := s.store.CreateMessage(ctx, message); err != nil {
		return nil, err
	}
	return message, nil
}

type Query struct {
	Page int
	Sort string
	Tag  string
}

// View 是對外的留言：只有誰說的、說了什麼、什麼時候（規格 §18）。
type View struct {
	ID        string
	UserName  string
	Content   string
	Tags      []string
	CreatedAt time.Time
}

// List 查詢留言。requester 為 nil 表示匿名；有身分時，
// 自己的留言 user_name 顯示為 "you"（規格 §16）。
func (s *Service) List(ctx context.Context, q Query, requester *auth.Identity) ([]View, bool, error) {
	desc, err := parseSort(q.Sort)
	if err != nil {
		return nil, false, err
	}
	page := q.Page
	if page == 0 {
		page = 1
	}
	if page < 1 || page-1 > math.MaxInt/PageSize {
		return nil, false, ErrInvalidPage
	}
	tag := strings.TrimSpace(q.Tag)
	if q.Tag != "" && tag == "" {
		return nil, false, ErrInvalidTag
	}

	messages, err := s.store.ListMessages(ctx, database.MessageQuery{
		Desc: desc, Limit: PageSize + 1, Offset: (page - 1) * PageSize, Tag: tag,
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(messages) > PageSize
	if hasMore {
		messages = messages[:PageSize]
	}

	names, err := s.store.AccountNames(ctx, accountIDs(messages))
	if err != nil {
		return nil, false, err
	}

	views := make([]View, 0, len(messages))
	for _, m := range messages {
		views = append(views, View{
			ID:        m.ID,
			UserName:  s.displayName(m, names, requester),
			Content:   m.Content,
			Tags:      m.Tags,
			CreatedAt: m.CreatedAt.UTC(),
		})
	}
	return views, hasMore, nil
}

func (s *Service) Delete(ctx context.Context, id string, requester auth.Identity) error {
	message, err := s.store.MessageByID(ctx, id)
	if err != nil {
		return err
	}
	if !requester.IsUser() && !(message.AuthorType == database.AuthorAccount && requester.Owns(message.AuthorID)) {
		return ErrForbidden
	}
	return s.store.DeleteMessage(ctx, id)
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

func parseSort(sort string) (desc bool, err error) {
	switch strings.TrimSpace(sort) {
	case "", "desc":
		return true, nil
	case "asc":
		return false, nil
	default:
		return false, ErrInvalidSort
	}
}

func normalizeTags(tags []string) ([]string, error) {
	normalized := make([]string, len(tags))
	seen := make(map[string]bool, len(tags))
	for index, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			return nil, ErrInvalidTags
		}
		seen[tag] = true
		normalized[index] = tag
	}
	return normalized, nil
}
