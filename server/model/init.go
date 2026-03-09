package model

import (
	"errors"
	"server/model/store"
	"server/utils"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

var DB *gorm.DB
var logger *utils.SysLogger

func InitDb() error {
	logger = utils.SysLog

	db, err := Factory()
	if err != nil {
		logger.Errorf("Failed to connect to database: %v", err)
		return err
	}
	if err := Migrate(db); err != nil {
		logger.Errorf("Failed to migrate database: %v", err)
		return err
	}
	DB = db

	if err := ensureRootUser(db); err != nil {
		logger.Errorf("Failed to bootstrap root user: %v", err)
		return err
	}

	logger.Info("Database migrated")
	return nil
}

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&store.User{},
		&store.Sandbox{},
		&store.Account{},
		&store.AccountToken{},
		&store.Order{},
		&store.Trade{},
		&store.Position{},
		&store.ReplayDataset{},
		&store.ReplayKline{},
		&store.DatasetImportJob{},
		&store.EventLog{},
	)
}

func Factory() (*gorm.DB, error) {
	dsn := utils.PostgreDSN
	if dsn == "" {
		return initSqliteDB()
	}
	return initPostgreSQLDB(dsn, true)
}

func initSqliteDB() (*gorm.DB, error) {
	return gorm.Open(sqlite.Open(utils.SQLitePath), &gorm.Config{PrepareStmt: true})
}

func initPostgreSQLDB(dsn string, isLog bool) (*gorm.DB, error) {
	cfg := &gorm.Config{}
	if isLog {
		cfg.Logger = gormLogger.Default.LogMode(gormLogger.Info)
	}

	db, err := gorm.Open(postgres.Open(dsn), cfg)
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err == nil {
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(100)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}
	return db, nil
}

func ensureRootUser(db *gorm.DB) error {
	if utils.RootUser == "" || utils.RootUserEmail == "" || utils.RootPassword == "" {
		return nil
	}

	var count int64
	if err := db.Model(&store.User{}).Where("role = ?", store.RoleRootUser).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	salt := utils.GetRandomString(16)
	hashPassword, err := utils.P2H(utils.RootPassword + salt)
	if err != nil {
		return err
	}

	rootUser := store.User{
		Username:    utils.RootUser,
		DisplayName: "Root User",
		Role:        store.RoleRootUser,
		Email:       utils.RootUserEmail,
		Password:    hashPassword,
		Salt:        salt,
	}
	if err := db.Create(&rootUser).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil
		}
		return err
	}
	return nil
}
