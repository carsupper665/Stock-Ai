package test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestMessageBoardMigrationKeepsLegacyMessagesAndAddsTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-message.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if err := legacy.AutoMigrate(&database.Message{}); err != nil {
		t.Fatalf("create legacy messages: %v", err)
	}
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := legacy.Create(&database.Message{ID: "msg_legacy", AuthorType: database.AuthorUser, Content: "legacy", CreatedAt: created}).Error; err != nil {
		t.Fatalf("insert legacy message: %v", err)
	}
	sqlDB, _ := legacy.DB()
	_ = sqlDB.Close()

	store, err := database.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate message tags: %v", err)
	}
	ctx := context.Background()
	rows, err := store.ListMessages(ctx, database.MessageQuery{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != "msg_legacy" || len(rows[0].Tags) != 0 {
		t.Fatalf("legacy message after migration: %+v error=%v", rows, err)
	}
	if err := store.CreateMessage(ctx, &database.Message{ID: "msg_tagged", AuthorType: database.AuthorUser, Content: "tagged", Tags: []string{"Research Agent", "Risk Agent"}}); err != nil {
		t.Fatalf("create tagged message: %v", err)
	}
	rows, err = store.ListMessages(ctx, database.MessageQuery{Tag: "Research Agent", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != "msg_tagged" || len(rows[0].Tags) != 2 || rows[0].Tags[0] != "Research Agent" || rows[0].Tags[1] != "Risk Agent" {
		t.Fatalf("tag query after migration: %+v error=%v", rows, err)
	}
}
