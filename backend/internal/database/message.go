package database

import (
	"context"
	"time"
)

type MessageQuery struct {
	Desc   bool
	Limit  int
	Offset int
	Tag    string
	// AfterCreatedAt／AfterID 非空時只取排在這則留言之後的留言。
	AfterCreatedAt *time.Time
	AfterID        string
	// BeforeCreatedAt／BeforeID 非空時只取排在這則留言之前的留言。
	BeforeCreatedAt *time.Time
	BeforeID        string
}

// AccountMessageWindow 回傳帳號在 since 之後的訊息數，以及由新到舊第 limit 則的時間：
// 要等這則離開窗口，數量才會低於 limit。數量不到 limit 時回 0。
func (s *Store) AccountMessageWindow(ctx context.Context, accountID string, since time.Time, limit int) (int64, time.Time, error) {
	var window struct {
		Count     int64
		CreatedAt time.Time
	}
	// 同一個 snapshot 讀取數量與時間，避免刪除與兩次查詢交錯造成誤判。
	err := s.db.WithContext(ctx).Model(&Message{}).
		Select("created_at, COUNT(*) OVER () AS count").
		Where("author_type = ? AND author_id = ? AND created_at > ?", AuthorAccount, accountID, since).
		Order("created_at desc").Offset(limit - 1).Limit(1).Scan(&window).Error
	return window.Count, window.CreatedAt, translate(err)
}

func (s *Store) CreateMessage(ctx context.Context, message *Message) error {
	return s.Tx(ctx, func(tx *Store) error {
		if err := translate(tx.db.Create(message).Error); err != nil {
			return err
		}
		if len(message.Tags) == 0 {
			return nil
		}
		tags := make([]MessageTag, 0, len(message.Tags))
		for position, tag := range message.Tags {
			tags = append(tags, MessageTag{MessageID: message.ID, Tag: tag, Position: position})
		}
		return translate(tx.db.Create(&tags).Error)
	})
}

func (s *Store) ListMessages(ctx context.Context, q MessageQuery) ([]Message, error) {
	query := s.db.WithContext(ctx).Model(&Message{})
	if q.Tag != "" {
		query = query.Joins("JOIN message_tags ON message_tags.message_id = messages.id AND message_tags.tag = ?", q.Tag)
	}
	if q.AfterCreatedAt != nil {
		query = query.Where("messages.created_at > ? OR (messages.created_at = ? AND messages.id > ?)", *q.AfterCreatedAt, *q.AfterCreatedAt, q.AfterID)
	}
	if q.BeforeCreatedAt != nil {
		query = query.Where("messages.created_at < ? OR (messages.created_at = ? AND messages.id < ?)", *q.BeforeCreatedAt, *q.BeforeCreatedAt, q.BeforeID)
	}
	order := "created_at asc, id asc"
	if q.Desc {
		order = "created_at desc, id desc"
	}

	var messages []Message
	if err := translate(query.Order(order).Offset(q.Offset).Limit(q.Limit).Find(&messages).Error); err != nil || len(messages) == 0 {
		return messages, err
	}
	ids := make([]string, len(messages))
	for index := range messages {
		ids[index] = messages[index].ID
	}
	var tags []MessageTag
	if err := translate(s.db.WithContext(ctx).Where("message_id IN ?", ids).Order("message_id asc, position asc").Find(&tags).Error); err != nil {
		return nil, err
	}
	byMessage := make(map[string][]string, len(messages))
	for _, tag := range tags {
		byMessage[tag.MessageID] = append(byMessage[tag.MessageID], tag.Tag)
	}
	for index := range messages {
		messages[index].Tags = byMessage[messages[index].ID]
		if messages[index].Tags == nil {
			messages[index].Tags = []string{}
		}
	}
	return messages, nil
}

func (s *Store) MessageByID(ctx context.Context, id string) (*Message, error) {
	var message Message
	if err := translate(s.db.WithContext(ctx).First(&message, "id = ?", id).Error); err != nil {
		return nil, err
	}
	return &message, nil
}

func (s *Store) DeleteMessage(ctx context.Context, id string) error {
	return s.Tx(ctx, func(tx *Store) error {
		if err := translate(tx.db.Where("message_id = ?", id).Delete(&MessageTag{}).Error); err != nil {
			return err
		}
		result := tx.db.Delete(&Message{}, "id = ?", id)
		if result.Error != nil {
			return translate(result.Error)
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) ClearMessages(ctx context.Context) error {
	return s.Tx(ctx, func(tx *Store) error {
		if err := translate(tx.db.Where("1 = 1").Delete(&MessageTag{}).Error); err != nil {
			return err
		}
		return translate(tx.db.Where("1 = 1").Delete(&Message{}).Error)
	})
}

// AccountNames 一次查出多個帳號的顯示名稱，避免每則留言各查一次。
func (s *Store) AccountNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}

	var accounts []Account
	err := s.db.WithContext(ctx).Select("id", "user_name").Where("id IN ?", ids).Find(&accounts).Error
	if err != nil {
		return nil, translate(err)
	}
	for _, a := range accounts {
		names[a.ID] = a.UserName
	}
	return names, nil
}

// AccountIDsByName 依名稱查帳號 id，用來確認留言的 tag 是存在的帳號。
func (s *Store) AccountIDsByName(ctx context.Context, names []string) (map[string]string, error) {
	ids := make(map[string]string, len(names))
	if len(names) == 0 {
		return ids, nil
	}

	var accounts []Account
	err := s.db.WithContext(ctx).Select("id", "user_name").Where("user_name IN ?", names).Find(&accounts).Error
	if err != nil {
		return nil, translate(err)
	}
	for _, a := range accounts {
		ids[a.UserName] = a.ID
	}
	return ids, nil
}
