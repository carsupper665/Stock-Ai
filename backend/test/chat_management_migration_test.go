package test

import (
	"backend/internal/database"
	"context"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"testing"
)

func TestChatManagementLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	// Actual pre-feature schema: the new columns must not exist before Migrate.
	if err := legacy.Exec(`CREATE TABLE accounts (id text PRIMARY KEY, user_name text NOT NULL UNIQUE, token text NOT NULL UNIQUE, initial_balance real NOT NULL, balance real NOT NULL, status text NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`INSERT INTO accounts (id,user_name,token,initial_balance,balance,status) VALUES ('old','legacy','old-token',100,100,'active')`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := legacy.DB()
	sqlDB.Close()
	store, err := database.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	acc, err := store.AccountByID(context.Background(), "old")
	if err != nil || acc.ChatRateMinutes != 1 || acc.ChatRateCount != 3 || acc.ChatMutedUntil != nil || acc.ChatMuteReason != "" {
		t.Fatalf("migrated: %+v %v", acc, err)
	}
	if err := store.CreateAccount(context.Background(), &database.Account{ID: "new", UserName: "new", Token: "new-token", InitialBalance: 100, Balance: 100, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	acc, err = store.AccountByID(context.Background(), "new")
	if err != nil || acc.ChatRateMinutes != 1 || acc.ChatRateCount != 3 {
		t.Fatalf("new: %+v %v", acc, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	check, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := check.DB()
	defer db.Close()
	if !check.Migrator().HasIndex(&database.Message{}, "idx_message_author_time") {
		t.Fatal("missing message author/time index")
	}
	var columns []struct{ Name string }
	if err := check.Raw(`PRAGMA index_info('idx_message_author_time')`).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 || columns[0].Name != "author_type" || columns[1].Name != "author_id" || columns[2].Name != "created_at" {
		t.Fatalf("index: %+v", columns)
	}
}
