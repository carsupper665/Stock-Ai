package database

import (
	"context"
	"time"
)

type MessageQuery struct {
	After  *time.Time
	Before *time.Time
	Desc   bool
	Limit  int
}

func (s *Store) CreateMessage(ctx context.Context, message *Message) error {
	return translate(s.db.WithContext(ctx).Create(message).Error)
}

func (s *Store) ListMessages(ctx context.Context, q MessageQuery) ([]Message, error) {
	query := s.db.WithContext(ctx)
	if q.After != nil {
		query = query.Where("created_at > ?", q.After.UTC())
	}
	if q.Before != nil {
		query = query.Where("created_at < ?", q.Before.UTC())
	}
	order := "created_at asc, id asc"
	if q.Desc {
		order = "created_at desc, id desc"
	}

	var messages []Message
	err := query.Order(order).Limit(q.Limit).Find(&messages).Error
	return messages, translate(err)
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
