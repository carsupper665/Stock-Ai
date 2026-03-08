package test

import (
	"path/filepath"
	"testing"

	"server/model"
	"server/model/store"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMigrateCreatesAllTablesOnFreshSQLite(t *testing.T) {
	db := newMigrationDB(t)

	if err := model.Migrate(db); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	for _, table := range managedTables() {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table for %T to exist", table)
		}
	}
}

func TestMigratePreservesExistingTablesOnUpgrade(t *testing.T) {
	db := newMigrationDB(t)

	if err := db.AutoMigrate(&store.Account{}, &store.User{}, &store.LLMUser{}, &store.RunSession{}); err != nil {
		t.Fatalf("initial AutoMigrate failed: %v", err)
	}

	seedLegacyRows(t, db)

	if err := model.Migrate(db); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	assertCount(t, db, &store.User{}, 1)
	assertCount(t, db, &store.Account{}, 1)
	assertCount(t, db, &store.LLMUser{}, 1)
	assertCount(t, db, &store.RunSession{}, 1)

	for _, table := range managedTables() {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table for %T to exist after upgrade", table)
		}
	}
}

func newMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "migration.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB failed: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	return db
}

func managedTables() []any {
	return []any{
		&store.Account{},
		&store.User{},
		&store.LLMUser{},
		&store.RunSession{},
		&store.MarketScenario{},
		&store.SymbolConfig{},
		&store.Bar{},
		&store.Wallet{},
		&store.LedgerEntry{},
		&store.Order{},
		&store.Fill{},
	}
}

func seedLegacyRows(t *testing.T, db *gorm.DB) {
	t.Helper()

	accessToken := "token-123"
	if err := db.Create(&store.User{
		Username:    "legacy",
		DisplayName: "Legacy User",
		Role:        1,
		Email:       "legacy@example.com",
		Password:    "hashed",
		Salt:        "salt",
	}).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	if err := db.Create(&store.Account{
		AccountID: 1,
		OwnerID:   1,
		Balance:   1000,
		Available: 1000,
		Frozen:    0,
	}).Error; err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	if err := db.Create(&store.LLMUser{
		Name:        "legacy-llm",
		AccessToken: &accessToken,
	}).Error; err != nil {
		t.Fatalf("create llm user failed: %v", err)
	}

	if err := db.Create(&store.RunSession{
		RunID:                 "run-legacy",
		ScenarioID:            "scenario-legacy",
		DatasetHash:           "dataset-hash",
		Stage:                 "execution",
		Cursor:                1,
		TrainStart:            0,
		TrainEnd:              0,
		ValidationStart:       1,
		ValidationEnd:         1,
		OOSStart:              2,
		OOSEnd:                2,
		ExecutionModelVersion: "exec-v1",
		FeeModelVersion:       "fee-v1",
		SlippageModelVersion:  "slip-v1",
	}).Error; err != nil {
		t.Fatalf("create run session failed: %v", err)
	}
}

func assertCount(t *testing.T, db *gorm.DB, model any, want int64) {
	t.Helper()

	var got int64
	if err := db.Model(model).Count(&got).Error; err != nil {
		t.Fatalf("count failed for %T: %v", model, err)
	}
	if got != want {
		t.Fatalf("expected %d rows for %T, got %d", want, model, got)
	}
}
