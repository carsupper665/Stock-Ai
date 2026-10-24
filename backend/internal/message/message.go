package message

import (
	"context"
	"errors"
	"fmt"
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
	MaxLimit      = 50
	MaxContentLen = 500

	You         = "you"
	deletedName = "[deleted]"
	// UserTag 是寄給 USER 的收件人標籤；寫 USER_NAME 也會存成 UserTag。
	UserTag = "USER"
)

var (
	ErrEmptyContent = errors.New("content 不可為空")
	ErrContentLong  = errors.New("content 超過 500 個 Unicode 字元")
	ErrInvalidSort  = errors.New("sort 只能是 asc 或 desc")
	ErrInvalidPage  = errors.New("page 必須是正整數")
	ErrInvalidTag   = errors.New("tag 不可為空")
	ErrInvalidTags  = errors.New("tags 必須是唯一的非空字串")
	ErrUnknownTag   = errors.New("tags 只能是 USER 或其他帳號的名稱")
	ErrInvalidAfter = errors.New("after 必須是存在的留言 id")
	ErrSelfTag      = errors.New("tags 不能是自己")
	ErrForbidden    = errors.New("只能刪除自己的留言")

	ErrInvalidLimit = errors.New("limit 必須是 1～50 的整數")
	// ErrInvalidBefore 也涵蓋 before 與 after 同時使用。
	ErrInvalidBefore = errors.New("before 必須是存在的留言 id，且不能和 after 同時使用")

	ErrChatPostDisabled = errors.New("此帳號已被管理者停用群聊發言權限")
	ErrChatReadDisabled = errors.New("此帳號已被管理者停用群聊讀取權限")
	ErrChatMuted        = errors.New("你已被管理者禁言")
	ErrChatRateLimited  = errors.New("發言太頻繁")
)

type Service struct {
	store    *database.Store
	userName string
	streams  *streams
}

func New(store *database.Store, userName string) *Service {
	return &Service{store: store, userName: userName, streams: &streams{subscribers: map[chan Event]struct{}{}}}
}

// Post 由 token 身分決定作者，不接受 client 指定（規格 §14）。
func (s *Service) Post(ctx context.Context, author auth.Identity, content string, tags []string) (*database.Message, error) {
	var posted *database.Message
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if !author.IsUser() {
			if err := tx.LockAccount(ctx, author.AccountID); err != nil {
				return err
			}
		}
		service := &Service{store: tx, userName: s.userName}
		var err error
		posted, err = service.post(ctx, author, content, tags)
		return err
	})
	if err != nil {
		return nil, err
	}
	// 推播給 USER，所以用 USER 看到的名稱（規格 §16）。
	name := You
	if !author.IsUser() {
		name = author.UserName
	}
	s.streams.publish(Event{Name: EventCreated, Message: View{
		ID: posted.ID, UserName: name, Content: posted.Content, Tags: posted.Tags, CreatedAt: posted.CreatedAt.UTC(),
	}})
	return posted, nil
}

func (s *Service) post(ctx context.Context, author auth.Identity, content string, tags []string) (*database.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, ErrEmptyContent
	}
	if utf8.RuneCountInString(content) > MaxContentLen {
		return nil, ErrContentLong
	}
	account, err := s.checkPermission(ctx, author, func(a *database.Account) bool { return a.ChatPost }, ErrChatPostDisabled)
	if err != nil {
		return nil, err
	}
	if account != nil {
		now := time.Now().UTC()
		if account.ChatMutedUntil != nil && account.ChatMutedUntil.After(now) {
			return nil, fmt.Errorf("%w，原因：%s；剩餘 %d 分鐘（到 %s）", ErrChatMuted, account.ChatMuteReason,
				int(math.Ceil(account.ChatMutedUntil.Sub(now).Minutes())), account.ChatMutedUntil.UTC().Format(time.RFC3339))
		}
		window := time.Duration(account.ChatRateMinutes) * time.Minute
		count, oldest, err := s.store.AccountMessageWindow(ctx, account.ID, now.Add(-window))
		if err != nil {
			return nil, err
		}
		if count >= int64(account.ChatRateCount) {
			wait := max(1, int(math.Ceil(oldest.Add(window).Sub(now).Minutes())))
			return nil, fmt.Errorf("%w：每 %d 分鐘最多 %d 則，約 %d 分鐘後可再發言", ErrChatRateLimited, account.ChatRateMinutes, account.ChatRateCount, wait)
		}
	}
	tags, err = s.normalizeTags(tags)
	if err != nil {
		return nil, err
	}
	if err := s.checkRecipients(ctx, author, tags); err != nil {
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
	// Limit 是每頁筆數，0 表示 PageSize。
	Limit int
	Sort  string
	Tag   string
	// After 只回傳這則留言之後（依時間與 id）的留言，用來讀「上次讀到之後」的新訊息。
	After string
	// Before 只回傳這則留言之前的留言，用來往上載入更早的訊息。
	Before string
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
	limit := q.Limit
	if limit == 0 {
		limit = PageSize
	}
	if limit < 1 || limit > MaxLimit {
		return nil, false, ErrInvalidLimit
	}
	page := q.Page
	if page == 0 {
		page = 1
	}
	if page < 1 || page-1 > math.MaxInt/limit {
		return nil, false, ErrInvalidPage
	}
	tag := strings.TrimSpace(q.Tag)
	if q.Tag != "" && tag == "" {
		return nil, false, ErrInvalidTag
	}
	if requester != nil {
		if _, err := s.checkPermission(ctx, *requester, func(a *database.Account) bool { return a.ChatRead }, ErrChatReadDisabled); err != nil {
			return nil, false, err
		}
	}
	after, before := strings.TrimSpace(q.After), strings.TrimSpace(q.Before)
	if after != "" && before != "" {
		return nil, false, ErrInvalidBefore
	}
	query := database.MessageQuery{Desc: desc, Limit: limit + 1, Offset: (page - 1) * limit, Tag: tag}
	if before != "" {
		anchor, err := s.store.MessageByID(ctx, before)
		if errors.Is(err, database.ErrNotFound) {
			return nil, false, ErrInvalidBefore
		}
		if err != nil {
			return nil, false, err
		}
		query.BeforeID, query.BeforeCreatedAt = anchor.ID, &anchor.CreatedAt
	}
	if after != "" {
		anchor, err := s.store.MessageByID(ctx, after)
		if errors.Is(err, database.ErrNotFound) {
			return nil, false, ErrInvalidAfter
		}
		if err != nil {
			return nil, false, err
		}
		query.AfterID, query.AfterCreatedAt = anchor.ID, &anchor.CreatedAt
	}

	messages, err := s.store.ListMessages(ctx, query)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
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
	if err := s.store.DeleteMessage(ctx, id); err != nil {
		return err
	}
	s.streams.publish(Event{Name: EventDeleted, ID: id})
	return nil
}

// Clear 刪除全部留言與 tag，只給 USER（由路由把關）。
func (s *Service) Clear(ctx context.Context) error {
	if err := s.store.ClearMessages(ctx); err != nil {
		return err
	}
	s.streams.publish(Event{Name: EventCleared})
	return nil
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

func (s *Service) normalizeTags(tags []string) ([]string, error) {
	normalized := make([]string, len(tags))
	seen := make(map[string]bool, len(tags))
	for index, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == s.userName {
			tag = UserTag
		}
		if tag == "" || seen[tag] {
			return nil, ErrInvalidTags
		}
		seen[tag] = true
		normalized[index] = tag
	}
	return normalized, nil
}

// checkPermission 擋下被 USER 停用群聊權限的帳號；USER 本身不受限。
func (s *Service) checkPermission(ctx context.Context, who auth.Identity, allowed func(*database.Account) bool, denied error) (*database.Account, error) {
	if who.IsUser() {
		return nil, nil
	}
	account, err := s.store.AccountByID(ctx, who.AccountID)
	if err != nil {
		return nil, err
	}
	if !allowed(account) {
		return nil, denied
	}
	return account, nil
}

// checkRecipients 檢查 tag（選填）：只能是 USER 或另一個帳號的名稱，不能 tag 自己。
func (s *Service) checkRecipients(ctx context.Context, author auth.Identity, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != UserTag {
			names = append(names, tag)
		}
	}
	ids, err := s.store.AccountIDsByName(ctx, names)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if tag == UserTag {
			if author.IsUser() {
				return ErrSelfTag
			}
			continue
		}
		id, ok := ids[tag]
		if !ok {
			return fmt.Errorf("%w：找不到 %q", ErrUnknownTag, tag)
		}
		if author.Owns(id) {
			return ErrSelfTag
		}
	}
	return nil
}
